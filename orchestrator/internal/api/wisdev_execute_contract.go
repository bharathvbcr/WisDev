package api

import (
	"fmt"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

var canonicalCapabilityStepActions = map[string]struct{}{
	"continue":          {},
	"pause_for_review":  {},
	"escalate":          {},
	"abort":             {},
	"approve":           {},
	"skip":              {},
	"edit_payload":      {},
	"reject_and_replan": {},
}

type executeCapabilityRequest struct {
	SessionID     string         `json:"sessionId"`
	StepID        string         `json:"stepId"`
	Action        string         `json:"action"`
	Payload       map[string]any `json:"payload"`
	Context       map[string]any `json:"context"`
	EditedPayload map[string]any `json:"editedPayload"`
	StepAction    string         `json:"stepAction"`
	Confirm       bool           `json:"confirm"`
	DryRun        bool           `json:"dryRun"`
}

func normalizeExecuteCapabilityRequest(req executeCapabilityRequest) executeCapabilityRequest {
	req.SessionID = normalizeOptionalString(req.SessionID)
	req.StepID = normalizeOptionalString(req.StepID)
	req.Action = normalizeOptionalString(req.Action)
	req.StepAction = normalizeCapabilityStepAction(req.StepAction, asMap(req.Payload)["stepAction"])

	payload := cloneMap(req.Payload)
	if payload == nil {
		payload = make(map[string]any)
	}
	context := cloneMap(req.Context)
	if context == nil {
		context = make(map[string]any)
	}

	if req.StepAction != "" {
		payload["stepAction"] = req.StepAction
	}

	if toolInvocationPolicy := normalizeOptionalString(payload["toolInvocationPolicy"]); toolInvocationPolicy != "" {
		payload["toolInvocationPolicy"] = strings.ToLower(toolInvocationPolicy)
	}

	currentStepID := firstNonEmptyAnyString(
		payload["stepId"],
		context["currentStepId"],
		req.StepID,
	)
	selectedParallelStepIDs := mergeUniqueAnyStringSlices(
		normalizeStringSlice(payload["selectedParallelStepIds"]),
		normalizeStringSlice(context["selectedParallelStepIds"]),
	)
	if currentStepID == "" && len(selectedParallelStepIDs) > 0 {
		currentStepID = selectedParallelStepIDs[0]
	}
	if currentStepID != "" {
		req.StepID = currentStepID
		payload["stepId"] = currentStepID
		context["currentStepId"] = currentStepID
		selectedParallelStepIDs = mergeUniqueAnyStringSlices(selectedParallelStepIDs, []string{currentStepID})
	}
	if len(selectedParallelStepIDs) > 0 {
		payload["selectedParallelStepIds"] = selectedParallelStepIDs
		context["selectedParallelStepIds"] = selectedParallelStepIDs
	}

	if _, ok := payload["enforceTieredControl"]; !ok {
		payload["enforceTieredControl"] = true
	}

	if req.EditedPayload != nil && payload["editedPayload"] == nil {
		payload["editedPayload"] = cloneMap(req.EditedPayload)
	}

	req.Payload = payload
	req.Context = context
	return req
}

func normalizeExecutionPayload(payload map[string]any, reqPayload map[string]any, reqContext map[string]any) map[string]any {
	normalized := cloneMap(payload)
	if normalized == nil {
		normalized = make(map[string]any)
	}

	nextActions := normalizeCapabilityNextActions(normalized["nextActions"])
	if len(nextActions) > 0 {
		normalized["nextActions"] = nextActions
	} else {
		delete(normalized, "nextActions")
	}

	guardrailReason := normalizeOptionalString(normalized["guardrailReason"])
	if guardrailReason == "" && len(nextActions) > 0 && (boolValue(normalized["requiresConfirmation"]) || !boolValue(normalized["applied"])) {
		guardrailReason = "action_required"
	}
	if guardrailReason != "" {
		normalized["guardrailReason"] = guardrailReason
	}

	data := cloneMap(asMap(normalized["data"]))
	if data == nil {
		data = make(map[string]any)
	}
	if _, ok := data["controlPlane"]; !ok {
		data["controlPlane"] = "go"
	}

	if sourceMix := mergeUniqueAnyStringSlices(
		normalizeStringSlice(data["sourceMix"]),
		normalizeStringSlice(reqPayload["sourceMix"]),
	); len(sourceMix) > 0 {
		data["sourceMix"] = sourceMix
	}

	if providers := mergeUniqueAnyStringSlices(
		normalizeStringSlice(data["providers"]),
		normalizeStringSlice(reqPayload["providers"]),
		normalizeStringSlice(reqContext["sourcePreferences"]),
	); len(providers) > 0 {
		data["providers"] = providers
	}

	if toolInvocationPolicy := normalizeOptionalString(reqPayload["toolInvocationPolicy"]); toolInvocationPolicy != "" {
		data["toolInvocationPolicy"] = strings.ToLower(toolInvocationPolicy)
	}

	if stepID := firstNonEmptyAnyString(
		data["stepId"],
		normalized["stepId"],
		reqPayload["stepId"],
	); stepID != "" {
		data["stepId"] = stepID
	}

	if selectedParallelStepIDs := mergeUniqueAnyStringSlices(
		normalizeStringSlice(data["selectedParallelStepIds"]),
		normalizeStringSlice(reqPayload["selectedParallelStepIds"]),
		normalizeStringSlice(reqContext["selectedParallelStepIds"]),
	); len(selectedParallelStepIDs) > 0 {
		data["selectedParallelStepIds"] = selectedParallelStepIDs
	}

	if runtimeToolCandidateNames := normalizeRuntimeToolCandidateNames(data["runtimeToolCandidates"]); len(runtimeToolCandidateNames) > 0 {
		data["runtimeToolCandidateNames"] = runtimeToolCandidateNames
	}

	if structuredValidation := asMap(data["structuredValidation"]); structuredValidation != nil {
		if _, ok := data["structuredValidationValid"]; !ok {
			data["structuredValidationValid"] = boolValue(structuredValidation["valid"])
		}
		if schemaType := normalizeOptionalString(structuredValidation["schemaType"]); schemaType != "" {
			data["structuredValidationSchemaType"] = schemaType
		}
		if reason := normalizeOptionalString(structuredValidation["reason"]); reason != "" {
			data["structuredValidationReason"] = reason
		}
	}

	normalized["data"] = data
	return normalized
}

func buildClaimEvidenceTableExecution(req executeCapabilityRequest) map[string]any {
	evidence := buildClaimEvidenceGatePayload(req.Payload)
	claimCount := intValue(evidence["claimCount"])
	linkedClaimCount := intValue(evidence["linkedClaimCount"])
	unlinkedClaimCount := intValue(evidence["unlinkedClaimCount"])
	strictGatePass := claimCount > 0 && unlinkedClaimCount == 0

	message := "Strict evidence gate passed."
	risk := "low"
	if claimCount == 0 {
		message = "Strict evidence gate failed: no claims were supplied."
		risk = "medium"
	} else if !strictGatePass {
		message = fmt.Sprintf("Strict evidence gate failed: %d of %d claims are not linked to evidence.", unlinkedClaimCount, claimCount)
		risk = "medium"
	}
	if req.DryRun {
		message = "Strict evidence gate dry run completed."
	}

	claimEvidenceTable := map[string]any{
		"rowCount":           claimCount,
		"linkedClaimCount":   linkedClaimCount,
		"unlinkedClaimCount": unlinkedClaimCount,
		"strictGatePass":     strictGatePass,
	}
	payload := map[string]any{
		"sessionId":            req.SessionID,
		"stepId":               req.StepID,
		"applied":              strictGatePass && !req.DryRun,
		"requiresConfirmation": !strictGatePass,
		"risk":                 risk,
		"message":              message,
		"traceId":              wisdev.NewTraceID(),
		"evidence":             evidence,
		"data": map[string]any{
			"result": map[string]any{
				"claimEvidenceTable": claimEvidenceTable,
				"table":              fmt.Sprintf("claims=%d linked=%d unlinked=%d", claimCount, linkedClaimCount, unlinkedClaimCount),
				"rowCount":           claimCount,
			},
			"claimEvidenceTable": claimEvidenceTable,
			"evidenceGate":       true,
			"workerPlane":        "go",
			"controlPlane":       "go",
		},
	}
	if !strictGatePass {
		payload["guardrailReason"] = "unlinked_claims"
		payload["nextActions"] = wisdev.ConfirmationActions()
	} else {
		payload["nextActions"] = []string{"approve"}
	}
	if req.DryRun {
		payload["requiresConfirmation"] = false
		payload["data"].(map[string]any)["previewOnly"] = true
	}
	return payload
}

func buildClaimEvidenceGatePayload(payload map[string]any) map[string]any {
	claims := normalizeClaimEvidenceClaims(payload["claims"])
	claimCount := len(claims)
	if claimCount == 0 {
		claimCount = maxInt(
			intValue(payload["heuristicClaimCount"]),
			len(normalizeStringSlice(payload["queries"])),
		)
	}

	linkedClaimCount := 0
	for _, claim := range claims {
		if claimHasEvidenceLink(claim) {
			linkedClaimCount++
		}
	}
	if len(claims) == 0 {
		linkedClaimCount = minInt(intValue(payload["heuristicLinkedClaimCount"]), claimCount)
	}
	if linkedClaimCount > claimCount {
		claimCount = linkedClaimCount
	}
	unlinkedClaimCount := claimCount - linkedClaimCount
	if unlinkedClaimCount < 0 {
		unlinkedClaimCount = 0
	}

	return map[string]any{
		"claimCount":         claimCount,
		"linkedClaimCount":   linkedClaimCount,
		"unlinkedClaimCount": unlinkedClaimCount,
		"strictGatePass":     claimCount > 0 && unlinkedClaimCount == 0,
	}
}

func normalizeClaimEvidenceClaims(value any) []map[string]any {
	switch typed := value.(type) {
	case []map[string]any:
		return typed
	case []any:
		claims := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			switch claim := item.(type) {
			case map[string]any:
				if strings.TrimSpace(normalizeOptionalString(claim["claim"])) != "" || claimHasEvidenceLink(claim) {
					claims = append(claims, claim)
				}
			case string:
				if normalized := normalizeOptionalString(claim); normalized != "" {
					claims = append(claims, map[string]any{"claim": normalized})
				}
			}
		}
		return claims
	case []string:
		claims := make([]map[string]any, 0, len(typed))
		for _, claim := range typed {
			if normalized := normalizeOptionalString(claim); normalized != "" {
				claims = append(claims, map[string]any{"claim": normalized})
			}
		}
		return claims
	default:
		return nil
	}
}

func claimHasEvidenceLink(claim map[string]any) bool {
	for _, key := range []string{
		"source",
		"sources",
		"sourceId",
		"sourceIds",
		"source_id",
		"source_ids",
		"paperId",
		"paperIds",
		"paper_id",
		"paper_ids",
		"doi",
		"link",
		"url",
		"citation",
		"citationId",
		"citationIds",
		"evidence",
		"evidenceId",
		"evidenceIds",
	} {
		if hasEvidenceValue(claim[key]) {
			return true
		}
	}
	return false
}

func hasEvidenceValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(typed) != ""
	case []string:
		return len(normalizeStringSlice(typed)) > 0
	case []any:
		for _, item := range typed {
			if hasEvidenceValue(item) {
				return true
			}
		}
		return false
	case map[string]any:
		return len(typed) > 0
	default:
		return normalizeOptionalString(typed) != ""
	}
}

func normalizeCapabilityStepAction(values ...any) string {
	for _, value := range values {
		raw := normalizeOptionalString(value)
		if raw == "" {
			continue
		}
		action := strings.ToLower(raw)
		if _, ok := canonicalCapabilityStepActions[action]; ok {
			return action
		}
		if action = wisdev.CanonicalizeConfirmationAction(action); action != "" {
			if _, ok := canonicalCapabilityStepActions[action]; ok {
				return action
			}
		}
	}
	return ""
}

func normalizeCapabilityNextActions(value any) []string {
	raw := normalizeStringSlice(value)
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		action := wisdev.CanonicalizeConfirmationAction(item)
		if _, ok := canonicalCapabilityStepActions[action]; !ok {
			continue
		}
		if _, exists := seen[action]; exists {
			continue
		}
		seen[action] = struct{}{}
		out = append(out, action)
	}
	return out
}

func normalizeRuntimeToolCandidateNames(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	names := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		name := ""
		switch typed := item.(type) {
		case string:
			name = strings.TrimSpace(typed)
		case map[string]any:
			name = normalizeOptionalString(typed["name"])
			if name == "" {
				name = normalizeOptionalString(typed["tool"])
			}
		}
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names
}

func normalizeStringSlice(value any) []string {
	switch typed := value.(type) {
	case nil:
		return nil
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if normalized := normalizeOptionalString(item); normalized != "" {
				out = append(out, normalized)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if normalized := normalizeOptionalString(item); normalized != "" {
				out = append(out, normalized)
			}
		}
		return out
	case string:
		if normalized := normalizeOptionalString(typed); normalized != "" {
			return []string{normalized}
		}
	}
	return nil
}

func mergeUniqueAnyStringSlices(values ...[]string) []string {
	out := make([]string, 0)
	seen := make(map[string]struct{})
	for _, group := range values {
		for _, item := range group {
			normalized := normalizeOptionalString(item)
			if normalized == "" {
				continue
			}
			if _, exists := seen[normalized]; exists {
				continue
			}
			seen[normalized] = struct{}{}
			out = append(out, normalized)
		}
	}
	return out
}

func normalizeOptionalString(value any) string {
	text := strings.TrimSpace(fmt.Sprintf("%v", value))
	if text == "" || text == "<nil>" {
		return ""
	}
	return text
}

func firstNonEmptyAnyString(values ...any) string {
	for _, value := range values {
		if normalized := normalizeOptionalString(value); normalized != "" {
			return normalized
		}
	}
	return ""
}

func asMap(value any) map[string]any {
	if value == nil {
		return nil
	}
	record, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return record
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func boolValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}
