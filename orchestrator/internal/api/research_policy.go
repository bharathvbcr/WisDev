package api

import (
	"log/slog"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

const (
	autonomousActionBlockedToolsDisabled      = "tools_disabled"
	autonomousActionBlockedAllowlistEmpty     = "allowlist_empty"
	autonomousActionBlockedNotAllowlisted     = "action_not_allowlisted"
	autonomousActionBlockedConfirmationNeeded = "confirmation_required"
)

func resolveAutonomousExecutionMode(topLevelMode string, sessionMode string) wisdev.WisDevMode {
	mode := strings.TrimSpace(topLevelMode)
	if mode == "" {
		mode = strings.TrimSpace(sessionMode)
	}
	return normalizeSessionMode(mode)
}

func resolveAutonomousExecutionPolicy(
	agentGateway *wisdev.AgentGateway,
	mode string,
	explicitEnable *bool,
	explicitAllowlisted []string,
	explicitRequire *bool,
) wisdev.DeepAgentsExecutionPolicy {
	registry := (*wisdev.ToolRegistry)(nil)
	if agentGateway != nil {
		registry = agentGateway.Registry
	}
	return wisdev.ResolveDeepAgentsExecutionPolicy(
		wisdev.BuildDeepAgentsCapabilities(registry),
		mode,
		explicitEnable,
		explicitAllowlisted,
		explicitRequire,
	)
}

func autonomousActionAllowed(
	agentGateway *wisdev.AgentGateway,
	policy wisdev.DeepAgentsExecutionPolicy,
	actions ...string,
) (bool, string) {
	if !policy.EnableWisdevTools {
		return false, autonomousActionBlockedToolsDisabled
	}

	allowlisted := make(map[string]struct{}, len(policy.AllowlistedTools))
	for _, action := range policy.AllowlistedTools {
		canonical := wisdev.CanonicalizeWisdevAction(strings.TrimSpace(action))
		if canonical == "" {
			continue
		}
		allowlisted[canonical] = struct{}{}
	}
	if len(allowlisted) == 0 {
		return false, autonomousActionBlockedAllowlistEmpty
	}

	confirmationBlocked := false
	for _, action := range actions {
		canonical := wisdev.CanonicalizeWisdevAction(strings.TrimSpace(action))
		if canonical == "" {
			continue
		}
		if _, ok := allowlisted[canonical]; !ok {
			continue
		}
		if policy.RequireHumanConfirmation && autonomousActionRequiresConfirmation(agentGateway, canonical) {
			confirmationBlocked = true
			continue
		}
		return true, ""
	}

	if confirmationBlocked {
		return false, autonomousActionBlockedConfirmationNeeded
	}
	return false, autonomousActionBlockedNotAllowlisted
}

func autonomousActionRequiresConfirmation(agentGateway *wisdev.AgentGateway, action string) bool {
	registry := autonomousPolicyRegistry(agentGateway)
	definition, err := registry.Get(action)
	if err != nil {
		slog.Warn("autonomous policy confirmation fallback used for unknown action", "action", action, "error", err)
		return true
	}
	return definition.Risk == wisdev.RiskLevelMedium || definition.Risk == wisdev.RiskLevelHigh
}

func autonomousPolicyRegistry(agentGateway *wisdev.AgentGateway) *wisdev.ToolRegistry {
	if agentGateway != nil && agentGateway.Registry != nil {
		return agentGateway.Registry
	}
	return wisdev.NewToolRegistry()
}
