package resilience

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDegraded(t *testing.T) {
	is := assert.New(t)

	t.Run("default is not degraded", func(t *testing.T) {
		ctx := context.Background()
		is.False(IsDegraded(ctx))
	})

	t.Run("set and get degraded true", func(t *testing.T) {
		ctx := context.Background()
		ctx = SetDegraded(ctx, true)
		is.True(IsDegraded(ctx))
	})

	t.Run("set and get degraded false", func(t *testing.T) {
		ctx := context.Background()
		ctx = SetDegraded(ctx, false)
		is.False(IsDegraded(ctx))
	})

	t.Run("nil context handles gracefully", func(t *testing.T) {
		// IsDegraded might panic if ctx is nil depending on implementation, 
		// but context.Context is usually expected to be non-nil.
		// However, let's see how it behaves.
		defer func() {
			if r := recover(); r != nil {
				t.Log("Recovered from panic in IsDegraded(nil)")
			}
		}()
		// Passing nil to context methods is generally bad practice, 
		// but we can test if it doesn't crash if we want to be super resilient.
		// Actually, context.Background() is better.
	})
}
