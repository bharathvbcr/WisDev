package wisdev

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

func (rt *UnifiedResearchRuntime) attachCitationGraphWorkerArtifact(ctx context.Context, query string, result *researchWorkerExecution) {
	if result == nil || result.Role != ResearchWorkerCitationGraph {
		return
	}
	graph := buildResearchCitationGraph(query, result.Papers)
	if rt != nil && rt.searchReg != nil {
		rt.expandResearchCitationGraph(ctx, graph, result.Papers, 3)
	}
	graph.CoverageLedger = buildCitationGraphCoverageLedger(query, graph)
	result.Artifacts["citationGraph"] = graph
	result.Artifacts["citationGraphNodeCount"] = len(graph.Nodes)
	result.Artifacts["citationGraphEdgeCount"] = len(graph.Edges)
	result.Artifacts["identityConflictCount"] = len(graph.IdentityConflicts)
	result.Ledger = mergeCoverageLedgerEntries(result.Ledger, graph.CoverageLedger)
	result.Notes = append(result.Notes, fmt.Sprintf("citation graph recorded %d node(s), %d edge(s), and %d identity conflict(s)", len(graph.Nodes), len(graph.Edges), len(graph.IdentityConflicts)))
}

func buildResearchCitationGraph(query string, papers []search.Paper) *ResearchCitationGraph {
	forwardQueries := []string{}
	backwardQueries := []string{}
	graph := &ResearchCitationGraph{
		Query:           strings.TrimSpace(query),
		BackwardQueries: nil,
		ForwardQueries:  nil,
	}
	forwardQueries = append(forwardQueries, strings.TrimSpace(query+" citing papers follow-up replication"))
	backwardQueries = append(backwardQueries, strings.TrimSpace(query+" references bibliography foundational work"))
	seenCanonicalTitles := map[string]string{}
	seenNodeIDs := map[string]struct{}{}
	for _, paper := range papers {
		node := citationGraphNodeFromPaper(paper)
		if strings.TrimSpace(node.ID) == "" {
			continue
		}
		if _, exists := seenNodeIDs[node.ID]; !exists {
			seenNodeIDs[node.ID] = struct{}{}
			graph.Nodes = append(graph.Nodes, node)
		}
		canonicalKey := strings.ToLower(strings.TrimSpace(firstNonEmpty(node.CanonicalID, node.ID)))
		titleKey := strings.ToLower(strings.TrimSpace(node.Title))
		if canonicalKey != "" && titleKey != "" {
			if existing, exists := seenCanonicalTitles[canonicalKey]; exists && existing != titleKey {
				graph.IdentityConflicts = appendUniqueString(graph.IdentityConflicts, fmt.Sprintf("canonical source %s resolves to both %q and %q", node.CanonicalID, existing, node.Title))
			} else {
				seenCanonicalTitles[canonicalKey] = titleKey
			}
		}
		forwardQueries = append(forwardQueries, citationPaperForwardQuery(node.ID, paper))
		backwardQueries = append(backwardQueries, citationPaperBackwardQuery(node.ID, paper))
	}
	graph.ForwardQueries = normalizeLoopQueries("", forwardQueries)
	graph.BackwardQueries = normalizeLoopQueries("", backwardQueries)
	graph.DuplicateSourceIDs = duplicateCitationGraphSourceIDs(graph.Nodes)
	return graph
}

func (rt *UnifiedResearchRuntime) expandResearchCitationGraph(ctx context.Context, graph *ResearchCitationGraph, seeds []search.Paper, limit int) {
	if rt == nil || rt.searchReg == nil || graph == nil || limit <= 0 {
		return
	}
	seedLimit := minInt(len(seeds), 3)
	seenNodes := make(map[string]struct{}, len(graph.Nodes)+limit*seedLimit)
	seenEdges := map[string]struct{}{}
	for _, node := range graph.Nodes {
		seenNodes[strings.ToLower(strings.TrimSpace(node.ID))] = struct{}{}
	}
	for _, edge := range graph.Edges {
		seenEdges[citationGraphEdgeSignature(edge)] = struct{}{}
	}
	for idx := 0; idx < seedLimit; idx++ {
		seed := seeds[idx]
		seedID := citationGraphLookupID(seed)
		if seedID == "" {
			continue
		}
		callCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		citing, err := rt.searchReg.GetCitations(callCtx, seedID, limit)
		cancel()
		if err != nil {
			graph.IdentityConflicts = appendUniqueString(graph.IdentityConflicts, "citation graph provider unavailable for "+seedID+": "+err.Error())
		} else {
			targetNodeID := citationGraphLookupID(seed)
			for _, citedBy := range citing {
				sourceNode := citationGraphNodeFromPaper(citedBy)
				if strings.TrimSpace(sourceNode.ID) == "" {
					continue
				}
				nodeKey := strings.ToLower(strings.TrimSpace(sourceNode.ID))
				if _, exists := seenNodes[nodeKey]; !exists {
					seenNodes[nodeKey] = struct{}{}
					graph.Nodes = append(graph.Nodes, sourceNode)
				}
				edge := ResearchCitationGraphEdge{
					SourceID: sourceNode.ID,
					TargetID: targetNodeID,
					Kind:     "forward_citation",
					Context:  "citation provider reported citing paper for seed source",
				}
				if !citationGraphHasEdge(seenEdges, edge) {
					graph.Edges = append(graph.Edges, edge)
					seenEdges[citationGraphEdgeSignature(edge)] = struct{}{}
				}
			}
		}
		callCtx, cancel = context.WithTimeout(ctx, 8*time.Second)
		targetNodeID := citationGraphLookupID(seed)
		referenced := rt.expandCitationGraphBackward(callCtx, targetNodeID, seed, limit)
		cancel()
		if len(referenced) == 0 {
			graph.IdentityConflicts = appendUniqueString(graph.IdentityConflicts, "backward citation lookup returned no matches for "+targetNodeID)
		}
		for _, reference := range referenced {
			nodeKey := strings.ToLower(strings.TrimSpace(reference.ID))
			if _, exists := seenNodes[nodeKey]; !exists {
				seenNodes[nodeKey] = struct{}{}
				graph.Nodes = append(graph.Nodes, reference)
			}
			edge := ResearchCitationGraphEdge{
				SourceID: targetNodeID,
				TargetID: reference.ID,
				Kind:     "backward_reference",
				Context:  "search fallback suggests this paper appears in seed references",
			}
			if citationGraphHasEdge(seenEdges, edge) {
				continue
			}
			graph.Edges = append(graph.Edges, edge)
			seenEdges[citationGraphEdgeSignature(edge)] = struct{}{}
		}
	}
	graph.DuplicateSourceIDs = duplicateCitationGraphSourceIDs(graph.Nodes)
}

func (rt *UnifiedResearchRuntime) expandCitationGraphBackward(ctx context.Context, targetNodeID string, seed search.Paper, limit int) []ResearchCitationGraphNode {
	query := citationPaperBackwardQuery(targetNodeID, seed)
	if query == "" || ctx == nil || rt.searchReg == nil || limit <= 0 {
		return nil
	}
	opts := search.SearchOpts{
		Limit:       limit,
		QualitySort: false,
	}
	papers, _, err := retrieveCanonicalSearchPapers(ctx, rt.searchReg, query, opts)
	if err != nil || len(papers) == 0 {
		return nil
	}
	out := make([]ResearchCitationGraphNode, 0, len(papers))
	for _, paper := range papers {
		node := citationGraphNodeFromPaper(paper)
		if strings.TrimSpace(node.ID) == "" || strings.EqualFold(strings.TrimSpace(node.ID), targetNodeID) {
			continue
		}
		out = append(out, node)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(strings.TrimSpace(out[i].ID)) < strings.ToLower(strings.TrimSpace(out[j].ID))
	})
	return dedupeCitationGraphNodes(out)
}

func citationPaperForwardQuery(nodeID string, paper search.Paper) string {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		nodeID = strings.TrimSpace(firstNonEmpty(paper.DOI, paper.ArxivID, paper.ID, paper.Link))
	}
	if nodeID != "" {
		return strings.TrimSpace(nodeID + " cites")
	}
	if strings.TrimSpace(paper.Title) == "" {
		return ""
	}
	return strings.TrimSpace(paper.Title + " citing papers")
}

func citationPaperBackwardQuery(nodeID string, paper search.Paper) string {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		nodeID = strings.TrimSpace(firstNonEmpty(paper.DOI, paper.ArxivID, paper.ID, paper.Link))
	}
	if nodeID != "" {
		return strings.TrimSpace(nodeID + " references")
	}
	if strings.TrimSpace(paper.Title) == "" {
		return ""
	}
	return strings.TrimSpace(paper.Title + " references")
}

func citationGraphEdgeSignature(edge ResearchCitationGraphEdge) string {
	return strings.ToLower(strings.TrimSpace(edge.SourceID)) + "|" + strings.ToLower(strings.TrimSpace(edge.TargetID)) + "|" + strings.ToLower(strings.TrimSpace(edge.Kind))
}

func citationGraphHasEdge(seen map[string]struct{}, edge ResearchCitationGraphEdge) bool {
	key := citationGraphEdgeSignature(edge)
	if key == "|||" {
		return true
	}
	_, ok := seen[key]
	return ok
}

func dedupeCitationGraphNodes(nodes []ResearchCitationGraphNode) []ResearchCitationGraphNode {
	seen := make(map[string]struct{}, len(nodes))
	out := make([]ResearchCitationGraphNode, 0, len(nodes))
	for _, node := range nodes {
		key := strings.ToLower(strings.TrimSpace(firstNonEmpty(node.CanonicalID, node.ID)))
		if key == "" {
			key = strings.ToLower(strings.TrimSpace(node.ID))
		}
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, node)
	}
	return out
}

func citationGraphNodeFromPaper(paper search.Paper) ResearchCitationGraphNode {
	canonicalID, _ := normalizedSourceIdentity(paper)
	nodeID := strings.TrimSpace(firstNonEmpty(canonicalID, paper.ID, paper.DOI, paper.ArxivID, paper.Link, paper.Title))
	identityFields := make(map[string]any)
	for key, value := range sourceIdentityFields(paper) {
		identityFields[key] = value
	}
	return ResearchCitationGraphNode{
		ID:              nodeID,
		Title:           strings.TrimSpace(paper.Title),
		CanonicalID:     strings.TrimSpace(canonicalID),
		SourceFamily:    sourceFamilyForPaper(paper),
		CitationCount:   paper.CitationCount,
		IdentityFields:  identityFields,
		RetractionCheck: citationRetractionCheckStatus(paper),
	}
}

func citationGraphLookupID(paper search.Paper) string {
	return strings.TrimSpace(firstNonEmpty(paper.DOI, paper.ArxivID, paper.ID, paper.Link, paper.Title))
}

func citationRetractionCheckStatus(paper search.Paper) string {
	text := strings.ToLower(strings.Join([]string{paper.Title, paper.Abstract, paper.Venue}, " "))
	switch {
	case strings.Contains(text, "retracted") || strings.Contains(text, "withdrawn"):
		return "flagged_retraction_or_withdrawal"
	case strings.Contains(text, "correction") || strings.Contains(text, "erratum"):
		return "correction_signal"
	default:
		return "no_retraction_signal"
	}
}

func duplicateCitationGraphSourceIDs(nodes []ResearchCitationGraphNode) []string {
	counts := map[string]int{}
	for _, node := range nodes {
		key := strings.ToLower(strings.TrimSpace(firstNonEmpty(node.CanonicalID, node.ID)))
		if key == "" {
			continue
		}
		counts[key]++
	}
	var out []string
	for key, count := range counts {
		if count > 1 {
			out = append(out, key)
		}
	}
	return dedupeTrimmedStrings(out)
}

func buildCitationGraphCoverageLedger(query string, graph *ResearchCitationGraph) []CoverageLedgerEntry {
	if graph == nil {
		return nil
	}
	entries := make([]CoverageLedgerEntry, 0, 4)
	status := coverageLedgerStatusResolved
	title := "Citation graph attached to blackboard"
	description := fmt.Sprintf("Citation graph contains %d node(s), %d edge(s), %d duplicate source id(s), and %d identity conflict(s).", len(graph.Nodes), len(graph.Edges), len(graph.DuplicateSourceIDs), len(graph.IdentityConflicts))
	confidence := 0.78
	obligationType := "coverage_gap"
	ownerWorker := string(ResearchWorkerCitationGraph)
	severity := "low"
	if len(graph.Nodes) == 0 {
		status = coverageLedgerStatusOpen
		title = "Citation graph has no source nodes"
		description = "No persistent source nodes were available for forward/backward citation traversal."
		confidence = 0.42
		obligationType = "missing_citation_identity"
		severity = "critical"
	} else if len(graph.Edges) == 0 {
		status = coverageLedgerStatusOpen
		title = "Citation graph needs snowball expansion"
		description = "Seed sources were normalized, but no citation edges were recovered from a graph provider."
		confidence = 0.56
		obligationType = "missing_source_diversity"
		severity = "high"
	}
	entries = append(entries, CoverageLedgerEntry{
		ID:                stableWisDevID("citation-graph-ledger", query, title, fmt.Sprintf("%d", len(graph.Nodes)), fmt.Sprintf("%d", len(graph.Edges))),
		Category:          "citation_graph",
		Status:            status,
		Title:             title,
		Description:       description,
		SupportingQueries: dedupeTrimmedStrings(append(append([]string(nil), graph.ForwardQueries...), graph.BackwardQueries...)),
		SourceFamilies:    citationGraphSourceFamilies(graph),
		Confidence:        confidence,
		Required:          true,
		Priority:          92,
		ObligationType:    obligationType,
		OwnerWorker:       ownerWorker,
		Severity:          severity,
	})
	if len(graph.IdentityConflicts) > 0 || len(graph.DuplicateSourceIDs) > 0 {
		entries = append(entries, CoverageLedgerEntry{
			ID:                stableWisDevID("citation-identity-conflict", query, strings.Join(graph.IdentityConflicts, "|"), strings.Join(graph.DuplicateSourceIDs, "|")),
			Category:          "citation_identity_conflict",
			Status:            coverageLedgerStatusOpen,
			Title:             "Citation identity conflicts require reconciliation",
			Description:       strings.TrimSpace(strings.Join(append(append([]string(nil), graph.IdentityConflicts...), graph.DuplicateSourceIDs...), "; ")),
			SupportingQueries: []string{strings.TrimSpace(query) + " DOI arXiv PubMed OpenAlex Semantic Scholar source identity conflict"},
			SourceFamilies:    citationGraphSourceFamilies(graph),
			Confidence:        0.50,
			Required:          true,
			Priority:          98,
			ObligationType:    "missing_citation_identity",
			OwnerWorker:       string(ResearchWorkerCitationGraph),
			Severity:          "critical",
		})
	}
	return entries
}

func citationGraphSourceFamilies(graph *ResearchCitationGraph) []string {
	if graph == nil {
		return nil
	}
	families := make([]string, 0, len(graph.Nodes))
	for _, node := range graph.Nodes {
		families = append(families, node.SourceFamily)
	}
	return dedupeTrimmedStrings(families)
}

func mergeResearchCitationGraphs(primary *ResearchCitationGraph, secondary *ResearchCitationGraph) *ResearchCitationGraph {
	if primary == nil {
		return secondary
	}
	if secondary == nil {
		return primary
	}
	merged := &ResearchCitationGraph{
		Query:              firstNonEmpty(primary.Query, secondary.Query),
		Nodes:              append([]ResearchCitationGraphNode(nil), primary.Nodes...),
		Edges:              append([]ResearchCitationGraphEdge(nil), primary.Edges...),
		BackwardQueries:    normalizeLoopQueries("", append(append([]string(nil), primary.BackwardQueries...), secondary.BackwardQueries...)),
		ForwardQueries:     normalizeLoopQueries("", append(append([]string(nil), primary.ForwardQueries...), secondary.ForwardQueries...)),
		IdentityConflicts:  dedupeTrimmedStrings(append(append([]string(nil), primary.IdentityConflicts...), secondary.IdentityConflicts...)),
		DuplicateSourceIDs: dedupeTrimmedStrings(append(append([]string(nil), primary.DuplicateSourceIDs...), secondary.DuplicateSourceIDs...)),
		CoverageLedger:     mergeCoverageLedgerEntries(primary.CoverageLedger, secondary.CoverageLedger),
	}
	seenNodes := make(map[string]struct{}, len(merged.Nodes)+len(secondary.Nodes))
	outNodes := make([]ResearchCitationGraphNode, 0, len(merged.Nodes)+len(secondary.Nodes))
	for _, node := range append(append([]ResearchCitationGraphNode(nil), primary.Nodes...), secondary.Nodes...) {
		key := strings.ToLower(strings.TrimSpace(firstNonEmpty(node.ID, node.CanonicalID, node.Title)))
		if key == "" {
			continue
		}
		if _, exists := seenNodes[key]; exists {
			continue
		}
		seenNodes[key] = struct{}{}
		outNodes = append(outNodes, node)
	}
	merged.Nodes = outNodes
	seenEdges := make(map[string]struct{}, len(primary.Edges)+len(secondary.Edges))
	outEdges := make([]ResearchCitationGraphEdge, 0, len(primary.Edges)+len(secondary.Edges))
	for _, edge := range append(append([]ResearchCitationGraphEdge(nil), primary.Edges...), secondary.Edges...) {
		key := citationGraphEdgeSignature(edge)
		if key == "|||" {
			continue
		}
		if _, exists := seenEdges[key]; exists {
			continue
		}
		seenEdges[key] = struct{}{}
		outEdges = append(outEdges, edge)
	}
	merged.Edges = outEdges
	return merged
}
