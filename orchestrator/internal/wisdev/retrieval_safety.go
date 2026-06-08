package wisdev

import (
	"log/slog"
	"strings"
	"sync"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/rag"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

var wisdevRetrievalSafetyLogSeen sync.Map

func SanitizeRetrievedPapersForLLM(papers []search.Paper, operation string) []search.Paper {
	if len(papers) == 0 {
		return nil
	}
	out := make([]search.Paper, 0, len(papers))
	for _, paper := range papers {
		normalized, ok := normalizeAndValidateRetrievedPaperForWisDev(paper, operation)
		if !ok {
			continue
		}
		out = append(out, normalized)
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
			logWisDevRetrievalSafetyIssue("evidence item rejected by prompt-injection scan", operation, item.PaperID, reason)
			continue
		}
		out = append(out, item)
	}
	return out
}

func normalizeAndValidateRetrievedPaperForWisDev(paper search.Paper, operation string) (search.Paper, bool) {
	paper.ID = strings.TrimSpace(paper.ID)
	paper.Title = strings.TrimSpace(paper.Title)
	paper.Abstract = strings.TrimSpace(paper.Abstract)
	paper.FullText = strings.TrimSpace(paper.FullText)
	paper.DOI = strings.TrimSpace(paper.DOI)
	paper.ArxivID = strings.TrimSpace(paper.ArxivID)
	paper.Link = strings.TrimSpace(paper.Link)
	paper.Source = strings.TrimSpace(paper.Source)

	if safe, reason := IsSafeRetrievedLLMInput(paper.Title, paper.Abstract, paper.FullText, strings.Join(paper.Keywords, " ")); !safe {
		logWisDevRetrievalSafetyIssue("retrieved paper rejected by prompt-injection scan", operation, paper.ID, reason)
		return search.Paper{}, false
	}

	if paper.Title == "" {
		fallback := retrievedPaperFallbackTitle(paper)
		if fallback == "" {
			logWisDevRetrievalSafetyIssue("retrieved paper rejected because stable source identity is missing", operation, paper.ID, "missing_title_and_identity")
			return search.Paper{}, false
		}
		paper.Title = fallback
		logWisDevRetrievalSafetyIssue("retrieved paper missing title; using stable fallback title", operation, paper.ID, "missing_title")
	}
	return paper, true
}

func retrievedPaperFallbackTitle(paper search.Paper) string {
	identity := stableRetrievedPaperIdentity(paper)
	if identity == "" {
		return ""
	}
	source := firstNonEmptyRetrievedPaperValue(paper.Source, "source")
	return "Untitled " + source + " paper (" + identity + ")"
}

func stableRetrievedPaperIdentity(paper search.Paper) string {
	if value := firstNonEmptyRetrievedPaperValue(paper.DOI, paper.ArxivID, paper.Link); value != "" {
		return value
	}
	id := firstNonEmptyRetrievedPaperValue(paper.ID)
	lowerID := strings.ToLower(id)
	for _, prefix := range []string{"openalex:", "s2:", "arxiv:", "pubmed:", "pmid:", "doi:"} {
		if strings.HasPrefix(lowerID, prefix) {
			return id
		}
	}
	return ""
}

func firstNonEmptyRetrievedPaperValue(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func logWisDevRetrievalSafetyIssue(message string, operation string, paperID string, reason string) {
	key := strings.Join([]string{message, strings.TrimSpace(paperID), strings.TrimSpace(reason)}, "\x00")
	if _, loaded := wisdevRetrievalSafetyLogSeen.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	slog.Warn(message,
		"component", "wisdev.retrieval_safety",
		"operation", operation,
		"paper_id", paperID,
		"reason", reason,
	)
}

func resetWisDevRetrievalSafetyLogForTest() {
	wisdevRetrievalSafetyLogSeen.Range(func(key, _ any) bool {
		wisdevRetrievalSafetyLogSeen.Delete(key)
		return true
	})
}

func IsSafeRetrievedLLMInput(parts ...string) (bool, string) {
	return rag.IsSafeSnippet(strings.Join(parts, "\n"))
}
