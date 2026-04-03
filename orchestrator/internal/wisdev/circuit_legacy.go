package wisdev

import (
	"log/slog"
	"sync"
	"time"
)

// ==========================================
// Per-Source Circuit Breaker
// ==========================================

// CircuitState represents the three possible states of a circuit breaker.
type CircuitState int

const (
	CircuitClosed   CircuitState = iota // normal operation — requests flow through
	CircuitOpen                         // failures exceeded threshold — requests blocked
	CircuitHalfOpen                     // testing recovery — limited requests allowed
)

// String returns a human-readable name for the circuit state.
func (s CircuitState) String() string {
	switch s {
	case CircuitClosed:
		return "closed"
	case CircuitOpen:
		return "open"
	case CircuitHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// CircuitBreakerStats holds a snapshot of a circuit breaker's current status.
type CircuitBreakerStats struct {
	State        string `json:"state"`
	Failures     int    `json:"failures"`
	Successes    int    `json:"successes"`
	TotalCalls   int64  `json:"totalCalls"`
	LastFailure  string `json:"lastFailure,omitempty"`
	HalfOpenUsed int    `json:"halfOpenUsed,omitempty"`
}

// CircuitBreaker implements the circuit breaker pattern for a single
// upstream source. It is safe for concurrent use.
type CircuitBreaker struct {
	mu sync.RWMutex

	name string

	// Configuration
	failureThreshold int           // consecutive failures to trip open
	resetTimeout     time.Duration // how long to stay open before half-open
	halfOpenMax      int           // max test requests in half-open state

	// Runtime state
	state        CircuitState
	failures     int
	successes    int
	totalCalls   int64
	lastFailure  time.Time
	openedAt     time.Time
	halfOpenUsed int
}

// CircuitBreakerOption is a functional option for configuring a CircuitBreaker.
type CircuitBreakerOption func(*CircuitBreaker)

// WithFailureThreshold sets the number of consecutive failures required to
// trip the breaker open. Default is 5.
func WithFailureThreshold(n int) CircuitBreakerOption {
	return func(cb *CircuitBreaker) {
		if n > 0 {
			cb.failureThreshold = n
		}
	}
}

// WithResetTimeout sets how long the breaker stays open before transitioning
// to half-open. Default is 30 seconds.
func WithResetTimeout(d time.Duration) CircuitBreakerOption {
	return func(cb *CircuitBreaker) {
		if d > 0 {
			cb.resetTimeout = d
		}
	}
}

// WithHalfOpenMax sets the number of test requests allowed in the half-open
// state. Default is 1.
func WithHalfOpenMax(n int) CircuitBreakerOption {
	return func(cb *CircuitBreaker) {
		if n > 0 {
			cb.halfOpenMax = n
		}
	}
}

// NewCircuitBreaker creates a circuit breaker for the named Source with the
// given options. Without options the defaults are: failureThreshold=5,
// resetTimeout=30s, halfOpenMax=1.
func NewCircuitBreaker(name string, opts ...CircuitBreakerOption) *CircuitBreaker {
	cb := &CircuitBreaker{
		name:             name,
		failureThreshold: 5,
		resetTimeout:     30 * time.Second,
		halfOpenMax:      1,
		state:            CircuitClosed,
	}
	for _, opt := range opts {
		opt(cb)
	}
	return cb
}

// Allow checks whether a request should be permitted through the breaker.
//
// - Closed: always allows.
// - Open: blocks unless the reset timeout has elapsed, in which case it
//
//	transitions to half-open.
//
// - HalfOpen: allows up to halfOpenMax test requests.
func (cb *CircuitBreaker) Allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.totalCalls++

	switch cb.state {
	case CircuitClosed:
		return true

	case CircuitOpen:
		// Check if enough time has passed to try recovery
		if time.Since(cb.openedAt) >= cb.resetTimeout {
			cb.state = CircuitHalfOpen
			cb.halfOpenUsed = 0
			slog.Info("Circuit breaker transitioning to half-open", "name", cb.name)
			cb.halfOpenUsed++
			return true
		}
		slog.Debug("Circuit breaker OPEN — request blocked", "name", cb.name)
		return false

	case CircuitHalfOpen:
		if cb.halfOpenUsed < cb.halfOpenMax {
			cb.halfOpenUsed++
			return true
		}
		slog.Debug("Circuit breaker HALF-OPEN — max test requests reached", "name", cb.name)
		return false

	default:
		return true
	}
}

// RecordSuccess records a successful call. In half-open state, a success
// closes the breaker and resets failure counts.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.successes++

	switch cb.state {
	case CircuitHalfOpen:
		// Recovery confirmed — close the circuit
		cb.state = CircuitClosed
		cb.failures = 0
		cb.halfOpenUsed = 0
		slog.Info("Circuit breaker recovered — now CLOSED", "name", cb.name)

	case CircuitClosed:
		// Reset consecutive failure count on any success
		cb.failures = 0
	}
}

// RecordFailure records a failed call. In the closed state, if the failure
// count reaches the threshold the breaker trips open. In the half-open
// state, any failure immediately re-opens the breaker.
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failures++
	cb.lastFailure = time.Now()

	switch cb.state {
	case CircuitClosed:
		if cb.failures >= cb.failureThreshold {
			cb.state = CircuitOpen
			cb.openedAt = time.Now()
			slog.Warn("Circuit breaker OPENED",
				"name", cb.name,
				"failures", cb.failures,
				"threshold", cb.failureThreshold,
			)
		}

	case CircuitHalfOpen:
		// Any failure in half-open re-opens immediately
		cb.state = CircuitOpen
		cb.openedAt = time.Now()
		cb.halfOpenUsed = 0
		slog.Warn("Circuit breaker re-opened from half-open", "name", cb.name)
	}
}

// Stats returns a snapshot of the breaker's current state.
func (cb *CircuitBreaker) Stats() CircuitBreakerStats {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	stats := CircuitBreakerStats{
		State:      cb.state.String(),
		Failures:   cb.failures,
		Successes:  cb.successes,
		TotalCalls: cb.totalCalls,
	}
	if !cb.lastFailure.IsZero() {
		stats.LastFailure = cb.lastFailure.Format(time.RFC3339)
	}
	if cb.state == CircuitHalfOpen {
		stats.HalfOpenUsed = cb.halfOpenUsed
	}
	return stats
}

// Name returns the breaker's Source name.
func (cb *CircuitBreaker) Name() string {
	return cb.name
}

// ==========================================
// Pre-built breakers for academic API sources
// ==========================================

var (
	s2Breaker       = NewCircuitBreaker("semantic-scholar")
	openAlexBreaker = NewCircuitBreaker("openalex")
	pubmedBreaker   = NewCircuitBreaker("pubmed")
	coreBreaker     = NewCircuitBreaker("core")
	arxivBreaker    = NewCircuitBreaker("arxiv")
)

// AllBreakers returns every pre-built circuit breaker, useful for health
// endpoints or status pages.
func AllBreakers() []*CircuitBreaker {
	return []*CircuitBreaker{
		s2Breaker,
		openAlexBreaker,
		pubmedBreaker,
		coreBreaker,
		arxivBreaker,
	}
}
