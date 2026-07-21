package docgen

import (
	"context"
	"fmt"
	"strings"

	internalwisdev "github.com/bharathvbcr/wisdev-arc/orchestrator/internal/wisdev"
	agent "github.com/bharathvbcr/wisdev-arc/orchestrator/pkg/wisdev"
)

func generateFullPaper(ctx context.Context, opts Options) (Document, internalwisdev.ManuscriptPipelineResult, error) {
	query := strings.TrimSpace(opts.Query)
	if query == "" {
		return Document{}, internalwisdev.ManuscriptPipelineResult{}, fmt.Errorf("query is required")
	}

	pipeline := internalwisdev.NewManuscriptPipeline(opts.PythonURL)
	if opts.Offline && opts.PythonURL == "" {
		pipeline = internalwisdev.NewManuscriptPipelineOffline()
	}
	applyManuscriptControls(pipeline, opts.Manuscript)
	// VoiceInstructions doubles as full-paper CustomInstructions when Manuscript
	// did not set an explicit steering string (MCP instructions → VoiceInstructions).
	if strings.TrimSpace(pipeline.CustomInstructions) == "" {
		if vi := strings.TrimSpace(opts.VoiceInstructions); vi != "" {
			pipeline.CustomInstructions = vi
		}
	}
	if opts.OnStage != nil {
		pipeline.OnStage = opts.OnStage
	}
	jobID := strings.TrimSpace(opts.JobID)
	if jobID == "" {
		jobID = defaultJobID("docgen")
	}

	result, err := pipeline.Run(ctx, jobID, query, opts.Papers)
	if err != nil {
		return Document{}, result, err
	}
	doc := documentFromPipeline(query, opts.Research, result, opts.IncludeUncited)
	return doc, result, nil
}

func applyManuscriptControls(p *internalwisdev.ManuscriptPipeline, c ManuscriptControls) {
	if c.TargetWords > 0 {
		p.TargetWords = c.TargetWords
	}
	if c.MinCitations > 0 {
		p.MinCitations = c.MinCitations
	}
	if len(c.SectionFlow) > 0 {
		p.SectionFlow = c.SectionFlow
	}
	if c.ReviewRounds > 0 {
		p.ReviewRounds = c.ReviewRounds
	}
	if g := strings.TrimSpace(c.Genre); g != "" {
		p.Genre = g
	}
	if ci := strings.TrimSpace(c.CustomInstructions); ci != "" {
		p.CustomInstructions = ci
	}
}

func documentFromPipeline(query string, research *agent.YOLOResult, result internalwisdev.ManuscriptPipelineResult, includeUncited bool) Document {
	refEntries, packetRef := buildReferenceModel(research, result, includeUncited)
	doiRef := doiRefMap(refEntries)

	draftBySection := make(map[string]internalwisdev.SectionDraftArtifact, len(result.SectionDrafts))
	for _, draft := range result.SectionDrafts {
		draftBySection[draft.SectionID] = draft
	}
	order := result.Blueprint.SectionOrder
	if len(order) == 0 {
		for _, draft := range result.SectionDrafts {
			order = append(order, draft.SectionID)
		}
	}

	sections := make([]Section, 0, len(order))
	for _, sectionID := range order {
		draft, ok := draftBySection[sectionID]
		if !ok {
			continue
		}
		content := strings.TrimSpace(remapDOICitations(remapSectionContentCitations(draft.Content, draft.ClaimPacketIDs, packetRef), doiRef))
		sections = append(sections, Section{
			ID: sectionID, Title: firstNonEmpty(draft.Title, sectionID), Content: content,
			CitationIDs: append([]string(nil), draft.ClaimPacketIDs...),
		})
	}

	return Document{
		Title:      strings.TrimSpace(query),
		Intent:     IntentFullPaper,
		Sections:   sections,
		References: referencesFromEntries(refEntries),
		Visuals:    result.VisualArtifacts,
		Critique:   result.CritiqueReport,
		Pipeline:   &result,
	}
}
