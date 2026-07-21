package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/llm"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/wisdev"
	llmv1 "github.com/bharathvbcr/wisdev-arc/orchestrator/proto/llm"
)

// ResearchHypothesisHandler owns the prompt/schema authorship for the hypothesis
// generator pipeline (hypothesisGeneratorService.ts) and the hypothesis tester
// workflow (hypothesisService.ts). Prompts, JSON schemas, and the end-to-end
// action=test orchestration (search fan-out, evidence batching, assess,
// suggest-experiments) live here. Thin clients POST and normalize the result.
//
// Route: POST /wisdev/research/hypothesis?action=<action>
// This is deliberately distinct from the existing /wisdev/hypothesis/ quest
// management subtree (HandleWisDevHypotheses).
//
// Generator actions: analyze-topic | generate-queries | analyze-literature |
// formulate | refine | generate
// Tester actions: parse-claims | tester-queries | analyze-evidence | assess |
// suggest-experiments | test
// End-to-end generator orchestration (search fan-out + formulate) is action=generate.
type ResearchHypothesisHandler struct {
	llmClient analysisClient
	db        wisdev.DBProvider
	// searchFn is injectable for tests; defaults to wisdev parallel search.
	searchFn func(ctx context.Context, query string, limit int) ([]hypothesisPaperLite, error)
}

// hypothesisPaperLite is the paper shape used for search + HypothesisResult evidence.
type hypothesisPaperLite struct {
	Title    string `json:"title"`
	Abstract string `json:"abstract,omitempty"`
	Summary  string `json:"summary,omitempty"`
	DOI      string `json:"doi,omitempty"`
	Link     string `json:"link"`
	PaperID  string `json:"paperId,omitempty"`
}

var errHypothesisLLMUnavailable = errors.New("llm client unavailable")

const hypothesisEvidenceSearchUnavailableMsg = "Hypothesis evidence search is currently unavailable for this request."

func NewResearchHypothesisHandler(llmClient analysisClient, db wisdev.DBProvider) *ResearchHypothesisHandler {
	return &ResearchHypothesisHandler{
		llmClient: llmClient,
		db:        db,
		searchFn:  defaultHypothesisSearch,
	}
}

func defaultHypothesisSearch(ctx context.Context, query string, limit int) ([]hypothesisPaperLite, error) {
	result, err := runModularParallelSearch(ctx, nil, wisdev.GlobalSearchRegistry, query, wisdev.SearchOptions{
		Limit: limit,
	})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, nil
	}
	papers := make([]hypothesisPaperLite, 0, len(result.Papers))
	for _, p := range result.Papers {
		abstract := strings.TrimSpace(p.Abstract)
		if abstract == "" {
			abstract = strings.TrimSpace(p.Summary)
		}
		link := strings.TrimSpace(p.Link)
		if link == "" {
			link = strings.TrimSpace(p.URL)
		}
		doi := strings.TrimSpace(p.DOI)
		if link == "" && doi != "" {
			link = "https://doi.org/" + doi
		}
		papers = append(papers, hypothesisPaperLite{
			Title:    strings.TrimSpace(p.Title),
			Abstract: abstract,
			Summary:  strings.TrimSpace(p.Summary),
			DOI:      doi,
			Link:     link,
			PaperID:  strings.TrimSpace(p.ID),
		})
	}
	return papers, nil
}

// RegisterResearchHypothesisRoutes registers the hypothesis prompt-authorship
// route on the shared mux. Kept in this file (rather than the frequently-churned
// wisdev_*_routes.go files) so the whole port lands as one self-contained unit.
func RegisterResearchHypothesisRoutes(mux *http.ServeMux, llmClient analysisClient, db wisdev.DBProvider) {
	handler := NewResearchHypothesisHandler(llmClient, db)
	mux.HandleFunc("/wisdev/research/hypothesis", handler.HandleResearchHypothesis)
}

// ---------------------------------------------------------------------------
// JSON schemas (ported 1:1 from the client DSL in
// frontend/services/hypothesisGeneratorService.ts). objectSchema in the DSL
// emits additionalProperties:false + propertyOrdering, so those are preserved
// here to keep the structured request byte-for-byte equivalent.
// ---------------------------------------------------------------------------

const researchHypothesisMethodologySchema = `{"type":"object","properties":{"studyType":{"type":"string","enum":["experimental","correlational","longitudinal","meta-analysis","qualitative","mixed-methods"]},"sampleDescription":{"type":"string","minLength":1},"dataCollectionMethod":{"type":"string","minLength":1},"analysisApproach":{"type":"string","minLength":1}},"required":["studyType","sampleDescription","dataCollectionMethod","analysisApproach"],"additionalProperties":false,"propertyOrdering":["studyType","sampleDescription","dataCollectionMethod","analysisApproach"]}`

const researchHypothesisTopicAnalysisSchema = `{"type":"object","properties":{"field":{"type":"string","minLength":1},"concepts":{"type":"array","items":{"type":"string","minLength":1}},"potentialIVs":{"type":"array","items":{"type":"string","minLength":1}},"potentialDVs":{"type":"array","items":{"type":"string","minLength":1}},"relatedConstructs":{"type":"array","items":{"type":"string","minLength":1}}},"required":["field","concepts","potentialIVs","potentialDVs","relatedConstructs"],"additionalProperties":false,"propertyOrdering":["field","concepts","potentialIVs","potentialDVs","relatedConstructs"]}`

const researchHypothesisSearchQueryArraySchema = `{"type":"array","items":{"type":"string","minLength":1}}`

const researchHypothesisLiteratureAnalysisSchema = `{"type":"object","properties":{"summary":{"type":"string","minLength":1},"gaps":{"type":"array","items":{"type":"string","minLength":1}},"methodologies":{"type":"array","items":{"type":"string","minLength":1}}},"required":["summary","gaps","methodologies"],"additionalProperties":false,"propertyOrdering":["summary","gaps","methodologies"]}`

var researchHypothesisDraftSchema = fmt.Sprintf(`{"type":"object","properties":{"statement":{"type":"string","minLength":1},"independentVariable":{"type":"string","minLength":1},"dependentVariable":{"type":"string","minLength":1},"controlVariables":{"type":"array","items":{"type":"string","minLength":1}},"suggestedMethodology":%s,"relevanceScore":{"type":"number","minimum":0,"maximum":100},"literatureSupport":{"type":"array","items":{"type":"integer","minimum":0}},"testability":{"type":"string","enum":["high","medium","low"]},"novelty":{"type":"string","enum":["high","medium","low"]},"status":{"type":"string"}},"required":["statement","independentVariable","dependentVariable","suggestedMethodology","relevanceScore","literatureSupport","testability","novelty"],"additionalProperties":false,"propertyOrdering":["statement","independentVariable","dependentVariable","controlVariables","suggestedMethodology","relevanceScore","literatureSupport","testability","novelty","status"]}`, researchHypothesisMethodologySchema)

var researchHypothesisFormulatedSchema = fmt.Sprintf(`{"type":"object","properties":{"hypotheses":{"type":"array","items":%s}},"required":["hypotheses"],"additionalProperties":false,"propertyOrdering":["hypotheses"]}`, researchHypothesisDraftSchema)

var researchHypothesisRefinedSchema = fmt.Sprintf(`{"type":"object","properties":{"statement":{"type":"string","minLength":1},"independentVariable":{"type":"string","minLength":1},"dependentVariable":{"type":"string","minLength":1},"controlVariables":{"type":"array","items":{"type":"string","minLength":1}},"suggestedMethodology":%s,"relevanceScore":{"type":"number","minimum":0,"maximum":100},"literatureSupport":{"type":"array","items":{"type":"object","properties":{"paperId":{"type":"string","minLength":1},"title":{"type":"string","minLength":1},"relevance":{"type":"string","minLength":1}},"required":["paperId","title","relevance"],"additionalProperties":false,"propertyOrdering":["paperId","title","relevance"]}},"testability":{"type":"string","enum":["high","medium","low"]},"novelty":{"type":"string","enum":["high","medium","low"]},"status":{"type":"string"}},"required":[],"additionalProperties":false,"propertyOrdering":["statement","independentVariable","dependentVariable","controlVariables","suggestedMethodology","relevanceScore","literatureSupport","testability","novelty","status"]}`, researchHypothesisMethodologySchema)

// Tester schemas ported from hypothesisService.ts (objectSchema DSL).
const researchHypothesisClaimsSchema = `{"type":"object","properties":{"claims":{"type":"array","items":{"type":"string","minLength":1}}},"required":["claims"],"additionalProperties":false}`

const researchHypothesisTesterQueriesSchema = `{"type":"object","properties":{"queries":{"type":"array","items":{"type":"string","minLength":1}}},"required":["queries"],"additionalProperties":false}`

const researchHypothesisEvidenceAnalysisSchema = `{"type":"object","properties":{"papers":{"type":"array","items":{"type":"object","properties":{"index":{"type":"integer","minimum":1},"stance":{"type":"string","enum":["supporting","contradicting","neutral"]},"relevance":{"type":"number","minimum":0,"maximum":1},"quote":{"type":"string"},"reasoning":{"type":"string"}},"required":["index","stance","relevance"],"additionalProperties":false}}},"required":["papers"],"additionalProperties":false}`

const researchHypothesisAssessmentSchema = `{"type":"object","properties":{"verdict":{"type":"string","enum":["strongly_supported","partially_supported","inconclusive","partially_contradicted","strongly_contradicted"]},"confidence":{"type":"number","minimum":0,"maximum":1},"summary":{"type":"string","minLength":1},"caveats":{"type":"array","items":{"type":"string"}}},"required":["verdict","confidence","summary","caveats"],"additionalProperties":false}`

const researchHypothesisExperimentsSchema = `{"type":"object","properties":{"experiments":{"type":"array","items":{"type":"string","minLength":1}}},"required":["experiments"],"additionalProperties":false}`

// Assessment-stage statistics prompt (owned by Go; formerly mirrored from academicPrompts.ts).
const researchHypothesisStatisticsPrompt = `You are a statistical analysis expert. Interpret and explain statistical findings with clarity and precision.

REPORTING CONVENTIONS:
- Report exact p-values (p = .023) rather than thresholds (p < .05)
- Include effect sizes with confidence intervals
- Use standard notation: M, SD, r, β, F, t, χ², η², d
- Report degrees of freedom: t(45) = 2.34, F(2, 87) = 4.56

INTERPRETATION GUIDELINES:
- Effect sizes: d = 0.2 (small), 0.5 (medium), 0.8 (large)
- Correlations: r = .1 (small), .3 (medium), .5 (large)
- Distinguish statistical from practical significance
- Note power limitations for non-significant findings

LANGUAGE:
- "Results revealed a significant main effect..."
- "Post-hoc comparisons indicated..."
- "The effect remained robust after controlling for..."
- "These findings should be interpreted with caution given..."

AVOID:
- "The results proved that..."
- "This definitively shows..."
- Causal claims from correlational data`

// ---------------------------------------------------------------------------
// Dispatch
// ---------------------------------------------------------------------------

func (h *ResearchHypothesisHandler) HandleResearchHypothesis(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}


	action := r.URL.Query().Get("action")
	logAPIRouteLifecycle(r, "api.research_hypothesis", "research_hypothesis."+action, "request_received", "", "result", "accepted")

	switch action {
	case "analyze-topic":
		h.handleAnalyzeTopic(w, r)
	case "generate-queries":
		h.handleGenerateQueries(w, r)
	case "analyze-literature":
		h.handleAnalyzeLiterature(w, r)
	case "formulate":
		h.handleFormulate(w, r)
	case "refine":
		h.handleRefine(w, r)
	case "parse-claims":
		h.handleParseClaims(w, r)
	case "tester-queries":
		h.handleTesterQueries(w, r)
	case "analyze-evidence":
		h.handleAnalyzeEvidence(w, r)
	case "assess":
		h.handleAssess(w, r)
	case "suggest-experiments":
		h.handleSuggestExperiments(w, r)
	case "test":
		h.handleTestHypothesis(w, r)
	case "generate":
		h.handleGenerateHypotheses(w, r)
	default:
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "invalid or missing action", map[string]any{
			"allowedActions": []string{
				"analyze-topic", "generate-queries", "analyze-literature", "formulate", "refine",
				"parse-claims", "tester-queries", "analyze-evidence", "assess", "suggest-experiments",
				"test", "generate",
			},
		})
	}
}

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------

func (h *ResearchHypothesisHandler) handleAnalyzeTopic(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Topic string `json:"topic"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", nil)
		return
	}
	if strings.TrimSpace(req.Topic) == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "topic required", map[string]any{"field": "topic"})
		return
	}

	prompt := fmt.Sprintf(`Analyze this research topic and extract key information for hypothesis generation.

TOPIC: "%s"

Extract the analysis using the supplied structured output schema.
- field: primary research field
- concepts: 3-5 key concepts from the topic
- potentialIVs: 2-4 plausible independent variables
- potentialDVs: 2-4 plausible dependent variables
- relatedConstructs: 2-4 related constructs or frameworks`, req.Topic)

	h.runStructured(w, r, prompt, researchHypothesisTopicAnalysisSchema, "light")
}

func (h *ResearchHypothesisHandler) handleGenerateQueries(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Topic        string   `json:"topic"`
		Field        string   `json:"field"`
		Concepts     []string `json:"concepts"`
		PotentialIVs []string `json:"potentialIVs"`
		PotentialDVs []string `json:"potentialDVs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", nil)
		return
	}
	if strings.TrimSpace(req.Topic) == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "topic required", map[string]any{"field": "topic"})
		return
	}

	variables := append(append([]string{}, req.PotentialIVs...), req.PotentialDVs...)
	prompt := fmt.Sprintf(`Generate 4-6 search queries to find relevant academic literature for hypothesis generation.

TOPIC: "%s"
FIELD: %s
KEY CONCEPTS: %s
POTENTIAL VARIABLES: %s

Return 4-6 concise search queries using the supplied structured output schema.`,
		req.Topic, req.Field, strings.Join(req.Concepts, ", "), strings.Join(variables, ", "))

	h.runStructured(w, r, prompt, researchHypothesisSearchQueryArraySchema, "light")
}

func (h *ResearchHypothesisHandler) handleAnalyzeLiterature(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Topic  string `json:"topic"`
		Field  string `json:"field"`
		Papers []struct {
			Title string `json:"title"`
			Date  string `json:"date"`
		} `json:"papers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", nil)
		return
	}

	limit := len(req.Papers)
	if limit > 20 {
		limit = 20
	}
	var summaries strings.Builder
	for i := 0; i < limit; i++ {
		date := strings.TrimSpace(req.Papers[i].Date)
		if date == "" {
			date = "n.d."
		}
		if i > 0 {
			summaries.WriteString("\n")
		}
		summaries.WriteString(fmt.Sprintf(`%d. "%s" (%s)`, i+1, req.Papers[i].Title, date))
	}

	prompt := fmt.Sprintf(`Analyze this collection of papers related to the research topic and identify:
1. A brief summary of the current state of research
2. Research gaps and opportunities
3. Common methodological approaches

TOPIC: "%s"
FIELD: %s

PAPERS:
%s

Provide the summary, gaps, and common methodologies using the supplied structured output schema.`,
		req.Topic, req.Field, summaries.String())

	h.runStructured(w, r, prompt, researchHypothesisLiteratureAnalysisSchema, "light")
}

func (h *ResearchHypothesisHandler) handleFormulate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Topic   string `json:"topic"`
		Field   string `json:"field"`
		Summary string `json:"summary"`
		Count   int    `json:"count"`
		Papers  []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"papers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", nil)
		return
	}
	count := req.Count
	if count <= 0 {
		count = 4 // DEFAULT_HYPOTHESIS_COUNT
	}

	var refs strings.Builder
	for i, p := range req.Papers {
		if i > 0 {
			refs.WriteString("\n")
		}
		refs.WriteString(fmt.Sprintf("[%d] %s", i, p.Title))
	}

	prompt := fmt.Sprintf(`You are an expert research scientist. Generate %d testable hypotheses based on this context.

TOPIC: "%s"
FIELD: %s

LITERATURE SUMMARY: %s

AVAILABLE PAPERS FOR REFERENCES:
%s

Generate %d hypotheses using the supplied structured output schema.
- literatureSupport should reference the zero-based paper indexes shown above.
- Prefer precise, testable statements over broad topic summaries.`,
		count, req.Topic, req.Field, req.Summary, refs.String(), count)

	h.runStructured(w, r, prompt, researchHypothesisFormulatedSchema, "heavy")
}

func (h *ResearchHypothesisHandler) handleRefine(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Statement string `json:"statement"`
		Feedback  string `json:"feedback"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", nil)
		return
	}

	prompt := fmt.Sprintf(`Refine this research hypothesis based on user feedback.
ORIGINAL: "%s"
FEEDBACK: "%s"

Return the refined hypothesis using the supplied structured output schema.`,
		req.Statement, req.Feedback)

	h.runStructured(w, r, prompt, researchHypothesisRefinedSchema, "heavy")
}

// handleGenerateHypotheses runs the full generator pipeline end-to-end
// (analyze-topic → generate-queries → search → analyze-literature → formulate)
// previously orchestrated in frontend/services/hypothesisGeneratorService.ts.
func (h *ResearchHypothesisHandler) handleGenerateHypotheses(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Topic     string `json:"topic"`
		FocusArea string `json:"focusArea"`
		Count     int    `json:"count"`
		MaxPapers int    `json:"maxPapers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", nil)
		return
	}
	topic := strings.TrimSpace(req.Topic)
	if topic == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "topic required", map[string]any{"field": "topic"})
		return
	}
	fullTopic := topic
	if focus := strings.TrimSpace(req.FocusArea); focus != "" {
		fullTopic = fmt.Sprintf("%s (Focus: %s)", topic, focus)
	}
	count := req.Count
	if count <= 0 {
		count = 4
	}
	maxPapers := req.MaxPapers
	if maxPapers <= 0 {
		maxPapers = 30
	}
	if maxPapers > 60 {
		maxPapers = 60
	}

	ctx := r.Context()
	if h.searchFn == nil {
		h.searchFn = defaultHypothesisSearch
	}

	// 1. Analyze topic
	analyzePrompt := fmt.Sprintf(`Analyze this research topic and extract key information for hypothesis generation.

TOPIC: "%s"

Extract the analysis using the supplied structured output schema.
- field: primary research field
- concepts: 3-5 key concepts from the topic
- potentialIVs: 2-4 plausible independent variables
- potentialDVs: 2-4 plausible dependent variables
- relatedConstructs: 2-4 related constructs or frameworks`, fullTopic)
	analysisRaw, err := h.structuredJSON(ctx, analyzePrompt, researchHypothesisTopicAnalysisSchema, "light")
	if err != nil {
		h.writeStructuredError(w, err)
		return
	}
	if err := hypothesisCheckCancelled(w, ctx); err != nil {
		return
	}
	analysis := decodeHypothesisTopicAnalysis(analysisRaw, topic)

	// 2. Search queries
	variables := append(append([]string{}, analysis.PotentialIVs...), analysis.PotentialDVs...)
	queriesPrompt := fmt.Sprintf(`Generate 4-6 search queries to find relevant academic literature for hypothesis generation.

TOPIC: "%s"
FIELD: %s
KEY CONCEPTS: %s
POTENTIAL VARIABLES: %s

Return 4-6 concise search queries using the supplied structured output schema.`,
		topic, analysis.Field, strings.Join(analysis.Concepts, ", "), strings.Join(variables, ", "))
	queriesRaw, err := h.structuredJSON(ctx, queriesPrompt, researchHypothesisSearchQueryArraySchema, "light")
	if err != nil {
		h.writeStructuredError(w, err)
		return
	}
	if err := hypothesisCheckCancelled(w, ctx); err != nil {
		return
	}
	queries := normalizeHypothesisStringList(decodeHypothesisQueryArray(queriesRaw), []string{topic, analysis.Field + " research"})
	if len(queries) > 6 {
		queries = queries[:6]
	}

	// 3. Parallel literature search
	type searchOutcome struct {
		papers []hypothesisPaperLite
	}
	outcomes := make([]searchOutcome, len(queries))
	var wg sync.WaitGroup
	for i, q := range queries {
		wg.Add(1)
		go func(idx int, query string) {
			defer wg.Done()
			papers, searchErr := h.searchFn(ctx, query, 15)
			if searchErr != nil {
				return
			}
			outcomes[idx] = searchOutcome{papers: papers}
		}(i, q)
	}
	wg.Wait()
	if err := hypothesisCheckCancelled(w, ctx); err != nil {
		return
	}

	seen := map[string]bool{}
	allPapers := make([]hypothesisPaperLite, 0)
	for _, outcome := range outcomes {
		for _, paper := range outcome.papers {
			key := strings.ToLower(strings.TrimSpace(paper.DOI))
			if key == "" {
				key = strings.ToLower(strings.TrimSpace(paper.Title))
			}
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			allPapers = append(allPapers, paper)
			if len(allPapers) >= maxPapers {
				break
			}
		}
		if len(allPapers) >= maxPapers {
			break
		}
	}

	// 4. Analyze literature
	var litBlock strings.Builder
	limit := len(allPapers)
	if limit > 20 {
		limit = 20
	}
	for i := 0; i < limit; i++ {
		p := allPapers[i]
		fmt.Fprintf(&litBlock, "- %s\n", p.Title)
	}
	litPrompt := fmt.Sprintf(`Analyze this collection of papers related to the research topic and identify:
1. A brief summary of the current state of research
2. Research gaps and opportunities
3. Common methodological approaches

TOPIC: "%s"
FIELD: %s

PAPERS:
%s

Provide the summary, gaps, and common methodologies using the supplied structured output schema.`,
		topic, analysis.Field, litBlock.String())
	litRaw, err := h.structuredJSON(ctx, litPrompt, researchHypothesisLiteratureAnalysisSchema, "light")
	if err != nil {
		h.writeStructuredError(w, err)
		return
	}
	if err := hypothesisCheckCancelled(w, ctx); err != nil {
		return
	}
	lit := decodeHypothesisLiterature(litRaw)

	// 5. Formulate hypotheses
	paperRefs := make([]struct {
		ID    string
		Title string
	}, 0, 15)
	var refs strings.Builder
	for i, p := range allPapers {
		if i >= 15 {
			break
		}
		id := strings.TrimSpace(p.PaperID)
		if id == "" {
			id = strings.TrimSpace(p.DOI)
		}
		if id == "" {
			id = fmt.Sprintf("paper_%d", i)
		}
		paperRefs = append(paperRefs, struct {
			ID    string
			Title string
		}{ID: id, Title: p.Title})
		if i > 0 {
			refs.WriteString("\n")
		}
		refs.WriteString(fmt.Sprintf("[%d] %s", i, p.Title))
	}
	formulatePrompt := fmt.Sprintf(`You are an expert research scientist. Generate %d testable hypotheses based on this context.

TOPIC: "%s"
FIELD: %s

LITERATURE SUMMARY: %s

AVAILABLE PAPERS FOR REFERENCES:
%s

Generate %d hypotheses using the supplied structured output schema.
- literatureSupport should reference the zero-based paper indexes shown above.
- Prefer precise, testable statements over broad topic summaries.`,
		count, topic, analysis.Field, lit.Summary, refs.String(), count)
	formulateRaw, err := h.structuredJSON(ctx, formulatePrompt, researchHypothesisFormulatedSchema, "heavy")
	if err != nil {
		h.writeStructuredError(w, err)
		return
	}
	if err := hypothesisCheckCancelled(w, ctx); err != nil {
		return
	}

	hypotheses := mapFormulatedHypotheses(formulateRaw, paperRefs)
	fieldContext := analysis.Field
	if len(analysis.Concepts) > 0 {
		fieldContext = analysis.Field + ": " + strings.Join(analysis.Concepts, ", ")
	}

	writeEnvelope(w, "hypothesisGeneration", map[string]any{
		"inputTopic":        topic,
		"fieldContext":      fieldContext,
		"literatureSummary": lit.Summary,
		"gaps":              lit.Gaps,
		"hypotheses":        hypotheses,
		"sourcePapersCount": len(allPapers),
	})
}

type hypothesisTopicAnalysisDecoded struct {
	Field             string   `json:"field"`
	Concepts          []string `json:"concepts"`
	PotentialIVs      []string `json:"potentialIVs"`
	PotentialDVs      []string `json:"potentialDVs"`
	RelatedConstructs []string `json:"relatedConstructs"`
}

type hypothesisLiteratureDecoded struct {
	Summary       string   `json:"summary"`
	Gaps          []string `json:"gaps"`
	Methodologies []string `json:"methodologies"`
}

func decodeHypothesisTopicAnalysis(raw json.RawMessage, fallbackTopic string) hypothesisTopicAnalysisDecoded {
	var parsed hypothesisTopicAnalysisDecoded
	_ = json.Unmarshal(raw, &parsed)
	if strings.TrimSpace(parsed.Field) == "" {
		parsed.Field = "General Science"
	}
	if len(parsed.Concepts) == 0 {
		parsed.Concepts = []string{fallbackTopic}
	}
	if parsed.PotentialIVs == nil {
		parsed.PotentialIVs = []string{}
	}
	if parsed.PotentialDVs == nil {
		parsed.PotentialDVs = []string{}
	}
	if parsed.RelatedConstructs == nil {
		parsed.RelatedConstructs = []string{}
	}
	return parsed
}

func decodeHypothesisQueryArray(raw json.RawMessage) []string {
	var queries []string
	if err := json.Unmarshal(raw, &queries); err == nil {
		return queries
	}
	var wrapped struct {
		Queries []string `json:"queries"`
	}
	_ = json.Unmarshal(raw, &wrapped)
	return wrapped.Queries
}

func decodeHypothesisLiterature(raw json.RawMessage) hypothesisLiteratureDecoded {
	var parsed hypothesisLiteratureDecoded
	_ = json.Unmarshal(raw, &parsed)
	if strings.TrimSpace(parsed.Summary) == "" {
		parsed.Summary = "Summary unavailable."
	}
	if parsed.Gaps == nil {
		parsed.Gaps = []string{}
	}
	if parsed.Methodologies == nil {
		parsed.Methodologies = []string{}
	}
	return parsed
}

func mapFormulatedHypotheses(raw json.RawMessage, paperRefs []struct {
	ID    string
	Title string
}) []map[string]any {
	var parsed struct {
		Hypotheses []struct {
			Statement             string   `json:"statement"`
			IndependentVariable   string   `json:"independentVariable"`
			DependentVariable     string   `json:"dependentVariable"`
			ControlVariables      []string `json:"controlVariables"`
			SuggestedMethodology  any      `json:"suggestedMethodology"`
			RelevanceScore        float64  `json:"relevanceScore"`
			LiteratureSupport     []int    `json:"literatureSupport"`
			Testability           string   `json:"testability"`
			Novelty               string   `json:"novelty"`
			Status                string   `json:"status"`
		} `json:"hypotheses"`
	}
	_ = json.Unmarshal(raw, &parsed)
	out := make([]map[string]any, 0, len(parsed.Hypotheses))
	for i, h := range parsed.Hypotheses {
		support := make([]map[string]string, 0, len(h.LiteratureSupport))
		for _, idx := range h.LiteratureSupport {
			if idx < 0 || idx >= len(paperRefs) {
				continue
			}
			support = append(support, map[string]string{
				"paperId":   paperRefs[idx].ID,
				"title":     paperRefs[idx].Title,
				"relevance": "Supporting evidence",
			})
		}
		id := fmt.Sprintf("hyp_%d_%d", timeNowUnixMilli(), i)
		item := map[string]any{
			"id":                    id,
			"statement":             h.Statement,
			"independentVariable":   h.IndependentVariable,
			"dependentVariable":     h.DependentVariable,
			"controlVariables":      h.ControlVariables,
			"suggestedMethodology":  h.SuggestedMethodology,
			"relevanceScore":        h.RelevanceScore,
			"literatureSupport":     support,
			"testability":           h.Testability,
			"novelty":               h.Novelty,
		}
		if strings.TrimSpace(h.Status) != "" {
			item["status"] = h.Status
		}
		out = append(out, item)
	}
	return out
}

// timeNowUnixMilli is a tiny indirection for tests.
var timeNowUnixMilli = func() int64 {
	return time.Now().UTC().UnixMilli()
}

// ---------------------------------------------------------------------------
// Tester actions (hypothesisService.ts)
// ---------------------------------------------------------------------------

func (h *ResearchHypothesisHandler) handleParseClaims(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Hypothesis string `json:"hypothesis"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", nil)
		return
	}
	if strings.TrimSpace(req.Hypothesis) == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "hypothesis required", map[string]any{"field": "hypothesis"})
		return
	}

	prompt := fmt.Sprintf(`Parse this research hypothesis into specific, testable claims:

Hypothesis: "%s"

Break it down into 2-4 specific empirical claims that can be verified or refuted by research.

Return the claims using the supplied structured output schema.`, req.Hypothesis)

	h.runStructured(w, r, prompt, researchHypothesisClaimsSchema, "heavy")
}

func (h *ResearchHypothesisHandler) handleTesterQueries(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Hypothesis string   `json:"hypothesis"`
		Claims     []string `json:"claims"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", nil)
		return
	}
	if strings.TrimSpace(req.Hypothesis) == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "hypothesis required", map[string]any{"field": "hypothesis"})
		return
	}

	prompt := fmt.Sprintf(`Generate search queries to find research papers testing this hypothesis:

Hypothesis: "%s"
Claims: %s

Generate 3-5 search queries that would find:
1. Papers supporting this hypothesis
2. Papers contradicting this hypothesis
3. Papers with relevant experimental data

Return the queries using the supplied structured output schema.`,
		req.Hypothesis, strings.Join(req.Claims, "; "))

	h.runStructured(w, r, prompt, researchHypothesisTesterQueriesSchema, "heavy")
}

func (h *ResearchHypothesisHandler) handleAnalyzeEvidence(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Hypothesis string `json:"hypothesis"`
		Papers     []struct {
			Title    string `json:"title"`
			Abstract string `json:"abstract"`
			Summary  string `json:"summary"`
		} `json:"papers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", nil)
		return
	}
	if strings.TrimSpace(req.Hypothesis) == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "hypothesis required", map[string]any{"field": "hypothesis"})
		return
	}
	if len(req.Papers) == 0 {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "papers required", map[string]any{"field": "papers"})
		return
	}
	if len(req.Papers) > 10 {
		req.Papers = req.Papers[:10]
	}

	var paperBlock strings.Builder
	for i, p := range req.Papers {
		abstract := p.Summary
		if strings.TrimSpace(abstract) == "" {
			abstract = p.Abstract
		}
		if strings.TrimSpace(abstract) == "" {
			abstract = "No abstract"
		}
		if len(abstract) > 400 {
			abstract = abstract[:400]
		}
		if i > 0 {
			paperBlock.WriteString("\n")
		}
		paperBlock.WriteString(fmt.Sprintf(`
[%d] "%s"
Abstract: %s
`, i+1, p.Title, abstract))
	}

	prompt := fmt.Sprintf(`Analyze how these papers relate to the hypothesis: "%s"

Papers:
%s
For each paper, determine:
1. Stance: "supporting" (provides evidence for), "contradicting" (provides evidence against), or "neutral" (not directly relevant)
2. Relevance: 0-1 score (how relevant to the hypothesis)
3. Quote: A key finding or claim from the paper (if stance is not neutral)
4. Reasoning: Brief explanation of why this supports/contradicts

Return the paper analyses using the supplied structured output schema.`,
		req.Hypothesis, paperBlock.String())

	h.runStructured(w, r, prompt, researchHypothesisEvidenceAnalysisSchema, "heavy")
}

func (h *ResearchHypothesisHandler) handleAssess(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Hypothesis    string `json:"hypothesis"`
		Supporting    []struct {
			Title     string `json:"title"`
			Reasoning string `json:"reasoning"`
		} `json:"supporting"`
		Contradicting []struct {
			Title     string `json:"title"`
			Reasoning string `json:"reasoning"`
		} `json:"contradicting"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", nil)
		return
	}
	if strings.TrimSpace(req.Hypothesis) == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "hypothesis required", map[string]any{"field": "hypothesis"})
		return
	}

	var supportingBlock strings.Builder
	limit := len(req.Supporting)
	if limit > 5 {
		limit = 5
	}
	for i := 0; i < limit; i++ {
		supportingBlock.WriteString(fmt.Sprintf("- %s: %s\n", req.Supporting[i].Title, req.Supporting[i].Reasoning))
	}

	var contradictingBlock strings.Builder
	limit = len(req.Contradicting)
	if limit > 5 {
		limit = 5
	}
	for i := 0; i < limit; i++ {
		contradictingBlock.WriteString(fmt.Sprintf("- %s: %s\n", req.Contradicting[i].Title, req.Contradicting[i].Reasoning))
	}

	prompt := fmt.Sprintf(`%s

---

Assess the overall evidence for this hypothesis:

Hypothesis: "%s"

Supporting evidence (%d papers):
%s
Contradicting evidence (%d papers):
%s
Provide a rigorous academic assessment:
1. Verdict: strongly_supported, partially_supported, inconclusive, partially_contradicted, or strongly_contradicted
2. Confidence: 0-1 (based on evidence quality, not just quantity)
3. Summary: 2-3 sentences using hedging language ("The evidence suggests...", "Findings indicate...")
4. Caveats: Key methodological limitations or considerations

CRITICAL: Use academic language. Avoid "proves" or absolute statements. Acknowledge uncertainty.
Return the assessment using the supplied structured output schema.`,
		researchHypothesisStatisticsPrompt,
		req.Hypothesis,
		len(req.Supporting), supportingBlock.String(),
		len(req.Contradicting), contradictingBlock.String())

	h.runStructured(w, r, prompt, researchHypothesisAssessmentSchema, "heavy")
}

func (h *ResearchHypothesisHandler) handleSuggestExperiments(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Hypothesis string `json:"hypothesis"`
		HasGaps    bool   `json:"hasGaps"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", nil)
		return
	}
	if strings.TrimSpace(req.Hypothesis) == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "hypothesis required", map[string]any{"field": "hypothesis"})
		return
	}

	gapsNote := ""
	if req.HasGaps {
		gapsNote = "Note: There are significant gaps in the literature on this topic."
	}

	prompt := fmt.Sprintf(`Based on the hypothesis and existing evidence, suggest experiments to further test it:

Hypothesis: "%s"

%s

Suggest 3-4 specific experiments or studies that could:
1. Provide stronger evidence for or against the hypothesis
2. Address limitations in existing research
3. Explore unexplored aspects

Return the experiments using the supplied structured output schema.`,
		req.Hypothesis, gapsNote)

	h.runStructured(w, r, prompt, researchHypothesisExperimentsSchema, "heavy")
}

// ---------------------------------------------------------------------------
// End-to-end tester workflow (action=test)
// ---------------------------------------------------------------------------

type hypothesisEvidenceItem struct {
	Paper     hypothesisPaperLite `json:"paper"`
	Stance    string              `json:"stance"`
	Relevance float64             `json:"relevance"`
	Quote     string              `json:"quote"`
	Reasoning string              `json:"reasoning"`
}

type hypothesisOverallAssessment struct {
	Verdict    string   `json:"verdict"`
	Confidence float64  `json:"confidence"`
	Summary    string   `json:"summary"`
	Caveats    []string `json:"caveats"`
}

type hypothesisTestResult struct {
	Hypothesis            string                       `json:"hypothesis"`
	ParsedClaims          []string                     `json:"parsedClaims"`
	SupportingEvidence    []hypothesisEvidenceItem     `json:"supportingEvidence"`
	ContradictingEvidence []hypothesisEvidenceItem     `json:"contradictingEvidence"`
	NeutralEvidence       []hypothesisEvidenceItem     `json:"neutralEvidence"`
	OverallAssessment     hypothesisOverallAssessment  `json:"overallAssessment"`
	SuggestedExperiments  []string                     `json:"suggestedExperiments"`
}

func (h *ResearchHypothesisHandler) handleTestHypothesis(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Hypothesis string `json:"hypothesis"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", nil)
		return
	}
	hypothesis := strings.TrimSpace(req.Hypothesis)
	if hypothesis == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "hypothesis required", map[string]any{"field": "hypothesis"})
		return
	}

	ctx := r.Context()
	if h.searchFn == nil {
		h.searchFn = defaultHypothesisSearch
	}

	// 1. Parse claims
	claimsPrompt := fmt.Sprintf(`Parse this research hypothesis into specific, testable claims:

Hypothesis: "%s"

Break it down into 2-4 specific empirical claims that can be verified or refuted by research.

Return the claims using the supplied structured output schema.`, hypothesis)
	claimsRaw, err := h.structuredJSON(ctx, claimsPrompt, researchHypothesisClaimsSchema, "heavy")
	if err != nil {
		h.writeStructuredError(w, err)
		return
	}
	if err := hypothesisCheckCancelled(w, ctx); err != nil {
		return
	}
	claims := normalizeHypothesisStringList(decodeHypothesisClaims(claimsRaw), []string{hypothesis})

	// 2. Tester queries
	queriesPrompt := fmt.Sprintf(`Generate search queries to find research papers testing this hypothesis:

Hypothesis: "%s"
Claims: %s

Generate 3-5 search queries that would find:
1. Papers supporting this hypothesis
2. Papers contradicting this hypothesis
3. Papers with relevant experimental data

Return the queries using the supplied structured output schema.`,
		hypothesis, strings.Join(claims, "; "))
	queriesRaw, err := h.structuredJSON(ctx, queriesPrompt, researchHypothesisTesterQueriesSchema, "heavy")
	if err != nil {
		h.writeStructuredError(w, err)
		return
	}
	if err := hypothesisCheckCancelled(w, ctx); err != nil {
		return
	}
	queries := normalizeHypothesisStringList(decodeHypothesisQueries(queriesRaw), []string{hypothesis})
	if len(queries) > 3 {
		queries = queries[:3]
	}

	// 3. Parallel literature search (up to 3 queries × 15)
	type searchOutcome struct {
		papers []hypothesisPaperLite
		failed bool
	}
	outcomes := make([]searchOutcome, len(queries))
	var wg sync.WaitGroup
	for i, q := range queries {
		wg.Add(1)
		go func(idx int, query string) {
			defer wg.Done()
			if ctx.Err() != nil {
				outcomes[idx] = searchOutcome{failed: true}
				return
			}
			papers, searchErr := h.searchFn(ctx, query, 15)
			if searchErr != nil {
				outcomes[idx] = searchOutcome{failed: true}
				return
			}
			outcomes[idx] = searchOutcome{papers: papers}
		}(i, q)
	}
	wg.Wait()
	if err := hypothesisCheckCancelled(w, ctx); err != nil {
		return
	}

	resolved := 0
	failed := 0
	var allPapers []hypothesisPaperLite
	for _, o := range outcomes {
		if o.failed {
			failed++
			continue
		}
		resolved++
		allPapers = append(allPapers, o.papers...)
	}
	if resolved == 0 && failed > 0 {
		WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, hypothesisEvidenceSearchUnavailableMsg, map[string]any{
			"failedSearchCount": failed,
		})
		return
	}

	uniquePapers := dedupeHypothesisPapers(allPapers, 30)

	// 4. Analyze evidence in batches of 5
	evidence := h.analyzeEvidenceBatches(ctx, hypothesis, uniquePapers)

	// 5. Assess
	supporting := filterHypothesisEvidence(evidence, "supporting")
	contradicting := filterHypothesisEvidence(evidence, "contradicting")
	neutral := filterHypothesisEvidence(evidence, "neutral")

	assessment, err := h.runHypothesisAssessment(ctx, hypothesis, supporting, contradicting)
	if err != nil {
		h.writeStructuredError(w, err)
		return
	}
	if err := hypothesisCheckCancelled(w, ctx); err != nil {
		return
	}

	// 6. Suggest experiments
	hasGaps := len(neutral) > len(evidence)/2
	experiments, err := h.runHypothesisExperiments(ctx, hypothesis, hasGaps)
	if err != nil {
		h.writeStructuredError(w, err)
		return
	}

	result := hypothesisTestResult{
		Hypothesis:            hypothesis,
		ParsedClaims:          claims,
		SupportingEvidence:    supporting,
		ContradictingEvidence: contradicting,
		NeutralEvidence:       neutral,
		OverallAssessment:     assessment,
		SuggestedExperiments:  experiments,
	}
	if result.SupportingEvidence == nil {
		result.SupportingEvidence = []hypothesisEvidenceItem{}
	}
	if result.ContradictingEvidence == nil {
		result.ContradictingEvidence = []hypothesisEvidenceItem{}
	}
	if result.NeutralEvidence == nil {
		result.NeutralEvidence = []hypothesisEvidenceItem{}
	}
	if result.SuggestedExperiments == nil {
		result.SuggestedExperiments = []string{}
	}
	if result.OverallAssessment.Caveats == nil {
		result.OverallAssessment.Caveats = []string{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"result": result})
}

func (h *ResearchHypothesisHandler) analyzeEvidenceBatches(ctx context.Context, hypothesis string, papers []hypothesisPaperLite) []hypothesisEvidenceItem {
	evidence := make([]hypothesisEvidenceItem, 0, len(papers))
	for i := 0; i < len(papers); i += 5 {
		if ctx.Err() != nil {
			break
		}
		end := i + 5
		if end > len(papers) {
			end = len(papers)
		}
		batch := papers[i:end]

		var paperBlock strings.Builder
		for j, p := range batch {
			abstract := strings.TrimSpace(p.Summary)
			if abstract == "" {
				abstract = strings.TrimSpace(p.Abstract)
			}
			if abstract == "" {
				abstract = "No abstract"
			}
			if len(abstract) > 400 {
				abstract = abstract[:400]
			}
			if j > 0 {
				paperBlock.WriteString("\n")
			}
			paperBlock.WriteString(fmt.Sprintf(`
[%d] "%s"
Abstract: %s
`, j+1, p.Title, abstract))
		}

		prompt := fmt.Sprintf(`Analyze how these papers relate to the hypothesis: "%s"

Papers:
%s
For each paper, determine:
1. Stance: "supporting" (provides evidence for), "contradicting" (provides evidence against), or "neutral" (not directly relevant)
2. Relevance: 0-1 score (how relevant to the hypothesis)
3. Quote: A key finding or claim from the paper (if stance is not neutral)
4. Reasoning: Brief explanation of why this supports/contradicts

Return the paper analyses using the supplied structured output schema.`,
			hypothesis, paperBlock.String())

		raw, err := h.structuredJSON(ctx, prompt, researchHypothesisEvidenceAnalysisSchema, "heavy")
		if err != nil {
			continue
		}
		for _, analysis := range decodeHypothesisEvidenceAnalyses(raw, len(batch)) {
			paper := batch[analysis.index-1]
			if analysis.relevance <= 0.3 {
				continue
			}
			evidence = append(evidence, hypothesisEvidenceItem{
				Paper:     paper,
				Stance:    analysis.stance,
				Relevance: analysis.relevance,
				Quote:     analysis.quote,
				Reasoning: analysis.reasoning,
			})
		}
	}

	sort.SliceStable(evidence, func(i, j int) bool {
		return evidence[i].Relevance > evidence[j].Relevance
	})
	return evidence
}

func (h *ResearchHypothesisHandler) runHypothesisAssessment(
	ctx context.Context,
	hypothesis string,
	supporting, contradicting []hypothesisEvidenceItem,
) (hypothesisOverallAssessment, error) {
	defaultAssessment := hypothesisOverallAssessment{
		Verdict:    "inconclusive",
		Confidence: 0.5,
		Summary:    "Insufficient data for assessment.",
		Caveats:    []string{},
	}

	var supportingBlock strings.Builder
	limit := len(supporting)
	if limit > 5 {
		limit = 5
	}
	for i := 0; i < limit; i++ {
		supportingBlock.WriteString(fmt.Sprintf("- %s: %s\n", supporting[i].Paper.Title, supporting[i].Reasoning))
	}

	var contradictingBlock strings.Builder
	limit = len(contradicting)
	if limit > 5 {
		limit = 5
	}
	for i := 0; i < limit; i++ {
		contradictingBlock.WriteString(fmt.Sprintf("- %s: %s\n", contradicting[i].Paper.Title, contradicting[i].Reasoning))
	}

	prompt := fmt.Sprintf(`%s

---

Assess the overall evidence for this hypothesis:

Hypothesis: "%s"

Supporting evidence (%d papers):
%s
Contradicting evidence (%d papers):
%s
Provide a rigorous academic assessment:
1. Verdict: strongly_supported, partially_supported, inconclusive, partially_contradicted, or strongly_contradicted
2. Confidence: 0-1 (based on evidence quality, not just quantity)
3. Summary: 2-3 sentences using hedging language ("The evidence suggests...", "Findings indicate...")
4. Caveats: Key methodological limitations or considerations

CRITICAL: Use academic language. Avoid "proves" or absolute statements. Acknowledge uncertainty.
Return the assessment using the supplied structured output schema.`,
		researchHypothesisStatisticsPrompt,
		hypothesis,
		len(supporting), supportingBlock.String(),
		len(contradicting), contradictingBlock.String())

	raw, err := h.structuredJSON(ctx, prompt, researchHypothesisAssessmentSchema, "heavy")
	if err != nil {
		return defaultAssessment, err
	}
	parsed := decodeHypothesisAssessment(raw)
	if parsed.Verdict == "" {
		return defaultAssessment, nil
	}
	return parsed, nil
}

func (h *ResearchHypothesisHandler) runHypothesisExperiments(ctx context.Context, hypothesis string, hasGaps bool) ([]string, error) {
	gapsNote := ""
	if hasGaps {
		gapsNote = "Note: There are significant gaps in the literature on this topic."
	}
	prompt := fmt.Sprintf(`Based on the hypothesis and existing evidence, suggest experiments to further test it:

Hypothesis: "%s"

%s

Suggest 3-4 specific experiments or studies that could:
1. Provide stronger evidence for or against the hypothesis
2. Address limitations in existing research
3. Explore unexplored aspects

Return the experiments using the supplied structured output schema.`,
		hypothesis, gapsNote)

	raw, err := h.structuredJSON(ctx, prompt, researchHypothesisExperimentsSchema, "heavy")
	if err != nil {
		return nil, err
	}
	return normalizeHypothesisStringList(decodeHypothesisExperiments(raw), nil), nil
}

func hypothesisCheckCancelled(w http.ResponseWriter, ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		WriteError(w, http.StatusRequestTimeout, ErrBadRequest, "request cancelled", nil)
		return err
	}
	return nil
}

func dedupeHypothesisPapers(papers []hypothesisPaperLite, capLimit int) []hypothesisPaperLite {
	seen := make(map[string]struct{}, len(papers))
	out := make([]hypothesisPaperLite, 0, len(papers))
	for _, p := range papers {
		key := strings.TrimSpace(p.DOI)
		if key == "" {
			key = strings.TrimSpace(p.Link)
		}
		if key == "" {
			key = strings.TrimSpace(p.PaperID)
		}
		if key == "" {
			key = strings.TrimSpace(p.Title)
		}
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, p)
		if capLimit > 0 && len(out) >= capLimit {
			break
		}
	}
	return out
}

func filterHypothesisEvidence(items []hypothesisEvidenceItem, stance string) []hypothesisEvidenceItem {
	out := make([]hypothesisEvidenceItem, 0)
	for _, item := range items {
		if item.Stance == stance {
			out = append(out, item)
		}
	}
	return out
}

func normalizeHypothesisStringList(values []string, fallback []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return append([]string(nil), fallback...)
	}
	return out
}

func decodeHypothesisClaims(raw json.RawMessage) []string {
	var parsed struct {
		Claims []string `json:"claims"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil
	}
	return parsed.Claims
}

func decodeHypothesisQueries(raw json.RawMessage) []string {
	var parsed struct {
		Queries []string `json:"queries"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil
	}
	return parsed.Queries
}

func decodeHypothesisExperiments(raw json.RawMessage) []string {
	var parsed struct {
		Experiments []string `json:"experiments"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil
	}
	return parsed.Experiments
}

type hypothesisEvidenceAnalysis struct {
	index     int
	stance    string
	relevance float64
	quote     string
	reasoning string
}

func decodeHypothesisEvidenceAnalyses(raw json.RawMessage, batchLength int) []hypothesisEvidenceAnalysis {
	var parsed struct {
		Papers []struct {
			Index     int     `json:"index"`
			Stance    string  `json:"stance"`
			Relevance float64 `json:"relevance"`
			Quote     string  `json:"quote"`
			Reasoning string  `json:"reasoning"`
		} `json:"papers"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil
	}
	out := make([]hypothesisEvidenceAnalysis, 0, len(parsed.Papers))
	for _, p := range parsed.Papers {
		if p.Index < 1 || p.Index > batchLength {
			continue
		}
		if p.Stance != "supporting" && p.Stance != "contradicting" && p.Stance != "neutral" {
			continue
		}
		rel := p.Relevance
		if rel < 0 {
			rel = 0
		}
		if rel > 1 {
			rel = 1
		}
		out = append(out, hypothesisEvidenceAnalysis{
			index:     p.Index,
			stance:    p.Stance,
			relevance: rel,
			quote:     strings.TrimSpace(p.Quote),
			reasoning: strings.TrimSpace(p.Reasoning),
		})
	}
	return out
}

func decodeHypothesisAssessment(raw json.RawMessage) hypothesisOverallAssessment {
	var parsed hypothesisOverallAssessment
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return hypothesisOverallAssessment{}
	}
	switch parsed.Verdict {
	case "strongly_supported", "partially_supported", "inconclusive", "partially_contradicted", "strongly_contradicted":
	default:
		parsed.Verdict = ""
	}
	if parsed.Confidence < 0 {
		parsed.Confidence = 0
	}
	if parsed.Confidence > 1 {
		parsed.Confidence = 1
	}
	if parsed.Caveats == nil {
		parsed.Caveats = []string{}
	}
	return parsed
}

// ---------------------------------------------------------------------------
// Shared structured-compute execution
// ---------------------------------------------------------------------------

// structuredJSON authors the LLM structured request with the canonical policy
// and returns the parsed JSON payload (without writing an HTTP response).
func (h *ResearchHypothesisHandler) structuredJSON(ctx context.Context, prompt, schema, tier string) (json.RawMessage, error) {
	if h.llmClient == nil {
		return nil, errHypothesisLLMUnavailable
	}

	llmCtx, cancel := analysisLLMContext(ctx)
	defer cancel()

	structuredClient := analysisStructuredClient(llmCtx, h.llmClient)
	resp, err := structuredClient.StructuredOutput(llmCtx, llm.ApplyStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     prompt,
		JsonSchema: schema,
	}, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
		RequestedTier: tier,
		Structured:    true,
		HighValue:     true,
	})))
	if err != nil {
		return nil, err
	}

	var result json.RawMessage
	if err := json.Unmarshal([]byte(resp.JsonResult), &result); err != nil {
		return nil, fmt.Errorf("llm returned invalid structured output: %w", err)
	}
	return result, nil
}

func (h *ResearchHypothesisHandler) writeStructuredError(w http.ResponseWriter, err error) {
	if errors.Is(err, errHypothesisLLMUnavailable) {
		WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "llm client unavailable", nil)
		return
	}
	msg := "llm structured output failed"
	if strings.Contains(err.Error(), "invalid structured") {
		msg = "llm returned invalid structured output"
	}
	WriteError(w, http.StatusBadGateway, ErrDependencyFailed, msg, map[string]any{
		"error": err.Error(),
	})
}

// runStructured authors the LLM structured request with the canonical policy
// (RequestedTier tier + Structured/HighValue → StructuredHighValue class, model
// resolved from tier), matching what fetchStructuredOutput previously sent to
// /generate. The parsed structured JSON is returned under "result" so the thin
// client can apply its per-stage default when absent.
func (h *ResearchHypothesisHandler) runStructured(w http.ResponseWriter, r *http.Request, prompt, schema, tier string) {
	result, err := h.structuredJSON(r.Context(), prompt, schema, tier)
	if err != nil {
		h.writeStructuredError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"result": result})
}
