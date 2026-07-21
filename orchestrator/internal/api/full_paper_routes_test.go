package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/evidence"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/wisdev"
)

type fullPaperRecordingProvider struct {
	search.BaseProvider
	queries []string
	opts    []search.SearchOpts
}

func (p *fullPaperRecordingProvider) Name() string      { return "recording" }
func (p *fullPaperRecordingProvider) Domains() []string { return []string{"general"} }
func (p *fullPaperRecordingProvider) Healthy() bool     { return true }
func (p *fullPaperRecordingProvider) Search(ctx context.Context, query string, opts search.SearchOpts) ([]search.Paper, error) {
	p.queries = append(p.queries, query)
	p.opts = append(p.opts, opts)
	return []search.Paper{{ID: "recording-" + query, Title: "Paper " + query, Source: "recording"}}, nil
}

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

func TestFullPaperStartHydratesEvidenceFromPlanQueries(t *testing.T) {
	recorder := &fullPaperRecordingProvider{}
	reg := search.NewProviderRegistry()
	reg.Register(recorder)

	mux := http.NewServeMux()
	server := &wisdevServer{}
	gw := &wisdev.AgentGateway{
		StateStore:     wisdev.NewRuntimeStateStore(nil, nil),
		SearchRegistry: reg,
	}
	server.registerFullPaperRoutes(mux, gw)

	body, _ := json.Marshal(map[string]any{
		"userId": "u1",
		"query":  "main evidence query",
		"orchestrationPlan": map[string]any{
			"queries": []string{"related evidence query"},
		},
		"metadata": map[string]any{
			"detectedDomain": "general",
		},
	})
	req := httptest.NewRequest("POST", "/full-paper/start", bytes.NewReader(body))
	req = withTestUserID(req, "u1")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, []string{"main evidence query", "related evidence query"}, recorder.queries)
	if assert.Len(t, recorder.opts, 2) {
		assert.Equal(t, 5, recorder.opts[0].Limit)
		assert.True(t, recorder.opts[0].QualitySort)
		assert.Equal(t, "general", recorder.opts[0].Domain)
	}
	assert.Contains(t, w.Body.String(), "Paper main evidence query")
	assert.NotContains(t, w.Body.String(), "No source papers were supplied")
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
		if titles[0] != "Title 1" || titles[1] != "Title 2" {
			t.Fatalf("expected titles aligned with sourceIds, got %#v", titles)
		}

		duplicateTitlePacket := map[string]any{
			"evidenceSpans": []any{
				map[string]any{"sourceCanonicalId": "s1"},
				map[string]any{"sourceCanonicalId": "s2"},
			},
		}
		dupTitles := titlesFromPacket(duplicateTitlePacket, map[string]string{"s1": "Same Title", "s2": "Same Title"})
		require.Len(t, dupTitles, 2)
		assert.Equal(t, []string{"Same Title", "Same Title"}, dupTitles)

		snippetPacket := map[string]any{
			"evidenceSpans": []any{
				map[string]any{"snippet": " First snippet. "},
				map[string]any{"snippet": ""},
				map[string]any{"snippet": "Second snippet."},
			},
		}
		assert.Equal(t, []string{"First snippet.", "Second snippet."}, evidenceSnippetsFromPacket(snippetPacket))

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

func TestBuildWorkspaceEvidenceDossier(t *testing.T) {
	result := wisdev.ManuscriptPipelineResult{
		Dossier: evidence.Dossier{
			DossierID:       "dossier-1",
			Gaps:            []string{"gap-a"},
			CoverageMetrics: map[string]any{"verified": 2},
		},
		RawMaterials: evidence.ManuscriptRawMaterialSet{
			RawMaterialSetID: "bundle-1",
			CanonicalSources: []evidence.CanonicalCitationRecord{
				{CanonicalID: "doi:10.1/a", Title: "Paper A"},
				{CanonicalID: "doi:10.1/b", Title: "Paper B"},
				{CanonicalID: "doi:10.1/c", Title: "Paper C"},
			},
			ClaimPackets: []evidence.EvidencePacket{
				{
					PacketID:        "pkt-multi",
					ClaimText:       "Multi-source verified claim",
					SourceClusterID: "cluster-paper-a",
					EvidenceSpans: []evidence.EvidenceSpan{
						{SourceCanonicalID: "doi:10.1/a", Snippet: "Snippet from A."},
						{SourceCanonicalID: "doi:10.1/b", Snippet: "Snippet from B."},
					},
					VerifierStatus: "verified",
					Confidence:     0.82,
				},
				{
					PacketID:       "pkt-dup-a",
					ClaimText:      "Shared conclusion claim",
					EvidenceSpans:  []evidence.EvidenceSpan{{SourceCanonicalID: "doi:10.1/a", Snippet: "Support A."}},
					VerifierStatus: "verified",
					Confidence:     0.7,
				},
				{
					PacketID:       "pkt-dup-b",
					ClaimText:      "Shared conclusion claim",
					EvidenceSpans:  []evidence.EvidenceSpan{{SourceCanonicalID: "doi:10.1/c", Snippet: "Support C."}},
					VerifierStatus: "verified",
					Confidence:     0.9,
				},
				{
					PacketID:               "pkt-contra",
					ClaimText:              "Contradictory claim",
					ContradictionPacketIDs: []string{"pkt-other"},
					EvidenceSpans:          []evidence.EvidenceSpan{{SourceCanonicalID: "doi:10.1/b", Snippet: "Conflict snippet."}},
					VerifierStatus:         "verified",
					Confidence:             0.5,
				},
				{
					PacketID:       "pkt-unsupported",
					ClaimText:      "Rejected claim",
					EvidenceSpans:  []evidence.EvidenceSpan{{SourceCanonicalID: "doi:10.1/a", Snippet: "Weak support."}},
					VerifierStatus: "rejected",
					Confidence:     0.2,
				},
				{
					PacketID:       "pkt-tentative",
					ClaimText:      "Tentative claim",
					EvidenceSpans:  []evidence.EvidenceSpan{{SourceCanonicalID: "doi:10.1/b", Snippet: "Maybe support."}},
					VerifierStatus: "pending",
					Confidence:     0.4,
				},
			},
		},
	}

	dossier := buildWorkspaceEvidenceDossier(result)

	assert.Equal(t, "dossier-1", dossier["dossierId"])
	assert.Equal(t, "bundle-1", dossier["bundleId"])
	assert.Equal(t, []string{"gap-a"}, dossier["unresolvedGaps"])

	verified := dossier["verifiedFindings"].([]map[string]any)
	require.Len(t, verified, 3)

	multi := verified[0]
	assert.Equal(t, "pkt-multi", multi["id"])
	assert.Equal(t, "cluster-paper-a", multi["sourceClusterId"])
	assert.Equal(t, []string{"doi:10.1/a", "doi:10.1/b"}, multi["sourceIds"])
	assert.Equal(t, []string{"Paper A", "Paper B"}, multi["sourceTitles"])
	assert.Equal(t, []string{"Snippet from A.", "Snippet from B."}, multi["evidenceSnippets"])
	assert.Equal(t, 0.82, multi["supportScore"])

	unsupported := dossier["unsupportedFindings"].([]map[string]any)
	require.Len(t, unsupported, 1)
	assert.Equal(t, "pkt-unsupported", unsupported[0]["id"])
	assert.Equal(t, "Rejected claim", unsupported[0]["claim"])
	assert.Equal(t, []string{"Weak support."}, unsupported[0]["evidenceSnippets"])

	tentative := dossier["tentativeFindings"].([]map[string]any)
	require.Len(t, tentative, 1)
	assert.Equal(t, "pkt-tentative", tentative[0]["id"])

	contradictions := dossier["contradictoryFindings"].([]map[string]any)
	require.Len(t, contradictions, 1)
	assert.Equal(t, "pkt-contra", contradictions[0]["id"])

	conclusions := dossier["conclusions"].([]map[string]any)
	require.Len(t, conclusions, 2)

	shared := conclusions[1]
	assert.Equal(t, "Shared conclusion claim", shared["claim"])
	assert.ElementsMatch(t, []string{"pkt-dup-a", "pkt-dup-b"}, shared["findingIds"])
	assert.ElementsMatch(t, []string{"doi:10.1/a", "doi:10.1/c"}, shared["sourceIds"])
	assert.ElementsMatch(t, []string{"Paper A", "Paper C"}, shared["sourceTitles"])
	assert.Equal(t, 0.9, shared["supportScore"])
}

func TestBuildFullPaperWorkspaceDossierArtifactContent(t *testing.T) {
	result := wisdev.ManuscriptPipelineResult{
		Dossier: evidence.Dossier{
			DossierID: "dossier-1",
			Gaps:      []string{"gap-a", "gap-b"},
		},
		RawMaterials: evidence.ManuscriptRawMaterialSet{
			RawMaterialSetID: "bundle-1",
			ClaimPackets: []evidence.EvidencePacket{
				{
					PacketID:       "pkt-1",
					ClaimText:      "Verified claim",
					EvidenceSpans:  []evidence.EvidenceSpan{{SourceCanonicalID: "doi:10.1/a", Snippet: "Support."}},
					VerifierStatus: "verified",
					Confidence:     0.8,
				},
			},
		},
	}

	workspace := buildFullPaperWorkspace("job-1", "session-1", "test query", result, nil)
	dossierArtifact := workspace["latestDossierArtifact"].(map[string]any)
	content := dossierArtifact["content"].(map[string]any)

	assert.Equal(t, []string{"gap-a", "gap-b"}, content["unresolvedGaps"])
	assert.NotNil(t, content["verifiedFindings"])
	assert.NotNil(t, content["rawMaterialSet"])
}
