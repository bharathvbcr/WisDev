package paper

import (
	"fmt"
	"io"
	"strings"

	"github.com/ledongthuc/pdf"
)

// ExtractPDFText extracts text from a PDF reader.
func ExtractPDFText(r io.ReaderAt, size int64) (string, error) {
	reader, err := pdf.NewReader(r, size)
	if err != nil {
		return "", fmt.Errorf("failed to create pdf reader: %w", err)
	}

	var textBuilder strings.Builder
	numPages := reader.NumPage()

	for i := 1; i <= numPages; i++ {
		page := reader.Page(i)
		if page.V.IsNull() {
			continue
		}

		content, err := page.GetPlainText(nil)
		if err != nil {
			return "", fmt.Errorf("failed to get text from page %d: %w", i, err)
		}
		textBuilder.WriteString(content)
		textBuilder.WriteString("\n")
	}

	return textBuilder.String(), nil
}
