package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRaiseSearchBudgetForDocGen(t *testing.T) {
	assert.Equal(t, 12, raiseSearchBudgetForDocGen(12, 0))
	assert.Equal(t, 10, raiseSearchBudgetForDocGen(5, 0))
	assert.Equal(t, 25, raiseSearchBudgetForDocGen(12, 25))
	assert.Equal(t, 80, raiseSearchBudgetForDocGen(12, 120))
	assert.Equal(t, 80, raiseSearchBudgetForDocGen(200, 50))
}

func TestResolveFullPaperHydrationBudget(t *testing.T) {
	defaultBudget := resolveFullPaperHydrationBudget(0)
	assert.Equal(t, 10, defaultBudget.dedupCap)
	assert.Equal(t, 4, defaultBudget.maxQueries)
	assert.GreaterOrEqual(t, defaultBudget.limitPerQuery, 5)

	high := resolveFullPaperHydrationBudget(50)
	assert.Equal(t, 50, high.dedupCap)
	assert.Equal(t, 8, high.maxQueries)
	assert.Greater(t, high.limitPerQuery, defaultBudget.limitPerQuery)

	capped := resolveFullPaperHydrationBudget(200)
	assert.Equal(t, 80, capped.dedupCap)
}
