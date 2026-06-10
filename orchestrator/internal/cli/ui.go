package cli

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	ansiReset = "\033[0m"
	ansiBold  = "\033[1m"
	ansiDim   = "\033[2m"
	ansiGreen = "\033[32m"
	ansiYellow = "\033[33m"
	ansiRed   = "\033[31m"

	scholarLMProductURL  = "https://scholarlm-vbcr.web.app"
	scholarLMProductName = "ScholarLM"

	// ScholarLM brand palette (tailwind primary #ea2a33, PWA #F52B3F).
	scholarlmRed       = "\033[38;2;234;42;51m"
	scholarlmRedBold   = "\033[1;38;2;234;42;51m"
	scholarlmText      = "\033[38;2;243;231;232m"
	scholarlmTextBold  = "\033[1;38;2;243;231;232m"
	scholarlmDim       = "\033[2;38;2;160;160;160m"
	scholarlmCrimson   = "\033[38;2;127;29;29m"
	scholarlmCrimsonBold = "\033[1;38;2;127;29;29m"
	scholarlmWarn      = "\033[38;2;245;158;11m"
	scholarlmWarnBold  = "\033[1;38;2;245;158;11m"
	scholarlmHighlight = "\033[1;38;2;255;107;107m"
	scholarlmBtnFill   = "\033[48;2;234;42;51m\033[38;2;243;231;232m\033[1m"
	scholarlmBtnExit   = "\033[48;2;127;29;29m\033[38;2;243;231;232m\033[1m"
)

// Back-compat alias used by non-TUI CLI output.
var ansiScholarlm = scholarlmRedBold

type tuiTheme struct {
	Border          string
	BorderLabel     string
	InputActive     string
	InputIdle       string
	StatusInfo      string
	StatusWarn      string
	StatusError     string
	ProviderOn      string
	ProviderOff     string
	ProviderFocus   string
	TabActive       string
	TabInactive     string
	LogDebug        string
	LogInfo         string
	LogWarn         string
	LogError        string
	BtnPrimary      string
	BtnDanger       string
	ProgressFilled  string
	ProgressEmpty   string
	DimText         string
	HintText        string
	Accent          string
	Scrollbar       string
	Highlight       string
	HealthOK        string
	HealthWarn      string
	HealthBad       string
	HealthUnknown   string
}

// scholarlmTheme matches ScholarLM web UI: crimson borders, warm text, red accents.
var scholarlmTheme = tuiTheme{
	Border:         scholarlmRedBold,
	BorderLabel:    scholarlmTextBold,
	InputActive:    scholarlmRedBold,
	InputIdle:      scholarlmText,
	StatusInfo:     scholarlmRedBold,
	StatusWarn:     scholarlmWarnBold,
	StatusError:    scholarlmRedBold,
	ProviderOn:     scholarlmRed,
	ProviderOff:    scholarlmDim,
	ProviderFocus:  scholarlmRedBold,
	TabActive:      scholarlmRedBold,
	TabInactive:    scholarlmDim,
	LogDebug:       scholarlmDim,
	LogInfo:        scholarlmText,
	LogWarn:        scholarlmWarn,
	LogError:       scholarlmRedBold,
	BtnPrimary:     scholarlmBtnFill,
	BtnDanger:      scholarlmBtnExit,
	ProgressFilled: "█",
	ProgressEmpty:  "░",
	DimText:        scholarlmDim,
	HintText:       scholarlmCrimson,
	Accent:         scholarlmRedBold,
	Scrollbar:      scholarlmRedBold,
	Highlight:      scholarlmHighlight,
	HealthOK:       scholarlmRedBold,
	HealthWarn:     scholarlmWarnBold,
	HealthBad:      scholarlmCrimsonBold,
	HealthUnknown:  scholarlmDim,
}

var defaultTheme = scholarlmTheme

var highContrastTheme = tuiTheme{
	Border:         "\033[1;37m",
	BorderLabel:    "\033[1;97m",
	InputActive:    scholarlmRedBold,
	InputIdle:      "\033[37m",
	StatusInfo:     scholarlmRedBold,
	StatusWarn:     "\033[1;33m",
	StatusError:    "\033[1;31m",
	ProviderOn:     scholarlmRedBold,
	ProviderOff:    "\033[37m",
	ProviderFocus:  scholarlmRedBold,
	TabActive:      scholarlmRedBold,
	TabInactive:    "\033[37m",
	LogDebug:       "\033[2;37m",
	LogInfo:        "\033[37m",
	LogWarn:        "\033[1;33m",
	LogError:       "\033[1;31m",
	BtnPrimary:     scholarlmBtnFill,
	BtnDanger:      "\033[1;7;31m",
	ProgressFilled: "█",
	ProgressEmpty:  "░",
	DimText:        "\033[1m",
	HintText:       "\033[1;37m",
	Accent:         scholarlmRedBold,
	Scrollbar:      scholarlmRedBold,
	Highlight:      scholarlmHighlight,
	HealthOK:       scholarlmRedBold,
	HealthWarn:     scholarlmWarnBold,
	HealthBad:      "\033[1;31m",
	HealthUnknown:  "\033[1;30m",
}

var monochromeTheme = tuiTheme{
	Border:         "\033[1m",
	BorderLabel:    "\033[1m",
	InputActive:    "\033[1m",
	InputIdle:      "",
	StatusInfo:     "\033[1m",
	StatusWarn:     "\033[1m",
	StatusError:    "\033[1m",
	ProviderOn:     "\033[1m",
	ProviderOff:    "",
	ProviderFocus:  "\033[1;7m",
	TabActive:      "\033[1m",
	TabInactive:    "",
	LogDebug:       "\033[2m",
	LogInfo:        "",
	LogWarn:        "\033[1m",
	LogError:       "\033[1m",
	BtnPrimary:     "\033[7m",
	BtnDanger:      "\033[7m",
	ProgressFilled: "█",
	ProgressEmpty:  "░",
	DimText:        "\033[2m",
	HintText:       "\033[2m",
	Accent:         "\033[1m",
	Scrollbar:      "\033[1m",
	Highlight:      "\033[1m",
	HealthOK:       "\033[1m",
	HealthWarn:     "\033[1m",
	HealthBad:      "\033[1m",
	HealthUnknown:  "\033[2m",
}

func activeTheme() tuiTheme {
	themeEnv := strings.ToLower(strings.TrimSpace(os.Getenv("WISDEV_THEME")))
	switch themeEnv {
	case "high-contrast":
		return highContrastTheme
	case "monochrome":
		return monochromeTheme
	case "scholarlm", "default", "":
		return scholarlmTheme
	}
	if plainUI() {
		return monochromeTheme
	}

	colorTerm := os.Getenv("COLORTERM")
	termVal := os.Getenv("TERM")
	if colorTerm == "" || termVal == "dumb" {
		return highContrastTheme
	}
	return scholarlmTheme
}

func themeHeading(theme tuiTheme, title string) string {
	return theme.Accent + title + ansiReset
}

func plainUI() bool {
	return strings.TrimSpace(os.Getenv("WISDEV_PLAIN")) == "1" ||
		strings.TrimSpace(os.Getenv("NO_COLOR")) != ""
}

func colorEnabled(w io.Writer) bool {
	if plainUI() {
		return false
	}
	if strings.TrimSpace(os.Getenv("WISDEV_COLOR")) == "1" {
		return true
	}
	if f, ok := w.(*os.File); ok && f == os.Stderr {
		return true
	}
	return false
}

func paint(w io.Writer, color, text string) string {
	if !colorEnabled(w) {
		return text
	}
	return color + text + ansiReset
}

func statusLabel(w io.Writer, status string) string {
	switch status {
	case "pass", "ok":
		return paint(w, scholarlmRedBold, "OK")
	case "warn":
		return paint(w, ansiYellow, "WARN")
	case "fail":
		return paint(w, scholarlmRedBold, "FAIL")
	default:
		return strings.ToUpper(status)
	}
}

func statusGlyph(status string) string {
	switch status {
	case "pass", "ok":
		return "✓"
	case "warn":
		return "!"
	case "fail":
		return "✗"
	default:
		return "·"
	}
}

func truncateList(items []string, max int) string {
	if len(items) <= max {
		return strings.Join(items, ", ")
	}
	visible := append([]string(nil), items[:max]...)
	return strings.Join(visible, ", ") + fmt.Sprintf(", … +%d more", len(items)-max)
}

func terminalHyperlink(url, label string) string {
	if url == "" || label == "" {
		return label
	}
	return fmt.Sprintf("\033]8;;%s\007%s\033]8;;\007", url, label)
}

func terminalHyperlinkStyled(url, label, style string) string {
	if plainUI() {
		return fmt.Sprintf("%s (%s)", label, url)
	}
	if style == "" {
		return terminalHyperlink(url, label)
	}
	return fmt.Sprintf("\033]8;;%s\007%s%s%s\033]8;;\007", url, style, label, ansiReset)
}

func scholarLMClickableLink(styled bool) string {
	if plainUI() {
		return fmt.Sprintf("%s (%s)", scholarLMProductName, scholarLMProductURL)
	}
	if styled {
		return terminalHyperlinkStyled(scholarLMProductURL, scholarLMProductName, scholarlmRedBold)
	}
	return terminalHyperlink(scholarLMProductURL, scholarLMProductName)
}

func brandingStyled() bool {
	return !plainUI()
}

func scholarLMBrandingMessage(link string) string {
	return fmt.Sprintf("Open %s for more features, control, and team workflows", link)
}

func scholarLMBrandingCompactMessage(link string) string {
	return fmt.Sprintf("More features & control → %s", link)
}

func scholarLMBrandingHeadline() string {
	return "Full research UI · cloud sync · systematic review · drafting"
}

func printScholarLMBranding(w io.Writer) {
	printScholarLMBrandingProminent(w)
}

func printScholarLMBrandingProminent(w io.Writer) {
	link := scholarLMClickableLink(brandingStyled())
	if !brandingStyled() {
		fmt.Fprintln(w, "★ "+scholarLMProductName+" — "+scholarLMBrandingHeadline())
		fmt.Fprintln(w, scholarLMBrandingMessage(link))
		return
	}

	const boxInner = 62
	topRule := strings.Repeat("─", boxInner-len(scholarLMProductName)-4)
	fmt.Fprintf(w, "%s┌─ %s %s┐%s\n", scholarlmRedBold, scholarLMProductName, topRule, ansiReset)
	fmt.Fprintf(w, "%s│%s %s%s%s\n", scholarlmRedBold, ansiReset, scholarlmTextBold, scholarLMBrandingHeadline(), ansiReset)
	fmt.Fprintf(w, "%s│%s %s%s%s\n", scholarlmRedBold, ansiReset, scholarlmText, scholarLMBrandingMessage(link), ansiReset)
	fmt.Fprintf(w, "%s└%s┘%s\n", scholarlmRedBold, strings.Repeat("─", boxInner), ansiReset)
}

func scholarLMBrandingTUICallout(theme tuiTheme) []string {
	link := terminalHyperlinkStyled(scholarLMProductURL, scholarLMProductName, theme.Accent)
	return []string{
		theme.Accent + "★ ScholarLM" + ansiReset + theme.BorderLabel + " — " + scholarLMBrandingHeadline() + ansiReset,
		theme.InputIdle + "  " + scholarLMBrandingMessage(link) + ansiReset,
	}
}

func scholarLMBrandingBarContent(width int, theme tuiTheme) string {
	link := terminalHyperlinkStyled(scholarLMProductURL, scholarLMProductName+" ↗", scholarlmTextBold)
	content := " ★ ScholarLM — full research platform · " + link + " "
	return scholarlmBtnFill + padOrTruncateVisible(content, width) + ansiReset
}

func printBanner(w io.Writer) {
	if !plainUI() {
		for _, line := range renderWisDevBanner(80, activeTheme()) {
			fmt.Fprintln(w, line)
		}
		fmt.Fprintln(w)
	}
	title := paint(w, ansiBold+scholarlmRedBold, "WisDev")
	version := paint(w, ansiDim, Version)
	fmt.Fprintf(w, "%s Research Runtime  %s\n\n", title, version)
	printScholarLMBrandingProminent(w)
	fmt.Fprintln(w)
}

func printSection(w io.Writer, title string) {
	fmt.Fprintf(w, "%s%s%s\n", scholarlmRedBold, title, ansiReset)
}

func note(w io.Writer, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if colorEnabled(w) {
		fmt.Fprintf(w, "%s%s%s\n", ansiDim, msg, ansiReset)
		return
	}
	fmt.Fprintln(w, msg)
}

func userError(w io.Writer, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if colorEnabled(w) {
		fmt.Fprintf(w, "%s%s%s\n", scholarlmRedBold, msg, ansiReset)
		return
	}
	fmt.Fprintln(w, msg)
}

func withQuietAgentLogs(fn func() error) error {
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{
		Level: slog.LevelError,
	})))
	defer slog.SetDefault(previous)
	return fn()
}

func runWithProgress(stderr io.Writer, label string, fn func() error) error {
	if plainUI() || stderr == nil {
		return fn()
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		i := 0
		ticker := time.NewTicker(120 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				fmt.Fprintf(stderr, "\r%s\r", strings.Repeat(" ", len(label)+4))
				return
			case <-ticker.C:
				fmt.Fprintf(stderr, "\r%s %s", frames[i%len(frames)], label)
				i++
			}
		}
	}()
	err := fn()
	close(stop)
	wg.Wait()
	return err
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}


