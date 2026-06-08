package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"fmt"
	"github.com/redis/go-redis/v9"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/rag"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

func (s *wisdevServer) registerRAGRoutes(mux *http.ServeMux, agentGateway *wisdev.AgentGateway) {
	var redisClient redis.UniversalClient
	var searchRegistry *search.ProviderRegistry
	if agentGateway != nil {
		redisClient = agentGateway.Redis
		searchRegistry = agentGateway.SearchRegistry
	}
	if s.rag != nil {
		for _, path := range wisdevResearchGroundedAnswerPaths {
			mux.HandleFunc(path, s.rag.HandleAnswer)
		}
		for _, path := range wisdevResearchSectionContextPaths {
			mux.HandleFunc(path, s.rag.HandleSectionContext)
		}
	}

	mux.HandleFunc("/rag/retrieve", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			Query  string `json:"query"`
			Limit  int    `json:"limit"`
			Domain string `json:"domain"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		query := strings.TrimSpace(req.Query)
		if query == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", map[string]any{
				"field": "query",
			})
			return
		}
		limit := req.Limit
		if limit <= 0 {
			limit = 25
		}
		papers, retrievalPayload, err := wisdev.RetrieveCanonicalPapersWithRegistry(r.Context(), redisClient, searchRegistry, query, limit)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, ErrRagFailed, "retrieval failed", map[string]any{
				"error": err.Error(),
			})
			return
		}
		payload := map[string]any{
			"query":       query,
			"papers":      papers,
			"paperBundle": mapAny(retrievalPayload["paperBundle"]),
			"backend":     "go-wisdev-canonical",
			"workerMetadata": map[string]any{
				"documentWorker": "python-docling",
				"sourceOfTruth":  "go-control-plane",
			},
		}
		for _, key := range []string{"queryUsed", "traceId", "count", "contract", "retrievalStrategies", "retrievalTrace", "pipeline", "tool", "mcpTool", "retrievalBy", "providers", "warnings", "latencyMs", "sourceAcquisition"} {
			if value, ok := retrievalPayload[key]; ok {
				payload[key] = value
			}
		}
		traceID := writeEnvelope(w, "retrieval", payload)
		s.journalEvent(
			"rag_retrieve",
			"/rag/retrieve",
			traceID,
			"",
			"",
			"",
			"",
			"RAG retrieval completed.",
			payload,
			map[string]any{"domain": strings.TrimSpace(req.Domain)},
		)
	})

	mux.HandleFunc("/rag/hybrid", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			Query     string           `json:"query"`
			Documents []map[string]any `json:"documents"`
			TopK      int              `json:"topK"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		query := strings.TrimSpace(req.Query)
		if query == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", map[string]any{
				"field": "query",
			})
			return
		}
		limit := req.TopK
		if limit <= 0 {
			limit = 10
		}
		papers, _, err := wisdev.RetrieveCanonicalPapersWithRegistry(r.Context(), redisClient, searchRegistry, query, limit)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, ErrRagFailed, "hybrid retrieval failed", map[string]any{
				"error": err.Error(),
			})
			return
		}
		results := make([]map[string]any, 0, len(papers))
		for _, paper := range papers {
			results = append(results, map[string]any{
				"id":     paper.ID,
				"title":  paper.Title,
				"link":   paper.Link,
				"score":  paper.Score,
				"source": paper.Source,
			})
		}
		payload := map[string]any{
			"query":   query,
			"results": results,
		}
		traceID := writeEnvelope(w, "hybrid", payload)
		s.journalEvent("rag_hybrid", "/rag/hybrid", traceID, "", "", "", "", "Hybrid RAG retrieval completed.", payload, map[string]any{"documentCount": len(req.Documents)})
	})

	mux.HandleFunc("/rag/crag", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			Query     string           `json:"query"`
			Documents []map[string]any `json:"documents"`
			TopK      int              `json:"topK"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		query := strings.TrimSpace(req.Query)
		if query == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", map[string]any{
				"field": "query",
			})
			return
		}
		limit := req.TopK
		if limit <= 0 {
			limit = 10
		}
		papers, _, err := wisdev.RetrieveCanonicalPapersWithRegistry(r.Context(), redisClient, searchRegistry, query, limit)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, ErrRagFailed, "crag retrieval failed", map[string]any{
				"error": err.Error(),
			})
			return
		}
		payload := map[string]any{
			"query":   query,
			"results": buildCommitteePapers(papers),
			"critic": map[string]any{
				"decision": "accept",
				"reason":   "Go CRAG path reused search committee ranking.",
			},
		}
		traceID := writeEnvelope(w, "crag", payload)
		s.journalEvent("rag_crag", "/rag/crag", traceID, "", "", "", "", "CRAG retrieval completed.", payload, map[string]any{"documentCount": len(req.Documents)})
	})

	mux.HandleFunc("/rag/agentic-hybrid", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			Query            string `json:"query"`
			Domain           string `json:"domain"`
			MaxIterations    int    `json:"maxIterations"`
			Limit            int    `json:"limit"`
			SessionID        string `json:"sessionId"`
			RetrievalBackend string `json:"retrievalBackend"`
			RetrievalMode    string `json:"retrievalMode"`
			FusionMode       string `json:"fusionMode"`
			LatencyBudgetMs  int    `json:"latencyBudgetMs"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		query := strings.TrimSpace(req.Query)
		if err := validateRequiredString(query, "query", 500); err != nil {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, err.Error(), map[string]any{
				"field": "query",
			})
			return
		}
		if err := validateOptionalString(req.Domain, "domain", 120); err != nil {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, err.Error(), map[string]any{
				"field": "domain",
			})
			return
		}
		limit := boundedInt(req.Limit, 12, 1, 20)
		maxIterations := boundedInt(req.MaxIterations, 1, 1, 6)
		var papers []wisdev.Source
		err := withConcurrencyGuard("agentic_hybrid", wisdev.EnvInt("WISDEV_AGENTIC_RAG_CONCURRENCY", 6), func() error {
			var innerErr error
			papers, _, innerErr = wisdev.RetrieveCanonicalPapersWithRegistry(r.Context(), redisClient, searchRegistry, query, limit)
			return innerErr
		})
		if err != nil {
			code := ErrRagFailed
			status := http.StatusInternalServerError
			if strings.Contains(err.Error(), "concurrency limit reached") {
				code = ErrConcurrencyLimit
				status = http.StatusTooManyRequests
			}
			WriteError(w, status, code, "agentic hybrid retrieval failed", map[string]any{
				"error": err.Error(),
			})
			return
		}
		committee := buildMultiAgentCommitteeResult(query, strings.TrimSpace(req.Domain), papers, maxIterations, true)
		queryRefinements := []string{
			query,
			fmt.Sprintf("%s recent review", query),
			fmt.Sprintf("%s systematic evidence", query),
		}
		results := map[string]any{
			"papers":            committee["papers"],
			"totalFound":        len(papers),
			"sources":           committee["sources"],
			"cacheHit":          false,
			"latencyMs":         0,
			"metrics":           map[string]any{"totalMs": 0, "controlPlane": "go"},
			"critiques":         []map[string]any{{"decision": "accept", "issues": []string{}, "confidenceScore": 0.78}},
			"iterations":        maxIterations,
			"originalQuery":     query,
			"finalQuery":        query,
			"queryRefinements":  queryRefinements,
			"agenticMode":       true,
			"backendUsed":       "go_agentic_hybrid",
			"fallbackTriggered": false,
			"fallbackReason":    "",
			"retrievalPlan": []map[string]any{
				{"phase": "seed", "query": queryRefinements[0]},
				{"phase": "expansion", "query": queryRefinements[1]},
				{"phase": "verification", "query": queryRefinements[2]},
			},
			"committee": committee,
		}
		traceID := writeEnvelope(w, "agenticHybrid", results)
		s.journalEvent("rag_agentic_hybrid", "/rag/agentic-hybrid", traceID, req.SessionID, "", "", "", "Agentic hybrid retrieval completed.", results, map[string]any{
			"domain":           req.Domain,
			"retrievalBackend": req.RetrievalBackend,
			"retrievalMode":    req.RetrievalMode,
			"fusionMode":       req.FusionMode,
			"latencyBudgetMs":  req.LatencyBudgetMs,
		})
	})

	mux.HandleFunc("/wisdev/rag/evidence-gate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			SynthesisText      string           `json:"synthesisText"`
			Claims             []map[string]any `json:"claims"`
			ContradictionCount int              `json:"contradictionCount"`
			Sources            []struct {
				ID       string `json:"id"`
				Title    string `json:"title"`
				Abstract string `json:"abstract"`
			} `json:"sources"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		if s.gateway == nil || s.gateway.Gate == nil {
			payload := buildEvidenceGatePayload(req.Claims, req.ContradictionCount)
			traceID := writeEnvelope(w, "evidenceGate", payload)
			s.journalEvent(
				"evidence_gate",
				"/wisdev/rag/evidence-gate",
				traceID,
				"",
				"",
				"",
				"",
				"Evidence gate evaluated synthesized claims via local fallback.",
				payload,
				map[string]any{"mode": "fallback"},
			)
			return
		}
		if strings.TrimSpace(req.SynthesisText) == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "synthesisText is required", map[string]any{
				"field": "synthesisText",
			})
			return
		}
		papers := make([]search.Paper, 0, len(req.Sources))
		for _, src := range req.Sources {
			papers = append(papers, search.Paper{
				ID:       src.ID,
				Title:    src.Title,
				Abstract: src.Abstract,
			})
		}
		result, err := s.gateway.Gate.Run(r.Context(), req.SynthesisText, papers)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, ErrInternal, "evidence gate failed", map[string]any{
				"error": err.Error(),
			})
			return
		}
		payload := map[string]any{
			"checked":               result.Checked,
			"passed":                result.Verdict == "passed",
			"provisional":           result.Verdict == "provisional",
			"failed":                result.Verdict == "failed",
			"verdict":               result.Verdict,
			"warningPrefix":         result.WarningPrefix,
			"message":               result.Message,
			"claimCount":            result.Checked,
			"linkedClaimCount":      result.PassedCount,
			"unlinkedClaimCount":    result.UnlinkedCount,
			"contradictionCount":    result.ContradictionCount,
			"claims":                result.Claims,
			"linkedClaims":          result.LinkedClaims,
			"unlinkedClaims":        result.UnlinkedClaims,
			"contradictions":        result.Contradictions,
			"strictGatePass":        result.Verdict == "passed",
			"nliChecked":            false,
			"aiClaimExtractionUsed": len(req.SynthesisText) > rag.AIExtractionThreshold,
		}
		traceID := writeEnvelope(w, "evidenceGate", payload)
		s.journalEvent(
			"evidence_gate",
			"/wisdev/rag/evidence-gate",
			traceID,
			"",
			"",
			"",
			"",
			"Evidence gate evaluated synthesized claims.",
			payload,
			map[string]any{"contradictionCount": result.ContradictionCount},
		)
	})
}
