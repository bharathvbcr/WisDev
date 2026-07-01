package wisdev

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestManuscriptPipelineForwardsControlsToSidecar is the integration test for the
// granular DocGen controls: it points the pipeline at a stub sidecar that captures
// every request, then asserts the controls actually reach the section-generate and
// review payloads, and that SectionFlow reshapes the section plan.
func TestManuscriptPipelineForwardsControlsToSidecar(t *testing.T) {
	var mu sync.Mutex
	generatePayloads := []map[string]any{}
	var reviewPayload map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		mu.Lock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/section/generate"), strings.HasSuffix(r.URL.Path, "/section/refine"):
			generatePayloads = append(generatePayloads, payload)
			mu.Unlock()
			sid, _ := payload["section_id"].(string)
			_ = json.NewEncoder(w).Encode(map[string]any{"content": "Drafted " + sid + " grounded in the literature [1]."})
			return
		case strings.HasSuffix(r.URL.Path, "/review"):
			reviewPayload = payload
			mu.Unlock()
			// No redundancies -> the coordinated dedup pass is a no-op.
			_ = json.NewEncoder(w).Encode(map[string]any{"content_score": 0.9})
			return
		default:
			mu.Unlock()
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	pipeline := NewManuscriptPipeline(server.URL)
	pipeline.TargetWords = 2100 // 7 default sections -> 300/section
	pipeline.MinCitations = 15
	pipeline.Genre = "research paper"
	pipeline.SectionFlow = []string{"introduction", "results", "discussion"}

	result, err := pipeline.Run(context.Background(), "job-ctrl", "graphene battery anodes", []search.Paper{
		{ID: "p1", Title: "Graphene anode review", Abstract: "Graphene anodes improve capacity and cycle life.", DOI: "10.1/g", Source: "test", Year: 2024},
	})
	require.NoError(t, err)

	// SectionFlow reshaped the plan to exactly the requested sections, in order.
	assert.Equal(t, []string{"introduction", "results", "discussion"}, result.Blueprint.SectionOrder)

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, generatePayloads, "the pipeline must call the sidecar for section drafting")
	// Every section-generate payload carries the per-section word budget and citation floor.
	for _, p := range generatePayloads {
		if _, isGenerate := p["section_outline"]; !isGenerate {
			continue // refine payloads don't carry these
		}
		assert.EqualValues(t, 700, p["target_words"], "2100 words / 3 sections = 700 each")
		assert.EqualValues(t, 15, p["min_citations"])
	}
	// The reviewer received the genre so it grades a research paper (no first-person penalty).
	require.NotNil(t, reviewPayload, "the pipeline must call the sidecar reviewer")
	assert.Equal(t, "research paper", reviewPayload["genre"])
}
