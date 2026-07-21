package wisdev

import (
	"sync"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/resilience"
)

type CircuitBreakerOption func(*CircuitBreaker)

type CircuitBreaker struct {
	name             string
	failureThreshold int
	resetTimeout     time.Duration

	mu                  sync.Mutex
	consecutiveFailures int
	openedAt            time.Time
}

func NewCircuitBreaker(name string, opts ...CircuitBreakerOption) *CircuitBreaker {
	cb := &CircuitBreaker{
		name:             name,
		failureThreshold: 5,
		resetTimeout:     20 * time.Second,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(cb)
		}
	}
	return cb
}

func WithFailureThreshold(threshold int) CircuitBreakerOption {
	return func(cb *CircuitBreaker) {
		if threshold > 0 {
			cb.failureThreshold = threshold
		}
	}
}

func WithResetTimeout(timeout time.Duration) CircuitBreakerOption {
	return func(cb *CircuitBreaker) {
		if timeout > 0 {
			cb.resetTimeout = timeout
		}
	}
}

func (cb *CircuitBreaker) Allow() bool {
	if cb == nil {
		return true
	}
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.openedAt.IsZero() {
		return true
	}
	if time.Since(cb.openedAt) >= cb.resetTimeout {
		cb.openedAt = time.Time{}
		cb.consecutiveFailures = 0
		return true
	}
	return false
}

func (cb *CircuitBreaker) RecordSuccess() {
	if cb == nil {
		return
	}
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.consecutiveFailures = 0
	cb.openedAt = time.Time{}
}

func (cb *CircuitBreaker) RecordFailure() {
	cb.RecordFailureFor(nil)
}

func (cb *CircuitBreaker) RecordFailureFor(err error) {
	if cb == nil {
		return
	}
	if err != nil && !resilience.ShouldTripCircuitBreaker(err) {
		return
	}
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.consecutiveFailures++
	if cb.consecutiveFailures >= cb.failureThreshold && cb.openedAt.IsZero() {
		cb.openedAt = time.Now()
	}
}
