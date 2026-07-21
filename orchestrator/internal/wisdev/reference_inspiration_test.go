package wisdev

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReferenceResearchInspirationPlanCoversBacklogSignals(t *testing.T) {
	plan := BuildReferenceResearchInspirationPlan(ResearchQuestRequest{
		UserID:            "user-1",
		Query:             "Evaluate a sparse retrieval improvement",
		Domain:            "cs",
		KickoffMode:       ReferenceKickoffModeReferenceBasedIdeation,
		ReferencePaperIDs: []string{"paper-a", "paper-b", "paper-a"},
	})

	require.NotNil(t, plan)
	assert.Equal(t, ReferenceKickoffModeReferenceBasedIdeation, plan.KickoffMode)
	assert.Equal(t, []string{"paper-a", "paper-b"}, plan.ReferencePaperIDs)
	assert.Contains(t, plan.ExperimentTemplate.RequiredFiles, "experiment.py")
	assert.Contains(t, plan.ExperimentTemplate.RequiredFiles, "latex/template.tex")
	assert.ElementsMatch(t, RequiredReferenceTemplateFiles(), plan.ExperimentTemplate.RequiredFiles)
	assert.Equal(t, "baseline", plan.BaselineRunArtifact.Kind)
	assert.Equal(t, "candidate", plan.CandidateRunArtifact.Kind)
	assert.NotEmpty(t, plan.DoctorPreflight.Checks)
	assert.False(t, plan.DoctorPreflight.Ready)
	assert.Contains(t, plan.DoctorPreflight.BlockingIssues, "preflight_not_run")
	checkIDs := make([]string, 0, len(plan.DoctorPreflight.Checks))
	for _, check := range plan.DoctorPreflight.Checks {
		checkIDs = append(checkIDs, check.ID)
	}
	assert.ElementsMatch(t, []string{"repo_cleanliness", "required_clis", "model_credentials", "ports", "sandbox_policy"}, checkIDs)
	assert.NotEmpty(t, plan.InnovationGraph.Nodes)
	assert.NotEmpty(t, plan.InnovationGraph.Edges)
	assert.Len(t, plan.ReplaySignals, 19)
	assert.Empty(t, plan.ValidationIssues)
}

func TestValidateReferenceResearchInspirationPlanRequiresEverySignal(t *testing.T) {
	plan := BuildReferenceResearchInspirationPlan(ResearchQuestRequest{
		UserID: "user-2",
		Query:  "test missing signals",
	})
	plan.ReplaySignals = []string{"experiment_template"}

	issues := ValidateReferenceResearchInspirationPlan(plan)

	assert.Contains(t, issues, "missing_replay_signal:baseline_run_artifact")
	assert.Contains(t, issues, "missing_replay_signal:innovation_graph_trace")
}

func TestValidateReferenceResearchInspirationPlanRejectsIncompleteBacklogArtifacts(t *testing.T) {
	plan := BuildReferenceResearchInspirationPlan(ResearchQuestRequest{
		UserID: "user-2b",
		Query:  "validate stronger artifacts",
	})
	plan.ExperimentTemplate.RequiredFiles = []string{"experiment.py"}
	plan.BaselineRunArtifact.Runner = ""
	plan.MetricDeltas = nil
	plan.DoctorPreflight.Checks = []ResearchDoctorCheck{{
		ID:       "ports",
		Area:     "runtime",
		Status:   "pending",
		Summary:  "ports only is incomplete",
		Required: true,
	}}

	issues := ValidateReferenceResearchInspirationPlan(plan)

	assert.Contains(t, issues, "experiment_template_required_file_missing:plot.py")
	assert.Contains(t, issues, "experiment_template_required_file_missing:prompt.json")
	assert.Contains(t, issues, "baseline_run_runner_missing")
	assert.Contains(t, issues, "metric_deltas_missing")
	assert.Contains(t, issues, "doctor_preflight_check_missing:model_credentials")
}

func TestNormalizeReferenceResearchInspirationPlanCompletesPartialOverride(t *testing.T) {
	plan := NormalizeReferenceResearchInspirationPlan(ResearchQuestRequest{
		UserID:            "user-2c",
		Query:             "reference kickoff",
		Domain:            "cs",
		KickoffMode:       "reference",
		ReferencePaperIDs: []string{"seed-paper-1", "seed-paper-1"},
		ReferenceInspiration: &ReferenceResearchInspirationPlan{
			ExperimentTemplate: ResearchExperimentTemplate{Name: "custom template name"},
			ReplaySignals:      []string{"experiment_template"},
		},
	})

	require.NotNil(t, plan)
	assert.Equal(t, ReferenceKickoffModeReferenceBasedIdeation, plan.KickoffMode)
	assert.Equal(t, []string{"seed-paper-1"}, plan.ReferencePaperIDs)
	assert.Equal(t, "custom template name", plan.ExperimentTemplate.Name)
	assert.NotEmpty(t, plan.ExperimentTemplate.ID)
	assert.ElementsMatch(t, RequiredReferenceTemplateFiles(), plan.ExperimentTemplate.RequiredFiles)
	assert.NotEmpty(t, plan.BaselineRunArtifact.ID)
	assert.NotEmpty(t, plan.CandidateRunArtifact.ID)
	assert.NotEmpty(t, plan.MetricDeltas)
	assert.NotEmpty(t, plan.DoctorPreflight.Checks)
	assert.NotEmpty(t, plan.InnovationGraph.Nodes)
	assert.Len(t, plan.ReplaySignals, 19)
	assert.Empty(t, plan.ValidationIssues)
}

func TestRunReferenceDoctorPreflightExecutesChecksAndKeepsExecutionBlocked(t *testing.T) {
	plan := BuildReferenceResearchInspirationPlan(ResearchQuestRequest{
		UserID: "user-preflight",
		Query:  "preflight execution gate",
		Domain: "cs",
	})

	RunReferenceDoctorPreflight(context.Background(), plan, ResearchDoctorPreflightOptions{
		GitStatus: func(context.Context, string) (string, error) {
			return "", nil
		},
		LookPath: func(tool string) (string, error) {
			return "C:/tools/" + tool, nil
		},
		CredentialCheck: func(context.Context) error {
			return nil
		},
		PortCheck: func(context.Context, string) error {
			return nil
		},
		PortTargets: []string{"127.0.0.1:8081"},
	})

	require.NotNil(t, plan)
	assert.True(t, plan.DoctorPreflight.Ready)
	assert.Empty(t, plan.DoctorPreflight.BlockingIssues)
	for _, check := range plan.DoctorPreflight.Checks {
		assert.Equal(t, "passed", check.Status)
	}
	assert.False(t, plan.ExperimentExecutionGate.Ready)
	assert.Equal(t, "blocked", plan.ExperimentExecutionGate.Status)
	assert.Contains(t, plan.ExperimentExecutionGate.BlockingIssues, "code_execution_disabled")
	assert.Contains(t, plan.ValidationIssues, "experiment_execution_gate_blocked:code_execution_disabled")
}

func TestRunReferenceDoctorPreflightReportsBlockingFailures(t *testing.T) {
	plan := BuildReferenceResearchInspirationPlan(ResearchQuestRequest{
		UserID: "user-preflight-fail",
		Query:  "preflight failures",
	})

	RunReferenceDoctorPreflight(context.Background(), plan, ResearchDoctorPreflightOptions{
		GitStatus: func(context.Context, string) (string, error) {
			return " M orchestrator/internal/wisdev/reference_inspiration.go", nil
		},
		LookPath: func(tool string) (string, error) {
			if tool == "python" {
				return "", errors.New("python missing")
			}
			return "C:/tools/" + tool, nil
		},
		CredentialCheck: func(context.Context) error {
			return errors.New("credentials unavailable")
		},
		PortCheck: func(context.Context, string) error {
			return errors.New("port busy")
		},
		PortTargets: []string{"127.0.0.1:8081"},
	})

	assert.False(t, plan.DoctorPreflight.Ready)
	assert.Contains(t, plan.DoctorPreflight.BlockingIssues, "repo_dirty")
	assert.Contains(t, plan.DoctorPreflight.BlockingIssues, "required_clis_missing:python")
	assert.Contains(t, plan.DoctorPreflight.BlockingIssues, "model_credentials_unavailable")
	assert.Contains(t, plan.DoctorPreflight.BlockingIssues, "ports_unavailable:127.0.0.1:8081")
	assert.False(t, plan.ExperimentExecutionGate.Ready)
	assert.Contains(t, plan.ExperimentExecutionGate.BlockingIssues, "doctor_preflight_not_ready")
}

func TestResearchQuestRuntimeAttachesReferenceInspirationArtifacts(t *testing.T) {
	runtime := newResearchQuestRuntimeForTest(t, stubQuestHooks(testQuestSources(2), CitationVerdict{
		Status:        "promoted",
		Promoted:      true,
		VerifiedCount: 2,
	}))

	quest, err := runtime.StartQuest(context.Background(), ResearchQuestRequest{
		UserID:            "user-3",
		Query:             "reference-only ideation from seed papers",
		Domain:            "cs",
		KickoffMode:       ReferenceKickoffModeReferenceBasedIdeation,
		ReferencePaperIDs: []string{"seed-paper-1"},
	})
	require.NoError(t, err)
	require.NotNil(t, quest.ReferenceInspiration)

	assert.Equal(t, ReferenceKickoffModeReferenceBasedIdeation, quest.ReferenceInspiration.KickoffMode)
	assert.Len(t, quest.ReferenceInspiration.ReplaySignals, 19)
	assert.Empty(t, quest.ReferenceInspiration.ValidationIssues)
	assert.Contains(t, quest.Artifacts, referenceInspirationArtifactKey)
	assert.Contains(t, quest.Artifacts, referenceReplaySignalsKey)

	loaded, err := runtime.LoadQuest(context.Background(), quest.QuestID)
	require.NoError(t, err)
	require.NotNil(t, loaded.ReferenceInspiration)
	assert.Equal(t, ReferenceKickoffModeReferenceBasedIdeation, loaded.ReferenceInspiration.KickoffMode)
	assert.Len(t, loaded.ReferenceInspiration.ReplaySignals, 19)
}

func TestResearchQuestRuntimePersistsReferenceInspirationToResearchMemory(t *testing.T) {
	runtime := newResearchQuestRuntimeForTest(t, stubQuestHooks(testQuestSources(2), CitationVerdict{
		Status:        "promoted",
		Promoted:      true,
		VerifiedCount: 2,
	}))
	runtime.gateway.ResearchMemory = NewResearchMemoryCompiler(runtime.gateway.StateStore, runtime.gateway.Journal)
	customPlan := BuildReferenceResearchInspirationPlan(ResearchQuestRequest{
		UserID: "user-5",
		Query:  "baseline lineage persistence",
		Domain: "cs",
	})
	customPlan.FailedPathMemory = []FailedPathMemoryEntry{{
		ID:          "failed-path-1",
		Kind:        "no_result_retrieval",
		Stage:       "retrieve",
		Summary:     "first retrieval branch returned no usable papers",
		Lesson:      "capture blocked branches for replay",
		Recoverable: true,
		CreatedAt:   123,
	}}

	quest, err := runtime.StartQuest(context.Background(), ResearchQuestRequest{
		UserID:               "user-5",
		Query:                "baseline lineage persistence",
		Domain:               "cs",
		KickoffMode:          ReferenceKickoffModeDetailedIdea,
		ReferenceInspiration: customPlan,
	})
	require.NoError(t, err)
	require.NotNil(t, quest.ReferenceInspiration)

	state, err := runtime.gateway.StateStore.LoadResearchMemoryState("user-5", "")
	require.NoError(t, err)
	require.NotNil(t, state)
	require.NotEmpty(t, state.Episodes)

	metadata := state.Episodes[len(state.Episodes)-1].Metadata
	require.NotNil(t, metadata)
	assert.Contains(t, metadata, "referenceResearchInspiration")
	assert.Contains(t, metadata, "baselineRunArtifact")
	assert.Contains(t, metadata, "candidateRunArtifact")
	assert.Contains(t, metadata, "metricDeltas")
	assert.Contains(t, metadata, "innovationGraph")
	assert.Contains(t, metadata, "reviewImprovementDelta")
	assert.Contains(t, metadata, "referenceSignals")
	failedPathMemory, ok := metadata["failedPathMemory"].([]any)
	require.True(t, ok)
	assert.NotEmpty(t, failedPathMemory)
}

func TestAppendReferenceFailedPathMemoryUpdatesArtifact(t *testing.T) {
	quest := &ResearchQuest{
		QuestID:   "quest-1",
		UserID:    "user-4",
		Query:     "failed path memory",
		Domain:    "cs",
		Artifacts: map[string]any{},
	}

	AppendReferenceFailedPathMemory(quest, FailedPathMemoryEntry{
		Kind:        "blocked_setup",
		Stage:       "doctor_preflight",
		Summary:     "missing local runner",
		Lesson:      "run preflight before YOLO",
		Recoverable: true,
	})

	require.NotNil(t, quest.ReferenceInspiration)
	require.Len(t, quest.ReferenceInspiration.FailedPathMemory, 1)
	assert.Equal(t, "blocked_setup", quest.ReferenceInspiration.FailedPathMemory[0].Kind)
	assert.Contains(t, quest.Artifacts, referenceInspirationArtifactKey)
}
