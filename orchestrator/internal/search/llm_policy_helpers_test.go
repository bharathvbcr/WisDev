package search

import (
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

func TestAppendSearchStructuredOutputInstruction(t *testing.T) {
	t.Run("blank prompt returns instruction", func(t *testing.T) {
		got := appendSearchStructuredOutputInstruction("   ")
		if got != searchStructuredOutputSchemaInstruction {
			t.Fatalf("unexpected instruction: got %q want %q", got, searchStructuredOutputSchemaInstruction)
		}
	})

	t.Run("trimmed prompt appends instruction", func(t *testing.T) {
		got := appendSearchStructuredOutputInstruction("  Select providers  \n")
		want := "Select providers\n\n" + searchStructuredOutputSchemaInstruction
		if got != want {
			t.Fatalf("unexpected instruction: got %q want %q", got, want)
		}
	})
}

func TestApplySearchStandardGeneratePolicy(t *testing.T) {
	req := applySearchStandardGeneratePolicy(&llmv1.GenerateRequest{
		Prompt: "query",
		Model:  "custom-model",
	})

	if req.Model != "custom-model" {
		t.Fatalf("expected explicit model override to be preserved, got %q", req.Model)
	}
	if req.ServiceTier != "standard" {
		t.Fatalf("expected standard service tier, got %q", req.ServiceTier)
	}
	if req.RetryProfile != string(llm.RetryProfileStandard) {
		t.Fatalf("expected standard retry profile, got %q", req.RetryProfile)
	}
	if req.RequestClass != string(llm.RequestClassStandard) {
		t.Fatalf("expected standard request class, got %q", req.RequestClass)
	}
	if req.ThinkingBudget == nil || *req.ThinkingBudget != 1024 {
		t.Fatalf("expected standard thinking budget, got %#v", req.ThinkingBudget)
	}
}

func TestApplySearchStandardStructuredPolicy(t *testing.T) {
	req := applySearchStandardStructuredPolicy(&llmv1.StructuredRequest{
		Prompt: "query",
	})

	if req.Model != llm.ResolveStandardModel() {
		t.Fatalf("expected standard model, got %q", req.Model)
	}
	if req.ServiceTier != "standard" {
		t.Fatalf("expected standard service tier, got %q", req.ServiceTier)
	}
	if req.RetryProfile != string(llm.RetryProfileStandard) {
		t.Fatalf("expected standard retry profile, got %q", req.RetryProfile)
	}
	if req.RequestClass != string(llm.RequestClassStandard) {
		t.Fatalf("expected standard request class, got %q", req.RequestClass)
	}
	if req.ThinkingBudget == nil || *req.ThinkingBudget != 1024 {
		t.Fatalf("expected standard thinking budget, got %#v", req.ThinkingBudget)
	}
}
