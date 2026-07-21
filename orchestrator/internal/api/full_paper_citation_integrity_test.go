package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/wisdev"
)

func newCitationIntegrityTestServer(t *testing.T, job map[string]any) (*http.ServeMux, *wisdev.AgentGateway, string, int64) {
	t.Helper()
	mux := http.NewServeMux()
	server := &wisdevServer{}
	tmpDir, err := os.MkdirTemp("", "wisdev_citation_integrity_test")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })
	_ = os.Setenv("WISDEV_STATE_DIR", tmpDir)
	t.Cleanup(func() { _ = os.Unsetenv("WISDEV_STATE_DIR") })

	store := wisdev.NewRuntimeStateStore(nil, nil)
	gateway := &wisdev.AgentGateway{StateStore: store}
	server.registerFullPaperRoutes(mux, gateway)

	jobID := wisdev.AsOptionalString(job["jobId"])
	require.NotEmpty(t, jobID)
	require.NoError(t, store.SaveFullPaperJob(jobID, job))
	saved, err := store.LoadFullPaperJob(jobID)
	require.NoError(t, err)
	return mux, gateway, jobID, wisdev.IntValue64(saved["updatedAt"])
}

func postCitationIntegrity(t *testing.T, mux *http.ServeMux, userID string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/full-paper/citation-integrity", bytes.NewReader(payload))
	if userID != "" {
		req = withTestUserID(req, userID)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func sampleCitationIntegrityJob(userID, jobID string) map[string]any {
	workspace := map[string]any{
		"sectionDraftArtifacts": []any{
			map[string]any{
				"sectionId": "intro",
				"content":   "Grounded [1].",
				"version":   1,
			},
			map[string]any{
				"sectionId": "body",
				"content":   "Missing [99].",
				"version":   1,
			},
		},
		"artifacts": []any{
			map[string]any{
				"type": "source_bundle",
				"content": map[string]any{
					"sources": []any{
						map[string]any{"paperId": "paper-1", "title": "Source One"},
					},
				},
			},
		},
		"rawMaterialSet": map[string]any{
			"claimPackets": []any{
				map[string]any{"packetId": "pkt-1", "claimText": "claim", "confidence": 0.9},
			},
		},
		"drafting":                 map[string]any{"sections": map[string]any{}, "sectionArtifactIds": []string{}},
		"latestManuscriptArtifact": map[string]any{"artifactId": "man_a", "type": "manuscript_snapshot", "version": 1, "content": map[string]any{}},
	}
	return map[string]any{
		"jobId":     jobID,
		"userId":    userID,
		"status":    "awaiting_approval",
		"artifacts": []any{},
		"workspace": workspace,
	}
}

func TestFullPaperCitationIntegrity_Verify(t *testing.T) {
	job := sampleCitationIntegrityJob("owner-1", "job_verify")
	mux, _, jobID, _ := newCitationIntegrityTestServer(t, job)

	rec := postCitationIntegrity(t, mux, "owner-1", map[string]any{
		"jobId":  jobID,
		"action": "verify",
	})
	require.Equal(t, http.StatusOK, rec.Code)

	var envelope map[string]any
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&envelope))
	result := envelope["result"].(map[string]any)
	summary := result["summary"].(map[string]any)
	assert.Equal(t, float64(2), summary["total"])
	assert.Equal(t, float64(1), summary["verified"])
	ungrounded := result["ungrounded"].([]any)
	require.Len(t, ungrounded, 1)
}

func TestFullPaperCitationIntegrity_RegroundSkipsUserEdited(t *testing.T) {
	job := sampleCitationIntegrityJob("owner-1", "job_reground")
	workspace := mapAny(job["workspace"])
	sections := sliceAnyMap(workspace["sectionDraftArtifacts"])
	for i := range sections {
		if wisdev.AsOptionalString(sections[i]["sectionId"]) == "body" {
			sections[i]["userEdited"] = true
		}
	}
	workspace["sectionDraftArtifacts"] = sections
	job["workspace"] = workspace

	_, _, _, _ = newCitationIntegrityTestServer(t, job)
	persisted := cloneAnyMap(job)

	_, err := runFullPaperCitationIntegrityReground(context.Background(), persisted, "fix citations", 1)
	require.NoError(t, err)

	updatedSections := sliceAnyMap(mapAny(persisted["workspace"])["sectionDraftArtifacts"])
	for _, section := range updatedSections {
		if wisdev.AsOptionalString(section["sectionId"]) == "body" {
			assert.Equal(t, true, section["userEdited"])
		}
	}
}

func TestFullPaperCitationIntegrity_OCCConflict(t *testing.T) {
	job := sampleCitationIntegrityJob("owner-1", "job_occ")
	mux, _, jobID, _ := newCitationIntegrityTestServer(t, job)

	rec := postCitationIntegrity(t, mux, "owner-1", map[string]any{
		"jobId":             jobID,
		"action":            "reground",
		"expectedUpdatedAt": 1,
	})
	assert.Equal(t, http.StatusConflict, rec.Code)
}

func TestFullPaperCitationIntegrity_MethodGuard(t *testing.T) {
	mux := http.NewServeMux()
	server := &wisdevServer{}
	server.registerFullPaperRoutes(mux, &wisdev.AgentGateway{})

	req := httptest.NewRequest(http.MethodGet, "/full-paper/citation-integrity", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestVerifyCitations_StructuredMode(t *testing.T) {
	handler := NewSynthesisHandler(stubGenerateClient{}, nil)
	body := `{
		"mode":"structured",
		"structuredCitations":[{"citationId":"cite_1","marker":"[1]","sectionId":"intro","paperId":"1"}],
		"sources":[{"paperId":"paper-1","title":"Gains Paper","authors":[{"name":"Lee"}],"year":2022}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/synthesis?action=verify-citations", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	handler.HandleSynthesis(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Citations []FETracedCitation `json:"citations"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Len(t, resp.Citations, 1)
	assert.True(t, resp.Citations[0].Verified)
}

func TestPersistFullPaperJobFromManuscript_BestEffort(t *testing.T) {
	assert.False(t, persistFullPaperJobFromManuscript(nil, "job_1", "user_1", "sess", "query", wisdev.ManuscriptPipelineResult{}, nil))
}
