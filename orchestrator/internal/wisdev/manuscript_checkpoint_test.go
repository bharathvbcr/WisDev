package wisdev

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newCheckpointTestSidecar is a stub sidecar for checkpoint tests: it serves
// deterministic section prose and counts /section/generate calls per section so
// tests can assert exactly which sections were (re)drafted.
func newCheckpointTestSidecar(t *testing.T) (*httptest.Server, func() map[string]int) {
	t.Helper()
	var mu sync.Mutex
	generateCalls := map[string]int{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		sectionID, _ := payload["section_id"].(string)
		switch {
		case strings.HasSuffix(r.URL.Path, "/section/generate"):
			mu.Lock()
			generateCalls[sectionID]++
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"content": "Grounded synthesis for " + sectionID + " [1]."})
		case strings.HasSuffix(r.URL.Path, "/section/refine"), strings.HasSuffix(r.URL.Path, "/section/revise"):
			// Echo stable prose so the review loop converges immediately.
			_ = json.NewEncoder(w).Encode(map[string]any{"content": "Grounded synthesis for " + sectionID + " [1]."})
		case strings.HasSuffix(r.URL.Path, "/review"):
			_ = json.NewEncoder(w).Encode(map[string]any{"content_score": 0.9})
		default:
			w.WriteHeader(http.StatusNotFound) // no-op degradation paths (coordinate, fact-check)
		}
	}))
	t.Cleanup(server.Close)

	snapshot := func() map[string]int {
		mu.Lock()
		defer mu.Unlock()
		out := make(map[string]int, len(generateCalls))
		for k, v := range generateCalls {
			out[k] = v
		}
		return out
	}
	return server, snapshot
}

func newCheckpointTestPipeline(baseURL string, store CheckpointStore) *ManuscriptPipeline {
	pipeline := NewManuscriptPipeline(baseURL)
	pipeline.SectionFlow = []string{"introduction", "results"}
	pipeline.Checkpoints = store
	return pipeline
}

func checkpointTestPapers() []search.Paper {
	return []search.Paper{
		{ID: "p1", Title: "Graphene anode overview", Abstract: "Graphene anodes improve capacity and cycle life across chemistries.", DOI: "10.1/g", Source: "test", Year: 2024},
	}
}

func TestManuscriptRunResumesFromCheckpoint(t *testing.T) {
	server, generateCalls := newCheckpointTestSidecar(t)
	store := NewInMemoryCheckpointStore()
	const jobID = "job-ckpt-resume"
	const query = "graphene battery anodes"

	// Run 1 drafts every section and checkpoints each one.
	first, err := newCheckpointTestPipeline(server.URL, store).Run(context.Background(), jobID, query, checkpointTestPapers())
	require.NoError(t, err)
	require.Len(t, first.SectionDrafts, 2)
	afterFirst := generateCalls()
	assert.Equal(t, 1, afterFirst["introduction"])
	assert.Equal(t, 1, afterFirst["results"])

	// The stored checkpoint carries both completed drafts at the write stage.
	payload, err := store.Load(context.Background(), manuscriptCheckpointKey(jobID))
	require.NoError(t, err)
	var doc manuscriptCheckpointDoc
	require.NoError(t, json.Unmarshal(payload, &doc))
	require.Len(t, doc.Sections, 2)
	for sectionID, cp := range doc.Sections {
		assert.Equal(t, "write_sections", cp.Stage)
		assert.Equal(t, manuscriptContentFingerprint(cp.Artifact.Content), cp.Fingerprint)
		assert.Contains(t, cp.Artifact.Content, sectionID)
	}

	// Run 2 (fresh pipeline, same jobID + config, e.g. after a crash) must skip
	// drafting entirely: no new /section/generate calls.
	second, err := newCheckpointTestPipeline(server.URL, store).Run(context.Background(), jobID, query, checkpointTestPapers())
	require.NoError(t, err)
	require.Len(t, second.SectionDrafts, 2)
	afterSecond := generateCalls()
	assert.Equal(t, afterFirst, afterSecond, "a full checkpoint must skip all section drafting on resume")
	for _, section := range second.SectionDrafts {
		assert.NotEmpty(t, section.Content)
	}
}

func TestManuscriptRunPartialCheckpointRedraftsOnlyMissingSections(t *testing.T) {
	server, generateCalls := newCheckpointTestSidecar(t)
	store := NewInMemoryCheckpointStore()
	const jobID = "job-ckpt-partial"
	const query = "graphene battery anodes"

	_, err := newCheckpointTestPipeline(server.URL, store).Run(context.Background(), jobID, query, checkpointTestPapers())
	require.NoError(t, err)
	baseline := generateCalls()

	// Simulate a crash that happened after "introduction" completed but before
	// "results" did: drop the results entry from the stored checkpoint.
	payload, err := store.Load(context.Background(), manuscriptCheckpointKey(jobID))
	require.NoError(t, err)
	var doc manuscriptCheckpointDoc
	require.NoError(t, json.Unmarshal(payload, &doc))
	delete(doc.Sections, "results")
	truncated, err := json.Marshal(doc)
	require.NoError(t, err)
	require.NoError(t, store.Save(context.Background(), manuscriptCheckpointKey(jobID), truncated, time.Hour))

	_, err = newCheckpointTestPipeline(server.URL, store).Run(context.Background(), jobID, query, checkpointTestPapers())
	require.NoError(t, err)
	resumed := generateCalls()
	assert.Equal(t, baseline["introduction"], resumed["introduction"], "the checkpointed section must not be re-drafted")
	assert.Equal(t, baseline["results"]+1, resumed["results"], "only the missing section is re-drafted")
}

func TestManuscriptRunConfigChangeInvalidatesCheckpoint(t *testing.T) {
	server, generateCalls := newCheckpointTestSidecar(t)
	store := NewInMemoryCheckpointStore()
	const jobID = "job-ckpt-config"
	const query = "graphene battery anodes"

	_, err := newCheckpointTestPipeline(server.URL, store).Run(context.Background(), jobID, query, checkpointTestPapers())
	require.NoError(t, err)
	baseline := generateCalls()

	// Same jobID, different pipeline config: the checkpoint must NOT be reused.
	changed := newCheckpointTestPipeline(server.URL, store)
	changed.TargetWords = 1200
	_, err = changed.Run(context.Background(), jobID, query, checkpointTestPapers())
	require.NoError(t, err)
	after := generateCalls()
	assert.Equal(t, baseline["introduction"]+1, after["introduction"], "config drift must force a redraft")
	assert.Equal(t, baseline["results"]+1, after["results"], "config drift must force a redraft")
}

func TestFileCheckpointStoreSaveLoadAndExpiry(t *testing.T) {
	store := NewFileCheckpointStore(t.TempDir())
	ctx := context.Background()

	t.Run("missing checkpoint", func(t *testing.T) {
		_, err := store.Load(ctx, "absent")
		require.Error(t, err)
		assert.Equal(t, "checkpoint_not_found", err.Error())
	})

	t.Run("save then load roundtrip", func(t *testing.T) {
		require.NoError(t, store.Save(ctx, "job-1", []byte(`{"ok":true}`), time.Hour))
		loaded, err := store.Load(ctx, "job-1")
		require.NoError(t, err)
		assert.Equal(t, []byte(`{"ok":true}`), loaded)
	})

	t.Run("overwrite keeps latest payload", func(t *testing.T) {
		require.NoError(t, store.Save(ctx, "job-1", []byte(`{"v":2}`), time.Hour))
		loaded, err := store.Load(ctx, "job-1")
		require.NoError(t, err)
		assert.Equal(t, []byte(`{"v":2}`), loaded)
	})

	t.Run("expired checkpoint is rejected", func(t *testing.T) {
		require.NoError(t, store.Save(ctx, "job-ttl", []byte("x"), -time.Second))
		_, err := store.Load(ctx, "job-ttl")
		require.Error(t, err)
		assert.Equal(t, "checkpoint_expired", err.Error())
	})
}
