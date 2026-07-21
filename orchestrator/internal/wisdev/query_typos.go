package wisdev

import (
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/researchquery"
)

func correctCommonResearchTypos(query string) string {
	return researchquery.CorrectTypos(query)
}
