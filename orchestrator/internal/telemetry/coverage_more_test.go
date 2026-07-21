package telemetry

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRecordLLMBudgetRequest(t *testing.T) {
	RecordLLMBudgetRequest("structured_output", "gemini-2.5-flash", nil, 250*time.Millisecond, 1200, true)
	RecordLLMBudgetRequest("structured_output", "gemini-2.5-flash", errors.New("fail"), 250*time.Millisecond, 1200, false)
}

func TestLogLLMBudgetEvent(t *testing.T) {
	LogLLMBudgetEvent(context.Background(), "llm_budget_event", "operation", "generate", "budget_ms", 1200)
}
