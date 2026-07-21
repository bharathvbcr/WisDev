package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestScholarLMClickableLink(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("WISDEV_PLAIN", "")

	link := scholarLMClickableLink(true)
	if !strings.Contains(link, scholarLMProductURL) {
		t.Fatalf("expected product URL in link, got %q", link)
	}
	if !strings.Contains(link, scholarLMProductName) {
		t.Fatalf("expected product name in link, got %q", link)
	}
	if !strings.Contains(link, "\033]8;;") {
		t.Fatalf("expected OSC 8 hyperlink, got %q", link)
	}
}

func TestScholarLMClickableLinkPlain(t *testing.T) {
	t.Setenv("WISDEV_PLAIN", "1")

	link := scholarLMClickableLink(true)
	if !strings.Contains(link, scholarLMProductURL) {
		t.Fatalf("expected plain URL in link, got %q", link)
	}
	if strings.Contains(link, "\033]8;;") {
		t.Fatalf("expected no OSC hyperlink in plain mode, got %q", link)
	}
}

func TestPrintScholarLMBranding(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("WISDEV_PLAIN", "")

	var buf bytes.Buffer
	printScholarLMBranding(&buf)
	out := buf.String()
	if !strings.Contains(out, scholarLMProductName) {
		t.Fatalf("expected branding name, got %q", out)
	}
	if !strings.Contains(out, "more features") {
		t.Fatalf("expected branding tagline, got %q", out)
	}
	if brandingStyled() && !strings.Contains(out, "┌─") {
		t.Fatalf("expected prominent branding box, got %q", out)
	}
}

func TestScholarLMBrandingBarContent(t *testing.T) {
	t.Setenv("WISDEV_PLAIN", "")
	theme := scholarlmTheme
	bar := scholarLMBrandingBarContent(80, theme)
	if !strings.Contains(bar, scholarLMProductName) {
		t.Fatalf("expected product name in bar, got %q", bar)
	}
	if visibleWidth(bar) != 80 {
		t.Fatalf("expected bar padded to width 80, visible=%d", visibleWidth(bar))
	}
}

func TestPrintUsageIncludesScholarLM(t *testing.T) {
	var buf bytes.Buffer
	printUsage(&buf)
	out := buf.String()
	if !strings.Contains(out, "ScholarLM") {
		t.Fatalf("expected ScholarLM section in usage, got %q", out)
	}
}

func TestRunVersionIncludesScholarLMBranding(t *testing.T) {
	var buf bytes.Buffer
	if err := runVersion(&buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), scholarLMProductName) {
		t.Fatalf("expected ScholarLM branding in version output, got %q", buf.String())
	}
}
