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
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
	llmv1 "github.com/bharathvbcr/wisdev-arc/orchestrator/proto/llm"
)

func seedStructuredPrep(t *testing.T, msc *mockLLMServiceClient, query string, seeds []string) {
	t.Helper()
	payload := structuredResearchQueryPrep{
		CorrectedQuery: query,
		SearchQuery:    query,
		Domain:         "medicine",
		Intent:         "medical",
		Keywords:       queryAnchorTerms(query),
		Synonyms:       nil,
		SeedQueries:    seeds,
		AgendaQueries:  []string{query + " systematic review"},
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.Prompt, "Prepare this academic research query")
	})).Return(&llmv1.StructuredResponse{JsonResult: string(data)}, nil).Once()
}

func withMockGlobalLLM(t *testing.T, fn func(msc *mockLLMServiceClient, client *llm.Client)) {
	t.Helper()
	preparedQueryCache = sync.Map{}
	oldClient := GlobalLLMClient
	defer func() { GlobalLLMClient = oldClient }()

	msc := &mockLLMServiceClient{}
	client := llm.NewClient()
	client.SetClient(msc)
	GlobalLLMClient = client
	fn(msc, client)
}

func TestBuildTopicFocusedQueries(t *testing.T) {
	withMockGlobalLLM(t, func(msc *mockLLMServiceClient, _ *llm.Client) {
		query := "What is the new research for meniscus scaffolds"
		seedStructuredPrep(t, msc, query, []string{
			"meniscus scaffolds tissue engineering",
			"meniscus scaffold hydrogel",
			"meniscal tissue engineering",
		})
		queries := BuildTopicFocusedQueries(query)
		if len(queries) == 0 {
			t.Fatal("expected focused queries")
		}
		joined := strings.Join(queries, " ")
		if !strings.Contains(joined, "meniscus") || !strings.Contains(joined, "scaffold") {
			t.Fatalf("expected meniscus scaffold focus, got %v", queries)
		}
		if !strings.Contains(joined, "tissue engineering") {
			t.Fatalf("expected biomedical focused queries, got %v", queries)
		}
	})
}

func TestPaperMatchesCompoundQueryTopicClauses(t *testing.T) {
	query := "meniscus scaffolds and acl reconstruction strategies"
	meniscusPaper := search.Paper{
		Title:    "3D printed meniscus scaffold for knee repair",
		Abstract: "Porous meniscus scaffold for meniscal regeneration.",
	}
	aclPaper := search.Paper{
		Title:    "ACL reconstruction with hamstring graft outcomes",
		Abstract: "Anterior cruciate ligament reconstruction strategies in athletes.",
	}
	offTopic := search.Paper{
		Title:    "Bone scaffold composite for femur repair",
		Abstract: "Bone regeneration scaffold for long-bone defects in adults.",
	}
	if !paperMatchesQueryRelevance(query, meniscusPaper) {
		t.Fatal("expected meniscus paper to match compound query")
	}
	if !paperMatchesQueryRelevance(query, aclPaper) {
		t.Fatal("expected ACL paper to match compound query")
	}
	if paperMatchesQueryRelevance(query, offTopic) {
		t.Fatal("expected off-topic bone paper to be rejected")
	}
}

func TestBuildTopicFocusedQueriesCompound(t *testing.T) {
	withMockGlobalLLM(t, func(msc *mockLLMServiceClient, _ *llm.Client) {
		query := "meniscus scaffolds and acl reconstruction strategies"
		seedStructuredPrep(t, msc, query, []string{
			"meniscus scaffolds",
			"acl reconstruction strategies",
		})
		queries := BuildTopicFocusedQueries(query)
		joined := strings.Join(queries, " | ")
		if !strings.Contains(joined, "meniscus") || !strings.Contains(joined, "acl") {
			t.Fatalf("expected compound focused queries, got %v", queries)
		}
	})
}

func TestBuildTopicFocusedQueriesScaffoldExtras(t *testing.T) {
	withMockGlobalLLM(t, func(msc *mockLLMServiceClient, _ *llm.Client) {
		query := "meniscus scaffolds and acl reconstruction strategies"
		seedStructuredPrep(t, msc, query, []string{
			"meniscus scaffold hydrogel",
			"meniscal tissue engineering",
			"ACL graft strategy",
		})
		queries := BuildTopicFocusedQueries(query)
		joined := strings.Join(queries, " ")
		for _, want := range []string{"hydrogel", "meniscal tissue engineering", "graft strategy"} {
			if !strings.Contains(joined, want) {
				t.Fatalf("expected scaffold-focused seed %q in %#v", want, queries)
			}
		}
	})
}

func TestPaperMatchesQueryRelevanceMeniscusQuery(t *testing.T) {
	withMockGlobalLLM(t, func(msc *mockLLMServiceClient, client *llm.Client) {
		raw := "Menicius reconstruction stratiges"
		payload := structuredResearchQueryPrep{
			CorrectedQuery: "meniscus reconstruction strategies",
			SearchQuery:    "meniscus reconstruction strategies",
			Domain:         "medicine",
			Intent:         "medical",
			Keywords:       []string{"meniscus", "reconstruction", "strategies"},
			Synonyms:       nil,
			SeedQueries:    []string{"meniscus reconstruction strategies"},
			AgendaQueries:  []string{"meniscus reconstruction systematic review"},
		}
		data, err := json.Marshal(payload)
		require.NoError(t, err)
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			return strings.Contains(req.Prompt, "Prepare this academic research query")
		})).Return(&llmv1.StructuredResponse{JsonResult: string(data)}, nil).Once()

		prepared := PrepareResearchQueryWithContext(context.Background(), raw, ResearchQueryPrepareOptions{
			LLMClient: client,
		})
		corrected := prepared.Corrected
		meniscusPaper := search.Paper{
			Title:    "Meniscus reconstruction strategies after knee injury",
			Abstract: "We review meniscus reconstruction outcomes and repair strategies.",
		}
		if !paperMatchesQueryRelevance(corrected, meniscusPaper) {
			t.Fatal("expected meniscus paper to match corrected query")
		}
		filtered := filterPapersByQueryRelevance(corrected, []search.Paper{meniscusPaper})
		if len(filtered) != 1 {
			t.Fatalf("expected corrected query to admit meniscus paper, got %d", len(filtered))
		}
		_, accepted := admitSearchPapersForQuery(nil, corrected, []search.Paper{meniscusPaper}, 10)
		if len(accepted) != 1 {
			t.Fatalf("expected admitted paper for corrected query, got %d", len(accepted))
		}
	})
}

func TestBuildTopicFocusedQueriesNonBiomedical(t *testing.T) {
	withMockGlobalLLM(t, func(msc *mockLLMServiceClient, _ *llm.Client) {
		query := "map evidence for open source research agents"
		seedStructuredPrep(t, msc, query, []string{
			"open source research agents",
			"research agents systematic review",
		})
		queries := BuildTopicFocusedQueries(query)
		joined := strings.Join(queries, " ")
		if strings.Contains(joined, "tissue engineering") || strings.Contains(joined, "clinical trial") {
			t.Fatalf("expected non-biomedical focused queries, got %v", queries)
		}
		if !strings.Contains(joined, "systematic review") {
			t.Fatalf("expected general research focused queries, got %v", queries)
		}
	})
}
