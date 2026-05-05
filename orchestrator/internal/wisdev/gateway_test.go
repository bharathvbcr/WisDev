package wisdev

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

func TestAgentGateway_DefaultPythonExecutor(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)

	gw := NewAgentGateway(nil, nil, nil)
	gw.Brain = NewBrainCapabilities(client)

	ctx := context.Background()

	t.Run("research.queryDecompose", func(t *testing.T) {
		mockLLM.On("StructuredOutput", mock.Anything, mock.Anything).Return(&llmv1.StructuredResponse{JsonResult: `{"tasks":[]}`}, nil).Once()
		res, err := gw.defaultPythonExecutor(ctx, "research.queryDecompose", map[string]any{"query": "test"}, nil)
		assert.NoError(t, err)
		assert.NotNil(t, res["tasks"])
	})

	t.Run("research.proposeHypotheses", func(t *testing.T) {
		payload, _ := json.Marshal(map[string]any{"hypotheses": []map[string]any{
			{
				"claim":                   "Proposed hypothesis",
				"falsifiabilityCondition": "Run a targeted validation study.",
				"confidenceThreshold":     0.81,
			},
		}})
		expectedPrompt := appendWisdevStructuredOutputInstruction("Propose 3-5 hypotheses for query: test. Intent: discovery.")
		mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			assertWisdevStructuredPromptHygiene(t, req.Prompt)
			return req != nil &&
				req.Model == llm.ResolveStandardModel() &&
				req.Prompt == expectedPrompt
		})).Return(&llmv1.StructuredResponse{JsonResult: string(payload)}, nil).Once()
		res, err := gw.defaultPythonExecutor(ctx, "research.proposeHypotheses", map[string]any{"query": "test", "intent": "discovery"}, nil)
		assert.NoError(t, err)
		hypotheses, ok := res["hypotheses"].([]Hypothesis)
		if assert.True(t, ok) && assert.Len(t, hypotheses, 1) {
			assert.Equal(t, "Proposed hypothesis", hypotheses[0].Claim)
		}
	})

	t.Run("research.generateThoughts", func(t *testing.T) {
		mockLLM.On("StructuredOutput", mock.Anything, mock.Anything).Return(&llmv1.StructuredResponse{
			JsonResult: `{"branches":[{"hypothesis":"focus reward modeling","nodes":[{"label":"reward modeling focus","reasoning":"Inspect reward-model evidence first.","search_weight":0.81}]}],"confidence":0.81}`,
		}, nil).Once()
		res, err := gw.defaultPythonExecutor(ctx, "research.generateThoughts", map[string]any{"query": "test"}, nil)
		assert.NoError(t, err)
		branches, ok := res["branches"].([]any)
		if assert.True(t, ok) {
			assert.Len(t, branches, 1)
		}
		assert.Equal(t, 0.81, res["confidence"])
	})

	t.Run("research.generateHypotheses", func(t *testing.T) {
		payload, _ := json.Marshal(map[string]any{"hypotheses": []map[string]any{
			{
				"claim":                   "Generated candidate hypothesis",
				"falsifiabilityCondition": "A broader evidence screen rejects the pattern.",
				"confidenceThreshold":     0.78,
			},
		}})
		expectedPrompt := appendWisdevStructuredOutputInstruction("Propose 3-5 hypotheses for query: test. Intent: exploration.")
		mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			assertWisdevStructuredPromptHygiene(t, req.Prompt)
			return req != nil &&
				req.Model == llm.ResolveStandardModel() &&
				req.Prompt == expectedPrompt
		})).Return(&llmv1.StructuredResponse{JsonResult: string(payload)}, nil).Once()
		res, err := gw.defaultPythonExecutor(ctx, ActionResearchGenerateHypotheses, map[string]any{
			"query":  "test",
			"domain": "neuroscience",
			"intent": "exploration",
		}, nil)
		assert.NoError(t, err)
		hypotheses, ok := res["hypotheses"].([]Hypothesis)
		if assert.True(t, ok) && assert.Len(t, hypotheses, 1) {
			assert.Equal(t, "Generated candidate hypothesis", hypotheses[0].Claim)
		}
		branches, ok := res["branches"].([]any)
		if assert.True(t, ok) {
			assert.Len(t, branches, 1)
		}
	})

	t.Run("research.synthesize-answer", func(t *testing.T) {
		mockLLM.On("StructuredOutput", mock.Anything, mock.Anything).Return(&llmv1.StructuredResponse{JsonResult: `{"sections":[{"heading":"Answer","sentences":[{"text":"answer","evidenceIds":[]}]}]}`}, nil).Once()
		res, err := gw.defaultPythonExecutor(ctx, "research.synthesize-answer", map[string]any{"query": "test", "papers": []any{}}, nil)
		assert.NoError(t, err)
		assert.Equal(t, "## Answer\n\nanswer", res["text"])
		assert.NotNil(t, res["structuredAnswer"])
	})

	t.Run("query.enhanceAcademic", func(t *testing.T) {
		mockLLM.On("Generate", mock.Anything, mock.Anything).Return(&llmv1.GenerateResponse{Text: "enhanced"}, nil).Once()
		res, err := gw.defaultPythonExecutor(ctx, "query.enhanceAcademic", map[string]any{"query": "test"}, nil)
		assert.NoError(t, err)
		assert.Equal(t, "enhanced", res["enhanced_query"])
	})

	t.Run("unknown action", func(t *testing.T) {
		_, err := gw.defaultPythonExecutor(ctx, "unknown", nil, nil)
		assert.Error(t, err)
	})
}

func TestAgentGateway_DefaultPythonExecutor_QueryDecomposeBypassesSlowVertexDirectAndUsesSidecar(t *testing.T) {
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")

	var captured llmv1.StructuredRequest
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/llm/structured-output", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"jsonResult": `{"tasks":[{"id":"step_1","name":"Gather evidence","action":"search","dependsOnIds":[]}]}`,
			"modelUsed":  "test-gateway-decompose-sidecar",
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

	gw := NewAgentGateway(nil, nil, nil)
	gw.Brain = NewBrainCapabilities(client)

	start := time.Now()
	res, err := gw.defaultPythonExecutor(context.Background(), "research.queryDecompose", map[string]any{
		"query":  "sleep and memory",
		"domain": "cs",
	}, nil)
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Less(t, elapsed, time.Second)
	assert.False(t, slowDirect.called.Load())
	assert.Equal(t, llm.ResolveStandardModel(), captured.Model)
	assertWisdevStructuredPromptHygiene(t, captured.Prompt)
	tasks, ok := res["tasks"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, tasks, 1)
	assert.Equal(t, "step_1", tasks[0]["id"])
	assert.Equal(t, "Gather evidence", tasks[0]["name"])
	assert.Equal(t, "search", tasks[0]["action"])
}

func TestAgentGateway_DefaultPythonExecutor_ProposeHypothesesBypassesSlowVertexDirectAndUsesSidecar(t *testing.T) {
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")

	var captured llmv1.StructuredRequest
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/llm/structured-output", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"jsonResult": `{"hypotheses":[{"claim":"Sleep improves consolidation","falsifiabilityCondition":"No benefit after controlled sleep exposure","confidenceThreshold":0.72}]}`,
			"modelUsed":  "test-gateway-hypotheses-sidecar",
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

	gw := NewAgentGateway(nil, nil, nil)
	gw.Brain = NewBrainCapabilities(client)

	start := time.Now()
	res, err := gw.defaultPythonExecutor(context.Background(), ActionResearchProposeHypotheses, map[string]any{
		"query":  "sleep and memory",
		"intent": "discovery",
	}, nil)
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Less(t, elapsed, time.Second)
	assert.False(t, slowDirect.called.Load())
	assert.Equal(t, llm.ResolveStandardModel(), captured.Model)
	assertWisdevStructuredPromptHygiene(t, captured.Prompt)
	hypotheses, ok := res["hypotheses"].([]Hypothesis)
	require.True(t, ok)
	require.Len(t, hypotheses, 1)
	assert.Equal(t, "Sleep improves consolidation", hypotheses[0].Claim)
	assert.Equal(t, "No benefit after controlled sleep exposure", hypotheses[0].FalsifiabilityCondition)
	assert.Equal(t, 0.72, hypotheses[0].ConfidenceThreshold)
}

func TestAgentGateway_DefaultPythonExecutor_GenerateHypothesesBypassesSlowVertexDirectAndUsesSidecar(t *testing.T) {
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")

	var captured llmv1.StructuredRequest
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/llm/structured-output", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"jsonResult": `{"hypotheses":[{"claim":"Generated candidate hypothesis","falsifiabilityCondition":"A broader evidence screen rejects the pattern","confidenceThreshold":0.78}]}`,
			"modelUsed":  "test-gateway-generate-hypotheses-sidecar",
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

	gw := NewAgentGateway(nil, nil, nil)
	gw.Brain = NewBrainCapabilities(client)

	start := time.Now()
	res, err := gw.defaultPythonExecutor(context.Background(), ActionResearchGenerateHypotheses, map[string]any{
		"query":  "sleep and memory",
		"domain": "neuroscience",
		"intent": "exploration",
	}, nil)
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Less(t, elapsed, time.Second)
	assert.False(t, slowDirect.called.Load())
	assert.Equal(t, llm.ResolveStandardModel(), captured.Model)
	assertWisdevStructuredPromptHygiene(t, captured.Prompt)
	hypotheses, ok := res["hypotheses"].([]Hypothesis)
	require.True(t, ok)
	require.Len(t, hypotheses, 1)
	assert.Equal(t, "Generated candidate hypothesis", hypotheses[0].Claim)
}

func TestAgentGateway_DefaultPythonExecutor_SnowballFallbackBypassesSlowVertexDirectAndUsesSidecar(t *testing.T) {
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")

	var captured llmv1.StructuredRequest
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/llm/structured-output", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"jsonResult": `{"queries":["query 1","query 2"]}`,
			"modelUsed":  "test-gateway-snowball-sidecar",
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

	gw := NewAgentGateway(nil, nil, nil)
	gw.Brain = NewBrainCapabilities(client)
	gw.Loop = &AutonomousLoop{searchReg: search.NewProviderRegistry()}

	start := time.Now()
	res, err := gw.defaultPythonExecutor(context.Background(), "research.snowballCitations", map[string]any{
		"papers": []any{
			map[string]any{"id": "p1", "title": "Paper 1"},
		},
	}, nil)
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Less(t, elapsed, time.Second)
	assert.False(t, slowDirect.called.Load())
	assert.Equal(t, llm.ResolveLightModel(), captured.Model)
	assertWisdevStructuredPromptHygiene(t, captured.Prompt)
	assert.Equal(t, []string{"query 1", "query 2"}, res["exploratory_queries"])
}

func TestAgentGateway_DefaultPythonExecutor_BuildClaimEvidenceTableBypassesSlowVertexDirectAndUsesSidecar(t *testing.T) {
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")

	var captured llmv1.StructuredRequest
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/llm/structured-output", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"jsonResult": `{"table":"| Claim | Evidence |","rowCount":2}`,
			"modelUsed":  "test-gateway-claim-evidence-sidecar",
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

	gw := NewAgentGateway(nil, nil, nil)
	gw.Brain = NewBrainCapabilities(client)

	start := time.Now()
	res, err := gw.defaultPythonExecutor(context.Background(), "research.buildClaimEvidenceTable", map[string]any{
		"query": "sleep and memory",
		"papers": []any{
			map[string]any{"id": "p1", "title": "Paper 1", "abstract": "abstract"},
		},
	}, nil)
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Less(t, elapsed, time.Second)
	assert.False(t, slowDirect.called.Load())
	assert.Equal(t, llm.ResolveStandardModel(), captured.Model)
	assertWisdevStructuredPromptHygiene(t, captured.Prompt)
	assert.Equal(t, "| Claim | Evidence |", res["table"])
	assert.Equal(t, float64(2), res["rowCount"])
}

func TestAgentGateway_DefaultPythonExecutor_AskFollowUpBypassesSlowVertexDirectAndUsesSidecar(t *testing.T) {
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")

	var captured llmv1.StructuredRequest
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/llm/structured-output", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"jsonResult": `{"isAmbiguous":true,"question":"Which population are you studying?"}`,
			"modelUsed":  "test-gateway-follow-up-sidecar",
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

	gw := NewAgentGateway(nil, nil, nil)
	gw.Brain = NewBrainCapabilities(client)

	start := time.Now()
	res, err := gw.defaultPythonExecutor(context.Background(), "clarify.askFollowUpIfAmbiguous", map[string]any{
		"query": "sleep effects",
	}, nil)
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Less(t, elapsed, time.Second)
	assert.False(t, slowDirect.called.Load())
	assert.Equal(t, llm.ResolveStandardModel(), captured.Model)
	assertWisdevStructuredPromptHygiene(t, captured.Prompt)
	assert.Equal(t, true, res["isAmbiguous"])
	assert.Equal(t, "Which population are you studying?", res["question"])
}

func TestAgentGateway_SessionManagement(t *testing.T) {
	gw := NewAgentGateway(nil, nil, nil)
	ctx := context.Background()

	sess, err := gw.CreateSession(ctx, "u1", "  query  ")
	assert.NoError(t, err)
	assert.NotNil(t, sess)
	assert.Equal(t, "query", sess.OriginalQuery)
	assert.Equal(t, "query", sess.CorrectedQuery)

	loaded, err := gw.GetSession(ctx, sess.SessionID)
	assert.NoError(t, err)
	assert.Equal(t, sess.SessionID, loaded.SessionID)
	assert.Equal(t, "query", loaded.OriginalQuery)
	assert.Equal(t, "query", loaded.CorrectedQuery)
}

func TestAgentGateway_CreateSessionRejectsEmptyQuery(t *testing.T) {
	gw := NewAgentGateway(nil, nil, nil)
	_, err := gw.CreateSession(context.Background(), "u1", "   ")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "query is required")
}

func TestResolvePythonBase(t *testing.T) {
	t.Setenv("PYTHON_SIDECAR_HTTP_URL", "http://python-sidecar:8090/")
	assert.Equal(t, "http://python-sidecar:8090", ResolvePythonBase())
}

func TestResolvePythonBase_FallsBackToManifest(t *testing.T) {
	t.Setenv("PYTHON_SIDECAR_HTTP_URL", "")
	assert.NotEmpty(t, ResolvePythonBase())
}

func TestAgentGateway_ProgrammaticLoopExecutor(t *testing.T) {
	gw := &AgentGateway{}
	assert.NotNil(t, gw.ProgrammaticLoopExecutor())

	custom := func(ctx context.Context, action string, payload map[string]any, session *AgentSession) (map[string]any, error) {
		return map[string]any{"action": action}, nil
	}
	gw.PythonExecute = custom
	result, err := gw.ProgrammaticLoopExecutor()(context.Background(), "test.action", nil, nil)
	assert.NoError(t, err)
	assert.Equal(t, "test.action", result["action"])

	var nilGateway *AgentGateway
	assert.Nil(t, nilGateway.ProgrammaticLoopExecutor())
}

func TestAgentGateway_ExecuteADKAction_GoNativeSynthesisAndBatchVerifier(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)
	gw := NewAgentGateway(nil, nil, nil)
	gw.LLMClient = client
	gw.Brain = NewBrainCapabilities(client)
	gw.Registry = NewToolRegistry()

	ctx := context.Background()
	session := gw.ensureADKSessionWithContext(ctx, "session-1", "sleep memory", "")

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return req != nil &&
			req.Model != "" &&
			req.JsonSchema != "" &&
			strings.Contains(req.Prompt, "Synthesize a comprehensive research report") &&
			strings.Contains(req.Prompt, "sleep memory") &&
			strings.Contains(req.Prompt, "Sleep Study")
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"sections":[{"heading":"Answer","sentences":[{"text":"sleep supports memory consolidation","evidenceIds":["p1"]}]}]}`}, nil).Once()

	synthesis, err := gw.ExecuteADKAction(ctx, ToolDefinition{
		Name:            " research.synthesize-answer ",
		ExecutionTarget: ExecutionTargetGoNative,
	}, map[string]any{
		"query": "sleep memory",
		"evidence": []any{
			map[string]any{"id": "p1", "title": "Sleep Study", "summary": "REM sleep improves consolidation."},
		},
	}, session)
	require.NoError(t, err)
	assert.Equal(t, "## Answer\n\nsleep supports memory consolidation", synthesis["text"])
	assert.NotNil(t, synthesis["structuredAnswer"])

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return req != nil &&
			req.Model != "" &&
			req.JsonSchema != "" &&
			strings.Contains(req.Prompt, "Rank and verify these research findings") &&
			strings.Contains(req.Prompt, "candidate claim") &&
			strings.Contains(req.Prompt, "Sleep Study")
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"results":[{"verified":true,"score":0.87,"report":"supported"}]}`}, nil).Once()

	batch, err := gw.ExecuteADKAction(ctx, ToolDefinition{
		Name:            ActionResearchVerifyClaimsBatch,
		ExecutionTarget: ExecutionTargetGoNative,
	}, map[string]any{
		"candidateOutputs": []any{map[string]any{"claim": "candidate claim"}},
		"sources":          []any{map[string]any{"id": "p1", "title": "Sleep Study"}},
	}, session)
	require.NoError(t, err)
	assert.NotEmpty(t, batch["results"])
	mockLLM.AssertExpectations(t)
}

func TestAgentGateway_DefaultPythonExecutor_ExecuteLoopAndSnowballFallback(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)

	gw := NewAgentGateway(nil, nil, nil)
	gw.Brain = NewBrainCapabilities(client)
	gw.Loop = &AutonomousLoop{searchReg: search.NewProviderRegistry()}

	ctx := context.Background()

	t.Run("research.execute-loop", func(t *testing.T) {
		gw.Runtime = nil
		_, err := gw.defaultPythonExecutor(ctx, "research.execute-loop", map[string]any{
			"query": "   ",
		}, nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "query is required")
		assert.NotNil(t, gw.Runtime)
	})

	t.Run("research.execute-loop returns unified metadata shape", func(t *testing.T) {
		originalRunner := gatewayRunUnifiedResearchLoop
		t.Cleanup(func() {
			gatewayRunUnifiedResearchLoop = originalRunner
		})
		gw.Runtime = NewUnifiedResearchRuntime(nil, nil, nil, nil)
		gatewayRunUnifiedResearchLoop = func(_ context.Context, _ *UnifiedResearchRuntime, req LoopRequest, plane ResearchExecutionPlane, _ func(PlanExecutionEvent)) (*UnifiedResearchResult, error) {
			require.Equal(t, ResearchExecutionPlaneJob, plane)
			require.Equal(t, "gateway canonical query", req.Query)
			return &UnifiedResearchResult{
				State: &ResearchSessionState{
					Query:           req.Query,
					Plane:           ResearchExecutionPlaneJob,
					PlannedQueries:  []string{req.Query, "gateway follow up"},
					ExecutedQueries: []string{req.Query},
					BranchEvaluations: []ResearchBranchEvaluation{
						{ID: "gateway-branch", Query: req.Query, Status: "promote", VerifierVerdict: "promote"},
					},
					VerifierDecision: &ResearchVerifierDecision{
						Role:         ResearchWorkerIndependentVerifier,
						Verdict:      "promote",
						StopReason:   "verified_final",
						Confidence:   0.91,
						EvidenceOnly: true,
					},
					Workers: []ResearchWorkerState{
						{Role: ResearchWorkerScout, Status: "completed"},
					},
					Blackboard: &ResearchBlackboard{ReadyForSynthesis: true},
					StopReason: "verified_final",
				},
				LoopResult: &LoopResult{
					FinalAnswer: "gateway final",
					Papers: []search.Paper{
						{ID: "gateway-paper", Title: "Gateway Paper", Source: "crossref"},
					},
					FinalizationGate: &ResearchFinalizationGate{
						Status:          "promote",
						Ready:           true,
						Provisional:     false,
						StopReason:      "verified_final",
						VerifierVerdict: "promote",
					},
					StopReason: "verified_final",
				},
			}, nil
		}

		res, err := gw.defaultPythonExecutor(ctx, "research.execute-loop", map[string]any{
			"query": "gateway canonical query",
		}, nil)
		require.NoError(t, err)
		require.Equal(t, "unified_research_runtime", AsOptionalString(res["engine"]))
		require.Equal(t, "gateway final", AsOptionalString(res["finalAnswer"]))
		require.Equal(t, "verified", AsOptionalString(res["answerStatus"]))
		require.Len(t, res["branchEvaluations"].([]any), 1)
		require.Len(t, res["workerReports"].([]any), 1)
		require.Equal(t, "promote", AsOptionalString(asMap(res["finalizationGate"])["status"]))
	})

	t.Run("research.snowballCitations fallback", func(t *testing.T) {
		expectedQueries := []string{"query 1", "query 2"}
		mockLLM.On("StructuredOutput", mock.Anything, mock.Anything).Return(&llmv1.StructuredResponse{
			JsonResult: `{"queries":["query 1","query 2"]}`,
		}, nil).Once()

		res, err := gw.defaultPythonExecutor(ctx, "research.snowballCitations", map[string]any{
			"papers": []any{
				map[string]any{"id": "p1", "title": "Paper 1"},
			},
		}, nil)
		assert.NoError(t, err)
		assert.Equal(t, expectedQueries, res["exploratory_queries"])
		mockLLM.AssertExpectations(t)
	})
}
