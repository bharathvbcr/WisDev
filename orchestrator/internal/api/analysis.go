package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

type analysisClient interface {
	Generate(ctx context.Context, req *llmv1.GenerateRequest) (*llmv1.GenerateResponse, error)
	StructuredOutput(ctx context.Context, req *llmv1.StructuredRequest) (*llmv1.StructuredResponse, error)
}

type AnalysisHandler struct {
	llmClient  analysisClient
	httpClient HTTPClient
}

var (
	analysisLLMRequestTimeout      = 45 * time.Second
	analysisExternalRequestTimeout = 45 * time.Second
	analysisStructuredLLMGrace     = 2 * time.Second
)

func NewAnalysisHandler(llmClient analysisClient, httpClient HTTPClient) *AnalysisHandler {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &AnalysisHandler{
		llmClient:  llmClient,
		httpClient: httpClient,
	}
}

func (h *AnalysisHandler) SetHTTPClient(client HTTPClient) {
	h.httpClient = client
}

func analysisLLMContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if analysisLLMRequestTimeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, analysisLLMRequestTimeout)
}

func analysisStructuredClient(ctx context.Context, client analysisClient) analysisClient {
	llmClient, ok := client.(*llm.Client)
	if !ok || llmClient == nil || llmClient.VertexDirect == nil {
		return client
	}

	backstop := analysisLLMRequestTimeout + analysisStructuredLLMGrace
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining > 0 {
			backstop = remaining + analysisStructuredLLMGrace
		}
	}
	if backstop <= 0 {
		backstop = analysisStructuredLLMGrace
	}

	return llmClient.WithoutVertexDirect().WithTimeout(backstop)
}

func analysisExternalContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if analysisExternalRequestTimeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, analysisExternalRequestTimeout)
}

func (h *AnalysisHandler) HandleAnalysis(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	action := r.URL.Query().Get("action")
	switch action {
	case "trends":
		h.handleTrends(w, r)
	case "gaps":
		h.handleGaps(w, r)
	case "methodology":
		h.handleMethodology(w, r)
	default:
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "invalid or missing action", map[string]any{
			"allowedActions": []string{"trends", "gaps", "methodology"},
		})
	}
}

func (h *AnalysisHandler) handleTrends(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query     string `json:"query"`
		YearStart int    `json:"yearStart"`
		YearEnd   int    `json:"yearEnd"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	if req.Query == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query required", map[string]any{
			"field": "query",
		})
		return
	}

	currentYear := time.Now().Year()
	if req.YearEnd == 0 {
		req.YearEnd = currentYear
	}
	if req.YearStart == 0 {
		req.YearStart = currentYear - 10
	}

	email := os.Getenv("OPENALEX_EMAIL")
	if email == "" {
		email = "scholar.focus.app@gmail.com"
	}

	oaUrl := fmt.Sprintf("https://api.openalex.org/works?search=%s&filter=publication_year:%d-%d&group_by=publication_year&mailto=%s",
		url.QueryEscape(req.Query), req.YearStart, req.YearEnd, url.QueryEscape(email))

	fetchCtx, fetchCancel := analysisExternalContext(r.Context())
	defer fetchCancel()

	fetchReq, _ := http.NewRequestWithContext(fetchCtx, "GET", oaUrl, nil)
	resp, err := h.httpClient.Do(fetchReq)
	if err != nil {
		WriteError(w, http.StatusBadGateway, ErrDependencyFailed, "openalex request failed", map[string]any{
			"error": err.Error(),
		})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		WriteError(w, http.StatusBadGateway, ErrDependencyFailed, "openalex returned unexpected status", map[string]any{
			"status": resp.StatusCode,
		})
		return
	}

	var oaResponse struct {
		GroupBy []struct {
			Key   string `json:"key"`
			Count int    `json:"count"`
		} `json:"group_by"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&oaResponse); err != nil {
		WriteError(w, http.StatusBadGateway, ErrDependencyFailed, "failed to decode openalex response", map[string]any{
			"error": err.Error(),
		})
		return
	}

	type trendPoint struct {
		Year  int `json:"year"`
		Count int `json:"count"`
	}
	points := make([]trendPoint, 0, len(oaResponse.GroupBy))
	for _, g := range oaResponse.GroupBy {
		year, _ := time.Parse("2006", g.Key)
		points = append(points, trendPoint{
			Year:  year.Year(),
			Count: g.Count,
		})
	}

	sort.Slice(points, func(i, j int) bool {
		return points[i].Year < points[j].Year
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"query":  req.Query,
		"trends": points,
	})
}

func (h *AnalysisHandler) handleGaps(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Papers []struct {
			Title    string `json:"title"`
			Abstract string `json:"abstract"`
			Year     string `json:"year"`
		} `json:"papers"`
		Topic   string `json:"topic"`
		Query   string `json:"query"`
		Context string `json:"context"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", nil)
		return
	}

	if h.llmClient == nil {
		WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "llm client unavailable", nil)
		return
	}

	topic := strings.TrimSpace(req.Topic)
	if topic == "" {
		topic = strings.TrimSpace(req.Query)
	}

	var prompt string
	switch {
	case len(req.Papers) > 0:
		for _, p := range req.Papers {
			if strings.TrimSpace(p.Title) == "" || strings.TrimSpace(p.Abstract) == "" {
				WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "each paper must have a title and abstract", nil)
				return
			}
		}

		var contextBuilder strings.Builder
		for i, p := range req.Papers {
			contextBuilder.WriteString(fmt.Sprintf("[%d] %s (%s): %s\n\n", i+1, p.Title, p.Year, p.Abstract))
		}

		prompt = fmt.Sprintf("Based on these research papers about '%s', identify the primary research gaps, unanswered questions, and future directions:\n\n%s",
			topic, contextBuilder.String())
	case strings.TrimSpace(req.Context) != "":
		prompt = fmt.Sprintf("Based on this research context about '%s', identify the primary research gaps, unanswered questions, and future directions:\n\n%s",
			topic, strings.TrimSpace(req.Context))
	default:
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "papers required", map[string]any{
			"field": "papers",
		})
		return
	}

	llmCtx, llmCancel := analysisLLMContext(r.Context())
	defer llmCancel()

	resp, err := h.llmClient.Generate(llmCtx, llm.ApplyGeneratePolicy(&llmv1.GenerateRequest{
		Prompt:      prompt,
		MaxTokens:   1500,
		Temperature: 0.3,
	}, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
		RequestedTier: "standard",
		TaskType:      "analysis",
	})))
	if err != nil {
		WriteError(w, http.StatusBadGateway, ErrDependencyFailed, "llm analysis failed", map[string]any{
			"error": err.Error(),
		})
		return
	}

	analysisText, err := normalizeGeneratedResponseText("llm analysis", resp)
	if err != nil {
		WriteError(w, http.StatusBadGateway, ErrDependencyFailed, "llm analysis returned empty text", map[string]any{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"text": analysisText})
}

func (h *AnalysisHandler) handleMethodology(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title    string `json:"title"`
		Abstract string `json:"abstract"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", nil)
		return
	}
	if h.llmClient == nil {
		WriteError(w, http.StatusServiceUnavailable, ErrDependencyFailed, "llm client unavailable", nil)
		return
	}

	prompt := fmt.Sprintf("Extract and summarize the core methodology, study design, and key variables from this paper title and abstract.\n\nTitle: %s\nAbstract: %s\n\n%s",
		req.Title, req.Abstract, structuredOutputSchemaInstruction)

	llmCtx, llmCancel := analysisLLMContext(r.Context())
	defer llmCancel()

	structuredClient := analysisStructuredClient(llmCtx, h.llmClient)
	resp, err := structuredClient.StructuredOutput(llmCtx, llm.ApplyStructuredPolicy(&llmv1.StructuredRequest{
		Prompt: prompt,
		Model:  llm.ResolveStandardModel(),
		JsonSchema: `{
			"type":"object",
			"properties":{
				"methodology":{"type":"string"},
				"studyDesign":{"type":"string"},
				"keyVariables":{"type":"array","items":{"type":"string"}}
			},
			"required":["methodology","studyDesign","keyVariables"]
		}`,
	}, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
		RequestedTier: "standard",
		Structured:    true,
		HighValue:     true,
	})))
	if err != nil {
		WriteError(w, http.StatusBadGateway, ErrDependencyFailed, "llm methodology extraction failed", map[string]any{
			"error": err.Error(),
		})
		return
	}

	var methodology any
	if err := json.Unmarshal([]byte(resp.JsonResult), &methodology); err != nil {
		WriteError(w, http.StatusBadGateway, ErrDependencyFailed, "llm methodology extraction returned invalid structured output", map[string]any{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"title":       req.Title,
		"methodology": methodology,
	})
}
