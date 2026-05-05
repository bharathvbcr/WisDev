package resilience

import (
	"context"
	"fmt"
	"log/slog"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/telemetry"
	"sync"
	"time"
)

// CircuitBreakerState represents the state of a circuit breaker.
type CircuitBreakerState string

const (
	StateClosed   CircuitBreakerState = "closed"    // Normal operation
	StateOpen     CircuitBreakerState = "open"      // Failing, reject requests
	StateHalfOpen CircuitBreakerState = "half_open" // Testing if service recovered
)

// CircuitBreaker implements the circuit breaker pattern to prevent cascading failures.
// States:
//   - Closed: Normal operation, pass requests through
//   - Open: Service failing, reject requests immediately
//   - HalfOpen: Testing if service recovered, allow limited requests
type CircuitBreaker struct {
	name             string
	maxFailures      int           // Failures before opening (default: 3)
	resetTimeout     time.Duration // Time before half-open attempt (default: 30s)
	successThreshold int           // Successful calls needed to close (default: 2)

	mu                sync.RWMutex
	state             CircuitBreakerState
	failureCount      int
	successCount      int
	lastFailureTime   time.Time
	lastFailureError  error
	totalRequestCount int64
	totalErrorCount   int64
}

// NewCircuitBreaker creates a new circuit breaker with default settings.
func NewCircuitBreaker(name string) *CircuitBreaker {
	return &CircuitBreaker{
		name:             name,
		maxFailures:      3,
		resetTimeout:     30 * time.Second,
		successThreshold: 2,
		state:            StateClosed,
	}
}

// State returns the current state of the circuit breaker.
func (cb *CircuitBreaker) State() CircuitBreakerState {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state
}

// Call executes a function with circuit breaker protection.
// If the circuit is open, fails fast without attempting the call.
func (cb *CircuitBreaker) Call(ctx context.Context, fn func(context.Context) error) error {
	cb.mu.Lock()

	// Check if we need to transition to half-open
	if cb.state == StateOpen && time.Since(cb.lastFailureTime) > cb.resetTimeout {
		cb.state = StateHalfOpen
		cb.successCount = 0
		cb.failureCount = 0
		slog.Info("circuit_breaker_half_open",
			"service", "go_orchestrator",
			"runtime", "go",
			"component", "resilience",
			"operation", "circuit_breaker_transition",
			"stage", "half_open",
			"breaker", cb.name,
			"state", cb.state,
			"result", "probe_allowed",
		)
	}

	// Reject if open
	if cb.state == StateOpen {
		err := fmt.Errorf("circuit breaker %s is open", cb.name)
		telemetry.RecordCircuitBreakerTrip(cb.name, string(cb.state), err)
		slog.Error("circuit_breaker_open_reject",
			"service", "go_orchestrator",
			"runtime", "go",
			"component", "resilience",
			"operation", "circuit_breaker_admit",
			"stage", "fast_reject",
			"breaker", cb.name,
			"state", cb.state,
			"result", "rejected",
			"error_code", "system_overload",
			"error", err,
		)
		cb.mu.Unlock()
		return err
	}

	cb.totalRequestCount++
	cb.mu.Unlock()

	// Execute the call
	err := fn(ctx)

	cb.mu.Lock()
	defer cb.mu.Unlock()

	if err != nil {
		cb.totalErrorCount++
		cb.recordFailure(err)
	} else {
		cb.recordSuccess()
	}

	return err
}

// recordFailure increments failure counter and may transition to open state.
func (cb *CircuitBreaker) recordFailure(err error) {
	cb.failureCount++
	cb.lastFailureTime = time.Now()
	cb.lastFailureError = err
	cb.successCount = 0 // Reset success counter

	// Transition to open if threshold reached
	if cb.failureCount >= cb.maxFailures && cb.state != StateOpen {
		cb.state = StateOpen
		telemetry.RecordCircuitBreakerTrip(cb.name, string(cb.state), err)
		slog.Error("circuit_breaker_tripped",
			"service", "go_orchestrator",
			"runtime", "go",
			"component", "resilience",
			"operation", "circuit_breaker_transition",
			"stage", "open",
			"breaker", cb.name,
			"state", cb.state,
			"result", "tripped",
			"error_code", "upstream_failure",
			"failure_count", cb.failureCount,
			"error", err,
		)
	}
}

// recordSuccess increments success counter and may transition to closed state.
func (cb *CircuitBreaker) recordSuccess() {
	cb.failureCount = 0 // Reset failure counter
	cb.successCount++

	// Transition to closed if threshold reached (only in half-open)
	if cb.state == StateHalfOpen && cb.successCount >= cb.successThreshold {
		cb.state = StateClosed
	}
}

// Reset manually resets the circuit breaker to closed state.
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.state = StateClosed
	cb.failureCount = 0
	cb.successCount = 0
	cb.lastFailureError = nil
}

// Metrics returns current circuit breaker metrics.
func (cb *CircuitBreaker) Metrics() *CircuitBreakerMetrics {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	errorRate := 0.0
	if cb.totalRequestCount > 0 {
		errorRate = float64(cb.totalErrorCount) / float64(cb.totalRequestCount)
	}

	return &CircuitBreakerMetrics{
		Name:             cb.name,
		State:            cb.state,
		TotalRequests:    cb.totalRequestCount,
		TotalErrors:      cb.totalErrorCount,
		ErrorRate:        errorRate,
		CurrentFailures:  cb.failureCount,
		CurrentSuccesses: cb.successCount,
		LastFailureError: cb.lastFailureError,
		LastFailureTime:  cb.lastFailureTime,
	}
}

// CircuitBreakerMetrics represents snapshot of circuit breaker state.
type CircuitBreakerMetrics struct {
	Name             string
	State            CircuitBreakerState
	TotalRequests    int64
	TotalErrors      int64
	ErrorRate        float64
	CurrentFailures  int
	CurrentSuccesses int
	LastFailureError error
	LastFailureTime  time.Time
}

// CircuitBreakerRegistry manages multiple circuit breakers.
type CircuitBreakerRegistry struct {
	mu               sync.RWMutex
	breakers         map[string]*CircuitBreaker
	defaultResetTime time.Duration
}

// NewCircuitBreakerRegistry creates a new registry.
func NewCircuitBreakerRegistry() *CircuitBreakerRegistry {
	return &CircuitBreakerRegistry{
		breakers:         make(map[string]*CircuitBreaker),
		defaultResetTime: 30 * time.Second,
	}
}

// GetOrCreate retrieves or creates a circuit breaker for a service.
func (cbr *CircuitBreakerRegistry) GetOrCreate(name string) *CircuitBreaker {
	cbr.mu.Lock()
	defer cbr.mu.Unlock()

	if cb, exists := cbr.breakers[name]; exists {
		return cb
	}

	cb := NewCircuitBreaker(name)
	cbr.breakers[name] = cb
	return cb
}

// Get retrieves a circuit breaker by name (returns nil if not found).
func (cbr *CircuitBreakerRegistry) Get(name string) *CircuitBreaker {
	cbr.mu.RLock()
	defer cbr.mu.RUnlock()
	return cbr.breakers[name]
}

// All returns all registered circuit breakers.
func (cbr *CircuitBreakerRegistry) All() []*CircuitBreaker {
	cbr.mu.RLock()
	defer cbr.mu.RUnlock()

	breakers := make([]*CircuitBreaker, 0, len(cbr.breakers))
	for _, cb := range cbr.breakers {
		breakers = append(breakers, cb)
	}
	return breakers
}

// ResetAll resets all circuit breakers to closed state.
func (cbr *CircuitBreakerRegistry) ResetAll() {
	cbr.mu.RLock()
	defer cbr.mu.RUnlock()

	for _, cb := range cbr.breakers {
		cb.Reset()
	}
}

// HealthReport generates a health report for all circuit breakers.
func (cbr *CircuitBreakerRegistry) HealthReport() *HealthReport {
	breakers := cbr.All()
	report := &HealthReport{
		Timestamp: time.Now(),
		Breakers:  make([]*CircuitBreakerMetrics, 0, len(breakers)),
	}

	healthy := 0
	for _, cb := range breakers {
		metrics := cb.Metrics()
		report.Breakers = append(report.Breakers, metrics)

		if metrics.State == StateClosed && metrics.ErrorRate < 0.1 {
			healthy++
		}
	}

	report.TotalBreakers = len(breakers)
	report.HealthyBreakers = healthy
	report.HealthStatus = "healthy"
	if healthy < len(breakers)/2 {
		report.HealthStatus = "degraded"
	}

	return report
}

// HealthReport represents system health across all circuit breakers.
type HealthReport struct {
	Timestamp       time.Time
	TotalBreakers   int
	HealthyBreakers int
	HealthStatus    string // "healthy", "degraded", "critical"
	Breakers        []*CircuitBreakerMetrics
}

// FallbackStrategy provides an alternative when the primary call fails.
type FallbackStrategy interface {
	Fallback(ctx context.Context) error
	Name() string
}

// CircuitBreakerWithFallback wraps a circuit breaker with fallback logic.
type CircuitBreakerWithFallback struct {
	breaker  *CircuitBreaker
	fallback FallbackStrategy
}

// NewCircuitBreakerWithFallback creates a new breaker with fallback.
func NewCircuitBreakerWithFallback(breaker *CircuitBreaker, fallback FallbackStrategy) *CircuitBreakerWithFallback {
	return &CircuitBreakerWithFallback{
		breaker:  breaker,
		fallback: fallback,
	}
}

// Call attempts the primary call, falling back on circuit breaker failure.
func (cbf *CircuitBreakerWithFallback) Call(ctx context.Context, fn func(context.Context) error) error {
	err := cbf.breaker.Call(ctx, fn)

	// If circuit is open, try fallback
	if cbf.breaker.State() == StateOpen && cbf.fallback != nil {
		// Log that fallback is being used
		// logger.Logger.Debugf("Circuit breaker %s is open, using fallback: %s", cbf.breaker.name, cbf.fallback.Name())
		return cbf.fallback.Fallback(ctx)
	}

	return err
}

// Example Fallback Strategies

// CachedResponseFallback returns a cached response when circuit is open.
type CachedResponseFallback struct {
	cachedResult interface{}
}

// Fallback returns the cached result.
func (crf *CachedResponseFallback) Fallback(ctx context.Context) error {
	// In real implementation, would return cached data to caller
	return fmt.Errorf("circuit is open, returning cached response")
}

// Name returns the fallback name.
func (crf *CachedResponseFallback) Name() string {
	return "cached_response"
}

// DegradedModeFallback returns lower-quality results when circuit is open.
type DegradedModeFallback struct {
	degradedFn func(context.Context) error
}

// Fallback runs the degraded mode function.
func (dmf *DegradedModeFallback) Fallback(ctx context.Context) error {
	if dmf.degradedFn == nil {
		return fmt.Errorf("degraded mode not available")
	}
	return dmf.degradedFn(ctx)
}

// Name returns the fallback name.
func (dmf *DegradedModeFallback) Name() string {
	return "degraded_mode"
}
