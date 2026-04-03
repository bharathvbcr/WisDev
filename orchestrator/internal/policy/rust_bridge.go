package policy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type rustPolicyEvalInput struct {
	Config             PolicyConfig `json:"config"`
	Budget             BudgetState  `json:"budget"`
	Risk               RiskLevel    `json:"risk"`
	IsScript           bool         `json:"isScript"`
	EstimatedCostCents int          `json:"estimatedCostCents"`
}

type rustPolicyEvalOutput struct {
	Allowed              bool   `json:"allowed"`
	RequiresConfirmation bool   `json:"requiresConfirmation"`
	Reason               string `json:"reason"`
}

type RustSandboxValidationResult struct {
	Valid  bool   `json:"valid"`
	Reason string `json:"reason"`
}

type rustSandboxValidationInput struct {
	Snippet string `json:"snippet"`
}

type RustStructuredValidationResult struct {
	Valid      bool                   `json:"valid"`
	Reason     string                 `json:"reason"`
	Normalized map[string]any `json:"normalized"`
}

type rustStructuredValidationInput struct {
	SchemaType string                 `json:"schemaType"`
	Payload    map[string]any `json:"payload"`
}

type rustBridgeEnvelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error string          `json:"error"`
}

var bridgeClient = &http.Client{
	Timeout: 5 * time.Second,
}

func rustBridgeBaseURL() string {
	base := strings.TrimSpace(os.Getenv("RUST_GATEWAY_INTERNAL_URL"))
	if base == "" {
		return "http://localhost:8080/internal/wisdev-bridge"
	}
	return strings.TrimRight(base, "/") + "/internal/wisdev-bridge"
}

func rustBridgeEnabled() bool {
	return true // Rust Gateway is now the primary entry point
}

func RustRequired() bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv("WISDEV_RUST_REQUIRED")))
	if raw == "" {
		return true
	}
	return raw == "1" || raw == "true" || raw == "yes"
}

func runRustBridgeCommand(command string, payload any, out any) error {
	url := fmt.Sprintf("%s/%s", rustBridgeBaseURL(), command)
	
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	
	// Add internal service key if configured
	if key := os.Getenv("INTERNAL_SERVICE_KEY"); key != "" {
		req.Header.Set("X-Internal-Service-Key", key)
	}

	resp, err := bridgeClient.Do(req)
	if err != nil {
		return fmt.Errorf("rust bridge request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("rust bridge returned status %d", resp.StatusCode)
	}

	return decodeRustBridgeOutput(resp.Body, out)
}

func decodeRustBridgeOutput(r io.Reader, out any) error {
	var envelope rustBridgeEnvelope
	if err := json.NewDecoder(r).Decode(&envelope); err != nil {
		return fmt.Errorf("failed to decode rust bridge response: %w", err)
	}

	if !envelope.OK {
		if strings.TrimSpace(envelope.Error) != "" {
			return errors.New(strings.TrimSpace(envelope.Error))
		}
		return errors.New("rust bridge reported failure")
	}

	if out == nil {
		return nil
	}

	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return nil
	}

	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("decode rust bridge envelope data: %w", err)
	}
	return nil
}

func evaluateGuardrailViaRust(cfg PolicyConfig, budget BudgetState, risk RiskLevel, isScript bool, estimatedCostCents int) (GuardrailDecision, bool) {
	input := rustPolicyEvalInput{
		Config:             cfg,
		Budget:             budget,
		Risk:               risk,
		IsScript:           isScript,
		EstimatedCostCents: estimatedCostCents,
	}
	var out rustPolicyEvalOutput
	if err := runRustBridgeCommand("policy-eval", input, &out); err != nil {
		if RustRequired() {
			return GuardrailDecision{
				Allowed: false,
				Reason:  "rust_policy_unavailable",
			}, true
		}
		return GuardrailDecision{}, false
	}
	if strings.TrimSpace(out.Reason) == "" {
		out.Reason = "rust_policy_default"
	}
	return GuardrailDecision{
		Allowed:              out.Allowed,
		RequiresConfirmation: out.RequiresConfirmation,
		Reason:               out.Reason,
	}, true
}

func ValidateSandboxSnippet(snippet string) RustSandboxValidationResult {
	trimmed := strings.TrimSpace(snippet)
	if trimmed == "" {
		return RustSandboxValidationResult{Valid: false, Reason: "empty_snippet"}
	}

	if rustBridgeEnabled() {
		var out RustSandboxValidationResult
		if err := runRustBridgeCommand("sandbox-validate", rustSandboxValidationInput{Snippet: snippet}, &out); err == nil {
			return out
		} else if RustRequired() {
			return RustSandboxValidationResult{Valid: false, Reason: "rust_sandbox_unavailable"}
		}
	}

	disallowed := []string{"__import__", "exec(", "eval(", "subprocess", "socket", "open(", "os.", "sys."}
	lower := strings.ToLower(trimmed)
	for _, token := range disallowed {
		if strings.Contains(lower, token) {
			return RustSandboxValidationResult{Valid: false, Reason: "disallowed_token:" + token}
		}
	}
	return RustSandboxValidationResult{Valid: true, Reason: "heuristic_allow"}
}

func ValidateStructuredOutput(schemaType string, payload map[string]any) RustStructuredValidationResult {
	normalizedType := strings.TrimSpace(strings.ToLower(schemaType))
	if normalizedType == "" {
		return RustStructuredValidationResult{Valid: false, Reason: "missing_schema_type"}
	}

	if rustBridgeEnabled() {
		var out RustStructuredValidationResult
		if err := runRustBridgeCommand("structured-validate", rustStructuredValidationInput{SchemaType: normalizedType, Payload: payload}, &out); err == nil {
			if out.Normalized == nil {
				out.Normalized = map[string]any{}
			}
			return out
		} else if RustRequired() {
			return RustStructuredValidationResult{
				Valid:      false,
				Reason:     "rust_structured_validator_unavailable",
				Normalized: payload,
			}
		}
	}

	result := RustStructuredValidationResult{Valid: true, Reason: "heuristic_allow", Normalized: payload}
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
