package policy

import "strings"

type PolicyHints struct {
	ExecutionMode           string           `json:"executionMode,omitempty"`
	OperationMode           string           `json:"operationMode,omitempty"`
	VerificationRequired    bool             `json:"verificationRequired,omitempty"`
	RequiresHumanCheckpoint bool             `json:"requiresHumanCheckpoint,omitempty"`
	AcademicIntegrity       map[string]any   `json:"academicIntegrity,omitempty"`
	Citations               []map[string]any `json:"citations,omitempty"`
}

type SandboxValidationResult struct {
	Valid  bool   `json:"valid"`
	Reason string `json:"reason"`
}

type StructuredValidationResult struct {
	Valid      bool           `json:"valid"`
	Reason     string         `json:"reason"`
	Normalized map[string]any `json:"normalized"`
}

func normalizeGuardrailReason(reason string) string {
	switch strings.TrimSpace(reason) {
	case "medium_risk_requires_confirmation":
		return "medium_risk_confirmation_required"
	case "high_risk_requires_confirmation":
		return "high_risk_confirmation_required"
	default:
		return strings.TrimSpace(reason)
	}
}

func ValidateSandboxSnippet(snippet string) SandboxValidationResult {
	trimmed := strings.TrimSpace(snippet)
	if trimmed == "" {
		return SandboxValidationResult{Valid: false, Reason: "empty_snippet"}
	}

	disallowed := []string{"__import__", "exec(", "eval(", "subprocess", "socket", "open(", "os.", "sys."}
	lower := strings.ToLower(trimmed)
	for _, token := range disallowed {
		if strings.Contains(lower, token) {
			return SandboxValidationResult{Valid: false, Reason: "disallowed_token:" + token}
		}
	}
	return SandboxValidationResult{Valid: true, Reason: "heuristic_allow"}
}

func ValidateStructuredOutput(schemaType string, payload map[string]any) StructuredValidationResult {
	normalizedType := strings.TrimSpace(strings.ToLower(schemaType))
	if normalizedType == "" {
		return StructuredValidationResult{Valid: false, Reason: "missing_schema_type"}
	}

	result := StructuredValidationResult{Valid: true, Reason: "heuristic_allow", Normalized: payload}
	switch normalizedType {
	case "claim_table", "claim-evidence", "claim_evidence":
		claims, _ := payload["claims"].([]any)
		if len(claims) == 0 {
			result.Valid = false
			result.Reason = "missing_claims"
		}
	case "prisma":
		if _, ok := payload["identified"]; !ok {
			result.Valid = false
			result.Reason = "missing_identified"
		}
	case "rebuttal":
		if strings.TrimSpace(toString(payload["summary"])) == "" {
			result.Valid = false
			result.Reason = "missing_summary"
		}
	case "reasoning_paths", "reasoning-paths", "reasoning_paths_verify":
		branches, _ := payload["branches"].([]any)
		if len(branches) == 0 {
			result.Valid = false
			result.Reason = "missing_branches"
			break
		}
		verified := 0
		for _, branch := range branches {
			branchMap, _ := branch.(map[string]any)
			score, _ := branchMap["supportScore"].(float64)
			if score >= 0.6 {
				verified++
			}
		}
		if verified == 0 {
			result.Valid = false
			result.Reason = "no_supported_reasoning_path"
		}
	default:
		result.Valid = false
		result.Reason = "unsupported_schema_type"
	}
	return result
}

func toString(value any) string {
	if value == nil {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
}
