package rag

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDetectSectionType(t *testing.T) {
	assert.Equal(t, "table", detectSectionType("Table 1. Summary"))
	assert.Equal(t, "figure", detectSectionType("Fig. 2. Overview"))
	assert.Equal(t, "", detectSectionType("plain paragraph"))
}

func TestDetectSections(t *testing.T) {
	is := assert.New(t)

	text := `Abstract: This is the abstract.
Introduction: This is the introduction.
Methods: These are the methods.
Results: These are the results.
Discussion: This is the discussion.
Conclusion: This is the conclusion.
References: These are the references.`

	sections := DetectSections(text)
	is.Len(sections, 7)
	is.Equal("abstract", sections[0].Type)
	is.Equal("introduction", sections[1].Type)
	is.Equal("methods", sections[2].Type)
	is.Equal("results", sections[3].Type)
	is.Equal("discussion", sections[4].Type)
	is.Equal("conclusion", sections[5].Type)
	is.Equal("references", sections[6].Type)
}

func TestDetectSections_CodeBlockAndUnknown(t *testing.T) {
	text := "Plain intro line\n```go\nfmt.Println(\"hi\")\n```\nTable 1: Results"

	sections := DetectSections(text)
	assert.Len(t, sections, 3)
	assert.Equal(t, "unknown", sections[0].Type)
	assert.Equal(t, "code", sections[1].Type)
	assert.Equal(t, "table", sections[2].Type)
	assert.Contains(t, sections[1].Content, "fmt.Println")
}

func TestDetectSections_EmptyReturnsUnknown(t *testing.T) {
	sections := DetectSections("")
	assert.Len(t, sections, 1)
	assert.Equal(t, "unknown", sections[0].Type)
	assert.Equal(t, "", sections[0].Content)
}

func TestAdaptiveChunking(t *testing.T) {
	is := assert.New(t)

	text := "Abstract: This is a very long abstract that should be chunked if the limit is small enough. " +
		"Actually, let's just use a short text and small limit."
	
	// initialSize 10 tokens (approx 40 chars)
	chunks := AdaptiveChunking(text, "paper1", 10, 2)
	is.NotEmpty(chunks)
	is.Contains(chunks[0].ID, "paper1_chunk_0")
	is.NotEmpty(chunks[0].Content)
}

func TestChunkText(t *testing.T) {
	is := assert.New(t)

	text := "12345678901234567890123456789012345678901234567890" // 50 chars
	
	t.Run("Small text, large limit", func(t *testing.T) {
		// size 20 tokens = 80 chars
		chunks := chunkText(text, "p1", 20, 5, "abstract", 0)
		is.Len(chunks, 1)
		is.Equal(text, chunks[0].Content)
	})

	t.Run("Large text, small limit", func(t *testing.T) {
		// size 5 tokens = 20 chars, overlap 1 token = 4 chars
		chunks := chunkText(text, "p1", 5, 1, "abstract", 0)
		is.Greater(len(chunks), 1)
	})
	
	t.Run("Overlap larger than size", func(t *testing.T) {
		// size 5 tokens, overlap 10 tokens -> step 1
		chunks := chunkText(text, "p1", 5, 10, "abstract", 0)
		is.NotEmpty(chunks)
	})
}
