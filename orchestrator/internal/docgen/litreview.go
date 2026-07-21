package docgen

import (
	"context"
	"fmt"
	"strings"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
)

func sourcePapersFromSearch(papers []search.Paper) []sourcePaper {
	out := make([]sourcePaper, 0, len(papers))
	for _, p := range papers {
		if strings.TrimSpace(p.Title) == "" {
			continue
		}
		year := ""
		if p.Year > 0 {
			year = fmt.Sprintf("%d", p.Year)
		}
		out = append(out, sourcePaper{
			Title:    p.Title,
			Authors:  strings.Join(p.Authors, ", "),
			Year:     year,
			Abstract: p.Abstract,
			DOI:      p.DOI,
		})
	}
	return out
}

type sourcePaper struct {
	Title    string
	Authors  string
	Year     string
	Abstract string
	DOI      string
}

func litReviewPrompt(topic string, papers []sourcePaper, style string) string {
	var papersText strings.Builder
	for i, p := range papers {
		fmt.Fprintf(&papersText, "[%d] %s by %s (%s)\nAbstract: %s\n\n", i+1, p.Title, p.Authors, p.Year, p.Abstract)
	}
	styleText := "Write in a formal academic style suitable for a journal submission."
	if strings.EqualFold(style, "accessible") {
		styleText = "Write in an accessible style suitable for a general educated audience."
	}
	return fmt.Sprintf(`You are an expert academic writer. Generate a comprehensive literature review based on the following papers.

%s

Topic: %s

Papers to review:
%s

Structure your review with:
1. Introduction - Context and importance of the topic
2. Thematic Analysis - Group findings by theme, not by paper
3. Synthesis - Identify patterns, agreements, and contradictions
4. Research Gaps - What questions remain unanswered?
5. Conclusion - Summary and future directions

Use in-text citations like [1], [2] referring to the paper numbers above.
Write approximately 800-1200 words.`, styleText, topic, papersText.String())
}

func offlineLitReviewDocument(query string, papers []search.Paper) Document {
	title := strings.TrimSpace(query)
	if title == "" {
		title = "Literature Review"
	}
	var intro strings.Builder
	intro.WriteString("Grounded literature-review scaffold built from retrieved sources. ")
	intro.WriteString("Connect an LLM backend for fluent thematic synthesis.\n\n")
	for i, paper := range papers {
		if strings.TrimSpace(paper.Title) == "" {
			continue
		}
		fmt.Fprintf(&intro, "[%d] **%s**", i+1, paper.Title)
		if len(paper.Authors) > 0 {
			fmt.Fprintf(&intro, " — %s", strings.Join(paper.Authors, ", "))
		}
		if paper.Year > 0 {
			fmt.Fprintf(&intro, " (%d)", paper.Year)
		}
		intro.WriteString("\n")
		if abstract := strings.TrimSpace(paper.Abstract); abstract != "" {
			fmt.Fprintf(&intro, "%s\n\n", truncateWords(abstract, 80))
		}
	}
	refs := referencesFromPapers(agentPapersFromSearch(papers))
	return Document{
		Title:  title,
		Intent: IntentLitReview,
		Sections: []Section{
			{ID: "introduction", Title: "Introduction", Content: intro.String()},
			{ID: "thematic-analysis", Title: "Thematic Analysis", Content: "Themes will be synthesized once an LLM backend is available."},
			{ID: "synthesis", Title: "Synthesis", Content: "Cross-paper synthesis requires an LLM backend."},
			{ID: "research-gaps", Title: "Research Gaps", Content: "Gap analysis requires an LLM backend."},
			{ID: "conclusion", Title: "Conclusion", Content: "Conclusion synthesis requires an LLM backend."},
		},
		References: refs,
		Sources:    papers,
	}
}

func generateLitReview(ctx context.Context, opts Options) (Document, error) {
	query := strings.TrimSpace(opts.Query)
	if query == "" {
		return Document{}, fmt.Errorf("query is required")
	}
	papers := opts.Papers
	if opts.Offline || len(papers) == 0 {
		return offlineLitReviewDocument(query, papers), nil
	}

	srcPapers := sourcePapersFromSearch(papers)
	if len(srcPapers) == 0 {
		return offlineLitReviewDocument(query, papers), nil
	}

	prompt := litReviewPrompt(query, srcPapers, opts.ReviewStyle)
	text, err := generateText(ctx, opts.LLMClient, prompt, "heavy", 4096, 0.7)
	if err != nil {
		doc := offlineLitReviewDocument(query, papers)
		doc.Sections = append([]Section{{ID: "notice", Title: "Generation Notice", Content: "LLM synthesis unavailable; showing grounded scaffold instead. Error: " + err.Error()}}, doc.Sections...)
		return doc, nil
	}

	sections := splitMarkdownSections(text)
	if len(sections) == 0 {
		sections = []Section{{ID: "review", Title: "Literature Review", Content: text}}
	}
	refs := referencesFromPapers(agentPapersFromSearch(papers))
	return Document{
		Title:      query,
		Intent:     IntentLitReview,
		Sections:   sections,
		References: refs,
		Sources:    papers,
	}, nil
}
