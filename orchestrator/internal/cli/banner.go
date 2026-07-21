package cli

// banner.go renders the WisDev trident banner used by the TUI input screen
// and the `wisdev help` header. The art evokes the ScholarLM trident mark:
// three pronged spears over a long shaft with a diamond joint, drawn with
// box/geometric characters and a vertical red gradient from the theme.

const (
	wisdevBannerTagline    = "autonomous research agent"
	wisdevBannerSubline    = "plan · search · synthesize"
	wisdevBannerWidthLimit = 60
)

// wisdevBannerFallbackLine is the no-art, single-line banner used in plain
// mode and in terminals narrower than the art.
func wisdevBannerFallbackLine(styled bool, theme tuiTheme) string {
	if !styled {
		return "WisDev — " + wisdevBannerTagline
	}
	return theme.Accent + "WisDev" + ansiReset + " — " + wisdevBannerTagline
}

// renderWisDevBanner returns the banner lines for the given terminal width.
// Plain mode (WISDEV_PLAIN=1 / NO_COLOR) yields one unstyled line; widths
// narrower than the art fall back to a single styled line.
func renderWisDevBanner(width int, theme tuiTheme) []string {
	if plainUI() {
		return []string{wisdevBannerFallbackLine(false, theme)}
	}
	art := wisdevBannerArt(theme)
	maxWidth := 0
	for _, line := range art {
		if w := visibleWidth(line); w > maxWidth {
			maxWidth = w
		}
	}
	if width > 0 && width < maxWidth {
		return []string{wisdevBannerFallbackLine(true, theme)}
	}
	return art
}

// wisdevBannerArt draws the trident with a top-to-bottom gradient: bright
// highlight on the prong tips, brand red on the arms, crimson on the shaft.
func wisdevBannerArt(theme tuiTheme) []string {
	tip := theme.Highlight  // prong tips — brightest
	arm := theme.Accent     // crossbar + diamond joint
	shaft := theme.HintText // shaft — darkest
	word := theme.BorderLabel
	dim := theme.DimText
	return []string{
		"  " + tip + "▲     ▲     ▲" + ansiReset,
		"  " + tip + "█     █     █" + ansiReset + "      " + word + "WisDev" + ansiReset,
		"  " + arm + "╚════◆█◆════╝" + ansiReset + "      " + dim + wisdevBannerTagline + ansiReset,
		"  " + arm + "      █" + ansiReset + "            " + dim + wisdevBannerSubline + ansiReset,
		"  " + shaft + "      █" + ansiReset,
		"  " + shaft + "      ▼" + ansiReset,
	}
}
