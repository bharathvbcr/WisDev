package paper

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

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
	pdfCandidates := []string{
		filepath.Join("..", "..", "..", "artifacts", "github.com", "ledongthuc", "pdf@v0.0.0-20250511090121-5959a4027728", "examples", "read_plain_text", "pdf_test.pdf"),
		filepath.Join("..", "..", "..", "artifacts", "gomodcache", "github.com", "ledongthuc", "pdf@v0.0.0-20250511090121-5959a4027728", "examples", "read_plain_text", "pdf_test.pdf"),
		filepath.Join("..", "..", "..", ".gomodcache", "github.com", "ledongthuc", "pdf@v0.0.0-20250511090121-5959a4027728", "examples", "read_plain_text", "pdf_test.pdf"),
		filepath.Join("..", "..", "..", ".gopath", "pkg", "mod", "github.com", "ledongthuc", "pdf@v0.0.0-20250511090121-5959a4027728", "examples", "read_plain_text", "pdf_test.pdf"),
	}

	var pdfPath string
	for _, candidate := range pdfCandidates {
		if _, err := os.Stat(candidate); err == nil {
			pdfPath = candidate
			break
		}
	}
	if pdfPath == "" {
		t.Fatalf("pdf fixture not found in cache candidates: %v", pdfCandidates)
	}

	data, err := os.ReadFile(pdfPath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	text, err := ExtractPDFText(bytes.NewReader(data), int64(len(data)))
	assert.NoError(t, err)
	assert.NotEmpty(t, text)
}
