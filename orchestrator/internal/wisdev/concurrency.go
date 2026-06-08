package wisdev

import "sync"

type AdaptiveConcurrencyController struct {
	mu          sync.RWMutex
	current     int
	min         int
	max         int
	perProvider map[string]int
}

func NewAdaptiveConcurrencyController(initial, min, max int) *AdaptiveConcurrencyController {
	if min <= 0 {
		min = 1
	}
	if max < min {
		max = min
	}
	if initial < min {
		initial = min
	}
	if initial > max {
		initial = max
	}
	return &AdaptiveConcurrencyController{
		current: initial,
		min:     min,
		max:     max,
		perProvider: map[string]int{
			"semantic-scholar": 3,
			"openalex":         10,
			"pubmed":           2,
		},
	}
}

func (c *AdaptiveConcurrencyController) Current() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.current
}

func (c *AdaptiveConcurrencyController) RecordSuccess() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current < c.max {
		c.current++
	}
}

func (c *AdaptiveConcurrencyController) RecordFailure() {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Multiplicative decrease.
	next := c.current / 2
	if next < c.min {
		next = c.min
	}
	c.current = next
}

func (c *AdaptiveConcurrencyController) ProviderLimit(provider string) int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if limit, ok := c.perProvider[provider]; ok {
		return limit
	}
	return c.current
}
