package wisdev

// manuscript_checkpoint.go adds crash/cancellation resume to ManuscriptPipeline.Run:
// each completed section draft is checkpointed through the existing generic
// CheckpointStore contract (checkpoint.go — Redis / in-memory / fallback), plus the
// file-backed implementation below for single-host durability, so a later Run with
// the SAME jobID and the SAME pipeline config restores finished sections instead of
// re-drafting them. Restored drafts are pipeline-internal artifacts saved before any
// human could edit them (UserEdited is always false), so the Run invariant that
// content-mutating stages never overwrite a manual edit is unaffected.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// manuscriptCheckpointTTL bounds how long an interrupted run's section drafts stay
// resumable; after this they expire and the next run redrafts from scratch.
const manuscriptCheckpointTTL = 24 * time.Hour

// manuscriptCheckpointKey namespaces manuscript section checkpoints per job inside
// the shared CheckpointStore keyspace (which also holds agent-session snapshots).
func manuscriptCheckpointKey(jobID string) string {
	return "manuscript_sections:" + jobID
}

// manuscriptSectionCheckpoint is one completed section draft persisted mid-run.
type manuscriptSectionCheckpoint struct {
	SectionID   string               `json:"sectionId"`
	Fingerprint string               `json:"fingerprint"` // manuscriptContentFingerprint(Artifact.Content)
	Stage       string               `json:"stage"`
	Artifact    SectionDraftArtifact `json:"artifact"`
	SavedAt     int64                `json:"savedAt"`
}

// manuscriptCheckpointDoc is the whole-job checkpoint document: every section
// completed so far plus the config fingerprint that gates resumability.
type manuscriptCheckpointDoc struct {
	JobID             string                                 `json:"jobId"`
	ConfigFingerprint string                                 `json:"configFingerprint"`
	Sections          map[string]manuscriptSectionCheckpoint `json:"sections"`
	UpdatedAt         int64                                  `json:"updatedAt"`
}

// manuscriptContentFingerprint is a short deterministic content hash used both for
// checkpoint integrity (was the artifact stored intact?) and for log correlation.
func manuscriptContentFingerprint(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:8])
}

// checkpointConfigFingerprint hashes every pipeline knob that shapes drafted
// section content, so a resume only ever reuses drafts produced under the exact
// same configuration; any divergence deterministically forces a full redraft.
func (p *ManuscriptPipeline) checkpointConfigFingerprint(query string) string {
	return manuscriptContentFingerprint(strings.Join([]string{
		"mode=" + p.pipelineMode(),
		"query=" + strings.TrimSpace(query),
		fmt.Sprintf("target_words=%d", p.TargetWords),
		fmt.Sprintf("min_citations=%d", p.MinCitations),
		"genre=" + p.reviewGenre(),
		"section_flow=" + strings.Join(p.SectionFlow, ","),
		fmt.Sprintf("review_rounds=%d", p.reviewRounds()),
		"custom_instructions=" + p.trimmedCustomInstructions(),
	}, "\n"))
}

// loadManuscriptCheckpoint returns the resumable per-section drafts for jobID, or
// nil when checkpointing is disabled, nothing usable was saved, the config
// fingerprint diverged, or a stored artifact fails its integrity check.
func (p *ManuscriptPipeline) loadManuscriptCheckpoint(ctx context.Context, jobID, configFingerprint string) map[string]manuscriptSectionCheckpoint {
	if p.Checkpoints == nil || strings.TrimSpace(jobID) == "" {
		return nil
	}
	payload, err := p.Checkpoints.Load(ctx, manuscriptCheckpointKey(jobID))
	if err != nil {
		// Expected on a fresh job; debug keeps the no-resume path traceable.
		slog.Debug("manuscript checkpoint not loaded — starting fresh",
			"component", manuscriptLogComponent, "operation", "checkpoint.load",
			"job_id", jobID, "result", "none", "error", err.Error())
		return nil
	}
	var doc manuscriptCheckpointDoc
	if err := json.Unmarshal(payload, &doc); err != nil {
		slog.Warn("manuscript checkpoint unreadable — ignoring stored checkpoint",
			"component", manuscriptLogComponent, "operation", "checkpoint.load",
			"job_id", jobID, "result", "error", "error_code", "decode_error", "error", err.Error())
		return nil
	}
	if doc.ConfigFingerprint != configFingerprint {
		slog.Info("manuscript checkpoint config diverged — redrafting all sections",
			"component", manuscriptLogComponent, "operation", "checkpoint.load",
			"job_id", jobID, "result", "config_mismatch",
			"stored_config", doc.ConfigFingerprint, "current_config", configFingerprint)
		return nil
	}
	resumable := make(map[string]manuscriptSectionCheckpoint, len(doc.Sections))
	for sectionID, cp := range doc.Sections {
		if strings.TrimSpace(cp.Artifact.Content) == "" ||
			cp.Fingerprint != manuscriptContentFingerprint(cp.Artifact.Content) {
			slog.Warn("manuscript checkpoint section failed integrity check — will redraft",
				"component", manuscriptLogComponent, "operation", "checkpoint.load",
				"job_id", jobID, "section_id", sectionID, "result", "integrity_mismatch")
			continue
		}
		resumable[sectionID] = cp
	}
	if len(resumable) == 0 {
		return nil
	}
	slog.Info("manuscript checkpoint loaded",
		"component", manuscriptLogComponent, "operation", "checkpoint.load",
		"job_id", jobID, "result", "ok", "sections", len(resumable))
	return resumable
}

// manuscriptCheckpointer serializes the concurrent per-section saves that
// writeSections performs (sections draft in parallel goroutines) into a single
// read-modify-write checkpoint document. Nil-safe: a nil checkpointer no-ops.
type manuscriptCheckpointer struct {
	store CheckpointStore
	jobID string
	mu    sync.Mutex
	doc   manuscriptCheckpointDoc
}

// newManuscriptCheckpointer returns nil (checkpointing disabled) unless the
// pipeline has a store and a jobID. resumed entries seed the document so a
// partial resume does not drop previously saved sections on its next save.
func (p *ManuscriptPipeline) newManuscriptCheckpointer(jobID, configFingerprint string, resumed map[string]manuscriptSectionCheckpoint) *manuscriptCheckpointer {
	if p.Checkpoints == nil || strings.TrimSpace(jobID) == "" {
		return nil
	}
	sections := make(map[string]manuscriptSectionCheckpoint, len(resumed))
	for sectionID, cp := range resumed {
		sections[sectionID] = cp
	}
	return &manuscriptCheckpointer{
		store: p.Checkpoints,
		jobID: jobID,
		doc: manuscriptCheckpointDoc{
			JobID:             jobID,
			ConfigFingerprint: configFingerprint,
			Sections:          sections,
		},
	}
}

// saveSection records one completed section draft. Best-effort by design: a save
// failure is logged and never fails or slows the pipeline beyond the store call.
func (c *manuscriptCheckpointer) saveSection(ctx context.Context, stage string, artifact SectionDraftArtifact) {
	if c == nil || strings.TrimSpace(artifact.SectionID) == "" {
		return
	}
	fingerprint := manuscriptContentFingerprint(artifact.Content)
	now := time.Now().UnixMilli()
	c.mu.Lock()
	c.doc.Sections[artifact.SectionID] = manuscriptSectionCheckpoint{
		SectionID:   artifact.SectionID,
		Fingerprint: fingerprint,
		Stage:       stage,
		Artifact:    artifact,
		SavedAt:     now,
	}
	c.doc.UpdatedAt = now
	payload, err := json.Marshal(c.doc)
	c.mu.Unlock()
	if err != nil {
		slog.Warn("manuscript checkpoint marshal failed — section not persisted",
			"component", manuscriptLogComponent, "operation", "checkpoint.save",
			"job_id", c.jobID, "section_id", artifact.SectionID, "stage", stage,
			"result", "error", "error_code", "marshal_error", "error", err.Error())
		return
	}
	if err := c.store.Save(ctx, manuscriptCheckpointKey(c.jobID), payload, manuscriptCheckpointTTL); err != nil {
		slog.Warn("manuscript checkpoint save failed — continuing without persistence",
			"component", manuscriptLogComponent, "operation", "checkpoint.save",
			"job_id", c.jobID, "section_id", artifact.SectionID, "stage", stage,
			"result", "error", "error_code", "store_error", "error", err.Error())
		return
	}
	slog.Info("manuscript checkpoint saved",
		"component", manuscriptLogComponent, "operation", "checkpoint.save",
		"job_id", c.jobID, "section_id", artifact.SectionID, "stage", stage,
		"result", "ok", "content_fingerprint", fingerprint)
}

// FileCheckpointStore is an on-disk CheckpointStore: one JSON record file per key
// under dir (default os.TempDir()/scholarlm_manuscript_checkpoints). It gives the
// manuscript pipeline crash durability on a single host without requiring Redis;
// deployments with Redis can compose it via NewFallbackCheckpointStore.
type FileCheckpointStore struct {
	dir string
	mu  sync.Mutex
}

// NewFileCheckpointStore returns a file-backed store rooted at dir ("" = the
// default directory under os.TempDir()). The directory is created lazily on the
// first Save.
func NewFileCheckpointStore(dir string) *FileCheckpointStore {
	if strings.TrimSpace(dir) == "" {
		dir = filepath.Join(os.TempDir(), "scholarlm_manuscript_checkpoints")
	}
	return &FileCheckpointStore{dir: dir}
}

type fileCheckpointRecord struct {
	Key       string    `json:"key"`
	Payload   []byte    `json:"payload"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// path maps a checkpoint key to a stable, filesystem-safe file name.
func (s *FileCheckpointStore) path(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(s.dir, "ckpt_"+hex.EncodeToString(sum[:12])+".json")
}

func (s *FileCheckpointStore) Save(_ context.Context, sessionID string, payload []byte, ttl time.Duration) error {
	record, err := json.Marshal(fileCheckpointRecord{
		Key:       sessionID,
		Payload:   append([]byte(nil), payload...),
		ExpiresAt: time.Now().Add(ttl),
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	// Write-then-rename so a crash mid-write never leaves a truncated checkpoint.
	tmp := s.path(sessionID) + ".tmp"
	if err := os.WriteFile(tmp, record, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(sessionID))
}

func (s *FileCheckpointStore) Load(_ context.Context, sessionID string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(s.path(sessionID))
	if err != nil {
		return nil, errors.New("checkpoint_not_found")
	}
	var record fileCheckpointRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, errors.New("checkpoint_not_found")
	}
	if time.Now().After(record.ExpiresAt) {
		_ = os.Remove(s.path(sessionID))
		return nil, errors.New("checkpoint_expired")
	}
	return record.Payload, nil
}
