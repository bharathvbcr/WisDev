package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

// ── Data Contracts ───────────────────────────────────────────────────────────

type ManuscriptDraftHTTPRequest struct {
	Title            string   `json:"title"`
	ContextDocuments []string `json:"context_documents"`
	Findings         []string `json:"findings"`
	SessionID        string   `json:"sessionId,omitempty"`
	TraceID          string   `json:"traceId,omitempty"`
	LegacyTraceID    string   `json:"trace_id,omitempty"`
	Model            string   `json:"model"` // Optional model override
}

type ReviewerRebuttalHTTPRequest struct {
	ReviewerComments []string `json:"reviewer_comments"`
	PaperText        string   `json:"paper_text"`
	SessionID        string   `json:"sessionId,omitempty"`
	TraceID          string   `json:"traceId,omitempty"`
	LegacyTraceID    string   `json:"trace_id,omitempty"`
	Model            string   `json:"model"` // Optional model override
}

// ── Handlers ─────────────────────────────────────────────────────────────────

var draftingLLMRequestTimeout = 45 * time.Second

func draftingLLMContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if draftingLLMRequestTimeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, draftingLLMRequestTimeout)
}

func (s *wisdevServer) HandleManuscriptDraft(w http.ResponseWriter, r *http.Request) {
	if s.gateway == nil || s.gateway.LLMClient == nil {
		WriteError(w, http.StatusServiceUnavailable, ErrWisdevFailed, "LLM client not configured", nil)
		return
	}
	var req ManuscriptDraftHTTPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", nil)
		return
	}
	traceID := resolveDraftingTraceID(r, req.TraceID, req.LegacyTraceID)
	req.TraceID = traceID
	userID, sessionID, ok := resolveDraftingActor(w, r, s.gateway, req.SessionID)
	if !ok {
		return
	}

	if req.Title == "" {
		req.Title = "Research Synthesis"
	}

	model := req.Model
	if model == "" {
		model = llm.ResolveHeavyModel() // Default to heavy for manuscript drafting
	}

	prompt := buildDraftingPrompt(req)
	systemPrompt := "You are an expert academic writing assistant. Write a well-structured, scholarly manuscript draft based on the provided research context. Use formal academic language, hedge appropriately, and cite supporting evidence inline (e.g. [Source 1]). Do NOT fabricate statistics or citations."

	llmCtx, llmCancel := draftingLLMContext(r.Context())
	defer llmCancel()

	resp, err := s.gateway.LLMClient.Generate(llmCtx, llm.ApplyGeneratePolicy(&llmv1.GenerateRequest{
		Prompt:       prompt,
		SystemPrompt: systemPrompt,
		MaxTokens:    3000,
		Temperature:  0.5,
		Model:        model,
		Metadata:     map[string]string{"trace_id": req.TraceID},
	}, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
		RequestedTier: "heavy",
		TaskType:      "synthesis",
	})))

	if err != nil {
		WriteError(w, http.StatusBadGateway, ErrWisdevFailed, "LLM generation failed", map[string]any{"error": err.Error()})
		return
	}

	draftText, err := normalizeGeneratedResponseText("manuscript draft generation", resp)
	if err != nil {
		WriteError(w, http.StatusBadGateway, ErrWisdevFailed, "LLM generation returned empty text", map[string]any{"error": err.Error()})
		return
	}

	payload := map[string]any{
		"content":    draftText,
		"confidence": estimateGrounding(draftText, strings.Join(req.ContextDocuments, " ")),
		"latency_ms": resp.LatencyMs,
	}
	addDraftingTraceFields(payload, traceID)
	w.Header().Set("X-Trace-Id", traceID)
	traceID = writeEnvelopeWithTraceID(w, traceID, "manuscriptDraft", payload)
	s.journalEvent("manuscript_draft", "/manuscript/draft", traceID, sessionID, userID, "", "", "Manuscript draft generated.", payload, nil)
}

func (s *wisdevServer) HandleManuscriptDraftStream(w http.ResponseWriter, r *http.Request) {
	if s.gateway == nil || s.gateway.LLMClient == nil {
		WriteError(w, http.StatusServiceUnavailable, ErrWisdevFailed, "LLM client not configured", nil)
		return
	}
	var req ManuscriptDraftHTTPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", nil)
		return
	}
	traceID := resolveDraftingTraceID(r, req.TraceID, req.LegacyTraceID)
	req.TraceID = traceID
	_, _, ok := resolveDraftingActor(w, r, s.gateway, req.SessionID)
	if !ok {
		return
	}

	if req.Title == "" {
		req.Title = "Research Synthesis"
	}

	model := req.Model
	if model == "" {
		model = llm.ResolveHeavyModel()
	}

	prompt := buildDraftingPrompt(req)
	systemPrompt := "You are an expert academic writing assistant. Write a well-structured, scholarly manuscript draft based on the provided research context. Use formal academic language, hedge appropriately, and cite supporting evidence inline (e.g. [Source 1]). Do NOT fabricate statistics or citations."

	stream, err := s.gateway.LLMClient.GenerateStream(r.Context(), llm.ApplyGeneratePolicy(&llmv1.GenerateRequest{
		Prompt:       prompt,
		SystemPrompt: systemPrompt,
		MaxTokens:    3000,
		Temperature:  0.5,
		Model:        model,
		Metadata:     map[string]string{"trace_id": req.TraceID},
	}, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
		RequestedTier: "heavy",
		TaskType:      "synthesis",
	})))

	if err != nil {
		WriteError(w, http.StatusBadGateway, ErrWisdevFailed, "LLM stream initialization failed", map[string]any{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Trace-Id", traceID)

	const maxBufferSize = 1024 // Prevent buffer bloat for long contiguous generations
	var buffer strings.Builder
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			encodeSSE(w, "error", draftingTracePayload(traceID, map[string]any{"message": err.Error()}))
			return
		}

		buffer.WriteString(chunk.Delta)
		content := buffer.String()

		// Yield by paragraph if possible
		for strings.Contains(content, "\n\n") {
			parts := strings.SplitN(content, "\n\n", 2)
			para := parts[0]
			content = parts[1]

			if strings.TrimSpace(para) != "" {
				encodeSSE(w, "chunk", draftingTracePayload(traceID, map[string]any{
					"chunk":    para + "\n\n",
					"is_final": false,
				}))
			}
		}

		// Improvement: Flush if buffer exceeds limit to prevent delays on very long paragraphs
		if len(content) > maxBufferSize {
			encodeSSE(w, "chunk", draftingTracePayload(traceID, map[string]any{
				"chunk":    content,
				"is_final": false,
			}))
			content = ""
		}

		buffer.Reset()
		buffer.WriteString(content)
	}

	if strings.TrimSpace(buffer.String()) != "" {
		encodeSSE(w, "chunk", draftingTracePayload(traceID, map[string]any{
			"chunk":    buffer.String(),
			"is_final": true,
		}))
	} else {
		encodeSSE(w, "chunk", draftingTracePayload(traceID, map[string]any{
			"chunk":    "",
			"is_final": true,
		}))
	}
}

func (s *wisdevServer) HandleReviewerRebuttal(w http.ResponseWriter, r *http.Request) {
	if s.gateway == nil || s.gateway.LLMClient == nil {
		WriteError(w, http.StatusServiceUnavailable, ErrWisdevFailed, "LLM client not configured", nil)
		return
	}
	var req ReviewerRebuttalHTTPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", nil)
		return
	}
	traceID := resolveDraftingTraceID(r, req.TraceID, req.LegacyTraceID)
	req.TraceID = traceID
	userID, sessionID, ok := resolveDraftingActor(w, r, s.gateway, req.SessionID)
	if !ok {
		return
	}

	if len(req.ReviewerComments) == 0 {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "reviewer_comments is required", nil)
		return
	}

	model := req.Model
	if model == "" {
		model = llm.ResolveHeavyModel()
	}

	prompt := buildRebuttalPrompt(req)
	systemPrompt := "You are an expert academic writing assistant specialising in peer-review rebuttals. Your rebuttals are evidence-grounded, professionally toned, and intellectually honest: concede valid points, counter weak ones with reasoning and references, and always propose concrete manuscript revisions. Never be dismissive of reviewers."

	llmCtx, llmCancel := draftingLLMContext(r.Context())
	defer llmCancel()

	resp, err := s.gateway.LLMClient.Generate(llmCtx, llm.ApplyGeneratePolicy(&llmv1.GenerateRequest{
		Prompt:       prompt,
		SystemPrompt: systemPrompt,
		MaxTokens:    3500,
		Temperature:  0.4,
		Model:        model,
		Metadata:     map[string]string{"trace_id": req.TraceID},
	}, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
		RequestedTier: "heavy",
		TaskType:      "synthesis",
	})))

	if err != nil {
		WriteError(w, http.StatusBadGateway, ErrWisdevFailed, "LLM generation failed", map[string]any{"error": err.Error()})
		return
	}

	rebuttalText, err := normalizeGeneratedResponseText("reviewer rebuttal generation", resp)
	if err != nil {
		WriteError(w, http.StatusBadGateway, ErrWisdevFailed, "LLM generation returned empty text", map[string]any{"error": err.Error()})
		return
	}

	payload := map[string]any{
		"rebuttal_text":      rebuttalText,
		"overall_confidence": 0.85,
		"grounding_ratio":    estimateGrounding(rebuttalText, req.PaperText),
		"latency_ms":         resp.LatencyMs,
	}
	addDraftingTraceFields(payload, traceID)
	w.Header().Set("X-Trace-Id", traceID)
	traceID = writeEnvelopeWithTraceID(w, traceID, "reviewerRebuttal", payload)
	s.journalEvent("reviewer_rebuttal", "/reviewer/rebuttal", traceID, sessionID, userID, "", "", "Reviewer rebuttal generated.", payload, nil)
}

func (s *wisdevServer) HandleReviewerRebuttalStream(w http.ResponseWriter, r *http.Request) {
	if s.gateway == nil || s.gateway.LLMClient == nil {
		WriteError(w, http.StatusServiceUnavailable, ErrWisdevFailed, "LLM client not configured", nil)
		return
	}
	var req ReviewerRebuttalHTTPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", nil)
		return
	}
	traceID := resolveDraftingTraceID(r, req.TraceID, req.LegacyTraceID)
	req.TraceID = traceID
	_, _, ok := resolveDraftingActor(w, r, s.gateway, req.SessionID)
	if !ok {
		return
	}

	if len(req.ReviewerComments) == 0 {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "reviewer_comments is required", nil)
		return
	}

	model := req.Model
	if model == "" {
		model = llm.ResolveHeavyModel()
	}

	prompt := buildRebuttalPrompt(req)
	systemPrompt := "You are an expert academic writing assistant specialising in peer-review rebuttals. Your rebuttals are evidence-grounded, professionally toned, and intellectually honest: concede valid points, counter weak ones with reasoning and references, and always propose concrete manuscript revisions. Never be dismissive of reviewers."

	stream, err := s.gateway.LLMClient.GenerateStream(r.Context(), llm.ApplyGeneratePolicy(&llmv1.GenerateRequest{
		Prompt:       prompt,
		SystemPrompt: systemPrompt,
		MaxTokens:    3500,
		Temperature:  0.4,
		Model:        model,
		Metadata:     map[string]string{"trace_id": req.TraceID},
	}, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
		RequestedTier: "heavy",
		TaskType:      "synthesis",
	})))

	if err != nil {
		WriteError(w, http.StatusBadGateway, ErrWisdevFailed, "LLM stream initialization failed", map[string]any{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Trace-Id", traceID)

	const maxBufferSize = 1024
	var buffer strings.Builder
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			encodeSSE(w, "error", draftingTracePayload(traceID, map[string]any{"message": err.Error()}))
			return
		}

		buffer.WriteString(chunk.Delta)
		content := buffer.String()

		for strings.Contains(content, "\n\n") {
			parts := strings.SplitN(content, "\n\n", 2)
			para := parts[0]
			content = parts[1]

			if strings.TrimSpace(para) != "" {
				encodeSSE(w, "chunk", draftingTracePayload(traceID, map[string]any{
					"chunk":    para + "\n\n",
					"is_final": false,
				}))
			}
		}

		if len(content) > maxBufferSize {
			encodeSSE(w, "chunk", draftingTracePayload(traceID, map[string]any{
				"chunk":    content,
				"is_final": false,
			}))
			content = ""
		}

		buffer.Reset()
		buffer.WriteString(content)
	}

	if strings.TrimSpace(buffer.String()) != "" {
		encodeSSE(w, "chunk", draftingTracePayload(traceID, map[string]any{
			"chunk":    buffer.String(),
			"is_final": true,
		}))
	} else {
		encodeSSE(w, "chunk", draftingTracePayload(traceID, map[string]any{
			"chunk":    "",
			"is_final": true,
		}))
	}
}

func resolveDraftingActor(w http.ResponseWriter, r *http.Request, agentGateway *wisdev.AgentGateway, requestedSessionID string) (string, string, bool) {
	userID, err := resolveAuthorizedUserID(r, "")
	if err != nil {
		WriteError(w, http.StatusForbidden, ErrUnauthorized, err.Error(), nil)
		return "", "", false
	}
	sessionID := strings.TrimSpace(requestedSessionID)
	if _, ok := requireSessionBindingAccess(w, r, agentGateway, sessionID, userID); !ok {
		return "", "", false
	}
	return userID, sessionID, true
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func encodeSSE(w http.ResponseWriter, event string, data any) {
	payload, _ := json.Marshal(data)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, string(payload))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func resolveDraftingTraceID(r *http.Request, candidates ...string) string {
	for _, candidate := range candidates {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return resolveRequestTraceID(r)
}

func addDraftingTraceFields(payload map[string]any, traceID string) {
	if payload == nil {
		return
	}
	payload["traceId"] = traceID
	payload["trace_id"] = traceID
}

func draftingTracePayload(traceID string, payload map[string]any) map[string]any {
	cloned := make(map[string]any, len(payload)+2)
	for key, value := range payload {
		cloned[key] = value
	}
	addDraftingTraceFields(cloned, traceID)
	return cloned
}

func buildDraftingPrompt(req ManuscriptDraftHTTPRequest) string {
	var sourcesBlock string
	if len(req.ContextDocuments) > 0 {
		var sources []string
		for i, doc := range req.ContextDocuments {
			if i >= 8 {
				break
			}
			limit := 400
			if len(doc) < limit {
				limit = len(doc)
			}
			sources = append(sources, fmt.Sprintf("[Source %d] %s", i+1, doc[:limit]))
		}
		sourcesBlock = "\n\nSUPPORTING SOURCES:\n" + strings.Join(sources, "\n")
	}

	var findingsBlock string
	if len(req.Findings) > 0 {
		var findings []string
		for i, f := range req.Findings {
			if i >= 10 {
				break
			}
			findings = append(findings, "  - "+f)
		}
		findingsBlock = "\n\nKEY FINDINGS TO INCORPORATE:\n" + strings.Join(findings, "\n")
	}

	return fmt.Sprintf(
		"MANUSCRIPT TITLE: %s%s%s\n\n"+
			"Write a complete academic manuscript draft with the following sections:\n"+
			"## Abstract\n## 1. Introduction\n## 2. Related Work\n## 3. Methods\n"+
			"## 4. Results\n## 5. Discussion\n## 6. Conclusion\n\n"+
			"Each section should be substantive (150–300 words). "+
			"Ground claims in the provided sources using [Source N] citations. "+
			"Use formal academic tone throughout.",
		req.Title, sourcesBlock, findingsBlock,
	)
}

func buildRebuttalPrompt(req ReviewerRebuttalHTTPRequest) string {
	paperExcerpt := "(not provided)"
	if req.PaperText != "" {
		limit := 800
		if len(req.PaperText) < limit {
			limit = len(req.PaperText)
		}
		paperExcerpt = req.PaperText[:limit]
	}

	var numberedComments strings.Builder
	for i, comment := range req.ReviewerComments {
		fmt.Fprintf(&numberedComments, "\n### Reviewer %d\n%s\n", i+1, strings.TrimSpace(comment))
	}

	return fmt.Sprintf(
		"PAPER ABSTRACT / TEXT:\n%s\n\n"+
			"REVIEWER COMMENTS:%s\n"+
			"---\n"+
			"Write a complete, structured rebuttal letter addressing EVERY reviewer comment.\n\n"+
			"Format:\n"+
			"- Begin with a brief opening thanking reviewers.\n"+
			"- For each reviewer, use a heading: **Response to Reviewer N**\n"+
			"- For each comment point, respond with:\n"+
			"  - **Reviewer's concern:** (brief paraphrase)\n"+
			"  - **Our response:** (counter-argument, concession, or clarification with evidence)\n"+
			"  - **Manuscript change:** (specific revision made or proposed)\n"+
			"- End with a brief closing statement.\n\n"+
			"Be thorough, precise, and evidence-based. Concede valid points honestly.",
		paperExcerpt, numberedComments.String(),
	)
}

func estimateGrounding(rebuttalText, paperText string) float64 {
	if paperText == "" || rebuttalText == "" {
		return 0.5
	}

	paperTokens := make(map[string]bool)
	for _, t := range strings.FieldsFunc(strings.ToLower(paperText), isNotAlphanumeric) {
		if len(t) > 3 {
			paperTokens[t] = true
		}
	}

	sentences := strings.FieldsFunc(rebuttalText, isSentenceEnd)
	var total, grounded int
	for _, s := range sentences {
		s = strings.TrimSpace(s)
		if len(s) < 20 {
			continue
		}
		total++

		matchCount := 0
		for _, t := range strings.FieldsFunc(strings.ToLower(s), isNotAlphanumeric) {
			if paperTokens[t] {
				matchCount++
				if matchCount >= 2 {
					grounded++
					break
				}
			}
		}
	}

	if total == 0 {
		return 0.5
	}
	return float64(int(float64(grounded)/float64(total)*1000)) / 1000.0
}

func isNotAlphanumeric(r rune) bool {
	return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'))
}

func isSentenceEnd(r rune) bool {
	return r == '.' || r == '!' || r == '?'
}
