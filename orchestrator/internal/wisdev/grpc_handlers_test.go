package wisdev

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/policy"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	wisdevpb "github.com/wisdev/wisdev-agent-os/orchestrator/proto/wisdev"
)

type mockSessionStore struct {
	mock.Mock
}

func (m *mockSessionStore) Put(ctx context.Context, session *AgentSession, ttl time.Duration) error {
	return m.Called(ctx, session, ttl).Error(0)
}
func (m *mockSessionStore) Get(ctx context.Context, sessionID string) (*AgentSession, error) {
	args := m.Called(ctx, sessionID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*AgentSession), args.Error(1)
}
func (m *mockSessionStore) Delete(ctx context.Context, sessionID string) error {
	return m.Called(ctx, sessionID).Error(0)
}
func (m *mockSessionStore) List(ctx context.Context, userID string) ([]*AgentSession, error) {
	args := m.Called(ctx, userID)
	return args.Get(0).([]*AgentSession), args.Error(1)
}
func (m *mockSessionStore) GetMemoryTiers(ctx context.Context, sessionID string) *MemoryTierState {
	args := m.Called(ctx, sessionID)
	if args.Get(0) == nil {
		return &MemoryTierState{}
	}
	return args.Get(0).(*MemoryTierState)
}

func TestAgentGateway_GRPC_Handlers(t *testing.T) {
	store := new(mockSessionStore)
	gw := &AgentGateway{
		Store:         store,
		Registry:      NewToolRegistry(),
		Checkpoints:   NewInMemoryCheckpointStore(),
		CheckpointTTL: time.Hour,
	}
	srv := &agentGatewayGRPCServer{gateway: gw}

	t.Run("CreateSession", func(t *testing.T) {
		req := &wisdevpb.CreateSessionRequest{
			UserId: "u1",
			Query:  "test query",
		}
		store.On("Put", mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
		resp, err := srv.CreateSession(context.Background(), req)
		assert.NoError(t, err)
		assert.Equal(t, "u1", resp.Session.UserId)
	})

	t.Run("GetSession", func(t *testing.T) {
		session := &AgentSession{SessionID: "s1", UserID: "u1"}
		store.On("Get", mock.Anything, "s1").Return(session, nil).Once()
		resp, err := srv.GetSession(context.Background(), &wisdevpb.GetSessionRequest{SessionId: "s1"})
		assert.NoError(t, err)
		assert.Equal(t, "s1", resp.Session.SessionId)
	})

	t.Run("ListTools", func(t *testing.T) {
		gw.Registry.Register(ToolDefinition{Name: "t1"})
		resp, err := srv.ListTools(context.Background(), &wisdevpb.ListToolsRequest{})
		assert.NoError(t, err)
		assert.NotEmpty(t, resp.Tools)
	})

	t.Run("InvokeToolUsesCanonicalPlanningQuery", func(t *testing.T) {
		registry := search.NewProviderRegistry()
		registry.Register(&mockSearchProvider{
			name: "grpc-mcp-provider",
			SearchFunc: func(ctx context.Context, query string, opts search.SearchOpts) ([]search.Paper, error) {
				assert.Equal(t, "planner canonical query", query)
				assert.Equal(t, 10, opts.Limit)
				return []search.Paper{{ID: "paper-1", Title: "Planner Query Result"}}, nil
			},
		})
		registry.SetDefaultOrder([]string{"grpc-mcp-provider"})
		gw.SearchRegistry = registry

		gw.Registry.Register(ToolDefinition{
			Name:            "parallel_search",
			ExecutionTarget: ExecutionTargetGoNative,
		})
		session := &AgentSession{
			SessionID:      "s-search",
			Query:          "planner canonical query",
			CorrectedQuery: "legacy corrected query",
			OriginalQuery:  "legacy original query",
		}
		store.On("Get", mock.Anything, "s-search").Return(session, nil).Once()

		resp, err := srv.InvokeTool(context.Background(), &wisdevpb.InvokeToolRequest{
			SessionId: "s-search",
			ToolName:  "parallel_search",
		})
		assert.NoError(t, err)
		assert.True(t, resp.Ok)

		var payload map[string]any
		assert.NoError(t, json.Unmarshal([]byte(resp.ResultJson), &payload))
		assert.Equal(t, "planner canonical query", payload["queryUsed"])
	})

	t.Run("SaveLoadCheckpoint", func(t *testing.T) {
		session := &AgentSession{SessionID: "s1"}
		store.On("Get", mock.Anything, "s1").Return(session, nil).Once()

		_, err := srv.SaveCheckpoint(context.Background(), &wisdevpb.SaveCheckpointRequest{SessionId: "s1"})
		assert.NoError(t, err)

		resp, err := srv.LoadCheckpoint(context.Background(), &wisdevpb.LoadCheckpointRequest{SessionId: "s1"})
		assert.NoError(t, err)
		assert.Equal(t, "s1", resp.Session.SessionId)
	})

	t.Run("ProgrammaticLoopUsesExecutorBackedTreeLoop", func(t *testing.T) {
		gw.PolicyConfig = policy.DefaultPolicyConfig()
		session := &AgentSession{SessionID: "s1", Budget: policy.BudgetState{MaxToolCalls: 5}}
		store.On("Get", mock.Anything, "s1").Return(session, nil).Once()
		store.On("Put", mock.Anything, mock.MatchedBy(func(sess *AgentSession) bool {
			return sess != nil && sess.SessionID == "s1"
		}), mock.Anything).Return(nil).Maybe()
		gw.PythonExecute = func(ctx context.Context, action string, payload map[string]any, session *AgentSession) (map[string]any, error) {
			switch action {
			case "research.queryDecompose":
				return map[string]any{"tasks": []any{"decompose"}}, nil
			case "research.generateThoughts":
				return map[string]any{
					"branches": []any{
						map[string]any{
							"nodes": []any{
								map[string]any{"label": "branch-a"},
							},
						},
					},
				}, nil
			default:
				return map[string]any{"ok": true}, nil
			}
		}

		resp, err := srv.ProgrammaticLoop(context.Background(), &wisdevpb.ProgrammaticLoopRequest{
			SessionId:     "s1",
			Action:        "research.queryDecompose",
			PayloadJson:   `{"query":"sleep memory"}`,
			MaxIterations: 2,
		})
		assert.NoError(t, err)
		assert.True(t, resp.Completed)
		assert.NotEmpty(t, resp.Iterations)
		assert.NotEmpty(t, resp.Iterations[0].OutputJson)
	})
}
