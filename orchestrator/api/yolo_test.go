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

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

type mockYoloLoop struct {
	mock.Mock
}

func (m *mockYoloLoop) Run(ctx context.Context, req wisdev.LoopRequest) (*wisdev.LoopResult, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*wisdev.LoopResult), args.Error(1)
}

func TestYoloHandlers_FullRestore(t *testing.T) {
	ml := &mockYoloLoop{}
	GlobalYoloLoop = ml

	t.Run("YoloExecuteHandler - Success", func(t *testing.T) {
		body := `{"query":"test"}`
		req := httptest.NewRequest(http.MethodPost, "/execute", strings.NewReader(body))
		rec := httptest.NewRecorder()
		ml.On("Run", mock.Anything, mock.Anything).Return(&wisdev.LoopResult{Papers: []search.Paper{}}, nil).Once()
		YoloExecuteHandler(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
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
		rec := httptest.NewRecorder()
		ml.On("Run", mock.Anything, mock.Anything).Return(&wisdev.LoopResult{Papers: []search.Paper{}}, nil).Once()
		WisDevJobHandler(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
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

	t.Run("WisDevStreamHandler - Success", func(t *testing.T) {
		jobID := "ws_success"
		job := &YoloJob{
			ID:            jobID,
			UnifiedEvents: make(chan UnifiedEvent, 10),
		}
		yoloJobStore.put(job)
		job.UnifiedEvents <- UnifiedEvent{Type: "job_done"}
		close(job.UnifiedEvents)

		req := httptest.NewRequest(http.MethodGet, "/wisdev/job/"+jobID+"/stream", nil)
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
			LegacyEvents: make(chan YoloEvent, 10),
		}
		yoloJobStore.put(job)
		job.LegacyEvents <- YoloEvent{Type: "complete"}
		close(job.LegacyEvents)

		req := httptest.NewRequest(http.MethodGet, "/stream?job_id="+jobID, nil)
		rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
		YoloStreamHandler(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
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

	t.Run("WisDevJobStatusHandler - Success", func(t *testing.T) {
		jobID := "status_job"
		job := &YoloJob{ID: jobID, CreatedAt: time.Now()}
		yoloJobStore.put(job)
		req := httptest.NewRequest(http.MethodGet, "/wisdev/job/"+jobID, nil)
		rec := httptest.NewRecorder()
		WisDevJobStatusHandler(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
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
		handler := NewWisDevHandler(nil, nil, nil, nil, nil, nil)
		handler.WisDevScheduleHandler(rec, req)
		assert.Equal(t, http.StatusCreated, rec.Code)
	})

	t.Run("WisDevScheduleRunHandler", func(t *testing.T) {
		// WisDevScheduleRunHandler fires `go runWisDevPipeline(ctx, job, GlobalYoloLoop)`
		// asynchronously. Register a .Maybe() expectation narrowed to the exact
		// LoopRequest the cron goroutine produces (ProjectID:"default", BudgetCents:100)
		// so the background call is absorbed without stealing the .Once() expectations
		// that later subtests ("runYoloPipeline - Success Path" etc.) depend on.
		ml.On("Run", mock.Anything, mock.MatchedBy(func(req wisdev.LoopRequest) bool {
			return req.ProjectID == "default" && req.BudgetCents == 100
		})).Return(&wisdev.LoopResult{Papers: []search.Paper{}}, nil).Maybe()
		req := httptest.NewRequest(http.MethodPost, "/wisdev/schedule/run/1", nil)
		rec := httptest.NewRecorder()
		handler := NewWisDevHandler(nil, nil, nil, nil, nil, nil)
		handler.WisDevScheduleRunHandler(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
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
		)
		handler.HandlePaper2Skill(rec, req)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})

	t.Run("yoloCancelHandler - Success", func(t *testing.T) {
		jobID := "cancel_me"
		_, cancel := context.WithCancel(context.Background())
		job := &YoloJob{
			ID:            jobID,
			Cancel:        cancel,
			LegacyEvents:  make(chan YoloEvent, 1),
			UnifiedEvents: make(chan UnifiedEvent, 1),
		}
		yoloJobStore.put(job)
		req := httptest.NewRequest(http.MethodPost, "/cancel", strings.NewReader(fmt.Sprintf(`{"job_id":"%s"}`, jobID)))
		rec := httptest.NewRecorder()
		yoloCancelHandler(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
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
			LegacyEvents: make(chan YoloEvent, 10),
		}
		ml.On("Run", mock.Anything, mock.Anything).Return(&wisdev.LoopResult{Papers: []search.Paper{{ID: "1"}}}, nil).Once()
		runYoloPipeline(context.Background(), job, ml)
		var events []YoloEvent
		for e := range job.LegacyEvents {
			events = append(events, e)
		}
		assert.NotEmpty(t, events)
		assert.Equal(t, "complete", events[len(events)-1].Type)
	})

	t.Run("runWisDevPipeline - Success Path", func(t *testing.T) {
		job := &YoloJob{
			ID:            "wisdev_success",
			UnifiedEvents: make(chan UnifiedEvent, 10),
			LegacyEvents:  make(chan YoloEvent, 10),
		}
		ml.On("Run", mock.Anything, mock.Anything).Return(&wisdev.LoopResult{Papers: []search.Paper{{ID: "1"}}}, nil).Once()
		runWisDevPipeline(context.Background(), job, ml)
		var events []UnifiedEvent
		for e := range job.UnifiedEvents {
			events = append(events, e)
		}
		assert.NotEmpty(t, events)
		assert.Equal(t, "job_done", events[len(events)-1].Type)
	})
	
	t.Run("runYoloPipeline - Error Path", func(t *testing.T) {
		ml.ExpectedCalls = nil
		ml.Calls = nil
		job := &YoloJob{LegacyEvents: make(chan YoloEvent, 10)}
		ml.On("Run", mock.Anything, mock.Anything).Return(nil, errors.New("fail")).Once()
		runYoloPipeline(context.Background(), job, ml)
		var last YoloEvent
		for e := range job.LegacyEvents { last = e }
		assert.Equal(t, "error", last.Type)
	})
}
