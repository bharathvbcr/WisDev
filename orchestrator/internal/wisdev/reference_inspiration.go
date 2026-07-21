package wisdev

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

const (
	ReferenceKickoffModeDetailedIdea           = "detailed_idea"
	ReferenceKickoffModeReferenceBasedIdeation = "reference_based_ideation"

	referenceInspirationArtifactKey = "referenceResearchInspiration"
	referenceReplaySignalsKey       = "referenceSignals"
)

var requiredReferenceReplaySignals = []string{
	"agent_surface_separation",
	"algorithm_validation",
	"automated_peer_review",
	"baseline_run_artifact",
	"benchmark_construction_trace",
	"detailed_idea_kickoff",
	"doctor_preflight",
	"experiment_rounds",
	"experiment_template",
	"findings_memory",
	"human_takeover_checkpoint",
	"idea_execution_log",
	"innovation_graph_trace",
	"manuscript_creation",
	"novelty_review",
	"reference_based_ideation",
	"research_map_lineage",
	"review_improvement_delta",
	"workspace_runner_profile",
}

var requiredReferenceTemplateFiles = []string{
	"experiment.py",
	"plot.py",
	"prompt.json",
	"seed_ideas.json",
	"latex/template.tex",
}

var requiredReferenceDoctorCheckIDs = []string{
	"repo_cleanliness",
	"required_clis",
	"model_credentials",
	"ports",
	"sandbox_policy",
}

type ResearchMetricExtractor struct {
	ID             string `json:"id"`
	Metric         string `json:"metric"`
	SourceFile     string `json:"sourceFile"`
	JSONPath       string `json:"jsonPath,omitempty"`
	HigherIsBetter bool   `json:"higherIsBetter"`
}

type ResearchSandboxPolicy struct {
	CodeExecutionAllowed bool     `json:"codeExecutionAllowed"`
	NetworkPolicy        string   `json:"networkPolicy"`
	MaxRuntimeSeconds    int      `json:"maxRuntimeSeconds"`
	RequiredApprovals    []string `json:"requiredApprovals,omitempty"`
}

type ResearchExperimentTemplate struct {
	ID               string                    `json:"id"`
	Name             string                    `json:"name"`
	RequiredFiles    []string                  `json:"requiredFiles"`
	AllowedFiles     []string                  `json:"allowedFiles,omitempty"`
	BaselineData     []string                  `json:"baselineData,omitempty"`
	MetricExtractors []ResearchMetricExtractor `json:"metricExtractors"`
	SandboxPolicy    ResearchSandboxPolicy     `json:"sandboxPolicy"`
}

type ResearchRunArtifact struct {
	ID              string             `json:"id"`
	Kind            string             `json:"kind"`
	Status          string             `json:"status"`
	Runner          string             `json:"runner"`
	HardwareProfile string             `json:"hardwareProfile,omitempty"`
	ArtifactURI     string             `json:"artifactUri,omitempty"`
	Logs            []string           `json:"logs,omitempty"`
	Metrics         map[string]float64 `json:"metrics,omitempty"`
	CreatedAt       int64              `json:"createdAt"`
}

type ResearchMetricDelta struct {
	Metric         string  `json:"metric"`
	BaselineValue  float64 `json:"baselineValue"`
	CandidateValue float64 `json:"candidateValue"`
	Delta          float64 `json:"delta"`
	DeltaRatio     float64 `json:"deltaRatio,omitempty"`
	Direction      string  `json:"direction"`
}

type ResearchDoctorCheck struct {
	ID       string `json:"id"`
	Area     string `json:"area"`
	Status   string `json:"status"`
	Summary  string `json:"summary"`
	Required bool   `json:"required"`
}

type ResearchDoctorPreflight struct {
	Ready          bool                  `json:"ready"`
	ExpectedRunner string                `json:"expectedRunner"`
	Checks         []ResearchDoctorCheck `json:"checks"`
	BlockingIssues []string              `json:"blockingIssues,omitempty"`
	CheckedAt      int64                 `json:"checkedAt,omitempty"`
}

type ResearchExperimentExecutionGate struct {
	Ready             bool     `json:"ready"`
	Status            string   `json:"status"`
	Reason            string   `json:"reason,omitempty"`
	RequiredApprovals []string `json:"requiredApprovals,omitempty"`
	BlockingIssues    []string `json:"blockingIssues,omitempty"`
	CheckedAt         int64    `json:"checkedAt,omitempty"`
}

type ResearchDoctorPreflightOptions struct {
	RepoRoot               string
	ExpectedRunner         string
	RequiredCLIs           []string
	PortTargets            []string
	SandboxApprovalGranted bool
	GitStatus              func(context.Context, string) (string, error)
	LookPath               func(string) (string, error)
	CredentialCheck        func(context.Context) error
	PortCheck              func(context.Context, string) error
}

type FailedPathMemoryEntry struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Stage       string   `json:"stage"`
	Summary     string   `json:"summary"`
	Lesson      string   `json:"lesson,omitempty"`
	Evidence    []string `json:"evidence,omitempty"`
	Recoverable bool     `json:"recoverable"`
	CreatedAt   int64    `json:"createdAt"`
}

type InnovationGraphNode struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Label    string         `json:"label"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type InnovationGraphEdge struct {
	FromID   string `json:"fromId"`
	ToID     string `json:"toId"`
	Relation string `json:"relation"`
}

type ResearchInnovationGraph struct {
	Nodes []InnovationGraphNode `json:"nodes"`
	Edges []InnovationGraphEdge `json:"edges"`
}

type ReviewImprovementDelta struct {
	ReviewerScore        float64  `json:"reviewerScore"`
	ImprovedDraftScore   float64  `json:"improvedDraftScore"`
	Delta                float64  `json:"delta"`
	Weaknesses           []string `json:"weaknesses,omitempty"`
	ResolvedWeaknesses   []string `json:"resolvedWeaknesses,omitempty"`
	UnresolvedWeaknesses []string `json:"unresolvedWeaknesses,omitempty"`
	ReadyForFinalization bool     `json:"readyForFinalization"`
}

type ReferenceResearchInspirationPlan struct {
	SourceBacklog           string                          `json:"sourceBacklog"`
	KickoffMode             string                          `json:"kickoffMode"`
	DetailedIdea            string                          `json:"detailedIdea,omitempty"`
	ReferencePaperIDs       []string                        `json:"referencePaperIds,omitempty"`
	ExperimentTemplate      ResearchExperimentTemplate      `json:"experimentTemplate"`
	BaselineRunArtifact     ResearchRunArtifact             `json:"baselineRunArtifact"`
	CandidateRunArtifact    ResearchRunArtifact             `json:"candidateRunArtifact"`
	MetricDeltas            []ResearchMetricDelta           `json:"metricDeltas,omitempty"`
	DoctorPreflight         ResearchDoctorPreflight         `json:"doctorPreflight"`
	ExperimentExecutionGate ResearchExperimentExecutionGate `json:"experimentExecutionGate"`
	FailedPathMemory        []FailedPathMemoryEntry         `json:"failedPathMemory,omitempty"`
	InnovationGraph         ResearchInnovationGraph         `json:"innovationGraph"`
	ReviewImprovementDelta  ReviewImprovementDelta          `json:"reviewImprovementDelta"`
	ReplaySignals           []string                        `json:"replaySignals"`
	ValidationIssues        []string                        `json:"validationIssues,omitempty"`
}

func RequiredReferenceReplaySignals() []string {
	return append([]string(nil), requiredReferenceReplaySignals...)
}

func NormalizeReferenceKickoffMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case ReferenceKickoffModeReferenceBasedIdeation, "reference", "reference_only", "paper_only":
		return ReferenceKickoffModeReferenceBasedIdeation
	case ReferenceKickoffModeDetailedIdea, "detailed", "idea", "idea_provided":
		return ReferenceKickoffModeDetailedIdea
	default:
		return ReferenceKickoffModeDetailedIdea
	}
}

func BuildReferenceResearchInspirationPlan(req ResearchQuestRequest) *ReferenceResearchInspirationPlan {
	query := strings.TrimSpace(req.Query)
	detailedIdea := firstNonEmpty(strings.TrimSpace(req.DetailedIdea), query)
	mode := NormalizeReferenceKickoffMode(req.KickoffMode)
	referencePaperIDs := dedupeTrimmedStrings(req.ReferencePaperIDs)
	if mode == ReferenceKickoffModeDetailedIdea && query == "" && len(referencePaperIDs) > 0 {
		mode = ReferenceKickoffModeReferenceBasedIdeation
	}
	now := NowMillis()
	templateID := stableWisDevID("experiment-template", req.UserID, query, mode)
	template := ResearchExperimentTemplate{
		ID:            templateID,
		Name:          firstNonEmpty(strings.TrimSpace(req.Domain), "general") + " computational quest template",
		RequiredFiles: RequiredReferenceTemplateFiles(),
		AllowedFiles: []string{
			"README.md",
			"requirements.txt",
			"results/metrics.json",
			"results/figures/*.png",
		},
		BaselineData: []string{"baseline/run_0/metrics.json"},
		MetricExtractors: []ResearchMetricExtractor{{
			ID:             "primary_quality",
			Metric:         "quality",
			SourceFile:     "results/metrics.json",
			JSONPath:       "$.quality",
			HigherIsBetter: true,
		}},
		SandboxPolicy: ResearchSandboxPolicy{
			CodeExecutionAllowed: false,
			NetworkPolicy:        "blocked_by_default",
			MaxRuntimeSeconds:    0,
			RequiredApprovals:    []string{"human_or_yolo_preflight"},
		},
	}
	plan := &ReferenceResearchInspirationPlan{
		SourceBacklog:      "plans/wisdev_reference_repo_inspiration_backlog.md",
		KickoffMode:        mode,
		DetailedIdea:       detailedIdea,
		ReferencePaperIDs:  referencePaperIDs,
		ExperimentTemplate: template,
		BaselineRunArtifact: ResearchRunArtifact{
			ID:        stableWisDevID("baseline-run", templateID, "run_0"),
			Kind:      "baseline",
			Status:    "pending",
			Runner:    "wisdev-local",
			CreatedAt: now,
		},
		CandidateRunArtifact: ResearchRunArtifact{
			ID:        stableWisDevID("candidate-run", templateID, "candidate"),
			Kind:      "candidate",
			Status:    "pending",
			Runner:    "wisdev-local",
			CreatedAt: now,
		},
		MetricDeltas: []ResearchMetricDelta{{
			Metric:    "quality",
			Direction: "higher_is_better",
		}},
		DoctorPreflight: ResearchDoctorPreflight{
			Ready:          false,
			ExpectedRunner: "wisdev-local",
			Checks: []ResearchDoctorCheck{
				{ID: "repo_cleanliness", Area: "workspace", Status: "pending", Summary: "Check dirty files and generated artifacts before YOLO execution.", Required: true},
				{ID: "required_clis", Area: "tools", Status: "pending", Summary: "Verify Go, Node, Python, and benchmark commands are available.", Required: true},
				{ID: "model_credentials", Area: "secrets", Status: "pending", Summary: "Verify server-side model credentials without logging values.", Required: true},
				{ID: "ports", Area: "runtime", Status: "pending", Summary: "Verify required local ports are free or correctly assigned.", Required: true},
				{ID: "sandbox_policy", Area: "security", Status: "pending", Summary: "Confirm experiment code execution policy before running generated code.", Required: true},
			},
			BlockingIssues: []string{"preflight_not_run"},
		},
		ExperimentExecutionGate: ResearchExperimentExecutionGate{
			Ready:             false,
			Status:            "pending",
			Reason:            "doctor_preflight_not_run",
			RequiredApprovals: append([]string(nil), template.SandboxPolicy.RequiredApprovals...),
			BlockingIssues:    []string{"doctor_preflight_not_run"},
		},
		InnovationGraph: buildDefaultInnovationGraph(query, referencePaperIDs, templateID),
		ReviewImprovementDelta: ReviewImprovementDelta{
			ReadyForFinalization: false,
		},
		ReplaySignals: RequiredReferenceReplaySignals(),
	}
	plan.ValidationIssues = ValidateReferenceResearchInspirationPlan(plan)
	return plan
}

func ValidateReferenceResearchInspirationPlan(plan *ReferenceResearchInspirationPlan) []string {
	if plan == nil {
		return []string{"reference_inspiration_plan_missing"}
	}
	issues := []string{}
	if NormalizeReferenceKickoffMode(plan.KickoffMode) != plan.KickoffMode {
		issues = append(issues, "kickoff_mode_invalid")
	}
	switch plan.KickoffMode {
	case ReferenceKickoffModeDetailedIdea:
		if strings.TrimSpace(plan.DetailedIdea) == "" {
			issues = append(issues, "detailed_idea_missing")
		}
	case ReferenceKickoffModeReferenceBasedIdeation:
		if len(dedupeTrimmedStrings(plan.ReferencePaperIDs)) == 0 {
			issues = append(issues, "reference_paper_ids_missing")
		}
	}
	if len(plan.ExperimentTemplate.RequiredFiles) == 0 {
		issues = append(issues, "experiment_template_required_files_missing")
	}
	for _, missingFile := range missingRequiredStrings(plan.ExperimentTemplate.RequiredFiles, requiredReferenceTemplateFiles) {
		issues = append(issues, "experiment_template_required_file_missing:"+missingFile)
	}
	if len(plan.ExperimentTemplate.MetricExtractors) == 0 {
		issues = append(issues, "experiment_template_metric_extractors_missing")
	}
	if strings.TrimSpace(plan.ExperimentTemplate.SandboxPolicy.NetworkPolicy) == "" {
		issues = append(issues, "experiment_template_sandbox_policy_missing")
	}
	if strings.TrimSpace(plan.BaselineRunArtifact.ID) == "" || plan.BaselineRunArtifact.Kind != "baseline" {
		issues = append(issues, "baseline_run_artifact_missing")
	}
	if strings.TrimSpace(plan.BaselineRunArtifact.Runner) == "" {
		issues = append(issues, "baseline_run_runner_missing")
	}
	if strings.TrimSpace(plan.CandidateRunArtifact.ID) == "" || plan.CandidateRunArtifact.Kind != "candidate" {
		issues = append(issues, "candidate_run_artifact_missing")
	}
	if strings.TrimSpace(plan.CandidateRunArtifact.Runner) == "" {
		issues = append(issues, "candidate_run_runner_missing")
	}
	if len(plan.MetricDeltas) == 0 {
		issues = append(issues, "metric_deltas_missing")
	}
	if len(plan.DoctorPreflight.Checks) == 0 {
		issues = append(issues, "doctor_preflight_checks_missing")
	}
	for _, missingCheckID := range missingRequiredDoctorChecks(plan.DoctorPreflight.Checks) {
		issues = append(issues, "doctor_preflight_check_missing:"+missingCheckID)
	}
	if plan.ExperimentExecutionGate.CheckedAt > 0 && !plan.ExperimentExecutionGate.Ready {
		for _, blockingIssue := range plan.ExperimentExecutionGate.BlockingIssues {
			issues = append(issues, "experiment_execution_gate_blocked:"+blockingIssue)
		}
	}
	if len(plan.InnovationGraph.Nodes) == 0 || len(plan.InnovationGraph.Edges) == 0 {
		issues = append(issues, "innovation_graph_missing")
	}
	missingSignals := missingReferenceSignals(plan.ReplaySignals)
	for _, signal := range missingSignals {
		issues = append(issues, "missing_replay_signal:"+signal)
	}
	return dedupeTrimmedStrings(issues)
}

func NormalizeReferenceResearchInspirationPlan(req ResearchQuestRequest) *ReferenceResearchInspirationPlan {
	base := BuildReferenceResearchInspirationPlan(req)
	if req.ReferenceInspiration == nil {
		return base
	}
	plan := *req.ReferenceInspiration
	if strings.TrimSpace(plan.SourceBacklog) == "" {
		plan.SourceBacklog = base.SourceBacklog
	}
	plan.KickoffMode = NormalizeReferenceKickoffMode(firstNonEmpty(plan.KickoffMode, req.KickoffMode, base.KickoffMode))
	plan.DetailedIdea = firstNonEmpty(strings.TrimSpace(plan.DetailedIdea), strings.TrimSpace(req.DetailedIdea), strings.TrimSpace(req.Query), base.DetailedIdea)
	if len(plan.ReferencePaperIDs) == 0 {
		plan.ReferencePaperIDs = base.ReferencePaperIDs
	} else {
		plan.ReferencePaperIDs = dedupeTrimmedStrings(plan.ReferencePaperIDs)
	}
	if strings.TrimSpace(plan.ExperimentTemplate.ID) == "" {
		plan.ExperimentTemplate.ID = base.ExperimentTemplate.ID
	}
	if strings.TrimSpace(plan.ExperimentTemplate.Name) == "" {
		plan.ExperimentTemplate.Name = base.ExperimentTemplate.Name
	}
	if len(plan.ExperimentTemplate.RequiredFiles) == 0 {
		plan.ExperimentTemplate.RequiredFiles = append([]string(nil), base.ExperimentTemplate.RequiredFiles...)
	}
	if len(plan.ExperimentTemplate.AllowedFiles) == 0 {
		plan.ExperimentTemplate.AllowedFiles = append([]string(nil), base.ExperimentTemplate.AllowedFiles...)
	}
	if len(plan.ExperimentTemplate.BaselineData) == 0 {
		plan.ExperimentTemplate.BaselineData = append([]string(nil), base.ExperimentTemplate.BaselineData...)
	}
	if len(plan.ExperimentTemplate.MetricExtractors) == 0 {
		plan.ExperimentTemplate.MetricExtractors = append([]ResearchMetricExtractor(nil), base.ExperimentTemplate.MetricExtractors...)
	}
	if isZeroResearchSandboxPolicy(plan.ExperimentTemplate.SandboxPolicy) {
		plan.ExperimentTemplate.SandboxPolicy = base.ExperimentTemplate.SandboxPolicy
	}
	if strings.TrimSpace(plan.BaselineRunArtifact.ID) == "" {
		plan.BaselineRunArtifact = base.BaselineRunArtifact
	}
	if strings.TrimSpace(plan.CandidateRunArtifact.ID) == "" {
		plan.CandidateRunArtifact = base.CandidateRunArtifact
	}
	if len(plan.MetricDeltas) == 0 {
		plan.MetricDeltas = append([]ResearchMetricDelta(nil), base.MetricDeltas...)
	}
	if len(plan.DoctorPreflight.Checks) == 0 {
		plan.DoctorPreflight = base.DoctorPreflight
	}
	if strings.TrimSpace(plan.ExperimentExecutionGate.Status) == "" {
		plan.ExperimentExecutionGate = base.ExperimentExecutionGate
	}
	if len(plan.InnovationGraph.Nodes) == 0 && len(plan.InnovationGraph.Edges) == 0 {
		plan.InnovationGraph = base.InnovationGraph
	}
	plan.ReplaySignals = completeReferenceReplaySignals(plan.ReplaySignals)
	plan.ValidationIssues = ValidateReferenceResearchInspirationPlan(&plan)
	return &plan
}

func SyncReferenceResearchInspirationArtifact(quest *ResearchQuest) {
	if quest == nil {
		return
	}
	if quest.ReferenceInspiration == nil {
		quest.ReferenceInspiration = BuildReferenceResearchInspirationPlan(ResearchQuestRequest{
			UserID:            quest.UserID,
			Query:             quest.Query,
			Domain:            quest.Domain,
			KickoffMode:       ReferenceKickoffModeDetailedIdea,
			ReferencePaperIDs: nil,
		})
	}
	quest.ReferenceInspiration.ReplaySignals = completeReferenceReplaySignals(quest.ReferenceInspiration.ReplaySignals)
	quest.ReferenceInspiration.ValidationIssues = ValidateReferenceResearchInspirationPlan(quest.ReferenceInspiration)
	if quest.Artifacts == nil {
		quest.Artifacts = map[string]any{}
	}
	quest.Artifacts[referenceInspirationArtifactKey] = quest.ReferenceInspiration
	quest.Artifacts[referenceReplaySignalsKey] = append([]string(nil), quest.ReferenceInspiration.ReplaySignals...)
}

func AppendReferenceFailedPathMemory(quest *ResearchQuest, entry FailedPathMemoryEntry) {
	if quest == nil {
		return
	}
	SyncReferenceResearchInspirationArtifact(quest)
	if strings.TrimSpace(entry.ID) == "" {
		entry.ID = stableWisDevID("failed-path", quest.QuestID, entry.Kind, entry.Stage, entry.Summary)
	}
	if entry.CreatedAt == 0 {
		entry.CreatedAt = NowMillis()
	}
	quest.ReferenceInspiration.FailedPathMemory = append(quest.ReferenceInspiration.FailedPathMemory, entry)
	quest.ReferenceInspiration.ReplaySignals = completeReferenceReplaySignals(quest.ReferenceInspiration.ReplaySignals)
	SyncReferenceResearchInspirationArtifact(quest)
}

func RunReferenceDoctorPreflight(ctx context.Context, plan *ReferenceResearchInspirationPlan, opts ResearchDoctorPreflightOptions) {
	if plan == nil {
		return
	}
	now := NowMillis()
	expectedRunner := firstNonEmpty(strings.TrimSpace(opts.ExpectedRunner), plan.DoctorPreflight.ExpectedRunner, "wisdev-local")
	checks := []ResearchDoctorCheck{}
	blocking := []string{}

	repoCheck, repoIssues := runReferenceRepoCleanlinessCheck(ctx, opts)
	checks = append(checks, repoCheck)
	blocking = append(blocking, repoIssues...)

	cliCheck, cliIssues := runReferenceRequiredCLIsCheck(opts)
	checks = append(checks, cliCheck)
	blocking = append(blocking, cliIssues...)

	credentialCheck, credentialIssues := runReferenceCredentialCheck(ctx, opts)
	checks = append(checks, credentialCheck)
	blocking = append(blocking, credentialIssues...)

	portCheck, portIssues := runReferencePortsCheck(ctx, opts)
	checks = append(checks, portCheck)
	blocking = append(blocking, portIssues...)

	sandboxCheck, sandboxIssues := runReferenceSandboxPolicyCheck(plan)
	checks = append(checks, sandboxCheck)
	blocking = append(blocking, sandboxIssues...)

	plan.DoctorPreflight = ResearchDoctorPreflight{
		Ready:          len(blocking) == 0,
		ExpectedRunner: expectedRunner,
		Checks:         checks,
		BlockingIssues: dedupeTrimmedStrings(blocking),
		CheckedAt:      now,
	}
	plan.ExperimentExecutionGate = EvaluateReferenceExperimentExecutionGate(plan, opts.SandboxApprovalGranted, now)
	plan.ValidationIssues = ValidateReferenceResearchInspirationPlan(plan)
}

func EvaluateReferenceExperimentExecutionGate(plan *ReferenceResearchInspirationPlan, sandboxApprovalGranted bool, checkedAt int64) ResearchExperimentExecutionGate {
	gate := ResearchExperimentExecutionGate{
		Ready:             false,
		Status:            "blocked",
		RequiredApprovals: nil,
		CheckedAt:         checkedAt,
	}
	if plan == nil {
		gate.Reason = "reference_inspiration_plan_missing"
		gate.BlockingIssues = []string{"reference_inspiration_plan_missing"}
		return gate
	}
	policy := plan.ExperimentTemplate.SandboxPolicy
	gate.RequiredApprovals = append([]string(nil), policy.RequiredApprovals...)
	blocking := []string{}
	if !plan.DoctorPreflight.Ready {
		blocking = append(blocking, "doctor_preflight_not_ready")
	}
	if !policy.CodeExecutionAllowed {
		blocking = append(blocking, "code_execution_disabled")
	}
	if len(policy.RequiredApprovals) > 0 && !sandboxApprovalGranted {
		blocking = append(blocking, "sandbox_approval_missing")
	}
	if len(blocking) == 0 {
		gate.Ready = true
		gate.Status = "ready"
		gate.Reason = "preflight_and_sandbox_ready"
		return gate
	}
	gate.BlockingIssues = dedupeTrimmedStrings(blocking)
	gate.Reason = gate.BlockingIssues[0]
	return gate
}

func completeReferenceReplaySignals(signals []string) []string {
	combined := dedupeTrimmedStrings(append(append([]string(nil), signals...), requiredReferenceReplaySignals...))
	sort.Strings(combined)
	return combined
}

func RequiredReferenceTemplateFiles() []string {
	return append([]string(nil), requiredReferenceTemplateFiles...)
}

func missingReferenceSignals(signals []string) []string {
	present := map[string]bool{}
	for _, signal := range signals {
		present[strings.TrimSpace(signal)] = true
	}
	missing := []string{}
	for _, signal := range requiredReferenceReplaySignals {
		if !present[signal] {
			missing = append(missing, signal)
		}
	}
	return missing
}

func missingRequiredStrings(values []string, required []string) []string {
	present := map[string]bool{}
	for _, value := range values {
		present[strings.TrimSpace(value)] = true
	}
	missing := []string{}
	for _, requiredValue := range required {
		if !present[requiredValue] {
			missing = append(missing, requiredValue)
		}
	}
	return missing
}

func missingRequiredDoctorChecks(checks []ResearchDoctorCheck) []string {
	present := map[string]bool{}
	for _, check := range checks {
		present[strings.TrimSpace(check.ID)] = true
	}
	return missingRequiredStringsFromSet(present, requiredReferenceDoctorCheckIDs)
}

func missingRequiredStringsFromSet(present map[string]bool, required []string) []string {
	missing := []string{}
	for _, requiredValue := range required {
		if !present[requiredValue] {
			missing = append(missing, requiredValue)
		}
	}
	return missing
}

func isZeroResearchSandboxPolicy(policy ResearchSandboxPolicy) bool {
	return !policy.CodeExecutionAllowed &&
		strings.TrimSpace(policy.NetworkPolicy) == "" &&
		policy.MaxRuntimeSeconds == 0 &&
		len(policy.RequiredApprovals) == 0
}

func runReferenceRepoCleanlinessCheck(ctx context.Context, opts ResearchDoctorPreflightOptions) (ResearchDoctorCheck, []string) {
	gitStatus := opts.GitStatus
	if gitStatus == nil {
		gitStatus = defaultReferenceGitStatus
	}
	status, err := gitStatus(ctx, strings.TrimSpace(opts.RepoRoot))
	if err != nil {
		return ResearchDoctorCheck{ID: "repo_cleanliness", Area: "workspace", Status: "failed", Summary: "Unable to inspect git workspace cleanliness.", Required: true}, []string{"repo_status_unavailable"}
	}
	if strings.TrimSpace(status) != "" {
		return ResearchDoctorCheck{ID: "repo_cleanliness", Area: "workspace", Status: "failed", Summary: "Workspace has dirty files; review generated artifacts before autonomous experiment execution.", Required: true}, []string{"repo_dirty"}
	}
	return ResearchDoctorCheck{ID: "repo_cleanliness", Area: "workspace", Status: "passed", Summary: "Workspace has no git status output.", Required: true}, nil
}

func runReferenceRequiredCLIsCheck(opts ResearchDoctorPreflightOptions) (ResearchDoctorCheck, []string) {
	lookPath := opts.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	required := opts.RequiredCLIs
	if len(required) == 0 {
		required = []string{"go", "node", "python"}
	}
	missing := []string{}
	for _, tool := range required {
		if _, err := lookPath(tool); err != nil {
			missing = append(missing, tool)
		}
	}
	if len(missing) > 0 {
		issues := make([]string, 0, len(missing))
		for _, tool := range missing {
			issues = append(issues, "required_clis_missing:"+tool)
		}
		return ResearchDoctorCheck{ID: "required_clis", Area: "tools", Status: "failed", Summary: "Missing required local CLIs: " + strings.Join(missing, ", "), Required: true}, issues
	}
	return ResearchDoctorCheck{ID: "required_clis", Area: "tools", Status: "passed", Summary: "Required local CLIs are available.", Required: true}, nil
}

func runReferenceCredentialCheck(ctx context.Context, opts ResearchDoctorPreflightOptions) (ResearchDoctorCheck, []string) {
	credentialCheck := opts.CredentialCheck
	if credentialCheck == nil {
		credentialCheck = defaultReferenceCredentialCheck
	}
	if err := credentialCheck(ctx); err != nil {
		return ResearchDoctorCheck{ID: "model_credentials", Area: "secrets", Status: "failed", Summary: "Server-side model credentials are not available for experiment measurement.", Required: true}, []string{"model_credentials_unavailable"}
	}
	return ResearchDoctorCheck{ID: "model_credentials", Area: "secrets", Status: "passed", Summary: "Server-side model credential source is available.", Required: true}, nil
}

func runReferencePortsCheck(ctx context.Context, opts ResearchDoctorPreflightOptions) (ResearchDoctorCheck, []string) {
	portCheck := opts.PortCheck
	if portCheck == nil {
		portCheck = defaultReferencePortCheck
	}
	targets := opts.PortTargets
	if len(targets) == 0 {
		return ResearchDoctorCheck{ID: "ports", Area: "runtime", Status: "passed", Summary: "No dedicated experiment-runner ports configured.", Required: true}, nil
	}
	blocked := []string{}
	for _, target := range targets {
		address := strings.TrimSpace(target)
		if address == "" {
			continue
		}
		if err := portCheck(ctx, address); err != nil {
			blocked = append(blocked, address)
		}
	}
	if len(blocked) > 0 {
		issues := make([]string, 0, len(blocked))
		for _, address := range blocked {
			issues = append(issues, "ports_unavailable:"+address)
		}
		return ResearchDoctorCheck{ID: "ports", Area: "runtime", Status: "failed", Summary: "Experiment runner ports unavailable: " + strings.Join(blocked, ", "), Required: true}, issues
	}
	return ResearchDoctorCheck{ID: "ports", Area: "runtime", Status: "passed", Summary: "Configured experiment-runner ports are available.", Required: true}, nil
}

func runReferenceSandboxPolicyCheck(plan *ReferenceResearchInspirationPlan) (ResearchDoctorCheck, []string) {
	if plan == nil || isZeroResearchSandboxPolicy(plan.ExperimentTemplate.SandboxPolicy) {
		return ResearchDoctorCheck{ID: "sandbox_policy", Area: "security", Status: "failed", Summary: "Experiment sandbox policy is missing.", Required: true}, []string{"sandbox_policy_missing"}
	}
	return ResearchDoctorCheck{ID: "sandbox_policy", Area: "security", Status: "passed", Summary: "Experiment sandbox policy is explicit; code execution remains controlled by the execution gate.", Required: true}, nil
}

func defaultReferenceGitStatus(ctx context.Context, repoRoot string) (string, error) {
	if strings.TrimSpace(repoRoot) == "" {
		repoRoot = "."
	}
	output, err := exec.CommandContext(ctx, "git", "-C", repoRoot, "status", "--porcelain").Output()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func defaultReferenceCredentialCheck(context.Context) error {
	for _, key := range []string{"GOOGLE_API_KEY", "GEMINI_API_KEY", "VERTEX_PROJECT_ID", "GOOGLE_CLOUD_PROJECT"} {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return nil
		}
	}
	return errors.New("model credential source unavailable")
}

func defaultReferencePortCheck(ctx context.Context, address string) error {
	dialer := net.Dialer{Timeout: 250 * time.Millisecond}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil
	}
	_ = conn.Close()
	return fmt.Errorf("port already accepts connections: %s", address)
}

func buildDefaultInnovationGraph(query string, referencePaperIDs []string, templateID string) ResearchInnovationGraph {
	queryLabel := firstNonEmpty(strings.TrimSpace(query), "reference-only research kickoff")
	nodes := []InnovationGraphNode{
		{ID: "seed-query", Type: "seed_query", Label: queryLabel},
		{ID: "experiment-template", Type: "experiment_template", Label: templateID},
		{ID: "hypothesis", Type: "generated_hypothesis", Label: "candidate hypothesis"},
		{ID: "manuscript-claim", Type: "manuscript_claim", Label: "claim awaiting evidence-grounded draft"},
	}
	for idx, paperID := range referencePaperIDs {
		nodes = append(nodes, InnovationGraphNode{
			ID:    fmt.Sprintf("reference-paper-%d", idx+1),
			Type:  "seed_paper",
			Label: paperID,
		})
	}
	edges := []InnovationGraphEdge{
		{FromID: "seed-query", ToID: "hypothesis", Relation: "generates"},
		{FromID: "hypothesis", ToID: "experiment-template", Relation: "requires_experiment"},
		{FromID: "experiment-template", ToID: "manuscript-claim", Relation: "supports_claim"},
	}
	for idx := range referencePaperIDs {
		edges = append(edges, InnovationGraphEdge{
			FromID:   fmt.Sprintf("reference-paper-%d", idx+1),
			ToID:     "hypothesis",
			Relation: "inspires",
		})
	}
	return ResearchInnovationGraph{Nodes: nodes, Edges: edges}
}
