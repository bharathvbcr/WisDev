package wisdev

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/llm"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/policy"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/rag"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/resilience"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/stackconfig"
)

type ExecutionRunner interface {
	RunStepWithRecovery(ctx context.Context, session *AgentSession, step PlanStep, laneID int) StepResult
	CoordinateAgentFeedback(ctx context.Context, session *AgentSession, outcomes []PlanOutcome) (string, error)
	Execute(ctx context.Context, session *AgentSession, out chan<- PlanExecutionEvent)
}

type ExecutionService interface {
	Start(ctx context.Context, sessionID string) (*ExecutionStartResult, error)
	Cancel(ctx context.Context, sessionID string) error
	Abandon(ctx context.Context, sessionID string) error
	Stream(ctx context.Context, sessionID string, emit func(PlanExecutionEvent) error) error
}

type AgentGateway struct {
	Store            SessionStore
	Checkpoints      CheckpointStore
	Registry         *ToolRegistry
	SearchRegistry   *search.ProviderRegistry
	ADKRuntime       *ADKRuntime
	Executor         ExecutionRunner
	LLMClient        *llm.Client
	PythonExecute    func(ctx context.Context, action string, payload map[string]any, session *AgentSession) (map[string]any, error)
	Idempotency      *IdempotencyStore
	PolicyConfig     policy.PolicyConfig
	SessionTTL       time.Duration
	CheckpointTTL    time.Duration
	Journal          *RuntimeJournal
	DB               DBProvider
	StateStore       *RuntimeStateStore
	Redis            redis.UniversalClient
	MemoryStore      MemoryStore
	Memory           *MemoryConsolidator
	ResearchMemory   *ResearchMemoryCompiler
	QuestRuntime     *ResearchQuestRuntime
	Execution        ExecutionService
	ResourceGovernor *resilience.ResourceGovernor

	// New modular WisDev components
	WisdevSessions *SessionManager
	WisdevGuided   *GuidedFlow
	Brain          *BrainCapabilities
	Gate           *rag.EvidenceGate
	Loop           *AutonomousLoop
	Runtime        *UnifiedResearchRuntime
}

var gatewayRunUnifiedResearchLoop = func(
	ctx context.Context,
	runtime *UnifiedResearchRuntime,
	req LoopRequest,
	plane ResearchExecutionPlane,
	emit func(PlanExecutionEvent),
) (*UnifiedResearchResult, error) {
	return runtime.RunLoop(ctx, req, plane, emit)
}

const defaultPythonSidecarURL = "http://127.0.0.1:8090"

func ResolvePythonBase() string {
	if explicit := strings.TrimSpace(stackconfig.ResolveEnv("PYTHON_SIDECAR_HTTP_URL")); explicit != "" {
		return strings.TrimSuffix(explicit, "/")
	}
	if baseURL := strings.TrimSpace(stackconfig.ResolveBaseURL("python_sidecar")); baseURL != "" {
		return strings.TrimSuffix(baseURL, "/")
	}
	return defaultPythonSidecarURL
}

func (gw *AgentGateway) defaultPythonExecutor(ctx context.Context, action string, payload map[string]any, session *AgentSession) (map[string]any, error) {
	// 1. Check if the action can be handled natively by Go BrainCapabilities, EvidenceGate, or AutonomousLoop
	switch CanonicalizeWisdevAction(action) {
	case ActionResearchQueryDecompose:
		if gw.Brain == nil {
			return nil, fmt.Errorf("BrainCapabilities not initialised for action %s", action)
		}
		query, _ := payload["query"].(string)
		domain, _ := payload["domain"].(string)
		model, _ := payload["model"].(string)
		tasks, err := gw.Brain.DecomposeTaskInteractive(ctx, query, domain, model)
		if err != nil {
			return nil, err
		}
		return map[string]any{"tasks": tasks}, nil

	case ActionResearchProposeHypotheses:
		if gw.Brain == nil {
			return nil, fmt.Errorf("BrainCapabilities not initialised for action %s", action)
		}
		query, _ := payload["query"].(string)
		intent, _ := payload["intent"].(string)
		model, _ := payload["model"].(string)
		hypotheses, err := gw.Brain.ProposeHypothesesInteractive(ctx, query, intent, model)
		if err != nil {
			return nil, err
		}
		result := map[string]any{"hypotheses": hypotheses}
		if branches := hypothesesToBranches(hypotheses); len(branches) > 0 {
			result["branches"] = mapsToAny(typedBranchesToMaps(branches))
		}
		return result, nil

	case ActionResearchGenerateHypotheses:
		if gw.Brain == nil {
			return nil, fmt.Errorf("BrainCapabilities not initialised for action %s", action)
		}
		query, _ := payload["query"].(string)
		domain, _ := payload["domain"].(string)
		intent, _ := payload["intent"].(string)
		model, _ := payload["model"].(string)
		hypotheses, err := gw.Brain.GenerateHypothesesInteractive(ctx, query, domain, intent, model)
		if err != nil {
			return nil, err
		}
		result := map[string]any{"hypotheses": hypotheses}
		if branches := hypothesesToBranches(hypotheses); len(branches) > 0 {
			result["branches"] = mapsToAny(typedBranchesToMaps(branches))
		}
		return result, nil

	case "research.coordinateReplan":
		if gw.Brain == nil {
			return nil, fmt.Errorf("BrainCapabilities not initialised for action %s", action)
		}
		failedID, _ := payload["failedStepId"].(string)
		reason, _ := payload["reason"].(string)
		contextData, _ := payload["context"].(map[string]any)
		model, _ := payload["model"].(string)
		tasks, err := gw.Brain.CoordinateReplan(ctx, failedID, reason, contextData, model)
		if err != nil {
			return nil, err
		}
		return map[string]any{"tasks": tasks}, nil

	case ActionResearchSynthesizeAnswer:
		if gw.Brain == nil {
			return nil, fmt.Errorf("BrainCapabilities not initialised for action %s", action)
		}
		query, _ := payload["query"].(string)
		papersRaw, _ := payload["papers"].([]any)
		model, _ := payload["model"].(string)

		var papers []Source
		for _, p := range papersRaw {
			if pm, ok := p.(map[string]any); ok {
				abs := ""
				if a, ok := pm["abstract"].(string); ok && a != "" {
					abs = a
				} else if s, ok := pm["summary"].(string); ok {
					abs = s
				}
				papers = append(papers, Source{
					ID:      fmt.Sprintf("%v", pm["id"]),
					Title:   fmt.Sprintf("%v", pm["title"]),
					Summary: abs,
				})
			}
		}

		answer, err := gw.Brain.SynthesizeAnswer(ctx, query, papers, model)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"text":             answer.RenderText(),
			"structuredAnswer": answer,
		}, nil

	case ActionResearchEvaluateEvidence:
		if gw.Gate == nil {
			return nil, fmt.Errorf("EvidenceGate not initialised for action %s", action)
		}

		// Q4.1: Handle structured answer if provided
		if structuredAnswerRaw, ok := payload["structured_answer"].(map[string]any); ok {
			var structuredAnswer rag.StructuredAnswer
			data, _ := json.Marshal(structuredAnswerRaw)
			if err := json.Unmarshal(data, &structuredAnswer); err == nil {
				papersRaw, _ := payload["sources"].([]any)
				var papers []search.Paper
				for _, p := range papersRaw {
					if pMap, ok := p.(map[string]any); ok {
						data, _ := json.Marshal(pMap)
						var paper search.Paper
						json.Unmarshal(data, &paper)
						papers = append(papers, paper)
					}
				}
				res, err := gw.Gate.RunStructured(ctx, &structuredAnswer, papers)
				if err == nil {
					data, _ := json.Marshal(res)
					var out map[string]any
					json.Unmarshal(data, &out)
					return out, nil
				}
			}
		}

		text, _ := payload["synthesis_text"].(string)
		papersRaw, _ := payload["sources"].([]any)

		var papers []search.Paper
		for _, p := range papersRaw {
			if pm, ok := p.(map[string]any); ok {
				abs := ""
				if a, ok := pm["abstract"].(string); ok && a != "" {
					abs = a
				} else if s, ok := pm["summary"].(string); ok {
					abs = s
				}
				papers = append(papers, search.Paper{
					ID:       fmt.Sprintf("%v", pm["paperId"]),
					Title:    fmt.Sprintf("%v", pm["title"]),
					Abstract: abs,
				})
			}
		}

		result, err := gw.Gate.Run(ctx, text, papers)
		if err != nil {
			return nil, err
		}

		// Convert result to map for JSON compatibility
		b, _ := json.Marshal(result)
		var m map[string]any
		json.Unmarshal(b, &m)
		return m, nil

	case "research.execute-loop":
		if gw.Runtime == nil && gw.Loop != nil {
			gw.Runtime = NewUnifiedResearchRuntime(gw.Loop, gw.SearchRegistry, gw.LLMClient, gw.ProgrammaticLoopExecutor()).
				WithDurableResearchState(gw.StateStore, gw.Journal)
		}
		if gw.Runtime == nil {
			return nil, fmt.Errorf("UnifiedResearchRuntime not initialised for action %s", action)
		}
		query, _ := payload["query"].(string)
		domain, _ := payload["domain"].(string)
		maxIter, _ := payload["maxIterations"].(float64)
		budget, _ := payload["budgetCents"].(float64)

		loopReq := LoopRequest{
			Query:         query,
			OriginalQuery: strings.TrimSpace(query),
			Domain:        domain,
			MaxIterations: int(maxIter),
			BudgetCents:   int(budget),
		}

		runtimeResult, err := gatewayRunUnifiedResearchLoop(ctx, gw.Runtime, loopReq, ResearchExecutionPlaneJob, nil)
		if err != nil {
			return nil, err
		}
		result := runtimeResult.LoopResult

		// Convert result to map
		b, _ := json.Marshal(result)
		var m map[string]any
		json.Unmarshal(b, &m)
		m["engine"] = "unified_research_runtime"
		if result != nil {
			m["finalAnswer"] = result.FinalAnswer
			m["workerReports"] = valueToAny(result.WorkerReports)
		}
		if runtimeResult.State != nil {
			gate := (*ResearchFinalizationGate)(nil)
			if result != nil {
				gate = result.FinalizationGate
			}
			m["answerStatus"] = ResearchAnswerStatusFromState(runtimeResult.State, gate, gate != nil && gate.Ready, firstNonEmpty(runtimeResult.State.StopReason, func() string {
				if result != nil {
					return result.StopReason
				}
				return ""
			}()))
			m["branchEvaluations"] = valueToAny(runtimeResult.State.BranchEvaluations)
			if len(runtimeResult.State.Workers) > 0 {
				m["workerReports"] = valueToAny(runtimeResult.State.Workers)
			}
		}
		return m, nil

	case "research.verifyCitations":
		if gw.Brain == nil {
			return nil, fmt.Errorf("BrainCapabilities not initialised for action %s", action)
		}
		papersRaw, _ := payload["papers"].([]any)
		var papers []Source
		for _, p := range papersRaw {
			if pm, ok := p.(map[string]any); ok {
				papers = append(papers, Source{
					ID:    fmt.Sprintf("%v", pm["id"]),
					Title: fmt.Sprintf("%v", pm["title"]),
					DOI:   AsOptionalString(pm["doi"]),
				})
			}
		}
		model, _ := payload["model"].(string)
		return gw.Brain.VerifyCitationsInteractive(ctx, papers, model)

	case ActionResearchResolveCanonicalCitations:
		if gw.Brain == nil {
			return nil, fmt.Errorf("BrainCapabilities not initialised for action %s", action)
		}
		papers := sourcesFromAnyList(firstNonEmptyValue(payload["papers"], payload["citations"], payload["canonicalSources"]))
		model, _ := payload["model"].(string)
		return gw.Brain.ResolveCanonicalCitations(ctx, papers, model)

	case ActionResearchVerifyReasoningPaths:
		if gw.Brain == nil {
			return nil, fmt.Errorf("BrainCapabilities not initialised for action %s", action)
		}
		model, _ := payload["model"].(string)
		return gw.Brain.VerifyReasoningPaths(ctx, firstArtifactMaps(payload["branches"]), model)

	case "research.snowballCitations":
		if gw.Brain == nil {
			return nil, fmt.Errorf("BrainCapabilities not initialised for action %s", action)
		}
		papersRaw, _ := payload["papers"].([]any)

		var allNewPapers []Source
		// For each seed paper, fetch its citations from the graph
		for _, p := range papersRaw {
			if pm, ok := p.(map[string]any); ok {
				paperID := fmt.Sprintf("%v", pm["id"])

				// Real Citation Graph Lookup via Registry
				citations, err := gw.Loop.searchReg.GetCitations(ctx, paperID, 5)
				if err != nil {
					slog.Warn("Citation lookup failed for paper", "id", paperID, "error", err)
					continue
				}

				for _, c := range citations {
					allNewPapers = append(allNewPapers, Source{
						ID:            c.ID,
						Title:         c.Title,
						Summary:       c.Abstract,
						Link:          c.Link,
						Source:        c.Source,
						DOI:           c.DOI,
						CitationCount: c.CitationCount,
						Year:          c.Year,
					})
				}
			}
		}

		// If we found real citations, return them.
		// If not, fallback to LLM-generated exploratory queries.
		if len(allNewPapers) > 0 {
			return map[string]any{"papers": allNewPapers}, nil
		}

		// Fallback to query generation
		var seedPapers []Source
		for _, p := range papersRaw {
			if pm, ok := p.(map[string]any); ok {
				seedPapers = append(seedPapers, Source{
					ID:    fmt.Sprintf("%v", pm["id"]),
					Title: fmt.Sprintf("%v", pm["title"]),
				})
			}
		}
		model, _ := payload["model"].(string)
		queries, err := gw.Brain.GenerateSnowballQueriesInteractive(ctx, seedPapers, model)
		if err != nil {
			return nil, err
		}
		return map[string]any{"exploratory_queries": queries}, nil

	case "research.buildClaimEvidenceTable":
		if gw.Brain == nil {
			return nil, fmt.Errorf("BrainCapabilities not initialised for action %s", action)
		}
		query, _ := payload["query"].(string)
		papersRaw, _ := payload["papers"].([]any)
		var papers []Source
		for _, p := range papersRaw {
			if pm, ok := p.(map[string]any); ok {
				papers = append(papers, Source{
					ID:      fmt.Sprintf("%v", pm["id"]),
					Title:   fmt.Sprintf("%v", pm["title"]),
					Summary: AsOptionalString(pm["abstract"]),
				})
			}
		}
		model, _ := payload["model"].(string)
		return gw.Brain.BuildClaimEvidenceTableInteractive(ctx, query, papers, model)

	case "research.generateThoughts":
		if gw.Brain == nil {
			return nil, fmt.Errorf("BrainCapabilities not initialised for action %s", action)
		}
		model, _ := payload["model"].(string)
		return gw.Brain.GenerateThoughtsInteractive(ctx, payload, model)

	case "research.detectContradictions":
		if gw.Brain == nil {
			return nil, fmt.Errorf("BrainCapabilities not initialised for action %s", action)
		}
		papersRaw, _ := payload["papers"].([]any)
		var papers []Source
		for _, p := range papersRaw {
			if pm, ok := p.(map[string]any); ok {
				papers = append(papers, Source{
					ID:      fmt.Sprintf("%v", pm["id"]),
					Title:   fmt.Sprintf("%v", pm["title"]),
					Summary: AsOptionalString(pm["abstract"]),
				})
			}
		}
		model, _ := payload["model"].(string)
		return gw.Brain.DetectContradictionsInteractive(ctx, papers, model)

	case "research.verifyClaims":
		if gw.Brain == nil {
			return nil, fmt.Errorf("BrainCapabilities not initialised for action %s", action)
		}
		text, _ := payload["synthesis_text"].(string)
		papersRaw, _ := payload["papers"].([]any)
		var papers []Source
		for _, p := range papersRaw {
			if pm, ok := p.(map[string]any); ok {
				papers = append(papers, Source{
					ID:      fmt.Sprintf("%v", pm["id"]),
					Title:   fmt.Sprintf("%v", pm["title"]),
					Summary: AsOptionalString(pm["abstract"]),
				})
			}
		}
		model, _ := payload["model"].(string)
		return gw.Brain.VerifyClaimsInteractive(ctx, text, papers, model)

	case ActionResearchVerifyClaimsBatch:
		if gw.Brain == nil {
			return nil, fmt.Errorf("BrainCapabilities not initialised for action %s", action)
		}
		outputs := firstArtifactMaps(firstNonEmptyValue(payload["candidateOutputs"], payload["outputs"], payload["claims"]))
		papersRaw, _ := payload["papers"].([]any)
		var papers []Source
		for _, p := range papersRaw {
			if pm, ok := p.(map[string]any); ok {
				papers = append(papers, Source{
					ID:      fmt.Sprintf("%v", pm["id"]),
					Title:   fmt.Sprintf("%v", pm["title"]),
					Summary: AsOptionalString(pm["abstract"]),
				})
			}
		}
		model, _ := payload["model"].(string)
		return gw.Brain.VerifyClaimsBatchInteractive(ctx, outputs, papers, model)

	case "research.systematicReviewPrisma", "review.systematicReviewPrisma":
		if gw.Brain == nil {
			return nil, fmt.Errorf("BrainCapabilities not initialised for action %s", action)
		}
		query, _ := payload["query"].(string)
		papersRaw, _ := payload["papers"].([]any)
		var papers []Source
		for _, p := range papersRaw {
			if pm, ok := p.(map[string]any); ok {
				papers = append(papers, Source{
					ID:      fmt.Sprintf("%v", pm["id"]),
					Title:   fmt.Sprintf("%v", pm["title"]),
					Summary: AsOptionalString(pm["abstract"]),
				})
			}
		}
		model, _ := payload["model"].(string)
		return gw.Brain.SystematicReviewPrismaInteractive(ctx, query, papers, model)

	case "query.enhanceAcademic":
		if gw.Brain == nil {
			return nil, fmt.Errorf("BrainCapabilities not initialised for action %s", action)
		}
		query, _ := payload["query"].(string)
		model, _ := payload["model"].(string)
		enhanced, err := gw.Brain.EnhanceAcademicQuery(ctx, query, model)
		if err != nil {
			return nil, err
		}
		return map[string]any{"enhanced_query": enhanced}, nil

	case "retrieval.selectPrimarySource":
		if gw.Brain == nil {
			return nil, fmt.Errorf("BrainCapabilities not initialised for action %s", action)
		}
		query, _ := payload["query"].(string)
		papersRaw, _ := payload["papers"].([]any)
		var papers []Source
		for _, p := range papersRaw {
			if pm, ok := p.(map[string]any); ok {
				papers = append(papers, Source{
					ID:      fmt.Sprintf("%v", pm["id"]),
					Title:   fmt.Sprintf("%v", pm["title"]),
					Summary: AsOptionalString(pm["abstract"]),
				})
			}
		}
		model, _ := payload["model"].(string)
		return gw.Brain.SelectPrimarySourceInteractive(ctx, query, papers, model)

	case "clarify.askFollowUpIfAmbiguous":
		if gw.Brain == nil {
			return nil, fmt.Errorf("BrainCapabilities not initialised for action %s", action)
		}
		query, _ := payload["query"].(string)
		model, _ := payload["model"].(string)
		return gw.Brain.AskFollowUpIfAmbiguousInteractive(ctx, query, model)
	}

	// 2. Fallback to Python HTTP for remaining specialized endpoints (if any)
	// Currently all core research actions are moved to Go.
	// We keep this for future expansion or non-core tasks.
	return nil, fmt.Errorf("unknown action: %s", action)
}

func NewAgentGateway(db DBProvider, rdb redis.UniversalClient, journal *RuntimeJournal, searchReg ...*search.ProviderRegistry) *AgentGateway {
	policyCfg := policy.DefaultPolicyConfig()
	registry := NewToolRegistry()
	adkRuntime := LoadADKRuntime(registry)
	if adkRuntime != nil && adkRuntime.Config.Policy != nil {
		p := adkRuntime.Config.Policy
		// In unleashed mode these YAML overrides may only RAISE the limits, so a
		// low wisdev-adk.yaml cap cannot silently undo the unleashed policy.
		if p.MaxToolCallsPerSession != nil {
			policyCfg.MaxToolCallsPerSession = reconcileUnleashedPolicyOverride(policyCfg.MaxToolCallsPerSession, *p.MaxToolCallsPerSession)
		}
		if p.MaxScriptRunsPerSession != nil {
			policyCfg.MaxScriptRunsPerSession = reconcileUnleashedPolicyOverride(policyCfg.MaxScriptRunsPerSession, *p.MaxScriptRunsPerSession)
		}
		if p.MaxCostPerSessionCents != nil {
			policyCfg.MaxCostPerSessionCents = reconcileUnleashedPolicyOverride(policyCfg.MaxCostPerSessionCents, *p.MaxCostPerSessionCents)
		}
		slog.Info("wisdev gateway: applied policy overrides from wisdev-adk.yaml",
			"maxToolCalls", policyCfg.MaxToolCallsPerSession,
			"maxScriptRuns", policyCfg.MaxScriptRunsPerSession,
			"maxCostCents", policyCfg.MaxCostPerSessionCents,
		)
	}
	llmClient := llm.NewClient()
	// Wire native structured-output providers (Ollama/OpenAI-compatible or Vertex).
	// Use a short deadline so unreachable GCP metadata does not delay server startup.
	directInitCtx, directInitCancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer directInitCancel()
	llm.WireDirectProviders(directInitCtx, llmClient)
	if llmClient.OpenAICompatible == nil && llmClient.VertexDirect == nil {
		slog.Warn("wisdev gateway: no direct structured-output provider configured — structured output will use Python sidecar (JSON schema may not be enforced)")
	}
	memStore := NewInMemorySessionStore()
	memCheckpoints := NewInMemoryCheckpointStore()
	var sessionStore SessionStore = memStore
	var checkpointStore CheckpointStore = memCheckpoints

	if db != nil {
		sessionStore = NewPostgresSessionStore(db)
		if rdb != nil {
			sessionStore = NewFallbackSessionStore(sessionStore, NewRedisSessionStore(rdb))
		}
		sessionStore = NewFallbackSessionStore(sessionStore, memStore)
	} else if rdb != nil {
		sessionStore = NewFallbackSessionStore(NewRedisSessionStore(rdb), memStore)
		checkpointStore = NewFallbackCheckpointStore(NewRedisCheckpointStore(rdb), memCheckpoints)
	}

	idempotencyWindow := 10 * time.Minute
	if raw := strings.TrimSpace(os.Getenv("WISDEV_IDEMPOTENCY_WINDOW_MINUTES")); raw != "" {
		if minutes, err := strconv.Atoi(raw); err == nil && minutes >= 1 && minutes <= 120 {
			idempotencyWindow = time.Duration(minutes) * time.Minute
		}
	}
	var resolvedSearchRegistry *search.ProviderRegistry
	if len(searchReg) > 0 {
		resolvedSearchRegistry = searchReg[0]
	}
	if resolvedSearchRegistry == nil {
		resolvedSearchRegistry = search.BuildRegistry()
	}
	resolvedSearchRegistry.SetDB(db)
	resolvedSearchRegistry.SetRedis(rdb)
	GlobalDomainOutcomes.SetRedis(rdb)
	memoryStore := NewMemoryStoreFromRedis(rdb)
	GlobalLLMClient = llmClient

	gw := &AgentGateway{
		Store:            sessionStore,
		Checkpoints:      checkpointStore,
		Registry:         registry,
		ADKRuntime:       adkRuntime,
		LLMClient:        llmClient,
		Idempotency:      NewIdempotencyStore(idempotencyWindow),
		PolicyConfig:     policyCfg,
		SessionTTL:       24 * time.Hour,
		CheckpointTTL:    30 * 24 * time.Hour,
		Journal:          journal,
		DB:               db,
		StateStore:       NewRuntimeStateStore(db, journal),
		Redis:            rdb,
		MemoryStore:      memoryStore,
		SearchRegistry:   resolvedSearchRegistry,
		ResourceGovernor: resilience.NewResourceGovernorWithLimits(2048, 512, 100, 5*time.Second),
		WisdevSessions:   NewSessionManager(""),
		WisdevGuided:     NewGuidedFlow(),
		Brain:            NewBrainCapabilities(llmClient),
		Gate:             rag.NewEvidenceGate(llmClient),
		Loop:             NewAutonomousLoop(resolvedSearchRegistry, llmClient),
	}
	gw.QuestRuntime = NewResearchQuestRuntime(gw)
	gw.Memory = NewMemoryConsolidator(db, memoryStore)
	gw.ResearchMemory = NewResearchMemoryCompiler(gw.StateStore, journal)
	gw.PythonExecute = gw.defaultPythonExecutor
	gw.Executor = NewPlanExecutor(registry, policyCfg, llmClient, gw.Brain, rdb, gw.PythonExecute, adkRuntime, resolvedSearchRegistry)
	if gw.ADKRuntime != nil {
		gw.ADKRuntime.Bind(context.Background(), gw)
	}
	gw.Runtime = NewUnifiedResearchRuntime(gw.Loop, resolvedSearchRegistry, llmClient, gw.ProgrammaticLoopExecutor()).
		WithDurableResearchState(gw.StateStore, journal)
	gw.Execution = NewDurableExecutionService(gw)
	return gw
}

func (gw *AgentGateway) RuntimeMetadata() map[string]any {
	if gw == nil || gw.ADKRuntime == nil {
		return map[string]any{
			"enabled":        false,
			"artifactSchema": ArtifactSchemaMetadata(),
		}
	}
	meta := gw.ADKRuntime.Metadata()
	meta["artifactSchema"] = ArtifactSchemaMetadata()
	if card := gw.ADKRuntime.BuildA2ACard(); card != nil {
		meta["a2aCard"] = card
	} else if gw.ADKRuntime.Config.A2A.Enabled {
		meta["a2aCard"] = map[string]any{
			"agentId":  gw.ADKRuntime.Config.Runtime.AgentID,
			"name":     gw.ADKRuntime.Config.Runtime.Name,
			"version":  gw.ADKRuntime.Config.Runtime.Version,
			"protocol": gw.ADKRuntime.Config.A2A.ProtocolVersion,
		}
	}
	return meta
}

func (gw *AgentGateway) ensureADKSession(sessionID string, query string, domain string) *AgentSession {
	return gw.ensureADKSessionWithContext(context.Background(), sessionID, query, domain)
}

func (gw *AgentGateway) ensureADKSessionWithContext(ctx context.Context, sessionID string, query string, domain string) *AgentSession {
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now().UnixMilli()
	lookupSessionID := strings.TrimSpace(sessionID)
	if lookupSessionID == "" {
		sessionID = NewTraceID()
	} else {
		sessionID = lookupSessionID
	}
	policyVersion := ""
	if gw != nil {
		policyVersion = gw.PolicyConfig.PolicyVersion
		if lookupSessionID != "" && gw.Store != nil {
			sessionState, err := gw.GetSession(ctx, sessionID)
			if err == nil && sessionState != nil {
				return sessionState
			}
		}
	}
	rawQuery := strings.TrimSpace(query)
	llmClient := (*llm.Client)(nil)
	if gw != nil {
		llmClient = gw.LLMClient
	}
	originalQuery, correctedQuery, planningQuery, detectedDomain := ApplyEarlySessionQueryPrep(ctx, rawQuery, "", "", strings.TrimSpace(domain), llmClient, false)
	if strings.TrimSpace(correctedQuery) == "" {
		correctedQuery = originalQuery
	}
	if strings.TrimSpace(planningQuery) == "" {
		planningQuery = ResolveSessionQueryText(correctedQuery, originalQuery)
	}
	return &AgentSession{
		SchemaVersion:  "adk-go-v1",
		PolicyVersion:  policyVersion,
		SessionID:      sessionID,
		Query:          planningQuery,
		OriginalQuery:  originalQuery,
		CorrectedQuery: correctedQuery,
		DetectedDomain: detectedDomain,
		Status:         SessionQuestioning,
		Answers:        map[string]Answer{},
		FailureMemory:  map[string]int{},
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

func (gw *AgentGateway) ExecuteADKAction(ctx context.Context, toolDef ToolDefinition, payload map[string]any, sessionState *AgentSession) (map[string]any, error) {
	if sessionState == nil {
		sessionState = gw.ensureADKSessionWithContext(ctx, "", AsOptionalString(payload["query"]), AsOptionalString(payload["domain"]))
	}
	action := CanonicalizeWisdevAction(toolDef.Name)
	switch toolDef.ExecutionTarget {
	case ExecutionTargetGoNative:
		switch action {
		case ActionResearchRetrievePapers, "search":
			query, opts := resolveRetrievePapersSearchOptions(payload, sessionState, false)
			opts.Registry = gw.SearchRegistry
			_, result, err := runRetrievePapers(ctx, gw.Redis, query, opts)
			if err != nil {
				return nil, err
			}
			return result, nil
		case ActionResearchFullPaperRetrieve:
			result, _, err := executeFullPaperRetrieveAction(ctx, gw.Redis, sessionState, payload, false)
			return result, err
		case ActionResearchFullPaperGatewayDispatch:
			result, _, err := executeFullPaperGatewayDispatchAction(ctx, gw.Redis, sessionState, payload, false)
			return result, err
		case ActionResearchVerifyCitations:
			if gw.Brain == nil {
				gw.Brain = NewBrainCapabilities(gw.LLMClient)
			}
			papers := sourcesFromAnyList(firstNonEmptyValue(payload["papers"], payload["citations"]))
			return gw.Brain.VerifyCitationsInteractive(ctx, papers, AsOptionalString(payload["model"]))
		case ActionResearchResolveCanonicalCitations:
			if gw.Brain == nil {
				gw.Brain = NewBrainCapabilities(gw.LLMClient)
			}
			papers := sourcesFromAnyList(firstNonEmptyValue(payload["papers"], payload["citations"]))
			return gw.Brain.ResolveCanonicalCitations(ctx, papers, AsOptionalString(payload["model"]))
		case ActionResearchVerifyReasoningPaths:
			if gw.Brain == nil {
				gw.Brain = NewBrainCapabilities(gw.LLMClient)
			}
			return gw.Brain.VerifyReasoningPaths(ctx, firstArtifactMaps(payload["branches"]), AsOptionalString(payload["model"]))
		case ActionResearchVerifyClaimsBatch:
			if gw.Brain == nil {
				gw.Brain = NewBrainCapabilities(gw.LLMClient)
			}
			outputs := firstArtifactMaps(firstNonEmptyValue(payload["candidateOutputs"], payload["outputs"], payload["claims"]))
			papers := sourcesFromAnyList(firstNonEmptyValue(payload["papers"], payload["sources"], payload["evidence"]))
			return gw.Brain.VerifyClaimsBatchInteractive(ctx, outputs, papers, AsOptionalString(payload["model"]))
		case ActionResearchSynthesizeAnswer:
			if gw.Brain == nil {
				gw.Brain = NewBrainCapabilities(gw.LLMClient)
			}
			papers := sourcesFromAnyList(firstNonEmptyValue(payload["papers"], payload["sources"], payload["evidence"]))
			answer, err := gw.Brain.SynthesizeAnswer(ctx, AsOptionalString(payload["query"]), papers, AsOptionalString(payload["model"]))
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"text":             answer.RenderText(),
				"structuredAnswer": answer,
			}, nil
		}
		return nil, fmt.Errorf("unsupported Go-native WisDev action: %s", action)
	default:
		if gw.PythonExecute != nil {
			return gw.PythonExecute(ctx, action, payload, sessionState)
		}
		return gw.defaultPythonExecutor(ctx, action, payload, sessionState)
	}
}

func (gw *AgentGateway) ProgrammaticLoopExecutor() func(context.Context, string, map[string]any, *AgentSession) (map[string]any, error) {
	if gw == nil {
		return nil
	}
	direct := gw.programmaticLoopExecutorDirect()
	return func(ctx context.Context, action string, payload map[string]any, session *AgentSession) (map[string]any, error) {
		if payload == nil {
			payload = map[string]any{}
		}
		if gw.ADKRuntime != nil && gw.ADKRuntime.delegateExecutor != nil && payload["adkRunnerInternal"] != true {
			return gw.ADKRuntime.ExecuteProgrammaticAction(ctx, action, payload, session)
		}
		return direct(ctx, action, payload, session)
	}
}

func (gw *AgentGateway) programmaticLoopExecutorDirect() func(context.Context, string, map[string]any, *AgentSession) (map[string]any, error) {
	if gw == nil {
		return nil
	}
	return func(ctx context.Context, action string, payload map[string]any, session *AgentSession) (map[string]any, error) {
		if payload == nil {
			payload = map[string]any{}
		}
		toolDef := ToolDefinition{Name: CanonicalizeWisdevAction(action), ExecutionTarget: ExecutionTargetPythonCapability}
		if gw.Registry != nil {
			if registered, err := gw.Registry.Get(action); err == nil {
				toolDef = registered
			} else if registered, err := gw.Registry.Get(CanonicalizeWisdevAction(action)); err == nil {
				toolDef = registered
			}
		}
		return gw.ExecuteADKAction(ctx, toolDef, payload, session)
	}
}

func sourcesFromAnyList(raw any) []Source {
	// Full-fidelity conversion: dropping fields here (authors, link, score,
	// citation counts) previously stripped author names and relevance scores
	// from every step that round-tripped sources through this helper.
	return ArtifactMapsToSources(firstArtifactMaps(raw))
}

func (gw *AgentGateway) CreateSession(ctx context.Context, userID string, query string) (*AgentSession, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	llmClient := (*llm.Client)(nil)
	if gw != nil {
		llmClient = gw.LLMClient
	}
	originalQuery, correctedQuery, planningQuery, detectedDomain := ApplyEarlySessionQueryPrep(ctx, query, "", "", "", llmClient, false)
	if strings.TrimSpace(correctedQuery) == "" {
		correctedQuery = originalQuery
	}
	if strings.TrimSpace(planningQuery) == "" {
		planningQuery = correctedQuery
	}
	sessionID := NewTraceID()
	now := time.Now().UnixMilli()
	session := &AgentSession{
		SessionID:      sessionID,
		UserID:         userID,
		Query:          planningQuery,
		OriginalQuery:  originalQuery,
		CorrectedQuery: correctedQuery,
		DetectedDomain: detectedDomain,
		Status:         SessionQuestioning,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := gw.Store.Put(ctx, session, gw.SessionTTL); err != nil {
		return nil, err
	}
	return session, nil
}

func (gw *AgentGateway) GetSession(ctx context.Context, sessionID string) (*AgentSession, error) {
	return gw.Store.Get(ctx, sessionID)
}
