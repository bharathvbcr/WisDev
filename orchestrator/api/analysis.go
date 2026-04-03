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

	llmv1 "github.com/wisdev-agent/wisdev-agent-os/orchestrator/proto/llm/v1"
)

type generateClient interface {
	Generate(ctx context.Context, req *llmv1.GenerateRequest) (*llmv1.GenerateResponse, error)
}

type AnalysisHandler struct {
	llmClient generateClient
}

func NewAnalysisHandler(llmClient generateClient) *AnalysisHandler {
	return &AnalysisHandler{llmClient: llmClient}
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
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "invalid action", map[string]any{
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

	resp, err := http.Get(oaUrl)
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

	var data struct {
		GroupBy []struct {
			Key   string `json:"key"`
			Count int    `json:"count"`
		} `json:"group_by"`
		Meta struct {
			Count int `json:"count"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		WriteError(w, http.StatusBadGateway, ErrDependencyFailed, "failed to decode openalex response", map[string]any{
			"error": err.Error(),
		})
		return
	}

	type yearData struct {
		Year  int `json:"year"`
		Count int `json:"count"`
	}
	var yearlyData []yearData
	for _, g := range data.GroupBy {
		year := 0
		fmt.Sscanf(g.Key, "%d", &year)
		yearlyData = append(yearlyData, yearData{Year: year, Count: g.Count})
	}
	sort.Slice(yearlyData, func(i, j int) bool {
		return yearlyData[i].Year < yearlyData[j].Year
	})

	peakYear := req.YearStart
	maxCount := -1
	for _, d := range yearlyData {
		if d.Count > maxCount {
			maxCount = d.Count
			peakYear = d.Year
		}
	}

	trend := "stable"
	if len(yearlyData) >= 3 {
		firstAvg := 0.0
		for i := 0; i < 3 && i < len(yearlyData); i++ {
			firstAvg += float64(yearlyData[i].Count)
		}
		firstAvg /= 3.0

		lastAvg := 0.0
		for i := 0; i < 3 && i < len(yearlyData); i++ {
			lastAvg += float64(yearlyData[len(yearlyData)-1-i].Count)
		}
		lastAvg /= 3.0

		if firstAvg > 0 {
			change := ((lastAvg - firstAvg) / firstAvg) * 100
			if change > 20 {
				trend = "growing"
			} else if change < -20 {
				trend = "declining"
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"query":      req.Query,
		"yearRange":  map[string]int{"start": req.YearStart, "end": req.YearEnd},
		"yearlyData": yearlyData,
		"totalWorks": data.Meta.Count,
		"peakYear":   peakYear,
		"trend":      trend,
	})
}

func (h *AnalysisHandler) handleGaps(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Papers []struct {
			Title    string `json:"title"`
			Abstract string `json:"abstract"`
			Year     string `json:"year"`
		} `json:"papers"`
		Topic string `json:"topic"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	if len(req.Papers) < 3 {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "at least 3 papers required", map[string]any{
			"field": "papers",
			"count": len(req.Papers),
		})
		return
	}

	var papersText strings.Builder
	for i, p := range req.Papers {
		fmt.Fprintf(&papersText, "[%d] %q (%s)\nAbstract: %s\n\n", i+1, p.Title, p.Year, p.Abstract)
	}

	prompt := fmt.Sprintf("Analyze these %d papers to identify research gaps.\n%s\n\nPapers:\n%s\n\nProvide: Research Landscape Summary, Identified Gaps (under-researched areas, methodological gaps, theoretical gaps), Contradictions, Suggested Research Questions, Priority Ranking.",
		len(req.Papers), req.Topic, papersText.String())

	resp, err := h.llmClient.Generate(r.Context(), &llmv1.GenerateRequest{
		Prompt:      prompt,
		Temperature: 0.5,
		MaxTokens:   4096,
	})
	if err != nil {
		WriteError(w, http.StatusBadGateway, ErrDependencyFailed, "llm analysis failed", map[string]any{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"text": resp.Text})
}

func (h *AnalysisHandler) handleMethodology(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title    string `json:"title"`
		Abstract string `json:"abstract"`
		FullText string `json:"fullText"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	content := ""
	if req.FullText != "" {
		limit := 15000
		if len(req.FullText) < limit {
			limit = len(req.FullText)
		}
		content = fmt.Sprintf("Title: %s\n\nFull Text:\n%s", req.Title, req.FullText[:limit])
	} else {
		content = fmt.Sprintf("Title: %s\n\nAbstract: %s", req.Title, req.Abstract)
	}

	prompt := fmt.Sprintf("Extract methodology details from this paper as JSON:\n%s\n\nReturn JSON with: studyDesign, sample, dataCollection, analysis, limitations, ethics, confidence (high/medium/low).", content)

	resp, err := h.llmClient.Generate(r.Context(), &llmv1.GenerateRequest{
		Prompt:      prompt,
		Temperature: 0.2,
		MaxTokens:   2048,
	})
	if err != nil {
		WriteError(w, http.StatusBadGateway, ErrDependencyFailed, "llm extraction failed", map[string]any{
			"error": err.Error(),
		})
		return
	}

	text := resp.Text
	var methodology map[string]any
	jsonStart := strings.Index(text, "{")
	jsonEnd := strings.LastIndex(text, "}")
	if jsonStart != -1 && jsonEnd != -1 && jsonEnd > jsonStart {
		json.Unmarshal([]byte(text[jsonStart:jsonEnd+1]), &methodology)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"title":       req.Title,
		"methodology": methodology,
		"rawAnalysis": text,
	})
}
