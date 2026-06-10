package wisdev

import (
	"context"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"google.golang.org/adk/agent"
	adkmemory "google.golang.org/adk/memory"
	"google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/adk/tool/toolconfirmation"
	"google.golang.org/genai"
)

type mockADKAgent struct {
	agent.Agent
	mock.Mock
}

type mockADKLLM struct{}

func (m *mockADKLLM) Name() string { return "mock-llm" }

func (m *mockADKLLM) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {}
}

func (m *mockADKAgent) Name() string {
	return m.Called().String(0)
}

func (m *mockADKAgent) SubAgents() []agent.Agent {
	args := m.Called()
	return args.Get(0).([]agent.Agent)
}

type mockADKToolContext struct {
	context.Context
	actions adksession.EventActions
}

func (m *mockADKToolContext) UserContent() *genai.Content { return nil }
func (m *mockADKToolContext) InvocationID() string        { return "tool-invocation" }
func (m *mockADKToolContext) AgentName() string           { return "tool-agent" }
func (m *mockADKToolContext) ReadonlyState() adksession.ReadonlyState {
	return mockState{}
}
func (m *mockADKToolContext) UserID() string  { return "tool-user" }
func (m *mockADKToolContext) AppName() string { return "wisdev-test" }
func (m *mockADKToolContext) SessionID() string {
	return "tool-session"
}
func (m *mockADKToolContext) Branch() string { return "" }
func (m *mockADKToolContext) Artifacts() agent.Artifacts {
	return nil
}
func (m *mockADKToolContext) State() adksession.State { return mockState{} }
func (m *mockADKToolContext) FunctionCallID() string  { return "tool-call" }
func (m *mockADKToolContext) Actions() *adksession.EventActions {
	return &m.actions
}
func (m *mockADKToolContext) SearchMemory(context.Context, string) (*adkmemory.SearchResponse, error) {
	return nil, nil
}
func (m *mockADKToolContext) ToolConfirmation() *toolconfirmation.ToolConfirmation {
	return nil
}
func (m *mockADKToolContext) RequestConfirmation(string, any) error { return nil }

func TestADKRuntime_Bind_ErrorPaths(t *testing.T) {
	oldProjectResolver := resolveADKProjectID
	oldKeyResolver := resolveADKGoogleAPIKey
	oldModelFactory := newGeminiModel
	oldLocalConfig := resolveADKLocalLLMConfig
	t.Cleanup(func() {
		resolveADKProjectID = oldProjectResolver
		resolveADKGoogleAPIKey = oldKeyResolver
		newGeminiModel = oldModelFactory
		resolveADKLocalLLMConfig = oldLocalConfig
	})

	resolveADKLocalLLMConfig = func() (string, string, string, bool) { return "", "", "", false }

	resolveADKProjectID = func(context.Context) (string, string) { return "", "none" }
	resolveADKGoogleAPIKey = func(context.Context, string) (string, string, error) { return "", "", nil }
	r := &ADKRuntime{
		Config: DefaultADKRuntimeConfig(),
	}
	r.Bind(context.Background(), nil) // Should return early
	assert.Empty(t, r.InitError)

	gateway := &AgentGateway{
		Registry: NewToolRegistry(),
	}
	r.Bind(context.Background(), gateway)
	assert.Contains(t, r.InitError, "no GOOGLE_API_KEY or GEMINI_API_KEY credential")
}

func TestADKRuntime_Bind_UsesLocalOpenAICompatibleWhenConfigured(t *testing.T) {
	oldProjectResolver := resolveADKProjectID
	oldKeyResolver := resolveADKGoogleAPIKey
	oldModelFactory := newGeminiModel
	oldLocalConfig := resolveADKLocalLLMConfig
	oldLocalClient := newADKOpenAICompatibleClient
	oldLocalModel := newADKOpenAICompatibleModel
	t.Cleanup(func() {
		resolveADKProjectID = oldProjectResolver
		resolveADKGoogleAPIKey = oldKeyResolver
		newGeminiModel = oldModelFactory
		resolveADKLocalLLMConfig = oldLocalConfig
		newADKOpenAICompatibleClient = oldLocalClient
		newADKOpenAICompatibleModel = oldLocalModel
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	resolveADKProjectID = func(context.Context) (string, string) { return "", "none" }
	resolveADKLocalLLMConfig = func() (string, string, string, bool) {
		return server.URL + "/v1", "llama3.1", "", true
	}
	newADKOpenAICompatibleClient = llm.NewOpenAICompatibleClient
	newADKOpenAICompatibleModel = func(client *llm.OpenAICompatibleClient) (model.LLM, error) {
		return &mockADKLLM{}, nil
	}

	r := &ADKRuntime{Config: DefaultADKRuntimeConfig()}
	gateway := &AgentGateway{Registry: NewToolRegistry()}
	r.Bind(context.Background(), gateway)

	assert.Equal(t, "openai_compatible", r.ModelBackend)
	assert.Contains(t, r.CredentialSource, server.URL)
	assert.Contains(t, r.InitError, "requires a *PlanExecutor")
}

func TestADKRuntime_Bind_UsesResolvedAPIKey(t *testing.T) {
	oldProjectResolver := resolveADKProjectID
	oldKeyResolver := resolveADKGoogleAPIKey
	oldModelFactory := newGeminiModel
	oldLocalConfig := resolveADKLocalLLMConfig
	t.Cleanup(func() {
		resolveADKProjectID = oldProjectResolver
		resolveADKGoogleAPIKey = oldKeyResolver
		newGeminiModel = oldModelFactory
		resolveADKLocalLLMConfig = oldLocalConfig
	})

	resolveADKLocalLLMConfig = func() (string, string, string, bool) { return "", "", "", false }

	resolveADKProjectID = func(context.Context) (string, string) { return "", "none" }
	resolveADKGoogleAPIKey = func(context.Context, string) (string, string, error) {
		return "secret-api-key", "secret_manager:GOOGLE_API_KEY", nil
	}

	var capturedCfg *genai.ClientConfig
	newGeminiModel = func(ctx context.Context, modelName string, cfg *genai.ClientConfig) (model.LLM, error) {
		capturedCfg = cfg
		return &mockADKLLM{}, nil
	}

	r := &ADKRuntime{
		Config: DefaultADKRuntimeConfig(),
	}
	gateway := &AgentGateway{
		Registry: NewToolRegistry(),
	}
	r.Bind(context.Background(), gateway)

	assert.NotNil(t, capturedCfg)
	assert.Equal(t, genai.BackendGeminiAPI, capturedCfg.Backend)
	assert.Equal(t, "secret-api-key", capturedCfg.APIKey)
	assert.Contains(t, r.InitError, "requires a *PlanExecutor")
}

func TestADKRuntime_SubAgentNames(t *testing.T) {
	r := &ADKRuntime{}

	t.Run("Nil agent", func(t *testing.T) {
		names := r.subAgentNames()
		assert.Empty(t, names)
	})

	t.Run("With subagents", func(t *testing.T) {
		m := new(mockADKAgent)
		sa1 := new(mockADKAgent)
		sa1.On("Name").Return("sub1")

		m.On("SubAgents").Return([]agent.Agent{sa1})
		r.Agent = m

		names := r.subAgentNames()
		assert.Equal(t, []string{"sub1"}, names)
	})
}

func TestADKRuntime_Metadata(t *testing.T) {
	r := &ADKRuntime{
		Config:    DefaultADKRuntimeConfig(),
		ToolCount: 5,
	}
	meta := r.Metadata()
	assert.True(t, meta["enabled"].(bool))
	assert.True(t, meta["configured"].(bool))
	assert.False(t, meta["ready"].(bool))
	assert.Equal(t, "initializing", meta["status"])
	assert.Equal(t, 5, meta["toolCount"])
	assert.Equal(t, "google-adk-go", meta["framework"])
	assert.Equal(t, "google.golang.org/adk", meta["canonicalWrapper"])
	assert.Equal(t, "adk_primary_direct_executor_fallback_only", meta["executionPolicy"])
	directExecutor, ok := meta["directExecutor"].(map[string]any)
	if assert.True(t, ok) {
		assert.Equal(t, true, directExecutor["fallbackOnly"])
	}
}

func TestADKRuntime_WrapGatewayTool(t *testing.T) {
	r := &ADKRuntime{}
	gateway := &AgentGateway{
		Registry: NewToolRegistry(),
	}
	toolDef := ToolDefinition{
		Name:        "test_tool",
		Description: "A test tool",
		Risk:        RiskLevelLow,
	}

	wrapped, err := r.wrapGatewayTool(gateway, toolDef)
	assert.NoError(t, err)
	assert.NotNil(t, wrapped)
	assert.Equal(t, "test_tool", wrapped.Name())
	assert.Equal(t, "A test tool", wrapped.Description())
}

func TestADKToolExecutionContextPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got := adkToolExecutionContext(&mockADKToolContext{Context: ctx})
	assert.ErrorIs(t, got.Err(), context.Canceled)

	fallback := adkToolExecutionContext(nil)
	assert.NoError(t, fallback.Err())
}

func TestPlanEventFromADKEventMapsNativeToolConfirmation(t *testing.T) {
	event := adksession.NewEvent("inv-confirm")
	event.Author = "wisdev-workflow"
	event.Actions.RequestedToolConfirmations = map[string]toolconfirmation.ToolConfirmation{
		"call-1": {
			Hint: "approve retrieval",
			Payload: map[string]any{
				"approvalId": "approval-1",
				"stepId":     "step-1",
			},
		},
	}
	event.Content = &genai.Content{
		Role: genai.RoleModel,
		Parts: []*genai.Part{
			{
				FunctionCall: &genai.FunctionCall{
					ID:   "confirm-1",
					Name: toolconfirmation.FunctionCallName,
					Args: map[string]any{
						"originalFunctionCall": &genai.FunctionCall{
							ID:   "call-1",
							Name: "research.retrievePapers",
							Args: map[string]any{
								"query": "sleep memory",
							},
						},
						"toolConfirmation": toolconfirmation.ToolConfirmation{Hint: "approve retrieval"},
					},
				},
			},
		},
	}

	got := planEventFromADKEvent(event, &AgentSession{SessionID: "session-1", Plan: &PlanState{PlanID: "plan-1"}})

	assert.Equal(t, EventConfirmationNeed, got.Type)
	assert.Equal(t, "session-1", got.SessionID)
	assert.Equal(t, "plan-1", got.PlanID)
	assert.Equal(t, "step-1", got.StepID)
	assert.Equal(t, "approve retrieval", got.Message)
	assert.Equal(t, true, got.Payload["adkConfirmationRequired"])
	assert.Equal(t, toolconfirmation.FunctionCallName, got.Payload["adkConfirmationFunction"])
	assert.Equal(t, "confirm-1", got.Payload["adkConfirmationRequestId"])
	assert.Equal(t, "call-1", got.Payload["adkOriginalFunctionCallId"])
	assert.Equal(t, "research.retrievePapers", got.Payload["action"])
	assert.Equal(t, "sleep memory", got.Payload["query"])
	assert.Equal(t, "google.golang.org/adk", got.Payload["adkRuntime"])
	assert.Equal(t, "inv-confirm", got.Payload["adkInvocationId"])
}

func TestResolveADKConfigPath(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "wisdev-adk.yaml")
	os.WriteFile(tmpFile, []byte("test"), 0644)

	os.Setenv("WISDEV_ADK_CONFIG", tmpFile)
	defer os.Unsetenv("WISDEV_ADK_CONFIG")
	assert.Equal(t, tmpFile, ResolveADKConfigPath())
}

func TestADKRuntime_RegisterPlugins(t *testing.T) {
	r := &ADKRuntime{
		Config:     DefaultADKRuntimeConfig(),
		toolToPlug: make(map[string]ADKPluginConfig),
	}
	registry := NewToolRegistry()
	registry.Register(ToolDefinition{
		Name:            "search_papers",
		ExecutionTarget: ExecutionTargetPythonCapability,
	})

	r.registerPlugins(registry)

	p, ok := r.PluginForAction("search_papers")
	assert.True(t, ok)
	assert.Equal(t, "python-capability-tools", p.Name)

	p, ok = r.PluginForAction(" " + ActionResearchSynthesizeAnswer + " ")
	assert.True(t, ok)
	assert.Equal(t, "go-native-tools", p.Name)
}

func TestADKRuntime_BuildHITLRequest(t *testing.T) {
	r := &ADKRuntime{
		Config: DefaultADKRuntimeConfig(),
	}
	step := PlanStep{
		Action: "some_action",
		Risk:   RiskLevelHigh,
	}
	req := r.BuildHITLRequest("tok123", step, "Testing HITL")
	assert.Equal(t, "tok123", req["approvalToken"])
	assert.Equal(t, "some_action", req["action"])
	assert.Equal(t, "Testing HITL", req["rationale"])
	assert.Equal(t, toolconfirmation.FunctionCallName, req["adkConfirmationFunction"])
	confirmation, ok := req["adkRequestedToolConfirmation"].(toolconfirmation.ToolConfirmation)
	require.True(t, ok)
	assert.Equal(t, "Testing HITL", confirmation.Hint)
}

func TestADKRuntime_BuildA2ACard(t *testing.T) {
	r := &ADKRuntime{
		Config: DefaultADKRuntimeConfig(),
	}
	r.Config.A2A.Enabled = true
	r.Config.A2A.ExposeAgentCard = true
	r.Config.Runtime.Name = "TestAgent"

	card := r.BuildA2ACard()
	assert.Equal(t, "TestAgent", card["name"])
	assert.Equal(t, false, card["ready"])
	assert.Equal(t, "initializing", card["status"])
}
