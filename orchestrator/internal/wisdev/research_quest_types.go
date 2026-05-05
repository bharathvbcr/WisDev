package wisdev

import (
	"context"
	"time"
)

type QuestStage string

const (
	QuestStageInit       QuestStage = "init"
	QuestStageRetrieve   QuestStage = "retrieve"
	QuestStageHypotheses QuestStage = "hypotheses"
	QuestStageReason     QuestStage = "reason"
	QuestStageVerify     QuestStage = "verify"
	QuestStageDraft      QuestStage = "draft"
	QuestStageCritique   QuestStage = "critique"
	QuestStageComplete   QuestStage = "complete"
)

type QuestStatus string

const (
	QuestStatusRunning  QuestStatus = "running"
	QuestStatusBlocked  QuestStatus = "blocked"
	QuestStatusComplete QuestStatus = "complete"
	QuestStatusFailed   QuestStatus = "failed"
)

type QuestEvent struct {
	EventID   string         `json:"eventId"`
	Type      string         `json:"type"`
	Summary   string         `json:"summary"`
	CreatedAt int64          `json:"createdAt"`
	Payload   map[string]any `json:"payload,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	Stage     QuestStage     `json:"stage,omitempty"`
	Retryable bool           `json:"retryable,omitempty"`
}

type CitationAuthorityRecord struct {
	ID                 string `json:"id,omitempty"`
	Authority          string `json:"authority,omitempty"`
	CanonicalID        string `json:"canonicalId,omitempty"`
	DOI                string `json:"doi,omitempty"`
	ArxivID            string `json:"arxivId,omitempty"`
	Title              string `json:"title,omitempty"`
	Resolved           bool   `json:"resolved"`
	Verified           bool   `json:"verified"`
	AgreementCount     int    `json:"agreementCount,omitempty"`
	ResolutionEngine   string `json:"resolutionEngine,omitempty"`
	VerificationStatus string `json:"verificationStatus,omitempty"`
	SourceAuthority    string `json:"sourceAuthority,omitempty"`
	ResolverConflict   bool   `json:"resolverConflict,omitempty"`
	ConflictNote       string `json:"conflictNote,omitempty"`
	LandingURL         string `json:"landingUrl,omitempty"`
	SemanticScholarID  string `json:"semanticScholarId,omitempty"`
	OpenAlexID         string `json:"openAlexId,omitempty"`
	ProvenanceHash     string `json:"provenanceHash,omitempty"`
}

type CitationVerdict struct {
	Status              string         `json:"status"`
	Promoted            bool           `json:"promoted"`
	VerifiedCount       int            `json:"verifiedCount,omitempty"`
	AmbiguousCount      int            `json:"ambiguousCount,omitempty"`
	RejectedCount       int            `json:"rejectedCount,omitempty"`
	BlockingIssues      []string       `json:"blockingIssues,omitempty"`
	ConflictNote        string         `json:"conflictNote,omitempty"`
	RequiresHumanReview bool           `json:"requiresHumanReview,omitempty"`
	AgreementSources    []string       `json:"agreementSources,omitempty"`
	PromotionGate       map[string]any `json:"promotionGate,omitempty"`
}

type QuestBranchRecord struct {
	ID      string `json:"id"`
	Content string `json:"content"`
}

type QuestMemoryState struct {
	ShortTermWorking []MemoryEntry   `json:"shortTermWorking,omitempty"`
	LongTermVector   []MemoryEntry   `json:"longTermVector,omitempty"`
	ArtifactMemory   []MemoryEntry   `json:"artifactMemory,omitempty"`
	UserPersonalized []MemoryEntry   `json:"userPersonalized,omitempty"`
	PromotionRules   map[string]bool `json:"promotionRules,omitempty"`
}

type ResearchQuest struct {
	ID                     string                      `json:"id,omitempty"`
	SessionID              string                      `json:"sessionId,omitempty"`
	QuestID                string                      `json:"questId"`
	UserID                 string                      `json:"userId,omitempty"`
	Query                  string                      `json:"query"`
	Domain                 string                      `json:"domain,omitempty"`
	DetectedDomain         string                      `json:"detectedDomain,omitempty"`
	Status                 QuestStatus                 `json:"status"`
	CurrentStage           QuestStage                  `json:"currentStage"`
	Mode                   WisDevMode                  `json:"mode"`
	ServiceTier            ServiceTier                 `json:"serviceTier"`
	QualityMode            string                      `json:"qualityMode,omitempty"`
	PersistUserPreferences bool                        `json:"persistUserPreferences,omitempty"`
	Memory                 QuestMemoryState            `json:"memory"`
	ExecutionProfile       ResearchExecutionProfile    `json:"executionProfile"`
	DecisionModelTier      ModelTier                   `json:"decisionModelTier,omitempty"`
	HeavyModelRequired     bool                        `json:"heavyModelRequired,omitempty"`
	AgentAssignments       []AgentAssignment           `json:"agentAssignments,omitempty"`
	Papers                 []Source                    `json:"papers,omitempty"`
	RetrievedCount         int                         `json:"retrievedCount,omitempty"`
	Hypotheses             []*Hypothesis               `json:"hypotheses,omitempty"`
	AcceptedClaims         []EvidenceFinding           `json:"acceptedClaims,omitempty"`
	CoverageLedger         []CoverageLedgerEntry       `json:"coverageLedger,omitempty"`
	RejectedBranches       []QuestBranchRecord         `json:"rejectedBranches,omitempty"`
	CitationAuthorities    []CitationAuthorityRecord   `json:"citationAuthorities,omitempty"`
	CitationVerdict        CitationVerdict             `json:"citationVerdict"`
	BlockingIssues         []string                    `json:"blockingIssues,omitempty"`
	Artifacts              map[string]any              `json:"artifacts,omitempty"`
	Events                 []QuestEvent                `json:"events,omitempty"`
	FinalAnswer            string                      `json:"finalAnswer,omitempty"`
	ReviewerNotes          []string                    `json:"reviewerNotes,omitempty"`
	EvidenceDossiers       map[string]*EvidenceDossier `json:"evidenceDossiers,omitempty"`
	ResearchScratchpad     map[string]string           `json:"researchScratchpad,omitempty"`
	Synthesis              *SynthesisResult            `json:"synthesis,omitempty"`
	CurrentIteration       int                         `json:"currentIteration,omitempty"`
	CreatedAt              int64                       `json:"createdAt"`
	UpdatedAt              int64                       `json:"updatedAt"`
}

type ResearchQuestRequest struct {
	UserID                 string `json:"userId,omitempty"`
	Query                  string `json:"query"`
	Domain                 string `json:"domain,omitempty"`
	Mode                   string `json:"mode,omitempty"`
	QualityMode            string `json:"qualityMode,omitempty"`
	MaxIterations          int    `json:"maxIterations,omitempty"`
	PersistUserPreferences bool   `json:"persistUserPreferences,omitempty"`
	ForceResume            bool   `json:"forceResume,omitempty"`
}

type branchReasoningOutcome struct {
	AcceptedClaims   []EvidenceFinding   `json:"acceptedClaims,omitempty"`
	RejectedBranches []QuestBranchRecord `json:"rejectedBranches,omitempty"`
	Payload          map[string]any      `json:"payload,omitempty"`
}

type ResearchQuestHooks struct {
	DecomposeFn  func(context.Context, *ResearchQuest) (map[string]any, error)
	RetrieveFn   func(context.Context, *ResearchQuest) ([]Source, map[string]any, error)
	HypothesisFn func(context.Context, *ResearchQuest, []Source) ([]Hypothesis, error)
	BranchFn     func(context.Context, *ResearchQuest, []Source, []Hypothesis) (branchReasoningOutcome, error)
	CitationFn   func(context.Context, *ResearchQuest, []Source) ([]CitationAuthorityRecord, CitationVerdict, map[string]any, error)
	DraftFn      func(context.Context, *ResearchQuest, []Source, map[string]any) (string, error)
	CritiqueFn   func(context.Context, *ResearchQuest, []Source, CitationVerdict) (string, error)
	DossierFn    func(context.Context, *ResearchQuest, []Source) (map[string]*EvidenceDossier, error)
}

type persistedQuestCheckpoint struct {
	QuestID        string         `json:"questId"`
	Status         QuestStatus    `json:"status"`
	CurrentStage   QuestStage     `json:"currentStage"`
	RetrievedCount int            `json:"retrievedCount,omitempty"`
	Papers         []Source       `json:"papers,omitempty"`
	Payload        map[string]any `json:"payload"`
	UpdatedAt      int64          `json:"updatedAt"`
}

type ResearchQuestRuntime struct {
	gateway       *AgentGateway
	stateStore    *RuntimeStateStore
	checkpoints   CheckpointStore
	memoryStore   MemoryStore
	memory        *MemoryConsolidator
	workingTTL    time.Duration
	longTermTTL   time.Duration
	checkpointTTL time.Duration
	decomposeFn   func(context.Context, *ResearchQuest) (map[string]any, error)
	retrieveFn    func(context.Context, *ResearchQuest) ([]Source, map[string]any, error)
	hypothesisFn  func(context.Context, *ResearchQuest, []Source) ([]Hypothesis, error)
	branchFn      func(context.Context, *ResearchQuest, []Source, []Hypothesis) (branchReasoningOutcome, error)
	citationFn    func(context.Context, *ResearchQuest, []Source) ([]CitationAuthorityRecord, CitationVerdict, map[string]any, error)
	draftFn       func(context.Context, *ResearchQuest, []Source, map[string]any) (string, error)
	critiqueFn    func(context.Context, *ResearchQuest, []Source, CitationVerdict) (string, error)
	dossierFn     func(context.Context, *ResearchQuest, []Source) (map[string]*EvidenceDossier, error)
}
