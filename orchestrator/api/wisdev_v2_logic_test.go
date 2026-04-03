package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/policy"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
)

func TestWisDevV2_AuthHelpers(t *testing.T) {
	t.Run("requireOwnerAccess", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		// Set user in context
		ctx := context.WithValue(req.Context(), contextKey("user_id"), "u1")
		req = req.WithContext(ctx)

		rec := httptest.NewRecorder()
		allowed := requireOwnerAccess(rec, req, "u1")
		assert.True(t, allowed)

		allowed2 := requireOwnerAccess(rec, req, "u2")
		assert.False(t, allowed2)
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("resolveAuthorizedUserID", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		ctx := context.WithValue(req.Context(), contextKey("user_id"), "u1")
		req = req.WithContext(ctx)

		id, err := resolveAuthorizedUserID(req, "u1")
		assert.NoError(t, err)
		assert.Equal(t, "u1", id)

		_, err2 := resolveAuthorizedUserID(req, "someone-else")
		assert.Error(t, err2)
	})
}

func TestWisDevV2_LogicHelpers(t *testing.T) {
	t.Run("defaultPolicyPayload", func(t *testing.T) {
		gw := &wisdev.AgentGateway{
			PolicyConfig: policy.DefaultPolicyConfig(),
		}
		res := defaultPolicyPayload(gw, "u1", "")
		assert.NotNil(t, res["policy"])
	})

	t.Run("IntValue", func(t *testing.T) {
		assert.Equal(t, 10, IntValue(10))
		assert.Equal(t, 10, IntValue(10.0))
	})

	t.Run("AsFloat", func(t *testing.T) {
		assert.Equal(t, 10.5, AsFloat(10.5))
		assert.Equal(t, 10.0, AsFloat(10))
	})

	t.Run("validateEnum", func(t *testing.T) {
		assert.True(t, validateEnum("a", "a", "b"))
		assert.False(t, validateEnum("c", "a", "b"))
	})

	t.Run("validateRequiredString", func(t *testing.T) {
		assert.NoError(t, validateRequiredString("val", "field", 10))
		assert.Error(t, validateRequiredString("", "field", 10))
	})

	t.Run("validateStringSlice", func(t *testing.T) {
		assert.NoError(t, validateStringSlice([]string{"a"}, "f", 5, 10))
		assert.Error(t, validateStringSlice([]string{"a", "b"}, "f", 1, 10))
	})

	t.Run("boundedInt", func(t *testing.T) {
		assert.Equal(t, 5, boundedInt(5, 1, 1, 10))
	})

	t.Run("fullPaperHasTerminalStatus", func(t *testing.T) {
		assert.True(t, fullPaperHasTerminalStatus("completed"))
		assert.False(t, fullPaperHasTerminalStatus("running"))
	})

	t.Run("isAllowedFullPaperControlAction", func(t *testing.T) {
		job := map[string]any{"status": "running"}
		assert.NoError(t, isAllowedFullPaperControlAction(job, "pause", ""))
		assert.Error(t, isAllowedFullPaperControlAction(job, "resume", ""))
	})

	t.Run("normalizePolicyVersion", func(t *testing.T) {
		gw := &wisdev.AgentGateway{PolicyConfig: policy.PolicyConfig{PolicyVersion: "def"}}
		assert.Equal(t, "v1", normalizePolicyVersion(gw, " v1  ", nil))
	})
}

func TestWisDevV2_Concurrency(t *testing.T) {
	t.Run("withConcurrencyGuard", func(t *testing.T) {
		called := false
		err := withConcurrencyGuard("user1", 1, func() error {
			called = true
			return nil
		})
		assert.NoError(t, err)
		assert.True(t, called)
	})
}
