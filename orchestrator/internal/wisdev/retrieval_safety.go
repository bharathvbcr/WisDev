package wisdev

import (
	"log/slog"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/rag"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

func SanitizeRetrievedPapersForLLM(papers []search.Paper, operation string) []search.Paper {
	if len(papers) == 0 {
		return nil
	}
	out := make([]search.Paper, 0, len(papers))
	for _, paper := range papers {
		if safe, reason := IsSafeRetrievedLLMInput(paper.Title, paper.Abstract, paper.FullText, strings.Join(paper.Keywords, " ")); !safe {
			slog.Warn("retrieved paper rejected by prompt-injection scan",
				"component", "wisdev.retrieval_safety",
				"operation", operation,
				"paper_id", paper.ID,
				"reason", reason,
			)
			continue
		}
		out = append(out, paper)
	}
	return out
}

func SanitizeEvidenceItemsForLLM(items []EvidenceItem, operation string) []EvidenceItem {
	if len(items) == 0 {
		return nil
	}
	out := make([]EvidenceItem, 0, len(items))
	for _, item := range items {
		if safe, reason := IsSafeRetrievedLLMInput(item.Claim, item.Snippet, item.PaperTitle); !safe {
			slog.Warn("evidence item rejected by prompt-injection scan",
				"component", "wisdev.retrieval_safety",
				"operation", operation,
				"paper_id", item.PaperID,
				"reason", reason,
			)
			continue
		}
		out = append(out, item)
	}
	return out
}

func IsSafeRetrievedLLMInput(parts ...string) (bool, string) {
	return rag.IsSafeSnippet(strings.Join(parts, "\n"))
}
