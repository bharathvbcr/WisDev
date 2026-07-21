package wisdev

import "strings"

var canonicalConfirmationActions = []string{
	"approve",
	"skip",
	"edit_payload",
	"reject_and_replan",
}

func ConfirmationActions() []string {
	actions := make([]string, len(canonicalConfirmationActions))
	copy(actions, canonicalConfirmationActions)
	return actions
}

func CanonicalizeConfirmationAction(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "confirm_and_execute":
		return "approve"
	case "cancel":
		return "skip"
	case "reject_replan":
		return "reject_and_replan"
	default:
		return strings.ToLower(strings.TrimSpace(action))
	}
}
