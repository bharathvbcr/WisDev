package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genai"
)

type structuredRequestCapture struct {
	Prompt          string `json:"prompt"`
	Model           string `json:"model"`
	RequestClass    string `json:"requestClass"`
	RetryProfile    string `json:"retryProfile"`
	ServiceTier     string `json:"serviceTier"`
	LatencyBudgetMs int32  `json:"latencyBudgetMs"`
}

type slowVertexModelsClient struct {
	called atomic.Bool
}

func (m *slowVertexModelsClient) GenerateContent(ctx context.Context, _ string, _ []*genai.Content, _ *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	m.called.Store(true)
	select {
	case <-time.After(2 * time.Second):
		return &genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{
					Content: &genai.Content{
						Parts: []*genai.Part{{Text: `{"needsFollowUp":true,"followUpQuestion":{"id":"vertex_follow_up","question":"Vertex direct path should not run"}}`}},
					},
				},
			},
		}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (m *slowVertexModelsClient) EmbedContent(context.Context, string, []*genai.Content, *genai.EmbedContentConfig) (*genai.EmbedContentResponse, error) {
	return &genai.EmbedContentResponse{}, nil
}

func (m *slowVertexModelsClient) GenerateImages(context.Context, string, string, *genai.GenerateImagesConfig) (*genai.GenerateImagesResponse, error) {
	return &genai.GenerateImagesResponse{}, nil
}

func setUnexportedField(t *testing.T, target any, fieldName string, value any) {
	t.Helper()
	field := reflect.ValueOf(target).Elem().FieldByName(fieldName)
	require.Truef(t, field.IsValid(), "missing field %s", fieldName)
	reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().Set(reflect.ValueOf(value))
}

func assertStructuredPromptHygiene(t *testing.T, prompt string) {
	t.Helper()
	assert.Contains(t, prompt, structuredOutputSchemaInstruction)
	assert.NotContains(t, prompt, "Return JSON only")
	assert.NotContains(t, prompt, "Return strict JSON")
}

func TestBuildPlanRevisionMessage(t *testing.T) {
	msg := buildPlanRevisionMessage("task1", "fail")
	assert.Contains(t, msg, "task1")
	assert.Contains(t, msg, "fail")

	msg2 := buildPlanRevisionMessage("", "reason")
	assert.Contains(t, msg2, "reason")
}

func TestBuildPlanRevisionTasks_Fallback(t *testing.T) {
	tasks, source := buildPlanRevisionTasks(context.Background(), nil, "step1", "reason", nil)
	assert.NotEmpty(t, tasks)
	assert.Equal(t, "heuristic_fallback", source)
}

func TestBuildPlanRevisionTasks_BypassesSlowVertexDirectAndUsesBrainSidecar(t *testing.T) {
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")

	var captured structuredRequestCapture
	llmServer := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/llm/structured-output", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"jsonResult": `{"tasks":[{"id":"retry_1","name":"Retry retrieval","action":"retry_search","reason":"Retry evidence collection","dependsOnIds":["step1"]}]}`,
			"modelUsed":  "test-plan-revision-sidecar",
		}))
	}))
	defer llmServer.Close()
	t.Setenv("PYTHON_SIDECAR_HTTP_URL", llmServer.URL)

	client := llm.NewClientWithTimeout(500 * time.Millisecond)
	setUnexportedField(t, client, "transport", "http-json")
	setUnexportedField(t, client, "httpBaseURL", llmServer.URL)

	slowDirect := &slowVertexModelsClient{}
	vertexClient := &llm.VertexClient{}
	setUnexportedField(t, vertexClient, "client", slowDirect)
	setUnexportedField(t, vertexClient, "backend", "vertex_ai")
	client.VertexDirect = vertexClient

	brain := wisdev.NewBrainCapabilities(client)
	start := time.Now()
	tasks, source := buildPlanRevisionTasks(
		context.Background(),
		&wisdev.AgentGateway{Brain: brain},
		"step1",
		"timeout",
		map[string]any{"attempt": 1},
	)
	elapsed := time.Since(start)

	assert.Equal(t, []string{"Retry evidence collection"}, tasks)
	assert.Equal(t, "brain_coordinate_replan", source)
	assert.Less(t, elapsed, time.Second)
	assert.False(t, slowDirect.called.Load())
	assert.Equal(t, llm.ResolveStandardModel(), captured.Model)
	assert.Equal(t, "structured_high_value", captured.RequestClass)
	assert.Equal(t, "standard", captured.RetryProfile)
	assert.Equal(t, "priority", captured.ServiceTier)
	assert.Greater(t, captured.LatencyBudgetMs, int32(0))
	assertStructuredPromptHygiene(t, captured.Prompt)
}

func TestHandleDecomposeTaskBypassesSlowVertexDirectAndUsesSidecar(t *testing.T) {
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")

	var captured structuredRequestCapture
	llmServer := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/llm/structured-output", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"jsonResult": `{"tasks":[{"id":"step_1","name":"Gather evidence","action":"search","dependsOnIds":[]}]}`,
			"modelUsed":  "test-decompose-sidecar",
		}))
	}))
	defer llmServer.Close()
	t.Setenv("PYTHON_SIDECAR_HTTP_URL", llmServer.URL)

	client := llm.NewClientWithTimeout(500 * time.Millisecond)
	setUnexportedField(t, client, "transport", "http-json")
	setUnexportedField(t, client, "httpBaseURL", llmServer.URL)

	slowDirect := &slowVertexModelsClient{}
	vertexClient := &llm.VertexClient{}
	setUnexportedField(t, vertexClient, "client", slowDirect)
	setUnexportedField(t, vertexClient, "backend", "vertex_ai")
	client.VertexDirect = vertexClient

	handler := NewWisDevHandler(nil, nil, nil, nil, wisdev.NewBrainCapabilities(client), nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/wisdev/decompose", strings.NewReader(`{"query":"sleep and memory","domain":"cs"}`))
	w := httptest.NewRecorder()

	start := time.Now()
	handler.HandleDecomposeTask(w, req)
	elapsed := time.Since(start)

	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Tasks []wisdev.ResearchTask `json:"tasks"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, []wisdev.ResearchTask{{ID: "step_1", Name: "Gather evidence", Action: "search", DependsOnIDs: []string{}}}, resp.Tasks)
	assert.Less(t, elapsed, time.Second)
	assert.False(t, slowDirect.called.Load())
	assert.Equal(t, llm.ResolveStandardModel(), captured.Model)
	assert.Equal(t, "structured_high_value", captured.RequestClass)
	assert.Equal(t, "standard", captured.RetryProfile)
	assert.Equal(t, "priority", captured.ServiceTier)
	assert.Greater(t, captured.LatencyBudgetMs, int32(0))
	assertStructuredPromptHygiene(t, captured.Prompt)
}

func TestHandleProposeHypothesesBypassesSlowVertexDirectAndUsesSidecar(t *testing.T) {
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")

	var captured structuredRequestCapture
	llmServer := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/llm/structured-output", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"jsonResult": `{"hypotheses":[{"claim":"Memory consolidation improves with sleep","falsifiabilityCondition":"No improvement after controlled sleep manipulation","confidenceThreshold":0.65}]}`,
			"modelUsed":  "test-hypotheses-sidecar",
		}))
	}))
	defer llmServer.Close()
	t.Setenv("PYTHON_SIDECAR_HTTP_URL", llmServer.URL)

	client := llm.NewClientWithTimeout(500 * time.Millisecond)
	setUnexportedField(t, client, "transport", "http-json")
	setUnexportedField(t, client, "httpBaseURL", llmServer.URL)

	slowDirect := &slowVertexModelsClient{}
	vertexClient := &llm.VertexClient{}
	setUnexportedField(t, vertexClient, "client", slowDirect)
	setUnexportedField(t, vertexClient, "backend", "vertex_ai")
	client.VertexDirect = vertexClient

	handler := NewWisDevHandler(nil, nil, nil, nil, wisdev.NewBrainCapabilities(client), nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/wisdev/hypotheses", strings.NewReader(`{"query":"sleep and memory","intent":"discovery"}`))
	w := httptest.NewRecorder()

	start := time.Now()
	handler.HandleProposeHypotheses(w, req)
	elapsed := time.Since(start)

	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Hypotheses []wisdev.Hypothesis `json:"hypotheses"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Len(t, resp.Hypotheses, 1)
	assert.Equal(t, "Memory consolidation improves with sleep", resp.Hypotheses[0].Claim)
	assert.Equal(t, "No improvement after controlled sleep manipulation", resp.Hypotheses[0].FalsifiabilityCondition)
	assert.Equal(t, 0.65, resp.Hypotheses[0].ConfidenceThreshold)
	assert.Less(t, elapsed, time.Second)
	assert.False(t, slowDirect.called.Load())
	assert.Equal(t, llm.ResolveStandardModel(), captured.Model)
	assert.Equal(t, "structured_high_value", captured.RequestClass)
	assert.Equal(t, "standard", captured.RetryProfile)
	assert.Equal(t, "priority", captured.ServiceTier)
	assert.Greater(t, captured.LatencyBudgetMs, int32(0))
	assertStructuredPromptHygiene(t, captured.Prompt)
}

func TestChooseRecommendedAction(t *testing.T) {
	assert.Equal(t, "broaden_queries", chooseRecommendedAction("low coverage", nil))
	assert.Equal(t, "collect_counterevidence", chooseRecommendedAction("contradictory", nil))
	assert.Equal(t, "reduce_search_depth", chooseRecommendedAction("timeout", nil))
	assert.Equal(t, "regenerate_next_step", chooseRecommendedAction("other", map[string]any{"k": "v"}))
	assert.Equal(t, "replan", chooseRecommendedAction("other", nil))
}

func TestBuildSubtopicsResponse_Fallback(t *testing.T) {
	// With a known-domain query the fallback pool should fill up to the
	// requested limit (5) without an LLM client.
	s, k, v, source, explanation := buildSubtopicsResponse(context.Background(), nil, "machine learning safety evaluation", "cs", 5)
	assert.GreaterOrEqual(t, len(s), 5, "fallback subtopics should fill the requested limit for a known domain")
	assert.NotEmpty(t, k)
	assert.NotEmpty(t, v)
	assert.Equal(t, "heuristic_fallback", source)
	assert.Empty(t, explanation) // heuristic path provides no explanation
}

func TestBuildSubtopicsResponseWithExclusions_Fallback(t *testing.T) {
	s, _, _, source, explanation := buildSubtopicsResponseWithExclusions(
		context.Background(),
		nil,
		"alignment robustness benchmarks evaluation interpretability",
		"cs",
		5,
		[]string{"Alignment", "Robustness"},
	)
	assert.GreaterOrEqual(t, len(s), 3, "fallback should still return ≥3 novel options after exclusions")
	assert.NotContains(t, s, "Alignment")
	assert.NotContains(t, s, "Robustness")
	assert.Equal(t, "heuristic_fallback", source)
	assert.NotEmpty(t, explanation)
}

func TestRecommendAdditionalSubtopics_ReturnsEnoughCandidates(t *testing.T) {
	// The replenish function should return more than 3 candidates so that
	// avoidRepeatedDynamicOptions can fill any limit > 3.
	result := recommendAdditionalSubtopics("deep learning training efficiency", "cs", []string{"Training Data"})
	assert.Greater(t, len(result), 3, "replenishment pool must exceed 3 to fill limits up to 8")
	// Excluded item must not appear in results.
	for _, r := range result {
		assert.NotEqualValues(t, "training data", strings.ToLower(r))
	}
}

func TestRecommendAdditionalStudyTypes_ReturnsEnoughCandidates(t *testing.T) {
	result := recommendAdditionalStudyTypes("clinical drug trial outcomes", "medicine", []string{"randomized controlled trial"})
	assert.Greater(t, len(result), 3, "replenishment pool must exceed 3 to fill limits up to 6")
	for _, r := range result {
		assert.NotEqualValues(t, "randomized controlled trial", strings.ToLower(r))
	}
}

func TestBuildSubtopicsResponse_LLMStructuredPolicy(t *testing.T) {
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")

	var captured structuredRequestCapture
	llmServer := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/llm/structured-output", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"jsonResult": `{"subtopics":["Benchmarks","Evaluation"],"keywords":["benchmarking","evaluation"],"queryVariations":["query benchmarks"],"explanation":"Focus the retrieval surface."}`,
			"modelUsed":  "test-subtopics-llm",
		}))
	}))
	defer llmServer.Close()
	t.Setenv("PYTHON_SIDECAR_HTTP_URL", llmServer.URL)

	subtopics, keywords, variations, source, explanation := buildSubtopicsResponse(
		context.Background(),
		&wisdev.AgentGateway{LLMClient: llm.NewClient()},
		"machine learning safety",
		"cs",
		5,
	)

	require.GreaterOrEqual(t, len(subtopics), 2)
	assert.Equal(t, []string{"Benchmarks", "Evaluation"}, subtopics[:2])
	assert.Equal(t, []string{"benchmarking", "evaluation"}, keywords)
	assert.Equal(t, []string{"query benchmarks"}, variations)
	assert.Equal(t, "llm_structured", source)
	assert.Equal(t, "Focus the retrieval surface.", explanation)
	assert.Equal(t, llm.ResolveLightModel(), captured.Model)
	assert.Equal(t, "light", captured.RequestClass)
	assert.Equal(t, "conservative", captured.RetryProfile)
	assert.Equal(t, "standard", captured.ServiceTier)
	assert.Greater(t, captured.LatencyBudgetMs, int32(0))
	assertStructuredPromptHygiene(t, captured.Prompt)
}

func TestBuildSubtopicsResponse_InvalidStructuredOutputIsQuarantined(t *testing.T) {
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")

	llmServer := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"jsonResult": "prose {\"subtopics\":[\"wrapped\"]}",
			"modelUsed":  "test-invalid-subtopics-llm",
		}))
	}))
	defer llmServer.Close()
	t.Setenv("PYTHON_SIDECAR_HTTP_URL", llmServer.URL)

	subtopics, _, _, source, explanation := buildSubtopicsResponse(
		context.Background(),
		&wisdev.AgentGateway{LLMClient: llm.NewClient()},
		"machine learning safety",
		"cs",
		5,
	)

	require.NotEmpty(t, subtopics)
	assert.Equal(t, "structured_invalid_fallback", source)
	assert.Contains(t, explanation, "structured output")
}

func TestBuildSubtopicsResponse_TimeoutFallback(t *testing.T) {
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")

	previousTimeout := wisdevInteractiveStructuredTimeout
	wisdevInteractiveStructuredTimeout = 150 * time.Millisecond
	t.Cleanup(func() {
		wisdevInteractiveStructuredTimeout = previousTimeout
	})

	llmServer := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/llm/structured-output", r.URL.Path)
		select {
		case <-time.After(2 * time.Second):
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"jsonResult": `{"subtopics":["Slow"],"keywords":["slow"],"queryVariations":["slow query"],"explanation":"slow"}`,
				"modelUsed":  "slow-subtopics-llm",
			}))
		case <-r.Context().Done():
			return
		}
	}))
	defer llmServer.Close()
	t.Setenv("PYTHON_SIDECAR_HTTP_URL", llmServer.URL)

	start := time.Now()
	subtopics, keywords, variations, source, explanation := buildSubtopicsResponse(
		context.Background(),
		&wisdev.AgentGateway{LLMClient: llm.NewClientWithTimeout(5 * time.Second)},
		"machine learning safety",
		"cs",
		5,
	)
	elapsed := time.Since(start)

	assert.Less(t, elapsed, time.Second)
	assert.NotEmpty(t, subtopics)
	assert.NotEmpty(t, keywords)
	assert.NotEmpty(t, variations)
	assert.Equal(t, "heuristic_fallback", source)
	assert.Empty(t, explanation)
}

func TestBuildSubtopicsResponseBypassesSlowVertexDirectAndUsesSidecar(t *testing.T) {
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")

	var captured structuredRequestCapture
	llmServer := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/llm/structured-output", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"jsonResult": `{"subtopics":["Benchmarks","Evaluation"],"keywords":["benchmarking"],"queryVariations":["query benchmarks"],"explanation":"Focus on benchmark evidence."}`,
			"modelUsed":  "test-subtopics-sidecar",
		}))
	}))
	defer llmServer.Close()
	t.Setenv("PYTHON_SIDECAR_HTTP_URL", llmServer.URL)

	client := llm.NewClientWithTimeout(500 * time.Millisecond)
	setUnexportedField(t, client, "transport", "http-json")
	setUnexportedField(t, client, "httpBaseURL", llmServer.URL)

	slowDirect := &slowVertexModelsClient{}
	vertexClient := &llm.VertexClient{}
	setUnexportedField(t, vertexClient, "client", slowDirect)
	setUnexportedField(t, vertexClient, "backend", "vertex_ai")
	client.VertexDirect = vertexClient

	start := time.Now()
	subtopics, keywords, variations, source, explanation := buildSubtopicsResponse(
		context.Background(),
		&wisdev.AgentGateway{LLMClient: client},
		"machine learning",
		"cs",
		5,
	)
	elapsed := time.Since(start)

	require.GreaterOrEqual(t, len(subtopics), 2)
	assert.Equal(t, []string{"Benchmarks", "Evaluation"}, subtopics[:2])
	assert.Equal(t, []string{"benchmarking"}, keywords)
	assert.Equal(t, []string{"query benchmarks"}, variations)
	assert.Equal(t, "llm_structured", source)
	assert.Equal(t, "Focus on benchmark evidence.", explanation)
	assert.Less(t, elapsed, time.Second)
	assert.False(t, slowDirect.called.Load())
	assert.Equal(t, llm.ResolveLightModel(), captured.Model)
}

func TestBuildStudyTypesResponse_Fallback(t *testing.T) {
	st, sig, source, explanation := buildStudyTypesResponse(context.Background(), nil, "clinical trial outcomes", "medicine", []string{"s1"}, 5)
	assert.GreaterOrEqual(t, len(st), 5, "fallback study types should fill the requested limit for a known domain")
	assert.Equal(t, "heuristic_fallback", source)
	assert.Empty(t, explanation) // heuristic path provides no explanation
	_ = sig
}

func TestWisdevInteractiveStructuredTimeoutCoversWarmStructuredHelpers(t *testing.T) {
	assert.GreaterOrEqual(t, wisdevInteractiveStructuredTimeout, 15*time.Second)
	assert.GreaterOrEqual(t, wisdevInteractiveStructuredGrace, 3*time.Second)
}

func TestBuildStudyTypesResponseWithExclusions_Fallback(t *testing.T) {
	st, _, source, explanation := buildStudyTypesResponseWithExclusions(
		context.Background(),
		nil,
		"compare benchmark review",
		"medicine",
		[]string{"Clinical Outcomes", "Safety Signals"},
		5,
		[]string{"systematic review", "meta-analysis"},
	)
	assert.NotEmpty(t, st)
	assert.NotContains(t, st, "systematic review")
	assert.NotContains(t, st, "meta-analysis")
	assert.Equal(t, "heuristic_fallback", source)
	assert.NotEmpty(t, explanation)
}

func TestBuildStudyTypesResponse_LLMStructuredPolicy(t *testing.T) {
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")

	var captured structuredRequestCapture
	llmServer := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/llm/structured-output", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"jsonResult": `{"studyTypes":["benchmark","ablation study"],"matchedSignals":["comparative_intent"],"explanation":"These methods cover model comparisons."}`,
			"modelUsed":  "test-study-types-llm",
		}))
	}))
	defer llmServer.Close()
	t.Setenv("PYTHON_SIDECAR_HTTP_URL", llmServer.URL)

	studyTypes, signals, source, explanation := buildStudyTypesResponse(
		context.Background(),
		&wisdev.AgentGateway{LLMClient: llm.NewClient()},
		"compare transformer models",
		"ai",
		[]string{"Performance", "Architecture"},
		5,
	)

	require.GreaterOrEqual(t, len(studyTypes), 2)
	assert.Equal(t, []string{"benchmark", "ablation study"}, studyTypes[:2])
	assert.Equal(t, []string{"comparative_intent"}, signals)
	assert.Equal(t, "llm_structured", source)
	assert.Equal(t, "These methods cover model comparisons.", explanation)
	assert.Equal(t, llm.ResolveLightModel(), captured.Model)
	assert.Equal(t, "light", captured.RequestClass)
	assert.Equal(t, "conservative", captured.RetryProfile)
	assert.Equal(t, "standard", captured.ServiceTier)
	assert.Greater(t, captured.LatencyBudgetMs, int32(0))
	assertStructuredPromptHygiene(t, captured.Prompt)
}

func TestBuildStudyTypesResponse_TimeoutFallback(t *testing.T) {
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")

	previousTimeout := wisdevInteractiveStructuredTimeout
	wisdevInteractiveStructuredTimeout = 150 * time.Millisecond
	t.Cleanup(func() {
		wisdevInteractiveStructuredTimeout = previousTimeout
	})

	llmServer := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/llm/structured-output", r.URL.Path)
		select {
		case <-time.After(2 * time.Second):
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"jsonResult": `{"studyTypes":["slow study"],"matchedSignals":["slow"],"explanation":"slow"}`,
				"modelUsed":  "slow-study-types-llm",
			}))
		case <-r.Context().Done():
			return
		}
	}))
	defer llmServer.Close()
	t.Setenv("PYTHON_SIDECAR_HTTP_URL", llmServer.URL)

	start := time.Now()
	studyTypes, signals, source, explanation := buildStudyTypesResponse(
		context.Background(),
		&wisdev.AgentGateway{LLMClient: llm.NewClientWithTimeout(5 * time.Second)},
		"compare transformer models",
		"ai",
		[]string{"Performance", "Architecture"},
		5,
	)
	elapsed := time.Since(start)

	assert.Less(t, elapsed, time.Second)
	assert.NotEmpty(t, studyTypes)
	assert.NotEmpty(t, signals)
	assert.Equal(t, "heuristic_fallback", source)
	assert.Empty(t, explanation)
}

func TestBuildResearchPathScore_Fallback(t *testing.T) {
	score, reason, source := buildResearchPathScore(context.Background(), nil, "query", "domain", []string{"s1", "s2"}, []string{"t1"}, 0, 0.8)
	assert.Greater(t, score, 0.0)
	assert.NotEmpty(t, reason)
	assert.Equal(t, "heuristic_fallback", source)

	score2, _, source2 := buildResearchPathScore(context.Background(), nil, "query", "domain", nil, nil, 0.9, 0.8)
	assert.Equal(t, 0.9, score2)
	assert.Equal(t, "client_supplied", source2)
}

func TestBuildResearchPathScore_LLMStructuredPolicy(t *testing.T) {
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")

	var captured structuredRequestCapture
	llmServer := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/llm/structured-output", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"jsonResult": `{"pathScore":0.82,"reasoning":"Coverage and study diversity are sufficient."}`,
			"modelUsed":  "test-path-score-llm",
		}))
	}))
	defer llmServer.Close()
	t.Setenv("PYTHON_SIDECAR_HTTP_URL", llmServer.URL)

	score, reasoning, source := buildResearchPathScore(
		context.Background(),
		&wisdev.AgentGateway{LLMClient: llm.NewClient()},
		"sleep interventions",
		"medicine",
		[]string{"Clinical Outcomes", "Safety Signals"},
		[]string{"randomized trial"},
		0,
		0.8,
	)

	assert.Equal(t, 0.82, score)
	assert.Equal(t, "Coverage and study diversity are sufficient.", reasoning)
	assert.Equal(t, "llm_structured", source)
	assert.Equal(t, llm.ResolveStandardModel(), captured.Model)
	assert.Equal(t, "structured_high_value", captured.RequestClass)
	assert.Equal(t, "standard", captured.RetryProfile)
	assert.Equal(t, "priority", captured.ServiceTier)
	assert.Greater(t, captured.LatencyBudgetMs, int32(0))
	assertStructuredPromptHygiene(t, captured.Prompt)
}

func TestBuildResearchPathScore_TimeoutFallback(t *testing.T) {
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")

	previousTimeout := wisdevInteractiveStructuredTimeout
	wisdevInteractiveStructuredTimeout = 150 * time.Millisecond
	t.Cleanup(func() {
		wisdevInteractiveStructuredTimeout = previousTimeout
	})

	llmServer := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/llm/structured-output", r.URL.Path)
		select {
		case <-time.After(2 * time.Second):
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"jsonResult": `{"pathScore":0.99,"reasoning":"slow"}`,
				"modelUsed":  "slow-path-score-llm",
			}))
		case <-r.Context().Done():
			return
		}
	}))
	defer llmServer.Close()
	t.Setenv("PYTHON_SIDECAR_HTTP_URL", llmServer.URL)

	start := time.Now()
	score, reasoning, source := buildResearchPathScore(
		context.Background(),
		&wisdev.AgentGateway{LLMClient: llm.NewClientWithTimeout(5 * time.Second)},
		"sleep interventions",
		"medicine",
		[]string{"Clinical Outcomes", "Safety Signals"},
		[]string{"randomized trial"},
		0,
		0.8,
	)
	elapsed := time.Since(start)

	assert.Less(t, elapsed, time.Second)
	assert.Greater(t, score, 0.0)
	assert.NotEmpty(t, reasoning)
	assert.Equal(t, "heuristic_fallback", source)
}

func TestBuildResearchPathStrengths(t *testing.T) {
	res := buildResearchPathStrengths([]string{"s1", "s2"}, []string{"t1"}, 0.8)
	assert.NotEmpty(t, res)
	assert.Contains(t, res[0], "broad enough")
}

func TestBuildResearchPathGaps(t *testing.T) {
	res := buildResearchPathGaps([]string{"s1"}, nil, 0.5, 0.8)
	assert.Len(t, res, 3)
}

func TestBuildRecommendedNextStep(t *testing.T) {
	assert.Contains(t, buildRecommendedNextStep([]string{"s1"}, nil, 0.5, 0.8), "subtopics")
	assert.Contains(t, buildRecommendedNextStep([]string{"s1", "s2"}, nil, 0.5, 0.8), "study-type")
	assert.Contains(t, buildRecommendedNextStep([]string{"s1", "s2"}, []string{"t1"}, 0.5, 0.8), "Broaden")
	assert.Contains(t, buildRecommendedNextStep([]string{"s1", "s2"}, []string{"t1"}, 0.9, 0.8), "synthesis")
}

func TestRecommendAdditionalSubtopics(t *testing.T) {
	res := recommendAdditionalSubtopics("machine learning", "cs", []string{"Neural Networks"})
	assert.NotEmpty(t, res)
}

func TestRecommendAdditionalStudyTypes(t *testing.T) {
	res := recommendAdditionalStudyTypes("query", "medicine", []string{"Clinical Trial"})
	assert.NotEmpty(t, res)
}

func TestBuildResearchPathSignals(t *testing.T) {
	res := buildResearchPathSignals("cs", []string{"s1", "s2"}, []string{"t1"}, 0.8)
	assert.Contains(t, res, "query_present")
	assert.Contains(t, res, "domain:cs")
}

func TestBuildCoverageEvaluation_Fallback(t *testing.T) {
	score, missing, recommended, source := buildCoverageEvaluation(context.Background(), nil, "machine learning", []string{"ml"}, nil, nil)
	assert.Greater(t, score, 0.0)
	assert.Equal(t, "heuristic_fallback", source)
	_ = missing
	_ = recommended
}

func TestBuildCoverageEvaluation_LLMStructuredPolicy(t *testing.T) {
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")

	var captured structuredRequestCapture
	llmServer := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/llm/structured-output", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"jsonResult": `{"coverageScore":0.74,"missingTerms":["generalization","","generalization","robustness","safety","alignment","evaluation","datasets"],"recommendedQueries":["machine learning generalization","","machine learning generalization","machine learning robustness","machine learning safety"]}`,
			"modelUsed":  "test-coverage-llm",
		}))
	}))
	defer llmServer.Close()
	t.Setenv("PYTHON_SIDECAR_HTTP_URL", llmServer.URL)

	score, missing, recommended, source := buildCoverageEvaluation(
		context.Background(),
		&wisdev.AgentGateway{LLMClient: llm.NewClient()},
		"machine learning",
		[]string{"machine learning basics"},
		nil,
		nil,
	)

	assert.Equal(t, 0.74, score)
	assert.Equal(t, []string{"generalization", "robustness", "safety", "alignment", "evaluation", "datasets"}, missing)
	assert.Equal(t, []string{"machine learning generalization", "machine learning robustness", "machine learning safety"}, recommended)
	assert.Equal(t, "llm_structured", source)
	assert.Equal(t, llm.ResolveLightModel(), captured.Model)
	assert.Equal(t, "light", captured.RequestClass)
	assert.Equal(t, "conservative", captured.RetryProfile)
	assert.Equal(t, "standard", captured.ServiceTier)
	assert.Greater(t, captured.LatencyBudgetMs, int32(0))
	assertStructuredPromptHygiene(t, captured.Prompt)
}

func TestBuildCoverageEvaluation_TimeoutFallback(t *testing.T) {
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")

	previousTimeout := wisdevInteractiveStructuredTimeout
	wisdevInteractiveStructuredTimeout = 150 * time.Millisecond
	t.Cleanup(func() {
		wisdevInteractiveStructuredTimeout = previousTimeout
	})

	llmServer := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/llm/structured-output", r.URL.Path)
		select {
		case <-time.After(2 * time.Second):
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"jsonResult": `{"coverageScore":0.99,"missingTerms":["slow"],"recommendedQueries":["slow query"]}`,
				"modelUsed":  "slow-coverage-llm",
			}))
		case <-r.Context().Done():
			return
		}
	}))
	defer llmServer.Close()
	t.Setenv("PYTHON_SIDECAR_HTTP_URL", llmServer.URL)

	start := time.Now()
	score, missing, recommended, source := buildCoverageEvaluation(
		context.Background(),
		&wisdev.AgentGateway{LLMClient: llm.NewClientWithTimeout(5 * time.Second)},
		"machine learning",
		[]string{"machine learning basics"},
		nil,
		nil,
	)
	elapsed := time.Since(start)

	assert.Less(t, elapsed, time.Second)
	assert.Greater(t, score, 0.0)
	assert.Equal(t, "heuristic_fallback", source)
	assert.NotNil(t, missing)
	assert.NotNil(t, recommended)
}

func TestBuildFollowUpQuestion(t *testing.T) {
	res := buildFollowUpQuestion("query", "cs", []string{"term"}, nil, nil)
	assert.Equal(t, "follow_up_refinement", res["id"])
	assert.Equal(t, "q4_subtopics", res["targetQuestionId"])
	assert.Equal(t, "Which focus should the next search pass prioritize?", res["question"])
	assert.Equal(t, []string{"Term"}, questionOptionValues(res["options"]))

	res2 := buildFollowUpQuestion("query", "cs", nil, []string{"s1"}, nil)
	assert.Equal(t, "follow_up_refinement", res2["id"])
	assert.Equal(t, "q4_subtopics", res2["targetQuestionId"])
	assert.NotEmpty(t, questionOptionValues(res2["options"]))

	res3 := buildFollowUpQuestion("", "medicine", nil, []string{"Clinical Outcomes", "Patient Selection", "Safety Signals"}, nil)
	assert.Equal(t, "q5_study_types", res3["targetQuestionId"])
	assert.Contains(t, questionOptionValues(res3["options"]), "Randomized Controlled Trial")
}

func TestDeriveSubtopicsPrefersQueryAnchorsOverBareDomainDefaults(t *testing.T) {
	subtopics, keywords, variations := deriveSubtopics("RLHF reinforcement learning", "cs", 5)

	assert.Contains(t, subtopics, "RLHF")
	assert.Contains(t, subtopics, "Reinforcement Learning")
	assert.NotContains(t, subtopics, "Benchmarks")
	assert.Contains(t, keywords, "rlhf")
	assert.NotEmpty(t, variations)

	hasAnchoredFacet := false
	for _, subtopic := range subtopics {
		if strings.Contains(subtopic, "Benchmarks") && subtopic != "Benchmarks" {
			hasAnchoredFacet = true
			break
		}
	}
	assert.True(t, hasAnchoredFacet, "expected a query-anchored benchmark-style facet")
}

func TestBuildFollowUpQuestionUsesQueryAnchoredFallbackOptions(t *testing.T) {
	res := buildFollowUpQuestion("RLHF reinforcement learning", "cs", nil, nil, nil)
	options := questionOptionValues(res["options"])

	assert.Equal(t, "follow_up_refinement", res["id"])
	assert.Equal(t, "q4_subtopics", res["targetQuestionId"])
	assert.Contains(t, options, "RLHF methods and reward modeling")
	assert.Contains(t, options, "RL optimization methods")
	assert.NotContains(t, options, "Reinforcement Learning")
	assert.NotContains(t, options, "Benchmarks")

	hasAnchoredFacet := false
	for _, option := range options {
		if strings.Contains(option, "Evaluation benchmarks") {
			hasAnchoredFacet = true
			break
		}
	}
	assert.True(t, hasAnchoredFacet, "expected follow-up fallback options to stay query-shaped")
}

func TestBuildFollowUpQuestionCollapsesRedundantMissingTerms(t *testing.T) {
	res := buildFollowUpQuestion("RLHF reinforcement learning", "cs", []string{
		"RLHF",
		"Reinforcement Learning",
		"Reinforcement Learning Benchmarks",
		"Reinforcement Learning Training Data",
	}, nil, nil)
	options := questionOptionValues(res["options"])

	assert.Equal(t, []string{
		"RLHF methods and reward modeling",
		"RL optimization methods",
		"Evaluation benchmarks and generalization",
		"Training data and feedback quality",
	}, options)
	for _, option := range sliceAnyMap(res["options"]) {
		assert.NotEmpty(t, wisdev.AsOptionalString(option["description"]))
	}
}

func TestBuildFollowUpDecisionSanitizesLLMFollowUpOptions(t *testing.T) {
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")

	var captured structuredRequestCapture
	llmServer := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/llm/structured-output", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"jsonResult": `{"needsFollowUp":true,"followUpQuestion":{"id":"llm_follow_up","question":"Which missing area should WisDev expand next?","options":["RLHF","Reinforcement Learning","Reinforcement Learning Benchmarks","Reinforcement Learning Training Data"]}}`,
			"modelUsed":  "test-follow-up-sidecar",
		}))
	}))
	defer llmServer.Close()
	t.Setenv("PYTHON_SIDECAR_HTTP_URL", llmServer.URL)

	needed, question, source := buildFollowUpDecision(
		context.Background(),
		&wisdev.AgentGateway{LLMClient: llm.NewClient()},
		"RLHF reinforcement learning",
		"cs",
		0.45,
		0.45,
		0.8,
		[]string{"RLHF", "Reinforcement Learning Benchmarks"},
		nil,
		nil,
	)

	assert.True(t, needed)
	require.NotNil(t, question)
	assert.Equal(t, "llm_structured", source)
	assert.Equal(t, "Which focus should the next search pass prioritize?", question["question"])
	assert.Equal(t, "Pick the focus areas that should guide the next retrieval pass.", question["helpText"])
	assert.Equal(t, []string{
		"RLHF methods and reward modeling",
		"RL optimization methods",
		"Evaluation benchmarks and generalization",
		"Training data and feedback quality",
	}, questionOptionValues(question["options"]))
	for _, option := range sliceAnyMap(question["options"]) {
		assert.NotEmpty(t, wisdev.AsOptionalString(option["description"]))
	}
	assert.Contains(t, captured.Prompt, "distinct, actionable research facets")
}

func TestBuildFollowUpDecision_Fallback(t *testing.T) {
	needed, question, source := buildFollowUpDecision(context.Background(), nil, "query", "cs", 0.5, 0.8, 0.8, nil, nil, nil)
	assert.True(t, needed)
	assert.NotNil(t, question)
	assert.Equal(t, "heuristic_fallback", source)
	assert.Equal(t, "heuristic_fallback", question["questionSource"])
	assert.Equal(t, "heuristic_fallback", question["optionsSource"])
	assert.NotEmpty(t, question["questionExplanation"])
	assert.NotEmpty(t, question["optionsExplanation"])
	assert.Equal(t, "q4_subtopics", question["targetQuestionId"])
}

func TestBuildFollowUpDecision_PreservesHeuristicFallbackWhenLLMDeclines(t *testing.T) {
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")

	var captured structuredRequestCapture
	llmServer := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/llm/structured-output", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"jsonResult": `{"needsFollowUp":false,"followUpQuestion":null}`,
			"modelUsed":  "test-follow-up-llm",
		}))
	}))
	defer llmServer.Close()
	t.Setenv("PYTHON_SIDECAR_HTTP_URL", llmServer.URL)

	needed, question, source := buildFollowUpDecision(
		context.Background(),
		&wisdev.AgentGateway{LLMClient: llm.NewClient()},
		"sleep interventions adults",
		"general",
		1.0,
		1.0,
		0.7,
		nil,
		nil,
		nil,
	)
	assert.True(t, needed)
	require.NotNil(t, question)
	assert.Equal(t, "heuristic_fallback", source)
	assert.Equal(t, "follow_up_refinement", question["id"])
	assert.Equal(t, "heuristic_fallback", question["questionSource"])
	assert.Equal(t, "q4_subtopics", question["targetQuestionId"])
	assert.Equal(t, llm.ResolveStandardModel(), captured.Model)
	assert.Equal(t, "structured_high_value", captured.RequestClass)
	assert.Equal(t, "standard", captured.RetryProfile)
	assert.Equal(t, "priority", captured.ServiceTier)
	assert.Greater(t, captured.LatencyBudgetMs, int32(0))
	assertStructuredPromptHygiene(t, captured.Prompt)
}

func TestBuildFollowUpDecisionBypassesSlowVertexDirectAndUsesSidecar(t *testing.T) {
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")

	var captured structuredRequestCapture
	llmServer := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/llm/structured-output", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"jsonResult": `{"needsFollowUp":true,"followUpQuestion":{"id":"sidecar_follow_up","question":"Which area should WisDev expand next?"}}`,
			"modelUsed":  "test-follow-up-sidecar",
		}))
	}))
	defer llmServer.Close()
	t.Setenv("PYTHON_SIDECAR_HTTP_URL", llmServer.URL)

	client := llm.NewClientWithTimeout(500 * time.Millisecond)
	setUnexportedField(t, client, "transport", "http-json")
	setUnexportedField(t, client, "httpBaseURL", llmServer.URL)

	slowDirect := &slowVertexModelsClient{}
	vertexClient := &llm.VertexClient{}
	setUnexportedField(t, vertexClient, "client", slowDirect)
	setUnexportedField(t, vertexClient, "backend", "vertex_ai")
	client.VertexDirect = vertexClient

	start := time.Now()
	needed, question, source := buildFollowUpDecision(
		context.Background(),
		&wisdev.AgentGateway{LLMClient: client},
		"sleep interventions adults",
		"general",
		1.0,
		1.0,
		0.7,
		nil,
		nil,
		nil,
	)
	elapsed := time.Since(start)

	assert.True(t, needed)
	require.NotNil(t, question)
	assert.Equal(t, "llm_structured", source)
	assert.Equal(t, "sidecar_follow_up", question["id"])
	assert.Less(t, elapsed, time.Second)
	assert.False(t, slowDirect.called.Load())
	assert.Equal(t, llm.ResolveStandardModel(), captured.Model)
	assert.Equal(t, "structured_high_value", captured.RequestClass)
	assert.Equal(t, "standard", captured.RetryProfile)
	assert.Equal(t, "priority", captured.ServiceTier)
}

func TestTrimStrings(t *testing.T) {
	res := trimStrings([]string{"a", "b", "a", "c"}, 2)
	assert.Len(t, res, 2)
}
