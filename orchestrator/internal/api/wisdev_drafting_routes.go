package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

func (s *wisdevServer) registerDraftingRoutes(mux *http.ServeMux, agentGateway *wisdev.AgentGateway) {
	mux.HandleFunc("/drafting/outline", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			DocumentID        string   `json:"documentId"`
			Title             string   `json:"title"`
			TargetWordCount   int      `json:"targetWordCount"`
			CustomSections    []string `json:"customSections"`
			UserID            string   `json:"userId"`
			ExpectedUpdatedAt int64    `json:"expectedUpdatedAt"`
			TraceID           string   `json:"traceId,omitempty"`
			LegacyTraceID     string   `json:"trace_id,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		if strings.TrimSpace(req.DocumentID) == "" || strings.TrimSpace(req.Title) == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "documentId and title are required", map[string]any{
				"requiredFields": []string{"documentId", "title"},
			})
			return
		}
		if err := validateRequiredString(req.DocumentID, "documentId", 128); err != nil {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, err.Error(), map[string]any{
				"field": "documentId",
			})
			return
		}
		if err := validateRequiredString(req.Title, "title", 300); err != nil {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, err.Error(), map[string]any{
				"field": "title",
			})
			return
		}
		if err := validateStringSlice(req.CustomSections, "customSections", 12, 160); err != nil {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, err.Error(), map[string]any{
				"field": "customSections",
			})
			return
		}
		traceID := resolveWisdevRouteOptionalTraceID(r, req.TraceID, req.LegacyTraceID)
		job, ok := loadOwnedFullPaperJobState(w, r, agentGateway, req.DocumentID)
		if !ok {
			return
		}
		idempotencyKey := makeDraftOutlineIdempotencyKey(req.DocumentID, req.Title, req.TargetWordCount, req.CustomSections, req.ExpectedUpdatedAt)
		if status, cached, ok := enforceIdempotency(r, agentGateway, idempotencyKey); ok {
			writeCachedWisdevEnvelopeResponse(w, status, cached)
			return
		}
		if !assertExpectedUpdatedAt(w, req.ExpectedUpdatedAt, job) {
			return
		}
		if fullPaperHasTerminalStatus(wisdev.AsOptionalString(job["status"])) {
			WriteError(w, http.StatusConflict, ErrInvalidParameters, "cannot update outline for terminal full-paper job", map[string]any{
				"documentId": req.DocumentID,
			})
			return
		}
		payload := buildDraftOutlinePayload(req.DocumentID, req.Title, req.TargetWordCount, req.CustomSections)
		if err := upsertDraftingState(agentGateway, req.DocumentID, payload, "", nil); err != nil {
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to persist drafting outline", map[string]any{
				"error":      err.Error(),
				"documentId": req.DocumentID,
			})
			return
		}
		if job, err := loadFullPaperJobState(agentGateway, req.DocumentID); err == nil {
			job["currentStageId"] = "drafting"
			job["currentStage"] = "drafting"
			previousUpdatedAt := wisdev.IntValue64(job["updatedAt"])
			nextUpdatedAt := time.Now().UnixMilli()
			if nextUpdatedAt <= previousUpdatedAt {
				nextUpdatedAt = previousUpdatedAt + 1
			}
			job["updatedAt"] = nextUpdatedAt
			stages := sliceAnyMap(job["stages"])
			for index, stage := range stages {
				switch wisdev.AsOptionalString(stage["id"]) {
				case "evidence_dossier":
					stage["status"] = "completed"
					stage["completion"] = 100
				case "drafting":
					stage["status"] = "running"
					stage["completion"] = 35
				}
				stages[index] = stage
			}
			job["stages"] = stages
			if err := saveFullPaperJobState(agentGateway, job); err != nil {
				WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to persist full paper state", map[string]any{
					"error":      err.Error(),
					"documentId": req.DocumentID,
				})
				return
			}
		}
		w.Header().Set("X-Trace-Id", traceID)
		traceID = writeEnvelopeWithTraceID(w, traceID, "draftOutline", payload)
		body, _ := json.Marshal(buildEnvelopeBody(traceID, "draftOutline", payload))
		storeIdempotentResponse(agentGateway, r, idempotencyKey, body)
		s.journalEvent("draft_outline", "/drafting/outline", traceID, "", wisdev.AsOptionalString(job["userId"]), "", "", "Draft outline generated.", payload, nil)
	})

	mux.HandleFunc("/drafting/section", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			DocumentID        string           `json:"documentId"`
			SectionID         string           `json:"sectionId"`
			SectionTitle      string           `json:"sectionTitle"`
			TargetWords       int              `json:"targetWords"`
			Papers            []map[string]any `json:"papers"`
			UserID            string           `json:"userId"`
			ExpectedUpdatedAt int64            `json:"expectedUpdatedAt"`
			TraceID           string           `json:"traceId,omitempty"`
			LegacyTraceID     string           `json:"trace_id,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		if strings.TrimSpace(req.DocumentID) == "" || strings.TrimSpace(req.SectionID) == "" || strings.TrimSpace(req.SectionTitle) == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "documentId, sectionId, and sectionTitle are required", map[string]any{
				"requiredFields": []string{"documentId", "sectionId", "sectionTitle"},
			})
			return
		}
		if err := validateRequiredString(req.DocumentID, "documentId", 128); err != nil {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, err.Error(), map[string]any{
				"field": "documentId",
			})
			return
		}
		if err := validateRequiredString(req.SectionID, "sectionId", 80); err != nil {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, err.Error(), map[string]any{
				"field": "sectionId",
			})
			return
		}
		if err := validateRequiredString(req.SectionTitle, "sectionTitle", 240); err != nil {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, err.Error(), map[string]any{
				"field": "sectionTitle",
			})
			return
		}
		if err := validatePayloadSize(req.Papers, "papers", 128*1024); err != nil {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, err.Error(), map[string]any{
				"field": "papers",
			})
			return
		}
		traceID := resolveWisdevRouteOptionalTraceID(r, req.TraceID, req.LegacyTraceID)
		job, ok := loadOwnedFullPaperJobState(w, r, agentGateway, req.DocumentID)
		if !ok {
			return
		}
		idempotencyKey := makeDraftSectionIdempotencyKey(req.DocumentID, req.SectionID, req.SectionTitle, req.TargetWords, req.Papers, req.ExpectedUpdatedAt)
		if status, cached, ok := enforceIdempotency(r, agentGateway, idempotencyKey); ok {
			writeCachedWisdevEnvelopeResponse(w, status, cached)
			return
		}
		if !assertExpectedUpdatedAt(w, req.ExpectedUpdatedAt, job) {
			return
		}
		if fullPaperHasTerminalStatus(wisdev.AsOptionalString(job["status"])) {
			WriteError(w, http.StatusConflict, ErrInvalidParameters, "cannot update section for terminal full-paper job", map[string]any{
				"documentId": req.DocumentID,
				"sectionId":  req.SectionID,
			})
			return
		}
		payload := buildDraftSectionPayload(req.DocumentID, req.SectionID, req.SectionTitle, req.TargetWords, req.Papers)
		if err := upsertDraftingState(agentGateway, req.DocumentID, nil, req.SectionID, payload); err != nil {
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to persist drafting section", map[string]any{
				"error":      err.Error(),
				"documentId": req.DocumentID,
				"sectionId":  req.SectionID,
			})
			return
		}
		if job, err := loadFullPaperJobState(agentGateway, req.DocumentID); err == nil {
			job["currentStageId"] = "drafting"
			job["currentStage"] = "drafting"
			job["updatedAt"] = time.Now().UnixMilli()
			workspace := mapAny(job["workspace"])
			drafting := mapAny(workspace["drafting"])
			sectionOrder := sliceStrings(drafting["sectionOrder"])
			if len(sectionOrder) >= 3 {
				stages := sliceAnyMap(job["stages"])
				for index, stage := range stages {
					switch wisdev.AsOptionalString(stage["id"]) {
					case "drafting":
						stage["status"] = "completed"
						stage["completion"] = 100
					case "review":
						stage["status"] = "running"
						stage["completion"] = 20
					}
					stages[index] = stage
				}
				job["stages"] = stages
			}
			if err := saveFullPaperJobState(agentGateway, job); err != nil {
				WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to persist full paper state", map[string]any{
					"error":      err.Error(),
					"documentId": req.DocumentID,
				})
				return
			}
		}
		w.Header().Set("X-Trace-Id", traceID)
		traceID = writeEnvelopeWithTraceID(w, traceID, "draftSection", payload)
		body, _ := json.Marshal(buildEnvelopeBody(traceID, "draftSection", payload))
		storeIdempotentResponse(agentGateway, r, idempotencyKey, body)
		s.journalEvent("draft_section", "/drafting/section", traceID, "", wisdev.AsOptionalString(job["userId"]), "", req.SectionID, "Draft section generated.", payload, nil)
	})

	mux.HandleFunc("/manuscript/draft", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		s.HandleManuscriptDraft(w, r)
	})

	mux.HandleFunc("/manuscript/draft/stream", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		s.HandleManuscriptDraftStream(w, r)
	})

	mux.HandleFunc("/reviewer/rebuttal", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		s.HandleReviewerRebuttal(w, r)
	})

	mux.HandleFunc("/reviewer/rebuttal/stream", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		s.HandleReviewerRebuttalStream(w, r)
	})
}
