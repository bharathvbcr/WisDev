package wisdev

import (
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/researchquery"
)

func correctCommonResearchTypos(query string) string {
	return researchquery.CorrectTypos(query)
}
