package wisdev

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestKnowledgeGraphService_SaveFindingPersistsEvidenceMetadata(t *testing.T) {
	mdb := new(coverageMockDBProvider)
	service := NewKnowledgeGraphService(mdb)
	hypothesis := &Hypothesis{
		Text:            "Sleep improves memory consolidation",
		Claim:           "Sleep improves memory consolidation",
		Category:        "neuroscience",
		ConfidenceScore: 0.91,
		EvidenceCount:   2,
		Evidence: []*EvidenceFinding{
			{SourceID: "paper-1"},
			{SourceID: "paper-2"},
		},
	}
	embedding := []float64{0.1, 0.2, 0.3}

	mdb.On(
		"Exec",
		mock.Anything,
		mock.MatchedBy(func(sql string) bool {
			return strings.Contains(sql, "INSERT INTO knowledge_entities") &&
				strings.Contains(sql, "user_id") &&
				strings.Contains(sql, "embedding")
		}),
		mock.MatchedBy(func(args []any) bool {
			if len(args) != 10 {
				return false
			}
			papers, ok := args[6].([]string)
			if !ok {
				return false
			}
			return args[1] == "project-1" &&
				args[2] == "user-1" &&
				args[3] == "finding" &&
				args[4] == hypothesis.Text &&
				args[5] == hypothesis.Claim &&
				reflect.DeepEqual(papers, []string{"paper-1", "paper-2"}) &&
				args[8] == "[0.1,0.2,0.3]"
		}),
	).Return(pgconn.CommandTag{}, nil).Once()

	err := service.SaveFinding(context.Background(), "project-1", "user-1", hypothesis, embedding)
	require.NoError(t, err)
	assert.True(t, mdb.AssertExpectations(t))
}

func TestKnowledgeGraphService_SaveFindingSkipsEmptyEmbedding(t *testing.T) {
	mdb := new(coverageMockDBProvider)
	service := NewKnowledgeGraphService(mdb)
	hypothesis := &Hypothesis{Text: "claim", Claim: "claim"}

	mdb.On(
		"Exec",
		mock.Anything,
		mock.MatchedBy(func(sql string) bool {
			return strings.Contains(sql, "INSERT INTO knowledge_entities")
		}),
		mock.MatchedBy(func(args []any) bool {
			return len(args) == 10 && args[8] == nil
		}),
	).Return(pgconn.CommandTag{}, nil).Once()

	err := service.SaveFinding(context.Background(), "project-1", "user-1", hypothesis, nil)
	require.NoError(t, err)
	assert.True(t, mdb.AssertExpectations(t))
}

func TestKnowledgeGraphService_SaveFindingRejectsEmptyUserID(t *testing.T) {
	mdb := new(coverageMockDBProvider)
	service := NewKnowledgeGraphService(mdb)
	err := service.SaveFinding(context.Background(), "project-1", "  ", &Hypothesis{Text: "claim"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "user_id is required")
	assert.True(t, mdb.AssertExpectations(t))
}

func TestKnowledgeGraphService_RecordDeadEndPersistsReasoning(t *testing.T) {
	mdb := new(coverageMockDBProvider)
	service := NewKnowledgeGraphService(mdb)
	hypothesis := &Hypothesis{
		Text:            "Overly broad causal claim",
		ConfidenceScore: 0.18,
		EvidenceCount:   7,
	}

	mdb.On(
		"Exec",
		mock.Anything,
		mock.MatchedBy(func(sql string) bool {
			return strings.Contains(sql, "INSERT INTO research_dead_ends")
		}),
		mock.MatchedBy(func(args []any) bool {
			if len(args) != 6 {
				return false
			}
			reasoning, ok := args[4].(string)
			if !ok {
				return false
			}
			return args[0] == "user-1" &&
				args[1] == "sleep and learning" &&
				args[2] == hypothesis.Text &&
				args[3] == hypothesis.EvidenceCount &&
				strings.Contains(reasoning, "Low confidence (0.18)") &&
				strings.Contains(reasoning, "7 papers found")
		}),
	).Return(pgconn.CommandTag{}, nil).Once()

	err := service.RecordDeadEnd(context.Background(), "user-1", "sleep and learning", hypothesis)
	require.NoError(t, err)
	assert.True(t, mdb.AssertExpectations(t))
}

func TestKnowledgeGraphService_RecordDeadEndRejectsNilHypothesisAndEmptyUserID(t *testing.T) {
	mdb := new(coverageMockDBProvider)
	service := NewKnowledgeGraphService(mdb)

	err := service.RecordDeadEnd(context.Background(), "user-1", "q", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hypothesis is required")

	err = service.RecordDeadEnd(context.Background(), "", "q", &Hypothesis{Text: "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "user_id is required")
	assert.True(t, mdb.AssertExpectations(t))
}

func TestKnowledgeGraphService_GetRelevantPastFindingsPrefersVectorMatches(t *testing.T) {
	mdb := new(coverageMockDBProvider)
	service := NewKnowledgeGraphService(mdb)
	rows := &coverageFakeRows{values: [][]any{{"vector match"}}, index: -1}

	mdb.On(
		"Query",
		mock.Anything,
		mock.MatchedBy(func(sql string) bool {
			return strings.Contains(sql, "ORDER BY embedding <=> $1::vector") &&
				strings.Contains(sql, "embedding IS NOT NULL") &&
				strings.Contains(sql, "user_id = $2")
		}),
		mock.MatchedBy(func(args []any) bool {
			return len(args) == 2 &&
				args[0] == "[0.1,0.2]" &&
				args[1] == "user-1"
		}),
	).Return(rows, nil).Once()

	results, err := service.GetRelevantPastFindings(context.Background(), "user-1", "sleep memory", []float64{0.1, 0.2})
	require.NoError(t, err)
	assert.Equal(t, []string{"vector match"}, results)
	assert.True(t, rows.closed)
	assert.True(t, mdb.AssertExpectations(t))
}

func TestKnowledgeGraphService_GetRelevantPastFindingsFallsBackToKeywordSearch(t *testing.T) {
	mdb := new(coverageMockDBProvider)
	service := NewKnowledgeGraphService(mdb)
	keywordRows := &coverageFakeRows{values: [][]any{{"keyword match"}}, index: -1}

	mdb.On(
		"Query",
		mock.Anything,
		mock.MatchedBy(func(sql string) bool {
			return strings.Contains(sql, "ORDER BY embedding <=> $1::vector")
		}),
		mock.MatchedBy(func(args []any) bool {
			return len(args) == 2 && args[0] == "[0.4,0.5]" && args[1] == "user-1"
		}),
	).Return(nil, errors.New("vector query failed")).Once()

	mdb.On(
		"Query",
		mock.Anything,
		mock.MatchedBy(func(sql string) bool {
			return strings.Contains(sql, "name LIKE $2") &&
				strings.Contains(sql, `ESCAPE '\'`) &&
				strings.Contains(sql, "user_id = $1")
		}),
		[]any{"user-1", "%sleep memory%"},
	).Return(keywordRows, nil).Once()

	results, err := service.GetRelevantPastFindings(context.Background(), "user-1", "sleep memory", []float64{0.4, 0.5})
	require.NoError(t, err)
	assert.Equal(t, []string{"keyword match"}, results)
	assert.True(t, keywordRows.closed)
	assert.True(t, mdb.AssertExpectations(t))
}

func TestKnowledgeGraphService_GetRelevantPastFindingsEscapesLikeWildcards(t *testing.T) {
	mdb := new(coverageMockDBProvider)
	service := NewKnowledgeGraphService(mdb)
	keywordRows := &coverageFakeRows{values: [][]any{{"safe match"}}, index: -1}

	mdb.On(
		"Query",
		mock.Anything,
		mock.MatchedBy(func(sql string) bool {
			return strings.Contains(sql, "name LIKE $2") &&
				strings.Contains(sql, `ESCAPE '\'`)
		}),
		[]any{"user-1", `%100\%\_growth%`},
	).Return(keywordRows, nil).Once()

	results, err := service.GetRelevantPastFindings(context.Background(), "user-1", "100%_growth", nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"safe match"}, results)
	assert.True(t, mdb.AssertExpectations(t))
}

func TestKnowledgeGraphService_GetRelevantPastFindingsSurfacesVectorScanAndRowsErr(t *testing.T) {
	t.Run("scan error", func(t *testing.T) {
		mdb := new(coverageMockDBProvider)
		service := NewKnowledgeGraphService(mdb)
		rows := &coverageFakeRows{
			values: [][]any{{"vector match"}},
			errors: []error{errors.New("scan failed")},
			index:  -1,
		}
		mdb.On(
			"Query",
			mock.Anything,
			mock.MatchedBy(func(sql string) bool {
				return strings.Contains(sql, "$1::vector")
			}),
			mock.Anything,
		).Return(rows, nil).Once()

		results, err := service.GetRelevantPastFindings(context.Background(), "user-1", "q", []float64{0.1})
		require.Error(t, err)
		assert.Nil(t, results)
		assert.True(t, rows.closed)
		assert.True(t, mdb.AssertExpectations(t))
	})

	t.Run("rows.Err", func(t *testing.T) {
		mdb := new(coverageMockDBProvider)
		service := NewKnowledgeGraphService(mdb)
		rows := &coverageFakeRows{
			values:  [][]any{{"vector match"}},
			index:   -1,
			iterErr: errors.New("iteration failed"),
		}
		mdb.On(
			"Query",
			mock.Anything,
			mock.MatchedBy(func(sql string) bool {
				return strings.Contains(sql, "$1::vector")
			}),
			mock.Anything,
		).Return(rows, nil).Once()

		results, err := service.GetRelevantPastFindings(context.Background(), "user-1", "q", []float64{0.1})
		require.Error(t, err)
		assert.Nil(t, results)
		assert.True(t, rows.closed)
		assert.True(t, mdb.AssertExpectations(t))
	})
}

func TestKnowledgeGraphService_GetRelevantPastFindingsRequiresUserID(t *testing.T) {
	mdb := new(coverageMockDBProvider)
	service := NewKnowledgeGraphService(mdb)

	results, err := service.GetRelevantPastFindings(context.Background(), "", "sleep memory", []float64{0.1})
	require.NoError(t, err)
	assert.Empty(t, results)
	assert.True(t, mdb.AssertExpectations(t))
}

func TestKnowledgeGraphService_GetRelevantDeadEndsScansRows(t *testing.T) {
	mdb := new(coverageMockDBProvider)
	service := NewKnowledgeGraphService(mdb)
	rows := &coverageFakeRows{values: [][]any{{"dead end one"}, {"dead end two"}}, index: -1}

	mdb.On(
		"Query",
		mock.Anything,
		mock.MatchedBy(func(sql string) bool {
			return strings.Contains(sql, "SELECT hypothesis FROM research_dead_ends") &&
				strings.Contains(sql, `ESCAPE '\'`)
		}),
		[]any{"user-1", "%sleep%"},
	).Return(rows, nil).Once()

	results, err := service.GetRelevantDeadEnds(context.Background(), "user-1", "sleep")
	require.NoError(t, err)
	assert.Equal(t, []string{"dead end one", "dead end two"}, results)
	assert.True(t, rows.closed)
	assert.True(t, mdb.AssertExpectations(t))
}

func TestFormatPGVectorLiteral(t *testing.T) {
	assert.Equal(t, "[]", formatPGVectorLiteral(nil))
	assert.Equal(t, "[0.1,0.2]", formatPGVectorLiteral([]float64{0.1, 0.2}))
}

func TestEscapeLikePattern(t *testing.T) {
	assert.Equal(t, `100\%\_growth`, escapeLikePattern("100%_growth"))
	assert.Equal(t, `a\\b\%c\_d`, escapeLikePattern(`a\b%c_d`))
}
