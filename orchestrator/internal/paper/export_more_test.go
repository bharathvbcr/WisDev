package paper

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGenerateHTML(t *testing.T) {
	req := ExportRequest{
		Content: DocumentContent{
			Title: "Test Paper",
			Sections: []Section{
				{Name: "Intro", Content: "Hello world."},
			},
		},
	}
	html := GenerateHTML(req)
	assert.Contains(t, html, "<title>Test Paper</title>")
	assert.Contains(t, html, "<h2>Intro</h2>")
}

func TestGenerateLaTeX(t *testing.T) {
	req := ExportRequest{
		Content: DocumentContent{
			Title: "Test Paper",
			Sections: []Section{
				{Name: "Intro", Content: "Hello world."},
			},
		},
	}
	latex := GenerateLaTeX(req)
	assert.Contains(t, latex, "\\documentclass{article}")
	assert.Contains(t, latex, "\\title{Test Paper}")
}
