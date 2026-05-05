package wisdev

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/policy"
	internalsearch "github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type contextRecordingSessionStore struct {
	observedErr       error
	observedSessionID string
	getCalls          int
}

type gatewaySearchProvider struct {
	name  string
	query string
	opts  internalsearch.SearchOpts
}

func (p *gatewaySearchProvider) Name() string      { return p.name }
func (p *gatewaySearchProvider) Domains() []string { return nil }
func (p *gatewaySearchProvider) Healthy() bool     { return true }
func (p *gatewaySearchProvider) Tools() []string   { return nil }
func (p *gatewaySearchProvider) Search(ctx context.Context, query string, opts internalsearch.SearchOpts) ([]internalsearch.Paper, error) {
	p.query = query
	p.opts = opts
	return []internalsearch.Paper{{
		ID:            "mcp-paper-1",
		Title:         "MCP Core Retrieval",
		DOI:           "10.1000/core",
		Source:        p.name,
		SourceApis:    []string{p.name},
		Year:          2026,
		OpenAccessUrl: "https://example.test/paper",
	}}, nil
}

func (s *contextRecordingSessionStore) Get(ctx context.Context, sessionID string) (*AgentSession, error) {
	s.getCalls++
	s.observedErr = ctx.Err()
	s.observedSessionID = sessionID
	return nil, errSessionNotFound
}

func (s *contextRecordingSessionStore) Put(context.Context, *AgentSession, time.Duration) error {
	return nil
}

func (s *contextRecordingSessionStore) Delete(context.Context, string) error {
	return nil
}

func (s *contextRecordingSessionStore) List(context.Context, string) ([]*AgentSession, error) {
	return nil, nil
}

func TestAgentGateway_RuntimeMetadata(t *testing.T) {
	t.Run("Nil Runtime", func(t *testing.T) {
		gw := &AgentGateway{}
		meta := gw.RuntimeMetadata()
		assert.False(t, meta["enabled"].(bool))
		artifactSchema, ok := meta["artifactSchema"].(map[string]any)
		if assert.True(t, ok) {
			assert.Equal(t, ARTIFACT_SCHEMA_VERSION, artifactSchema["version"])
		}
	})

	t.Run("With Runtime", func(t *testing.T) {
		gw := &AgentGateway{
			ADKRuntime: &ADKRuntime{
				Config: DefaultADKRuntimeConfig(),
			},
		}
		gw.ADKRuntime.Config.A2A.Enabled = true
		gw.ADKRuntime.Config.Runtime.Name = "Test"

		meta := gw.RuntimeMetadata()
		assert.False(t, meta["enabled"].(bool))
		assert.True(t, meta["configured"].(bool))
		assert.False(t, meta["ready"].(bool))
		assert.Equal(t, "initializing", meta["status"])
		assert.NotNil(t, meta["a2aCard"])
		artifactSchema, ok := meta["artifactSchema"].(map[string]any)
		if assert.True(t, ok) {
			assert.Equal(t, ARTIFACT_SCHEMA_VERSION, artifactSchema["version"])
			assert.Contains(t, artifactSchema["bundles"], "citationBundle")
			assert.Contains(t, artifactSchema["legacyKeys"], "claimEvidenceTable")
		}
	})
}

func TestNewAgentGateway_InitializesQuestRuntime(t *testing.T) {
	gw := NewAgentGateway(nil, nil, nil)
	if gw == nil {
		t.Fatalf("expected gateway")
	}
	if gw.QuestRuntime == nil {
		t.Fatalf("expected quest runtime to be initialized")
	}
}

func TestAgentGateway_EnsureADKSession(t *testing.T) {
	gw := &AgentGateway{
		Store:        NewInMemorySessionStore(),
		PolicyConfig: policy.DefaultPolicyConfig(),
	}

	t.Run("New Session", func(t *testing.T) {
		sess := gw.ensureADKSession("", "query", "science")
		assert.NotEmpty(t, sess.SessionID)
		assert.Equal(t, "query", sess.OriginalQuery)
	})

	t.Run("Existing Session", func(t *testing.T) {
		existing := &AgentSession{SessionID: "s1", UserID: "u1"}
		gw.Store.Put(context.Background(), existing, 0)

		sess := gw.ensureADKSession("s1", "", "")
		assert.Equal(t, "u1", sess.UserID)
	})

	t.Run("Preserves Context Cancellation", func(t *testing.T) {
		store := &contextRecordingSessionStore{}
		gw := &AgentGateway{
			Store:        store,
			PolicyConfig: policy.DefaultPolicyConfig(),
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		sess := gw.ensureADKSessionWithContext(ctx, "cancelled-session", "query", "science")
		assert.Equal(t, "cancelled-session", sess.SessionID)
		assert.Equal(t, 1, store.getCalls)
		assert.Equal(t, "cancelled-session", store.observedSessionID)
		assert.ErrorIs(t, store.observedErr, context.Canceled)
	})

	t.Run("Generated Session Does Not Lookup Store", func(t *testing.T) {
		store := &contextRecordingSessionStore{}
		gw := &AgentGateway{
			Store:        store,
			PolicyConfig: policy.DefaultPolicyConfig(),
		}

		sess := gw.ensureADKSessionWithContext(context.Background(), "", "query", "science")
		assert.NotEmpty(t, sess.SessionID)
		assert.Equal(t, "query", sess.OriginalQuery)
		assert.Equal(t, 0, store.getCalls)
	})

	t.Run("Nil Store Creates Session", func(t *testing.T) {
		gw := &AgentGateway{PolicyConfig: policy.DefaultPolicyConfig()}
		sess := gw.ensureADKSessionWithContext(context.Background(), "nil-store-session", "query", "science")
		assert.Equal(t, "nil-store-session", sess.SessionID)
		assert.Equal(t, "query", sess.OriginalQuery)
	})
}

func TestAgentGateway_ExecuteADKAction(t *testing.T) {
	// Mock ParallelSearch
	originalSearch := ParallelSearch
	defer func() { ParallelSearch = originalSearch }()
	ParallelSearch = func(ctx context.Context, rdb redis.UniversalClient, query string, opts SearchOptions) (*MultiSourceResult, error) {
		return &MultiSourceResult{
			Papers:    []Source{{ID: "p1"}},
			TraceID:   "gateway-extra-trace",
			QueryUsed: query,
		}, nil
	}

	gw := &AgentGateway{
		Store:        NewInMemorySessionStore(),
		PolicyConfig: policy.DefaultPolicyConfig(),
	}
	tool := ToolDefinition{
		Name:            "search",
		ExecutionTarget: ExecutionTargetGoNative,
	}
	payload := map[string]any{"query": "test"}

	t.Run("Success", func(t *testing.T) {
		res, err := gw.ExecuteADKAction(context.Background(), tool, payload, nil)
		assert.NoError(t, err)
		assert.Equal(t, 1, res["count"])
		assert.Equal(t, "paperBundle.v1", res["contract"])
		papers, ok := res["papers"].([]any)
		if assert.True(t, ok) {
			first, ok := papers[0].(map[string]any)
			if assert.True(t, ok) {
				assert.Equal(t, "p1", first["id"])
			}
		}
	})
}

func TestAgentGateway_ExecuteADKAction_RetrievePapers(t *testing.T) {
	originalParallelSearch := ParallelSearch
	defer func() { ParallelSearch = originalParallelSearch }()
	ParallelSearch = func(ctx context.Context, rdb redis.UniversalClient, query string, opts SearchOptions) (*MultiSourceResult, error) {
		return &MultiSourceResult{
			Papers:              []Source{{ID: "p1", Title: "Paper One", Source: "semantic_scholar"}},
			TraceID:             "trace-gateway-1",
			QueryUsed:           query,
			RetrievalStrategies: []string{RetrievalStrategyLexicalBroad, RetrievalStrategySemanticFocus},
			RetrievalTrace: []map[string]any{
				{"strategy": RetrievalStrategyLexicalBroad, "resultCount": 1},
			},
		}, nil
	}

	gw := &AgentGateway{
		Store:        NewInMemorySessionStore(),
		PolicyConfig: policy.DefaultPolicyConfig(),
	}
	tool := ToolDefinition{
		Name:            "research.retrievePapers",
		ExecutionTarget: ExecutionTargetGoNative,
	}
	payload := map[string]any{
		"query":               "graph rag",
		"retrievalStrategies": []any{RetrievalStrategyLexicalBroad, RetrievalStrategySemanticFocus},
	}

	res, err := gw.ExecuteADKAction(context.Background(), tool, payload, nil)
	assert.NoError(t, err)
	assert.Equal(t, "trace-gateway-1", res["traceId"])
	assert.Equal(t, "graph rag", res["queryUsed"])
	assert.NotNil(t, res["retrievalTrace"])
	assert.NotNil(t, res["retrievalStrategies"])
	papers, ok := res["papers"].([]any)
	if assert.True(t, ok) {
		assert.Len(t, papers, 1)
	}
}

func TestAgentGatewayRetrievePapersUsesCoreMCPToolWhenRegistryConfigured(t *testing.T) {
	registry := internalsearch.NewProviderRegistry()
	provider := &gatewaySearchProvider{name: "openalex"}
	registry.Register(provider)

	gw := &AgentGateway{
		Store:          NewInMemorySessionStore(),
		PolicyConfig:   policy.DefaultPolicyConfig(),
		SearchRegistry: registry,
	}
	tool := ToolDefinition{
		Name:            ActionResearchRetrievePapers,
		ExecutionTarget: ExecutionTargetGoNative,
	}
	payload := map[string]any{
		"query":   "open-source mcp retrieval",
		"limit":   100,
		"sources": []any{"openalex", "openalex"},
		"traceId": "trace-core-mcp",
	}

	res, err := gw.ExecuteADKAction(context.Background(), tool, payload, nil)
	require.NoError(t, err)
	assert.Equal(t, "wisdev.mcp.paper_retrieval.v1", res["contract"])
	assert.Equal(t, internalsearch.ToolSearchPapersName, res["mcpTool"])
	assert.Equal(t, "wisdev_core_mcp_tool", res["retrievalBy"])
	assert.Equal(t, "open-source mcp retrieval", provider.query)
	assert.Equal(t, 50, provider.opts.Limit)
	assert.Equal(t, []string{"openalex"}, provider.opts.Sources)
	assert.NotNil(t, res["sourceAcquisition"])
	bundle, ok := res["paperBundle"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "wisdev.mcp.paper_retrieval.v1", bundle["contract"])
	assert.Equal(t, "wisdev_core_mcp_tool", bundle["retrievalBy"])
}

func TestRetrieveCanonicalPapersWithRegistryUsesCoreMCPTool(t *testing.T) {
	registry := internalsearch.NewProviderRegistry()
	provider := &gatewaySearchProvider{name: "semantic_scholar"}
	registry.Register(provider)

	papers, res, err := RetrieveCanonicalPapersWithRegistry(context.Background(), nil, registry, "registry canonical retrieval", 9)
	require.NoError(t, err)
	require.Len(t, papers, 1)
	assert.Equal(t, "registry canonical retrieval", provider.query)
	assert.Equal(t, 9, provider.opts.Limit)
	assert.Equal(t, "wisdev.mcp.paper_retrieval.v1", res["contract"])
	assert.Equal(t, internalsearch.ToolSearchPapersName, res["mcpTool"])
	assert.Equal(t, "wisdev_core_mcp_tool", res["retrievalBy"])
	bundle, ok := res["paperBundle"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "wisdev.mcp.paper_retrieval.v1", bundle["contract"])
	assert.Equal(t, internalsearch.ToolSearchPapersName, bundle["mcpTool"])
}

func TestAgentGateway_ExecuteADKAction_FullPaperActionsAndUnsupportedGoNative(t *testing.T) {
	originalParallelSearch := ParallelSearch
	defer func() { ParallelSearch = originalParallelSearch }()
	seenQueries := []string{}
	ParallelSearch = func(ctx context.Context, rdb redis.UniversalClient, query string, opts SearchOptions) (*MultiSourceResult, error) {
		seenQueries = append(seenQueries, query)
		return &MultiSourceResult{
			Papers:    []Source{{ID: "paper-" + query, Title: "Paper " + query}},
			TraceID:   "trace-" + query,
			QueryUsed: query,
		}, nil
	}

	gw := &AgentGateway{
		Store:        NewInMemorySessionStore(),
		PolicyConfig: policy.DefaultPolicyConfig(),
	}
	session := &AgentSession{OriginalQuery: "memory", CorrectedQuery: "memory"}

	retrieved, err := gw.ExecuteADKAction(context.Background(), ToolDefinition{
		Name:            ActionResearchFullPaperRetrieve,
		ExecutionTarget: ExecutionTargetGoNative,
	}, map[string]any{
		"query":       "memory",
		"planQueries": []any{"sleep memory", "memory"},
		"limit":       4,
	}, session)
	require.NoError(t, err)
	assert.Equal(t, "full_paper", retrieved["mode"])
	assert.Equal(t, "paperBundle.v1", retrieved["contract"])
	assert.NotEmpty(t, retrieved["queryTrajectory"])
	assert.Contains(t, seenQueries, "memory")
	assert.Contains(t, seenQueries, "sleep memory")

	preview, err := gw.ExecuteADKAction(context.Background(), ToolDefinition{
		Name:            ActionResearchFullPaperGatewayDispatch,
		ExecutionTarget: ExecutionTargetGoNative,
	}, map[string]any{
		"action":  "source_bundle_preview",
		"stageId": "source_review",
		"sources": []any{map[string]any{"id": "p1", "title": "Preview Paper"}},
	}, session)
	require.NoError(t, err)
	assert.Equal(t, true, preview["preview"])
	assert.Equal(t, "source_review", preview["stageId"])
	assert.Equal(t, 1, preview["count"])

	_, err = gw.ExecuteADKAction(context.Background(), ToolDefinition{
		Name:            "research.unknownGoNative",
		ExecutionTarget: ExecutionTargetGoNative,
	}, map[string]any{"query": "should not search"}, session)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported Go-native WisDev action")
}

func TestAgentGateway_ExecuteADKAction_AllRegisteredGoNativeTools(t *testing.T) {
	originalParallelSearch := ParallelSearch
	defer func() { ParallelSearch = originalParallelSearch }()
	ParallelSearch = func(ctx context.Context, rdb redis.UniversalClient, query string, opts SearchOptions) (*MultiSourceResult, error) {
		return &MultiSourceResult{
			Papers:              []Source{{ID: "paper-" + query, Title: "Paper " + query}},
			TraceID:             "trace-" + query,
			QueryUsed:           query,
			RetrievalStrategies: opts.RetrievalStrategies,
			RetrievalTrace:      []map[string]any{{"query": query, "count": 1}},
		}, nil
	}

	mockLLM := new(mockLLMServiceClient)
	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return req != nil && strings.Contains(req.Prompt, "Rank and verify these research findings")
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"results":[{"verified":true,"score":0.91,"report":"supported"}]}`}, nil).Maybe()
	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return req != nil && strings.Contains(req.Prompt, "Synthesize a comprehensive research report")
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"sections":[{"heading":"Answer","sentences":[{"text":"answer","evidenceIds":["p1"]}]}]}`}, nil).Maybe()

	client := llm.NewClient()
	client.SetClient(mockLLM)
	gw := &AgentGateway{
		Store:        NewInMemorySessionStore(),
		PolicyConfig: policy.DefaultPolicyConfig(),
		LLMClient:    client,
		Brain:        NewBrainCapabilities(client),
	}
	session := &AgentSession{OriginalQuery: "memory", CorrectedQuery: "memory"}

	payloadFor := func(action string) map[string]any {
		switch CanonicalizeWisdevAction(action) {
		case ActionResearchFullPaperGatewayDispatch:
			return map[string]any{
				"action":  "source_bundle_preview",
				"stageId": "source_review",
				"sources": []any{map[string]any{"id": "p1", "title": "Preview Paper"}},
			}
		case ActionResearchVerifyClaimsBatch:
			return map[string]any{
				"candidateOutputs": []any{map[string]any{"claim": "candidate claim"}},
				"sources":          []any{map[string]any{"id": "p1", "title": "Verifier Paper"}},
			}
		case ActionResearchSynthesizeAnswer:
			return map[string]any{
				"query":   "memory",
				"sources": []any{map[string]any{"id": "p1", "title": "Synthesis Paper"}},
			}
		default:
			return map[string]any{"query": "memory", "limit": 3}
		}
	}

	for _, tool := range NewToolRegistry().List() {
		if tool.ExecutionTarget != ExecutionTargetGoNative {
			continue
		}
		t.Run(tool.Name, func(t *testing.T) {
			result, err := gw.ExecuteADKAction(context.Background(), tool, payloadFor(tool.Name), session)
			require.NoError(t, err)
			require.NotNil(t, result)
			assert.NotContains(t, result, "error")
			switch CanonicalizeWisdevAction(tool.Name) {
			case ActionResearchRetrievePapers:
				assert.Equal(t, "paperBundle.v1", result["contract"])
			case ActionResearchFullPaperRetrieve:
				assert.Equal(t, "full_paper", result["mode"])
				assert.NotEmpty(t, result["queryTrajectory"])
			case ActionResearchFullPaperGatewayDispatch:
				assert.Equal(t, true, result["preview"])
			case ActionResearchVerifyClaimsBatch:
				assert.NotEmpty(t, result["results"])
			case ActionResearchSynthesizeAnswer:
				assert.NotEmpty(t, result["structuredAnswer"])
			default:
				t.Fatalf("registered Go-native tool lacks explicit assertion: %s", tool.Name)
			}
		})
	}
	mockLLM.AssertExpectations(t)
}
