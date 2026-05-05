package resilience

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestCircuitBreaker(t *testing.T) {
	is := assert.New(t)

	t.Run("initial state is closed", func(t *testing.T) {
		cb := NewCircuitBreaker("test")
		is.Equal(StateClosed, cb.State())
	})

	t.Run("transitions to open after failures", func(t *testing.T) {
		cb := NewCircuitBreaker("test")
		cb.maxFailures = 2

		// First failure
		err := cb.Call(context.Background(), func(ctx context.Context) error {
			return errors.New("fail")
		})
		is.Error(err)
		is.Equal(StateClosed, cb.State())

		// Second failure -> Open
		err = cb.Call(context.Background(), func(ctx context.Context) error {
			return errors.New("fail")
		})
		is.Error(err)
		is.Equal(StateOpen, cb.State())

		// Subsequent call should fail fast
		err = cb.Call(context.Background(), func(ctx context.Context) error {
			return nil
		})
		is.Error(err)
		is.Contains(err.Error(), "circuit breaker test is open")
	})

	t.Run("transitions to half-open after timeout", func(t *testing.T) {
		cb := NewCircuitBreaker("test")
		cb.maxFailures = 1
		cb.resetTimeout = 10 * time.Millisecond

		// Trip
		_ = cb.Call(context.Background(), func(ctx context.Context) error {
			return errors.New("fail")
		})
		is.Equal(StateOpen, cb.State())

		// Wait for timeout
		time.Sleep(20 * time.Millisecond)

		// Next call should be allowed (half-open)
		callExecuted := false
		err := cb.Call(context.Background(), func(ctx context.Context) error {
			callExecuted = true
			return nil
		})
		is.NoError(err)
		is.True(callExecuted)
		// It might stay half-open until success threshold reached
	})

	t.Run("closes after success threshold in half-open", func(t *testing.T) {
		cb := NewCircuitBreaker("test")
		cb.maxFailures = 1
		cb.resetTimeout = 10 * time.Millisecond
		cb.successThreshold = 2

		// Trip
		_ = cb.Call(context.Background(), func(ctx context.Context) error {
			return errors.New("fail")
		})
		is.Equal(StateOpen, cb.State())

		// Wait for timeout
		time.Sleep(20 * time.Millisecond)

		// First success in half-open
		_ = cb.Call(context.Background(), func(ctx context.Context) error {
			return nil
		})
		// Should still be half-open (internally, State() might return half_open or closed depending on implementation)
		// In this implementation, Call() transitions state.
		
		// Second success -> Closed
		_ = cb.Call(context.Background(), func(ctx context.Context) error {
			return nil
		})
		is.Equal(StateClosed, cb.State())
	})

	t.Run("manual reset", func(t *testing.T) {
		cb := NewCircuitBreaker("test")
		cb.maxFailures = 1
		_ = cb.Call(context.Background(), func(ctx context.Context) error {
			return errors.New("fail")
		})
		is.Equal(StateOpen, cb.State())

		cb.Reset()
		is.Equal(StateClosed, cb.State())
		is.Equal(0, cb.failureCount)
	})

	t.Run("metrics tracking", func(t *testing.T) {
		cb := NewCircuitBreaker("test")
		_ = cb.Call(context.Background(), func(ctx context.Context) error { return nil })
		_ = cb.Call(context.Background(), func(ctx context.Context) error { return errors.New("fail") })

		m := cb.Metrics()
		is.Equal("test", m.Name)
		is.Equal(int64(2), m.TotalRequests)
		is.Equal(int64(1), m.TotalErrors)
		is.Equal(0.5, m.ErrorRate)
	})
}

func TestCircuitBreakerRegistry(t *testing.T) {
	is := assert.New(t)

	t.Run("get or create", func(t *testing.T) {
		reg := NewCircuitBreakerRegistry()
		cb1 := reg.GetOrCreate("service-a")
		cb2 := reg.GetOrCreate("service-a")
		is.Same(cb1, cb2)

		cb3 := reg.GetOrCreate("service-b")
		is.NotSame(cb1, cb3)
	})

	t.Run("all and reset all", func(t *testing.T) {
		reg := NewCircuitBreakerRegistry()
		reg.GetOrCreate("a")
		reg.GetOrCreate("b")
		all := reg.All()
		is.Len(all, 2)

		reg.ResetAll()
		for _, cb := range all {
			is.Equal(StateClosed, cb.State())
		}
	})

	t.Run("health report", func(t *testing.T) {
		reg := NewCircuitBreakerRegistry()
		cb := reg.GetOrCreate("healthy")
		_ = cb.Call(context.Background(), func(ctx context.Context) error { return nil })

		cb2 := reg.GetOrCreate("unhealthy")
		cb2.maxFailures = 1
		_ = cb2.Call(context.Background(), func(ctx context.Context) error { return errors.New("fail") })

		report := reg.HealthReport()
		is.Equal(2, report.TotalBreakers)
		is.Equal(1, report.HealthyBreakers)
		is.Equal("healthy", report.HealthStatus)
	})

	t.Run("degraded health report", func(t *testing.T) {
		reg := NewCircuitBreakerRegistry()
		cb1 := reg.GetOrCreate("a")
		cb1.maxFailures = 1
		_ = cb1.Call(context.Background(), func(ctx context.Context) error { return errors.New("fail") })
		cb2 := reg.GetOrCreate("b")
		cb2.maxFailures = 1
		_ = cb2.Call(context.Background(), func(ctx context.Context) error { return errors.New("fail") })

		report := reg.HealthReport()
		is.Equal("degraded", report.HealthStatus)
		is.Equal(0, report.HealthyBreakers)
	})
}

type mockFallback struct {
	called bool
}

func (m *mockFallback) Fallback(ctx context.Context) error {
	m.called = true
	return nil
}
func (m *mockFallback) Name() string { return "mock" }

func TestCircuitBreakerWithFallback(t *testing.T) {
	is := assert.New(t)
	cb := NewCircuitBreaker("test")
	cb.maxFailures = 1
	fallback := &mockFallback{}
	cbf := NewCircuitBreakerWithFallback(cb, fallback)

	t.Run("uses fallback when open", func(t *testing.T) {
		// Trip it
		_ = cb.Call(context.Background(), func(ctx context.Context) error { return errors.New("fail") })
		is.Equal(StateOpen, cb.State())

		err := cbf.Call(context.Background(), func(ctx context.Context) error { return nil })
		is.NoError(err)
		is.True(fallback.called)
	})

	t.Run("returns open error without fallback", func(t *testing.T) {
		cb2 := NewCircuitBreaker("test2")
		cb2.maxFailures = 1
		_ = cb2.Call(context.Background(), func(ctx context.Context) error { return errors.New("fail") })

		cbf2 := NewCircuitBreakerWithFallback(cb2, nil)
		err := cbf2.Call(context.Background(), func(ctx context.Context) error { return nil })
		is.Error(err)
		is.Contains(err.Error(), "circuit breaker test2 is open")
	})
}

func TestFallbackStrategies(t *testing.T) {
	t.Run("cached response fallback", func(t *testing.T) {
		fb := &CachedResponseFallback{}
		err := fb.Fallback(context.Background())
		assert.Error(t, err)
		assert.Equal(t, "cached_response", fb.Name())
	})

	t.Run("degraded mode fallback nil fn", func(t *testing.T) {
		fb := &DegradedModeFallback{}
		err := fb.Fallback(context.Background())
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "degraded mode not available")
		assert.Equal(t, "degraded_mode", fb.Name())
	})

	t.Run("degraded mode fallback runs fn", func(t *testing.T) {
		called := false
		fb := &DegradedModeFallback{
			degradedFn: func(ctx context.Context) error {
				called = true
				return nil
			},
		}
		err := fb.Fallback(context.Background())
		assert.NoError(t, err)
		assert.True(t, called)
	})
}
