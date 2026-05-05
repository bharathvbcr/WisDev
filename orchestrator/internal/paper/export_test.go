package paper

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExport(t *testing.T) {
	is := assert.New(t)

	req := ExportRequest{
		Content: DocumentContent{
			Title: "Test Title",
			Sections: []Section{
				{Name: "Intro", Content: "Intro content"},
				{Name: "Body", Content: "Body content"},
			},
		},
	}

	t.Run("GenerateMarkdown", func(t *testing.T) {
		md := GenerateMarkdown(req)
		is.Contains(md, "# Test Title")
		is.Contains(md, "## Intro")
		is.Contains(md, "Intro content")
	})

	t.Run("GenerateHTML", func(t *testing.T) {
		html := GenerateHTML(req)
		is.Contains(html, "<title>Test Title</title>")
		is.Contains(html, "<h2>Intro</h2>")
		is.Contains(html, "<p>Intro content</p>")
	})

	t.Run("GenerateLaTeX", func(t *testing.T) {
		tex := GenerateLaTeX(req)
		is.Contains(tex, "\\title{Test Title}")
		is.Contains(tex, "\\section{Intro}")
		is.Contains(tex, "Intro content")
	})
}
