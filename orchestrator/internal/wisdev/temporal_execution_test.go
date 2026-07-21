package wisdev

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveTemporalConfigDefaults(t *testing.T) {
	t.Setenv("TEMPORAL_ENABLED", "")
	t.Setenv("TEMPORAL_ADDRESS", "")
	t.Setenv("TEMPORAL_NAMESPACE", "")
	t.Setenv("TEMPORAL_TASK_QUEUE", "")

	cfg := ResolveTemporalConfig()
	assert.False(t, cfg.Enabled)
	assert.Empty(t, cfg.Address)
	assert.Equal(t, "default", cfg.Namespace)
	assert.Equal(t, "wisdev-execution", cfg.TaskQueue)
}

func TestResolveTemporalConfig_FromEnvironment(t *testing.T) {
	t.Setenv("TEMPORAL_ENABLED", "true")
	t.Setenv("TEMPORAL_ADDRESS", "127.0.0.1:7233")
	t.Setenv("TEMPORAL_NAMESPACE", "custom-namespace")
	t.Setenv("TEMPORAL_TASK_QUEUE", "custom-queue")

	cfg := ResolveTemporalConfig()
	assert.True(t, cfg.Enabled)
	assert.Equal(t, "127.0.0.1:7233", cfg.Address)
	assert.Equal(t, "custom-namespace", cfg.Namespace)
	assert.Equal(t, "custom-queue", cfg.TaskQueue)
}

func TestResolveTemporalConfig_EnabledByAddress(t *testing.T) {
	t.Setenv("TEMPORAL_ENABLED", "0")
	t.Setenv("TEMPORAL_ADDRESS", "127.0.0.1:7233")
	t.Setenv("TEMPORAL_NAMESPACE", "")
	t.Setenv("TEMPORAL_TASK_QUEUE", "")

	cfg := ResolveTemporalConfig()
	assert.True(t, cfg.Enabled)
	assert.Equal(t, "127.0.0.1:7233", cfg.Address)
	assert.Equal(t, "default", cfg.Namespace)
	assert.Equal(t, "wisdev-execution", cfg.TaskQueue)
}

func TestNewTemporalClient(t *testing.T) {
	client, err := NewTemporalClient(TemporalConfig{})
	require.Error(t, err)
	assert.Nil(t, client)
	assert.Equal(t, "temporal is not configured", err.Error())

	client, err = NewTemporalClient(TemporalConfig{
		Enabled:   true,
		Address:   "127.0.0.1:0",
		Namespace: "default",
		TaskQueue: "wisdev-execution",
	})
	require.Error(t, err)
	assert.Nil(t, client)
}

func TestNewTemporalClient_DisabledByAddress(t *testing.T) {
	client, err := NewTemporalClient(TemporalConfig{
		Enabled: true,
	})
	require.Error(t, err)
	assert.Nil(t, client)
	assert.Equal(t, "temporal is not configured", err.Error())
}

func TestStartTemporalWorker_ErrorPaths(t *testing.T) {
	_, err := StartTemporalWorker(nil, nil, TemporalConfig{})
	require.ErrorContains(t, err, "gateway is required")

	stopFn, err := StartTemporalWorker(&AgentGateway{}, nil, TemporalConfig{})
	assert.Nil(t, stopFn)
	require.ErrorContains(t, err, "temporal client is required")
}

func TestNewTemporalExecutionService(t *testing.T) {
	client, err := NewTemporalClient(TemporalConfig{Enabled: true, Address: "127.0.0.1:0"})
	require.Error(t, err)
	assert.Nil(t, client)

	service := NewTemporalExecutionService(nil, nil, TemporalConfig{
		Enabled:   true,
		Address:   "127.0.0.1:0",
		TaskQueue: "tasks",
	})
	require.NotNil(t, service)
	assert.Equal(t, "tasks", service.taskQueue)
	assert.Nil(t, service.gateway)
	assert.Nil(t, service.client)
}

func TestTemporalWorkflowID(t *testing.T) {
	got := temporalWorkflowID("  session-001  ")
	assert.Equal(t, "wisdev-session-session-001", got)
}
