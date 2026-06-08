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

type panicYoloLoop struct {
	message string
}

func (p panicYoloLoop) Run(context.Context, wisdev.LoopRequest, ...func(wisdev.PlanExecutionEvent)) (*wisdev.LoopResult, error) {
	panic(p.message)
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

	t.Run("WisDevJobHandler - Success", func(t *testing.T) {
		body := `{"query":"test","serviceTier":"priority"}`
		req := httptest.NewRequest(http.MethodPost, "/wisdev/job", strings.NewReader(body))
		req = withWisDevJobTestUser(req)
		rec := httptest.NewRecorder()
		called := make(chan struct{}, 1)
		ml.On("Run", mock.Anything, mock.MatchedBy(func(req wisdev.LoopRequest) bool {
			return req.ServiceTier == wisdev.ServiceTierPriority
		}), mock.Anything).
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
		job := waitForYoloJob(t, fmt.Sprint(resp["job_id"]))
		assert.Equal(t, "priority", job.ServiceTier)
		waitForUnifiedJobCompletion(t, job.ID)
	})

	t.Run("WisDevJobHandler - forwards initial memory tiers", func(t *testing.T) {
		body := `{
			"query":"test memory handoff",
			"initialMemoryTiers":{
				"artifactMemory":[{"id":"prior-artifact","type":"paper","content":"Prior artifact."}]
			}
		}`
		req := httptest.NewRequest(http.MethodPost, "/wisdev/job", strings.NewReader(body))
		req = withWisDevJobTestUser(req)
		rec := httptest.NewRecorder()
		called := make(chan struct{}, 1)
		ml.On("Run", mock.Anything, mock.MatchedBy(func(req wisdev.LoopRequest) bool {
			return req.InitialMemoryTiers != nil &&
				len(req.InitialMemoryTiers.ArtifactMemory) == 1 &&
				req.InitialMemoryTiers.ArtifactMemory[0].ID == "prior-artifact"
		}), mock.Anything).
			Run(func(mock.Arguments) { called <- struct{}{} }).
			Return(&wisdev.LoopResult{Papers: []search.Paper{}}, nil).
			Once()

		WisDevJobHandler(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.NotEmpty(t, resp["job_id"])
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
		// so the background call is absorbed without stealing later .Once()
		// expectations in this test.
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

	t.Run("runWisDevPipeline - Panic Path", func(t *testing.T) {
		ml.ExpectedCalls = nil
		ml.Calls = nil
		previousGateway := GlobalYoloGateway
		previousRunner := runUnifiedWisDevJobLoop
		GlobalYoloGateway = &wisdev.AgentGateway{Runtime: wisdev.NewUnifiedResearchRuntime(nil, nil, nil, nil)}
		t.Cleanup(func() {
			GlobalYoloGateway = previousGateway
			runUnifiedWisDevJobLoop = previousRunner
		})
		runUnifiedWisDevJobLoop = func(context.Context, *wisdev.UnifiedResearchRuntime, wisdev.LoopRequest, func(wisdev.PlanExecutionEvent)) (*wisdev.UnifiedResearchResult, error) {
			panic("provider panic")
		}
		job := &YoloJob{
			ID:            "wisdev_panic",
			Mode:          "yolo",
			TraceID:       "trace-job-panic",
			UnifiedEvents: make(chan UnifiedEvent, 10),
		}

		runWisDevPipeline(context.Background(), job, ml)

		var events []UnifiedEvent
		for e := range job.UnifiedEvents {
			events = append(events, e)
		}
		if assert.NotEmpty(t, events) {
			last := events[len(events)-1]
			assert.Equal(t, "job_failed", last.Type)
			assert.Equal(t, "trace-job-panic", last.TraceID)
			assert.Equal(t, "yolo", last.Mode)
			assert.Contains(t, last.Error, "autonomous loop panic: provider panic")
			if assert.NotNil(t, last.Payload) {
				errPayload, ok := last.Payload["error"].(map[string]any)
				if assert.True(t, ok) {
					assert.Equal(t, "AUTONOMOUS_LOOP_PANIC", errPayload["code"])
					assert.Equal(t, "trace-job-panic", errPayload["traceId"])
				}
			}
		}
	})
}
