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

	mdb.On(
		"Exec",
		mock.Anything,
		mock.MatchedBy(func(sql string) bool {
			return strings.Contains(sql, "INSERT INTO knowledge_entities")
		}),
		mock.MatchedBy(func(args []any) bool {
			if len(args) != 8 {
				return false
			}
			papers, ok := args[5].([]string)
			if !ok {
				return false
			}
			return args[1] == "project-1" &&
				args[2] == "finding" &&
				args[3] == hypothesis.Text &&
				args[4] == hypothesis.Claim &&
				reflect.DeepEqual(papers, []string{"paper-1", "paper-2"})
		}),
	).Return(pgconn.CommandTag{}, nil).Once()

	err := service.SaveFinding(context.Background(), "project-1", hypothesis)
	require.NoError(t, err)
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

func TestKnowledgeGraphService_GetRelevantPastFindingsPrefersVectorMatches(t *testing.T) {
	mdb := new(coverageMockDBProvider)
	service := NewKnowledgeGraphService(mdb)
	rows := &coverageFakeRows{values: [][]any{{"vector match"}}, index: -1}

	mdb.On(
		"Query",
		mock.Anything,
		mock.MatchedBy(func(sql string) bool {
			return strings.Contains(sql, "ORDER BY embedding <=> $1")
		}),
		mock.MatchedBy(func(args []any) bool {
			return len(args) == 1 && reflect.DeepEqual(args[0], []float64{0.1, 0.2})
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
			return strings.Contains(sql, "ORDER BY embedding <=> $1")
		}),
		mock.MatchedBy(func(args []any) bool {
			return len(args) == 1 && reflect.DeepEqual(args[0], []float64{0.4, 0.5})
		}),
	).Return(nil, errors.New("vector query failed")).Once()

	mdb.On(
		"Query",
		mock.Anything,
		mock.MatchedBy(func(sql string) bool {
			return strings.Contains(sql, "name LIKE $1")
		}),
		[]any{"%sleep memory%"},
	).Return(keywordRows, nil).Once()

	results, err := service.GetRelevantPastFindings(context.Background(), "user-1", "sleep memory", []float64{0.4, 0.5})
	require.NoError(t, err)
	assert.Equal(t, []string{"keyword match"}, results)
	assert.True(t, keywordRows.closed)
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
			return strings.Contains(sql, "SELECT hypothesis FROM research_dead_ends")
		}),
		[]any{"user-1", "%sleep%"},
	).Return(rows, nil).Once()

	results, err := service.GetRelevantDeadEnds(context.Background(), "user-1", "sleep")
	require.NoError(t, err)
	assert.Equal(t, []string{"dead end one", "dead end two"}, results)
	assert.True(t, rows.closed)
	assert.True(t, mdb.AssertExpectations(t))
}
