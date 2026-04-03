package wisdev

import (
	"encoding/json"
	"errors"
	"sync"
)

var errToolNotFound = errors.New("tool_not_found")

type ToolRegistry struct {
	mu    sync.RWMutex
	tools map[string]ToolDefinition
}

func NewToolRegistry() *ToolRegistry {
	r := &ToolRegistry{
		tools: make(map[string]ToolDefinition),
	}
	r.registerDefaults()
	return r
}

func (r *ToolRegistry) registerDefaults() {
	r.Register(ToolDefinition{
		Name:               "research.initializeFlow",
		Description:        "Analyze query intent and return session bootstrap recommendations.",
		Risk:               RiskLevelLow,
		ExecutionTarget:    ExecutionTargetPythonCapability,
		Parallelizable:     true,
		EstimatedCostCents: 1,
		ParameterSchema:    json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
	})
	r.Register(ToolDefinition{
		Name:               "research.generateQueries",
		Description:        "Generate academic search query set for Plan execution.",
		Risk:               RiskLevelLow,
		ExecutionTarget:    ExecutionTargetPythonCapability,
		Parallelizable:     true,
		EstimatedCostCents: 1,
		ParameterSchema:    json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"domain":{"type":"string"}},"required":["query"]}`),
	})
	r.Register(ToolDefinition{
		Name:               "research.dynamicOptions",
		Description:        "Generate adaptive options for session questions.",
		Risk:               RiskLevelLow,
		ExecutionTarget:    ExecutionTargetPythonCapability,
		Parallelizable:     true,
		EstimatedCostCents: 1,
		ParameterSchema:    json.RawMessage(`{"type":"object","properties":{"questionId":{"type":"string"},"query":{"type":"string"},"domain":{"type":"string"}},"required":["questionId"]}`),
	})
	r.Register(ToolDefinition{
		Name:               "research.recommendedAnswers",
		Description:        "Recommend bounded answer values for active question context.",
		Risk:               RiskLevelLow,
		ExecutionTarget:    ExecutionTargetPythonCapability,
		Parallelizable:     true,
		EstimatedCostCents: 1,
		ParameterSchema:    json.RawMessage(`{"type":"object","properties":{"questionId":{"type":"string"},"query":{"type":"string"},"domain":{"type":"string"}},"required":["questionId"]}`),
	})
	r.Register(ToolDefinition{
		Name:               "research.regenerateOptions",
		Description:        "Regenerate non-duplicate options for dynamic question prompts.",
		Risk:               RiskLevelLow,
		ExecutionTarget:    ExecutionTargetPythonCapability,
		Parallelizable:     true,
		EstimatedCostCents: 1,
		ParameterSchema:    json.RawMessage(`{"type":"object","properties":{"questionId":{"type":"string"},"query":{"type":"string"},"domain":{"type":"string"},"previousOptions":{"type":"array","items":{"type":"string"}}},"required":["questionId"]}`),
	})
	r.Register(ToolDefinition{
		Name:               "research.enhanceQuery",
		Description:        "Expand and normalize research query for broader high-quality retrieval.",
		Risk:               RiskLevelLow,
		ExecutionTarget:    ExecutionTargetPythonCapability,
		Parallelizable:     true,
		EstimatedCostCents: 1,
		ParameterSchema:    json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
	})
	r.Register(ToolDefinition{
		Name:               "research.queryDecompose",
		Description:        "Decompose academic query into intent and evidence requirements.",
		Risk:               RiskLevelLow,
		ModelTier:          ModelTierHeavy,
		ExecutionTarget:    ExecutionTargetPythonCapability,
		Parallelizable:     true,
		EstimatedCostCents: 1,
		ParameterSchema:    json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
	})
	r.Register(ToolDefinition{
		Name:               "research.proposeHypotheses",
		Description:        "Propose research hypotheses and design validation steps based on query decomposition.",
		Risk:               RiskLevelLow,
		ModelTier:          ModelTierHeavy,
		ExecutionTarget:    ExecutionTargetPythonCapability,
		Parallelizable:     false,
		EstimatedCostCents: 2,
		ParameterSchema:    json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
	})
	r.Register(ToolDefinition{
		Name:               "research.coordinateReplan",
		Description:        "Mediate between agents to coordinate a replan when evidence is insufficient or steps fail.",
		Risk:               RiskLevelMedium,
		ModelTier:          ModelTierStandard,
		ExecutionTarget:    ExecutionTargetPythonCapability,
		Parallelizable:     false,
		EstimatedCostCents: 2,
		ParameterSchema:    json.RawMessage(`{"type":"object","properties":{"failedStepId":{"type":"string"},"reason":{"type":"string"}},"required":["failedStepId"]}`),
	})
	r.Register(ToolDefinition{
		Name:               "research.generateThoughts",
		Description:        "Generate exploratory thoughts and hypotheses for MCTS expansion.",
		Risk:               RiskLevelLow,
		ModelTier:          ModelTierHeavy,
		ExecutionTarget:    ExecutionTargetPythonCapability,
		Parallelizable:     true,
		EstimatedCostCents: 2,
		ParameterSchema:    json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"depth":{"type":"integer"}},"required":["query"]}`),
	})
	r.Register(ToolDefinition{
		Name:               "research.retrievePapers",
		Description:        "Run high-throughput retrieval across academic sources.",
		Risk:               RiskLevelLow,
		ModelTier:          ModelTierLight,
		ExecutionTarget:    ExecutionTargetGoNative,
		Parallelizable:     true,
		EstimatedCostCents: 1,
		ParameterSchema:    json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"limit":{"type":"integer"}},"required":["query"]}`),
	})
	r.Register(ToolDefinition{
		Name:               "research.fullPaperRetrieve",
		Description:        "Run bounded multi-query retrieval for Full Paper Mode and return trajectory plus Source bundles.",
		Risk:               RiskLevelLow,
		ExecutionTarget:    ExecutionTargetGoNative,
		Parallelizable:     true,
		EstimatedCostCents: 2,
		ParameterSchema:    json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"planQueries":{"type":"array","items":{"type":"string"}},"categories":{"type":"array","items":{"type":"string"}},"limit":{"type":"integer"},"domain":{"type":"string"}},"required":["query"]}`),
	})
	r.Register(ToolDefinition{
		Name:               "research.fullPaperGatewayDispatch",
		Description:        "Execute bounded, stage-scoped Full Paper gateway actions such as academic search and Source bundle preview.",
		Risk:               RiskLevelLow,
		ExecutionTarget:    ExecutionTargetGoNative,
		Parallelizable:     false,
		EstimatedCostCents: 2,
		ParameterSchema:    json.RawMessage(`{"type":"object","properties":{"action":{"type":"string"},"query":{"type":"string"},"stageId":{"type":"string"},"limit":{"type":"integer"},"input":{"type":"object"}},"required":["action","stageId"]}`),
	})
	r.Register(ToolDefinition{
		Name:               "research.buildClaimEvidenceTable",
		Description:        "Verify claim-evidence coverage and contradiction counts.",
		Risk:               RiskLevelMedium,
		ExecutionTarget:    ExecutionTargetPythonCapability,
		Parallelizable:     false,
		EstimatedCostCents: 3,
		ParameterSchema:    json.RawMessage(`{"type":"object","properties":{"claims":{"type":"array"}}}`),
	})
	r.Register(ToolDefinition{
		Name:               "research.graphRagMap",
		Description:        "Build entity-relation map for graph-grounded retrieval.",
		Risk:               RiskLevelLow,
		ExecutionTarget:    ExecutionTargetPythonCapability,
		Parallelizable:     true,
		EstimatedCostCents: 2,
		ParameterSchema:    json.RawMessage(`{"type":"object","properties":{"context_documents":{"type":"array","items":{"type":"string"}}}}`),
	})
	r.Register(ToolDefinition{
		Name:               "research.systematicReviewPrisma",
		Description:        "Construct PRISMA flow summary from screening counts.",
		Risk:               RiskLevelLow,
		ExecutionTarget:    ExecutionTargetPythonCapability,
		Parallelizable:     false,
		EstimatedCostCents: 2,
		ParameterSchema:    json.RawMessage(`{"type":"object","properties":{"records_identified":{"type":"integer"},"records_screened":{"type":"integer"},"full_text_assessed":{"type":"integer"},"studies_included":{"type":"integer"}}}`),
	})
	r.Register(ToolDefinition{
		Name:               "research.snowballCitations",
		Description:        "Generate forward/backward citation snowball traversal Plan.",
		Risk:               RiskLevelLow,
		ExecutionTarget:    ExecutionTargetPythonCapability,
		Parallelizable:     true,
		EstimatedCostCents: 2,
		ParameterSchema:    json.RawMessage(`{"type":"object","properties":{"seed_paper_ids":{"type":"array","items":{"type":"string"}},"max_depth":{"type":"integer"}}}`),
	})
	r.Register(ToolDefinition{
		Name:               "research.verifyClaims",
		Description:        "Run claim verification and grounding confidence scoring.",
		Risk:               RiskLevelMedium,
		ExecutionTarget:    ExecutionTargetPythonCapability,
		Parallelizable:     false,
		EstimatedCostCents: 3,
		ParameterSchema:    json.RawMessage(`{"type":"object","properties":{"claims":{"type":"array","items":{"type":"string"}},"context_documents":{"type":"array","items":{"type":"string"}}}}`),
	})
	r.Register(ToolDefinition{
		Name:               "research.verifyCitations",
		Description:        "Run citation metadata consistency and integrity checks.",
		Risk:               RiskLevelMedium,
		ExecutionTarget:    ExecutionTargetPythonCapability,
		Parallelizable:     false,
		EstimatedCostCents: 3,
		ParameterSchema:    json.RawMessage(`{"type":"object","properties":{"citations":{"type":"array","items":{"type":"object","additionalProperties":true}}}}`),
	})
	r.Register(ToolDefinition{
		Name:               "research.finalDraft",
		Description:        "Generate final structured draft from synthesized findings.",
		Risk:               RiskLevelLow,
		ExecutionTarget:    ExecutionTargetPythonCapability,
		Parallelizable:     false,
		EstimatedCostCents: 2,
		ParameterSchema:    json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"},"findings":{"type":"array","items":{"type":"string"}}}}`),
	})
	r.Register(ToolDefinition{
		Name:               "script.runResearchPrimitive",
		Description:        "Run allowlisted research primitive in sandbox.",
		Risk:               RiskLevelMedium,
		ExecutionTarget:    ExecutionTargetPythonSandbox,
		Parallelizable:     false,
		EstimatedCostCents: 5,
		ParameterSchema:    json.RawMessage(`{"type":"object","properties":{"primitiveId":{"type":"string"},"input":{"type":"object"}},"required":["primitiveId"]}`),
	})
}

func (r *ToolRegistry) Register(Tool ToolDefinition) {
	if Tool.ModelTier == "" {
		switch Tool.Risk {
		case RiskLevelHigh:
			Tool.ModelTier = ModelTierHeavy
		case RiskLevelMedium:
			Tool.ModelTier = ModelTierStandard
		default:
			Tool.ModelTier = ModelTierLight
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[Tool.Name] = Tool
}

func (r *ToolRegistry) Get(name string) (ToolDefinition, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	Tool, ok := r.tools[name]
	if !ok {
		return ToolDefinition{}, errToolNotFound
	}
	return Tool, nil
}

func (r *ToolRegistry) List() []ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ToolDefinition, 0, len(r.tools))
	for _, Tool := range r.tools {
		out = append(out, Tool)
	}
	return out
}
