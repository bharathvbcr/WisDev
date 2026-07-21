package resilience

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestShouldTripCircuitBreakerIgnoresTransientErrors(t *testing.T) {
	assert.False(t, ShouldTripCircuitBreaker(nil))
	assert.False(t, ShouldTripCircuitBreaker(context.Canceled))
	assert.False(t, ShouldTripCircuitBreaker(errors.New("circuit breaker open for openalex")))
	assert.False(t, ShouldTripCircuitBreaker(errors.New("concurrency limit reached")))
	assert.False(t, ShouldTripCircuitBreaker(errors.New("provider rate gate canceled")))
	assert.True(t, ShouldTripCircuitBreaker(errors.New("openalex: upstream 503")))
}

func TestCircuitBreakerAdmitRecoversAfterTimeout(t *testing.T) {
	cb := NewSearchCircuitBreaker("openalex")
	cb.maxFailures = 1
	cb.resetTimeout = 15 * time.Millisecond

	_ = cb.Call(context.Background(), func(context.Context) error {
		return errors.New("upstream 503")
	})
	assert.Equal(t, StateOpen, cb.State())
	assert.False(t, cb.Admit())

	time.Sleep(25 * time.Millisecond)
	assert.True(t, cb.Admit())
	assert.Equal(t, StateHalfOpen, cb.State())

	err := cb.Call(context.Background(), func(context.Context) error { return nil })
	assert.NoError(t, err)
	assert.Equal(t, StateClosed, cb.State())
}

func TestCircuitBreakerIgnoresCanceledFailures(t *testing.T) {
	cb := NewCircuitBreaker("test")
	cb.maxFailures = 1

	_ = cb.Call(context.Background(), func(context.Context) error {
		return context.Canceled
	})
	assert.Equal(t, StateClosed, cb.State())
}

func TestCircuitBreakerHalfOpenFailureReopensImmediately(t *testing.T) {
	cb := NewCircuitBreaker("test")
	cb.maxFailures = 1
	cb.resetTimeout = 10 * time.Millisecond

	_ = cb.Call(context.Background(), func(context.Context) error {
		return errors.New("upstream 503")
	})
	time.Sleep(20 * time.Millisecond)

	_ = cb.Call(context.Background(), func(context.Context) error {
		return errors.New("upstream 503")
	})
	assert.Equal(t, StateOpen, cb.State())
}
