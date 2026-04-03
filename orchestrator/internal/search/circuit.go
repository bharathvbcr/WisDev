package search

import (
	"sync"
	"time"
)

// CircuitState represents the current state of a circuit breaker.
type CircuitState int

const (
	StateClosed CircuitState = iota
	StateOpen
	StateHalfOpen
)

// CircuitBreaker prevents cascading failures by failing fast when a provider is down.
type CircuitBreaker struct {
	name         string
	maxFailures  int
	resetTimeout time.Duration

	mu           sync.RWMutex
	state        CircuitState
	failureCount int
	lastFailure  time.Time
	successCount int
}

func NewCircuitBreaker(name string, maxFailures int, resetTimeout time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		name:         name,
		maxFailures:  maxFailures,
		resetTimeout: resetTimeout,
		state:        StateClosed,
	}
}

func (cb *CircuitBreaker) Allow() bool {
	cb.mu.RLock()
	state := cb.state
	cb.mu.RUnlock()

	if state == StateClosed {
		return true
	}

	if state == StateOpen {
		if time.Since(cb.lastFailure) > cb.resetTimeout {
			cb.mu.Lock()
			cb.state = StateHalfOpen
			cb.mu.Unlock()
			return true
		}
		return false
	}

	// Half-open: allow one request to test
	return true
}

func (cb *CircuitBreaker) RecordResult(err error) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if err != nil {
		cb.failureCount++
		cb.lastFailure = time.Now()
		if cb.state == StateClosed && cb.failureCount >= cb.maxFailures {
			cb.state = StateOpen
		} else if cb.state == StateHalfOpen {
			cb.state = StateOpen
		}
		cb.successCount = 0
	} else {
		cb.successCount++
		if cb.state == StateHalfOpen && cb.successCount >= 3 {
			cb.state = StateClosed
			cb.failureCount = 0
		} else if cb.state == StateClosed {
			cb.failureCount = 0
		}
	}
}

func (cb *CircuitBreaker) State() string {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	switch cb.state {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}
