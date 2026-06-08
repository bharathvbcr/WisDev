package resilience

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestTokenBudget(t *testing.T) {
	is := assert.New(t)

	t.Run("allocation within budget", func(t *testing.T) {
		tb := NewTokenBudget("req-1", "user-1", 1000)
		err := tb.Allocate("llm_call", 500)
		is.NoError(err)
		is.Equal(500, tb.Usage())
		is.Equal(500, tb.Remaining())
		is.Equal(0.5, tb.Utilization())
	})

	t.Run("allocation exceeds budget", func(t *testing.T) {
		tb := NewTokenBudget("req-1", "user-1", 100)
		err := tb.Allocate("llm_call", 500)
		is.Error(err)
		is.True(tb.Exceeded())
		is.Contains(err.Error(), "insufficient budget")
	})

	t.Run("allocate operation by type", func(t *testing.T) {
		tb := NewTokenBudget("req-1", "user-1", 1000)
		err := tb.AllocateOperation("search_api")
		is.NoError(err)
		is.Equal(100, tb.Usage())
	})

	t.Run("unknown operation type", func(t *testing.T) {
		tb := NewTokenBudget("req-1", "user-1", 1000)
		err := tb.AllocateOperation("unknown")
		is.Error(err)
		is.Contains(err.Error(), "unknown operation type")
	})

	t.Run("record manual usage", func(t *testing.T) {
		tb := NewTokenBudget("req-1", "user-1", 1000)
		err := tb.RecordUsage("custom", 200)
		is.NoError(err)
		is.Equal(200, tb.Usage())
	})

	t.Run("report string contains details", func(t *testing.T) {
		tb := NewTokenBudget("req-1", "user-1", 1000)
		_ = tb.RecordUsage("custom", 100)
		report := tb.Report()
		is.Contains(report, "req-1")
		is.Contains(report, "user-1")
		is.Contains(report, "10.0%")
	})
}

func TestBudgetEnforcer(t *testing.T) {
	is := assert.New(t)
	tb := NewTokenBudget("req-1", "user-1", 1000)
	be := NewBudgetEnforcer(tb)

	t.Run("execute with sufficient budget", func(t *testing.T) {
		called := false
		err := be.ExecuteWithBudget(context.Background(), "llm_call", func(ctx context.Context) error {
			called = true
			return nil
		})
		is.NoError(err)
		is.True(called)
		is.Equal(500, tb.Usage())
	})

	t.Run("execute with deadline exceeded", func(t *testing.T) {
		tb.deadline = time.Now().Add(-1 * time.Second)
		err := be.ExecuteWithBudget(context.Background(), "search_api", func(ctx context.Context) error {
			return nil
		})
		is.Error(err)
		is.Contains(err.Error(), "deadline exceeded")
	})
}

func TestBudgetAwareAgentLoop(t *testing.T) {
	is := assert.New(t)
	tb := NewTokenBudget("req-1", "user-1", 1000)
	loop := NewBudgetAwareAgentLoop(tb, 3, 100)

	t.Run("iteration lifecycle", func(t *testing.T) {
		is.True(loop.CanIterate())

		err := loop.RecordIteration()
		is.NoError(err)
		is.Equal(1, loop.IterationCount())
		is.Equal(100, tb.Usage())

		_ = loop.RecordIteration()
		_ = loop.RecordIteration()
		is.Equal(3, loop.IterationCount())
		is.True(loop.ReachedIterationLimit())
		is.False(loop.CanIterate())
	})

	t.Run("budget limit stops iteration", func(t *testing.T) {
		tb2 := NewTokenBudget("req-2", "user-2", 50)
		loop2 := NewBudgetAwareAgentLoop(tb2, 10, 100)
		is.False(loop2.CanIterate())
	})
}

func TestTokenBudgetRemainingBranches(t *testing.T) {
	t.Run("allocate after exceeded flag", func(t *testing.T) {
		tb := NewTokenBudget("req-flag", "user", 100)
		atomic.StoreInt32(&tb.exceeded, 1)
		err := tb.Allocate("search_api", 1)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "budget exceeded")
	})

	t.Run("record usage over budget", func(t *testing.T) {
		tb := NewTokenBudget("req-record", "user", 50)
		err := tb.RecordUsage("custom", 60)
		assert.Error(t, err)
		assert.True(t, tb.Exceeded())
	})

	t.Run("execute with canceled context", func(t *testing.T) {
		tb := NewTokenBudget("req-cancel", "user", 1000)
		be := NewBudgetEnforcer(tb)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := be.ExecuteWithBudget(ctx, "search_api", func(ctx context.Context) error {
			return nil
		})
		assert.Error(t, err)
	})

	t.Run("execute with allocation error", func(t *testing.T) {
		tb := NewTokenBudget("req-alloc", "user", 0)
		be := NewBudgetEnforcer(tb)
		err := be.ExecuteWithBudget(context.Background(), "search_api", func(ctx context.Context) error {
			return nil
		})
		assert.Error(t, err)
	})
}

func TestTokenBudgetUsageRatioEdges(t *testing.T) {
	t.Run("zero allocation is safe", func(t *testing.T) {
		tb := NewTokenBudget("req-zero", "user", 0)
		assert.Equal(t, 0.0, tb.UsageRatio())
	})

	t.Run("reports fractional usage", func(t *testing.T) {
		tb := NewTokenBudget("req-ratio", "user", 400)
		assert.NoError(t, tb.RecordUsage("search", 100))
		assert.Equal(t, 0.25, tb.UsageRatio())
	})
}
