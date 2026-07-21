package wisdev

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeSignalID_EdgeCases(t *testing.T) {
	is := assert.New(t)

	t.Run("empty value", func(t *testing.T) {
		is.Equal("", normalizeSignalID("prefix", ""))
		is.Equal("", normalizeSignalID("prefix", "   "))
	})

	t.Run("valid value", func(t *testing.T) {
		is.Equal("prefix:value", normalizeSignalID("prefix", "value"))
		is.Equal("prefix:value", normalizeSignalID("prefix", "  value  "))
	})
}

func TestExtractDiscoverySignals_EdgeCases(t *testing.T) {
	is := assert.New(t)

	t.Run("maxSignals boundaries", func(t *testing.T) {
		// Using a text that only matches models to avoid ambiguity with ArXiv matching camelCase
		text := "GnnModel and TransformerModel and ResNetModel"

		// <= 0 case (defaults to 5)
		res0 := ExtractDiscoverySignals(text, 0)
		is.Len(res0.Signals, 3)

		// > 20 case (caps at 20)
		res21 := ExtractDiscoverySignals(text, 21)
		is.Len(res21.Signals, 3)

		// limit to 1
		res1 := ExtractDiscoverySignals(text, 1)
		is.Len(res1.Signals, 1)
	})

	t.Run("no matches", func(t *testing.T) {
		res := ExtractDiscoverySignals("plain text with no models or ids", 5)
		is.Empty(res.Signals)
		is.Empty(res.Details)
	})

	t.Run("duplicate signals", func(t *testing.T) {
		text := "GnnModel and GnnModel and ArXiv:2101.12345 and arXiv:2101.12345"
		res := ExtractDiscoverySignals(text, 10)
		// Note: ArXiv:2101.12345 might match both model (if ArXiv is camel) and arxiv regex.
		// Our implementation processes model regex first.
		is.Contains(res.Signals, "model:GnnModel")
		is.Contains(res.Signals, "arxiv:2101.12345")
	})

	t.Run("author et al", func(t *testing.T) {
		text := "Lovelace et al. (1843) proposed something. Turing et al reported."
		res := ExtractDiscoverySignals(text, 5)
		is.Len(res.Signals, 2)
		is.Equal("author:Lovelace", res.Signals[0])
		is.Equal("author:Turing", res.Signals[1])
	})
}
