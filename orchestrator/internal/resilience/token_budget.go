package resilience

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// TokenBudget tracks token consumption per request and enforces limits.
// Used to prevent runaway agent loops and manage LLM costs.
type TokenBudget struct {
	mu              sync.RWMutex
	requestID       string
	userID          string
	allocatedTokens int            // Initial budget
	usedTokens      int            // Running total
	costPerOp       map[string]int // Token cost per operation
	startTime       time.Time
	deadline        time.Time // Hard limit on duration
	exceeded        int32     // Atomic flag
}

// TokenCost defines the token cost for each operation type.
type TokenCost struct {
	SearchAPI           int // Cost per search API call
	LLMCall             int // Cost per LLM generation
	EmbeddingGeneration int // Cost per embedding
	RAGRetrieval        int // Cost per RAG query
	ClaimExtraction     int // Cost per claim extraction
	VerificationCheck   int // Cost per verification call
}

// DefaultTokenCost provides standard costs.
func DefaultTokenCost() TokenCost {
	return TokenCost{
		SearchAPI:           100,
		LLMCall:             500,
		EmbeddingGeneration: 50,
		RAGRetrieval:        150,
		ClaimExtraction:     300,
		VerificationCheck:   250,
	}
}

// NewTokenBudget creates a new token budget for a request.
// Budget is based on user tier + query complexity.
func NewTokenBudget(requestID, userID string, allocatedTokens int) *TokenBudget {
	return &TokenBudget{
		requestID:       requestID,
		userID:          userID,
		allocatedTokens: allocatedTokens,
		usedTokens:      0,
		costPerOp: map[string]int{
			"search_api":           100,
			"llm_call":             500,
			"embedding_generation": 50,
			"rag_retrieval":        150,
			"claim_extraction":     300,
			"verification_check":   250,
		},
		startTime: time.Now(),
		deadline:  time.Now().Add(5 * time.Minute), // Default: 5-minute deadline
	}
}

// Allocate reserves tokens for an operation.
// Returns error if budget is exceeded.
func (tb *TokenBudget) Allocate(operation string, count int) error {
	if atomic.LoadInt32(&tb.exceeded) == 1 {
		return fmt.Errorf("budget exceeded for request %s", tb.requestID)
	}

	tb.mu.Lock()
	defer tb.mu.Unlock()

	if tb.usedTokens+count > tb.allocatedTokens {
		atomic.StoreInt32(&tb.exceeded, 1)
		return fmt.Errorf("insufficient budget: allocated=%d, used=%d, requested=%d",
			tb.allocatedTokens, tb.usedTokens, count)
	}

	tb.usedTokens += count
	return nil
}

// AllocateOperation reserves tokens for a named operation type.
func (tb *TokenBudget) AllocateOperation(operation string) error {
	cost, ok := tb.costPerOp[operation]
	if !ok {
		return fmt.Errorf("unknown operation type: %s", operation)
	}
	return tb.Allocate(operation, cost)
}

// RecordUsage manually records token usage (for custom operations).
func (tb *TokenBudget) RecordUsage(operation string, tokensUsed int) error {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	if tb.usedTokens+tokensUsed > tb.allocatedTokens {
		atomic.StoreInt32(&tb.exceeded, 1)
		return fmt.Errorf("budget exceeded: %d + %d > %d",
			tb.usedTokens, tokensUsed, tb.allocatedTokens)
	}

	tb.usedTokens += tokensUsed
	return nil
}

// Remaining returns tokens still available.
func (tb *TokenBudget) Remaining() int {
	tb.mu.RLock()
	defer tb.mu.RUnlock()
	return tb.allocatedTokens - tb.usedTokens
}

// Usage returns current token usage.
func (tb *TokenBudget) Usage() int {
	tb.mu.RLock()
	defer tb.mu.RUnlock()
	return tb.usedTokens
}

// Exceeded returns true if budget is exhausted.
func (tb *TokenBudget) Exceeded() bool {
	return atomic.LoadInt32(&tb.exceeded) == 1
}

// Utilization returns usage as percentage (0.0-1.0).
func (tb *TokenBudget) Utilization() float64 {
	tb.mu.RLock()
	defer tb.mu.RUnlock()
	if tb.allocatedTokens == 0 {
		return 0.0
	}
	return float64(tb.usedTokens) / float64(tb.allocatedTokens)
}

// Deadline returns the hard deadline for this request.
func (tb *TokenBudget) Deadline() time.Time {
	return tb.deadline
}

// ElapsedTime returns time since budget creation.
func (tb *TokenBudget) ElapsedTime() time.Duration {
	return time.Since(tb.startTime)
}

// Report generates a human-readable budget report.
func (tb *TokenBudget) Report() string {
	return fmt.Sprintf(
		"TokenBudget[request=%s, user=%s, used=%d/%d (%.1f%%), remaining=%d, deadline=%v]",
		tb.requestID, tb.userID,
		tb.Usage(), tb.allocatedTokens, tb.Utilization()*100, tb.Remaining(),
		tb.deadline.Sub(time.Now()).String(),
	)
}

// BudgetEnforcer wraps an operation to enforce token budget constraints.
type BudgetEnforcer struct {
	budget *TokenBudget
}

// NewBudgetEnforcer creates a new enforcer.
func NewBudgetEnforcer(budget *TokenBudget) *BudgetEnforcer {
	return &BudgetEnforcer{budget: budget}
}

// ExecuteWithBudget runs a function if budget allows, allocating tokens beforehand.
// Returns error if budget is exceeded or deadline passed.
func (be *BudgetEnforcer) ExecuteWithBudget(ctx context.Context, operation string, fn func(context.Context) error) error {
	// Check deadline before executing
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		if time.Now().After(be.budget.deadline) {
			return fmt.Errorf("deadline exceeded for request %s", be.budget.requestID)
		}
	}

	// Allocate tokens
	if err := be.budget.AllocateOperation(operation); err != nil {
		return err
	}

	// Execute with timeout
	return fn(ctx)
}

// BudgetAwareAgentLoop manages iteration budgets for agent loops.
// Prevents infinite loops by limiting iterations.
type BudgetAwareAgentLoop struct {
	budget            *TokenBudget
	maxIterations     int
	iterationCount    int
	tokenPerIteration int
}

// NewBudgetAwareAgentLoop creates a new loop controller.
func NewBudgetAwareAgentLoop(budget *TokenBudget, maxIterations, tokenPerIter int) *BudgetAwareAgentLoop {
	return &BudgetAwareAgentLoop{
		budget:            budget,
		maxIterations:     maxIterations,
		iterationCount:    0,
		tokenPerIteration: tokenPerIter,
	}
}

// CanIterate returns true if loop can continue.
// Checks both iteration count and token budget.
func (bal *BudgetAwareAgentLoop) CanIterate() bool {
	if bal.iterationCount >= bal.maxIterations {
		return false
	}

	if bal.budget.Remaining() < bal.tokenPerIteration {
		return false
	}

	return true
}

// RecordIteration increments iteration counter and deducts tokens.
func (bal *BudgetAwareAgentLoop) RecordIteration() error {
	bal.iterationCount++
	return bal.budget.RecordUsage("agent_iteration", bal.tokenPerIteration)
}

// IterationCount returns current iteration number.
func (bal *BudgetAwareAgentLoop) IterationCount() int {
	return bal.iterationCount
}

// ReachedIterationLimit returns true if max iterations hit.
func (bal *BudgetAwareAgentLoop) ReachedIterationLimit() bool {
	return bal.iterationCount >= bal.maxIterations
}

// ReachedBudgetLimit returns true if budget exhausted.
func (bal *BudgetAwareAgentLoop) ReachedBudgetLimit() bool {
	return bal.budget.Exceeded()
}

// LoopStatus provides detailed status information.
func (bal *BudgetAwareAgentLoop) LoopStatus() string {
	return fmt.Sprintf(
		"AgentLoop: iteration=%d/%d, budget=%d/%d, can_continue=%v",
		bal.iterationCount, bal.maxIterations,
		bal.budget.Usage(), bal.budget.allocatedTokens,
		bal.CanIterate(),
	)
}

// UsageRatio returns the ratio of used tokens to allocated tokens.
func (tb *TokenBudget) UsageRatio() float64 {
	tb.mu.RLock()
	defer tb.mu.RUnlock()
	if tb.allocatedTokens <= 0 {
		return 0
	}
	return float64(tb.usedTokens) / float64(tb.allocatedTokens)
}
