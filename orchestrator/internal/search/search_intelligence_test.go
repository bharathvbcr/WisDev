package search

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
)

type spyDB struct {
	execArgs  []any
	queryArgs []any
	querySQL  string
	queryRows pgx.Rows
	queryErr  error
	queryPlan []queryResult
	queryIdx  int
}

type queryResult struct {
	rows pgx.Rows
	err  error
}

func (s *spyDB) Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	s.execArgs = arguments
	return pgconn.CommandTag{}, nil
}

func (s *spyDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	s.querySQL = sql
	s.queryArgs = args
	if len(s.queryPlan) > 0 {
		res := s.queryPlan[s.queryIdx]
		s.queryIdx++
		return res.rows, res.err
	}
	if s.queryRows != nil {
		return s.queryRows, s.queryErr
	}
	return nil, errors.New("query failed")
}

func (s *spyDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return nil
}

type fakeRows struct {
	values   [][]any
	errors   []error
	index    int
	closed   bool
}

func (r *fakeRows) Close() {
	r.closed = true
}

func (r *fakeRows) Err() error {
	return nil
}

func (r *fakeRows) CommandTag() pgconn.CommandTag {
	return pgconn.CommandTag{}
}

func (r *fakeRows) FieldDescriptions() []pgconn.FieldDescription {
	return nil
}

func (r *fakeRows) Next() bool {
	r.index++
	return r.index < len(r.values)
}

func (r *fakeRows) Scan(dest ...any) error {
	if r.index < 0 || r.index >= len(r.values) {
		return assert.AnError
	}
	if len(r.errors) > r.index && r.errors[r.index] != nil {
		return r.errors[r.index]
	}
	if len(dest) != len(r.values[r.index]) {
		return assert.AnError
	}

	for i, value := range r.values[r.index] {
		switch typed := dest[i].(type) {
		case *string:
			v, ok := value.(string)
			if !ok {
				return assert.AnError
			}
			*typed = v
		case *int:
			v, ok := value.(int)
			if !ok {
				return assert.AnError
			}
			*typed = v
		case *float64:
			v, ok := value.(float64)
			if !ok {
				return assert.AnError
			}
			*typed = v
		default:
			return assert.AnError
		}
	}
	return nil
}

func (r *fakeRows) Values() ([]any, error) {
	return nil, nil
}

func (r *fakeRows) RawValues() [][]byte {
	return nil
}

func (r *fakeRows) Conn() *pgx.Conn {
	return nil
}

func TestNormalizeQuery(t *testing.T) {
	got := normalizeQuery("  Cancer   Therapy\n  Outcomes  ")
	if got != "cancer therapy outcomes" {
		t.Fatalf("unexpected normalized query: %q", got)
	}
}

func TestRecordClick_NormalizesQuery(t *testing.T) {
	db := &spyDB{}
	si := NewSearchIntelligence(db)

	err := si.RecordClick(context.Background(), "u1", "  Cancer   Therapy  ", "p1", "mock", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(db.execArgs) < 2 {
		t.Fatalf("expected at least 2 exec args, got %d", len(db.execArgs))
	}
	if got, _ := db.execArgs[1].(string); got != "cancer therapy" {
		t.Fatalf("expected normalized query in exec args, got %q", got)
	}
}

func TestRecordClick_InvalidUserIDBecomesNil(t *testing.T) {
	db := &spyDB{}
	si := NewSearchIntelligence(db)

	if err := si.RecordClick(context.Background(), "firebase_uid_123", "query", "p1", "mock", 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if db.execArgs[0] != nil {
		t.Fatalf("expected invalid user ID to be stored as nil, got %#v", db.execArgs[0])
	}
}

func TestRecordClick_ValidUUIDIsPreserved(t *testing.T) {
	db := &spyDB{}
	si := NewSearchIntelligence(db)
	userID := uuid.NewString()

	if err := si.RecordClick(context.Background(), userID, "query", "p1", "mock", 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, _ := db.execArgs[0].(string); got != userID {
		t.Fatalf("expected valid UUID to be preserved, got %q", got)
	}
}

func TestRecordSearch_NormalizesQuery(t *testing.T) {
	db := &spyDB{}
	si := NewSearchIntelligence(db)

	err := si.RecordSearch(context.Background(), "  SYSTEMATIC   REVIEW  ", "mock", 1.0, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(db.execArgs) < 1 {
		t.Fatalf("expected exec args to be captured")
	}
	if got, _ := db.execArgs[0].(string); got != "systematic review" {
		t.Fatalf("expected normalized query in exec args, got %q", got)
	}
}

func TestRecordExpansionPerformance_NormalizesQueries(t *testing.T) {
	db := &spyDB{}
	si := NewSearchIntelligence(db)

	err := si.RecordExpansionPerformance(context.Background(), "  Expanded  Query  ", "  Better  Result  ", "temporal", 10, 0.75)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(db.execArgs) < 2 {
		t.Fatalf("expected exec args to be captured")
	}
	if got, _ := db.execArgs[0].(string); got != "expanded query" {
		t.Fatalf("expected normalized original query, got %q", got)
	}
	if got, _ := db.execArgs[1].(string); got != "better result" {
		t.Fatalf("expected normalized expanded query, got %q", got)
	}
}

func TestGetClickCountsForQuery_NormalizesQuery(t *testing.T) {
	db := &spyDB{}
	si := NewSearchIntelligence(db)

	_, err := si.GetClickCountsForQuery(context.Background(), "  Deep   Learning\tMedical ", 10)
	if err == nil {
		t.Fatal("expected query error from spy db")
	}
	if len(db.queryArgs) < 1 {
		t.Fatalf("expected query args to be captured")
	}
	if got, _ := db.queryArgs[0].(string); got != "deep learning medical" {
		t.Fatalf("expected normalized query in query args, got %q", got)
	}
}

func TestGetExpandedQueryScores_NormalizesOriginalQuery(t *testing.T) {
	db := &spyDB{}
	si := NewSearchIntelligence(db)

	_, err := si.GetExpandedQueryScores(context.Background(), "  Quantum   Gravity  ", 5)
	if err == nil {
		t.Fatal("expected query error from spy db")
	}
	if len(db.queryArgs) < 1 {
		t.Fatalf("expected query args to be captured")
	}
	if got, _ := db.queryArgs[0].(string); got != "quantum gravity" {
		t.Fatalf("expected normalized original query in query args, got %q", got)
	}
}

func TestRecordExpansionPerformance_NoDBReturnsNil(t *testing.T) {
	si := NewSearchIntelligence(nil)
	if err := si.RecordExpansionPerformance(context.Background(), "q", "q2", "temporal", 4, 1.0); err != nil {
		t.Fatalf("expected nil error when db is nil, got %v", err)
	}
}

func TestGetProviderScores_HappyPath(t *testing.T) {
	db := &spyDB{
		queryRows: &fakeRows{
			values: [][]any{
				{"semantic_scholar", 1.2},
				{"pubmed", 0.8},
			},
			index: -1,
		},
	}
	si := NewSearchIntelligence(db)

	scores, err := si.GetProviderScores(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if scores["semantic_scholar"] != 1.2 {
		t.Fatalf("expected score for semantic_scholar")
	}
	if scores["pubmed"] != 0.8 {
		t.Fatalf("expected score for pubmed")
	}
}

func TestGetTopProviders_FallsBackWhenDomainHasNoAverages(t *testing.T) {
	db := &spyDB{
		queryPlan: []queryResult{
			{
				rows: &fakeRows{
					values: [][]any{
						{"semantic_scholar", 4},
						{"pubmed", 1},
					},
					index: -1,
				},
				err: nil,
			},
			{
				rows: &fakeRows{
					values: [][]any{},
					index: -1,
				},
				err: nil,
			},
			{
				rows: &fakeRows{
					values: [][]any{
						{"semantic_scholar", 0.3},
						{"pubmed", 0.9},
					},
					index: -1,
				},
				err: nil,
			},
		},
	}
	si := NewSearchIntelligence(db)

	providers, err := si.GetTopProviders(context.Background(), "biomedical", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(providers) != 1 {
		t.Fatalf("expected one top provider, got %d", len(providers))
	}
	if providers[0] != "semantic_scholar" {
		t.Fatalf("expected semantic_scholar to win, got %q", providers[0])
	}
}

func TestGetProviderAverageGains_DomainPath(t *testing.T) {
	db := &spyDB{
		queryRows: &fakeRows{
			values: [][]any{
				{"semantic_scholar", "cancer treatment", 2.0},
				{"pubmed", "random query", 1.0},
				{"semantic_scholar", "distributed neural networks", 4.0},
			},
			index: -1,
		},
	}
	si := NewSearchIntelligence(db)

	scores, err := si.getProviderAverageGains(context.Background(), "biomedical")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if scores["semantic_scholar"] != 2.0 {
		t.Fatalf("expected semantic_scholar biomedical score of 2.0, got %v", scores["semantic_scholar"])
	}
	if _, ok := scores["pubmed"]; ok {
		t.Fatalf("did not expect pubmed to be scored for biomedical domain in this test")
	}
}

func TestGetProviderAverageGains_EmptyDomainAggregatesAllRows(t *testing.T) {
	db := &spyDB{
		queryRows: &fakeRows{
			values: [][]any{
				{"semantic_scholar", 3.0},
				{"pubmed", 1.5},
			},
			index: -1,
		},
	}
	si := NewSearchIntelligence(db)

	scores, err := si.getProviderAverageGains(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(scores) != 2 {
		t.Fatalf("expected all provider averages to be returned, got %+v", scores)
	}
	if scores["semantic_scholar"] != 3.0 || scores["pubmed"] != 1.5 {
		t.Fatalf("unexpected aggregated averages: %+v", scores)
	}
}

func TestGetExpandedQueryScores_FiltersBlankQueries(t *testing.T) {
	db := &spyDB{
		queryRows: &fakeRows{
			values: [][]any{
				{"", "temporal", 2.0},
				{"graph theory", "query_refine", 1.6},
			},
			index: -1,
		},
	}
	si := NewSearchIntelligence(db)

	scores, err := si.GetExpandedQueryScores(context.Background(), "graph theory", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(scores) != 1 {
		t.Fatalf("expected only one non-empty expansion score, got %d", len(scores))
	}
	if scores[0].Query != "graph theory" {
		t.Fatalf("unexpected query %q", scores[0].Query)
	}
}

func TestGetStrategyScores_ReturnsProviderScores(t *testing.T) {
	db := &spyDB{
		queryRows: &fakeRows{
			values: [][]any{
				{"temporal", 1.8},
				{"query_refine", 0.9},
			},
			index: -1,
		},
	}
	si := NewSearchIntelligence(db)

	scores, err := si.GetStrategyScores(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(scores) != 2 {
		t.Fatalf("expected 2 strategy scores, got %d", len(scores))
	}
	if scores["temporal"] != 1.8 {
		t.Fatalf("expected temporal score 1.8, got %v", scores["temporal"])
	}
}

func TestGetProviderAverageGains_DomainFilterDoesNotReferenceMissingDomainColumn(t *testing.T) {
	db := &spyDB{}
	si := NewSearchIntelligence(db)

	_, err := si.getProviderAverageGains(context.Background(), "biomedical")
	if err == nil {
		t.Fatal("expected query error from spy db")
	}
	if strings.Contains(strings.ToLower(db.querySQL), "where domain") {
		t.Fatalf("expected domain filtering to avoid nonexistent domain column, got query %q", db.querySQL)
	}
}

func TestInferDomainFromQuery(t *testing.T) {
	if got := inferDomainFromQuery("Cancer drug resistance"); got != "biomedical" {
		t.Fatalf("expected biomedical domain, got %q", got)
	}
	if got := inferDomainFromQuery("distributed neural network security"); got != "cs" {
		t.Fatalf("expected cs domain, got %q", got)
	}
}

func TestGetTopProviders_DefaultLimitAndErrorPaths(t *testing.T) {
	t.Run("NilDB", func(t *testing.T) {
		si := NewSearchIntelligence(nil)
		providers, err := si.GetTopProviders(context.Background(), "", 0)
		assert.Error(t, err)
		assert.Nil(t, providers)
	})

	t.Run("DefaultLimitAndSort", func(t *testing.T) {
		db := &spyDB{
			queryPlan: []queryResult{
				{
					rows: &fakeRows{
						values: [][]any{
							{"semantic_scholar", 2},
							{"pubmed", 1},
						},
						index: -1,
					},
				},
				{
					rows: &fakeRows{
						values: [][]any{
							{"semantic_scholar", 0.5},
							{"pubmed", 1.0},
						},
						index: -1,
					},
				},
			},
		}
		si := NewSearchIntelligence(db)
		providers, err := si.GetTopProviders(context.Background(), "", 0)
		assert.NoError(t, err)
		assert.Equal(t, []string{"pubmed", "semantic_scholar"}, providers)
	})

	t.Run("FallbackQueryError", func(t *testing.T) {
		db := &spyDB{
			queryPlan: []queryResult{
				{
					rows: &fakeRows{
						values: [][]any{{"semantic_scholar", 2}},
						index:  -1,
					},
				},
				{
					rows: &fakeRows{
						values: [][]any{},
						index:  -1,
					},
				},
				{err: errors.New("fallback failed")},
			},
		}
		si := NewSearchIntelligence(db)
		providers, err := si.GetTopProviders(context.Background(), "biomedical", 5)
		assert.Error(t, err)
		assert.Nil(t, providers)
	})
}

func TestSearchIntelligenceQueryHelpers(t *testing.T) {
	t.Run("ProviderAverageGains", func(t *testing.T) {
		db := &spyDB{queryPlan: []queryResult{{err: assert.AnError}}}
		si := NewSearchIntelligence(db)
		_, err := si.getProviderAverageGains(context.Background(), "")
		assert.Error(t, err)

		db = &spyDB{
			queryPlan: []queryResult{
				{
					rows: &fakeRows{
						values: [][]any{{"semantic_scholar", "cancer treatment", 2.0}},
						errors: []error{assert.AnError},
						index:  -1,
					},
				},
			},
		}
		si = NewSearchIntelligence(db)
		_, err = si.getProviderAverageGains(context.Background(), "biomedical")
		assert.Error(t, err)
	})

	t.Run("ProviderClickCounts", func(t *testing.T) {
		db := &spyDB{queryPlan: []queryResult{{err: assert.AnError}}}
		si := NewSearchIntelligence(db)
		_, err := si.getProviderClickCounts(context.Background())
		assert.Error(t, err)

		db = &spyDB{
			queryRows: &fakeRows{
				values: [][]any{{"semantic_scholar", 4}},
				errors: []error{assert.AnError},
				index:  -1,
			},
		}
		si = NewSearchIntelligence(db)
		_, err = si.getProviderClickCounts(context.Background())
		assert.Error(t, err)
	})

	t.Run("ProviderScoresAndClicks", func(t *testing.T) {
		si := NewSearchIntelligence(nil)
		scores, err := si.GetProviderScores(context.Background())
		assert.NoError(t, err)
		assert.Nil(t, scores)

		db := &spyDB{queryPlan: []queryResult{{err: assert.AnError}}}
		si = NewSearchIntelligence(db)
		_, err = si.GetProviderScores(context.Background())
		assert.Error(t, err)

		si = NewSearchIntelligence(nil)
		counts, err := si.GetClickCountsForQuery(context.Background(), "query", 10)
		assert.NoError(t, err)
		assert.Empty(t, counts)

		db = &spyDB{
			queryRows: &fakeRows{
				values: [][]any{
					{"paper-1", 3},
					{"paper-2", 1},
				},
				index: -1,
			},
		}
		si = NewSearchIntelligence(db)
		counts, err = si.GetClickCountsForQuery(context.Background(), "  Deep   Learning\tMedical ", 0)
		assert.NoError(t, err)
		assert.Equal(t, 200, db.queryArgs[1])
		assert.Equal(t, map[string]int{"paper-1": 3, "paper-2": 1}, counts)

		db = &spyDB{
			queryRows: &fakeRows{
				values: [][]any{{"paper-1", 3}},
				errors: []error{assert.AnError},
				index:  -1,
			},
		}
		si = NewSearchIntelligence(db)
		_, err = si.GetClickCountsForQuery(context.Background(), "query", 10)
		assert.Error(t, err)
	})

	t.Run("ExpandedQueryAndStrategyScores", func(t *testing.T) {
		si := NewSearchIntelligence(nil)
		scores, err := si.GetExpandedQueryScores(context.Background(), "query", 10)
		assert.NoError(t, err)
		assert.Nil(t, scores)

		db := &spyDB{
			queryRows: &fakeRows{
				values: [][]any{
					{"", "temporal", 2.0},
					{"graph theory", "query_refine", 1.6},
				},
				index: -1,
			},
		}
		si = NewSearchIntelligence(db)
		scores, err = si.GetExpandedQueryScores(context.Background(), "graph theory", 0)
		assert.NoError(t, err)
		assert.Equal(t, 10, db.queryArgs[1])
		assert.Len(t, scores, 1)
		assert.Equal(t, "graph theory", scores[0].Query)

		db = &spyDB{
			queryRows: &fakeRows{
				values: [][]any{{"graph theory", "query_refine", 1.6}},
				errors: []error{assert.AnError},
				index:  -1,
			},
		}
		si = NewSearchIntelligence(db)
		_, err = si.GetExpandedQueryScores(context.Background(), "graph theory", 5)
		assert.Error(t, err)

		si = NewSearchIntelligence(nil)
		strategyScores, err := si.GetStrategyScores(context.Background())
		assert.NoError(t, err)
		assert.Nil(t, strategyScores)

		db = &spyDB{
			queryRows: &fakeRows{
				values: [][]any{{"temporal", 1.8}},
				errors: []error{assert.AnError},
				index:  -1,
			},
		}
		si = NewSearchIntelligence(db)
		_, err = si.GetStrategyScores(context.Background())
		assert.Error(t, err)
	})
}

func TestSearchIntelligenceRemainingBranches(t *testing.T) {
	t.Run("TopProvidersTieBreakAndAvgGainError", func(t *testing.T) {
		db := &spyDB{
			queryPlan: []queryResult{
				{
					rows: &fakeRows{
						values: [][]any{
							{"semantic_scholar", 1},
							{"pubmed", 1},
						},
						index: -1,
					},
				},
				{
					rows: &fakeRows{
						values: [][]any{
							{"semantic_scholar", 1.0},
							{"pubmed", 1.0},
						},
						index: -1,
					},
				},
			},
		}
		si := NewSearchIntelligence(db)
		providers, err := si.GetTopProviders(context.Background(), "", 5)
		assert.NoError(t, err)
		assert.Equal(t, []string{"pubmed", "semantic_scholar"}, providers)

		db = &spyDB{
			queryPlan: []queryResult{
				{
					rows: &fakeRows{
						values: [][]any{{"semantic_scholar", 1}},
						index:  -1,
					},
				},
				{err: errors.New("avg failed")},
			},
		}
		si = NewSearchIntelligence(db)
		_, err = si.GetTopProviders(context.Background(), "", 5)
		assert.Error(t, err)
	})

	t.Run("ProviderAverageGainsScanErrorOnEmptyDomain", func(t *testing.T) {
		db := &spyDB{
			queryRows: &fakeRows{
				values: [][]any{{"semantic_scholar", 1.0}},
				errors: []error{assert.AnError},
				index:  -1,
			},
		}
		si := NewSearchIntelligence(db)
		_, err := si.getProviderAverageGains(context.Background(), "")
		assert.Error(t, err)
	})

	t.Run("ProviderScoresScanError", func(t *testing.T) {
		db := &spyDB{
			queryRows: &fakeRows{
				values: [][]any{{"semantic_scholar", 1.2}},
				errors: []error{assert.AnError},
				index:  -1,
			},
		}
		si := NewSearchIntelligence(db)
		_, err := si.GetProviderScores(context.Background())
		assert.Error(t, err)
	})

	t.Run("StrategyQueryError", func(t *testing.T) {
		db := &spyDB{queryPlan: []queryResult{{err: assert.AnError}}}
		si := NewSearchIntelligence(db)
		_, err := si.GetStrategyScores(context.Background())
		assert.Error(t, err)
	})
}
