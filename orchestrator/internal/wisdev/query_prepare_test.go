package wisdev

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/llm"
	llmv1 "github.com/bharathvbcr/wisdev-arc/orchestrator/proto/llm"
)

func allowStructuredQueryPrep(t *testing.T, msc *mockLLMServiceClient, payload structuredResearchQueryPrep) {
	t.Helper()
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return req != nil &&
			req.Model == llm.ResolveLightModel() &&
			strings.Contains(req.Prompt, "Prepare this academic research query")
	})).Return(&llmv1.StructuredResponse{JsonResult: string(data)}, nil).Once()
}

func TestPrepareResearchQueryUsesAIStructuredPrep(t *testing.T) {
	preparedQueryCache = sync.Map{}
	msc := &mockLLMServiceClient{}
	client := llm.NewClient()
	client.SetClient(msc)
	allowStructuredQueryPrep(t, msc, structuredResearchQueryPrep{
		CorrectedQuery: "meniscus reconstruction strategies",
		SearchQuery:    "meniscus reconstruction strategies",
		Domain:         "medicine",
		Intent:         "medical",
		Keywords:       []string{"meniscus", "reconstruction", "strategies"},
		Synonyms:       []string{"meniscal repair"},
		SeedQueries:    []string{"meniscus reconstruction strategies", "meniscal tissue engineering"},
		AgendaQueries:  []string{"meniscus reconstruction systematic review"},
	})

	prepared := PrepareResearchQueryWithContext(context.Background(), "Menicius reconstruction stratiges", ResearchQueryPrepareOptions{
		LLMClient: client,
	})
	for _, want := range []string{"meniscus", "strategies"} {
		if !strings.Contains(strings.ToLower(prepared.Corrected), want) {
			t.Fatalf("expected corrected query to contain %q: %s", want, prepared.Corrected)
		}
	}
	if prepared.Domain != "medicine" {
		t.Fatalf("expected medicine domain, got %q", prepared.Domain)
	}
	if len(prepared.SeedQueries) < 2 {
		t.Fatalf("expected focused seed queries, got %#v", prepared.SeedQueries)
	}
	msc.AssertExpectations(t)
}

func TestPrepareResearchQueryStructuredACLCompactSearchQuery(t *testing.T) {
	preparedQueryCache = sync.Map{}
	msc := &mockLLMServiceClient{}
	client := llm.NewClient()
	client.SetClient(msc)
	allowStructuredQueryPrep(t, msc, structuredResearchQueryPrep{
		CorrectedQuery: "meniscus scaffolds and ACL anterior cruciate ligament reconstruction strategies",
		SearchQuery:    "meniscus scaffolds ACL reconstruction strategies",
		Domain:         "medicine",
		Intent:         "medical",
		Keywords:       []string{"meniscus", "scaffolds", "acl", "reconstruction"},
		Synonyms:       []string{"meniscal scaffold"},
		SeedQueries: []string{
			"meniscus scaffolds ACL reconstruction",
			"meniscal tissue engineering",
			"ACL graft strategy",
		},
		AgendaQueries: []string{"meniscus scaffolds clinical trial outcomes"},
	})

	prepared := PrepareResearchQueryWithContext(context.Background(), "meniscus scaffolds and acl re constricution stratigies_", ResearchQueryPrepareOptions{
		LLMClient: client,
	})
	if prepared.Corrected == prepared.Original {
		t.Fatal("expected corrections to change the query")
	}
	if strings.Contains(prepared.SearchQuery, "anterior cruciate ligament") {
		t.Fatalf("expected compact search query: %s", prepared.SearchQuery)
	}
	if !strings.Contains(strings.ToLower(prepared.SearchQuery), "acl") {
		t.Fatalf("expected ACL token in search query: %s", prepared.SearchQuery)
	}
	if len(prepared.SeedQueries) < 3 {
		t.Fatalf("expected focused seed queries, got %#v", prepared.SeedQueries)
	}
}

func TestPrepareJobResearchQueryCorrectsTyposOffline(t *testing.T) {
	preparedQueryCache = sync.Map{}
	original, searchQuery, _ := PrepareJobResearchQuery(context.Background(), "Menicius reconstruction stratiges", "", nil, true)
	require.Equal(t, "Menicius reconstruction stratiges", original)
	require.Equal(t, "meniscus reconstruction strategies", searchQuery)
}

func TestApplyEarlySessionQueryPrepCorrectsTyposOffline(t *testing.T) {
	preparedQueryCache = sync.Map{}
	original, corrected, planning, domain := ApplyEarlySessionQueryPrep(
		context.Background(),
		"Menicius reconstruction stratiges",
		"",
		"",
		"",
		nil,
		true,
	)
	require.Equal(t, "Menicius reconstruction stratiges", original)
	require.Equal(t, "meniscus reconstruction strategies", corrected)
	require.Equal(t, "meniscus reconstruction strategies", planning)
	_ = domain
}

func TestPrepareResearchQueryFallsBackWithoutLLM(t *testing.T) {
	oldClient := GlobalLLMClient
	GlobalLLMClient = nil
	defer func() { GlobalLLMClient = oldClient }()
	preparedQueryCache = sync.Map{}

	prepared := PrepareResearchQueryWithContext(context.Background(), "Menicius reconstruction stratiges", ResearchQueryPrepareOptions{
		DisableAI: true,
	})
	require.Equal(t, "meniscus reconstruction strategies", prepared.Corrected)
	require.True(t, prepared.Changed)
}
