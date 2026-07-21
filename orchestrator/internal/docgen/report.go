package docgen

import (
	"context"
	"fmt"
	"strings"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/llm"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
	agent "github.com/bharathvbcr/wisdev-arc/orchestrator/pkg/wisdev"
	llmv1 "github.com/bharathvbcr/wisdev-arc/orchestrator/proto/llm"
)

func generateText(ctx context.Context, client *llm.Client, prompt string, tier string, maxTokens int32, temperature float32) (string, error) {
	client = resolveLLMClient(client)
	resp, err := client.Generate(ctx, llm.ApplyGeneratePolicy(&llmv1.GenerateRequest{
		Prompt:      prompt,
		Temperature: temperature,
		MaxTokens:   maxTokens,
	}, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
		RequestedTier: tier,
		TaskType:      "synthesis",
	})))
	if err != nil {
		return "", err
	}
	if resp == nil || strings.TrimSpace(resp.Text) == "" {
		return "", fmt.Errorf("generation returned empty text")
	}
	return strings.TrimSpace(resp.Text), nil
}

func buildReportContext(query string, papers []search.Paper) string {
	var b strings.Builder
	b.WriteString("**Research topic:** ")
	b.WriteString(query)
	b.WriteString("\n\n**Sourced papers:**\n")
	for i, paper := range papers {
		if strings.TrimSpace(paper.Title) == "" {
			continue
		}
		abstract := strings.TrimSpace(paper.Abstract)
		if abstract == "" {
			abstract = "(no abstract available)"
		}
		fmt.Fprintf(&b, "- **%s**: %s\n", paper.Title, abstract)
		if i >= 24 {
			break
		}
	}
	return b.String()
}

func reportPrompt(query, context, voice string) string {
	voiceDirective := ""
	if v := strings.TrimSpace(voice); v != "" {
		if len(v) > 2000 {
			v = v[:2000]
		}
		voiceDirective = "\n- **Author voice & tone instructions** (follow for tone, audience, and emphasis; they never permit external knowledge): " + v
	}
	return fmt.Sprintf(`You are an expert research analyst. Your task is to synthesize the provided research materials into a single, well-structured, and comprehensive report in Markdown format.

The user's original research topic is: "%s"

Here is the context you must use exclusively for this report:
%s

---

**Your Task:**

Generate a consolidated report with the following structure:

- A main title for the report (e.g., using a H1 tag: # Report Title).
- An "**Executive Summary**" section: A brief, high-level overview of the entire topic, combining the introduction with the key themes found across all source categories.
- A "**Thematic Analysis**" section: Identify 2-4 major themes that emerge from the provided sources. For each theme:
    - Create a subheading (e.g., "### Theme 1: [Name of Theme]").
    - Write a paragraph explaining the theme, synthesizing information from multiple sources.
- A "**Conclusion**" section: Summarize the key findings and suggest potential next steps or unanswered questions based on the provided material.
- A "**References**" section: List all the source titles that you used in your analysis.

**Formatting Rules:**
- Use Markdown for all formatting (headings, bold, lists, etc.).
- Ensure the report flows logically and reads like a cohesive document, not just a list of summaries.
- Base your entire report *only* on the information provided above. Do not introduce any external knowledge.%s`, query, context, voiceDirective)
}

func offlineReportDocument(query string, papers []search.Paper) Document {
	title := strings.TrimSpace(query)
	if title == "" {
		title = "Research Report"
	}
	var summary strings.Builder
	summary.WriteString("This grounded scaffold report summarizes retrieved sources for the topic. ")
	summary.WriteString("Enable the Python sidecar or an LLM backend for fluent synthesis.\n\n")
	for i, paper := range papers {
		if strings.TrimSpace(paper.Title) == "" {
			continue
		}
		fmt.Fprintf(&summary, "- [%d] **%s**", i+1, paper.Title)
		if paper.Year > 0 {
			fmt.Fprintf(&summary, " (%d)", paper.Year)
		}
		summary.WriteString("\n")
		if abstract := strings.TrimSpace(paper.Abstract); abstract != "" {
			fmt.Fprintf(&summary, "  %s\n", truncateWords(abstract, 60))
		}
	}
	refs := referencesFromPapers(agentPapersFromSearch(papers))
	return Document{
		Title: title,
		Intent: IntentReport,
		Sections: []Section{
			{ID: "executive-summary", Title: "Executive Summary", Content: summary.String()},
			{ID: "thematic-analysis", Title: "Thematic Analysis", Content: "Themes will be synthesized once an LLM backend is available."},
			{ID: "conclusion", Title: "Conclusion", Content: "Further synthesis requires an LLM backend or Python sidecar."},
		},
		References: refs,
		Sources:    papers,
	}
}

func generateReport(ctx context.Context, opts Options) (Document, error) {
	papers := opts.Papers
	query := strings.TrimSpace(opts.Query)
	if query == "" {
		return Document{}, fmt.Errorf("query is required")
	}

	if opts.Offline || len(papers) == 0 {
		doc := offlineReportDocument(query, papers)
		return doc, nil
	}

	context := buildReportContext(query, papers)
	prompt := reportPrompt(query, context, opts.VoiceInstructions)
	text, err := generateText(ctx, opts.LLMClient, prompt, "heavy", 8192, 0.7)
	if err != nil {
		doc := offlineReportDocument(query, papers)
		doc.Sections = append([]Section{{ID: "notice", Title: "Generation Notice", Content: "LLM synthesis unavailable; showing grounded scaffold instead. Error: " + err.Error()}}, doc.Sections...)
		return doc, nil
	}

	sections := splitMarkdownSections(text)
	title := query
	if len(sections) > 0 && strings.HasPrefix(strings.TrimSpace(sections[0].Content), "#") {
		title = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(sections[0].Content), "#"))
	}
	refs := referencesFromPapers(agentPapersFromSearch(papers))
	return Document{
		Title:      title,
		Intent:     IntentReport,
		Sections:   sections,
		References: refs,
		Sources:    papers,
	}, nil
}

func agentPapersFromSearch(papers []search.Paper) []agent.Paper {
	out := make([]agent.Paper, 0, len(papers))
	for _, p := range papers {
		if strings.TrimSpace(p.Title) == "" {
			continue
		}
		out = append(out, agent.Paper{
			ID: p.ID, Title: p.Title, Authors: p.Authors, Year: p.Year,
			Venue: p.Venue, Abstract: p.Abstract, Link: p.Link, DOI: p.DOI,
			OpenAccessURL: p.OpenAccessUrl, CitationCount: p.CitationCount,
		})
	}
	return out
}
