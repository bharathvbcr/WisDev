package wisdev

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveSessionSearchQuery_Priorities(t *testing.T) {
	tests := []struct {
		name           string
		query          string
		correctedQuery string
		originalQuery  string
		expected       string
	}{
		{
			name:           "Priority: Query > Corrected > Original",
			query:          "final query",
			correctedQuery: "corrected query",
			originalQuery:  "original query",
			expected:       "final query",
		},
		{
			name:           "Priority: Corrected > Original (Query empty)",
			query:          "",
			correctedQuery: "corrected query",
			originalQuery:  "original query",
			expected:       "corrected query",
		},
		{
			name:           "Priority: Original (Query and Corrected empty)",
			query:          "",
			correctedQuery: "",
			originalQuery:  "original query",
			expected:       "original query",
		},
		{
			name:           "Normalization: Query with spaces",
			query:          "  spaced   query  ",
			correctedQuery: "",
			originalQuery:  "",
			expected:       "spaced query",
		},
		{
			name:           "All empty",
			query:          "",
			correctedQuery: "",
			originalQuery:  "",
			expected:       "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ResolveSessionSearchQuery(tt.query, tt.correctedQuery, tt.originalQuery)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestQueryPreview(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		expected string
	}{
		{
			name:     "Short query",
			query:    "How does CRISPR work?",
			expected: "How does CRISPR work?",
		},
		{
			name:     "Long query truncation",
			query:    "This is a very long query that exceeds the limit of ninety-six characters and should be truncated with ellipses to maintain readability in logs and UI components.",
			expected: "This is a very long query that exceeds the limit of ninety-six characters and should be trunc...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := QueryPreview(tt.query)
			assert.Equal(t, tt.expected, result)
		})
	}
}
