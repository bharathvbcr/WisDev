package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/paper"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/rag"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/telemetry"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
)

func registerPathAlias(mux *http.ServeMux, aliasPath string, targetPath string) {
	mux.HandleFunc(aliasPath, func(w http.ResponseWriter, r *http.Request) {
		cloned := r.Clone(r.Context())
		urlCopy := *r.URL
		urlCopy.Path = targetPath
		urlCopy.RawPath = targetPath
		cloned.URL = &urlCopy
		mux.ServeHTTP(w, cloned)
	})
}

func registerJSONPostAlias(
	mux *http.ServeMux,
	aliasPath string,
	targetPath string,
	buildPayload func(*http.Request) map[string]any,
) {
	mux.HandleFunc(aliasPath, func(w http.ResponseWriter, r *http.Request) {
		cloned := r.Clone(r.Context())
		urlCopy := *r.URL
		urlCopy.Path = targetPath
		urlCopy.RawPath = targetPath
		cloned.URL = &urlCopy
		cloned.Method = http.MethodPost

		payloadBytes, err := json.Marshal(buildPayload(r))
		if err != nil {
			WriteError(w, http.StatusInternalServerError, ErrInternal, "failed to marshal alias payload", map[string]any{
				"aliasPath":  aliasPath,
				"targetPath": targetPath,
				"error":      err.Error(),
			})
			return
		}

		cloned.Body = io.NopCloser(bytes.NewReader(payloadBytes))
		cloned.ContentLength = int64(len(payloadBytes))
		cloned.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(payloadBytes)), nil
		}
		cloned.Header = r.Header.Clone()
		cloned.Header.Set("Content-Type", "application/json")

		mux.ServeHTTP(w, cloned)
	})
}

type ServerConfig struct {
	Version        string
	SearchRegistry *search.ProviderRegistry
	FastRegistry   *search.ProviderRegistry
	LLMClient      *llm.Client
	VertexClient   *llm.VertexClient
	AgentGateway   *wisdev.AgentGateway
	DB             wisdev.DBProvider
	Redis          redis.UniversalClient
	Journal        *wisdev.RuntimeJournal
}

func ensureAgentGateway(cfg ServerConfig) *wisdev.AgentGateway {
	if cfg.AgentGateway != nil {
		return cfg.AgentGateway
	}

	journal := cfg.Journal
	if journal == nil {
		journal = wisdev.NewRuntimeJournal(cfg.DB)
	}

	gateway := wisdev.NewAgentGateway(cfg.DB, cfg.Redis, journal)
	if cfg.LLMClient != nil {
		gateway.LLMClient = cfg.LLMClient
		gateway.Brain = wisdev.NewBrainCapabilities(cfg.LLMClient)
		gateway.Gate = rag.NewEvidenceGate(cfg.LLMClient)
		gateway.Executor = wisdev.NewPlanExecutor(gateway.Registry, gateway.PolicyConfig, cfg.LLMClient, gateway.Brain, cfg.Redis, gateway.PythonExecute, gateway.ADKRuntime)

	}
	if cfg.SearchRegistry != nil {
		gateway.Loop = wisdev.NewAutonomousLoop(cfg.SearchRegistry, gateway.LLMClient)
	}
	return gateway
}

func NewRouter(cfg ServerConfig) http.Handler {
	mux := http.NewServeMux()
	agentGateway := ensureAgentGateway(cfg)

	// Initialize engines
	ragEngine := rag.NewEngine(cfg.SearchRegistry, cfg.LLMClient)
	ragHandler := NewRAGHandler(ragEngine)

	wisdevSessions := wisdev.NewSessionManager("")
	wisdevGuided := wisdev.NewGuidedFlow()

	// Use AgentGateway's Loop if available, otherwise fallback to local init.
	var wisdevAutonomous *wisdev.AutonomousLoop
	if agentGateway != nil {
		wisdevAutonomous = agentGateway.Loop
	} else {
		wisdevAutonomous = wisdev.NewAutonomousLoop(cfg.SearchRegistry, cfg.LLMClient)
	}
	GlobalYoloLoop = wisdevAutonomous

	wisdevWorker := wisdev.NewAutonomousWorker(wisdevAutonomous)
	// BrainCapabilities requires an LLM client; it is nil-safe in handlers when
	// LLMClient is not provided (e.g. integration-test environments).
	var brainCaps *wisdev.BrainCapabilities
	if cfg.LLMClient != nil {
		brainCaps = wisdev.NewBrainCapabilities(cfg.LLMClient)
	}
	compiler := wisdev.NewPaper2SkillCompiler(cfg.LLMClient)
	wisdevHandler := NewWisDevHandler(wisdevSessions, wisdevGuided, wisdevWorker, agentGateway, brainCaps, compiler)

	paperProfiler := paper.NewProfiler(cfg.LLMClient)
	pythonBaseURL := wisdev.ResolvePythonBase()
	paperHandler := NewPaperHandler(paperProfiler, pythonBaseURL)

	analysisHandler := NewAnalysisHandler(cfg.LLMClient)
	synthesisHandler := NewSynthesisHandler(cfg.LLMClient)

	healthHandler := NewHealthHandler(cfg.LLMClient)

	searchHandler := NewSearchHandler(cfg.SearchRegistry, cfg.FastRegistry, cfg.Redis)
	topicTreeHandler := NewTopicTreeHandler(cfg.Redis)
	gatewayHandler := NewGatewayHandler(agentGateway)
	fullPaperHandler := NewFullPaperHandler(cfg.SearchRegistry)
	imageHandler := NewImageHandler(cfg.VertexClient)
	internalOpsHandler := NewInternalOpsHandler(cfg.DB, cfg.Journal)

	// 0. Operational Endpoints
	mux.HandleFunc("/healthz", healthHandler.Liveness)
	mux.HandleFunc("/readiness", healthHandler.Readiness)
	mux.Handle("/metrics", telemetry.MetricsHandler())
	mux.HandleFunc("/internal/account/delete", internalOpsHandler.HandleAccountDelete)
	mux.HandleFunc("/internal/billing/stripe/webhook", internalOpsHandler.HandleStripeBillingSync)
	mux.HandleFunc("/internal/billing/subscription", internalOpsHandler.HandleStripeBillingSync)

	// legacy health for compatibility
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		status := "healthy"
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"status": status, "version": cfg.Version})
	})

	// 1. WisDev Modernized V2 Routes (Primary truth)
	RegisterV2WisDevRoutes(mux, agentGateway, ragHandler)

	// 2. Paper Insight Routes
	mux.HandleFunc("/v2/paper/profile", paperHandler.HandleProfile)
	mux.HandleFunc("/v2/paper/extract-pdf", paperHandler.HandleExtractPDF)
	mux.HandleFunc("/v2/export/markdown", paperHandler.HandleExportMarkdown)
	mux.HandleFunc("/v2/export/html", paperHandler.HandleExportHTML)
	mux.HandleFunc("/v2/export/latex", paperHandler.HandleExportLaTeX)
	mux.HandleFunc("/v2/papers/related", searchHandler.HandleRelatedArticles)
	mux.HandleFunc("/v2/papers/network", paperHandler.HandleGetNetwork)

	// 3. Search Endpoints
	mux.HandleFunc("/search", searchHandler.HandleLegacySearch)
	mux.HandleFunc("/v2/search/parallel", searchHandler.HandleParallelSearch)
	mux.HandleFunc("/v2/search/hybrid", searchHandler.HandleHybridSearch)
	mux.HandleFunc("/v2/search/batch", searchHandler.HandleBatchSearch)
	mux.HandleFunc("/v2/search/tool", searchHandler.HandleToolSearch)
	mux.HandleFunc("/v2/expand/aggressive", searchHandler.HandleAggressiveExpansion)
	mux.HandleFunc("/v2/expand/splade", searchHandler.HandleSPLADEExpansion)

	// 4. Full Paper Endpoints
	mux.HandleFunc("/v2/full-paper/retrieval", fullPaperHandler.HandleFullPaperRetrieval)

	// 4a. Image Endpoints
	mux.HandleFunc("/v2/images/generate", imageHandler.HandleGenerate)

	// 4b. Analysis & Synthesis Endpoints
	mux.HandleFunc("/v2/analysis", analysisHandler.HandleAnalysis)
	mux.HandleFunc("/v2/synthesis", synthesisHandler.HandleSynthesis)

	// 5. RAG Endpoints
	mux.HandleFunc("/v2/rag/answer", ragHandler.HandleAnswer)
	mux.HandleFunc("/v2/rag/section-context", ragHandler.HandleSectionContext)
	mux.HandleFunc("/v2/rag/raptor/build", ragHandler.HandleRaptorBuild)
	mux.HandleFunc("/v2/rag/raptor/query", ragHandler.HandleRaptorQuery)
	mux.HandleFunc("/v2/rag/bm25/index", ragHandler.HandleBM25Index)
	mux.HandleFunc("/v2/rag/bm25/search", ragHandler.HandleBM25Search)
	mux.HandleFunc("/v2/rag/chunking/adaptive", ragHandler.HandleAdaptiveChunking)
	mux.HandleFunc("/v2/rag/evidence-gate", func(w http.ResponseWriter, r *http.Request) {
		// Proxy to the wisdev handler which already has the logic
		http.Redirect(w, r, "/v2/wisdev/rag/evidence-gate", http.StatusTemporaryRedirect)
	})

	// 6. Gateway 
	if agentGateway != nil {
		gatewayHandler.RegisterRoutes(mux)
	}

	// 7. Vector Operations
	mux.HandleFunc("/v2/vector/batch-similarity", HandleBatchSimilarity)
	mux.HandleFunc("/v2/vector/fuse", HandleFuseResults)
	mux.HandleFunc("/v2/query/field", searchHandler.HandleQueryField)
	mux.HandleFunc("/query/field", searchHandler.HandleQueryField)
	mux.HandleFunc("/v2/query/introduction", searchHandler.HandleQueryIntroduction)
	mux.HandleFunc("/query/introduction", searchHandler.HandleQueryIntroduction)
	mux.HandleFunc("/v2/summarization/batch", searchHandler.HandleBatchSummaries)
	mux.HandleFunc("/summarization/batch", searchHandler.HandleBatchSummaries)
	mux.HandleFunc("/v2/source/related", searchHandler.HandleRelatedArticles)
	mux.HandleFunc("/source/related", searchHandler.HandleRelatedArticles)

	// 8. Topic Tree
	mux.HandleFunc("/v2/topic-tree/generate", topicTreeHandler.HandleTopicTreeGenerate)
	mux.HandleFunc("/v2/topic-tree/children", topicTreeHandler.HandleTopicTreeChildren)
	mux.HandleFunc("/v2/topic-tree/refine-queries", topicTreeHandler.HandleTopicTreeRefineQueries)
	mux.HandleFunc("/topic-tree/generate", topicTreeHandler.HandleTopicTreeGenerate)
	mux.HandleFunc("/topic-tree/children", topicTreeHandler.HandleTopicTreeChildren)
	mux.HandleFunc("/topic-tree/refine-queries", topicTreeHandler.HandleTopicTreeRefineQueries)
	mux.HandleFunc("/v2/wisdev/paper2skill", wisdevHandler.HandlePaper2Skill)
	mux.HandleFunc("/wisdev/paper2skill", wisdevHandler.HandlePaper2Skill)

	// 9. YOLO Execution
	mux.HandleFunc("/agent/yolo/execute", YoloExecuteHandler)
	mux.HandleFunc("/agent/yolo/stream", YoloStreamHandler)
	mux.HandleFunc("/agent/yolo/cancel", yoloCancelHandler)
	
	// 9a. YOLO/WisDev Scheduling
	mux.HandleFunc("/wisdev/schedule", wisdevHandler.WisDevScheduleHandler)
	mux.HandleFunc("/wisdev/schedule/run/", wisdevHandler.WisDevScheduleRunHandler)

	// 10. Canonical browser aliases. Keep the browser contract stable and adapt
	// to internal V2 route names and request shapes here instead of in Vite.
	registerPathAlias(mux, "/api/v2/search/parallel", "/v2/search/parallel")
	registerPathAlias(mux, "/api/v2/search/batch", "/v2/search/batch")
	registerPathAlias(mux, "/api/v2/search/tool", "/v2/search/tool")
	registerPathAlias(mux, "/api/v2/search/hybrid", "/v2/search/hybrid")

	registerPathAlias(mux, "/api/v1/export/markdown", "/v2/export/markdown")
	registerPathAlias(mux, "/api/v1/export/html", "/v2/export/html")
	registerPathAlias(mux, "/api/v1/export/latex", "/v2/export/latex")
	registerPathAlias(mux, "/api/v1/images/generate", "/v2/images/generate")
	registerPathAlias(mux, "/api/v1/papers/network", "/v2/papers/network")

	registerPathAlias(mux, "/api/v1/wisdev-brain/plan", "/v2/wisdev/wisdev.Plan")
	registerPathAlias(mux, "/api/v1/wisdev-brain/decide", "/v2/wisdev/decide")
	registerPathAlias(mux, "/api/v1/wisdev-brain/observe", "/v2/wisdev/observe")
	registerPathAlias(mux, "/api/v1/wisdev-brain/execute", "/v2/wisdev/execute")
	registerPathAlias(mux, "/v2/wisdev/plan", "/v2/wisdev/wisdev.Plan")
	registerPathAlias(mux, "/api/v1/wisdev-brain/policy/get", "/v2/policy/get")
	registerPathAlias(mux, "/api/v1/wisdev-brain/multi-agent/execute", "/v2/wisdev/multi-agent")
	registerPathAlias(mux, "/api/v1/wisdev-brain/paper2skill", "/v2/wisdev/paper2skill")
	registerPathAlias(mux, "/api/v1/wisdev-brain/brain/decompose-task", "/v2/brain/decompose-task")
	registerPathAlias(mux, "/api/v1/wisdev-brain/brain/propose-hypotheses", "/v2/brain/propose-hypotheses")
	registerPathAlias(mux, "/api/v1/wisdev-brain/brain/coordinate-replan", "/v2/brain/coordinate-replan")
	registerPathAlias(mux, "/api/v1/wisdev-brain/rag/evidence-gate", "/v2/wisdev/rag/evidence-gate")
	registerPathAlias(mux, "/api/v1/wisdev-brain/tool-search", "/v2/wisdev/tool-search")
	registerPathAlias(mux, "/api/v1/wisdev-brain/structured-output", "/v2/wisdev/structured-output")

	registerJSONPostAlias(mux, "/api/v1/wisdev-brain/memory/profile/get", "/v2/memory/profile/get", func(r *http.Request) map[string]any {
		return map[string]any{
			"userId": r.URL.Query().Get("userId"),
		}
	})
	registerPathAlias(mux, "/api/v1/wisdev-brain/memory/profile/learn", "/v2/memory/profile/learn")

	registerJSONPostAlias(mux, "/api/v1/wisdev-brain/feedback/get", "/v2/feedback/get", func(r *http.Request) map[string]any {
		return map[string]any{
			"userId":    r.URL.Query().Get("userId"),
			"sessionId": r.URL.Query().Get("sessionId"),
		}
	})
	registerPathAlias(mux, "/api/v1/wisdev-brain/feedback/save", "/v2/feedback/save")
	registerJSONPostAlias(mux, "/api/v1/wisdev-brain/feedback/analytics", "/v2/feedback/analytics", func(r *http.Request) map[string]any {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		return map[string]any{
			"userId": r.URL.Query().Get("userId"),
			"limit":  limit,
		}
	})

	registerPathAlias(mux, "/api/v1/wisdev-brain/full-paper/start", "/v2/full-paper/start")
	registerJSONPostAlias(mux, "/api/v1/wisdev-brain/full-paper/status", "/v2/full-paper/status", func(r *http.Request) map[string]any {
		return map[string]any{
			"jobId":     r.URL.Query().Get("jobId"),
			"userId":    r.URL.Query().Get("userId"),
			"sessionId": r.URL.Query().Get("sessionId"),
		}
	})
	registerJSONPostAlias(mux, "/api/v1/wisdev-brain/full-paper/artifacts", "/v2/full-paper/artifacts", func(r *http.Request) map[string]any {
		return map[string]any{
			"jobId":     r.URL.Query().Get("jobId"),
			"userId":    r.URL.Query().Get("userId"),
			"sessionId": r.URL.Query().Get("sessionId"),
		}
	})
	registerJSONPostAlias(mux, "/api/v1/wisdev-brain/full-paper/workspace", "/v2/full-paper/workspace", func(r *http.Request) map[string]any {
		return map[string]any{
			"jobId":     r.URL.Query().Get("jobId"),
			"userId":    r.URL.Query().Get("userId"),
			"sessionId": r.URL.Query().Get("sessionId"),
		}
	})
	registerPathAlias(mux, "/api/v1/wisdev-brain/full-paper/review", "/v2/full-paper/checkpoint")
	registerPathAlias(mux, "/api/v1/wisdev-brain/full-paper/control", "/v2/full-paper/control")
	registerPathAlias(mux, "/api/v1/wisdev-brain/sandbox/execute", "/v2/full-paper/sandbox-action")
	registerPathAlias(mux, "/api/v1/wisdev-brain/drafting/outline", "/v2/drafting/outline")
	registerPathAlias(mux, "/api/v1/wisdev-brain/drafting/section", "/v2/drafting/section")

	// Manuscript drafting and reviewer rebuttal — canonical routes are now 
	// maintained via RegisterV2WisDevRoutes which wires them to Go handlers.
	registerPathAlias(mux, "/api/v1/wisdev-brain/manuscript/draft", "/v2/manuscript/draft")
	registerPathAlias(mux, "/api/v1/wisdev-brain/manuscript/draft/stream", "/v2/manuscript/draft/stream")
	registerPathAlias(mux, "/api/v1/wisdev-brain/reviewer/rebuttal", "/v2/reviewer/rebuttal")
	registerPathAlias(mux, "/api/v1/wisdev-brain/reviewer/rebuttal/stream", "/v2/reviewer/rebuttal/stream")

	// Build middleware chain.
	handler := otelhttp.NewHandler(
		telemetry.MetricsMiddleware(
			telemetry.RequestLogger(
				CORSMiddleware(
					InternalServiceMiddleware(
						AuthMiddleware(
							ResilienceMiddleware(cfg.LLMClient)(mux),
						),
					),
				),
			),
		),
		"wisdev-orchestrator", // span name prefix visible in OpenTelemetry
	)

	return handler
}
