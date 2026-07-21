package paper

import (
	"fmt"
	"io"
	"strings"

	"github.com/ledongthuc/pdf"
)

type pdfReader interface {
	NumPage() int
	Page(int) pdf.Page
}

var newPDFReader = func(r io.ReaderAt, size int64) (pdfReader, error) {
	return pdf.NewReader(r, size)
}

var pageIsNull = func(page pdf.Page) bool {
	return page.V.IsNull()
}

var pagePlainText = func(page pdf.Page) (string, error) {
	return page.GetPlainText(nil)
}

// ExtractPDFText extracts text from a PDF reader.
func ExtractPDFText(r io.ReaderAt, size int64) (string, error) {
	reader, err := newPDFReader(r, size)
	if err != nil {
		return "", fmt.Errorf("failed to create pdf reader: %w", err)
	}

	var textBuilder strings.Builder
	numPages := reader.NumPage()

	for i := 1; i <= numPages; i++ {
		page := reader.Page(i)
		if pageIsNull(page) {
			continue
		}

		content, err := pagePlainText(page)
		if err != nil {
			return "", fmt.Errorf("failed to get text from page %d: %w", i, err)
		}
		textBuilder.WriteString(content)
		textBuilder.WriteString("\n")
	}

	return textBuilder.String(), nil
}
