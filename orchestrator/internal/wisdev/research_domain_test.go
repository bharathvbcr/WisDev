package wisdev

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

func mockDomainPrep(t *testing.T, msc *mockLLMServiceClient, domain string) {
	t.Helper()
	payload := structuredResearchQueryPrep{
		CorrectedQuery: "query",
		SearchQuery:    "query",
		Domain:         domain,
		Intent:         "academic",
		Keywords:       []string{"query"},
		Synonyms:       nil,
		SeedQueries:    []string{"query"},
		AgendaQueries:  []string{"query systematic review"},
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.Prompt, "Prepare this academic research query")
	})).Return(&llmv1.StructuredResponse{JsonResult: string(data)}, nil).Once()
}

func TestInferResearchDomainMeniscusACL(t *testing.T) {
	preparedQueryCache = sync.Map{}
	oldClient := GlobalLLMClient
	defer func() { GlobalLLMClient = oldClient }()

	msc := &mockLLMServiceClient{}
	client := llm.NewClient()
	client.SetClient(msc)
	GlobalLLMClient = client
	mockDomainPrep(t, msc, "medicine")

	if got := InferResearchDomain("meniscus scaffolds and ACL reconstruction strategies"); got != "medicine" {
		t.Fatalf("expected medicine domain, got %q", got)
	}
}

func TestInferResearchDomainComputerScience(t *testing.T) {
	preparedQueryCache = sync.Map{}
	oldClient := GlobalLLMClient
	defer func() { GlobalLLMClient = oldClient }()

	msc := &mockLLMServiceClient{}
	client := llm.NewClient()
	client.SetClient(msc)
	GlobalLLMClient = client
	mockDomainPrep(t, msc, "cs")

	if got := InferResearchDomain("transformer architecture for code generation"); got != "cs" {
		t.Fatalf("expected cs domain, got %q", got)
	}
}
