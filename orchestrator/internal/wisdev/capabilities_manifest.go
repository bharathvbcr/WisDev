package wisdev

import (
	"sort"
	"strings"
)

type FrontendCapabilityDescriptor struct {
	ID          string    `json:"id"`
	Action      string    `json:"action"`
	Description string    `json:"description"`
	Risk        RiskLevel `json:"risk,omitempty"`
}

type DeepAgentsCapabilities struct {
	Backend                  string                          `json:"backend"`
	ArtifactSchema           string                          `json:"artifactSchema"`
	WisdevActions            []string                        `json:"wisdevActions"`
	SensitiveWisdevActions   []string                        `json:"sensitiveWisdevActions"`
	AllowlistedTools         []string                        `json:"allowlistedTools"`
	DefaultMaxExecutionMs    int                             `json:"defaultMaxExecutionMs"`
	ToolsEnabled             bool                            `json:"toolsEnabled"`
	ToolCount                int                             `json:"toolCount"`
	RequireHumanConfirmation bool                            `json:"requireHumanConfirmation"`
	PolicyByMode             map[string]DeepAgentsModePolicy `json:"policyByMode,omitempty"`
}

type DeepAgentsModePolicy struct {
	EnableWisdevTools        bool     `json:"enableWisdevTools"`
	AllowlistedTools         []string `json:"allowlistedTools"`
	RequireHumanConfirmation bool     `json:"requireHumanConfirmation"`
}

type DeepAgentsExecutionPolicy struct {
	EnableWisdevTools        bool     `json:"enableWisdevTools"`
	AllowlistedTools         []string `json:"allowlistedTools,omitempty"`
	RequireHumanConfirmation bool     `json:"requireHumanConfirmation"`
	Mode                     string   `json:"mode"`
	ResolutionSource         string   `json:"resolutionSource,omitempty"`
}

func BuildFrontendCapabilityManifest(registry *ToolRegistry) []FrontendCapabilityDescriptor {
	if registry == nil {
		registry = NewToolRegistry()
	}

	tools := registry.List()
	manifest := make([]FrontendCapabilityDescriptor, 0, len(tools))
	for _, tool := range tools {
		name := CanonicalizeWisdevAction(tool.Name)
		if name == "" {
			continue
		}
		manifest = append(manifest, FrontendCapabilityDescriptor{
			ID:          name,
			Action:      name,
			Description: tool.Description,
			Risk:        tool.Risk,
		})
	}

	sort.Slice(manifest, func(i, j int) bool {
		return manifest[i].ID < manifest[j].ID
	})

	return manifest
}

func BuildDeepAgentsCapabilities(registry *ToolRegistry) DeepAgentsCapabilities {
	if registry == nil {
		registry = NewToolRegistry()
	}

	available := make(map[string]struct{}, len(registry.List()))
	for _, tool := range registry.List() {
		available[CanonicalizeWisdevAction(tool.Name)] = struct{}{}
	}

	candidateActions := []string{
		ActionResearchQueryDecompose,
		ActionResearchGenerateThoughts,
		ActionResearchRetrievePapers,
		ActionResearchResolveCanonicalCitations,
		ActionResearchVerifyCitations,
		ActionResearchProposeHypotheses,
		ActionResearchGenerateHypotheses,
		ActionResearchVerifyReasoningPaths,
		ActionResearchVerifyClaimsBatch,
		ActionResearchBuildClaimEvidenceTable,
		ActionResearchEvaluateEvidence,
		ActionResearchSynthesizeAnswer,
	}

	actions := make([]string, 0, len(candidateActions))
	for _, action := range candidateActions {
		if _, ok := available[action]; ok {
			actions = append(actions, action)
		}
	}

	sensitive := []string{ActionResearchSynthesizeAnswer}
	guidedAllowlist := make([]string, 0, len(actions))
	for _, action := range actions {
		isSensitive := false
		for _, sensitiveAction := range sensitive {
			if action == sensitiveAction {
				isSensitive = true
				break
			}
		}
		if !isSensitive {
			guidedAllowlist = append(guidedAllowlist, action)
		}
	}
	return DeepAgentsCapabilities{
		Backend:                  "go-control-plane",
		ArtifactSchema:           ARTIFACT_SCHEMA_VERSION,
		WisdevActions:            actions,
		SensitiveWisdevActions:   sensitive,
		AllowlistedTools:         append([]string(nil), actions...),
		DefaultMaxExecutionMs:    45000,
		ToolsEnabled:             len(actions) > 0,
		ToolCount:                len(actions),
		RequireHumanConfirmation: true,
		PolicyByMode: map[string]DeepAgentsModePolicy{
			"guided": {
				EnableWisdevTools:        len(actions) > 0,
				AllowlistedTools:         append([]string(nil), guidedAllowlist...),
				RequireHumanConfirmation: true,
			},
			"yolo": {
				EnableWisdevTools:        len(actions) > 0,
				AllowlistedTools:         append([]string(nil), actions...),
				RequireHumanConfirmation: false,
			},
		},
	}
}

func ResolveDeepAgentsExecutionPolicy(
	caps DeepAgentsCapabilities,
	mode string,
	explicitEnable *bool,
	explicitAllowlisted []string,
	explicitRequire *bool,
) DeepAgentsExecutionPolicy {
	resolvedMode := "guided"
	if strings.Contains(strings.ToLower(strings.TrimSpace(mode)), "yolo") {
		resolvedMode = "yolo"
	}

	normalizeActions := func(actions []string) []string {
		if len(actions) == 0 {
			return nil
		}
		seen := make(map[string]struct{}, len(actions))
		out := make([]string, 0, len(actions))
		for _, action := range actions {
			normalized := strings.TrimSpace(action)
			if normalized == "" {
				continue
			}
			if _, ok := seen[normalized]; ok {
				continue
			}
			seen[normalized] = struct{}{}
			out = append(out, normalized)
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}

	availableActions := normalizeActions(caps.WisdevActions)
	if len(availableActions) == 0 {
		availableActions = normalizeActions(caps.AllowlistedTools)
	}
	modePolicy, hasModePolicy := caps.PolicyByMode[resolvedMode]

	enableWisdevTools := caps.ToolsEnabled || caps.ToolCount > 0 || len(availableActions) > 0
	if hasModePolicy {
		enableWisdevTools = modePolicy.EnableWisdevTools
	}
	if explicitEnable != nil {
		enableWisdevTools = *explicitEnable
	}

	allowlistedTools := normalizeActions(explicitAllowlisted)
	if len(allowlistedTools) == 0 && hasModePolicy {
		allowlistedTools = normalizeActions(modePolicy.AllowlistedTools)
	}
	if len(allowlistedTools) == 0 && enableWisdevTools {
		allowlistedTools = availableActions
	}

	requireHumanConfirmation := caps.RequireHumanConfirmation && resolvedMode == "guided"
	if hasModePolicy {
		requireHumanConfirmation = modePolicy.RequireHumanConfirmation
	}
	if explicitRequire != nil {
		requireHumanConfirmation = *explicitRequire
	}

	return DeepAgentsExecutionPolicy{
		EnableWisdevTools:        enableWisdevTools,
		AllowlistedTools:         allowlistedTools,
		RequireHumanConfirmation: requireHumanConfirmation,
		Mode:                     resolvedMode,
		ResolutionSource:         "go-control-plane",
	}
}
