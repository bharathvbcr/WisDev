package search

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

type mockSearchProvider struct {
	mock.Mock
}

func (m *mockSearchProvider) Name() string {
	return m.Called().String(0)
}

func (m *mockSearchProvider) Search(ctx context.Context, query string, opts SearchOpts) ([]Paper, error) {
	args := m.Called(ctx, query, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]Paper), args.Error(1)
}

func (m *mockSearchProvider) Domains() []string {
	return []string{}
}

func (m *mockSearchProvider) Healthy() bool {
	return m.Called().Bool(0)
}

func (m *mockSearchProvider) Tools() []string {
	return []string{}
}

func TestParallelSearch_FailureModes(t *testing.T) {
	ctx := context.Background()

	t.Run("Partial Provider Failure (Success with Warnings)", func(t *testing.T) {
		reg := NewProviderRegistry()
		
		p1 := new(mockSearchProvider)
		p1.On("Name").Return("p1")
		p1.On("Healthy").Return(true)
		p1.On("Search", mock.Anything, "test", mock.Anything).Return([]Paper{{Title: "Paper 1"}}, nil)
		
		p2 := new(mockSearchProvider)
		p2.On("Name").Return("p2")
		p2.On("Healthy").Return(true)
		p2.On("Search", mock.Anything, "test", mock.Anything).Return(nil, errors.New("provider p2 failed"))
		
		reg.Register(p1)
		reg.Register(p2)
		reg.SetDefaultOrder([]string{"p1", "p2"})

		result := ParallelSearch(ctx, reg, "test", SearchOpts{})
		
		assert.Len(t, result.Papers, 1)
		assert.Equal(t, "Paper 1", result.Papers[0].Title)
		assert.Len(t, result.Warnings, 1)
		assert.Equal(t, "p2", result.Warnings[0].Provider)
		assert.Contains(t, result.Warnings[0].Message, "provider p2 failed")
	})

	t.Run("Context Timeout Propagation", func(t *testing.T) {
		reg := NewProviderRegistry()
		
		p1 := new(mockSearchProvider)
		p1.On("Name").Return("p1")
		p1.On("Healthy").Return(true)
		// Slow provider
		p1.On("Search", mock.Anything, "test", mock.Anything).Run(func(args mock.Arguments) {
			select {
			case <-args.Get(0).(context.Context).Done():
			case <-time.After(100 * time.Millisecond):
			}
		}).Return(nil, context.DeadlineExceeded)
		
		reg.Register(p1)
		reg.SetDefaultOrder([]string{"p1"})

		timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
		defer cancel()

		result := ParallelSearch(timeoutCtx, reg, "test", SearchOpts{})
		
		// If ParallelSearch detects context cancel before dispatching, it returns early.
		// If it's already dispatched, it will collect the error from the channel.
		assert.True(t, len(result.Warnings) > 0)
		foundTimeout := false
		for _, w := range result.Warnings {
			if w.Message == context.DeadlineExceeded.Error() || w.Message == "query context canceled" {
				foundTimeout = true
			}
		}
		assert.True(t, foundTimeout, "should have found a timeout warning")
	})

	t.Run("Empty Query Guard", func(t *testing.T) {
		reg := NewProviderRegistry()
		result := ParallelSearch(ctx, reg, "  ", SearchOpts{})
		assert.Len(t, result.Papers, 0)
		assert.Len(t, result.Warnings, 1)
		assert.Equal(t, "system", result.Warnings[0].Provider)
		assert.Equal(t, "query is required", result.Warnings[0].Message)
	})
}
