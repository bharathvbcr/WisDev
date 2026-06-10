package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/wisdev"
)

type noFlushRecorder struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (w *noFlushRecorder) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *noFlushRecorder) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(p)
}

func (w *noFlushRecorder) WriteHeader(statusCode int) {
	w.status = statusCode
}

type yoloScheduleRow struct {
	query     string
	projectID string
	err       error
}

func (r yoloScheduleRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) >= 1 {
		if ptr, ok := dest[0].(*string); ok {
			*ptr = r.query
		}
	}
	if len(dest) >= 2 {
		if ptr, ok := dest[1].(*string); ok {
			*ptr = r.projectID
		}
	}
	return nil
}

type yoloScheduleDBStub struct {
	execErr     error
	queryRowErr error
	queryRowVal yoloScheduleRow
}

func (db yoloScheduleDBStub) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, db.execErr
}

func (db yoloScheduleDBStub) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("query not used")
}

func (db yoloScheduleDBStub) QueryRow(context.Context, string, ...any) pgx.Row {
	if db.queryRowErr != nil {
		return yoloScheduleRow{err: db.queryRowErr}
	}
	return db.queryRowVal
}

func (db yoloScheduleDBStub) Begin(context.Context) (pgx.Tx, error) {
	return nil, errors.New("begin not used")
}
func (db yoloScheduleDBStub) Ping(context.Context) error { return nil }
func (db yoloScheduleDBStub) Close()                     {}

func TestYoloNextPass(t *testing.T) {
	useIsolatedYoloState(t)
	t.Run("runWisDevPipeline nil loop emits unified failure and synthesizes trace", func(t *testing.T) {
		previousGateway := GlobalYoloGateway
		GlobalYoloGateway = nil
		t.Cleanup(func() {
			GlobalYoloGateway = previousGateway
		})

		job := &YoloJob{ID: "nil-unified", UnifiedEvents: make(chan UnifiedEvent, 1)}
		runWisDevPipeline(context.Background(), job, nil)
		var events []UnifiedEvent
		for e := range job.UnifiedEvents {
			events = append(events, e)
		}
		if assert.Len(t, events, 1) {
			assert.Equal(t, "job_failed", events[0].Type)
			assert.Equal(t, "WISDEV_UNIFIED_RUNTIME_UNAVAILABLE", events[0].Payload["error"].(map[string]any)["code"])
			assert.NotEmpty(t, events[0].TraceID)
			assert.Equal(t, events[0].TraceID, job.TraceID)
		}
	})

	t.Run("runWisDevPipeline canceled before send exits cleanly", func(t *testing.T) {
		previousGateway := GlobalYoloGateway
		GlobalYoloGateway = nil
		t.Cleanup(func() {
			GlobalYoloGateway = previousGateway
		})

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		job := &YoloJob{ID: "cancelled-wisdev", TraceID: "trace-cancelled-wisdev", UnifiedEvents: make(chan UnifiedEvent)}
		runWisDevPipeline(ctx, job, nil)
		var events []UnifiedEvent
		for e := range job.UnifiedEvents {
			events = append(events, e)
		}
		assert.Len(t, events, 0)
	})

	t.Run("runWisDevPipeline uses unified runtime result and callback filtering", func(t *testing.T) {
		previousGateway := GlobalYoloGateway
		previousRunner := runUnifiedWisDevJobLoop
		GlobalYoloGateway = &wisdev.AgentGateway{Runtime: wisdev.NewUnifiedResearchRuntime(nil, nil, nil, nil)}
		runUnifiedWisDevJobLoop = func(_ context.Context, _ *wisdev.UnifiedResearchRuntime, req wisdev.LoopRequest, callback func(wisdev.PlanExecutionEvent)) (*wisdev.UnifiedResearchResult, error) {
			assert.Equal(t, string(wisdev.WisDevModeGuided), req.Mode)
			assert.Equal(t, 100, req.BudgetCents)
			if callback != nil {
				callback(wisdev.PlanExecutionEvent{Type: wisdev.EventStepCompleted, StepID: "ignore", Message: "skip"})
				callback(wisdev.PlanExecutionEvent{Type: wisdev.EventProgress, StepID: "keep", Message: "keep"})
			}
			return &wisdev.UnifiedResearchResult{LoopResult: &wisdev.LoopResult{
				Papers:      []search.Paper{{ID: "paper-9", Title: "Unified Paper"}},
				Iterations:  4,
				Converged:   false,
				Mode:        "",
				ServiceTier: "",
			}}, nil
		}
		t.Cleanup(func() {
			GlobalYoloGateway = previousGateway
			runUnifiedWisDevJobLoop = previousRunner
		})

		job := &YoloJob{
			ID:            "fallback-result",
			Mode:          "guided",
			TraceID:       "trace-fallback-result",
			UnifiedEvents: make(chan UnifiedEvent, 8),
		}
		runWisDevPipeline(context.Background(), job, nil)
		var events []UnifiedEvent
		for e := range job.UnifiedEvents {
			events = append(events, e)
		}
		if assert.NotEmpty(t, events) {
			last := events[len(events)-1]
			assert.Equal(t, "job_done", last.Type)
			assert.Equal(t, string(wisdev.WisDevModeGuided), last.Mode)
			assert.Equal(t, string(wisdev.ServiceTierPriority), last.ServiceTier)
			assert.Equal(t, "Autonomous loop completed", last.Reason)
			if assert.NotNil(t, last.Payload) {
				assert.Equal(t, 4, last.Payload["iterations_used"])
			}
		}
	})

	t.Run("runWisDevPipeline preserves requested priority tier when runtime omits it", func(t *testing.T) {
		previousGateway := GlobalYoloGateway
		previousRunner := runUnifiedWisDevJobLoop
		GlobalYoloGateway = &wisdev.AgentGateway{Runtime: wisdev.NewUnifiedResearchRuntime(nil, nil, nil, nil)}
		runUnifiedWisDevJobLoop = func(_ context.Context, _ *wisdev.UnifiedResearchRuntime, req wisdev.LoopRequest, _ func(wisdev.PlanExecutionEvent)) (*wisdev.UnifiedResearchResult, error) {
			assert.Equal(t, string(wisdev.WisDevModeYOLO), req.Mode)
			assert.Equal(t, wisdev.ServiceTierPriority, req.ServiceTier)
			return &wisdev.UnifiedResearchResult{LoopResult: &wisdev.LoopResult{
				Papers:      []search.Paper{{ID: "paper-priority", Title: "Priority Paper"}},
				Iterations:  1,
				Mode:        wisdev.WisDevModeYOLO,
				ServiceTier: "",
			}}, nil
		}
		t.Cleanup(func() {
			GlobalYoloGateway = previousGateway
			runUnifiedWisDevJobLoop = previousRunner
		})

		job := &YoloJob{
			ID:            "priority-yolo-result",
			Mode:          "yolo",
			ServiceTier:   "priority",
			TraceID:       "trace-priority-yolo-result",
			UnifiedEvents: make(chan UnifiedEvent, 8),
		}
		runWisDevPipeline(context.Background(), job, nil)
		var events []UnifiedEvent
		for e := range job.UnifiedEvents {
			events = append(events, e)
		}
		if assert.NotEmpty(t, events) {
			last := events[len(events)-1]
			assert.Equal(t, "job_done", last.Type)
			assert.Equal(t, "yolo", last.Mode)
			assert.Equal(t, "priority", last.ServiceTier)
		}
	})

	t.Run("runWisDevPipeline writes replayable unified events into the session journal", func(t *testing.T) {
		t.Setenv("WISDEV_JOURNAL_PATH", filepath.Join(t.TempDir(), "wisdev_journal.jsonl"))
		gateway := &wisdev.AgentGateway{Journal: wisdev.NewRuntimeJournal(nil)}
		previousGateway := GlobalYoloGateway
		previousRunner := runUnifiedWisDevJobLoop
		gateway.Runtime = wisdev.NewUnifiedResearchRuntime(nil, nil, nil, nil)
		GlobalYoloGateway = gateway
		runUnifiedWisDevJobLoop = func(_ context.Context, _ *wisdev.UnifiedResearchRuntime, _ wisdev.LoopRequest, callback func(wisdev.PlanExecutionEvent)) (*wisdev.UnifiedResearchResult, error) {
			if callback != nil {
				callback(wisdev.PlanExecutionEvent{
					Type:    wisdev.EventProgress,
					StepID:  "retrieve",
					Message: "retrieving evidence",
					Payload: map[string]any{"phase": "retrieve"},
				})
			}
			return &wisdev.UnifiedResearchResult{LoopResult: &wisdev.LoopResult{
				Papers:      []search.Paper{{ID: "paper-journal", Title: "Journaled Paper"}},
				Iterations:  2,
				Mode:        wisdev.WisDevModeGuided,
				ServiceTier: wisdev.ServiceTierStandard,
			}}, nil
		}
		t.Cleanup(func() {
			GlobalYoloGateway = previousGateway
			runUnifiedWisDevJobLoop = previousRunner
		})

		job := &YoloJob{
			ID:            "journal-job",
			Query:         "journal query",
			ProjectID:     "session-journal",
			Mode:          "guided",
			TraceID:       "trace-journal",
			UnifiedEvents: make(chan UnifiedEvent, 8),
		}
		runWisDevPipeline(context.Background(), job, nil)

		entries := gateway.Journal.ReadSession("session-journal", 10)
		if assert.Len(t, entries, 3) {
			assert.Equal(t, "job_started", entries[0].EventType)
			assert.Equal(t, "running", entries[0].Status)
			assert.Equal(t, "progress", entries[1].EventType)
			assert.Equal(t, "retrieve", entries[1].StepID)
			assert.Equal(t, "job_done", entries[2].EventType)
			assert.Equal(t, "completed", entries[2].Status)
			assert.Equal(t, "trace-journal", entries[2].TraceID)
			assert.Equal(t, "journal-job", entries[2].Payload["job_id"])
			assert.Equal(t, string(wisdev.WisDevModeGuided), entries[2].Payload["mode"])
			assert.Equal(t, "wisdev_autonomous_job", entries[2].Metadata["source"])
			payload, ok := entries[2].Payload["payload"].(map[string]any)
			if assert.True(t, ok) {
				papers, ok := payload["papers"].([]any)
				if assert.True(t, ok) {
					assert.Len(t, papers, 1)
				}
			}
		}
	})

	t.Run("normalizeWisDevJobMode", func(t *testing.T) {
		assert.Equal(t, string(wisdev.WisDevModeYOLO), normalizeWisDevJobMode(""))
		assert.Equal(t, string(wisdev.WisDevModeGuided), normalizeWisDevJobMode("unknown"))
		assert.Equal(t, string(wisdev.WisDevModeGuided), normalizeWisDevJobMode("guided"))
	})

	t.Run("buildUnifiedLoopPayload", func(t *testing.T) {
		assert.Nil(t, buildUnifiedLoopPayload(nil))
		payload := buildUnifiedLoopPayload(&wisdev.LoopResult{
			FinalAnswer: "  final answer  ",
			Papers:      []search.Paper{{ID: "paper-1", Title: "Paper 1"}},
			Evidence:    []wisdev.EvidenceFinding{{ID: "e1", Claim: "c1", Confidence: 0.9}},
			Iterations:  3,
			ExecutedQueries: []string{
				"final answer replication benchmark",
			},
			BranchPlans: []wisdev.ResearchBranchPlan{{
				ID:                "branch-replication",
				Query:             "final answer replication benchmark",
				Hypothesis:        "Replication evidence should support or falsify the synthesis.",
				ReasoningStrategy: "replication_first",
			}},
			Hypotheses: []wisdev.Hypothesis{{
				ID:                      "hyp-replication",
				Claim:                   "Replication evidence should support or falsify the synthesis.",
				FalsifiabilityCondition: "failed replication invalidates the branch",
				ConfidenceScore:         0.62,
			}},
			GapAnalysis: &wisdev.LoopGapState{
				Reasoning: "Need interventional evidence.",
				NextQueries: []string{
					"interventional replication evidence",
				},
				Ledger: []wisdev.CoverageLedgerEntry{
					{ID: "gap-1", Title: "Need interventional evidence", Status: "open"},
				},
			},
			DraftCritique: &wisdev.LoopDraftCritique{
				NeedsRevision:           true,
				RetrievalReopened:       true,
				AdditionalEvidenceFound: true,
				Reasoning:               "The draft still needed interventional evidence.",
			},
			ReasoningTrace: []wisdev.ReasoningTraceEntry{{
				Phase:     "synthesis",
				Decision:  "post_critique_review",
				Reasoning: "The revised draft still needs interventional replication before promotion.",
			}},
			RuntimeState: &wisdev.ResearchSessionState{
				SessionID: "runtime-session",
				Query:     "final answer",
				Plane:     wisdev.ResearchExecutionPlaneDeep,
				Budget: &wisdev.ResearchBudgetDecision{
					WorkerSearchBudget:   6,
					FollowUpSearchBudget: 4,
				},
				StopReason: "coverage_open",
			},
			Mode: wisdev.WisDevModeYOLO,
		})
		if assert.NotNil(t, payload) {
			assert.Equal(t, "Provisional answer: final answer", payload["finalAnswer"])
			assert.Equal(t, 3, payload["iterations_used"])
			assert.True(t, payload["converged"] == false)
			assert.NotEmpty(t, payload["papers"])
			assert.NotEmpty(t, payload["evidence"])
			assert.Equal(t, []string{"final answer replication benchmark"}, payload["executedQueries"])
			assert.Equal(t, []string{"final answer replication benchmark"}, payload["plannedQueries"])
			assert.NotEmpty(t, payload["branchPlans"])
			assert.NotEmpty(t, payload["hypotheses"])
			assert.NotNil(t, payload["gapAnalysis"])
			assert.NotNil(t, payload["draftCritique"])
			assert.NotEmpty(t, payload["reasoningTrace"])
			review, ok := payload["autonomousReview"].(map[string]any)
			if assert.True(t, ok, "expected autonomous review metadata") {
				assert.Equal(t, "revision_required", review["status"])
				assert.Equal(t, true, review["postCritiqueReview"])
			}
			assert.NotEmpty(t, payload["coverageLedger"])
			assert.Equal(t, "revise_required", payload["answerStatus"])
			assert.Equal(t, true, payload["blockingFinalization"])
			assert.Equal(t, "coverage_open", payload["stopReason"])
			assert.Equal(t, 1, payload["openLedgerCount"])
			assert.Equal(t, []string{"interventional replication evidence"}, payload["followUpQueries"])
			reasoningRuntime, ok := payload["reasoningRuntime"].(map[string]any)
			if assert.True(t, ok, "expected reasoning runtime metadata") {
				assert.Equal(t, "tree_search_with_programmatic_planner", reasoningRuntime["runtimeMode"])
				assert.Equal(t, true, reasoningRuntime["treeSearchRuntime"])
				assert.Equal(t, true, reasoningRuntime["programmaticTreePlanner"])
				assert.Equal(t, wisdev.WisDevModeYOLO, reasoningRuntime["mode"])
			}
			runtimeState, ok := payload["runtimeState"].(map[string]any)
			if assert.True(t, ok, "expected runtime state metadata") {
				assert.Equal(t, "runtime-session", runtimeState["sessionId"])
				assert.Equal(t, wisdev.ResearchExecutionPlaneDeep, runtimeState["plane"])
				assert.NotNil(t, runtimeState["reasoningRuntime"])
			}
			gate, ok := payload["finalizationGate"].(*wisdev.ResearchFinalizationGate)
			if assert.True(t, ok, "expected synthesized finalization gate") {
				assert.Equal(t, "revise_required", gate.Status)
				assert.True(t, gate.Provisional)
				assert.Equal(t, 1, gate.OpenLedgerCount)
				assert.Equal(t, "coverage_open", gate.StopReason)
			}
		}
	})

	t.Run("buildUnifiedLoopPayload exposes streamed job reasoning runtime", func(t *testing.T) {
		payload := buildUnifiedLoopPayload(&wisdev.LoopResult{
			FinalAnswer: "streamed answer",
			Papers:      []search.Paper{{ID: "paper-job", Title: "Job Paper"}},
			RuntimeState: &wisdev.ResearchSessionState{
				SessionID: "job-runtime-session",
				Query:     "streamed yolo",
				Plane:     wisdev.ResearchExecutionPlaneJob,
				Budget: &wisdev.ResearchBudgetDecision{
					WorkerSearchBudget:   4,
					FollowUpSearchBudget: 3,
				},
			},
			Mode: wisdev.WisDevModeYOLO,
		})

		reasoningRuntime, ok := payload["reasoningRuntime"].(map[string]any)
		if assert.True(t, ok, "expected streamed job reasoning metadata") {
			assert.Equal(t, "tree_search_with_programmatic_planner", reasoningRuntime["runtimeMode"])
			assert.Equal(t, true, reasoningRuntime["treeSearchRuntime"])
			assert.Equal(t, true, reasoningRuntime["programmaticTreePlanner"])
			assert.Equal(t, wisdev.ResearchExecutionPlaneJob, reasoningRuntime["researchPlane"])
		}
	})

	t.Run("serializeLoopPapers", func(t *testing.T) {
		out := serializeLoopPapers([]search.Paper{
			{
				ID:            "sparse",
				Title:         "Sparse",
				Month:         13,
				Source:        "source-a",
				Authors:       []string{"", "Ada"},
				Keywords:      []string{"k1"},
				Year:          2022,
				Venue:         "Venue A",
				Link:          "https://example.com/a",
				DOI:           "10.1/a",
				PdfUrl:        "",
				OpenAccessUrl: "",
			},
			{
				ID:                       "rich",
				Title:                    "Rich",
				Month:                    6,
				Source:                   "source-b",
				Authors:                  []string{"Bob"},
				Keywords:                 []string{"k2"},
				Year:                     2023,
				Venue:                    "Venue B",
				ReferenceCount:           7,
				InfluentialCitationCount: 2,
				OpenAccessUrl:            "https://oa.example.com/rich",
				PdfUrl:                   "https://pdf.example.com/rich",
			},
		})
		if assert.Len(t, out, 2) {
			assert.NotContains(t, out[0]["publishDate"].(map[string]any), "month")
			assert.Equal(t, "Ada", out[0]["authors"].([]map[string]any)[0]["name"])
			assert.Equal(t, 7, out[1]["referenceCount"])
			assert.Equal(t, 2, out[1]["influentialCitationCount"])
			assert.Equal(t, "https://oa.example.com/rich", out[1]["openAccessUrl"])
			assert.Equal(t, "https://pdf.example.com/rich", out[1]["pdfUrl"])
		}
	})

	t.Run("WisDevStreamHandler no flusher", func(t *testing.T) {
		jobID := "no-flush-wisdev"
		yoloJobStore.put(ownedWisDevJobForTest(&YoloJob{ID: jobID, UnifiedEvents: make(chan UnifiedEvent, 1)}))
		t.Cleanup(func() { yoloJobStore.delete(jobID) })
		req := httptest.NewRequest(http.MethodGet, "/wisdev/job/"+jobID+"/stream", nil)
		req = withWisDevJobTestUser(req)
		rec := &noFlushRecorder{}
		WisDevStreamHandler(rec, req)
		assert.Equal(t, http.StatusInternalServerError, rec.status)
	})

	t.Run("WisDevStreamHandler method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/wisdev/job/any/stream", nil)
		rec := httptest.NewRecorder()
		WisDevStreamHandler(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("WisDevStreamHandler context done", func(t *testing.T) {
		jobID := "ctx-done-wisdev"
		yoloJobStore.put(ownedWisDevJobForTest(&YoloJob{ID: jobID, UnifiedEvents: make(chan UnifiedEvent, 1)}))
		t.Cleanup(func() { yoloJobStore.delete(jobID) })
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		req := httptest.NewRequest(http.MethodGet, "/wisdev/job/"+jobID+"/stream", nil).WithContext(ctx)
		req = withWisDevJobTestUser(req)
		rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
		WisDevStreamHandler(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("WisDevStreamHandler query fallback and closed channel", func(t *testing.T) {
		jobID := "query-fallback-wisdev"
		events := make(chan UnifiedEvent)
		close(events)
		yoloJobStore.put(ownedWisDevJobForTest(&YoloJob{ID: jobID, UnifiedEvents: events}))
		t.Cleanup(func() { yoloJobStore.delete(jobID) })
		req := httptest.NewRequest(http.MethodGet, "/wisdev/job//stream?job_id="+jobID, nil)
		req = withWisDevJobTestUser(req)
		rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
		WisDevStreamHandler(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), `"type":"job_failed"`)
		assert.Contains(t, rec.Body.String(), `"code":"JOB_STREAM_CLOSED"`)
		assert.Contains(t, rec.Body.String(), `"synthetic":true`)
		stored, ok := yoloJobStore.get(jobID)
		if assert.True(t, ok) {
			assert.Equal(t, "failed", stored.statusSnapshot())
		}
	})

	t.Run("WisDevStreamHandler closed channel replays recorded terminal event", func(t *testing.T) {
		jobID := "recorded-terminal-wisdev"
		events := make(chan UnifiedEvent)
		close(events)
		job := &YoloJob{ID: jobID, TraceID: "trace-recorded-wisdev", UnifiedEvents: events}
		ownedWisDevJobForTest(job)
		job.setTerminalUnifiedEvent(UnifiedEvent{Type: "job_done", JobID: jobID, TraceID: "trace-recorded-wisdev", Message: "completed earlier"})
		yoloJobStore.put(job)
		t.Cleanup(func() { yoloJobStore.delete(jobID) })
		req := httptest.NewRequest(http.MethodGet, "/wisdev/job/"+jobID+"/stream", nil)
		req = withWisDevJobTestUser(req)
		rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
		WisDevStreamHandler(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), `"type":"job_done"`)
		assert.Contains(t, rec.Body.String(), `"synthetic":true`)
		assert.Contains(t, rec.Body.String(), `"traceId":"trace-recorded-wisdev"`)
	})

	t.Run("WisDevStreamHandler terminal event stays replayable through status and a second stream", func(t *testing.T) {
		jobID := "replayable-terminal-wisdev"
		events := make(chan UnifiedEvent, 1)
		events <- UnifiedEvent{Type: "job_done", TraceID: "trace-replayable-wisdev", Message: "done"}
		close(events)
		yoloJobStore.put(ownedWisDevJobForTest(&YoloJob{ID: jobID, TraceID: "trace-replayable-wisdev", Status: "running", UnifiedEvents: events, CreatedAt: time.UnixMilli(100)}))
		t.Cleanup(func() { yoloJobStore.delete(jobID) })

		firstReq := httptest.NewRequest(http.MethodGet, "/wisdev/job/"+jobID+"/stream", nil)
		firstReq = withWisDevJobTestUser(firstReq)
		firstRec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
		WisDevStreamHandler(firstRec, firstReq)
		assert.Equal(t, http.StatusOK, firstRec.Code)
		assert.Contains(t, firstRec.Body.String(), `"type":"job_done"`)

		statusReq := httptest.NewRequest(http.MethodGet, "/wisdev/job/"+jobID, nil)
		statusReq = withWisDevJobTestUser(statusReq)
		statusRec := httptest.NewRecorder()
		WisDevJobStatusHandler(statusRec, statusReq)
		assert.Equal(t, http.StatusOK, statusRec.Code)
		assert.Contains(t, statusRec.Body.String(), `"status":"completed"`)

		secondReq := httptest.NewRequest(http.MethodGet, "/wisdev/job/"+jobID+"/stream", nil)
		secondReq = withWisDevJobTestUser(secondReq)
		secondRec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
		WisDevStreamHandler(secondRec, secondReq)
		assert.Equal(t, http.StatusOK, secondRec.Code)
		assert.Contains(t, secondRec.Body.String(), `"type":"job_done"`)
		assert.Contains(t, secondRec.Body.String(), `"synthetic":true`)
	})

	t.Run("WisDevStreamHandler replays journal terminal event after in-memory job loss", func(t *testing.T) {
		t.Setenv("WISDEV_JOURNAL_PATH", filepath.Join(t.TempDir(), "wisdev_journal.jsonl"))
		gateway := &wisdev.AgentGateway{Journal: wisdev.NewRuntimeJournal(nil)}
		previousGateway := GlobalYoloGateway
		GlobalYoloGateway = gateway
		t.Cleanup(func() {
			GlobalYoloGateway = previousGateway
		})

		jobID := "journal-replay-terminal"
		job := &YoloJob{
			ID:        jobID,
			TraceID:   "trace-journal-terminal",
			UserID:    testWisDevJobUserID,
			Query:     "RLHF reinforcement learning",
			ProjectID: "",
			Mode:      "guided",
			CreatedAt: time.UnixMilli(123),
		}
		appendWisDevJobRegistrationJournalEvent(job)
		appendUnifiedAutonomousJournalEvent(job, UnifiedEvent{
			Type:        "job_done",
			JobID:       jobID,
			TraceID:     "trace-journal-terminal",
			Mode:        "guided",
			Message:     "autonomous loop completed",
			ServiceTier: "priority",
			Payload: map[string]any{
				"papers": []map[string]any{{"id": "p1", "title": "Recovered paper"}},
				"reasoningRuntime": map[string]any{
					"runtimeMode":             "tree_search_with_programmatic_planner",
					"treeSearchRuntime":       true,
					"programmaticTreePlanner": true,
				},
			},
		})

		req := httptest.NewRequest(http.MethodGet, "/wisdev/job/"+jobID+"/stream", nil)
		req = withWisDevJobTestUser(req)
		rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
		WisDevStreamHandler(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), `"type":"job_done"`)
		assert.Contains(t, rec.Body.String(), `"traceId":"trace-journal-terminal"`)
		assert.Contains(t, rec.Body.String(), `"serviceTier":"priority"`)
		assert.Contains(t, rec.Body.String(), `"reasoningRuntime"`)
		assert.Contains(t, rec.Body.String(), `"runtimeMode":"tree_search_with_programmatic_planner"`)
	})

	t.Run("WisDevStreamHandler synthesizes lost-state failure from journal registration after restart", func(t *testing.T) {
		t.Setenv("WISDEV_JOURNAL_PATH", filepath.Join(t.TempDir(), "wisdev_journal.jsonl"))
		gateway := &wisdev.AgentGateway{Journal: wisdev.NewRuntimeJournal(nil)}
		previousGateway := GlobalYoloGateway
		GlobalYoloGateway = gateway
		t.Cleanup(func() {
			GlobalYoloGateway = previousGateway
		})

		jobID := "journal-lost-state"
		job := &YoloJob{
			ID:        jobID,
			TraceID:   "trace-journal-lost",
			UserID:    testWisDevJobUserID,
			Query:     "RLHF reinforcement learning",
			ProjectID: "",
			Mode:      "guided",
			CreatedAt: time.UnixMilli(456),
		}
		appendWisDevJobRegistrationJournalEvent(job)

		req := httptest.NewRequest(http.MethodGet, "/wisdev/job/"+jobID+"/stream", nil)
		req = withWisDevJobTestUser(req)
		rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
		WisDevStreamHandler(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), `"type":"job_failed"`)
		assert.Contains(t, rec.Body.String(), `"code":"JOB_STATE_LOST"`)
		assert.Contains(t, rec.Body.String(), `"traceId":"trace-journal-lost"`)
	})

	t.Run("WisDevStreamHandler job_failed retains terminal job status", func(t *testing.T) {
		jobID := "failed-wisdev"
		events := make(chan UnifiedEvent, 1)
		events <- UnifiedEvent{Type: "job_failed", Message: "boom"}
		close(events)
		yoloJobStore.put(ownedWisDevJobForTest(&YoloJob{ID: jobID, UnifiedEvents: events}))
		t.Cleanup(func() { yoloJobStore.delete(jobID) })
		req := httptest.NewRequest(http.MethodGet, "/wisdev/job/"+jobID+"/stream", nil)
		req = withWisDevJobTestUser(req)
		rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
		WisDevStreamHandler(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		stored, ok := yoloJobStore.get(jobID)
		if assert.True(t, ok) {
			assert.Equal(t, "failed", stored.statusSnapshot())
		}
	})

	t.Run("WisDevStreamHandler keepalive", func(t *testing.T) {
		jobID := "keepalive-wisdev"
		yoloJobStore.put(ownedWisDevJobForTest(&YoloJob{ID: jobID, UnifiedEvents: make(chan UnifiedEvent, 1)}))
		t.Cleanup(func() { yoloJobStore.delete(jobID) })
		origTicker := newStreamTicker
		newStreamTicker = func(d time.Duration) streamTicker {
			ch := make(chan time.Time, 1)
			ch <- time.Unix(0, 0)
			return streamTicker{C: ch, Stop: func() {}}
		}
		t.Cleanup(func() { newStreamTicker = origTicker })
		ctx, cancel := context.WithCancel(context.Background())
		req := httptest.NewRequest(http.MethodGet, "/wisdev/job/"+jobID+"/stream", nil).WithContext(ctx)
		req = withWisDevJobTestUser(req)
		rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
		done := make(chan struct{})
		go func() {
			WisDevStreamHandler(rec, req)
			close(done)
		}()
		time.Sleep(50 * time.Millisecond)
		cancel()
		<-done
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), ": keep-alive")
	})

	t.Run("WisDevScheduleHandler db error", func(t *testing.T) {
		handler := NewWisDevHandler(nil, nil, nil, &wisdev.AgentGateway{DB: yoloScheduleDBStub{execErr: errors.New("boom")}}, nil, nil, nil)
		req := httptest.NewRequest(http.MethodPost, "/schedule", bytes.NewBufferString(`{"project_id":"p1","schedule":"* * * * *","query":"q"}`))
		rec := httptest.NewRecorder()
		handler.WisDevScheduleHandler(rec, req)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("WisDevScheduleRunHandler missing id", func(t *testing.T) {
		handler := NewWisDevHandler(nil, nil, nil, nil, nil, nil, nil)
		req := httptest.NewRequest(http.MethodPost, "/wisdev/schedule/run/", nil)
		rec := httptest.NewRecorder()
		handler.WisDevScheduleRunHandler(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("WisDevScheduleRunHandler db fallback", func(t *testing.T) {
		handler := NewWisDevHandler(nil, nil, nil, &wisdev.AgentGateway{DB: yoloScheduleDBStub{queryRowErr: errors.New("boom")}}, nil, nil, nil)
		req := httptest.NewRequest(http.MethodPost, "/wisdev/schedule/run/sched-1", nil)
		rec := httptest.NewRecorder()
		handler.WisDevScheduleRunHandler(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		jobID, _ := resp["job_id"].(string)
		assert.NotEmpty(t, jobID)
		assert.Equal(t, "started", resp["status"])
		waitForUnifiedJobCompletion(t, jobID)
	})

	t.Run("WisDevJobStatusHandler method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/wisdev/job/status", nil)
		rec := httptest.NewRecorder()
		WisDevJobStatusHandler(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("WisDevJobStatusHandler recovers failed status from journal registration after in-memory loss", func(t *testing.T) {
		t.Setenv("WISDEV_JOURNAL_PATH", filepath.Join(t.TempDir(), "wisdev_journal.jsonl"))
		gateway := &wisdev.AgentGateway{Journal: wisdev.NewRuntimeJournal(nil)}
		previousGateway := GlobalYoloGateway
		GlobalYoloGateway = gateway
		t.Cleanup(func() {
			GlobalYoloGateway = previousGateway
		})

		jobID := "journal-status-recovery"
		job := &YoloJob{
			ID:        jobID,
			TraceID:   "trace-journal-status",
			UserID:    testWisDevJobUserID,
			Query:     "RLHF reinforcement learning",
			ProjectID: "",
			Mode:      "guided",
			CreatedAt: time.UnixMilli(789),
		}
		appendWisDevJobRegistrationJournalEvent(job)

		req := httptest.NewRequest(http.MethodGet, "/wisdev/job/"+jobID, nil)
		req = withWisDevJobTestUser(req)
		rec := httptest.NewRecorder()
		WisDevJobStatusHandler(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), `"job_id":"journal-status-recovery"`)
		assert.Contains(t, rec.Body.String(), `"status":"failed"`)
		assert.Contains(t, rec.Body.String(), `"traceId":"trace-journal-status"`)
		assert.Contains(t, rec.Body.String(), `"mode":"guided"`)
	})

	t.Run("WisDevJobStatusHandler recovers persisted durable research job", func(t *testing.T) {
		t.Setenv("WISDEV_STATE_DIR", t.TempDir())
		t.Setenv("WISDEV_JOURNAL_PATH", filepath.Join(t.TempDir(), "wisdev_journal.jsonl"))
		gateway := &wisdev.AgentGateway{
			Journal:    wisdev.NewRuntimeJournal(nil),
			StateStore: wisdev.NewRuntimeStateStore(nil, nil),
		}
		previousGateway := GlobalYoloGateway
		GlobalYoloGateway = gateway
		t.Cleanup(func() {
			GlobalYoloGateway = previousGateway
		})

		jobID := "research-durable-status"
		assert.NoError(t, gateway.StateStore.SaveResearchJob(jobID, map[string]any{
			"jobId":                 jobID,
			"sessionId":             "session-durable-status",
			"traceId":               "trace-durable-status",
			"userId":                testWisDevJobUserID,
			"query":                 "durable status query",
			"status":                "completed",
			"plane":                 "deep",
			"mode":                  "yolo",
			"startedAt":             int64(1000),
			"updatedAt":             int64(2000),
			"replayable":            true,
			"resumeSupported":       true,
			"cancellationSupported": true,
			"budgetUsed":            map[string]any{"executedQueries": float64(2)},
		}))

		req := httptest.NewRequest(http.MethodGet, "/wisdev/job/"+jobID, nil)
		req = withWisDevJobTestUser(req)
		rec := httptest.NewRecorder()
		WisDevJobStatusHandler(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "trace-durable-status", rec.Header().Get("X-Trace-Id"))
		assert.Contains(t, rec.Body.String(), `"job_id":"research-durable-status"`)
		assert.Contains(t, rec.Body.String(), `"status":"completed"`)
		assert.Contains(t, rec.Body.String(), `"durableJob"`)
		assert.Contains(t, rec.Body.String(), `"budgetUsed"`)
		assert.Contains(t, rec.Body.String(), `"resumable":true`)
		assert.Contains(t, rec.Body.String(), `"resumeSupported":true`)
		assert.Contains(t, rec.Body.String(), `"runtimeState"`)
		assert.Contains(t, rec.Body.String(), `"reasoningRuntime"`)
		assert.Contains(t, rec.Body.String(), `"runtimeMode":"tree_search_with_programmatic_planner"`)
	})

	t.Run("WisDevStreamHandler replays persisted durable research job snapshot", func(t *testing.T) {
		t.Setenv("WISDEV_STATE_DIR", t.TempDir())
		gateway := &wisdev.AgentGateway{StateStore: wisdev.NewRuntimeStateStore(nil, nil)}
		previousGateway := GlobalYoloGateway
		GlobalYoloGateway = gateway
		t.Cleanup(func() {
			GlobalYoloGateway = previousGateway
		})

		jobID := "research-durable-stream"
		assert.NoError(t, gateway.StateStore.SaveResearchJob(jobID, map[string]any{
			"jobId":     jobID,
			"sessionId": "session-durable-stream",
			"traceId":   "trace-durable-stream",
			"userId":    testWisDevJobUserID,
			"query":     "durable stream query",
			"status":    "completed",
			"plane":     "deep",
			"mode":      "yolo",
			"updatedAt": int64(3000),
		}))

		req := httptest.NewRequest(http.MethodGet, "/wisdev/job/"+jobID+"/stream", nil)
		req = withWisDevJobTestUser(req)
		rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
		WisDevStreamHandler(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "trace-durable-stream", rec.Header().Get("X-Trace-Id"))
		assert.Contains(t, rec.Body.String(), `"type":"job_done"`)
		assert.Contains(t, rec.Body.String(), `"durableJob"`)
		assert.Contains(t, rec.Body.String(), `"replayed":true`)
		assert.Contains(t, rec.Body.String(), `"runtimeState"`)
		assert.Contains(t, rec.Body.String(), `"reasoningRuntime"`)
		assert.Contains(t, rec.Body.String(), `"runtimeMode":"tree_search_with_programmatic_planner"`)
	})

	t.Run("WisDevJobCancelHandler cancels persisted durable research job through canonical route", func(t *testing.T) {
		t.Setenv("WISDEV_STATE_DIR", t.TempDir())
		t.Setenv("WISDEV_JOURNAL_PATH", filepath.Join(t.TempDir(), "wisdev_journal.jsonl"))
		gateway := &wisdev.AgentGateway{
			Journal:    wisdev.NewRuntimeJournal(nil),
			StateStore: wisdev.NewRuntimeStateStore(nil, nil),
		}
		previousGateway := GlobalYoloGateway
		GlobalYoloGateway = gateway
		t.Cleanup(func() {
			GlobalYoloGateway = previousGateway
		})

		jobID := "research-durable-cancel"
		assert.NoError(t, gateway.StateStore.SaveResearchJob(jobID, map[string]any{
			"jobId":     jobID,
			"sessionId": "session-durable-cancel",
			"traceId":   "trace-durable-cancel",
			"userId":    testWisDevJobUserID,
			"query":     "durable cancel query",
			"status":    "running",
			"plane":     "deep",
			"mode":      "yolo",
			"updatedAt": int64(4000),
		}))

		mux := http.NewServeMux()
		registerWisDevJobRoutes(mux)
		req := httptest.NewRequest(http.MethodPost, "/wisdev/job/"+jobID+"/cancel", nil)
		req = withWisDevJobTestUser(req)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "trace-durable-cancel", rec.Header().Get("X-Trace-Id"))
		assert.Contains(t, rec.Body.String(), `"cancelled":true`)
		assert.Contains(t, rec.Body.String(), `"runtimeState"`)
		assert.Contains(t, rec.Body.String(), `"reasoningRuntime"`)
		assert.Contains(t, rec.Body.String(), `"runtimeMode":"tree_search_with_programmatic_planner"`)
		loaded, err := gateway.StateStore.LoadResearchJob(jobID)
		assert.NoError(t, err)
		assert.Equal(t, "cancelled", wisdev.AsOptionalString(loaded["status"]))
		entries := gateway.Journal.ReadJob(jobID, 10)
		if assert.NotEmpty(t, entries) {
			last := entries[len(entries)-1]
			assert.Equal(t, "job_cancelled", last.EventType)
			assert.Equal(t, "wisdev_research_job", last.Metadata["source"])
			assert.NotNil(t, last.Payload["runtimeState"])
			assert.NotNil(t, last.Payload["reasoningRuntime"])
		}
	})

	t.Run("WisDevJobHandler method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wisdev/job", nil)
		rec := httptest.NewRecorder()
		WisDevJobHandler(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})
}
