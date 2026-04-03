package wisdev

import (
	"context"

	llmv1 "github.com/wisdev-agent/wisdev-agent-os/orchestrator/proto/llm/v1"

	"github.com/stretchr/testify/mock"
)

// mockLLM is a testify mock implementing LLMRequester.
// Used by paper2skill_test.go and any other wisdev tests that need an LLM stub.
type mockLLM struct {
	mock.Mock
}

func (m *mockLLM) StructuredOutput(ctx context.Context, req *llmv1.StructuredRequest) (*llmv1.StructuredResponse, error) {
	args := m.Called(ctx, req)
	resp, _ := args.Get(0).(*llmv1.StructuredResponse)
	return resp, args.Error(1)
}

func (m *mockLLM) Generate(ctx context.Context, req *llmv1.GenerateRequest) (*llmv1.GenerateResponse, error) {
	args := m.Called(ctx, req)
	resp, _ := args.Get(0).(*llmv1.GenerateResponse)
	return resp, args.Error(1)
}
