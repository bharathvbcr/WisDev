package resilience

import (
	"context"
	"errors"
	"strings"
)

// ShouldTripCircuitBreaker reports whether an error should count toward opening
// the breaker. Transient client-side, capacity, and cancellation errors are ignored.
func ShouldTripCircuitBreaker(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	lower := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case lower == "":
		return false
	case strings.Contains(lower, "circuit breaker"):
		return false
	case strings.Contains(lower, "concurrency limit"),
		strings.Contains(lower, "rate gate"),
		strings.Contains(lower, "rate limit"),
		strings.Contains(lower, "too many requests"),
		strings.Contains(lower, "429"):
		return false
	case strings.Contains(lower, "context canceled"),
		strings.Contains(lower, "context deadline exceeded"),
		strings.Contains(lower, "request canceled"):
		return false
	default:
		return true
	}
}

// IsRetriableProviderError reports whether a provider call should be retried
// before counting a circuit-breaker failure.
func IsRetriableProviderError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if !ShouldTripCircuitBreaker(err) {
		return false
	}

	lower := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(lower, "timeout") ||
		strings.Contains(lower, "temporarily unavailable") ||
		strings.Contains(lower, "connection reset") ||
		strings.Contains(lower, "connection refused") ||
		strings.Contains(lower, "eof") ||
		strings.Contains(lower, "503") ||
		strings.Contains(lower, "502") ||
		strings.Contains(lower, "504") ||
		strings.Contains(lower, "429")
}
