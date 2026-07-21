package api

import (
	"testing"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/wisdev"

	"github.com/stretchr/testify/assert"
)

// TestExtractDocGenControls covers the /full-paper/start options parsing: knobs may
// arrive under `options` (preferred) or `metadata` (fallback), numbers may be JSON
// floats, and the free-text author instructions accept either alias.
func TestExtractDocGenControls(t *testing.T) {
	options := map[string]any{
		"docGenWords":        float64(2400), // JSON numbers decode to float64
		"docGenMinCitations": 12,
		"docGenGenre":        "research paper",
		"docGenFlow":         []any{"introduction", "results"},
		"docGenReviewRounds": float64(3),
		"docGenInstructions": "Write for policymakers; avoid jargon.",
	}
	metadata := map[string]any{"docGenGenre": "narrative literature review"} // shadowed by options

	controls := extractDocGenControls(options, metadata)
	assert.Equal(t, 2400, controls.targetWords)
	assert.Equal(t, 12, controls.minCitations)
	assert.Equal(t, "research paper", controls.genre, "options must win over metadata")
	assert.Equal(t, []string{"introduction", "results"}, controls.sectionFlow)
	assert.Equal(t, 3, controls.reviewRounds)
	assert.Equal(t, "Write for policymakers; avoid jargon.", controls.customInstructions)
}

// TestExtractDocGenControlsFallbackAndAliases: metadata fills gaps options leaves, and
// customInstructions is accepted as an alias for docGenInstructions.
func TestExtractDocGenControlsFallbackAndAliases(t *testing.T) {
	options := map[string]any{"customInstructions": "Cite broadly but keep it readable."}
	metadata := map[string]any{"docGenWords": 1500}

	controls := extractDocGenControls(options, metadata)
	assert.Equal(t, 1500, controls.targetWords, "metadata fills what options omit")
	assert.Equal(t, "Cite broadly but keep it readable.", controls.customInstructions)
	assert.Zero(t, controls.minCitations)
	assert.Empty(t, controls.genre)
}

// TestWithSmartDocGenDefaults: an unset citation floor gets the auto default (parity
// with the wisdev-arc CLI); an explicit floor is preserved. Other knobs untouched.
func TestWithSmartDocGenDefaults(t *testing.T) {
	defaulted := withSmartDocGenDefaults(docGenControls{})
	assert.Equal(t, smartDocGenMinCitations, defaulted.minCitations)
	assert.Zero(t, defaulted.targetWords, "only the citation floor is defaulted")

	explicit := withSmartDocGenDefaults(docGenControls{minCitations: 25})
	assert.Equal(t, 25, explicit.minCitations)
}

// TestApplyDocGenControls: non-zero fields set the pipeline knobs; empty fields leave
// the pipeline defaults untouched.
func TestApplyDocGenControls(t *testing.T) {
	pipeline := wisdev.NewManuscriptPipelineOffline()
	applyDocGenControls(pipeline, docGenControls{
		targetWords:        1800,
		minCitations:       20,
		genre:              "research paper",
		sectionFlow:        []string{"abstract", "introduction"},
		reviewRounds:       4,
		customInstructions: "  Emphasize reproducibility.  ",
	})
	assert.Equal(t, 1800, pipeline.TargetWords)
	assert.Equal(t, 20, pipeline.MinCitations)
	assert.Equal(t, "research paper", pipeline.Genre)
	assert.Equal(t, []string{"abstract", "introduction"}, pipeline.SectionFlow)
	assert.Equal(t, 4, pipeline.ReviewRounds)
	assert.Equal(t, "Emphasize reproducibility.", pipeline.CustomInstructions, "instructions are trimmed")

	// An empty control set leaves defaults in place.
	fresh := wisdev.NewManuscriptPipelineOffline()
	applyDocGenControls(fresh, docGenControls{})
	assert.Zero(t, fresh.TargetWords)
	assert.Empty(t, fresh.CustomInstructions)
}
