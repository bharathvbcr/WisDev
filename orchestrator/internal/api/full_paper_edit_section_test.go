package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/wisdev"
)

// Returns the mux, gateway, jobId, and the ACTUAL persisted updatedAt (the store
// stamps a monotonic updatedAt on save, so callers must read it back for the
// optimistic-concurrency guard).
func newEditSectionTestServer(t *testing.T, _ int64, status string) (*http.ServeMux, *wisdev.AgentGateway, string, int64) {
	t.Helper()
	mux := http.NewServeMux()
	server := &wisdevServer{}
	tmpDir, _ := os.MkdirTemp("", "wisdev_edit_test")
	t.Cleanup(func() { os.RemoveAll(tmpDir) })
	os.Setenv("WISDEV_STATE_DIR", tmpDir)
	t.Cleanup(func() { os.Unsetenv("WISDEV_STATE_DIR") })

	store := wisdev.NewRuntimeStateStore(nil, nil)
	gw := &wisdev.AgentGateway{StateStore: store}
	server.registerFullPaperRoutes(mux, gw)

	jobID := "edit-job-1"
	job := map[string]any{
		"jobId":  jobID,
		"userId": "u1",
		"status": status,
		"artifacts": []any{
			map[string]any{"artifactId": "man_a", "type": "manuscript_snapshot", "version": 1},
		},
		"workspace": map[string]any{
			"sectionDraftArtifacts": []any{
				map[string]any{"sectionId": "sec1", "artifactId": "sec1_a", "version": 1, "content": "Original one.", "title": "Intro"},
				map[string]any{"sectionId": "sec2", "artifactId": "sec2_a", "version": 1, "content": "Original two.", "title": "Body"},
			},
			"drafting":                 map[string]any{"sections": map[string]any{}, "sectionArtifactIds": []string{}},
			"artifacts":                []any{map[string]any{"artifactId": "man_a", "type": "manuscript_snapshot", "version": 1, "content": map[string]any{}}},
			"latestManuscriptArtifact": map[string]any{"artifactId": "man_a", "type": "manuscript_snapshot", "version": 1, "content": map[string]any{}},
		},
	}
	require.NoError(t, store.SaveFullPaperJob(jobID, job))
	saved, err := store.LoadFullPaperJob(jobID)
	require.NoError(t, err)
	return mux, gw, jobID, wisdev.IntValue64(saved["updatedAt"])
}

func postEditSection(t *testing.T, mux *http.ServeMux, userID string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/full-paper/edit-section", bytes.NewReader(payload))
	if userID != "" {
		req = withTestUserID(req, userID)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestEditFullPaperSection_HappyPath(t *testing.T) {
	mux, gw, jobID, up := newEditSectionTestServer(t, 0, "awaiting_review")

	rec := postEditSection(t, mux, "u1", map[string]any{
		"jobId":             jobID,
		"sectionId":         "sec1",
		"contentHtml":       "<p>Edited <strong>intro</strong>.</p>",
		"expectedUpdatedAt": up,
	})
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	result := mapAny(resp["result"])
	sectionArtifact := mapAny(result["sectionArtifact"])
	assert.Equal(t, "<p>Edited <strong>intro</strong>.</p>", sectionArtifact["content"])
	assert.Equal(t, true, sectionArtifact["userEdited"])
	assert.Equal(t, "u1", sectionArtifact["editedBy"])
	assert.EqualValues(t, 2, wisdev.IntValue(sectionArtifact["version"]))

	// Persisted state carries the edit + a bumped updatedAt.
	saved, err := gw.StateStore.LoadFullPaperJob(jobID)
	require.NoError(t, err)
	assert.NotEqualValues(t, up, wisdev.IntValue64(saved["updatedAt"]))
	drafts := sliceAnyMap(mapAny(saved["workspace"])["sectionDraftArtifacts"])
	var found map[string]any
	for _, d := range drafts {
		if wisdev.AsOptionalString(d["sectionId"]) == "sec1" {
			found = d
		}
	}
	require.NotNil(t, found)
	assert.Equal(t, true, found["userEdited"])
}

// The rebuilt manuscript snapshot's section views must carry the userEdited /
// editedBy provenance so the UI can badge user-owned sections and the client-side
// merge rule can avoid overwriting them (P4).
func TestEditFullPaperSection_ManuscriptViewCarriesUserEdited(t *testing.T) {
	mux, _, jobID, up := newEditSectionTestServer(t, 0, "running")

	rec := postEditSection(t, mux, "u1", map[string]any{
		"jobId":             jobID,
		"sectionId":         "sec1",
		"contentHtml":       "<p>Edited.</p>",
		"expectedUpdatedAt": up,
	})
	require.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	manuscript := mapAny(mapAny(resp["result"])["manuscriptArtifact"])
	views := sliceAnyMap(mapAny(manuscript["content"])["sections"])
	require.NotEmpty(t, views)

	var editedView, untouchedView map[string]any
	for _, v := range views {
		switch wisdev.AsOptionalString(v["sectionId"]) {
		case "sec1":
			editedView = v
		case "sec2":
			untouchedView = v
		}
	}
	require.NotNil(t, editedView)
	assert.Equal(t, true, editedView["userEdited"])
	assert.Equal(t, "u1", editedView["editedBy"])
	// A section nobody edited must not falsely report userEdited.
	require.NotNil(t, untouchedView)
	assert.NotEqual(t, true, untouchedView["userEdited"])
}

func TestEditFullPaperSection_StripsScript(t *testing.T) {
	mux, _, jobID, up := newEditSectionTestServer(t, 0, "running")
	rec := postEditSection(t, mux, "u1", map[string]any{
		"jobId":             jobID,
		"sectionId":         "sec1",
		"contentHtml":       "<p>ok</p><script>alert(1)</script>",
		"expectedUpdatedAt": up,
	})
	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	content := wisdev.AsOptionalString(mapAny(mapAny(resp["result"])["sectionArtifact"])["content"])
	assert.Contains(t, content, "<p>ok</p>")
	assert.NotContains(t, content, "<script>")
}

func TestEditFullPaperSection_ConflictOnStaleUpdatedAt(t *testing.T) {
	mux, _, jobID, up := newEditSectionTestServer(t, 0, "running")
	rec := postEditSection(t, mux, "u1", map[string]any{
		"jobId":             jobID,
		"sectionId":         "sec1",
		"contentHtml":       "<p>x</p>",
		"expectedUpdatedAt": up - 1, // stale
	})
	assert.Equal(t, http.StatusConflict, rec.Code)
	assertAPIErrorCode(t, rec.Body.Bytes(), ErrInvalidParameters)
}

func TestEditFullPaperSection_TerminalJobRejected(t *testing.T) {
	mux, _, jobID, up := newEditSectionTestServer(t, 0, "completed")
	rec := postEditSection(t, mux, "u1", map[string]any{
		"jobId":             jobID,
		"sectionId":         "sec1",
		"contentHtml":       "<p>x</p>",
		"expectedUpdatedAt": up,
	})
	assert.Equal(t, http.StatusConflict, rec.Code)
}

func TestEditFullPaperSection_NonOwnerForbidden(t *testing.T) {
	mux, _, jobID, up := newEditSectionTestServer(t, 0, "running")
	rec := postEditSection(t, mux, "u2", map[string]any{
		"jobId":             jobID,
		"sectionId":         "sec1",
		"contentHtml":       "<p>x</p>",
		"expectedUpdatedAt": up,
	})
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestEditFullPaperSection_MissingFields(t *testing.T) {
	mux, _, jobID, up := newEditSectionTestServer(t, 0, "running")

	rec := postEditSection(t, mux, "u1", map[string]any{"jobId": jobID, "contentHtml": "<p>x</p>", "expectedUpdatedAt": up})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assertAPIErrorCode(t, rec.Body.Bytes(), ErrInvalidParameters)

	rec = postEditSection(t, mux, "u1", map[string]any{"jobId": jobID, "sectionId": "sec1", "expectedUpdatedAt": up})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestEditFullPaperSection_UnknownSection(t *testing.T) {
	mux, _, jobID, up := newEditSectionTestServer(t, 0, "running")
	rec := postEditSection(t, mux, "u1", map[string]any{
		"jobId":             jobID,
		"sectionId":         "does-not-exist",
		"contentHtml":       "<p>x</p>",
		"expectedUpdatedAt": up,
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestEditFullPaperSection_MethodGuard(t *testing.T) {
	mux, _, _, _ := newEditSectionTestServer(t, 0, "running")
	req := httptest.NewRequest(http.MethodGet, "/full-paper/edit-section", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}
