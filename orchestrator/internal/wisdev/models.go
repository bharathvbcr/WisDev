package wisdev

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/policy"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

type SessionStatus string

type WisDevMode string

const (
	WisDevModeGuided WisDevMode = "guided"
	WisDevModeYOLO   WisDevMode = "yolo"
)

type MemoryEntry struct {
	ID              string  `json:"id"`
	Type            string  `json:"type"`
	Content         string  `json:"content"`
	CreatedAt       int64   `json:"created_at"`
	EvaluationScore float64 `json:"evaluation_score,omitempty"` // R4: Value-based pruning
}

type MemoryTierState struct {
	ShortTermWorking []MemoryEntry `json:"shortTermWorking"`
	LongTermVector   []MemoryEntry `json:"longTermVector"`
	ArtifactMemory   []MemoryEntry `json:"artifactMemory"`
	UserPersonalized []MemoryEntry `json:"userPersonalized"`
}

type ReasoningNodeType string

const (
	ReasoningNodeQuestion   ReasoningNodeType = "question"
	ReasoningNodeAnswer     ReasoningNodeType = "answer"
	ReasoningNodeEvidence   ReasoningNodeType = "evidence"
	ReasoningNodeHypothesis ReasoningNodeType = "hypothesis"
	ReasoningNodeClaim      ReasoningNodeType = "claim"
	ReasoningNodeWorker     ReasoningNodeType = "worker"
	ReasoningNodeGap        ReasoningNodeType = "gap"
)

type ReasoningNode struct {
	ID           string            `json:"id"`
	Text         string            `json:"text"`
	Type         ReasoningNodeType `json:"type"`
	Label        string            `json:"label,omitempty"`
	Depth        int               `json:"depth"`
	ParentID     string            `json:"parentId,omitempty"`
	Children     []string          `json:"children,omitempty"`
	RefinedQuery string            `json:"refinedQuery,omitempty"`
	Confidence   float64           `json:"confidence,omitempty"`
	SourceIDs    []string          `json:"sourceIds,omitempty"`
	Metadata     map[string]any    `json:"metadata,omitempty"`
}

type ReasoningEdge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label,omitempty"`
}

type ReasoningGraph struct {
	Query    string                    `json:"query"`
	Nodes    []ReasoningNode           `json:"nodes"`
	Edges    []ReasoningEdge           `json:"edges"`
	Root     string                    `json:"root,omitempty"`
	NodesMap map[string]*ReasoningNode `json:"-"`
}

type SpecialistType string

const (
	SpecialistTypeMethodologist SpecialistType = "methodologist"
	SpecialistTypeSkeptic       SpecialistType = "skeptic"
	SpecialistTypeSynthesizer   SpecialistType = "synthesizer"
	SpecialistTypeCurator       SpecialistType = "curator"
)

type SpecialistStatus struct {
	Type         SpecialistType `json:"type,omitempty"`
	Verification int            `json:"verification"`
	DeepAnalysis string         `json:"deepAnalysis,omitempty"`
	Reasoning    string         `json:"reasoning,omitempty"`
}

type SpecialistNote struct {
	Type         SpecialistType `json:"type,omitempty"`
	DeepAnalysis string         `json:"deepAnalysis,omitempty"`
	Verification int            `json:"verification"`
}

type EvidenceFinding struct {
	ID              string            `json:"id"`
	Claim           string            `json:"claim"`
	Keywords        []string          `json:"keywords"`
	SourceID        string            `json:"source_id"`
	PaperTitle      string            `json:"paperTitle,omitempty"`
	Snippet         string            `json:"snippet,omitempty"`
	Year            int               `json:"year,omitempty"`
	Confidence      float64           `json:"confidence"`
	Status          string            `json:"status,omitempty"`
	OverlapRatio    float64           `json:"overlapRatio,omitempty"`
	Specialist      SpecialistStatus  `json:"specialist,omitempty"`
	SpecialistNotes []SpecialistNote  `json:"specialistNotes,omitempty"`
	ProvenanceChain []ProvenanceEntry `json:"provenanceChain,omitempty"` // R2: Track discovery path
}

type ReasoningTraceEntry struct {
	Timestamp     int64              `json:"timestamp"`
	Phase         string             `json:"phase"`     // "retrieval", "evaluation", "branching", "debate", "synthesis"
	Decision      string             `json:"decision"`  // What was decided
	Reasoning     string             `json:"reasoning"` // Why
	Alternatives  []string           `json:"alternatives,omitempty"`
	InputBeliefs  map[string]float64 `json:"inputBeliefs,omitempty"`
	OutputBeliefs map[string]float64 `json:"outputBeliefs,omitempty"`
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
	ID                      string             `json:"id"`
	ParentID                string             `json:"parentId,omitempty"` // R1: Tree of Thoughts parent
	Query                   string             `json:"query"`
	Text                    string             `json:"text"`
	Claim                   string             `json:"claim"`
	Category                string             `json:"category,omitempty"`
	FalsifiabilityCondition string             `json:"falsifiabilityCondition"`
	ConfidenceThreshold     float64            `json:"confidenceThreshold"`
	ConfidenceScore         float64            `json:"confidenceScore"`
	Status                  string             `json:"status"`
	IsTerminated            bool               `json:"isTerminated"`
	Evidence                []*EvidenceFinding `json:"evidence,omitempty"`
	Contradictions          []*EvidenceFinding `json:"contradictions,omitempty"`
	ContradictionCount      int                `json:"contradictionCount"`
	EvidenceCount           int                `json:"evidenceCount"`
	CreatedAt               int64              `json:"createdAt,omitempty"`
	UpdatedAt               int64              `json:"updatedAt"`
	AllocatedQueryBudget    int                `json:"allocatedQueryBudget,omitempty"` // R5: Adaptive compute allocation
	EvaluatedAt             int64              `json:"evaluatedAt,omitempty"`          // R1: Track last evaluation
	EvaluationHistory       []EvaluationResult `json:"evaluationHistory,omitempty"`    // R1: Refinement history
}

type ReasoningBranch struct {
	ID                      string             `json:"id"`
	ParentID                string             `json:"parentId,omitempty"` // R1: Tree of Thoughts parent
	Thought                 string             `json:"thought"`
	Claim                   string             `json:"claim"`
	FalsifiabilityCondition string             `json:"falsifiabilityCondition"`
	Status                  string             `json:"status"`
	Findings                []EvidenceFinding  `json:"findings"`
	SupportScore            float64            `json:"supportScore"`
	IsTerminated            bool               `json:"isTerminated"`
	CreatedAt               int64              `json:"createdAt"`
	Source                  string             `json:"source,omitempty"`
	EvaluatedAt             int64              `json:"evaluatedAt,omitempty"`       // R1: Track last evaluation
	EvaluationHistory       []EvaluationResult `json:"evaluationHistory,omitempty"` // R1: Refinement history
}

type ReasoningVerification struct {
	TotalBranches     int  `json:"totalBranches,omitempty"`
	VerifiedBranches  int  `json:"verifiedBranches,omitempty"`
	RejectedBranches  int  `json:"rejectedBranches,omitempty"`
	ReadyForSynthesis bool `json:"readyForSynthesis,omitempty"`
}

type ReasoningArtifactBundle struct {
	Branches              []ReasoningBranch      `json:"branches,omitempty"`
	Verification          *ReasoningVerification `json:"verification,omitempty"`
	MinimumReasoningPaths int                    `json:"minimumReasoningPaths,omitempty"`
}

type ReasoningBundle = ReasoningArtifactBundle

type ClaimEvidenceArtifact struct {
	Table    string `json:"table,omitempty"`
	RowCount int    `json:"rowCount,omitempty"`
}

const ARTIFACT_SCHEMA_VERSION = "artifacts-v1"

type SearchBudget struct {
	QualityMode     string
	MaxSearchTerms  int
	HitsPerSearch   int
	MaxUniquePapers int
}

type ResearchExecutionProfile struct {
	Mode                WisDevMode
	ServiceTier         ServiceTier
	QualityMode         string
	SearchBudget        SearchBudget
	PrimaryModelTier    ModelTier
	PrimaryModelName    string
	SpecialistModelTier ModelTier
	SpecialistModelName string
	MaxIterations       int
	AllocatedTokens     int
	MaxParallelism      int
	TimeoutPerAgent     time.Duration
	ComplexityScore     float64
	EstimatedTokens     int
}

type AgentAssignment struct {
	AgentID    string    `json:"agentId"`
	Role       string    `json:"role"`
	ModelTier  ModelTier `json:"modelTier"`
	ModelName  string    `json:"modelName"`
	Status     string    `json:"status"`
	Required   bool      `json:"required"`
	HeavyModel bool      `json:"heavyModel"`
	DependsOn  []string  `json:"dependsOn,omitempty"`
}

type TaskComplexity string

const (
	ComplexityLow    TaskComplexity = "low"
	ComplexityMedium TaskComplexity = "medium"
	ComplexityHigh   TaskComplexity = "high"
)

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

	TierHeavy    = ModelTierHeavy
	TierStandard = ModelTierStandard
	TierLight    = ModelTierLight
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

type QuestionStopReason string

const (
	QuestionStopReasonNone                       QuestionStopReason = ""
	QuestionStopReasonEvidenceSufficient         QuestionStopReason = "evidence_sufficient"
	QuestionStopReasonClarificationBudgetReached QuestionStopReason = "clarification_budget_reached"
	QuestionStopReasonUserProceed                QuestionStopReason = "user_proceed"
)

type PlanState struct {
	PlanID                   string                     `json:"planId"`
	Steps                    []PlanStep                 `json:"steps"`
	Reasoning                string                     `json:"reasoning"`
	CompletedStepIDs         map[string]bool            `json:"completedStepIds"`
	FailedStepIDs            map[string]string          `json:"failedStepIds"`
	StepAttempts             map[string]int             `json:"stepAttempts"`
	StepFailureCount         map[string]int             `json:"stepFailureCount"`
	ApprovedStepIDs          map[string]bool            `json:"approvedStepIds"`
	StepConfidences          map[string]float64         `json:"stepConfidences,omitempty"`
	StepArtifacts            map[string]StepArtifactSet `json:"stepArtifacts,omitempty"`
	ReplanCount              int                        `json:"replanCount"`
	PendingApprovalID        string                     `json:"pendingApprovalId,omitempty"`
	PendingApprovalTokenHash string                     `json:"pendingApprovalTokenHash,omitempty"`
	PendingApprovalStepID    string                     `json:"pendingApprovalStepId,omitempty"`
	PendingApprovalExpiresAt int64                      `json:"pendingApprovalExpiresAt,omitempty"`
}

type PlanOutcome struct {
	StepID       string   `json:"stepId"`
	Action       string   `json:"action,omitempty"`
	Success      bool     `json:"success"`
	Error        string   `json:"error,omitempty"`
	Summary      string   `json:"summary,omitempty"`
	ResultCount  int      `json:"resultCount,omitempty"`
	ArtifactKeys []string `json:"artifactKeys,omitempty"`
	Confidence   float64  `json:"confidence,omitempty"`
	Degraded     bool     `json:"degraded,omitempty"`
	ResultOrigin string   `json:"resultOrigin,omitempty"`
	SubAgent     string   `json:"subAgent,omitempty"`
}

type PlanStep struct {
	ID                      string          `json:"id"`
	Action                  string          `json:"action"`
	Reason                  string          `json:"reason"`
	Risk                    RiskLevel       `json:"risk"`
	ModelTier               ModelTier       `json:"modelTier,omitempty"`
	ExecutionTarget         ExecutionTarget `json:"executionTarget"`
	Parallelizable          bool            `json:"parallelizable"`
	RequiresHumanCheckpoint bool            `json:"requiresHumanCheckpoint,omitempty"`
	ParallelGroup           string          `json:"parallelGroup,omitempty"`
	DependsOnStepIDs        []string        `json:"dependsOnStepIds,omitempty"`
	EstimatedCostCents      int             `json:"estimatedCostCents"`
	MaxAttempts             int             `json:"maxAttempts"`
	TimeoutMs               int             `json:"timeoutMs"`
	Params                  map[string]any  `json:"params,omitempty"`
}

type ResearchTask struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Action       string   `json:"action"`
	Reason       string   `json:"reason"`
	DependsOnIDs []string `json:"dependsOnIds"`
}

type CitationRecord struct {
	ArxivID  string   `json:"arxiv_id"`
	Title    string   `json:"title"`
	Authors  []string `json:"authors"`
	Year     int      `json:"year"`
	DOI      string   `json:"doi"`
	Abstract string   `json:"abstract"`
}

type CanonicalCitation struct {
	ID                     string                     `json:"id"`
	Title                  string                     `json:"title"`
	DOI                    string                     `json:"doi,omitempty"`
	ArxivID                string                     `json:"arxivId,omitempty"`
	CanonicalID            string                     `json:"canonicalId"`
	Authors                []string                   `json:"authors"`
	Year                   int                        `json:"year"`
	Resolved               bool                       `json:"resolved"`
	Verified               bool                       `json:"verified"`
	VerificationStatus     CitationVerificationStatus `json:"verificationStatus,omitempty"`
	SourceAuthority        string                     `json:"sourceAuthority,omitempty"`
	ResolutionEngine       string                     `json:"resolutionEngine,omitempty"`
	ResolverAgreementCount int                        `json:"resolverAgreementCount,omitempty"`
	ResolverConflict       bool                       `json:"resolverConflict,omitempty"`
	ConflictNote           string                     `json:"conflictNote,omitempty"`
	LandingURL             string                     `json:"landingUrl,omitempty"`
	SemanticScholarID      string                     `json:"semanticScholarId,omitempty"`
	OpenAlexID             string                     `json:"openAlexId,omitempty"`
	ProvenanceHash         string                     `json:"provenanceHash,omitempty"`
}

type CitationVerificationStatus string

const (
	CitationStatusVerified  CitationVerificationStatus = "verified"
	CitationStatusAmbiguous CitationVerificationStatus = "ambiguous"
	CitationStatusRejected  CitationVerificationStatus = "rejected"
)

type CitationArtifactBundle struct {
	Citations        []CanonicalCitation `json:"citations,omitempty"`
	CanonicalSources []CanonicalCitation `json:"canonicalSources,omitempty"`
	VerifiedRecords  []CanonicalCitation `json:"verifiedRecords,omitempty"`
	ResolvedCount    int                 `json:"resolvedCount,omitempty"`
	ValidCount       int                 `json:"validCount,omitempty"`
	InvalidCount     int                 `json:"invalidCount,omitempty"`
	DuplicateCount   int                 `json:"duplicateCount,omitempty"`
}

type CitationBundle = CitationArtifactBundle

type CitationTrustBundle struct {
	Citations         []CanonicalCitation `json:"citations,omitempty"`
	VerifiedCount     int                 `json:"verifiedCount,omitempty"`
	AmbiguousCount    int                 `json:"ambiguousCount,omitempty"`
	RejectedCount     int                 `json:"rejectedCount,omitempty"`
	ResolverTrace     []map[string]any    `json:"resolverTrace,omitempty"`
	PromotionEligible bool                `json:"promotionEligible,omitempty"`
	PromotionGate     map[string]any      `json:"promotionGate,omitempty"`
	BlockingIssues    []string            `json:"blockingIssues,omitempty"`
}

type Source struct {
	ID                       string   `json:"id"`
	Title                    string   `json:"title"`
	Summary                  string   `json:"summary"`
	Abstract                 string   `json:"abstract,omitempty"`
	Link                     string   `json:"link"`
	DOI                      string   `json:"doi,omitempty"`
	ArxivID                  string   `json:"arxivId,omitempty"`
	Source                   string   `json:"source,omitempty"`
	SourceApis               []string `json:"sourceApis,omitempty"`
	SiteName                 string   `json:"siteName,omitempty"`
	Publication              string   `json:"publication,omitempty"`
	Authors                  []string `json:"authors,omitempty"`
	Keywords                 []string `json:"keywords,omitempty"`
	Year                     int      `json:"year,omitempty"`
	Month                    int      `json:"month,omitempty"`
	Score                    float64  `json:"score,omitempty"`
	CitationCount            int      `json:"citationCount,omitempty"`
	ReferenceCount           int      `json:"referenceCount,omitempty"`
	InfluentialCitationCount int      `json:"influentialCitationCount,omitempty"`
	OpenAccessUrl            string   `json:"openAccessUrl,omitempty"`
	PdfUrl                   string   `json:"pdfUrl,omitempty"`
	FullText                 string   `json:"fullText,omitempty"`
	StructureMap             []any    `json:"structureMap,omitempty"`
	SourceCount              int      `json:"-"`
	URL                      string   `json:"url,omitempty"`
}

type PaperArtifactBundle struct {
	Papers              []Source         `json:"papers,omitempty"`
	RetrievalStrategies []string         `json:"retrievalStrategies,omitempty"`
	RetrievalTrace      []map[string]any `json:"retrievalTrace,omitempty"`
	QueryUsed           string           `json:"queryUsed,omitempty"`
	TraceID             string           `json:"traceId,omitempty"`
}

type StepArtifactSet struct {
	StepID                string                   `json:"stepId"`
	Action                string                   `json:"action,omitempty"`
	Artifacts             map[string]any           `json:"artifacts"`
	PaperBundle           *PaperArtifactBundle     `json:"paperBundle,omitempty"`
	CitationBundle        *CitationArtifactBundle  `json:"citationBundle,omitempty"`
	CitationTrustBundle   *CitationTrustBundle     `json:"citationTrustBundle,omitempty"`
	ReasoningBundle       *ReasoningArtifactBundle `json:"reasoningBundle,omitempty"`
	ClaimEvidenceArtifact *ClaimEvidenceArtifact   `json:"claimEvidenceArtifact,omitempty"`
	CreatedAt             int64                    `json:"createdAt"`
}

type EvidenceDossier struct {
	JobID         string              `json:"job_id"`
	Verified      []EvidenceFinding   `json:"verified"`
	Tentative     []EvidenceFinding   `json:"tentative"`
	Contradictory []ContradictionPair `json:"contradictory"`
	Gaps          []string            `json:"gaps"`
}

type SynthesisResult struct {
	Sections  map[string]string `json:"sections,omitempty"`
	CreatedAt time.Time         `json:"createdAt"`
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

type LLMRequester interface {
	Generate(ctx context.Context, req *llmv1.GenerateRequest) (*llmv1.GenerateResponse, error)
	StructuredOutput(ctx context.Context, req *llmv1.StructuredRequest) (*llmv1.StructuredResponse, error)
}

type Model interface {
	Generate(ctx context.Context, prompt string) (string, error)
	GenerateHypotheses(ctx context.Context, query string) ([]string, error)
	ExtractClaims(ctx context.Context, text string) ([]string, error)
	VerifyClaim(ctx context.Context, claim, evidence string) (bool, float64, error)
	SynthesizeFindings(ctx context.Context, hypotheses []string, evidence map[string]interface{}) (string, error)
	CritiqueFindings(ctx context.Context, findings []string) (string, error)
	Name() string
	Tier() ModelTier
}

type VerificationLayer struct {
	config VerificationLayerConfig
}

type VerificationLayerConfig struct {
	EnableSourceScoring          bool
	EnableClaimLinking           bool
	EnableContradictionDetection bool
	EnableConfidenceAggregation  bool
}

func NewVerificationLayer(cfg VerificationLayerConfig) *VerificationLayer {
	return &VerificationLayer{config: cfg}
}

func (v *VerificationLayer) Verify(ctx context.Context, h *Hypothesis) error {
	return nil
}

type AgentSession struct {
	SchemaVersion        string                      `json:"schemaVersion"`
	PolicyVersion        string                      `json:"policyVersion"`
	ID                   string                      `json:"id,omitempty"`
	SessionID            string                      `json:"sessionId"`
	QuestID              string                      `json:"questId,omitempty"`
	UserID               string                      `json:"userId"`
	Query                string                      `json:"query"`
	OriginalQuery        string                      `json:"originalQuery"`
	CorrectedQuery       string                      `json:"correctedQuery"`
	Domain               string                      `json:"domain,omitempty"`
	DetectedDomain       string                      `json:"detectedDomain"`
	SecondaryDomains     []string                    `json:"secondaryDomains,omitempty"`
	Status               SessionStatus               `json:"status"`
	Mode                 WisDevMode                  `json:"mode"`
	ServiceTier          ServiceTier                 `json:"serviceTier"`
	ModeManifest         map[string]any              `json:"modeManifest,omitempty"`
	CurrentQuestionIndex int                         `json:"currentQuestionIndex"`
	QuestionSequence     []string                    `json:"questionSequence,omitempty"`
	MinQuestions         int                         `json:"minQuestions,omitempty"`
	MaxQuestions         int                         `json:"maxQuestions,omitempty"`
	ComplexityScore      float64                     `json:"complexityScore,omitempty"`
	ClarificationBudget  int                         `json:"clarificationBudget,omitempty"`
	QuestionStopReason   QuestionStopReason          `json:"questionStopReason,omitempty"`
	Answers              map[string]Answer           `json:"answers"`
	FailureMemory        map[string]int              `json:"failureMemory,omitempty"`
	Plan                 *PlanState                  `json:"plan,omitempty"`
	ReasoningGraph       *ReasoningGraph             `json:"reasoningGraph,omitempty"`
	MemoryTiers          *MemoryTierState            `json:"memoryTiers,omitempty"`
	Hypotheses           []*Hypothesis               `json:"hypotheses,omitempty"`
	AcceptedClaims       []EvidenceFinding           `json:"acceptedClaims,omitempty"`
	EvidenceDossiers     map[string]*EvidenceDossier `json:"evidenceDossiers,omitempty"`
	CurrentIteration     int                         `json:"currentIteration,omitempty"`
	ResearchScratchpad   map[string]string           `json:"researchScratchpad,omitempty"`
	ReviewerNotes        []string                    `json:"reviewerNotes,omitempty"`
	Synthesis            *SynthesisResult            `json:"synthesis,omitempty"`
	AssessedComplexity   string                      `json:"assessedComplexity,omitempty"`
	ThoughtSignature     string                      `json:"thoughtSignature,omitempty"`
	Budget               policy.BudgetState          `json:"budget"`
	BeliefState          *BeliefState                `json:"beliefState,omitempty"` // R2: Belief tracking
	Lineage              *ResearchLineage            `json:"lineage,omitempty"`     // R4: Provenance lineage
	CreatedAt            int64                       `json:"createdAt"`
	UpdatedAt            int64                       `json:"updatedAt"`
}

func (s *AgentSession) ToSession() *Session {
	if s == nil {
		return nil
	}
	return &Session{
		ID:                   s.SessionID,
		UserID:               s.UserID,
		Query:                s.Query,
		OriginalQuery:        s.OriginalQuery,
		CorrectedQuery:       s.CorrectedQuery,
		DetectedDomain:       s.DetectedDomain,
		SecondaryDomains:     s.SecondaryDomains,
		Answers:              s.Answers,
		CurrentQuestionIndex: s.CurrentQuestionIndex,
		QuestionSequence:     s.QuestionSequence,
		Status:               s.Status,
		BeliefState:          s.BeliefState,
		CreatedAt:            s.CreatedAt,
		UpdatedAt:            s.UpdatedAt,
	}
}

type QuestState = ResearchQuest

type PersistedQuestState struct {
	QuestID   string         `json:"questId"`
	UserID    string         `json:"userId,omitempty"`
	Payload   map[string]any `json:"payload"`
	UpdatedAt int64          `json:"updatedAt"`
}

type IterationRecord struct {
	Iteration  int       `json:"iteration"`
	Timestamp  time.Time `json:"timestamp"`
	Action     string    `json:"action"`
	Change     string    `json:"change,omitempty"`
	TokensUsed int       `json:"tokensUsed,omitempty"`
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
	EventPlanCancelled    PlanExecutionEventType = "plan_cancelled"
)

type PlanExecutionEvent struct {
	Type               PlanExecutionEventType `json:"type"`
	EventID            string                 `json:"eventId,omitempty"`
	TraceID            string                 `json:"traceId"`
	SessionID          string                 `json:"sessionId"`
	PlanID             string                 `json:"planId,omitempty"`
	StepID             string                 `json:"stepId,omitempty"`
	Message            string                 `json:"message,omitempty"`
	Payload            map[string]any         `json:"payload,omitempty"`
	Owner              string                 `json:"owner"`
	SubAgent           string                 `json:"subAgent"`
	OwningComponent    string                 `json:"owningComponent"`
	ResultOrigin       string                 `json:"resultOrigin"`
	ResultConfidence   float64                `json:"resultConfidence"`
	ResultFusionIntent string                 `json:"resultFusionIntent"`
	CreatedAt          int64                  `json:"createdAt"`
}

type ConfirmationRequired struct {
	ApprovalToken  string   `json:"approvalToken"`
	Action         string   `json:"action"`
	Rationale      string   `json:"rationale"`
	AllowedActions []string `json:"allowedActions"`
}

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
	Query                string            `json:"query"` // omitempty removed: empty string must be explicit, not absent
	OriginalQuery        string            `json:"originalQuery"`
	CorrectedQuery       string            `json:"correctedQuery"`
	DetectedDomain       string            `json:"detectedDomain"`
	SecondaryDomains     []string          `json:"secondaryDomains"`
	ExpertiseLevel       ExpertiseLevel    `json:"expertiseLevel"`
	Answers              map[string]Answer `json:"answers"`
	CurrentQuestionIndex int               `json:"currentQuestionIndex"`
	QuestionSequence     []string          `json:"questionSequence,omitempty"`
	Status               SessionStatus     `json:"status"`
	BeliefState          *BeliefState      `json:"beliefState,omitempty"` // R2: Belief tracking
	CreatedAt            int64             `json:"createdAt"`
	UpdatedAt            int64             `json:"updatedAt"`
}

type QuestStateStore interface {
	SaveQuestState(ctx context.Context, questID string, payload map[string]any) error
	LoadQuestState(questID string) (map[string]any, error)
}

func NowMillis() int64 {
	return time.Now().UnixMilli()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func EnsureSessionModeManifest(session *AgentSession) {
	if session == nil {
		return
	}
	if session.ModeManifest == nil {
		session.ModeManifest = BuildModeManifestMap(session.Mode, session.ServiceTier)
	}
}

func BuildModeManifestMap(mode WisDevMode, serviceTier ServiceTier) map[string]any {
	return map[string]any{
		"mode":        string(mode),
		"serviceTier": string(serviceTier),
	}
}

// ==================== R1: Hypothesis Evaluation ====================

// EvaluationResult captures the outcome of evaluating a hypothesis against evidence
type EvaluationResult struct {
	HypothesisID      string   `json:"hypothesisId,omitempty"`
	Score             float64  `json:"score"`
	Verdict           string   `json:"verdict"`                     // "supported", "uncertain", "refuted"
	BranchingDecision string   `json:"branchingDecision,omitempty"` // "keep", "prune", "branch", "backtrack"
	SubHypotheses     []string `json:"subHypotheses,omitempty"`
	MissingEvidence   []string `json:"missingEvidence,omitempty"`
	SuggestedQueries  []string `json:"suggestedQueries,omitempty"`
	Reasoning         string   `json:"reasoning,omitempty"`
	EvaluatedAt       int64    `json:"evaluatedAt"`
}

// ==================== R2: Belief State with Provenance ====================

// ProvenanceEntry tracks the causal chain from gap to query to evidence to belief
type ProvenanceEntry struct {
	GapID       string `json:"gapId,omitempty"`
	QueryID     string `json:"queryId,omitempty"`
	EvidenceID  string `json:"evidenceId,omitempty"`
	Timestamp   int64  `json:"timestamp"`
	Description string `json:"description,omitempty"`
}

// BeliefStatus represents the lifecycle state of a belief
type BeliefStatus string

const (
	BeliefStatusActive  BeliefStatus = "active"
	BeliefStatusRevised BeliefStatus = "revised"
	BeliefStatusRefuted BeliefStatus = "refuted"
)

// Belief represents a claim that the agent currently believes, with supporting/contradicting evidence
type Belief struct {
	ID                    string            `json:"id"`
	Claim                 string            `json:"claim"`
	Confidence            float64           `json:"confidence"`
	SupportingEvidence    []string          `json:"supportingEvidence,omitempty"`    // Evidence IDs
	ContradictingEvidence []string          `json:"contradictingEvidence,omitempty"` // Evidence IDs
	ProvenanceChain       []ProvenanceEntry `json:"provenanceChain,omitempty"`
	SourceFamilies        []string          `json:"sourceFamilies,omitempty"` // R4: Cross-source triangulation
	Triangulated          bool              `json:"triangulated,omitempty"`
	Status                BeliefStatus      `json:"status"`
	CreatedAt             int64             `json:"createdAt"`
	UpdatedAt             int64             `json:"updatedAt"`
	RevisedFromBeliefID   string            `json:"revisedFromBeliefId,omitempty"`
	SupersededByBeliefID  string            `json:"supersededByBeliefId,omitempty"`
}

// TriangulationEntry tracks the cross-source verification status of a claim
type TriangulationEntry struct {
	ID             string   `json:"id"`
	Claim          string   `json:"claim"`
	SourceFamilies []string `json:"sourceFamilies"`
	Status         string   `json:"status"` // "provisional", "triangulated", "conflicting"
	Confidence     float64  `json:"confidence"`
}

// BeliefState maintains the agent's current beliefs (append-only ledger)
type BeliefState struct {
	Beliefs map[string]*Belief `json:"beliefs"` // BeliefID -> Belief
}

// NewBeliefState creates an empty belief state
func NewBeliefState() *BeliefState {
	return &BeliefState{
		Beliefs: make(map[string]*Belief),
	}
}

// AddBelief adds a new belief to the state
func (bs *BeliefState) AddBelief(belief *Belief) {
	if bs.Beliefs == nil {
		bs.Beliefs = make(map[string]*Belief)
	}
	bs.Beliefs[belief.ID] = belief
}

// UpdateBelief updates an existing belief
func (bs *BeliefState) UpdateBelief(beliefID string, updater func(*Belief)) {
	if belief, exists := bs.Beliefs[beliefID]; exists {
		updater(belief)
		belief.UpdatedAt = NowMillis()
	}
}

// ReviseBelief creates a new belief that supersedes an old one
func (bs *BeliefState) ReviseBelief(oldBeliefID string, newBelief *Belief) {
	if oldBelief, exists := bs.Beliefs[oldBeliefID]; exists {
		oldBelief.Status = BeliefStatusRevised
		oldBelief.SupersededByBeliefID = newBelief.ID
		oldBelief.UpdatedAt = NowMillis()
	}
	newBelief.RevisedFromBeliefID = oldBeliefID
	bs.AddBelief(newBelief)
}

// RefuteBelief marks a belief as refuted
func (bs *BeliefState) RefuteBelief(beliefID string) {
	bs.UpdateBelief(beliefID, func(b *Belief) {
		b.Status = BeliefStatusRefuted
	})
}

// GetActiveBeliefs returns all beliefs with status = active
func (bs *BeliefState) GetActiveBeliefs() []*Belief {
	active := make([]*Belief, 0)
	for _, belief := range bs.Beliefs {
		if belief.Status == BeliefStatusActive {
			active = append(active, belief)
		}
	}
	return active
}

type ResearchDurableTaskState struct {
	TaskKey       string              `json:"taskKey"`
	CheckpointKey string              `json:"checkpointKey"`
	Operation     string              `json:"operation"`
	Role          ResearchWorkerRole  `json:"role"`
	Status        string              `json:"status"`
	TimeoutMs     int                 `json:"timeoutMs"`
	RetryPolicy   ResearchRetryPolicy `json:"retryPolicy"`
	Attempt       int                 `json:"attempt"`
	StartedAt     int64               `json:"startedAt"`
	FinishedAt    int64               `json:"finishedAt"`
	FailureReason string              `json:"failureReason,omitempty"`
	Artifacts     map[string]any      `json:"artifacts,omitempty"`
	TraceID       string              `json:"traceId,omitempty"`
}

type ResearchRetryPolicy struct {
	MaxAttempts         int      `json:"maxAttempts"`
	BackoffMs           int      `json:"backoffMs"`
	RetryableErrorCodes []string `json:"retryableErrorCodes,omitempty"`
}

type ResearchCitationGraph struct {
	Query              string                      `json:"query"`
	BackwardQueries    []string                    `json:"backwardQueries,omitempty"`
	ForwardQueries     []string                    `json:"forwardQueries,omitempty"`
	Nodes              []ResearchCitationGraphNode `json:"nodes"`
	Edges              []ResearchCitationGraphEdge `json:"edges"`
	IdentityConflicts  []string                    `json:"identityConflicts,omitempty"`
	DuplicateSourceIDs []string                    `json:"duplicateSourceIds,omitempty"`
	CoverageLedger     []CoverageLedgerEntry       `json:"coverageLedger,omitempty"`
}

type ResearchCitationGraphNode struct {
	ID              string         `json:"id"`
	Title           string         `json:"title"`
	CanonicalID     string         `json:"canonicalId,omitempty"`
	SourceFamily    string         `json:"sourceFamily,omitempty"`
	CitationCount   int            `json:"citationCount,omitempty"`
	IdentityFields  map[string]any `json:"identityFields,omitempty"`
	RetractionCheck string         `json:"retractionCheck,omitempty"`
}

type ResearchCitationGraphEdge struct {
	SourceID string `json:"sourceId"`
	TargetID string `json:"targetId"`
	Kind     string `json:"kind"`
	Context  string `json:"context,omitempty"`
}

type CoverageLedgerEntry struct {
	ID                string   `json:"id"`
	Category          string   `json:"category"`
	Status            string   `json:"status"`
	Title             string   `json:"title"`
	Description       string   `json:"description"`
	SupportingQueries []string `json:"supportingQueries"`
	SourceFamilies    []string `json:"sourceFamilies"`
	ClosureEvidence   []string `json:"closureEvidence,omitempty"`
	DueIteration      int      `json:"dueIteration,omitempty"`
	Confidence        float64  `json:"confidence"`
	Required          bool     `json:"required"`
	Priority          int      `json:"priority"`
	ObligationType    string   `json:"obligationType"`
	OwnerWorker       string   `json:"ownerWorker"`
	Severity          string   `json:"severity"`
}
