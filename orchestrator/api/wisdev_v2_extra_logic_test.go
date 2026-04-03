package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWisDevV2_MoreHelpers(t *testing.T) {
	t.Run("validatePayloadSize", func(t *testing.T) {
		assert.NoError(t, validatePayloadSize(map[string]string{"a": "b"}, "f", 100))
		assert.NoError(t, validatePayloadSize(map[string]string{"a": "b"}, "f", 0)) // skip
		assert.Error(t, validatePayloadSize(map[string]string{"a": "very long string"}, "f", 5))
	})

	t.Run("isAllowedFullPaperCheckpointAction", func(t *testing.T) {
		job := map[string]any{
			"status": "running",
			"pendingCheckpoint": map[string]any{"stageId": "retrieval"},
		}
		assert.NoError(t, isAllowedFullPaperCheckpointAction(job, "retrieval"))
		
		job["status"] = "completed"
		assert.Error(t, isAllowedFullPaperCheckpointAction(job, "retrieval"))
	})

	t.Run("validateOptionalString", func(t *testing.T) {
		assert.NoError(t, validateOptionalString("val", "f", 10))
		assert.NoError(t, validateOptionalString("", "f", 10))
		assert.Error(t, validateOptionalString("too long", "f", 2))
	})
}
