package api

import (
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"

	"github.com/stretchr/testify/assert"
)

func TestResearchRouteCrucialHelperCoverage(t *testing.T) {
	t.Run("sessionIDFromAutonomousRequest prefers explicit id", func(t *testing.T) {
		assert.Equal(t, "explicit", sessionIDFromAutonomousRequest(" explicit ", "nested"))
		assert.Equal(t, "nested", sessionIDFromAutonomousRequest(" ", " nested "))
		assert.Equal(t, "", sessionIDFromAutonomousRequest(" ", "\t"))
	})

	t.Run("buildDeepResearchSeedQueries combines query categories and domain", func(t *testing.T) {
		got := buildDeepResearchSeedQueries(" sleep memory ", []string{"clinical", "clinical", " mechanisms "}, "neuroscience")
		assert.Equal(t, []string{
			"sleep memory clinical",
			"sleep memory mechanisms",
			"sleep memory neuroscience",
		}, got)

		domainFallback := buildDeepResearchSeedQueries("neuroscience memory", nil, "neuroscience")
		assert.Equal(t, []string{"neuroscience memory neuroscience"}, domainFallback)
	})

	t.Run("extractAutonomousProgrammaticQueries handles canonical task shapes", func(t *testing.T) {
		fromTypedTasks := extractAutonomousProgrammaticQueries(map[string]any{
			"tasks": []wisdev.ResearchTask{
				{Name: " evidence triangulation "},
				{Name: "evidence triangulation"},
				{Name: ""},
			},
		})
		assert.Equal(t, []string{"evidence triangulation"}, fromTypedTasks)

		fromMapTasks := extractAutonomousProgrammaticQueries(map[string]any{
			"tasks": []map[string]any{
				{"name": " "},
				{"label": " citation trail "},
				{"query": "provider diversity"},
			},
		})
		assert.Equal(t, []string{"citation trail", "provider diversity"}, fromMapTasks)

		fromAnyTasks := extractAutonomousProgrammaticQueries(map[string]any{
			"tasks": []any{
				map[string]any{"query": "replication evidence"},
				"not-a-task",
				map[string]any{"name": "source identity conflicts"},
			},
		})
		assert.Equal(t, []string{"replication evidence", "source identity conflicts"}, fromAnyTasks)

		assert.Nil(t, extractAutonomousProgrammaticQueries(nil))
	})
}
