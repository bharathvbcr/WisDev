package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	llmv1 "github.com/wisdev-agent/wisdev-agent-os/orchestrator/proto/llm/v1"
)

type SynthesisHandler struct {
	llmClient generateClient
}

func NewSynthesisHandler(llmClient generateClient) *SynthesisHandler {
	return &SynthesisHandler{llmClient: llmClient}
}

func (h *SynthesisHandler) HandleSynthesis(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	action := r.URL.Query().Get("action")
	switch action {
	case "review":
		h.handleReview(w, r)
	case "summary":
		h.handleSummary(w, r)
	case "compare":
		h.handleCompare(w, r)
	default:
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "invalid action", map[string]any{
			"allowedActions": []string{"review", "summary", "compare"},
		})
	}
}

func (h *SynthesisHandler) handleReview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Papers []struct {
			Title    string `json:"title"`
			Authors  string `json:"authors"`
			Year     string `json:"year"`
			Abstract string `json:"abstract"`
		} `json:"papers"`
		Topic string `json:"topic"`
		Style string `json:"style"` // "academic" | "accessible"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	if len(req.Papers) == 0 {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "papers required", map[string]any{
			"field": "papers",
		})
		return
	}

	var papersText strings.Builder
	for i, p := range req.Papers {
		fmt.Fprintf(&papersText, "[%d] %s by %s (%s)\nAbstract: %s\n\n", i+1, p.Title, p.Authors, p.Year, p.Abstract)
	}

	styleText := "Write in a formal academic style suitable for a journal submission."
	if req.Style == "accessible" {
		styleText = "Write in an accessible style suitable for a general educated audience."
	}

	prompt := fmt.Sprintf(`You are an expert academic writer. Generate a comprehensive literature review based on the following papers.

%s

Topic: %s

Papers to review:
%s

Structure your review with:
1. Introduction - Context and importance of the topic
2. Thematic Analysis - Group findings by theme, not by paper
3. Synthesis - Identify patterns, agreements, and contradictions
4. Research Gaps - What questions remain unanswered?
5. Conclusion - Summary and future directions

Use in-text citations like [1], [2] referring to the paper numbers above.
Write approximately 800-1200 words.`, styleText, req.Topic, papersText.String())

	resp, err := h.llmClient.Generate(r.Context(), &llmv1.GenerateRequest{
		Prompt:      prompt,
		Temperature: 0.7,
		MaxTokens:   4096,
	})
	if err != nil {
		WriteError(w, http.StatusBadGateway, ErrDependencyFailed, "synthesis failed", map[string]any{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"text": resp.Text})
}

func (h *SynthesisHandler) handleSummary(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title    string `json:"title"`
		Abstract string `json:"abstract"`
		Level    string `json:"level"` // "tldr" | "brief" | "detailed"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	if req.Title == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "title required", map[string]any{
			"field": "title",
		})
		return
	}

	context := fmt.Sprintf("Title: %s", req.Title)
	if req.Abstract != "" {
		context += fmt.Sprintf("\n\nAbstract: %s", req.Abstract)
	}

	prompt := ""
	maxTokens := int32(1000)
	switch req.Level {
	case "tldr":
		prompt = fmt.Sprintf("Summarize this academic paper in one sentence (TL;DR style):\n\n%s", context)
		maxTokens = 100
	case "brief":
		prompt = fmt.Sprintf("Summarize this academic paper in 2-3 sentences:\n\n%s", context)
		maxTokens = 300
	case "detailed":
		fallthrough
	default:
		prompt = fmt.Sprintf("Provide a detailed summary of this academic paper (3-4 paragraphs covering: main findings, methodology, significance):\n\n%s", context)
		maxTokens = 1000
	}

	resp, err := h.llmClient.Generate(r.Context(), &llmv1.GenerateRequest{
		Prompt:      prompt,
		Temperature: 0.5,
		MaxTokens:   maxTokens,
	})
	if err != nil {
		WriteError(w, http.StatusBadGateway, ErrDependencyFailed, "summary failed", map[string]any{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"text": resp.Text})
}

func (h *SynthesisHandler) handleCompare(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Papers []struct {
			Title    string   `json:"title"`
			Abstract string   `json:"abstract"`
			Authors  string   `json:"authors"`
			Year     string   `json:"year"`
		} `json:"papers"`
		Aspects []string `json:"aspects"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	if len(req.Papers) < 2 {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "at least 2 papers required", map[string]any{
			"field": "papers",
			"count": len(req.Papers),
		})
		return
	}

	var papersText strings.Builder
	for i, p := range req.Papers {
		fmt.Fprintf(&papersText, "Paper %d: %q by %s (%s)\nAbstract: %s\n\n", i+1, p.Title, p.Authors, p.Year, p.Abstract)
	}

	aspectsText := "Compare across: methodology, findings, limitations, and future directions"
	if len(req.Aspects) > 0 {
		aspectsText = fmt.Sprintf("Focus on these aspects: %s", strings.Join(req.Aspects, ", "))
	}

	prompt := fmt.Sprintf("Compare the following %d academic papers. %s\n\n%s\n\nProvide a structured comparison in markdown format with tables where appropriate.",
		len(req.Papers), aspectsText, papersText.String())

	resp, err := h.llmClient.Generate(r.Context(), &llmv1.GenerateRequest{
		Prompt:      prompt,
		Temperature: 0.4,
		MaxTokens:   4096,
	})
	if err != nil {
		WriteError(w, http.StatusBadGateway, ErrDependencyFailed, "comparison failed", map[string]any{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"text": resp.Text})
}
