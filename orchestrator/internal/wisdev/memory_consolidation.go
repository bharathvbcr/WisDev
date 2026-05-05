package wisdev

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/pycompute"
)

// MemoryConsolidator handles cross-session learning and semantic memory construction.
type MemoryConsolidator struct {
	db      DBProvider
	kg      *KnowledgeGraphService
	store   MemoryStore
	compute *pycompute.Client
}

func NewMemoryConsolidator(db DBProvider, stores ...MemoryStore) *MemoryConsolidator {
	store := MemoryStore(&NoopMemoryStore{})
	if len(stores) > 0 && stores[0] != nil {
		store = stores[0]
	}
	return &MemoryConsolidator{
		db:      db,
		kg:      NewKnowledgeGraphService(db),
		store:   store,
		compute: pycompute.NewClient(),
	}
}

// ConsolidateQuest extracts semantic insights from a completed legacy quest.
func (c *MemoryConsolidator) ConsolidateQuest(ctx context.Context, quest *QuestState) error {
	if quest.Status != "complete" {
		return fmt.Errorf("cannot consolidate incomplete quest")
	}

	for _, hyp := range quest.Hypotheses {
		if hyp.ConfidenceScore > 0.8 {
			err := c.kg.SaveFinding(ctx, quest.ID, hyp)
			if err != nil {
				return err
			}
		}
	}

	for _, hyp := range quest.Hypotheses {
		if hyp.ConfidenceScore < 0.2 && hyp.EvidenceCount > 5 {
			err := c.kg.RecordDeadEnd(ctx, quest.UserID, quest.Query, hyp)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// ConsolidateResearchQuest persists replay-worthy findings and dead ends from the
// new typed ResearchQuest runtime.
func (c *MemoryConsolidator) ConsolidateResearchQuest(ctx context.Context, quest *ResearchQuest) error {
	if c == nil || quest == nil {
		return nil
	}

	if quest.CitationVerdict.Promoted {
		for _, finding := range quest.AcceptedClaims {
			hyp := &Hypothesis{
				ID:              stableWisDevID("kg", quest.QuestID, finding.SourceID, finding.Claim),
				Query:           quest.Query,
				Claim:           finding.Claim,
				Text:            finding.Claim,
				ConfidenceScore: ClampFloat(finding.Confidence, 0.0, 1.0),
				EvidenceCount:   1,
				Evidence: []*EvidenceFinding{
					{
						ID:         finding.ID,
						Claim:      finding.Claim,
						Snippet:    finding.Snippet,
						PaperTitle: finding.PaperTitle,
						Keywords:   append([]string(nil), finding.Keywords...),
						SourceID:   finding.SourceID,
						Year:       finding.Year,
						Confidence: finding.Confidence,
						Status:     finding.Status,
					},
				},
			}
			if err := c.kg.SaveFinding(ctx, quest.QuestID, hyp); err != nil {
				return err
			}
		}
	}

	for _, entry := range quest.RejectedBranches {
		claim := strings.TrimSpace(entry.Content)
		if claim == "" {
			continue
		}
		hyp := &Hypothesis{
			ID:              entry.ID,
			Query:           quest.Query,
			Claim:           claim,
			Text:            claim,
			ConfidenceScore: 0.1,
			EvidenceCount:   MaxInt(1, quest.RetrievedCount),
		}
		if err := c.kg.RecordDeadEnd(ctx, quest.UserID, quest.Query, hyp); err != nil {
			return err
		}
	}

	return nil
}

// GetRelevantPastFindings retrieves replay-worthy findings from both the Redis
// long-term store and the knowledge graph.
func (c *MemoryConsolidator) GetRelevantPastFindings(ctx context.Context, userID string, query string) ([]string, error) {
	entries, err := c.GetRelevantFindingEntries(ctx, userID, query)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if content := strings.TrimSpace(entry.Content); content != "" {
			out = append(out, content)
		}
	}
	return out, nil
}

func (c *MemoryConsolidator) GetRelevantFindingEntries(ctx context.Context, userID string, query string) ([]MemoryEntry, error) {
	if c == nil {
		return nil, nil
	}
	query = strings.TrimSpace(query)
	if userID == "" || query == "" {
		return nil, nil
	}

	relevant := make([]MemoryEntry, 0, 8)
	if c.store != nil {
		if entries, err := c.store.LoadLongTermVector(ctx, userID); err == nil {
			relevant = append(relevant, rankRelevantMemoryEntries(entries, query, 5)...)
		}
	}

	var embedding []float64
	if c.compute != nil {
		if vectors, err := c.compute.EmbedTextBatch(ctx, []string{query}); err == nil && len(vectors) > 0 {
			embedding = vectors[0]
		}
	}
	if c.kg != nil {
		findings, err := c.kg.GetRelevantPastFindings(ctx, userID, query, embedding)
		if err != nil {
			return dedupeMemoryEntries(relevant), err
		}
		for _, finding := range findings {
			content := strings.TrimSpace(finding)
			if content == "" {
				continue
			}
			relevant = append(relevant, MemoryEntry{
				ID:        stableWisDevID("replay", userID, query, content),
				Type:      "past_finding",
				Content:   content,
				CreatedAt: NowMillis(),
			})
		}
	}

	return dedupeMemoryEntries(relevant), nil
}

func rankRelevantMemoryEntries(entries []MemoryEntry, query string, limit int) []MemoryEntry {
	if len(entries) == 0 || strings.TrimSpace(query) == "" {
		return nil
	}
	type rankedEntry struct {
		entry MemoryEntry
		score int
	}
	terms := strings.Fields(strings.ToLower(query))
	ranked := make([]rankedEntry, 0, len(entries))
	for _, entry := range entries {
		content := strings.ToLower(strings.TrimSpace(entry.Content))
		if content == "" {
			continue
		}
		score := 0
		for _, term := range terms {
			if len(term) < 3 {
				continue
			}
			if strings.Contains(content, term) {
				score++
			}
		}
		if score == 0 && len(terms) > 0 {
			continue
		}
		ranked = append(ranked, rankedEntry{entry: entry, score: score})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score == ranked[j].score {
			return ranked[i].entry.CreatedAt > ranked[j].entry.CreatedAt
		}
		return ranked[i].score > ranked[j].score
	})
	if len(ranked) == 0 {
		latest := append([]MemoryEntry(nil), entries...)
		sort.SliceStable(latest, func(i, j int) bool { return latest[i].CreatedAt > latest[j].CreatedAt })
		if limit > 0 && len(latest) > limit {
			latest = latest[:limit]
		}
		return latest
	}
	out := make([]MemoryEntry, 0, MinInt(limit, len(ranked)))
	for _, item := range ranked {
		out = append(out, item.entry)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func dedupeMemoryEntries(entries []MemoryEntry) []MemoryEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]MemoryEntry, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		key := strings.TrimSpace(entry.ID)
		if key == "" {
			key = stableWisDevID("memory", entry.Type, entry.Content)
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, entry)
	}
	return out
}
