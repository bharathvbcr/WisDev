package wisdev

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeAnswerValues(t *testing.T) {
	values, displayValues := NormalizeAnswerValues(false, []string{" 1year ", "5years", "1year"}, []string{" Last Year ", "Last 5 Years", "Last Year"})
	assert.Equal(t, []string{"1year"}, values)
	assert.Equal(t, []string{"Last Year"}, displayValues)

	values, displayValues = NormalizeAnswerValues(true, []string{" retrieval ", "reranking", "retrieval"}, []string{" Retrieval ", "", "Duplicate"})
	assert.Equal(t, []string{"retrieval", "reranking"}, values)
	assert.Equal(t, []string{"Retrieval", "reranking"}, displayValues)
}

func TestAnswerNormalizationCrucialEdges(t *testing.T) {
	assert.True(t, IsKnownSingleSelectQuestionID(" q2_scope "))
	assert.True(t, IsKnownSingleSelectQuestionID("q3_timeframe"))
	assert.False(t, IsKnownSingleSelectQuestionID("q4_subtopics"))

	values, displayValues := NormalizeAnswerValues(true, []string{" ", "\t"}, []string{"Blank"})
	assert.Equal(t, []string{}, values)
	assert.Equal(t, []string{}, displayValues)

	answer := NormalizeAnswerForQuestion(Answer{
		QuestionID:    " q3_timeframe ",
		Values:        []string{" short ", "long"},
		DisplayValues: []string{" Short timeframe ", "Long timeframe"},
		AnsweredAt:    123,
	}, false)
	assert.Equal(t, "q3_timeframe", answer.QuestionID)
	assert.Equal(t, []string{"short"}, answer.Values)
	assert.Equal(t, []string{"Short timeframe"}, answer.DisplayValues)
	assert.Equal(t, int64(123), answer.AnsweredAt)
}
