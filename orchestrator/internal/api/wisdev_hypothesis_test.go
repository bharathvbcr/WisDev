package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type jobStreamFlushRecorder struct {
	*httptest.ResponseRecorder
}

func (r *jobStreamFlushRecorder) Flush() {}

type hypothesisListDBStub struct {
	row pgx.Row
}

func (s *hypothesisListDBStub) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (s *hypothesisListDBStub) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, nil
}

func (s *hypothesisListDBStub) QueryRow(context.Context, string, ...any) pgx.Row {
	return s.row
}

func (s *hypothesisListDBStub) Begin(context.Context) (pgx.Tx, error) {
	return nil, nil
}

func (s *hypothesisListDBStub) Ping(context.Context) error {
	return nil
}

func (s *hypothesisListDBStub) Close() {}

type hypothesisListRow struct {
	payload []byte
}

func (r *hypothesisListRow) Scan(dest ...any) error {
	if len(dest) > 0 {
		if payload, ok := dest[0].(*[]byte); ok {
			*payload = append([]byte(nil), r.payload...)
		}
	}
	return nil
}

func TestApplyRefinementResultUpdatesQuestState(t *testing.T) {
	quest := &wisdev.QuestState{
		ID:         "quest_1",
		Hypotheses: []*wisdev.Hypothesis{{ID: "hyp_1", Text: "original hypothesis"}},
	}
	target := quest.Hypotheses[0]

	applyRefinementResult(quest, target, target.ID, &wisdev.LoopResult{
		FinalAnswer: "Refinement summary",
		Evidence: []wisdev.EvidenceFinding{
			{
				ID:         "ev_1",
				Claim:      "Refined supporting claim",
				PaperTitle: "Paper One",
				Confidence: 0.91,
				Specialist: wisdev.SpecialistStatus{Verification: 1},
			},
		},
	})

	if target.Status != "refined" {
		t.Fatalf("expected hypothesis status to be refined, got %q", target.Status)
	}
	if target.EvidenceCount != 1 {
		t.Fatalf("expected evidence count to be 1, got %d", target.EvidenceCount)
	}
	if len(target.Evidence) != 1 {
		t.Fatalf("expected evidence slice to be updated")
	}
	if quest.EvidenceDossiers == nil || quest.EvidenceDossiers[target.ID] == nil {
		t.Fatalf("expected refinement dossier to be created")
	}
	if len(quest.EvidenceDossiers[target.ID].Verified) != 1 {
		t.Fatalf("expected verified dossier entries to be recorded")
	}
	if quest.ResearchScratchpad["refine_"+target.ID] != "Refinement summary" {
		t.Fatalf("expected refinement summary to be persisted to scratchpad")
	}
}

func TestHandleJobStreamRetainsCompletedModernJobForReplay(t *testing.T) {
	jobID := "modern_job_done"
	job := &YoloJob{
		ID:            jobID,
		TraceID:       "trace-modern-stream-1",
		UserID:        testWisDevJobUserID,
		UnifiedEvents: make(chan UnifiedEvent, 1),
	}
	yoloJobStore.put(job)
	t.Cleanup(func() { yoloJobStore.delete(jobID) })
	job.UnifiedEvents <- UnifiedEvent{Type: "job_done"}

	req := httptest.NewRequest("GET", "/wisdev/job/"+jobID+"/stream", nil)
	req = withWisDevJobTestUser(req)
	rec := &jobStreamFlushRecorder{ResponseRecorder: httptest.NewRecorder()}

	(&WisDevHandler{}).HandleJobStream(rec, req)

	if got := rec.Header().Get("X-Trace-Id"); got != "trace-modern-stream-1" {
		t.Fatalf("expected stream trace header to be propagated, got %q", got)
	}

	stored, ok := yoloJobStore.get(jobID)
	if !ok {
		t.Fatalf("expected completed modern job to remain replayable in store")
	}
	if got := stored.statusSnapshot(); got != "completed" {
		t.Fatalf("expected completed modern job status, got %q", got)
	}
	if stored.RetainUntil.IsZero() {
		t.Fatalf("expected completed modern job to have retention deadline")
	}
}

func TestHandleListHypothesesIncludesSynthesis(t *testing.T) {
	quest := &wisdev.QuestState{
		ID:     "quest_1",
		UserID: "user-1",
		Status: "complete",
		Hypotheses: []*wisdev.Hypothesis{
			{ID: "hyp_1", Text: "Recovered hypothesis"},
		},
		Synthesis: &wisdev.SynthesisResult{
			Sections: map[string]string{
				"main": "Recovered synthesis from snapshot",
			},
		},
	}
	payload, err := json.Marshal(quest)
	if err != nil {
		t.Fatalf("marshal quest: %v", err)
	}

	handler := &WisDevHandler{
		gateway: &wisdev.AgentGateway{
			DB: &hypothesisListDBStub{
				row: &hypothesisListRow{payload: payload},
			},
		},
	}
	req := httptest.NewRequest("GET", "/wisdev/hypothesis/quest_1/list", nil)
	req.Header.Set("X-Trace-Id", "trace-hypothesis-list-1")
	req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "user-1"))
	rec := httptest.NewRecorder()

	handler.handleListHypotheses(rec, req, "quest_1")

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp struct {
		OK                 bool                             `json:"ok"`
		TraceID            string                           `json:"traceId"`
		HypothesisSnapshot wisdevHypothesisSnapshotEnvelope `json:"hypothesisSnapshot"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected ok response, got %#v", resp)
	}
	if resp.TraceID != "trace-hypothesis-list-1" {
		t.Fatalf("expected traceId in response, got %q", resp.TraceID)
	}
	if got := rec.Header().Get("X-Trace-Id"); got != "trace-hypothesis-list-1" {
		t.Fatalf("expected X-Trace-Id to be propagated, got %q", got)
	}
	if resp.HypothesisSnapshot.QuestID != "quest_1" {
		t.Fatalf("expected questId to be preserved, got %q", resp.HypothesisSnapshot.QuestID)
	}
	if resp.HypothesisSnapshot.Synthesis == nil {
		t.Fatalf("expected synthesis to be included in list response")
	}
	if got := resp.HypothesisSnapshot.Synthesis.Sections["main"]; got != "Recovered synthesis from snapshot" {
		t.Fatalf("expected recovered synthesis, got %q", got)
	}
	if len(resp.HypothesisSnapshot.Hypotheses) != 1 || resp.HypothesisSnapshot.Hypotheses[0].ID != "hyp_1" {
		t.Fatalf("expected hypothesis payload to remain intact, got %#v", resp.HypothesisSnapshot.Hypotheses)
	}
}

func TestHandleAcceptHypothesisIncludesTraceID(t *testing.T) {
	quest := &wisdev.QuestState{
		ID:     "quest_1",
		UserID: "user-1",
		Status: "active",
		Hypotheses: []*wisdev.Hypothesis{
			{
				ID:   "hyp_1",
				Text: "Recovered hypothesis",
				Evidence: []*wisdev.EvidenceFinding{
					{
						ID:         "ev_1",
						Claim:      "Supported claim",
						PaperTitle: "Paper One",
						Confidence: 0.82,
					},
				},
			},
		},
	}
	payload, err := json.Marshal(quest)
	if err != nil {
		t.Fatalf("marshal quest: %v", err)
	}

	handler := &WisDevHandler{
		gateway: &wisdev.AgentGateway{
			DB: &hypothesisListDBStub{
				row: &hypothesisListRow{payload: payload},
			},
		},
	}
	req := httptest.NewRequest("POST", "/wisdev/hypothesis/quest_1/accept/hyp_1", nil)
	req.Header.Set("X-Trace-Id", "trace-hypothesis-accept-1")
	req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "user-1"))
	rec := httptest.NewRecorder()

	handler.handleAcceptHypothesis(rec, req, "quest_1", "hyp_1")

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp struct {
		OK               bool                           `json:"ok"`
		TraceID          string                         `json:"traceId"`
		HypothesisUpdate wisdevHypothesisUpdateEnvelope `json:"hypothesisUpdate"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected ok response, got %#v", resp)
	}
	if resp.TraceID != "trace-hypothesis-accept-1" {
		t.Fatalf("expected traceId in response, got %q", resp.TraceID)
	}
	if got := rec.Header().Get("X-Trace-Id"); got != "trace-hypothesis-accept-1" {
		t.Fatalf("expected X-Trace-Id to be propagated, got %q", got)
	}
	if got := resp.HypothesisUpdate.HypothesisID; got != "hyp_1" {
		t.Fatalf("expected hypothesisId hyp_1, got %q", got)
	}
	if got := resp.HypothesisUpdate.AcceptedClaimsN; got != 1 {
		t.Fatalf("expected acceptedClaimsN 1, got %d", got)
	}
	if resp.HypothesisUpdate.Hypothesis == nil {
		t.Fatalf("expected hypothesis payload to be returned")
	}
	if got := resp.HypothesisUpdate.Hypothesis.Status; got != "accepted" {
		t.Fatalf("expected accepted hypothesis status, got %q", got)
	}
}

func TestHandleListHypothesesRejectsMismatchedOwner(t *testing.T) {
	quest := &wisdev.QuestState{
		ID:     "quest_1",
		UserID: "owner-1",
		Status: "complete",
	}
	payload, err := json.Marshal(quest)
	if err != nil {
		t.Fatalf("marshal quest: %v", err)
	}

	handler := &WisDevHandler{
		gateway: &wisdev.AgentGateway{
			DB: &hypothesisListDBStub{
				row: &hypothesisListRow{payload: payload},
			},
		},
	}
	req := httptest.NewRequest("GET", "/wisdev/hypothesis/quest_1/list", nil)
	req.Header.Set("X-Trace-Id", "trace-hypothesis-forbidden-1")
	req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "other-user"))
	rec := httptest.NewRecorder()

	handler.handleListHypotheses(rec, req, "quest_1")

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
	var resp APIError
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error.Code != ErrUnauthorized {
		t.Fatalf("expected unauthorized error code, got %q", resp.Error.Code)
	}
	if resp.TraceID != "trace-hypothesis-forbidden-1" {
		t.Fatalf("expected trace ID in error payload, got %q", resp.TraceID)
	}
}

func TestHandleListHypothesesRejectsOwnerlessQuestForNormalUsers(t *testing.T) {
	quest := &wisdev.QuestState{
		ID:     "quest_1",
		UserID: "",
		Status: "complete",
	}
	payload, err := json.Marshal(quest)
	if err != nil {
		t.Fatalf("marshal quest: %v", err)
	}

	handler := &WisDevHandler{
		gateway: &wisdev.AgentGateway{
			DB: &hypothesisListDBStub{
				row: &hypothesisListRow{payload: payload},
			},
		},
	}
	req := httptest.NewRequest("GET", "/wisdev/hypothesis/quest_1/list", nil)
	req.Header.Set("X-Trace-Id", "trace-hypothesis-ownerless-1")
	req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "user-1"))
	rec := httptest.NewRecorder()

	handler.handleListHypotheses(rec, req, "quest_1")

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
	var resp APIError
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error.Code != ErrUnauthorized {
		t.Fatalf("expected unauthorized error code, got %q", resp.Error.Code)
	}
	if resp.Error.Message != "quest ownership missing; access denied" {
		t.Fatalf("expected ownerless access message, got %q", resp.Error.Message)
	}
}

func TestHandleWisDevHypothesesRejectsWrongMethodForList(t *testing.T) {
	handler := &WisDevHandler{}
	req := httptest.NewRequest(http.MethodPost, "/wisdev/hypothesis/quest_1/list", nil)
	req.Header.Set("X-Trace-Id", "trace-hypothesis-method-1")
	rec := httptest.NewRecorder()

	handler.HandleWisDevHypotheses(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
	var resp APIError
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error.Code != ErrBadRequest {
		t.Fatalf("expected bad request error code, got %q", resp.Error.Code)
	}
	if resp.TraceID != "trace-hypothesis-method-1" {
		t.Fatalf("expected trace ID in error payload, got %q", resp.TraceID)
	}
}
