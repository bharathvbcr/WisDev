package resilience

// retry.go is the shared bounded-retry helper for outbound calls (Go -> Python
// sidecar HTTP, and any future external integration). It owns the retry policy in
// ONE place per the contract rules: explicit attempt bound, exponential backoff
// with jitter, cancellation checked between attempts, and structured logs for
// every retry decision so recoverable failures stay observable.

import (
	"context"
	"log/slog"
	"math/rand"
	"time"
)

// RetryPolicy bounds a retried operation: at most MaxAttempts tries, sleeping an
// exponentially growing, jittered delay between them (BaseDelay doubled per
// attempt, capped at MaxDelay, jittered to 50-100% of the capped value).
type RetryPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

// normalized returns the policy with unset/invalid fields replaced by safe
// defaults so a zero value still behaves as a bounded single attempt.
func (p RetryPolicy) normalized() RetryPolicy {
	if p.MaxAttempts < 1 {
		p.MaxAttempts = 1
	}
	if p.BaseDelay <= 0 {
		p.BaseDelay = 250 * time.Millisecond
	}
	if p.MaxDelay < p.BaseDelay {
		p.MaxDelay = p.BaseDelay
	}
	return p
}

// backoffDelay is the sleep before the NEXT attempt after failed attempt n
// (1-based): BaseDelay * 2^(n-1), capped at MaxDelay, jittered to [d/2, d] so
// concurrent callers do not retry in lockstep.
func (p RetryPolicy) backoffDelay(attempt int) time.Duration {
	delay := p.BaseDelay
	for i := 1; i < attempt && delay < p.MaxDelay; i++ {
		delay *= 2
	}
	if delay > p.MaxDelay {
		delay = p.MaxDelay
	}
	half := delay / 2
	if half <= 0 {
		return delay
	}
	return half + time.Duration(rand.Int63n(int64(half)+1))
}

// Retry runs attempt up to policy.MaxAttempts times. attempt reports a stable
// errorCode for logging and whether its failure is retryable (transient:
// network error, timeout, upstream 5xx). Non-retryable failures (4xx, malformed
// response) return immediately. Context cancellation is honored between
// attempts and during backoff, returning ctx.Err(). The final error is the last
// attempt's error, so callers keep their existing deterministic fallbacks.
func Retry(ctx context.Context, operation string, policy RetryPolicy, attempt func(ctx context.Context) (errorCode string, retryable bool, err error)) error {
	policy = policy.normalized()
	var lastErr error
	for try := 1; try <= policy.MaxAttempts; try++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		started := time.Now()
		errorCode, retryable, err := attempt(ctx)
		latencyMS := time.Since(started).Milliseconds()
		if err == nil {
			if try > 1 {
				slog.Info("retried operation recovered",
					"component", "resilience.retry", "operation", operation,
					"attempt", try, "max_attempts", policy.MaxAttempts,
					"latency_ms", latencyMS, "result", "ok")
			}
			return nil
		}
		lastErr = err
		if !retryable || try == policy.MaxAttempts || ctx.Err() != nil {
			slog.Warn("retried operation failed",
				"component", "resilience.retry", "operation", operation,
				"attempt", try, "max_attempts", policy.MaxAttempts,
				"latency_ms", latencyMS, "result", "gave_up",
				"retryable", retryable, "error_code", errorCode, "error", err.Error())
			// A context cancelled/expired mid-attempt is honored as cancellation
			// (returning ctx.Err()), matching the between-attempts and during-backoff
			// paths; otherwise the last attempt's error is the deterministic result.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return err
		}
		delay := policy.backoffDelay(try)
		slog.Warn("retried operation failed — backing off",
			"component", "resilience.retry", "operation", operation,
			"attempt", try, "max_attempts", policy.MaxAttempts,
			"latency_ms", latencyMS, "result", "retry",
			"error_code", errorCode, "error", err.Error(),
			"backoff_ms", delay.Milliseconds())
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return lastErr
}
