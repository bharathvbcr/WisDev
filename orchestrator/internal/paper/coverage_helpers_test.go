package paper

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ledongthuc/pdf"
	"github.com/stretchr/testify/assert"
)

func TestAppendProfilerStructuredOutputInstruction(t *testing.T) {
	t.Run("blank prompt returns instruction", func(t *testing.T) {
		got := appendProfilerStructuredOutputInstruction("   ")
		if got != profilerStructuredOutputSchemaInstruction {
			t.Fatalf("unexpected instruction: got %q want %q", got, profilerStructuredOutputSchemaInstruction)
		}
	})

	t.Run("trimmed prompt appends instruction", func(t *testing.T) {
		got := appendProfilerStructuredOutputInstruction("  profile this paper  \n")
		want := "profile this paper\n\n" + profilerStructuredOutputSchemaInstruction
		if got != want {
			t.Fatalf("unexpected instruction: got %q want %q", got, want)
		}
	})
}

func TestExtractPDFTextErrorPaths(t *testing.T) {
	origNewPDFReader := newPDFReader
	origPageIsNull := pageIsNull
	origPagePlainText := pagePlainText
	t.Cleanup(func() {
		newPDFReader = origNewPDFReader
		pageIsNull = origPageIsNull
		pagePlainText = origPagePlainText
	})

	t.Run("reader creation failure", func(t *testing.T) {
		newPDFReader = func(r io.ReaderAt, size int64) (pdfReader, error) {
			return nil, errors.New("reader failure")
		}

		_, err := ExtractPDFText(strings.NewReader("x"), 1)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to create pdf reader")
	})

	t.Run("skips null pages and surfaces page text errors", func(t *testing.T) {
		newPDFReader = func(r io.ReaderAt, size int64) (pdfReader, error) {
			return &fakePDFReader{pages: 2}, nil
		}
		pageIsNull = func(page pdf.Page) bool {
			return false
		}
		pagePlainText = func(page pdf.Page) (string, error) {
			return "", errors.New("page text failure")
		}

		_, err := ExtractPDFText(strings.NewReader("x"), 1)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get text from page")
	})

	t.Run("skips null page without text call", func(t *testing.T) {
		calls := 0
		newPDFReader = func(r io.ReaderAt, size int64) (pdfReader, error) {
			return &fakePDFReader{pages: 1}, nil
		}
		pageIsNull = func(page pdf.Page) bool {
			return true
		}
		pagePlainText = func(page pdf.Page) (string, error) {
			calls++
			return "", nil
		}

		text, err := ExtractPDFText(strings.NewReader("x"), 1)
		assert.NoError(t, err)
		assert.Empty(t, text)
		assert.Zero(t, calls)
	})
}

type fakePDFReader struct {
	pages int
}

func (f *fakePDFReader) NumPage() int {
	return f.pages
}

func (f *fakePDFReader) Page(int) pdf.Page {
	return pdf.Page{}
}
