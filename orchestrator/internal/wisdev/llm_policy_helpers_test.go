package wisdev

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWisdevStructuredOutputCanUseTimeoutFallbackProviderDeadlineExpired(t *testing.T) {
	err := errors.New("generate structured content failed: Error 504, Message: Deadline expired before operation could complete., Status: DEADLINE_EXCEEDED, Details: []")

	assert.True(t, wisdevStructuredOutputCanUseTimeoutFallback(err))
}

func TestWisdevStructuredOutputContentBlockedClassification(t *testing.T) {
	// The sidecar safety-block error as the orchestrator forwards it (422 + code).
	blocked := errors.New("python structured output returned 422: Gemini returned empty text (CONTENT_BLOCKED)")
	assert.True(t, wisdevStructuredOutputContentBlocked(blocked))
	// A content block recovers via the deterministic path...
	assert.True(t, wisdevStructuredOutputCanUseDeterministicFallback(blocked))
	// ...but must never be treated as a timeout (no deadline extension/retry).
	assert.False(t, wisdevStructuredOutputCanUseTimeoutFallback(blocked))

	// A prompt-level block with no "empty text" prose is still classified as a block.
	promptBlock := errors.New("structured output failed: content blocked by safety policy")
	assert.True(t, wisdevStructuredOutputContentBlocked(promptBlock))
	assert.True(t, wisdevStructuredOutputCanUseDeterministicFallback(promptBlock))

	// Non-blocks are unaffected.
	assert.False(t, wisdevStructuredOutputContentBlocked(errors.New("structured output returned empty text")))
	assert.False(t, wisdevStructuredOutputContentBlocked(nil))
}
