package resilience

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fastRetryPolicy keeps backoff sleeps negligible so the retry tests stay fast.
func fastRetryPolicy(maxAttempts int) RetryPolicy {
	return RetryPolicy{MaxAttempts: maxAttempts, BaseDelay: time.Millisecond, MaxDelay: 4 * time.Millisecond}
}

func TestRetrySucceedsFirstTry(t *testing.T) {
	attempts := 0
	err := Retry(context.Background(), "test.op", fastRetryPolicy(3), func(context.Context) (string, bool, error) {
		attempts++
		return "", false, nil
	})
	require.NoError(t, err)
	assert.Equal(t, 1, attempts, "a successful first attempt must not be retried")
}

func TestRetryRecoversAfterTransientFailure(t *testing.T) {
	attempts := 0
	err := Retry(context.Background(), "test.op", fastRetryPolicy(3), func(context.Context) (string, bool, error) {
		attempts++
		if attempts == 1 {
			return "http_5xx", true, errors.New("upstream 500")
		}
		return "", false, nil
	})
	require.NoError(t, err)
	assert.Equal(t, 2, attempts, "one transient failure should cost exactly one extra attempt")
}

func TestRetryStopsImmediatelyOnNonRetryableError(t *testing.T) {
	attempts := 0
	wantErr := errors.New("bad request")
	err := Retry(context.Background(), "test.op", fastRetryPolicy(3), func(context.Context) (string, bool, error) {
		attempts++
		return "http_4xx", false, wantErr
	})
	require.ErrorIs(t, err, wantErr)
	assert.Equal(t, 1, attempts, "non-retryable failures must never be retried")
}

func TestRetryExhaustsBudgetAndReturnsLastError(t *testing.T) {
	attempts := 0
	err := Retry(context.Background(), "test.op", fastRetryPolicy(3), func(context.Context) (string, bool, error) {
		attempts++
		return "transport_error", true, errors.New("connection refused")
	})
	require.Error(t, err)
	assert.Equal(t, 3, attempts, "the attempt budget is a hard bound")
	assert.Contains(t, err.Error(), "connection refused")
}

func TestRetryHonorsContextCancellationDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	// Long backoff so the cancellation (issued mid-attempt) is what ends the wait.
	policy := RetryPolicy{MaxAttempts: 3, BaseDelay: 5 * time.Second, MaxDelay: 5 * time.Second}
	err := Retry(ctx, "test.op", policy, func(context.Context) (string, bool, error) {
		attempts++
		cancel()
		return "transport_error", true, errors.New("sidecar down")
	})
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, attempts, "cancellation must stop the loop before the next attempt")
}

func TestRetryZeroPolicyStillRunsOnce(t *testing.T) {
	attempts := 0
	err := Retry(context.Background(), "test.op", RetryPolicy{}, func(context.Context) (string, bool, error) {
		attempts++
		return "transport_error", true, errors.New("boom")
	})
	require.Error(t, err)
	assert.Equal(t, 1, attempts, "a zero policy normalizes to a single bounded attempt")
}
