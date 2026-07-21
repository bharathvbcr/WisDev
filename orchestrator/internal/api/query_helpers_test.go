package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClassifyQueryField(t *testing.T) {
	tests := []struct {
		query    string
		expected string
		minScore float64
	}{
		{"clinical trial for cancer", "medicine", 0.6},
		{"genome sequencing of species", "biology", 0.6},
		{"cognitive behavior therapy", "psychology", 0.6},
		{"machine learning algorithms", "computerscience", 0.6},
		{"quantum physics", "physics", 0.6},
		{"unknown topic", "computerscience", 0.5},
		{"", "", 0.0},
	}

	for _, tt := range tests {
		field, score := ClassifyQueryField(tt.query)
		assert.Equal(t, tt.expected, field)
		if tt.query != "" {
			assert.GreaterOrEqual(t, score, tt.minScore)
		}
	}
}

func TestBuildQueryIntroductionMarkdown(t *testing.T) {
	papers := []queryIntroductionPaper{
		{Title: "Paper 1", Year: 2023, Abstract: "A survey of LLMs"},
		{Title: "Paper 2", Year: 2021, Abstract: "Benchmark evaluation"},
	}

	t.Run("Basic", func(t *testing.T) {
		md := BuildQueryIntroductionMarkdown("LLM Research", papers, []string{"arxiv"})
		assert.Contains(t, md, "## What this field studies")
		assert.Contains(t, md, "## Why it matters")
		assert.Contains(t, md, "## Major themes in the evidence base")
		assert.Contains(t, md, "## Open gaps and contested claims")
		assert.Contains(t, md, "## Useful next research directions")
		assert.Contains(t, md, "This field brief organizes 2 papers around LLM Research")
		assert.Contains(t, md, "arxiv")
		assert.Contains(t, md, "Compare methods and benchmarks")
		assert.Contains(t, md, "Stress-test robustness and replication")
		assert.Contains(t, md, "Compare settings and populations")
	})

	t.Run("Empty papers", func(t *testing.T) {
		md := BuildQueryIntroductionMarkdown("Topic", nil, nil)
		assert.Contains(t, md, "This field brief uses Topic as its organizing frame")
		assert.Contains(t, md, "Method diversity across studies")
		assert.NotContains(t, md, "[1]")
	})

	t.Run("Inline citation markers ground themes on papers", func(t *testing.T) {
		cited := []queryIntroductionPaper{
			{Title: "Paper 1", Year: 2023, Abstract: "A survey of LLMs"},
			{Title: "Paper 2", Year: 2021, Abstract: "Benchmark evaluation"},
			{Title: "Paper 3", Year: 2022, Abstract: "Another benchmark evaluation study"},
		}
		md := BuildQueryIntroductionMarkdown("LLM Research", cited, []string{"arxiv"})
		// Theme bullets cite the 1-based indices of the papers that triggered them.
		assert.Contains(t, md, "- Synthesis and review literature [1]")
		assert.Contains(t, md, "- Benchmark design and evaluation quality [2][3]")
		// The overview's first mention of each theme is annotated too.
		assert.Regexp(t, `signals are .*\[1\]`, md)
		assert.Contains(t, md, "Bracketed numbers such as [1] cite the numbered sources grounding this brief.")

		// Meta stays clean of markers so chips and snippets render plain labels.
		meta := BuildQueryIntroductionMeta("LLM Research", cited, []string{"arxiv"})
		assert.NotContains(t, meta.Overview, "[1]")
		for _, theme := range meta.CoreThemes {
			assert.NotContains(t, theme, "[")
		}
	})

	t.Run("Citation markers cap at three papers per theme", func(t *testing.T) {
		many := make([]queryIntroductionPaper, 0, 5)
		for range 5 {
			many = append(many, queryIntroductionPaper{Title: "P", Abstract: "benchmark evaluation"})
		}
		md := BuildQueryIntroductionMarkdown("Benchmarks", many, nil)
		assert.Contains(t, md, "- Benchmark design and evaluation quality [1][2][3]")
		assert.NotContains(t, md, "[1][2][3][4]")
	})

	t.Run("Other methodology signals", func(t *testing.T) {
		p2 := []queryIntroductionPaper{
			{Title: "P1", Abstract: "survey"},
			{Title: "P2", Abstract: "benchmark"},
			{Title: "P3", Abstract: "alignment"},
			{Title: "P4", Abstract: "safety"},
		}
		md := BuildQueryIntroductionMarkdown("Safety", p2, nil)
		assert.Contains(t, md, "## What this field studies")
		assert.Contains(t, md, "## Useful next research directions")
		assert.Contains(t, md, "Search query")
		assert.Contains(t, md, "Compare methods and benchmarks")
	})

	t.Run("Medicine wording is domain specific", func(t *testing.T) {
		clinicalPapers := []queryIntroductionPaper{
			{Title: "Cohort study", Abstract: "Clinical trial outcomes in patient cohorts."},
			{Title: "Follow-up study", Abstract: "Comparator choice and endpoint selection affect clinical results."},
		}
		md := BuildQueryIntroductionMarkdown("clinical trial for cancer", clinicalPapers, []string{"pubmed"})
		assert.Contains(t, md, "Compare clinical methods and trial design")
		assert.Contains(t, md, "patient cohorts")
		assert.Contains(t, md, "Check translational outcomes")
		assert.Contains(t, md, "pubmed")
	})

	t.Run("RLHF adds a primer and keeps the query label in the overview", func(t *testing.T) {
		rlhfPapers := []queryIntroductionPaper{
			{Title: "RLHF Survey", Abstract: "A survey of preference optimization, alignment benchmarks, and multi-turn evaluation."},
			{Title: "Reward Modeling Study", Abstract: "Reward-model training changes downstream instruction following, safety behavior, and reward hacking resistance."},
		}

		meta := BuildQueryIntroductionMeta("RLHF reinforcement learning", rlhfPapers, []string{"openalex"})
		md := BuildQueryIntroductionMarkdown("RLHF reinforcement learning", rlhfPapers, []string{"openalex"})

		assert.Contains(t, meta.Overview, "RLHF (reinforcement learning from human feedback)")
		assert.Contains(t, meta.Overview, "This field brief organizes 2 papers around RLHF Reinforcement Learning")
		assert.NotContains(t, meta.Overview, "computerscience research")
		assert.Contains(t, meta.CoreThemes, "Preference data, reward modeling, and policy optimization")
		assert.Contains(t, meta.CoreThemes, "Alignment behavior, safety, and instruction following")
		assert.Contains(t, meta.CoreThemes, "Reward hacking, robustness, and multi-turn evaluation")
		assert.Contains(t, meta.OpenQuestions[0], "reward-model quality versus policy optimization versus preference-data curation")
		assert.Equal(t, "Compare reward models and preference data", meta.ResearchDirections[0].Title)
		assert.Contains(t, md, "Changes in preference data, reward-model calibration, and evaluation protocol")
		assert.Contains(t, md, "RLHF (reinforcement learning from human feedback)")
		assert.Contains(t, md, "Compare reward models and preference data")
		assert.Contains(t, md, "Compare RLHF with adjacent alignment methods")
		assert.NotContains(t, md, "computerscience research")
	})
}

func TestBuildLocalPaperSummary(t *testing.T) {
	assert.Contains(t, BuildLocalPaperSummary("Title", "", 3), "lightweight metadata summary")

	abstract := "First sentence. Second sentence! Third sentence."
	summary := BuildLocalPaperSummary("Title", abstract, 0)
	assert.Equal(t, "First sentence. Second sentence.", summary)

	assert.Contains(t, BuildLocalPaperSummary("Title", "...", 0), "conclusions")
}

func TestBuildKeyFindings(t *testing.T) {
	abstract := "F1. F2! F3? F4."
	findings := BuildKeyFindings(abstract, 2)
	assert.Len(t, findings, 2)
	assert.Equal(t, "F1", findings[0])
	assert.Equal(t, "F2", findings[1])

	empty := BuildKeyFindings("", 3)
	assert.Len(t, empty, 1)
	assert.Contains(t, empty[0], "Review this source directly")
}
