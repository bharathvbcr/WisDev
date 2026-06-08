package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

func TestFullPaperRoutes_Status(t *testing.T) {
	is := assert.New(t)
	mux := http.NewServeMux()
	server := &wisdevServer{}

	// Setup gateway with memory state store
	store := wisdev.NewRuntimeStateStore(nil, nil)
	gw := &wisdev.AgentGateway{
		StateStore: store,
	}
	server.registerFullPaperRoutes(mux, gw)

	t.Run("job not found", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"jobId": "nonexistent"})
		req := httptest.NewRequest("POST", "/full-paper/status", bytes.NewReader(body))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		is.Equal(http.StatusNotFound, w.Code)
	})

	t.Run("invalid request body", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/full-paper/status", bytes.NewReader([]byte("not json")))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		is.Equal(http.StatusBadRequest, w.Code)
	})

	t.Run("method not allowed", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/full-paper/status", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		is.Equal(http.StatusMethodNotAllowed, w.Code)
	})
}

func TestFullPaperRoutes_Artifacts(t *testing.T) {
	is := assert.New(t)
	mux := http.NewServeMux()
	server := &wisdevServer{}
	store := wisdev.NewRuntimeStateStore(nil, nil)
	gw := &wisdev.AgentGateway{StateStore: store}
	server.registerFullPaperRoutes(mux, gw)

	t.Run("missing job id", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"jobId": ""})
		req := httptest.NewRequest("POST", "/full-paper/artifacts", bytes.NewReader(body))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		is.Equal(http.StatusBadRequest, w.Code)
	})
}

func TestFullPaperRouteHelpers(t *testing.T) {
	t.Run("decodeSearchPapers filters empty titles", func(t *testing.T) {
		papers := decodeSearchPapers([]any{
			map[string]any{"title": "Paper 1", "abstract": "A"},
			map[string]any{"title": "", "abstract": "B"},
		})
		if len(papers) != 1 {
			t.Fatalf("expected 1 paper, got %d", len(papers))
		}
		if papers[0].Title != "Paper 1" {
			t.Fatalf("unexpected title %q", papers[0].Title)
		}
	})

	t.Run("toAnyMap and toAnySliceMap", func(t *testing.T) {
		m := toAnyMap(struct {
			Name string `json:"name"`
		}{Name: "test"})
		if m["name"] != "test" {
			t.Fatalf("unexpected map value: %#v", m)
		}

		s := toAnySliceMap([]map[string]any{{"id": 1}})
		if len(s) != 1 || s[0]["id"] != float64(1) {
			t.Fatalf("unexpected slice map: %#v", s)
		}
	})

	t.Run("sourceIDs titles and status", func(t *testing.T) {
		packet := map[string]any{
			"evidenceSpans": []any{
				map[string]any{"sourceCanonicalId": "s1"},
				map[string]any{"sourceCanonicalId": "s1"},
				map[string]any{"sourceCanonicalId": "s2"},
			},
			"verifierStatus": "verified",
		}
		ids := sourceIDsFromPacket(packet)
		if len(ids) != 2 {
			t.Fatalf("unexpected ids: %#v", ids)
		}
		titles := titlesFromPacket(packet, map[string]string{"s1": "Title 1", "s2": "Title 2"})
		if len(titles) != 2 {
			t.Fatalf("unexpected titles: %#v", titles)
		}
		if packetStatus(packet) != "verified" {
			t.Fatalf("unexpected packet status")
		}
		if packetStatus(map[string]any{"contradictionPacketIds": []any{"x"}}) != "contradictory" {
			t.Fatalf("expected contradictory status")
		}
		if packetStatus(map[string]any{"verifierStatus": "rejected"}) != "unsupported" {
			t.Fatalf("expected unsupported status")
		}
		if packetStatus(map[string]any{}) != "tentative" {
			t.Fatalf("expected tentative status")
		}
	})

	t.Run("artifact helpers", func(t *testing.T) {
		artifacts := []map[string]any{
			{"artifactId": "a1", "type": "figure"},
			{"artifactId": "a2", "type": "table"},
			{"artifactId": "a2", "type": "table"},
		}
		if got := artifactIDs(artifacts); len(got) != 2 {
			t.Fatalf("unexpected artifact ids: %#v", got)
		}
		if got := firstArtifactIDByType(artifacts, "table"); got != "a2" {
			t.Fatalf("unexpected first artifact id: %q", got)
		}
		if got := firstArtifactIDByType(artifacts, "unknown"); got != "" {
			t.Fatalf("expected empty first artifact id, got %q", got)
		}
	})

	t.Run("firstSentence", func(t *testing.T) {
		if got := firstSentence("Hello world. Second sentence."); got != "Hello world." {
			t.Fatalf("unexpected first sentence: %q", got)
		}
		if got := firstSentence("No punctuation"); got != "No punctuation" {
			t.Fatalf("unexpected no punctuation result: %q", got)
		}
		if got := firstSentence("   "); got != "" {
			t.Fatalf("expected empty string, got %q", got)
		}
	})

	t.Run("extractFullPaperStartPapers precedence", func(t *testing.T) {
		papers := extractFullPaperStartPapers(
			map[string]any{"papers": []search.Paper{{Title: "Options"}}},
			map[string]any{"papers": []search.Paper{{Title: "Plan"}}},
			map[string]any{"papers": []search.Paper{{Title: "Metadata"}}},
		)
		if len(papers) != 1 || papers[0].Title != "Options" {
			t.Fatalf("unexpected precedence result: %#v", papers)
		}
	})
}
