package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

type mockYoloLoop struct {
	mock.Mock
}

func (m *mockYoloLoop) Run(ctx context.Context, req wisdev.LoopRequest, onEvent ...func(wisdev.PlanExecutionEvent)) (*wisdev.LoopResult, error) {
	var callback func(wisdev.PlanExecutionEvent)
	if len(onEvent) > 0 {
		callback = onEvent[0]
	}
	args := m.Called(ctx, req, callback)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*wisdev.LoopResult), args.Error(1)
}

func waitForMockCall(t *testing.T, called <-chan struct{}) {
	t.Helper()
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for async loop invocation")
	}
}

func waitForYoloJob(t *testing.T, jobID string) *YoloJob {
	t.Helper()
	var job *YoloJob
	assert.Eventually(t, func() bool {
		candidate, ok := yoloJobStore.get(jobID)
		if !ok {
			return false
		}
		job = candidate
		return true
	}, 2*time.Second, 10*time.Millisecond, "timed out waiting for job %s", jobID)
	return job
}

func waitForLegacyJobCompletion(t *testing.T, jobID string) {
	t.Helper()
	job := waitForYoloJob(t, jobID)
	if job.LegacyEvents == nil {
		return
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, open := <-job.LegacyEvents:
			if !open {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for legacy job %s to close", jobID)
		}
	}
}

func waitForUnifiedJobCompletion(t *testing.T, jobID string) {
	t.Helper()
	job := waitForYoloJob(t, jobID)
	if job.UnifiedEvents == nil {
		return
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, open := <-job.UnifiedEvents:
			if !open {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for unified job %s to close", jobID)
		}
	}
}

func useIsolatedYoloState(t *testing.T) {
	t.Helper()
	previousLoop := GlobalYoloLoop
	previousGateway := GlobalYoloGateway
	previousStore := yoloJobStore
	GlobalYoloLoop = nil
	GlobalYoloGateway = nil
	yoloJobStore = &yoloStore{jobs: make(map[string]*YoloJob)}
	t.Cleanup(func() {
		GlobalYoloLoop = previousLoop
		GlobalYoloGateway = previousGateway
		yoloJobStore = previousStore
	})
}

func TestYoloHandlers_FullRestore(t *testing.T) {
	useIsolatedYoloState(t)
	ml := &mockYoloLoop{}
	GlobalYoloLoop = ml

	t.Run("YoloExecuteHandler - Success", func(t *testing.T) {
		body := `{"query":"test"}`
		req := httptest.NewRequest(http.MethodPost, "/execute", strings.NewReader(body))
		rec := httptest.NewRecorder()
		called := make(chan struct{}, 1)
		ml.On("Run", mock.Anything, mock.Anything, mock.Anything).
			Run(func(mock.Arguments) { called <- struct{}{} }).
			Return(&wisdev.LoopResult{Papers: []search.Paper{}}, nil).
			Once()
		YoloExecuteHandler(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp YoloExecuteResponse
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.NotEmpty(t, resp.JobID)
		assert.NotEmpty(t, resp.TraceID)
		assert.Equal(t, resp.TraceID, rec.Header().Get("X-Trace-Id"))
		waitForMockCall(t, called)
		waitForLegacyJobCompletion(t, resp.JobID)
	})

	t.Run("YoloExecuteHandler - Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/execute", nil)
		rec := httptest.NewRecorder()
		YoloExecuteHandler(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("YoloExecuteHandler - Invalid Body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/execute", strings.NewReader("{invalid"))
		rec := httptest.NewRecorder()
		YoloExecuteHandler(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("WisDevJobHandler - Success", func(t *testing.T) {
		body := `{"query":"test"}`
		req := httptest.NewRequest(http.MethodPost, "/wisdev/job", strings.NewReader(body))
		req = withWisDevJobTestUser(req)
		rec := httptest.NewRecorder()
		called := make(chan struct{}, 1)
		ml.On("Run", mock.Anything, mock.Anything, mock.Anything).
			Run(func(mock.Arguments) { called <- struct{}{} }).
			Return(&wisdev.LoopResult{Papers: []search.Paper{}}, nil).
			Once()
		WisDevJobHandler(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.NotEmpty(t, resp["job_id"])
		assert.NotEmpty(t, resp["traceId"])
		assert.Equal(t, resp["traceId"], rec.Header().Get("X-Trace-Id"))
		waitForMockCall(t, called)
		waitForUnifiedJobCompletion(t, fmt.Sprint(resp["job_id"]))
	})

	t.Run("WisDevJobHandler - Invalid Body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/wisdev/job", strings.NewReader(`{invalid`))
		rec := httptest.NewRecorder()
		WisDevJobHandler(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("WisDevJobHandler - Auth Required", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/wisdev/job", strings.NewReader(`{"query":"test"}`))
		rec := httptest.NewRecorder()
		WisDevJobHandler(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrUnauthorized, resp.Error.Code)
	})

	t.Run("WisDevJobHandler - Whitespace Query Rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/wisdev/job", strings.NewReader(`{"query":"   "}`))
		rec := httptest.NewRecorder()
		WisDevJobHandler(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("WisDevStreamHandler - Success", func(t *testing.T) {
		jobID := "ws_success"
		job := &YoloJob{
			ID:            jobID,
			TraceID:       "trace-stream-1",
			UnifiedEvents: make(chan UnifiedEvent, 10),
		}
		ownedWisDevJobForTest(job)
		yoloJobStore.put(job)
		job.UnifiedEvents <- UnifiedEvent{Type: "job_done"}
		close(job.UnifiedEvents)

		req := httptest.NewRequest(http.MethodGet, "/wisdev/job/"+jobID+"/stream", nil)
		req = withWisDevJobTestUser(req)
		rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
		WisDevStreamHandler(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "trace-stream-1", rec.Header().Get("X-Trace-Id"))
	})

	t.Run("WisDevStreamHandler - Cancelled", func(t *testing.T) {
		jobID := "ws_cancelled"
		job := &YoloJob{
			ID:            jobID,
			UnifiedEvents: make(chan UnifiedEvent, 10),
		}
		ownedWisDevJobForTest(job)
		yoloJobStore.put(job)
		job.UnifiedEvents <- UnifiedEvent{Type: "job_cancelled", Message: "autonomous research cancelled"}
		close(job.UnifiedEvents)

		req := httptest.NewRequest(http.MethodGet, "/wisdev/job/"+jobID+"/stream", nil)
		req = withWisDevJobTestUser(req)
		rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
		WisDevStreamHandler(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("WisDevStreamHandler - Not Found", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wisdev/job/none/stream", nil)
		rec := httptest.NewRecorder()
		WisDevStreamHandler(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrNotFound, resp.Error.Code)
	})

	t.Run("YoloStreamHandler - Success", func(t *testing.T) {
		jobID := "ys_success"
		job := &YoloJob{
			ID:           jobID,
			TraceID:      "trace-legacy-stream-1",
			LegacyEvents: make(chan YoloEvent, 10),
		}
		yoloJobStore.put(job)
		job.LegacyEvents <- YoloEvent{Type: "complete"}
		close(job.LegacyEvents)

		req := httptest.NewRequest(http.MethodGet, "/stream?job_id="+jobID, nil)
		rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
		YoloStreamHandler(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "trace-legacy-stream-1", rec.Header().Get("X-Trace-Id"))
	})

	t.Run("YoloStreamHandler - Cancelled", func(t *testing.T) {
		jobID := "ys_cancelled"
		job := &YoloJob{
			ID:           jobID,
			TraceID:      "trace-legacy-cancel-1",
			LegacyEvents: make(chan YoloEvent, 10),
		}
		yoloJobStore.put(job)
		job.LegacyEvents <- YoloEvent{Type: "cancelled", TraceID: "trace-legacy-cancel-1"}
		close(job.LegacyEvents)

		req := httptest.NewRequest(http.MethodGet, "/stream?job_id="+jobID, nil)
		rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
		YoloStreamHandler(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "trace-legacy-cancel-1", rec.Header().Get("X-Trace-Id"))
	})

	t.Run("YoloStreamHandler - Not Found", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/stream?job_id=none", nil)
		rec := httptest.NewRecorder()
		YoloStreamHandler(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrNotFound, resp.Error.Code)
	})

	t.Run("YoloStatusHandler - Success", func(t *testing.T) {
		jobID := "legacy_status_job"
		job := &YoloJob{ID: jobID, TraceID: "trace-legacy-status-1", CreatedAt: time.Now()}
		yoloJobStore.put(job)
		req := httptest.NewRequest(http.MethodGet, "/agent/yolo/status?job_id="+jobID, nil)
		rec := httptest.NewRecorder()
		YoloStatusHandler(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, jobID, resp["job_id"])
		assert.Equal(t, "trace-legacy-status-1", resp["traceId"])
		assert.Equal(t, "running", resp["status"])
		assert.Equal(t, "trace-legacy-status-1", rec.Header().Get("X-Trace-Id"))
	})

	t.Run("YoloStatusHandler - Not Found", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/agent/yolo/status?job_id=none", nil)
		rec := httptest.NewRecorder()
		YoloStatusHandler(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrNotFound, resp.Error.Code)
	})

	t.Run("WisDevJobStatusHandler - Success", func(t *testing.T) {
		jobID := "status_job"
		job := &YoloJob{ID: jobID, TraceID: "trace-status-1", CreatedAt: time.Now()}
		ownedWisDevJobForTest(job)
		yoloJobStore.put(job)
		req := httptest.NewRequest(http.MethodGet, "/wisdev/job/"+jobID, nil)
		req = withWisDevJobTestUser(req)
		rec := httptest.NewRecorder()
		WisDevJobStatusHandler(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, jobID, resp["job_id"])
		assert.Equal(t, "trace-status-1", resp["traceId"])
		assert.Equal(t, "running", resp["status"])
		assert.Equal(t, "trace-status-1", rec.Header().Get("X-Trace-Id"))
	})

	t.Run("WisDevJobStatusHandler - Wrong Owner Rejected", func(t *testing.T) {
		jobID := "status_wrong_owner"
		job := &YoloJob{ID: jobID, TraceID: "trace-status-owner", UserID: "owner-user", CreatedAt: time.Now()}
		yoloJobStore.put(job)
		t.Cleanup(func() { yoloJobStore.delete(jobID) })
		req := httptest.NewRequest(http.MethodGet, "/wisdev/job/"+jobID, nil)
		req = withTestUserID(req, "intruder-user")
		rec := httptest.NewRecorder()
		WisDevJobStatusHandler(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrUnauthorized, resp.Error.Code)
	})

	t.Run("WisDevJobStatusHandler - Not Found", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wisdev/job/none", nil)
		rec := httptest.NewRecorder()
		WisDevJobStatusHandler(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrNotFound, resp.Error.Code)
	})

	t.Run("WisDevScheduleHandler - Success", func(t *testing.T) {
		body := `{"project_id":"p1", "schedule":"* * * * *", "query":"q"}`
		req := httptest.NewRequest(http.MethodPost, "/schedule", strings.NewReader(body))
		rec := httptest.NewRecorder()
		handler := NewWisDevHandler(nil, nil, nil, nil, nil, nil, nil)
		handler.WisDevScheduleHandler(rec, req)
		assert.Equal(t, http.StatusCreated, rec.Code)
		var resp map[string]any
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.NotEmpty(t, resp["schedule_id"])
		assert.NotEmpty(t, resp["traceId"])
		assert.Equal(t, resp["traceId"], rec.Header().Get("X-Trace-Id"))
	})

	t.Run("WisDevScheduleHandler - Invalid JSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/schedule", strings.NewReader(`{invalid`))
		rec := httptest.NewRecorder()
		handler := NewWisDevHandler(nil, nil, nil, nil, nil, nil, nil)
		handler.WisDevScheduleHandler(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("WisDevScheduleHandler - Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/schedule", nil)
		rec := httptest.NewRecorder()
		handler := NewWisDevHandler(nil, nil, nil, nil, nil, nil, nil)
		handler.WisDevScheduleHandler(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("WisDevScheduleRunHandler - Success", func(t *testing.T) {
		// WisDevScheduleRunHandler fires `go runWisDevPipeline(ctx, job, GlobalYoloLoop)`
		// asynchronously. Register a .Maybe() expectation narrowed to the exact
		// LoopRequest the cron goroutine produces (ProjectID:"default", BudgetCents:100)
		// so the background call is absorbed without stealing the .Once() expectations
		// that later subtests ("runYoloPipeline - Success Path" etc.) depend on.
		called := make(chan struct{}, 1)
		ml.On("Run", mock.Anything, mock.MatchedBy(func(req wisdev.LoopRequest) bool {
			return req.ProjectID == "default" && req.BudgetCents == 100
		}), mock.Anything).
			Run(func(mock.Arguments) { called <- struct{}{} }).
			Return(&wisdev.LoopResult{Papers: []search.Paper{}}, nil).
			Maybe()
		req := httptest.NewRequest(http.MethodPost, "/wisdev/schedule/run/1", nil)
		rec := httptest.NewRecorder()
		handler := NewWisDevHandler(nil, nil, nil, nil, nil, nil, nil)
		handler.WisDevScheduleRunHandler(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.NotEmpty(t, resp["job_id"])
		assert.Equal(t, "started", resp["status"])
		assert.NotEmpty(t, resp["traceId"])
		assert.Equal(t, resp["traceId"], rec.Header().Get("X-Trace-Id"))
		waitForMockCall(t, called)
		waitForUnifiedJobCompletion(t, fmt.Sprint(resp["job_id"]))
	})

	t.Run("WisDevScheduleRunHandler - Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wisdev/schedule/run/1", nil)
		rec := httptest.NewRecorder()
		handler := NewWisDevHandler(nil, nil, nil, nil, nil, nil, nil)
		handler.WisDevScheduleRunHandler(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("HandlePaper2Skill - Service Unavailable Without Compiler", func(t *testing.T) {
		body := `{"arxiv_id":"123"}`
		req := httptest.NewRequest(http.MethodPost, "/p2s", strings.NewReader(body))
		rec := httptest.NewRecorder()
		handler := NewWisDevHandler(
			wisdev.NewSessionManager(""),
			wisdev.NewGuidedFlow(),
			nil,
			nil,
			nil,
			nil,
			nil,
		)
		handler.HandlePaper2Skill(rec, req)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})

	t.Run("yoloCancelHandler - Success", func(t *testing.T) {
		jobID := "cancel_me"
		_, cancel := context.WithCancel(context.Background())
		job := &YoloJob{
			ID:            jobID,
			TraceID:       "trace-legacy-cancel-handler-1",
			Cancel:        cancel,
			LegacyEvents:  make(chan YoloEvent, 1),
			UnifiedEvents: make(chan UnifiedEvent, 1),
		}
		yoloJobStore.put(job)
		req := httptest.NewRequest(http.MethodPost, "/cancel", strings.NewReader(fmt.Sprintf(`{"job_id":"%s"}`, jobID)))
		rec := httptest.NewRecorder()
		yoloCancelHandler(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, true, resp["cancelled"])
		assert.Equal(t, "trace-legacy-cancel-handler-1", resp["traceId"])
		assert.Equal(t, "trace-legacy-cancel-handler-1", rec.Header().Get("X-Trace-Id"))
		select {
		case legacyEvent := <-job.LegacyEvents:
			assert.Equal(t, "cancelled", legacyEvent.Type)
			assert.Equal(t, "trace-legacy-cancel-handler-1", legacyEvent.TraceID)
		default:
			t.Fatal("expected legacy cancellation event")
		}
		select {
		case unifiedEvent := <-job.UnifiedEvents:
			assert.Equal(t, "job_cancelled", unifiedEvent.Type)
			assert.Equal(t, "trace-legacy-cancel-handler-1", unifiedEvent.TraceID)
		default:
			t.Fatal("expected unified cancellation event")
		}
		stored, ok := yoloJobStore.get(jobID)
		if assert.True(t, ok) {
			assert.Equal(t, "cancelled", stored.statusSnapshot())
		}
	})

	t.Run("yoloCancelHandler - Missing Job ID", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/cancel", strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		yoloCancelHandler(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("runYoloPipeline - Success Path", func(t *testing.T) {
		job := &YoloJob{
			ID:           "yolo_success",
			TraceID:      "trace-yolo-success-1",
			LegacyEvents: make(chan YoloEvent, 10),
		}
		ml.On("Run", mock.Anything, mock.Anything, mock.Anything).Return(&wisdev.LoopResult{Papers: []search.Paper{{ID: "1"}}}, nil).Once()
		runYoloPipeline(context.Background(), job, ml)
		var events []YoloEvent
		for e := range job.LegacyEvents {
			events = append(events, e)
		}
		assert.NotEmpty(t, events)
		assert.Equal(t, "trace-yolo-success-1", events[0].TraceID)
		assert.Equal(t, "complete", events[len(events)-1].Type)
		assert.Equal(t, "trace-yolo-success-1", events[len(events)-1].TraceID)
	})

	t.Run("runWisDevPipeline - Success Path", func(t *testing.T) {
		ml.ExpectedCalls = nil
		ml.Calls = nil
		previousGateway := GlobalYoloGateway
		previousRunner := runUnifiedWisDevJobLoop
		GlobalYoloGateway = &wisdev.AgentGateway{Runtime: wisdev.NewUnifiedResearchRuntime(nil, nil, nil, nil)}
		t.Cleanup(func() {
			GlobalYoloGateway = previousGateway
			runUnifiedWisDevJobLoop = previousRunner
		})
		reasoningGraph := &wisdev.ReasoningGraph{
			Query: "CRISPR gene therapy safety",
			Nodes: []wisdev.ReasoningNode{{ID: "hyp-1", Type: wisdev.ReasoningNodeHypothesis, Label: "Candidate claim"}},
		}
		memoryTiers := &wisdev.MemoryTierState{
			ArtifactMemory: []wisdev.MemoryEntry{{ID: "mem-1", Type: "paper", Content: "Paper A", CreatedAt: 1}},
		}
		runUnifiedWisDevJobLoop = func(_ context.Context, _ *wisdev.UnifiedResearchRuntime, req wisdev.LoopRequest, callback func(wisdev.PlanExecutionEvent)) (*wisdev.UnifiedResearchResult, error) {
			if callback != nil {
				callback(wisdev.PlanExecutionEvent{
					Type:      wisdev.EventProgress,
					TraceID:   "trace-wisdev-1",
					StepID:    "retrieve",
					Message:   "retrieval in progress",
					Payload:   map[string]any{"stage": "retrieve"},
					CreatedAt: 1,
				})
			}
			return &wisdev.UnifiedResearchResult{LoopResult: &wisdev.LoopResult{
				Papers: []search.Paper{{
					ID:            "paper-1",
					Title:         "Paper A",
					Abstract:      "Abstract A",
					Link:          "https://example.com/paper-a",
					Authors:       []string{"Ada"},
					Venue:         "Nature",
					Year:          2024,
					CitationCount: 7,
				}},
				Iterations:     2,
				Converged:      true,
				Mode:           wisdev.WisDevModeYOLO,
				ServiceTier:    wisdev.ServiceTierFlex,
				ReasoningGraph: reasoningGraph,
				MemoryTiers:    memoryTiers,
			}}, nil
		}
		job := &YoloJob{
			ID:            "wisdev_success",
			TraceID:       "trace-job-1",
			UnifiedEvents: make(chan UnifiedEvent, 10),
			LegacyEvents:  make(chan YoloEvent, 10),
		}
		runWisDevPipeline(context.Background(), job, ml)
		var events []UnifiedEvent
		for e := range job.UnifiedEvents {
			events = append(events, e)
		}
		assert.NotEmpty(t, events)
		assert.Equal(t, "job_started", events[0].Type)
		assert.Equal(t, "trace-job-1", events[0].TraceID)
		assert.Equal(t, "progress", events[1].Type)
		assert.Equal(t, "trace-job-1", events[1].TraceID)
		assert.Equal(t, "retrieve", events[1].StepID)
		assert.Equal(t, "job_done", events[len(events)-1].Type)
		assert.Equal(t, "yolo", events[len(events)-1].Mode)
		assert.Equal(t, "flex", events[len(events)-1].ServiceTier)
		assert.Equal(t, "trace-job-1", events[len(events)-1].TraceID)
		assert.Equal(t, reasoningGraph, events[len(events)-1].ReasoningGraph)
		assert.Equal(t, memoryTiers, events[len(events)-1].MemoryTiers)
		if assert.NotNil(t, events[len(events)-1].Payload) {
			papers, ok := events[len(events)-1].Payload["papers"].([]map[string]any)
			if assert.True(t, ok) {
				assert.Len(t, papers, 1)
				assert.Equal(t, "Paper A", papers[0]["title"])
			}
			assert.Equal(t, 2, events[len(events)-1].Payload["iterations_used"])
		}
	})

	t.Run("runYoloPipeline - Error Path", func(t *testing.T) {
		ml.ExpectedCalls = nil
		ml.Calls = nil
		job := &YoloJob{TraceID: "trace-yolo-fail-1", LegacyEvents: make(chan YoloEvent, 10)}
		ml.On("Run", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("fail")).Once()
		runYoloPipeline(context.Background(), job, ml)
		var last YoloEvent
		for e := range job.LegacyEvents {
			last = e
		}
		assert.Equal(t, "error", last.Type)
		assert.Equal(t, "trace-yolo-fail-1", last.TraceID)
	})

	t.Run("runWisDevPipeline - Error Path", func(t *testing.T) {
		ml.ExpectedCalls = nil
		ml.Calls = nil
		previousGateway := GlobalYoloGateway
		previousRunner := runUnifiedWisDevJobLoop
		GlobalYoloGateway = &wisdev.AgentGateway{Runtime: wisdev.NewUnifiedResearchRuntime(nil, nil, nil, nil)}
		t.Cleanup(func() {
			GlobalYoloGateway = previousGateway
			runUnifiedWisDevJobLoop = previousRunner
		})
		runUnifiedWisDevJobLoop = func(_ context.Context, _ *wisdev.UnifiedResearchRuntime, _ wisdev.LoopRequest, _ func(wisdev.PlanExecutionEvent)) (*wisdev.UnifiedResearchResult, error) {
			return nil, errors.New("quota exhausted")
		}
		job := &YoloJob{
			ID:            "wisdev_fail",
			Mode:          "yolo",
			TraceID:       "trace-job-fail",
			UnifiedEvents: make(chan UnifiedEvent, 10),
			LegacyEvents:  make(chan YoloEvent, 10),
		}

		runWisDevPipeline(context.Background(), job, ml)

		var events []UnifiedEvent
		for e := range job.UnifiedEvents {
			events = append(events, e)
		}
		if assert.NotEmpty(t, events) {
			last := events[len(events)-1]
			assert.Equal(t, "job_failed", last.Type)
			assert.Equal(t, "quota exhausted", last.Error)
			assert.Equal(t, "quota exhausted", last.Message)
			assert.Equal(t, "yolo", last.Mode)
			assert.Equal(t, "trace-job-fail", last.TraceID)
			if assert.NotNil(t, last.Payload) {
				errPayload, ok := last.Payload["error"].(map[string]any)
				if assert.True(t, ok) {
					assert.Equal(t, "AUTONOMOUS_LOOP_FAILED", errPayload["code"])
					assert.Equal(t, "quota exhausted", errPayload["message"])
					assert.Equal(t, "trace-job-fail", errPayload["traceId"])
				}
			}
		}
	})
}
