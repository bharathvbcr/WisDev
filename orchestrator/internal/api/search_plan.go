package api

// Search-plan route: Go-owned port of the domain-aware research-architect
// prompts that previously lived in frontend/services/gemini/queryInsights.ts
// (generateSearchPlan / buildDomainAwareSearchPlanPrompt).
//
// CANONICAL OWNER: Go. The browser already POSTs to POST /search/plan via
// wisdevStructuredComputeClient.fetchSearchPlan; this handler fills that
// contract so clients no longer fall through to the default single-bucket plan.
//
// Response shape matches the FE client: { "buckets": [{ "name", "queries" }] }
// (not an envelope), so existing thin adapters keep working unchanged.

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/llm"
	llmv1 "github.com/bharathvbcr/wisdev-arc/orchestrator/proto/llm"
)

const searchPlanSchema = `{
	"type":"object",
	"additionalProperties":false,
	"required":["buckets"],
	"properties":{
		"buckets":{
			"type":"array",
			"minItems":1,
			"maxItems":6,
			"items":{
				"type":"object",
				"additionalProperties":false,
				"required":["name","queries"],
				"properties":{
					"name":{"type":"string"},
					"queries":{
						"type":"array",
						"minItems":1,
						"maxItems":3,
						"items":{"type":"string"}
					}
				}
			}
		}
	}
}`

// SearchPlanHandler serves POST /search/plan.
type SearchPlanHandler struct {
	llmClient analysisClient
}

// NewSearchPlanHandler constructs the handler.
func NewSearchPlanHandler(client analysisClient) *SearchPlanHandler {
	return &SearchPlanHandler{llmClient: client}
}

// Handle serves POST /search/plan with domain/intent-aware bucket planning.
func (h *SearchPlanHandler) Handle(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	traceID := resolveRequestTraceID(r)
	logSearchRouteLifecycle(r, "search_plan", "request_received", "", traceID, "result", "accepted")
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}
	if h == nil || h.llmClient == nil {
		WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "search plan backend unavailable", nil)
		return
	}

	var req struct {
		Query              string   `json:"query"`
		ExistingCategories []string `json:"existing_categories"`
		Domain             string   `json:"domain"`
		Intent             string   `json:"intent"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{"error": err.Error()})
		return
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", map[string]any{"field": "query"})
		return
	}

	prompt := buildDomainAwareSearchPlanPrompt(query, strings.TrimSpace(req.Domain), strings.TrimSpace(req.Intent), req.ExistingCategories)

	llmCtx, cancel := analysisLLMContext(r.Context())
	defer cancel()

	structuredClient := analysisStructuredClient(llmCtx, h.llmClient)
	resp, err := structuredClient.StructuredOutput(llmCtx, llm.ApplyStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:      prompt,
		Model:       llm.ResolveStandardModel(),
		JsonSchema:  searchPlanSchema,
		MaxTokens:   1536,
		Temperature: 0.3,
	}, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
		RequestedTier: "standard",
		Structured:    true,
		TaskType:      "orchestration",
	})))
	if err != nil {
		slog.Warn("search plan generation failed",
			"service", "go_orchestrator", "runtime", "go", "component", "api",
			"operation", "search.plan", "stage", "llm_call",
			"provider", "llm", "result", "error", "attempt", 1,
			"error_code", "search_plan_failed", "error", err.Error(),
		)
		WriteError(w, http.StatusBadGateway, ErrServiceUnavailable, "search plan generation unavailable", nil)
		return
	}

	payload := strings.TrimSpace(resp.GetJsonResult())
	if payload == "" {
		WriteError(w, http.StatusBadGateway, ErrServiceUnavailable, "search plan returned empty payload", nil)
		return
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		WriteError(w, http.StatusBadGateway, ErrServiceUnavailable, "search plan returned invalid payload", nil)
		return
	}
	if _, ok := parsed["buckets"]; !ok {
		WriteError(w, http.StatusBadGateway, ErrServiceUnavailable, "search plan missing buckets", nil)
		return
	}

	slog.Info("search plan generated",
		"service", "go_orchestrator", "runtime", "go", "component", "api",
		"operation", "search.plan", "stage", "response",
		"provider", "llm", "result", "ok",
		"domain", req.Domain,
		"intent", req.Intent,
		"latency_ms", time.Since(start).Milliseconds(),
	)
	logSearchRouteLifecycle(r, "search_plan", "response", query, traceID,
		"result", "ok", "latency_ms", time.Since(start).Milliseconds())

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(parsed)
}

func buildDomainAwareSearchPlanPrompt(query, domain, intent string, existingCategories []string) string {
	domainInstructions := map[string]string{
		"medicine": `DOMAIN: Medicine/Healthcare
- Use MeSH terms and clinical vocabulary
- Consider PICO framework (Population, Intervention, Comparison, Outcome)
- Include evidence hierarchy (RCTs, meta-analyses, systematic reviews)
- Search for clinical guidelines and treatment protocols`,
		"cs": `DOMAIN: Computer Science/AI
- Include major conference names (NeurIPS, ICML, ACL, CVPR, ICLR)
- Use arXiv categories (cs.CL, cs.CV, cs.LG, cs.AI)
- Reference benchmark names and dataset identifiers
- Include both theoretical and applied search angles`,
		"biology": `DOMAIN: Biology/Life Sciences
- Include gene/protein names and pathway identifiers
- Use organism-specific terminology (model organisms)
- Reference key databases (UniProt, GenBank, PDB)
- Consider molecular vs systems-level perspectives`,
		"social": `DOMAIN: Social Sciences
- Specify geographic and demographic scope
- Distinguish qualitative vs quantitative approaches
- Include policy implications and intervention studies
- Consider longitudinal and cross-sectional designs`,
		"physics": `DOMAIN: Physics
- Use precise physical quantities and standard notation
- Distinguish experimental vs theoretical vs computational
- Include arXiv categories (hep-th, cond-mat, quant-ph)
- Reference specific experiments, detectors, or facilities`,
		"neuro": `DOMAIN: Neuroscience
- Include neuroimaging modalities (fMRI, EEG, MEG)
- Reference brain regions and neural circuits
- Consider translational aspects (basic to clinical)
- Include cognitive and behavioral paradigms`,
		"mathematics": `DOMAIN: Mathematics
- Use standard mathematical notation and terminology
- Reference specific conjectures, theorems, or open problems
- Include proof technique keywords
- Consider pure vs applied mathematics angles`,
		"economics": `DOMAIN: Economics
- Include econometric methods and models
- Specify micro vs macro scope
- Reference key economic indicators and datasets
- Consider policy evaluation and causal inference methods`,
		"engineering": `DOMAIN: Engineering
- Include standards and specifications (ISO, ASTM, IEEE)
- Reference simulation methods (FEM, CFD)
- Consider design, manufacturing, and testing phases
- Include patent literature search angles`,
		"chemistry": `DOMAIN: Chemistry
- Include IUPAC nomenclature and CAS numbers
- Reference reaction types and catalytic systems
- Include spectroscopic characterization methods
- Consider computational chemistry approaches (DFT, MD)`,
		"law": `DOMAIN: Law
- Specify jurisdiction and legal system
- Include case law references and legal doctrines
- Reference statutes, regulations, and treaties
- Consider comparative law perspectives`,
		"climate": `DOMAIN: Climate/Environmental Science
- Include climate model references (CMIP, GCM)
- Reference emissions scenarios (RCP, SSP)
- Consider mitigation vs adaptation perspectives
- Include geographic and temporal scope`,
	}

	intentInstructions := map[string]string{
		"comparison":  `Create buckets that compare different approaches, methods, or viewpoints. Include "pros/cons" and "versus" style queries.`,
		"review":      "Create broader, more comprehensive buckets. Include historical context and evolution of the field.",
		"methodology": "Focus buckets on methods, techniques, protocols, and experimental designs.",
		"trends":      `Weight queries toward recent developments (2022-2026). Include "recent advances" and "emerging" keywords.`,
		"definition":  "Include foundational and introductory material. Create buckets for core concepts and definitions.",
		"papers":      "Focus on finding primary research papers. Include specific, targeted queries.",
	}

	domainGuide := ""
	if guide, ok := domainInstructions[domain]; ok {
		domainGuide = "\n" + guide
	}
	intentGuide := ""
	if guide, ok := intentInstructions[intent]; ok {
		intentGuide = "\nINTENT: " + guide
	}

	bucketRange := "3-4"
	switch intent {
	case "comparison", "review":
		bucketRange = "4-6"
	case "definition":
		bucketRange = "2-3"
	}

	categories := make([]string, 0, len(existingCategories))
	for _, c := range existingCategories {
		if name := strings.TrimSpace(c); name != "" {
			categories = append(categories, name)
		}
	}

	if len(categories) > 0 {
		encoded, _ := json.Marshal(categories)
		return fmt.Sprintf(`You are a research architect planning an academic literature search.%s%s

The user has selected these research buckets for %q: %s.
For *each* of these buckets, generate 3 distinct boolean search strings optimized for academic databases (OpenAlex/Semantic Scholar).

Return only an object with this shape:
{
  "buckets": [
    {
      "name": "Bucket Name",
      "queries": ["boolean query 1", "boolean query 2", "boolean query 3"]
    }
  ]
}`, domainGuide, intentGuide, query, string(encoded))
	}

	return fmt.Sprintf(`You are a research architect planning an academic literature search.%s%s

Split the topic %q into %s distinct research buckets.
For *each* bucket, generate 3 distinct boolean search strings optimized for academic databases (OpenAlex/Semantic Scholar).

Return only an object with this shape:
{
  "buckets": [
    {
      "name": "Bucket Name",
      "queries": ["boolean query 1", "boolean query 2", "boolean query 3"]
    }
  ]
}`, domainGuide, intentGuide, query, bucketRange)
}
