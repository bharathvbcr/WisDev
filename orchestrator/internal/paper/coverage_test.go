package paper

import (
	"bytes"
	"io"
	"testing"

	ledongpdf "github.com/ledongthuc/pdf"
	"github.com/stretchr/testify/assert"
)

func TestGenerateDocumentsWithoutSections(t *testing.T) {
	req := ExportRequest{
		Content: DocumentContent{
			Title:    "Empty Paper",
			Sections: nil,
		},
	}

	assert.Contains(t, GenerateMarkdown(req), "# Empty Paper")
	assert.NotContains(t, GenerateMarkdown(req), "## ")
	assert.Contains(t, GenerateHTML(req), "<h1>Empty Paper</h1>")
	assert.NotContains(t, GenerateHTML(req), "<h2>")
	assert.Contains(t, GenerateLaTeX(req), "\\title{Empty Paper}")
	assert.NotContains(t, GenerateLaTeX(req), "\\section{")
}

func TestExtractPDFText_Success(t *testing.T) {
	origNewPDFReader := newPDFReader
	origPageIsNull := pageIsNull
	origPagePlainText := pagePlainText
	t.Cleanup(func() {
		newPDFReader = origNewPDFReader
		pageIsNull = origPageIsNull
		pagePlainText = origPagePlainText
	})

	newPDFReader = func(r io.ReaderAt, size int64) (pdfReader, error) {
		return stubPDFReader{pages: []ledongpdf.Page{{}}}, nil
	}
	pageIsNull = func(page ledongpdf.Page) bool {
		return false
	}
	pagePlainText = func(page ledongpdf.Page) (string, error) {
		return "coverage fixture text", nil
	}

	text, err := ExtractPDFText(bytes.NewReader(nil), 0)
	assert.NoError(t, err)
	assert.Equal(t, "coverage fixture text\n", text)
}
