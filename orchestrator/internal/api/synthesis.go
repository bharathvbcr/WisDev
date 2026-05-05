package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

type SourcePaper struct {
	Title    string `json:"title"`
	Authors  string `json:"authors"`
	Year     string `json:"year"`
	Abstract string `json:"abstract"`
	DOI      string `json:"doi,omitempty"`
}

type GroundedCitation struct {
	InlineRef       string  `json:"inlineRef"`
	Title           string  `json:"title"`
	Authors         string  `json:"authors"`
	Year            string  `json:"year"`
	DOI             string  `json:"doi,omitempty"`
	S2PaperID       string  `json:"s2PaperId,omitempty"`
	Verified        bool    `json:"verified"`
	ConfidenceScore float64 `json:"confidenceScore"`
}

type CitationGrounder struct {
	s2Provider *search.SemanticScholarProvider
}

func NewCitationGrounder(s2Provider *search.SemanticScholarProvider) *CitationGrounder {
	return &CitationGrounder{s2Provider: s2Provider}
}

func (cg *CitationGrounder) GroundCitations(
	ctx context.Context,
	reviewText string,
	sourcePapers []SourcePaper,
) (groundingRatio float64, citations []GroundedCitation, err error) {
	if cg == nil || cg.s2Provider == nil || strings.TrimSpace(reviewText) == "" || len(sourcePapers) == 0 {
		return 0, []GroundedCitation{}, nil
	}

	inlineRefs := extractInlineRefs(reviewText)
	if len(inlineRefs) == 0 {
		return 0, []GroundedCitation{}, nil
	}

	citations = make([]GroundedCitation, len(inlineRefs))
	semaphore := make(chan struct{}, 3)
	var wg sync.WaitGroup

	for index, ref := range inlineRefs {
		wg.Add(1)
		go func(idx int, citationRef int) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			citations[idx] = cg.groundSingleCitation(ctx, citationRef, sourcePapers)
		}(index, ref)
	}

	wg.Wait()

	verifiedCount := 0
	for _, citation := range citations {
		if citation.Verified {
			verifiedCount++
		}
	}
	if len(citations) > 0 {
		groundingRatio = float64(verifiedCount) / float64(len(citations))
	}

	return groundingRatio, citations, nil
}

func (cg *CitationGrounder) groundSingleCitation(
	ctx context.Context,
	inlineRef int,
	sourcePapers []SourcePaper,
) GroundedCitation {
	citation := GroundedCitation{
		InlineRef:       fmt.Sprintf("[%d]", inlineRef),
		ConfidenceScore: 0,
	}
	if inlineRef < 1 || inlineRef > len(sourcePapers) {
		return citation
	}

	sourcePaper := sourcePapers[inlineRef-1]
	citation.Title = strings.TrimSpace(sourcePaper.Title)
	citation.Authors = strings.TrimSpace(sourcePaper.Authors)
	citation.Year = strings.TrimSpace(sourcePaper.Year)
	citation.DOI = strings.TrimSpace(sourcePaper.DOI)

	if normalized := normalizedDOI(citation.DOI); normalized != "" {
		doiCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		matchedPaper, err := cg.s2Provider.SearchByPaperID(doiCtx, "DOI:"+normalized)
		cancel()
		if err == nil && matchedPaper != nil {
			return groundedCitationFromPaper(citation, matchedPaper, 1.0, true)
		}
	}

	title := strings.TrimSpace(sourcePaper.Title)
	if title == "" {
		return citation
	}

	searchCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	matches, err := cg.s2Provider.Search(searchCtx, title, search.SearchOpts{Limit: 5})
	cancel()
	if err != nil || len(matches) == 0 {
		return citation
	}

	bestPaper, bestScore := bestCitationMatch(title, matches)
	if bestPaper == nil {
		return citation
	}

	return groundedCitationFromPaper(citation, bestPaper, bestScore, bestScore >= 0.85)
}

func groundedCitationFromPaper(
	base GroundedCitation,
	paper *search.Paper,
	score float64,
	verified bool,
) GroundedCitation {
	citation := base
	citation.ConfidenceScore = score
	citation.Verified = verified
	if paper == nil {
		return citation
	}
	if title := strings.TrimSpace(paper.Title); title != "" {
		citation.Title = title
	}
	if authors := strings.TrimSpace(strings.Join(paper.Authors, ", ")); authors != "" {
		citation.Authors = authors
	}
	if year := yearString(paper.Year); year != "" {
		citation.Year = year
	}
	if doi := strings.TrimSpace(paper.DOI); doi != "" {
		citation.DOI = doi
	}
	if paperID := strings.TrimSpace(strings.TrimPrefix(paper.ID, "s2:")); paperID != "" {
		citation.S2PaperID = paperID
	}
	return citation
}

func bestCitationMatch(sourceTitle string, matches []search.Paper) (*search.Paper, float64) {
	normalizedSource := normalizeTitle(sourceTitle)
	if normalizedSource == "" {
		return nil, 0
	}

	var bestPaper *search.Paper
	bestScore := 0.0
	for index := range matches {
		score := titleSimilarity(normalizedSource, normalizeTitle(matches[index].Title))
		if score > bestScore {
			bestScore = score
			bestPaper = &matches[index]
		}
	}
	return bestPaper, bestScore
}

func extractInlineRefs(reviewText string) []int {
	refPattern := regexp.MustCompile(`\[(\d+)\]`)
	matches := refPattern.FindAllStringSubmatch(reviewText, -1)
	if len(matches) == 0 {
		return nil
	}

	seen := make(map[int]struct{}, len(matches))
	refs := make([]int, 0, len(matches))
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		ref := 0
		fmt.Sscanf(match[1], "%d", &ref)
		if ref <= 0 {
			continue
		}
		if _, exists := seen[ref]; exists {
			continue
		}
		seen[ref] = struct{}{}
		refs = append(refs, ref)
	}
	sort.Ints(refs)
	return refs
}

func normalizedDOI(value string) string {
	doi := strings.ToLower(strings.TrimSpace(value))
	doi = strings.TrimPrefix(doi, "https://doi.org/")
	doi = strings.TrimPrefix(doi, "http://doi.org/")
	doi = strings.TrimPrefix(doi, "doi:")
	return doi
}

func normalizeTitle(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsSpace(r) {
			return r
		}
		return ' '
	}, value)), " ")
}

func titleSimilarity(left string, right string) float64 {
	if left == "" || right == "" {
		return 0
	}
	if left == right {
		return 1
	}
	distance := levenshteinDistance([]rune(left), []rune(right))
	maxLen := len([]rune(left))
	if otherLen := len([]rune(right)); otherLen > maxLen {
		maxLen = otherLen
	}
	if maxLen == 0 {
		return 1
	}
	score := 1 - (float64(distance) / float64(maxLen))
	if score < 0 {
		return 0
	}
	return score
}

func levenshteinDistance(left []rune, right []rune) int {
	if len(left) == 0 {
		return len(right)
	}
	if len(right) == 0 {
		return len(left)
	}

	prev := make([]int, len(right)+1)
	curr := make([]int, len(right)+1)
	for j := 0; j <= len(right); j++ {
		prev[j] = j
	}

	for i := 1; i <= len(left); i++ {
		curr[0] = i
		for j := 1; j <= len(right); j++ {
			cost := 0
			if left[i-1] != right[j-1] {
				cost = 1
			}
			curr[j] = minInt(
				curr[j-1]+1,
				prev[j]+1,
				prev[j-1]+cost,
			)
		}
		copy(prev, curr)
	}

	return prev[len(right)]
}

func minInt(values ...int) int {
	best := values[0]
	for _, value := range values[1:] {
		if value < best {
			best = value
		}
	}
	return best
}

func yearString(year int) string {
	if year <= 0 {
		return ""
	}
	return fmt.Sprintf("%d", year)
}

type SynthesisHandler struct {
	llmClient generateClient
	grounder  *CitationGrounder
}

var synthesisLLMRequestTimeout = 45 * time.Second

func NewSynthesisHandler(llmClient generateClient, grounder *CitationGrounder) *SynthesisHandler {
	return &SynthesisHandler{
		llmClient: llmClient,
		grounder:  grounder,
	}
}

func synthesisLLMContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if synthesisLLMRequestTimeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, synthesisLLMRequestTimeout)
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
		Papers []SourcePaper `json:"papers"`
		Topic  string        `json:"topic"`
		Style  string        `json:"style"` // "academic" | "accessible"
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

	llmCtx, llmCancel := synthesisLLMContext(r.Context())
	defer llmCancel()

	resp, err := h.llmClient.Generate(llmCtx, llm.ApplyGeneratePolicy(&llmv1.GenerateRequest{
		Prompt:      prompt,
		Temperature: 0.7,
		MaxTokens:   4096,
	}, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
		RequestedTier: "heavy",
		TaskType:      "synthesis",
	})))
	if err != nil {
		WriteError(w, http.StatusBadGateway, ErrDependencyFailed, "synthesis failed", map[string]any{
			"error": err.Error(),
		})
		return
	}

	reviewText, err := normalizeGeneratedResponseText("literature review synthesis", resp)
	if err != nil {
		WriteError(w, http.StatusBadGateway, ErrDependencyFailed, "synthesis returned empty text", map[string]any{
			"error": err.Error(),
		})
		return
	}

	groundingRatio, citations, _ := h.grounder.GroundCitations(r.Context(), reviewText, req.Papers)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"text":           reviewText,
		"groundingRatio": groundingRatio,
		"citations":      citations,
		"citationCount":  len(citations),
	})
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

	llmCtx, llmCancel := synthesisLLMContext(r.Context())
	defer llmCancel()

	resp, err := h.llmClient.Generate(llmCtx, llm.ApplyGeneratePolicy(&llmv1.GenerateRequest{
		Prompt:      prompt,
		Temperature: 0.5,
		MaxTokens:   maxTokens,
	}, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
		RequestedTier: "standard",
		TaskType:      "synthesis",
	})))
	if err != nil {
		WriteError(w, http.StatusBadGateway, ErrDependencyFailed, "summary failed", map[string]any{
			"error": err.Error(),
		})
		return
	}

	summaryText, err := normalizeGeneratedResponseText("paper summary", resp)
	if err != nil {
		WriteError(w, http.StatusBadGateway, ErrDependencyFailed, "summary returned empty text", map[string]any{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"text": summaryText})
}

func (h *SynthesisHandler) handleCompare(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Papers []struct {
			Title    string `json:"title"`
			Abstract string `json:"abstract"`
			Authors  string `json:"authors"`
			Year     string `json:"year"`
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

	llmCtx, llmCancel := synthesisLLMContext(r.Context())
	defer llmCancel()

	resp, err := h.llmClient.Generate(llmCtx, llm.ApplyGeneratePolicy(&llmv1.GenerateRequest{
		Prompt:      prompt,
		Temperature: 0.4,
		MaxTokens:   4096,
	}, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
		RequestedTier: "standard",
		TaskType:      "synthesis",
	})))
	if err != nil {
		WriteError(w, http.StatusBadGateway, ErrDependencyFailed, "comparison failed", map[string]any{
			"error": err.Error(),
		})
		return
	}

	comparisonText, err := normalizeGeneratedResponseText("paper comparison", resp)
	if err != nil {
		WriteError(w, http.StatusBadGateway, ErrDependencyFailed, "comparison returned empty text", map[string]any{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"text": comparisonText})
}
