package citations

import (
	"fmt"
	"strings"
)

// Style names the supported bibliography / in-text citation formats.
type Style string

const (
	StyleAPA       Style = "apa"
	StyleMLA       Style = "mla"
	StyleChicago   Style = "chicago"
	StyleVancouver Style = "vancouver"
	StyleIEEE      Style = "ieee"
	StyleHarvard   Style = "harvard"
	StyleNature    Style = "nature"
)

// OutputFormat selects escaping / markup for rendered bibliography lines.
type OutputFormat string

const (
	FormatPlain    OutputFormat = "plain"
	FormatMarkdown OutputFormat = "markdown"
	FormatHTML     OutputFormat = "html"
	FormatLaTeX    OutputFormat = "latex"
)

// Entry is a structured bibliography record used by formatters.
type Entry struct {
	ID        string
	Authors   []string
	Year      int
	Title     string
	Journal   string
	Volume    string
	Issue     string
	Pages     string
	DOI       string
	URL       string
	Publisher string
}

// ParseStyle normalizes a user-provided style name.
func ParseStyle(raw string) (Style, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "apa":
		return StyleAPA, nil
	case "mla":
		return StyleMLA, nil
	case "chicago":
		return StyleChicago, nil
	case "vancouver":
		return StyleVancouver, nil
	case "ieee":
		return StyleIEEE, nil
	case "harvard":
		return StyleHarvard, nil
	case "nature":
		return StyleNature, nil
	default:
		return "", fmt.Errorf("unknown citation style %q (want apa, mla, chicago, vancouver, ieee, harvard, or nature)", raw)
	}
}

// ParseOutputFormat normalizes an export/render format name.
func ParseOutputFormat(raw string) (OutputFormat, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "plain", "text":
		return FormatPlain, nil
	case "markdown", "md":
		return FormatMarkdown, nil
	case "html":
		return FormatHTML, nil
	case "latex", "tex":
		return FormatLaTeX, nil
	default:
		return "", fmt.Errorf("unknown output format %q", raw)
	}
}
