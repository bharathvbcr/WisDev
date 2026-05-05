package wisdev

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
	"google.golang.org/genai"
)

type brainSlowVertexModelsClient struct {
	called atomic.Bool
}

func (m *brainSlowVertexModelsClient) GenerateContent(ctx context.Context, _ string, _ []*genai.Content, _ *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	m.called.Store(true)
	select {
	case <-time.After(2 * time.Second):
		return &genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{
					Content: &genai.Content{
						Parts: []*genai.Part{{Text: `{"values":["vertex_only"],"explanation":"vertex path should not run"}`}},
					},
				},
			},
		}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (m *brainSlowVertexModelsClient) EmbedContent(context.Context, string, []*genai.Content, *genai.EmbedContentConfig) (*genai.EmbedContentResponse, error) {
	return &genai.EmbedContentResponse{}, nil
}

func (m *brainSlowVertexModelsClient) GenerateImages(context.Context, string, string, *genai.GenerateImagesConfig) (*genai.GenerateImagesResponse, error) {
	return &genai.GenerateImagesResponse{}, nil
}

func setBrainUnexportedField(t *testing.T, target any, fieldName string, value any) {
	t.Helper()
	field := reflect.ValueOf(target).Elem().FieldByName(fieldName)
	require.Truef(t, field.IsValid(), "missing field %s", fieldName)
	reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().Set(reflect.ValueOf(value))
}

func assertBrainRecoverableStructuredPolicy(t *testing.T, req *llmv1.StructuredRequest) bool {
	t.Helper()
	if req == nil {
		return false
	}
	assertWisdevStructuredPromptHygiene(t, req.Prompt)
	return req.RequestClass == "standard" &&
		req.RetryProfile == "standard" &&
		req.ServiceTier == "standard" &&
		req.GetThinkingBudget() == 1024 &&
		req.LatencyBudgetMs > 0
}

func assertBrainHighValueStructuredPolicy(t *testing.T, req *llmv1.StructuredRequest) bool {
	t.Helper()
	if req == nil {
		return false
	}
	assertWisdevStructuredPromptHygiene(t, req.Prompt)
	return req.RequestClass == "structured_high_value" &&
		req.RetryProfile == "standard" &&
		req.ServiceTier == "priority" &&
		req.GetThinkingBudget() == -1 &&
		req.LatencyBudgetMs > 0
}

func TestBrainCapabilities_DecomposeTask(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	ctx := context.Background()
	query := "test query"
	domain := "science"

	expectedTasks := []ResearchTask{
		{ID: "1", Name: "task 1", Action: "search", DependsOnIDs: []string{}},
	}
	jsonResp, _ := json.Marshal(map[string]any{"tasks": expectedTasks})

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		assertWisdevStructuredPromptHygiene(t, req.Prompt)
		return req.Model != "" &&
			req.Prompt != "" &&
			assertBrainHighValueStructuredPolicy(t, req)
	})).Return(&llmv1.StructuredResponse{JsonResult: string(jsonResp)}, nil)

	tasks, err := caps.DecomposeTask(ctx, query, domain, "")
	assert.NoError(t, err)
	assert.Equal(t, expectedTasks, tasks)
	mockLLM.AssertExpectations(t)
}

func TestBrainCapabilities_DecomposeTaskRateLimitUsesFallback(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return req != nil &&
			req.RequestClass == "structured_high_value" &&
			req.ServiceTier == "priority"
	})).Return((*llmv1.StructuredResponse)(nil), errors.New("429 RESOURCE_EXHAUSTED")).Once()

	tasks, err := caps.DecomposeTask(context.Background(), "RLHF reinforcement learning", "machine learning", "")
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	assert.Equal(t, "search", tasks[0].Action)
	assert.Equal(t, "evaluate_evidence", tasks[1].Action)
	assert.Equal(t, []string{tasks[0].ID}, tasks[1].DependsOnIDs)
	assert.NotEmpty(t, tasks[0].Reason)
	mockLLM.AssertExpectations(t)
}

func TestBrainCapabilities_ProposeHypotheses(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	ctx := context.Background()
	query := "test query"
	intent := "discovery"

	expectedHypotheses := []Hypothesis{
		{Claim: "claim 1", Text: "claim 1", FalsifiabilityCondition: "cond 1", ConfidenceThreshold: 0.8},
	}
	jsonResp, _ := json.Marshal(map[string]any{"hypotheses": expectedHypotheses})
	expectedPrompt := appendWisdevStructuredOutputInstruction("Propose 3-5 hypotheses for query: test query. Intent: discovery.")

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		assertWisdevStructuredPromptHygiene(t, req.Prompt)
		return req != nil &&
			req.Prompt == expectedPrompt &&
			assertBrainHighValueStructuredPolicy(t, req)
	})).Return(&llmv1.StructuredResponse{JsonResult: string(jsonResp)}, nil)

	hypotheses, err := caps.ProposeHypotheses(ctx, query, intent, "")
	assert.NoError(t, err)
	assert.Equal(t, expectedHypotheses, hypotheses)
}

func TestBrainCapabilities_ProposeHypothesesRateLimitUsesFallback(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	ctx := context.Background()
	query := "RLHF reinforcement learning"
	intent := "discovery"

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return req != nil &&
			req.RequestClass == "structured_high_value" &&
			req.ServiceTier == "priority"
	})).Return((*llmv1.StructuredResponse)(nil), errors.New("429 RESOURCE_EXHAUSTED")).Once()

	hypotheses, err := caps.ProposeHypotheses(ctx, query, intent, "")
	require.NoError(t, err)
	require.Len(t, hypotheses, 1)
	assert.Equal(t, query, hypotheses[0].Claim)
	assert.Equal(t, query, hypotheses[0].Text)
	assert.Equal(t, query, hypotheses[0].Query)
	assert.Equal(t, intent, hypotheses[0].Category)
	assert.Equal(t, "candidate", hypotheses[0].Status)
	assert.NotEmpty(t, hypotheses[0].FalsifiabilityCondition)
	assert.Greater(t, hypotheses[0].UpdatedAt, int64(0))
	mockLLM.AssertExpectations(t)
}

func TestBrainCapabilities_GenerateHypotheses(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	ctx := context.Background()
	query := "test query"
	domain := "neuroscience"
	intent := "exploration"

	expectedHypotheses := []Hypothesis{
		{Claim: "candidate 1", Text: "candidate 1", FalsifiabilityCondition: "cond a", ConfidenceThreshold: 0.72},
	}
	jsonResp, _ := json.Marshal(map[string]any{"hypotheses": expectedHypotheses})
	expectedPrompt := appendWisdevStructuredOutputInstruction("Propose 3-5 hypotheses for query: test query. Intent: exploration.")

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		assertWisdevStructuredPromptHygiene(t, req.Prompt)
		return req != nil &&
			req.Model == llm.ResolveStandardModel() &&
			req.Prompt == expectedPrompt &&
			assertBrainHighValueStructuredPolicy(t, req)
	})).Return(&llmv1.StructuredResponse{JsonResult: string(jsonResp)}, nil).Once()

	hypotheses, err := caps.GenerateHypotheses(ctx, query, domain, intent, "")
	assert.NoError(t, err)
	assert.Equal(t, expectedHypotheses, hypotheses)
	mockLLM.AssertExpectations(t)
}

func TestBrainCapabilities_CoordinateReplan(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	ctx := context.Background()

	expectedTasks := []ResearchTask{{ID: "retry_1", Name: "retry"}}
	jsonResp, _ := json.Marshal(map[string]any{"tasks": expectedTasks})

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return req != nil &&
			assertBrainHighValueStructuredPolicy(t, req)
	})).Return(&llmv1.StructuredResponse{JsonResult: string(jsonResp)}, nil)

	tasks, err := caps.CoordinateReplan(ctx, "task_1", "timeout", nil, "")
	assert.NoError(t, err)
	assert.Equal(t, expectedTasks, tasks)
}

func TestBrainCapabilities_CoordinateReplanRateLimitUsesFallback(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return req != nil &&
			req.RequestClass == "structured_high_value" &&
			req.ServiceTier == "priority"
	})).Return((*llmv1.StructuredResponse)(nil), errors.New("RESOURCE_EXHAUSTED quota exceeded")).Once()

	tasks, err := caps.CoordinateReplan(context.Background(), "task_1", "timeout", map[string]any{"attempt": 1}, "")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, "retry_search", tasks[0].Action)
	assert.Equal(t, []string{"task_1"}, tasks[0].DependsOnIDs)
	assert.Equal(t, "timeout", tasks[0].Reason)
	mockLLM.AssertExpectations(t)
}

func TestBrainCapabilities_CoordinateReplanBypassesSlowVertexDirectAndUsesSidecar(t *testing.T) {
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")

	var captured llmv1.StructuredRequest
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/llm/structured-output", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"jsonResult": `{"tasks":[{"id":"retry_1","name":"Retry retrieval","action":"retry_search","reason":"Retry evidence collection","dependsOnIds":["step_1"]}]}`,
			"modelUsed":  "test-coordinate-replan-sidecar",
		}))
	}))
	defer llmServer.Close()
	t.Setenv("PYTHON_SIDECAR_HTTP_URL", llmServer.URL)

	client := llm.NewClientWithTimeout(500 * time.Millisecond)
	setBrainUnexportedField(t, client, "transport", "http-json")
	setBrainUnexportedField(t, client, "httpBaseURL", llmServer.URL)

	slowDirect := &brainSlowVertexModelsClient{}
	vertexClient := &llm.VertexClient{}
	setBrainUnexportedField(t, vertexClient, "client", slowDirect)
	setBrainUnexportedField(t, vertexClient, "backend", "vertex_ai")
	client.VertexDirect = vertexClient

	caps := NewBrainCapabilities(client)
	start := time.Now()
	tasks, err := caps.CoordinateReplan(context.Background(), "step_1", "timeout", map[string]any{"attempt": 1}, "")
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Equal(t, []ResearchTask{{
		ID:           "retry_1",
		Name:         "Retry retrieval",
		Action:       "retry_search",
		Reason:       "Retry evidence collection",
		DependsOnIDs: []string{"step_1"},
	}}, tasks)
	assert.Less(t, elapsed, time.Second)
	assert.False(t, slowDirect.called.Load())
	assert.Equal(t, llm.ResolveStandardModel(), captured.Model)
	assertWisdevStructuredPromptHygiene(t, captured.Prompt)
}

func TestBrainCapabilities_AssessResearchComplexity(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	ctx := context.Background()

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		assertWisdevStructuredPromptHygiene(t, req.Prompt)
		return req != nil &&
			req.Model == llm.ResolveLightModel() &&
			req.RequestClass == "light" &&
			req.RetryProfile == "conservative" &&
			req.ServiceTier == "standard" &&
			req.GetThinkingBudget() == 0 &&
			req.LatencyBudgetMs > 0
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"complexity":"HIGH"}`}, nil)

	complexity, err := caps.AssessResearchComplexity(ctx, "complex query")
	assert.NoError(t, err)
	assert.Equal(t, "high", complexity)
}

func TestBrainCapabilities_AssessResearchComplexityRateLimitUsesFallback(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return req != nil &&
			req.RequestClass == "light" &&
			req.ServiceTier == "standard"
	})).Return((*llmv1.StructuredResponse)(nil), errors.New("429 RESOURCE_EXHAUSTED")).Once()

	complexity, err := caps.AssessResearchComplexity(context.Background(), "short query")
	require.NoError(t, err)
	assert.Equal(t, "low", complexity)
	mockLLM.AssertExpectations(t)
}

func TestBrainCapabilities_AssessResearchComplexityInteractiveBypassesSlowVertexDirectAndUsesSidecar(t *testing.T) {
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")

	var captured llmv1.StructuredRequest
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/llm/structured-output", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"jsonResult": `{"complexity":"MEDIUM"}`,
			"modelUsed":  "test-complexity-sidecar",
		}))
	}))
	defer llmServer.Close()
	t.Setenv("PYTHON_SIDECAR_HTTP_URL", llmServer.URL)

	client := llm.NewClientWithTimeout(500 * time.Millisecond)
	setBrainUnexportedField(t, client, "transport", "http-json")
	setBrainUnexportedField(t, client, "httpBaseURL", llmServer.URL)

	slowDirect := &brainSlowVertexModelsClient{}
	vertexClient := &llm.VertexClient{}
	setBrainUnexportedField(t, vertexClient, "client", slowDirect)
	setBrainUnexportedField(t, vertexClient, "backend", "vertex_ai")
	client.VertexDirect = vertexClient

	caps := NewBrainCapabilities(client)
	start := time.Now()
	complexity, err := caps.AssessResearchComplexityInteractive(context.Background(), "sleep and memory")
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Equal(t, "medium", complexity)
	assert.Less(t, elapsed, time.Second)
	assert.False(t, slowDirect.called.Load())
	assert.Equal(t, llm.ResolveLightModel(), captured.Model)
	assertWisdevStructuredPromptHygiene(t, captured.Prompt)
}

func TestBrainCapabilities_GenerateSnowballQueries(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	ctx := context.Background()
	seedPapers := []Source{{Title: "Paper A"}, {Title: "Paper B"}}

	expectedQueries := []string{"query 1", "query 2"}
	jsonResp, _ := json.Marshal(map[string]any{"queries": expectedQueries})

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return assertBrainRecoverableStructuredPolicy(t, req)
	})).Return(&llmv1.StructuredResponse{JsonResult: string(jsonResp)}, nil)

	queries, err := caps.GenerateSnowballQueries(ctx, seedPapers, "")
	assert.NoError(t, err)
	assert.Equal(t, expectedQueries, queries)
}

func TestBrainCapabilities_GenerateSnowballQueriesRateLimitUsesFallback(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return assertBrainRecoverableStructuredPolicy(t, req)
	})).Return((*llmv1.StructuredResponse)(nil), errors.New("RESOURCE_EXHAUSTED quota exceeded")).Once()

	queries, err := caps.GenerateSnowballQueries(context.Background(), []Source{{Title: "Paper A"}, {Title: "Paper B"}}, "")
	require.NoError(t, err)
	assert.Equal(t, []string{"Paper A related evidence", "Paper B related evidence"}, queries)
	mockLLM.AssertExpectations(t)
}

func TestBrainCapabilities_SnowballCitations(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	ctx := context.Background()
	seedPapers := []Source{{Title: "Paper A"}}

	expectedQueries := []string{"query 1"}
	jsonResp, _ := json.Marshal(map[string]any{"queries": expectedQueries})

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return assertBrainRecoverableStructuredPolicy(t, req)
	})).Return(&llmv1.StructuredResponse{JsonResult: string(jsonResp)}, nil)

	results, err := caps.SnowballCitations(ctx, seedPapers, "")
	assert.NoError(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, "query 1", results[0].Title)
}

func TestBrainCapabilities_VerifyCitations(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	ctx := context.Background()

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return assertBrainRecoverableStructuredPolicy(t, req)
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"validCount":1,"issues":["duplicate DOI","missing DOI for P3"]}`}, nil)

	papers := []Source{
		{ID: "p1", Title: "P1", DOI: "10.1000/test-1"},
		{ID: "p2", Title: "P2", DOI: "10.1000/test-1"},
		{ID: "p3", Title: "P3"},
	}
	result, err := caps.VerifyCitations(ctx, papers, "")
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, float64(1), result["validCount"])
	assert.NotEmpty(t, result["issues"])
}

func TestBrainCapabilities_VerifyCitationsRateLimitUsesFallback(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return assertBrainRecoverableStructuredPolicy(t, req)
	})).Return((*llmv1.StructuredResponse)(nil), errors.New("429 RESOURCE_EXHAUSTED")).Once()

	result, err := caps.VerifyCitations(context.Background(), []Source{
		{ID: "p1", Title: "P1", DOI: "10.1000/test"},
		{ID: "p2", Title: "P2", DOI: "10.1000/test"},
	}, "")
	require.NoError(t, err)
	assert.Equal(t, true, result["degraded"])
	assert.Equal(t, float64(1), result["validCount"])
	assert.NotEmpty(t, result["issues"])
	mockLLM.AssertExpectations(t)
}

func TestBrainCapabilities_ResolveCanonicalCitations(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)
	ctx := context.Background()

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return assertBrainRecoverableStructuredPolicy(t, req)
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"resolved":[{"id":"p1","canonicalId":"canonical-1"},{"id":"p2","canonicalId":"canonical-2"}]}`}, nil)

	result, err := caps.ResolveCanonicalCitations(ctx, []Source{
		{Title: "P1", DOI: "10.1000/test-1", Year: 2024},
		{Title: "P2", ArxivID: "2401.12345", Year: 2024},
	}, "")
	assert.NoError(t, err)
	assert.NotNil(t, result)
	resolved, ok := result["resolved"].([]any)
	assert.True(t, ok)
	assert.Len(t, resolved, 2)
}

func TestBrainCapabilities_ResolveCanonicalCitationsRateLimitUsesFallback(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return assertBrainRecoverableStructuredPolicy(t, req)
	})).Return((*llmv1.StructuredResponse)(nil), errors.New("RESOURCE_EXHAUSTED quota exceeded")).Once()

	result, err := caps.ResolveCanonicalCitations(context.Background(), []Source{
		{ID: "p1", Title: "P1", DOI: "10.1000/test-1"},
		{ID: "p2", Title: "P2", ArxivID: "2401.12345"},
	}, "")
	require.NoError(t, err)
	assert.Equal(t, true, result["degraded"])
	resolved, ok := result["resolved"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, resolved, 2)
	assert.Equal(t, "10.1000/test-1", resolved[0]["canonicalId"])
	assert.Equal(t, "2401.12345", resolved[1]["canonicalId"])
	mockLLM.AssertExpectations(t)
}

func TestBrainCapabilities_BuildClaimEvidenceTable(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	ctx := context.Background()

	expectedResult := map[string]any{"table": "| Claim | Evidence |", "rowCount": float64(1)}
	jsonResp, _ := json.Marshal(expectedResult)

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return assertBrainRecoverableStructuredPolicy(t, req)
	})).Return(&llmv1.StructuredResponse{JsonResult: string(jsonResp)}, nil)

	result, err := caps.BuildClaimEvidenceTable(ctx, "query", nil, "")
	assert.NoError(t, err)
	assert.Equal(t, "| Claim | Evidence |", result["table"])
}

func TestBrainCapabilities_BuildClaimEvidenceTableRateLimitUsesFallback(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return assertBrainRecoverableStructuredPolicy(t, req)
	})).Return((*llmv1.StructuredResponse)(nil), errors.New("429 RESOURCE_EXHAUSTED")).Once()

	result, err := caps.BuildClaimEvidenceTable(context.Background(), "query", []Source{{ID: "p1", Title: "P1"}}, "")
	require.NoError(t, err)
	assert.Equal(t, true, result["degraded"])
	assert.Equal(t, float64(1), result["rowCount"])
	assert.Contains(t, result["table"], "P1")
	mockLLM.AssertExpectations(t)
}

func TestBrainCapabilities_GenerateThoughts(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	ctx := context.Background()

	expectedResult := map[string]any{
		"branches": []any{
			map[string]any{
				"hypothesis": "thinking...",
				"nodes": []any{
					map[string]any{
						"label":         "branch-one",
						"reasoning":     "thinking...",
						"search_weight": 0.9,
					},
				},
			},
		},
		"confidence": 0.9,
	}
	jsonResp, _ := json.Marshal(expectedResult)

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		assertWisdevStructuredPromptHygiene(t, req.Prompt)
		return req != nil &&
			req.RequestClass == "standard" &&
			req.RetryProfile == "standard" &&
			req.ServiceTier == "standard" &&
			req.GetThinkingBudget() == 1024
	})).Return(&llmv1.StructuredResponse{JsonResult: string(jsonResp)}, nil)

	result, err := caps.GenerateThoughts(ctx, nil, "")
	assert.NoError(t, err)
	branches, ok := result["branches"].([]any)
	assert.True(t, ok)
	if assert.Len(t, branches, 1) {
		branch, ok := branches[0].(map[string]any)
		assert.True(t, ok)
		nodes, ok := branch["nodes"].([]any)
		assert.True(t, ok)
		if assert.Len(t, nodes, 1) {
			node, ok := nodes[0].(map[string]any)
			assert.True(t, ok)
			assert.Equal(t, "branch-one", node["label"])
			assert.Equal(t, "thinking...", node["reasoning"])
		}
	}
}

func TestBrainCapabilities_GenerateThoughtsRateLimitUsesFallback(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return req != nil &&
			req.RequestClass == "standard" &&
			req.ServiceTier == "standard"
	})).Return((*llmv1.StructuredResponse)(nil), errors.New("429 RESOURCE_EXHAUSTED")).Once()

	result, err := caps.GenerateThoughts(context.Background(), map[string]any{"query": "RLHF"}, "")
	require.NoError(t, err)
	assert.Equal(t, true, result["degraded"])
	assert.Contains(t, result["thoughts"], "RLHF")
	branches, ok := result["branches"].([]any)
	require.True(t, ok)
	require.Len(t, branches, 1)
	mockLLM.AssertExpectations(t)
}

func TestBrainCapabilities_GenerateThoughtsNormalizesLegacyThoughts(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	ctx := context.Background()

	expectedResult := map[string]any{"thoughts": "thinking...", "confidence": 0.9}
	jsonResp, _ := json.Marshal(expectedResult)

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		assertWisdevStructuredPromptHygiene(t, req.Prompt)
		return req != nil &&
			req.RequestClass == "standard" &&
			req.RetryProfile == "standard" &&
			req.ServiceTier == "standard" &&
			req.GetThinkingBudget() == 1024
	})).Return(&llmv1.StructuredResponse{JsonResult: string(jsonResp)}, nil)

	result, err := caps.GenerateThoughts(ctx, nil, "")
	assert.NoError(t, err)
	assert.Equal(t, "thinking...", result["thoughts"])
	branches, ok := result["branches"].([]any)
	assert.True(t, ok)
	if assert.Len(t, branches, 1) {
		branch, ok := branches[0].(map[string]any)
		assert.True(t, ok)
		nodes, ok := branch["nodes"].([]any)
		assert.True(t, ok)
		if assert.Len(t, nodes, 1) {
			node, ok := nodes[0].(map[string]any)
			assert.True(t, ok)
			assert.Equal(t, "thinking...", node["label"])
		}
	}
}

func TestBrainCapabilities_DetectContradictions(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	ctx := context.Background()

	expectedResult := map[string]any{"summary": "no contradictions"}
	jsonResp, _ := json.Marshal(expectedResult)

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return assertBrainRecoverableStructuredPolicy(t, req)
	})).Return(&llmv1.StructuredResponse{JsonResult: string(jsonResp)}, nil)

	result, err := caps.DetectContradictions(ctx, nil, "")
	assert.NoError(t, err)
	assert.Equal(t, "no contradictions", result["summary"])
}

func TestBrainCapabilities_DetectContradictionsRateLimitUsesFallback(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return assertBrainRecoverableStructuredPolicy(t, req)
	})).Return((*llmv1.StructuredResponse)(nil), errors.New("429 RESOURCE_EXHAUSTED")).Once()

	result, err := caps.DetectContradictions(context.Background(), []Source{{ID: "p1"}}, "")
	require.NoError(t, err)
	assert.Equal(t, true, result["degraded"])
	contradictions, ok := result["contradictions"].([]any)
	require.True(t, ok)
	assert.Empty(t, contradictions)
	mockLLM.AssertExpectations(t)
}

func TestBrainCapabilities_VerifyReasoningPaths(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)
	ctx := context.Background()

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return assertBrainRecoverableStructuredPolicy(t, req)
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"results":[{"branchId":"branch-1","verified":true,"score":0.9},{"branchId":"branch-2","verified":false,"score":0.4}]}`}, nil)

	result, err := caps.VerifyReasoningPaths(ctx, []map[string]any{
		{"id": "branch-1", "supportScore": 0.9, "findings": []any{
			map[string]any{"claim": "supported", "sourceId": "paper-1", "snippet": "primary support"},
			map[string]any{"claim": "supported", "sourceId": "paper-2", "snippet": "replication support"},
		}},
		{"id": "branch-2", "supportScore": 0.4},
	}, "")
	assert.NoError(t, err)
	results, ok := result["results"].([]any)
	assert.True(t, ok)
	if assert.Len(t, results, 2) {
		b1 := results[0].(map[string]any)
		b2 := results[1].(map[string]any)
		assert.Equal(t, true, b1["verified"])
		assert.Equal(t, false, b2["verified"])
	}
}

func TestBrainCapabilities_VerifyReasoningPathsRateLimitUsesFallback(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return assertBrainRecoverableStructuredPolicy(t, req)
	})).Return((*llmv1.StructuredResponse)(nil), errors.New("429 RESOURCE_EXHAUSTED")).Once()

	result, err := caps.VerifyReasoningPaths(context.Background(), []map[string]any{
		{"id": "branch-1", "findings": []any{map[string]any{"sourceId": "paper-1"}}},
	}, "")
	require.NoError(t, err)
	assert.Equal(t, true, result["degraded"])
	assert.Equal(t, true, result["readyForSynthesis"])
	results, ok := result["results"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, results, 1)
	assert.Equal(t, "branch-1", results[0]["branchId"])
	assert.Equal(t, true, results[0]["verified"])
	mockLLM.AssertExpectations(t)
}

func TestBrainCapabilities_VerifyReasoningPathsRejectsUnsupportedBranches(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)
	ctx := context.Background()

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return assertBrainRecoverableStructuredPolicy(t, req)
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"results":[{"branchId":"b1","verified":false,"score":0.2},{"branchId":"b2","verified":false,"score":0.3}]}`}, nil)

	result, err := caps.VerifyReasoningPaths(ctx, []map[string]any{
		{"supportScore": 0.2, "findings": []any{}},
		{"supportScore": 0.7, "contradictionCount": 1, "findings": []any{map[string]any{"claim": "conflicted"}}},
	}, "")
	assert.NoError(t, err)
	results, ok := result["results"].([]any)
	assert.True(t, ok)
	if assert.Len(t, results, 2) {
		assert.Equal(t, false, results[0].(map[string]any)["verified"])
		assert.Equal(t, false, results[1].(map[string]any)["verified"])
	}
}

func TestBrainCapabilities_VerifyReasoningPathsHonorsMinimumEvidenceAndSources(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)
	ctx := context.Background()

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return assertBrainRecoverableStructuredPolicy(t, req)
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"results":[{"branchId":"branch-needs-triangulation","verified":false,"score":0.5}]}`}, nil)

	result, err := caps.VerifyReasoningPaths(ctx, []map[string]any{
		{
			"id":                   "branch-needs-triangulation",
			"supportScore":         0.92,
			"minimumEvidenceCount": 2,
			"minimumSourceCount":   2,
			"findings": []any{
				map[string]any{"claim": "supported by one source", "sourceId": "paper-1", "snippet": "direct support"},
			},
		},
	}, "")

	assert.NoError(t, err)
	results, ok := result["results"].([]any)
	assert.True(t, ok)
	if assert.Len(t, results, 1) {
		assert.Equal(t, false, results[0].(map[string]any)["verified"])
	}
}

func TestBrainCapabilities_VerifyClaims(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	ctx := context.Background()

	expectedResult := map[string]any{"verified": true, "report": "all good"}
	jsonResp, _ := json.Marshal(expectedResult)

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return assertBrainRecoverableStructuredPolicy(t, req)
	})).Return(&llmv1.StructuredResponse{JsonResult: string(jsonResp)}, nil)

	result, err := caps.VerifyClaims(ctx, "text", nil, "")
	assert.NoError(t, err)
	assert.True(t, result["verified"].(bool))
}

func TestBrainCapabilities_VerifyClaimsRateLimitUsesFallback(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return assertBrainRecoverableStructuredPolicy(t, req)
	})).Return((*llmv1.StructuredResponse)(nil), errors.New("RESOURCE_EXHAUSTED quota exceeded")).Once()

	result, err := caps.VerifyClaims(context.Background(), "claim text", []Source{{ID: "p1"}}, "")
	require.NoError(t, err)
	assert.Equal(t, false, result["verified"])
	assert.Equal(t, true, result["degraded"])
	assert.Contains(t, result["report"], "claim text")
	mockLLM.AssertExpectations(t)
}

func TestBrainCapabilities_VerifyClaimsBatchRateLimitUsesFallback(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return req != nil &&
			req.RequestClass == "standard" &&
			req.ServiceTier == "standard"
	})).Return((*llmv1.StructuredResponse)(nil), errors.New("429 RESOURCE_EXHAUSTED")).Once()

	result, err := caps.VerifyClaimsBatchInteractive(context.Background(), []map[string]any{
		{"id": "claim-1", "claim": "unsupported"},
	}, []Source{{ID: "paper-1", Title: "P1"}}, "")
	require.NoError(t, err)
	assert.Equal(t, true, result["degraded"])
	results, ok := result["results"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, results, 1)
	assert.Equal(t, "claim-1", results[0]["id"])
	assert.Equal(t, false, results[0]["verified"])
	mockLLM.AssertExpectations(t)
}

func TestBrainCapabilities_SystematicReviewPrisma(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	ctx := context.Background()

	expectedResult := map[string]any{"review_text": "PRISMA review"}
	jsonResp, _ := json.Marshal(expectedResult)

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return assertBrainRecoverableStructuredPolicy(t, req)
	})).Return(&llmv1.StructuredResponse{JsonResult: string(jsonResp)}, nil)

	result, err := caps.SystematicReviewPrisma(ctx, "query", nil, "")
	assert.NoError(t, err)
	assert.Equal(t, "PRISMA review", result["review_text"])
}

func TestBrainCapabilities_SystematicReviewPrismaRateLimitUsesFallback(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return assertBrainRecoverableStructuredPolicy(t, req)
	})).Return((*llmv1.StructuredResponse)(nil), errors.New("429 RESOURCE_EXHAUSTED")).Once()

	result, err := caps.SystematicReviewPrisma(context.Background(), "query", []Source{{ID: "p1"}, {ID: "p2"}}, "")
	require.NoError(t, err)
	assert.Equal(t, true, result["degraded"])
	assert.Equal(t, float64(2), result["records_identified"])
	assert.Equal(t, float64(2), result["studies_included"])
	mockLLM.AssertExpectations(t)
}

func TestBrainCapabilities_EnhanceAcademicQuery(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	ctx := context.Background()

	mockLLM.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
		assert.NotContains(t, req.Prompt, "Return ONLY")
		return req != nil &&
			req.Model == llm.ResolveLightModel() &&
			req.RequestClass == "light" &&
			req.RetryProfile == "conservative" &&
			req.ServiceTier == "standard" &&
			req.GetThinkingBudget() == 0 &&
			req.LatencyBudgetMs > 0
	})).Return(&llmv1.GenerateResponse{Text: " enhanced query "}, nil)

	enhanced, err := caps.EnhanceAcademicQuery(ctx, "query", "")
	assert.NoError(t, err)
	assert.Equal(t, "enhanced query", enhanced)
}

func TestBrainCapabilities_EnhanceAcademicQueryRateLimitUsesFallback(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	mockLLM.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
		return req != nil &&
			req.RequestClass == "light" &&
			req.ServiceTier == "standard"
	})).Return((*llmv1.GenerateResponse)(nil), errors.New("429 RESOURCE_EXHAUSTED")).Once()

	enhanced, err := caps.EnhanceAcademicQuery(context.Background(), " original query ", "")
	require.NoError(t, err)
	assert.Equal(t, "original query", enhanced)
	mockLLM.AssertExpectations(t)
}

func TestBrainCapabilities_SelectPrimarySource(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	ctx := context.Background()

	expectedResult := map[string]any{"primarySourceId": "id1", "reason": "best"}
	jsonResp, _ := json.Marshal(expectedResult)

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return assertBrainRecoverableStructuredPolicy(t, req)
	})).Return(&llmv1.StructuredResponse{JsonResult: string(jsonResp)}, nil)

	result, err := caps.SelectPrimarySource(ctx, "query", nil, "")
	assert.NoError(t, err)
	assert.Equal(t, "id1", result["primarySourceId"])
}

func TestBrainCapabilities_SelectPrimarySourceRateLimitUsesFallback(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return assertBrainRecoverableStructuredPolicy(t, req)
	})).Return((*llmv1.StructuredResponse)(nil), errors.New("RESOURCE_EXHAUSTED quota exceeded")).Once()

	result, err := caps.SelectPrimarySource(context.Background(), "query", []Source{{ID: "p1", Title: "P1"}}, "")
	require.NoError(t, err)
	assert.Equal(t, "p1", result["primarySourceId"])
	assert.Equal(t, true, result["degraded"])
	mockLLM.AssertExpectations(t)
}

func TestBrainCapabilities_AskFollowUpIfAmbiguous(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	ctx := context.Background()

	expectedResult := map[string]any{"isAmbiguous": true, "question": "what do you mean?"}
	jsonResp, _ := json.Marshal(expectedResult)

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		assertWisdevStructuredPromptHygiene(t, req.Prompt)
		return req != nil && req.RequestClass == "standard" && req.ServiceTier == "standard"
	})).Return(&llmv1.StructuredResponse{JsonResult: string(jsonResp)}, nil)

	result, err := caps.AskFollowUpIfAmbiguous(ctx, "ambiguous query", "")
	assert.NoError(t, err)
	assert.True(t, result["isAmbiguous"].(bool))
}

func TestBrainCapabilities_AskFollowUpIfAmbiguousRateLimitUsesFallback(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return req != nil &&
			req.RequestClass == "standard" &&
			req.ServiceTier == "standard"
	})).Return((*llmv1.StructuredResponse)(nil), errors.New("429 RESOURCE_EXHAUSTED")).Once()

	result, err := caps.AskFollowUpIfAmbiguous(context.Background(), "query", "")
	require.NoError(t, err)
	assert.Equal(t, false, result["isAmbiguous"])
	assert.Equal(t, true, result["degraded"])
	mockLLM.AssertExpectations(t)
}

func TestBrainCapabilities_SynthesizeAnswer(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	ctx := context.Background()
	papers := []Source{{Title: "P1", Summary: "S1"}}

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return req != nil &&
			req.Model == llm.ResolveHeavyModel() &&
			req.RequestClass == "heavy" &&
			req.RetryProfile == "standard" &&
			req.ServiceTier == "priority" &&
			req.GetThinkingBudget() == 8192 &&
			req.LatencyBudgetMs > 0
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"sections":[{"heading":"Findings","sentences":[{"text":"final answer","evidenceIds":[]}]}]}`}, nil)

	answer, err := caps.SynthesizeAnswer(ctx, "query", papers, "")
	assert.NoError(t, err)
	require.NotNil(t, answer)
	assert.Equal(t, "## Findings\n\nfinal answer", answer.PlainText)
	require.Len(t, answer.Sections, 1)
	assert.Equal(t, "Findings", answer.Sections[0].Heading)
}

func TestBrainCapabilities_SynthesizeAnswerRateLimitUsesFallback(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	papers := []Source{{ID: "paper-1", Title: "P1", Summary: "S1"}}
	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return req != nil &&
			req.RequestClass == "heavy" &&
			req.ServiceTier == "priority"
	})).Return((*llmv1.StructuredResponse)(nil), errors.New("429 RESOURCE_EXHAUSTED")).Once()

	answer, err := caps.SynthesizeAnswer(context.Background(), "query", papers, "")
	require.NoError(t, err)
	require.NotNil(t, answer)
	require.Len(t, answer.Sections, 1)
	assert.Equal(t, "Evidence Summary", answer.Sections[0].Heading)
	require.Len(t, answer.Sections[0].Sentences, 1)
	assert.Equal(t, []string{"paper-1"}, answer.Sections[0].Sentences[0].EvidenceIDs)
	assert.False(t, answer.Sections[0].Sentences[0].Unsupported)
	assert.Contains(t, answer.PlainText, "P1 reports: S1")
	mockLLM.AssertExpectations(t)
}

func TestSuggestQuestionValues_HallucinatedOptionsReturnError(t *testing.T) {
	// When the LLM returns values that don't appear in the allowed option set,
	// SuggestQuestionValues must return a non-nil error (not nil + empty slice)
	// so callers can fall back to their heuristic path.
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	ctx := context.Background()
	options := []string{"focused", "comprehensive", "exhaustive"}

	// LLM returns a value not in the allowed set.
	jsonResp, _ := json.Marshal(map[string]any{
		"values":      []string{"hallucinated_option"},
		"explanation": "this option does not exist",
	})
	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		assertWisdevStructuredPromptHygiene(t, req.Prompt)
		return req != nil &&
			req.RequestClass == "light" &&
			req.RetryProfile == "conservative" &&
			req.ServiceTier == "standard"
	})).Return(&llmv1.StructuredResponse{JsonResult: string(jsonResp)}, nil)

	values, _, err := caps.SuggestQuestionValues(ctx, "my query", "q2_scope", "Scope", options, 1, "")
	assert.Error(t, err, "should return an error when LLM returns no valid options")
	assert.Empty(t, values)
}

func TestSuggestQuestionValues_CaseInsensitiveMatch(t *testing.T) {
	// LLM may return values with different casing than what is stored.
	// They should still be accepted and normalised to the canonical stored value.
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	ctx := context.Background()
	options := []string{"focused", "comprehensive", "exhaustive"}

	jsonResp, _ := json.Marshal(map[string]any{
		"values":      []string{"Focused"},
		"explanation": "best match",
	})
	mockLLM.On("StructuredOutput", mock.Anything, mock.Anything).Return(&llmv1.StructuredResponse{JsonResult: string(jsonResp)}, nil)

	values, explanation, err := caps.SuggestQuestionValues(ctx, "my query", "q2_scope", "Scope", options, 1, "")
	assert.NoError(t, err)
	assert.Equal(t, []string{"focused"}, values, "should return the canonical lowercase value")
	assert.NotEmpty(t, explanation)
}

func TestSuggestQuestionValues_RateLimitUsesFallback(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	options := []string{"focused", "comprehensive", "exhaustive"}
	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return req != nil &&
			req.RequestClass == "light" &&
			req.ServiceTier == "standard"
	})).Return((*llmv1.StructuredResponse)(nil), errors.New("429 RESOURCE_EXHAUSTED")).Once()

	values, explanation, err := caps.SuggestQuestionValues(context.Background(), "my query", "q2_scope", "Scope", options, 2, "")
	require.NoError(t, err)
	assert.Equal(t, []string{"focused", "comprehensive"}, values)
	assert.Contains(t, explanation, "provider rate limiting")
	mockLLM.AssertExpectations(t)
}

func TestSuggestQuestionValues_BypassesSlowVertexDirectAndUsesSidecar(t *testing.T) {
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")

	var captured llmv1.StructuredRequest
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/llm/structured-output", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"jsonResult": `{"values":["focused"],"explanation":"best match"}`,
			"modelUsed":  "test-suggest-values-sidecar",
		}))
	}))
	defer llmServer.Close()
	t.Setenv("PYTHON_SIDECAR_HTTP_URL", llmServer.URL)

	client := llm.NewClientWithTimeout(500 * time.Millisecond)
	setBrainUnexportedField(t, client, "transport", "http-json")
	setBrainUnexportedField(t, client, "httpBaseURL", llmServer.URL)

	slowDirect := &brainSlowVertexModelsClient{}
	vertexClient := &llm.VertexClient{}
	setBrainUnexportedField(t, vertexClient, "client", slowDirect)
	setBrainUnexportedField(t, vertexClient, "backend", "vertex_ai")
	client.VertexDirect = vertexClient

	caps := NewBrainCapabilities(client)
	start := time.Now()
	values, explanation, err := caps.SuggestQuestionValues(
		context.Background(),
		"my query",
		"q2_scope",
		"Scope",
		[]string{"focused", "comprehensive", "exhaustive"},
		1,
		"",
	)
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Equal(t, []string{"focused"}, values)
	assert.Equal(t, "best match", explanation)
	assert.Less(t, elapsed, time.Second)
	assert.False(t, slowDirect.called.Load())
	assert.Equal(t, llm.ResolveLightModel(), captured.Model)
	assertWisdevStructuredPromptHygiene(t, captured.Prompt)
}

func TestSuggestQuestionValuesTimeoutCoversWarmStructuredResponses(t *testing.T) {
	assert.GreaterOrEqual(t, questionValueSuggestionTimeout, 15*time.Second)
}

func TestSuggestQuestionValues_EmptyQueryOrOptions(t *testing.T) {
	caps := NewBrainCapabilities(nil)
	ctx := context.Background()

	_, _, err := caps.SuggestQuestionValues(ctx, "", "q1_domain", "Domain", []string{"cs", "medicine"}, 1, "")
	assert.Error(t, err, "empty query should return an error")

	_, _, err = caps.SuggestQuestionValues(ctx, "some query", "q1_domain", "Domain", []string{}, 1, "")
	assert.Error(t, err, "empty options should return an error")
}

func TestBrainCapabilities_CritiqueEvidenceSetRateLimitUsesFallback(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return req != nil &&
			req.RequestClass == "standard" &&
			req.ServiceTier == "standard"
	})).Return((*llmv1.StructuredResponse)(nil), errors.New("RESOURCE_EXHAUSTED quota exceeded")).Once()

	analysis, err := caps.CritiqueEvidenceSet(context.Background(), "RLHF", []EvidenceItem{{PaperTitle: "P1", Claim: "c1"}}, "")
	require.NoError(t, err)
	require.NotNil(t, analysis)
	assert.False(t, analysis.Sufficient)
	assert.Contains(t, analysis.MissingAspects, "model_critique_unavailable")
	mockLLM.AssertExpectations(t)
}

func TestBrainCapabilities_JudgeQuestExperienceRateLimitUsesFallback(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	caps := NewBrainCapabilities(client)

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return assertBrainRecoverableStructuredPolicy(t, req)
	})).Return((*llmv1.StructuredResponse)(nil), errors.New("429 RESOURCE_EXHAUSTED")).Once()

	output, err := caps.JudgeQuestExperience(context.Background(), &ResearchQuest{
		Query:          "RLHF",
		AcceptedClaims: []EvidenceFinding{{Claim: "supported"}},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, output)
	assert.Equal(t, TrajectoryOutcomeSuccess, output.Outcome)
	assert.Contains(t, output.FailureFactors, "model_judge_unavailable")
	mockLLM.AssertExpectations(t)
}
