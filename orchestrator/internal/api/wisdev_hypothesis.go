package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

type wisdevHypothesisSnapshotEnvelope struct {
	QuestID    string                  `json:"questId"`
	SessionID  string                  `json:"sessionId"`
	Status     string                  `json:"status"`
	Hypotheses []*wisdev.Hypothesis    `json:"hypotheses"`
	Synthesis  *wisdev.SynthesisResult `json:"synthesis,omitempty"`
}

type wisdevHypothesisUpdateEnvelope struct {
	Message         string                  `json:"message"`
	QuestID         string                  `json:"questId"`
	SessionID       string                  `json:"sessionId"`
	Status          string                  `json:"status"`
	HypothesisID    string                  `json:"hypothesisId"`
	Hypothesis      *wisdev.Hypothesis      `json:"hypothesis,omitempty"`
	Dossier         *wisdev.EvidenceDossier `json:"dossier,omitempty"`
	Converged       bool                    `json:"converged,omitempty"`
	PapersFound     int                     `json:"papersFound,omitempty"`
	AcceptedClaimsN int                     `json:"acceptedClaimsN,omitempty"`
}

func writeWisdevHypothesisError(w http.ResponseWriter, traceID string, status int, code ErrorCode, message string, details map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	if trace := strings.TrimSpace(traceID); trace != "" {
		w.Header().Set("X-Trace-Id", trace)
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(APIError{
		OK:      false,
		TraceID: traceID,
		Error: ErrorDetail{
			Code:    code,
			Message: message,
			Status:  status,
			Details: details,
		},
	})
}

func requireWisdevHypothesisUser(w http.ResponseWriter, r *http.Request, traceID string) bool {
	userID := strings.TrimSpace(GetUserID(r))
	if userID == "" || userID == "anonymous" {
		writeWisdevHypothesisError(w, traceID, http.StatusUnauthorized, ErrUnauthorized, "missing authentication context from gateway", nil)
		return false
	}
	return true
}

func requireWisdevHypothesisAccess(w http.ResponseWriter, r *http.Request, traceID string, quest *wisdev.QuestState) bool {
	if !requireWisdevHypothesisUser(w, r, traceID) {
		return false
	}
	if quest == nil {
		return true
	}
	ownerID := strings.TrimSpace(quest.UserID)
	if ownerID == "" {
		userID := strings.TrimSpace(GetUserID(r))
		if userID == "admin" || userID == "internal-service" {
			return true
		}
		writeWisdevHypothesisError(w, traceID, http.StatusForbidden, ErrUnauthorized, "quest ownership missing; access denied", map[string]any{
			"questId": firstNonEmpty(quest.ID, quest.SessionID, quest.QuestID),
		})
		return false
	}
	userID := strings.TrimSpace(GetUserID(r))
	if userID == ownerID || userID == "admin" || userID == "internal-service" {
		return true
	}
	writeWisdevHypothesisError(w, traceID, http.StatusForbidden, ErrUnauthorized, "access denied to resource", map[string]any{
		"questId": firstNonEmpty(quest.ID, quest.SessionID, quest.QuestID),
	})
	return false
}

// HandleWisDevHypotheses multiplexes hypothesis management sub-paths.
func (h *WisDevHandler) HandleWisDevHypotheses(w http.ResponseWriter, r *http.Request) {
	traceID := resolveWisdevRouteTraceID(r, "")
	path := r.URL.Path
	// Expected formats:
	// /wisdev/hypothesis/{questId}/list
	// /wisdev/hypothesis/{questId}/refine/{hypId}
	// /wisdev/hypothesis/{questId}/accept/{hypId}

	parts := strings.Split(strings.TrimPrefix(path, "/wisdev/hypothesis/"), "/")
	if len(parts) < 2 {
		writeWisdevHypothesisError(w, traceID, http.StatusBadRequest, ErrBadRequest, "invalid hypothesis route", map[string]any{
			"path": path,
		})
		return
	}

	questID := strings.TrimSpace(parts[0])
	action := parts[1]
	if questID == "" {
		writeWisdevHypothesisError(w, traceID, http.StatusBadRequest, ErrInvalidParameters, "questId is required", nil)
		return
	}

	switch action {
	case "list":
		if r.Method != http.MethodGet {
			writeWisdevHypothesisError(w, traceID, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethods": []string{http.MethodGet},
			})
			return
		}
		if len(parts) != 2 {
			writeWisdevHypothesisError(w, traceID, http.StatusBadRequest, ErrBadRequest, "invalid hypothesis route", map[string]any{
				"path": path,
			})
			return
		}
		h.handleListHypotheses(w, r, questID)
	case "refine":
		if r.Method != http.MethodPost {
			writeWisdevHypothesisError(w, traceID, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethods": []string{http.MethodPost},
			})
			return
		}
		if len(parts) != 3 || strings.TrimSpace(parts[2]) == "" {
			writeWisdevHypothesisError(w, traceID, http.StatusBadRequest, ErrInvalidParameters, "hypothesisId is required", nil)
			return
		}
		h.handleRefineHypothesis(w, r, questID, strings.TrimSpace(parts[2]))
	case "accept":
		if r.Method != http.MethodPost {
			writeWisdevHypothesisError(w, traceID, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethods": []string{http.MethodPost},
			})
			return
		}
		if len(parts) != 3 || strings.TrimSpace(parts[2]) == "" {
			writeWisdevHypothesisError(w, traceID, http.StatusBadRequest, ErrInvalidParameters, "hypothesisId is required", nil)
			return
		}
		h.handleAcceptHypothesis(w, r, questID, strings.TrimSpace(parts[2]))
	default:
		writeWisdevHypothesisError(w, traceID, http.StatusNotFound, ErrNotFound, "unknown hypothesis action", map[string]any{
			"action": action,
			"path":   path,
		})
	}
}

func (h *WisDevHandler) handleListHypotheses(w http.ResponseWriter, r *http.Request, questID string) {
	traceID := resolveWisdevRouteTraceID(r, "")
	if h.gateway == nil || h.gateway.DB == nil {
		writeWisdevHypothesisError(w, traceID, http.StatusServiceUnavailable, ErrServiceUnavailable, "quest runtime unavailable", nil)
		return
	}

	store := wisdev.NewPostgresQuestStore(h.gateway.DB)
	quest, err := store.LoadQuestState(r.Context(), questID)
	if err != nil {
		writeWisdevHypothesisError(w, traceID, http.StatusInternalServerError, ErrInternal, "failed to load quest", map[string]any{
			"error":   err.Error(),
			"questId": questID,
		})
		return
	}

	if quest == nil {
		writeWisdevHypothesisError(w, traceID, http.StatusNotFound, ErrNotFound, "quest not found", map[string]any{
			"questId": questID,
		})
		return
	}
	if !requireWisdevHypothesisAccess(w, r, traceID, quest) {
		return
	}

	writeEnvelopeWithTraceID(w, traceID, "hypothesisSnapshot", wisdevHypothesisSnapshotEnvelope{
		QuestID:    firstNonEmpty(quest.ID, quest.SessionID, quest.QuestID, questID),
		SessionID:  firstNonEmpty(quest.SessionID, quest.ID, quest.QuestID, questID),
		Status:     string(quest.Status),
		Hypotheses: quest.Hypotheses,
		Synthesis:  quest.Synthesis,
	})
}

func (h *WisDevHandler) handleRefineHypothesis(w http.ResponseWriter, r *http.Request, questID string, hypID string) {
	traceID := resolveWisdevRouteTraceID(r, "")
	if h.gateway == nil || h.gateway.DB == nil || h.gateway.Loop == nil {
		writeWisdevHypothesisError(w, traceID, http.StatusServiceUnavailable, ErrServiceUnavailable, "quest runtime unavailable", nil)
		return
	}

	store := wisdev.NewPostgresQuestStore(h.gateway.DB)
	quest, err := store.LoadQuestState(r.Context(), questID)
	if err != nil {
		writeWisdevHypothesisError(w, traceID, http.StatusInternalServerError, ErrInternal, "failed to load quest", map[string]any{
			"error":   err.Error(),
			"questId": questID,
		})
		return
	}
	if quest == nil {
		writeWisdevHypothesisError(w, traceID, http.StatusNotFound, ErrNotFound, "quest not found", map[string]any{
			"questId": questID,
		})
		return
	}
	if !requireWisdevHypothesisAccess(w, r, traceID, quest) {
		return
	}

	// Find the target hypothesis.
	var target *wisdev.Hypothesis
	for _, hyp := range quest.Hypotheses {
		if hyp.ID == hypID {
			target = hyp
			break
		}
	}
	if target == nil {
		writeWisdevHypothesisError(w, traceID, http.StatusNotFound, ErrNotFound, "hypothesis not found", map[string]any{
			"questId":      questID,
			"hypothesisId": hypID,
		})
		return
	}

	bgCtx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	req := wisdev.LoopRequest{
		Query:     hypothesisRefinementQuery(target),
		ProjectID: questID,
		Domain:    quest.Domain,
		Mode:      string(wisdev.WisDevModeYOLO),
	}
	result, swarmErr := h.gateway.Loop.Run(bgCtx, req, nil)
	if swarmErr != nil {
		writeWisdevHypothesisError(w, traceID, http.StatusInternalServerError, ErrWisdevFailed, "failed to refine hypothesis", map[string]any{
			"error":        swarmErr.Error(),
			"questId":      questID,
			"hypothesisId": hypID,
		})
		return
	}

	applyRefinementResult(quest, target, hypID, result)

	iter := wisdev.IterationRecord{
		Iteration:  quest.CurrentIteration + 1,
		Timestamp:  time.Now(),
		Action:     "refine_hypothesis",
		Change:     fmt.Sprintf("Refined hypothesis %s using targeted evidence loop", hypID),
		TokensUsed: 0,
	}
	quest.CurrentIteration = iter.Iteration
	quest.UpdatedAt = time.Now().UnixMilli()

	if saveErr := store.SaveQuestState(bgCtx, quest); saveErr != nil {
		writeWisdevHypothesisError(w, traceID, http.StatusInternalServerError, ErrInternal, "failed to persist refinement", map[string]any{
			"error":        saveErr.Error(),
			"questId":      questID,
			"hypothesisId": hypID,
		})
		return
	}
	_ = store.SaveIteration(bgCtx, questID, iter)

	papersFound := 0
	converged := false
	if result != nil {
		papersFound = len(result.Papers)
		converged = result.Converged
	}

	writeEnvelopeWithTraceID(w, traceID, "hypothesisUpdate", wisdevHypothesisUpdateEnvelope{
		Message:      "Refinement complete for hypothesis " + hypID,
		QuestID:      firstNonEmpty(quest.ID, quest.SessionID, quest.QuestID, questID),
		SessionID:    firstNonEmpty(quest.SessionID, quest.ID, quest.QuestID, questID),
		Status:       string(quest.Status),
		HypothesisID: hypID,
		Hypothesis:   target,
		Dossier:      quest.EvidenceDossiers[hypID],
		Converged:    converged,
		PapersFound:  papersFound,
	})
}

func (h *WisDevHandler) handleAcceptHypothesis(w http.ResponseWriter, r *http.Request, questID string, hypID string) {
	traceID := resolveWisdevRouteTraceID(r, "")
	if h.gateway == nil || h.gateway.DB == nil {
		writeWisdevHypothesisError(w, traceID, http.StatusServiceUnavailable, ErrServiceUnavailable, "quest runtime unavailable", nil)
		return
	}

	store := wisdev.NewPostgresQuestStore(h.gateway.DB)
	quest, err := store.LoadQuestState(r.Context(), questID)
	if err != nil {
		writeWisdevHypothesisError(w, traceID, http.StatusInternalServerError, ErrInternal, "failed to load quest", map[string]any{
			"error":   err.Error(),
			"questId": questID,
		})
		return
	}
	if quest == nil {
		writeWisdevHypothesisError(w, traceID, http.StatusNotFound, ErrNotFound, "quest not found", map[string]any{
			"questId": questID,
		})
		return
	}
	if !requireWisdevHypothesisAccess(w, r, traceID, quest) {
		return
	}

	// Find the hypothesis and mark it accepted.
	found := false
	var accepted *wisdev.Hypothesis
	acceptedClaims := make(map[string]struct{}, len(quest.AcceptedClaims))
	for _, claim := range quest.AcceptedClaims {
		if strings.TrimSpace(claim.ID) == "" {
			continue
		}
		acceptedClaims[claim.ID] = struct{}{}
	}

	for _, hyp := range quest.Hypotheses {
		if hyp.ID == hypID {
			hyp.Status = "accepted"
			// Promote all evidence findings into AcceptedClaims.
			for _, ev := range hyp.Evidence {
				if ev == nil {
					continue
				}
				if _, seen := acceptedClaims[ev.ID]; seen {
					continue
				}
				quest.AcceptedClaims = append(quest.AcceptedClaims, *ev)
				if strings.TrimSpace(ev.ID) != "" {
					acceptedClaims[ev.ID] = struct{}{}
				}
			}
			accepted = hyp
			found = true
			break
		}
	}
	if !found {
		writeWisdevHypothesisError(w, traceID, http.StatusNotFound, ErrNotFound, "hypothesis not found", map[string]any{
			"questId":      questID,
			"hypothesisId": hypID,
		})
		return
	}

	quest.UpdatedAt = time.Now().UnixMilli()
	iter := wisdev.IterationRecord{
		Iteration: quest.CurrentIteration + 1,
		Timestamp: time.Now(),
		Action:    "accept_hypothesis",
		Change:    fmt.Sprintf("Hypothesis %s accepted by user", hypID),
	}
	quest.CurrentIteration = iter.Iteration

	if saveErr := store.SaveQuestState(r.Context(), quest); saveErr != nil {
		writeWisdevHypothesisError(w, traceID, http.StatusInternalServerError, ErrInternal, "failed to persist acceptance", map[string]any{
			"error":        saveErr.Error(),
			"questId":      questID,
			"hypothesisId": hypID,
		})
		return
	}
	_ = store.SaveIteration(r.Context(), questID, iter)

	writeEnvelopeWithTraceID(w, traceID, "hypothesisUpdate", wisdevHypothesisUpdateEnvelope{
		Message:         "Hypothesis " + hypID + " accepted",
		QuestID:         firstNonEmpty(quest.ID, quest.SessionID, quest.QuestID, questID),
		SessionID:       firstNonEmpty(quest.SessionID, quest.ID, quest.QuestID, questID),
		Status:          string(quest.Status),
		HypothesisID:    hypID,
		Hypothesis:      accepted,
		AcceptedClaimsN: len(quest.AcceptedClaims),
	})
}

// HandleJobStream handles GET /wisdev/job/{jobId}/stream.
func (h *WisDevHandler) HandleJobStream(w http.ResponseWriter, r *http.Request) {
	WisDevStreamHandler(w, r)
}

func hypothesisRefinementQuery(target *wisdev.Hypothesis) string {
	if target == nil {
		return ""
	}
	if trimmed := strings.TrimSpace(target.Text); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(target.Claim)
}

func applyRefinementResult(quest *wisdev.QuestState, target *wisdev.Hypothesis, hypID string, result *wisdev.LoopResult) {
	if quest == nil || target == nil || result == nil {
		return
	}

	now := time.Now().UnixMilli()
	findings := result.Evidence
	if len(findings) == 0 {
		findings = findingsFromPapers(result.Papers)
	}

	target.Evidence = evidenceFindingPointers(findings)
	target.EvidenceCount = len(target.Evidence)
	target.Contradictions = contradictoryEvidencePointers(findings)
	target.ContradictionCount = len(target.Contradictions)
	target.ConfidenceScore = averageEvidenceConfidence(findings)
	target.UpdatedAt = now
	target.Status = "refined"

	if quest.EvidenceDossiers == nil {
		quest.EvidenceDossiers = make(map[string]*wisdev.EvidenceDossier)
	}
	quest.EvidenceDossiers[hypID] = buildRefinementDossier(hypID, now, findings)

	if quest.ResearchScratchpad == nil {
		quest.ResearchScratchpad = make(map[string]string)
	}
	if summary := strings.TrimSpace(result.FinalAnswer); summary != "" {
		quest.ResearchScratchpad["refine_"+hypID] = summary
		quest.ReviewerNotes = append(quest.ReviewerNotes, fmt.Sprintf("Refined hypothesis %s at %s", hypID, time.Now().Format(time.RFC3339)))
	}
}

func findingsFromPapers(papers []search.Paper) []wisdev.EvidenceFinding {
	findings := make([]wisdev.EvidenceFinding, 0, len(papers))
	for idx, paper := range papers {
		title := strings.TrimSpace(paper.Title)
		abstract := strings.TrimSpace(paper.Abstract)
		if title == "" && abstract == "" {
			continue
		}
		claim := title
		if claim == "" {
			claim = abstract
		}
		snippet := abstract
		if snippet == "" {
			snippet = title
		}
		findings = append(findings, wisdev.EvidenceFinding{
			ID:         fmt.Sprintf("paper_%d_%s", idx+1, strings.TrimSpace(paper.ID)),
			Claim:      claim,
			Snippet:    snippet,
			PaperTitle: title,
			Keywords:   append([]string(nil), paper.Keywords...),
			SourceID:   strings.TrimSpace(paper.ID),
			Year:       paper.Year,
			Confidence: paper.Score,
			Status:     "tentative",
		})
	}
	return findings
}

func evidenceFindingPointers(findings []wisdev.EvidenceFinding) []*wisdev.EvidenceFinding {
	pointers := make([]*wisdev.EvidenceFinding, 0, len(findings))
	for _, finding := range findings {
		copy := finding
		pointers = append(pointers, &copy)
	}
	return pointers
}

func contradictoryEvidencePointers(findings []wisdev.EvidenceFinding) []*wisdev.EvidenceFinding {
	var contradictory []*wisdev.EvidenceFinding
	for _, finding := range findings {
		if finding.Specialist.Verification >= 0 && !strings.EqualFold(strings.TrimSpace(finding.Status), "contradicted") {
			continue
		}
		copy := finding
		contradictory = append(contradictory, &copy)
	}
	return contradictory
}

func averageEvidenceConfidence(findings []wisdev.EvidenceFinding) float64 {
	if len(findings) == 0 {
		return 0
	}
	total := 0.0
	for _, finding := range findings {
		total += finding.Confidence
	}
	return total / float64(len(findings))
}

func buildRefinementDossier(hypID string, now int64, findings []wisdev.EvidenceFinding) *wisdev.EvidenceDossier {
	dossier := &wisdev.EvidenceDossier{
		JobID:         fmt.Sprintf("refine_%s_%d", hypID, now),
		Verified:      make([]wisdev.EvidenceFinding, 0, len(findings)),
		Tentative:     make([]wisdev.EvidenceFinding, 0, len(findings)),
		Contradictory: make([]wisdev.ContradictionPair, 0),
		Gaps:          make([]string, 0),
	}

	for _, finding := range findings {
		switch {
		case finding.Specialist.Verification < 0 || strings.EqualFold(strings.TrimSpace(finding.Status), "contradicted"):
			dossier.Tentative = append(dossier.Tentative, finding)
		case finding.Specialist.Verification > 0 || finding.Confidence >= 0.7:
			dossier.Verified = append(dossier.Verified, finding)
		default:
			dossier.Tentative = append(dossier.Tentative, finding)
		}
	}

	if len(findings) == 0 {
		dossier.Gaps = append(dossier.Gaps, "No additional supporting evidence extracted during refinement")
	}

	return dossier
}
