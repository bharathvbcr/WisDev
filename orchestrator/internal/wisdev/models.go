package wisdev

import (
	"context"
	"encoding/json"
	"time"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/policy"
	llmv1 "github.com/wisdev-agent/wisdev-agent-os/orchestrator/proto/llm/v1"
)

type SessionStatus string

const (
	SessionQuestioning    SessionStatus = "questioning"
	SessionGeneratingTree SessionStatus = "generating_tree"
	SessionEditingTree    SessionStatus = "editing_tree"
	SessionExecutingPlan  SessionStatus = "searching"
	SessionPaused         SessionStatus = "paused"
	SessionComplete       SessionStatus = "complete"
	SessionFailed         SessionStatus = "failed"
)

const (
	StatusQuestioning    SessionStatus = "questioning"
	StatusGeneratingTree SessionStatus = "generating_tree"
	StatusEditingTree    SessionStatus = "editing_tree"
	StatusSearching      SessionStatus = "searching"
	StatusComplete       SessionStatus = "complete"
	StatusAbandoned      SessionStatus = "abandoned"
)

type ExecutionTarget string

const (
	ExecutionTargetGoNative         ExecutionTarget = "GO_NATIVE"
	ExecutionTargetPythonCapability ExecutionTarget = "PYTHON_CAPABILITY"
	ExecutionTargetPythonSandbox    ExecutionTarget = "PYTHON_SANDBOX"
)

type RiskLevel string

const (
	RiskLevelLow    RiskLevel = "low"
	RiskLevelMedium RiskLevel = "medium"
	RiskLevelHigh   RiskLevel = "high"
)

type ModelTier string

const (
	ModelTierHeavy    ModelTier = "heavy"
	ModelTierStandard ModelTier = "standard"
	ModelTierLight    ModelTier = "light"
)

func ToPolicyRisk(risk RiskLevel) policy.RiskLevel {
	switch risk {
	case RiskLevelHigh:
		return policy.RiskHigh
	case RiskLevelMedium:
		return policy.RiskMedium
	default:
		return policy.RiskLow
	}
}

type QuestionOption struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Icon        string `json:"icon,omitempty"`
}

type QuestionAnswer struct {
	QuestionID    string   `json:"questionId"`
	Values        []string `json:"values"`
	DisplayValues []string `json:"displayValues,omitempty"`
	AnsweredAt    int64    `json:"answeredAt"`
}

type QuestionStopReason string

const (
	QuestionStopReasonNone                       QuestionStopReason = ""
	QuestionStopReasonEvidenceSufficient         QuestionStopReason = "evidence_sufficient"
	QuestionStopReasonClarificationBudgetReached QuestionStopReason = "clarification_budget_reached"
	QuestionStopReasonUserProceed                QuestionStopReason = "user_proceed"
)

type PlanState struct {
	PlanID                   string             `json:"planId"`
	Steps                    []PlanStep         `json:"steps"`
	Reasoning                string             `json:"reasoning"`
	CompletedStepIDs         map[string]bool    `json:"completedStepIds"`
	FailedStepIDs            map[string]string  `json:"failedStepIds"`
	StepAttempts             map[string]int     `json:"stepAttempts"`
	StepFailureCount         map[string]int     `json:"stepFailureCount"`
	ApprovedStepIDs          map[string]bool    `json:"approvedStepIds"`
	StepConfidences          map[string]float64 `json:"stepConfidences,omitempty"`
	ReplanCount              int                `json:"replanCount"`
	PendingApprovalID        string             `json:"pendingApprovalId,omitempty"`
	PendingApprovalStepID    string             `json:"pendingApprovalStepId,omitempty"`
	PendingApprovalExpiresAt int64              `json:"pendingApprovalExpiresAt,omitempty"`
}

type PlanOutcome struct {
	StepID  string `json:"stepId"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

type PlanStep struct {
	ID                 string          `json:"id"`
	Action             string          `json:"action"`
	Reason             string          `json:"reason"`
	Risk               RiskLevel       `json:"risk"`
	ModelTier          ModelTier       `json:"modelTier,omitempty"`
	ExecutionTarget    ExecutionTarget `json:"executionTarget"`
	Parallelizable     bool            `json:"parallelizable"`
	ParallelGroup      string          `json:"parallelGroup,omitempty"`
	DependsOnStepIDs   []string        `json:"dependsOnStepIds,omitempty"`
	EstimatedCostCents int             `json:"estimatedCostCents"`
	MaxAttempts        int             `json:"maxAttempts"`
	TimeoutMs          int             `json:"timeoutMs"`
	Params             map[string]any  `json:"params,omitempty"`
}

type CitationRecord struct {
	ArxivID  string   `json:"arxiv_id"`
	Title    string   `json:"title"`
	Authors  []string `json:"authors"`
	Year     int      `json:"year"`
	DOI      string   `json:"doi"`
	Abstract string   `json:"abstract"`
}

type EvidenceFinding struct {
	ID         string   `json:"id"`
	Claim      string   `json:"claim"`
	Keywords   []string `json:"keywords"`  // used for topic-cluster filtering
	SourceID   string   `json:"source_id"` // arxiv ID or DOI
	Confidence float64  `json:"confidence"`
}

type ContradictionSeverity string

const (
	ContradictionLow    ContradictionSeverity = "low"
	ContradictionMedium ContradictionSeverity = "medium"
	ContradictionHigh   ContradictionSeverity = "high"
)

type ContradictionPair struct {
	FindingA    EvidenceFinding       `json:"finding_a"`
	FindingB    EvidenceFinding       `json:"finding_b"`
	Severity    ContradictionSeverity `json:"severity"`
	Explanation string                `json:"explanation"`
}

type Hypothesis struct {
	Claim                   string  `json:"claim"`
	FalsifiabilityCondition string  `json:"falsifiabilityCondition"`
	ConfidenceThreshold     float64 `json:"confidenceThreshold"`
	IsTerminated            bool    `json:"isTerminated"`
}

type EvidenceDossier struct {
	JobID         string              `json:"job_id"`
	Verified      []EvidenceFinding   `json:"verified"`
	Tentative     []EvidenceFinding   `json:"tentative"`
	Contradictory []ContradictionPair `json:"contradictory"`
	Gaps          []string            `json:"gaps"`
}

type SkillParam struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
}

type SkillSchema struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	Inputs       []any          `json:"inputs"`
	Outputs      []any          `json:"outputs"`
	Steps        []string       `json:"steps"`
	CodeTemplate string         `json:"code_template"`
	SourcePaper  CitationRecord `json:"source_paper"`
}

type MemoryEntry struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Content   string `json:"content"`
	CreatedAt int64  `json:"created_at"`
}

type LLMRequester interface {
	Generate(ctx context.Context, req *llmv1.GenerateRequest) (*llmv1.GenerateResponse, error)
	StructuredOutput(ctx context.Context, req *llmv1.StructuredRequest) (*llmv1.StructuredResponse, error)
}

type AgentSession struct {
	SchemaVersion        string                    `json:"schemaVersion"`
	PolicyVersion        string                    `json:"policyVersion"`
	SessionID            string                    `json:"sessionId"`
	UserID               string                    `json:"userId"`
	OriginalQuery        string                    `json:"originalQuery"`
	CorrectedQuery       string                    `json:"correctedQuery"`
	DetectedDomain       string                    `json:"detectedDomain"`
	SecondaryDomains     []string                  `json:"secondaryDomains,omitempty"`
	Status               SessionStatus             `json:"status"`
	CurrentQuestionIndex int                       `json:"currentQuestionIndex"`
	QuestionSequence     []string                  `json:"questionSequence,omitempty"`
	MinQuestions         int                       `json:"minQuestions,omitempty"`
	MaxQuestions         int                       `json:"maxQuestions,omitempty"`
	ComplexityScore      float64                   `json:"complexityScore,omitempty"`
	ClarificationBudget  int                       `json:"clarificationBudget,omitempty"`
	QuestionStopReason   QuestionStopReason        `json:"questionStopReason,omitempty"`
	Answers              map[string]QuestionAnswer `json:"answers"`
	FailureMemory        map[string]int            `json:"failureMemory,omitempty"`
	Plan                 *PlanState                `json:"plan,omitempty"`
	AssessedComplexity   string                    `json:"assessedComplexity,omitempty"`
	Budget               policy.BudgetState        `json:"budget"`
	CreatedAt            int64                     `json:"createdAt"`
	UpdatedAt            int64                     `json:"updatedAt"`
}

type ToolDefinition struct {
	Name               string          `json:"name"`
	Description        string          `json:"description"`
	Risk               RiskLevel       `json:"risk"`
	ModelTier          ModelTier       `json:"modelTier,omitempty"`
	ParameterSchema    json.RawMessage `json:"parameterSchema"`
	ExecutionTarget    ExecutionTarget `json:"executionTarget"`
	Parallelizable     bool            `json:"parallelizable"`
	Dependencies       []string        `json:"dependencies,omitempty"`
	EstimatedCostCents int             `json:"estimatedCostCents,omitempty"`
}

type PlanExecutionEventType string

const (
	EventStepStarted      PlanExecutionEventType = "step_started"
	EventStepCompleted    PlanExecutionEventType = "step_completed"
	EventStepFailed       PlanExecutionEventType = "step_failed"
	EventConfirmationNeed PlanExecutionEventType = "confirmation_needed"
	EventPlanRevised      PlanExecutionEventType = "plan_revised"
	EventPaperFound       PlanExecutionEventType = "paper_found"
	EventProgress         PlanExecutionEventType = "progress"
	EventCompleted        PlanExecutionEventType = "completed"
)

type PlanExecutionEvent struct {
	Type      PlanExecutionEventType `json:"type"`
	TraceID   string                 `json:"traceId"`
	SessionID string                 `json:"sessionId"`
	PlanID    string                 `json:"planId,omitempty"`
	StepID    string                 `json:"stepId,omitempty"`
	Message   string                 `json:"message,omitempty"`
	Payload   map[string]any         `json:"payload,omitempty"`
	CreatedAt int64                  `json:"createdAt"`
}

type ConfirmationRequired struct {
	ApprovalToken  string   `json:"approvalToken"`
	Action         string   `json:"action"`
	Rationale      string   `json:"rationale"`
	AllowedActions []string `json:"allowedActions"`
}

func NowMillis() int64 {
	return time.Now().UnixMilli()
}

// --- V3 Specifics ---

type QuestionType string

const (
	TypeDomain        QuestionType = "domain"
	TypeScope         QuestionType = "scope"
	TypeTimeframe     QuestionType = "timeframe"
	TypeSubtopics     QuestionType = "subtopics"
	TypeStudyTypes    QuestionType = "study_types"
	TypeExclusions    QuestionType = "exclusions"
	TypeClarification QuestionType = "clarification"
)

type Question struct {
	ID            string           `json:"id"`
	Type          QuestionType     `json:"type"`
	Question      string           `json:"question"`
	Options       []QuestionOption `json:"options"`
	IsMultiSelect bool             `json:"isMultiSelect"`
	IsRequired    bool             `json:"isRequired"`
	HelpText      string           `json:"helpText,omitempty"`
}

type Answer struct {
	QuestionID    string   `json:"questionId"`
	Values        []string `json:"values"`
	DisplayValues []string `json:"displayValues,omitempty"`
	AnsweredAt    int64    `json:"answeredAt"`
}

type Session struct {
	ID                   string            `json:"sessionId"`
	UserID               string            `json:"userId"`
	OriginalQuery        string            `json:"originalQuery"`
	CorrectedQuery       string            `json:"correctedQuery"`
	DetectedDomain       string            `json:"detectedDomain"`
	SecondaryDomains     []string          `json:"secondaryDomains"`
	ExpertiseLevel       ExpertiseLevel    `json:"expertiseLevel"`
	Answers              map[string]Answer `json:"answers"`
	CurrentQuestionIndex int               `json:"currentQuestionIndex"`
	QuestionSequence     []string          `json:"questionSequence,omitempty"`
	Status               SessionStatus     `json:"status"`
	CreatedAt            int64             `json:"createdAt"`
	UpdatedAt            int64             `json:"updatedAt"`
}
