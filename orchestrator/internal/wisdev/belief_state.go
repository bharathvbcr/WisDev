package wisdev

import (
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

// BeliefStateManager manages the agent's belief state with provenance tracking
type BeliefStateManager struct {
	state *BeliefState
}

// NewBeliefStateManager creates a new belief state manager
func NewBeliefStateManager() *BeliefStateManager {
	return &BeliefStateManager{
		state: NewBeliefState(),
	}
}

// GetState returns the current belief state
func (bsm *BeliefStateManager) GetState() *BeliefState {
	return bsm.state
}

// CreateBeliefFromHypothesis creates a new belief from a hypothesis and evidence
func (bsm *BeliefStateManager) CreateBeliefFromHypothesis(
	hypothesis *Hypothesis,
	evidence []EvidenceFinding,
	gapID string,
	queryID string,
) *Belief {
	if hypothesis == nil || strings.TrimSpace(hypothesis.Claim) == "" {
		return nil
	}
	beliefID := stableWisDevID("belief", hypothesis.Claim, gapID, queryID)

	// Extract supporting evidence IDs
	supportingIDs := make([]string, 0)
	if hypothesis.Evidence != nil {
		for _, ev := range hypothesis.Evidence {
			if ev != nil && strings.TrimSpace(ev.ID) != "" {
				supportingIDs = append(supportingIDs, ev.ID)
			}
		}
	}

	// Extract contradicting evidence IDs
	contradictingIDs := make([]string, 0)
	if hypothesis.Contradictions != nil {
		for _, ev := range hypothesis.Contradictions {
			if ev != nil && strings.TrimSpace(ev.ID) != "" {
				contradictingIDs = append(contradictingIDs, ev.ID)
			}
		}
	}
	if len(supportingIDs) == 0 && len(contradictingIDs) == 0 {
		for _, ev := range evidence {
			evID := strings.TrimSpace(ev.ID)
			if evID == "" {
				continue
			}
			if ev.Confidence >= 0.5 {
				supportingIDs = append(supportingIDs, evID)
			} else {
				contradictingIDs = append(contradictingIDs, evID)
			}
		}
	}

	// Build provenance chain. Even unsupported hypotheses get an origin entry so
	// downstream graph consumers can trace why the belief exists.
	provenanceChain := make([]ProvenanceEntry, 0, len(supportingIDs)+len(contradictingIDs)+1)
	if gapID != "" || queryID != "" {
		provenanceChain = append(provenanceChain, ProvenanceEntry{
			GapID:       gapID,
			QueryID:     queryID,
			Timestamp:   NowMillis(),
			Description: fmt.Sprintf("Belief seeded from hypothesis evaluation for %q", hypothesis.Claim),
		})
	}
	for _, evID := range supportingIDs {
		provenanceChain = append(provenanceChain, ProvenanceEntry{
			GapID:       gapID,
			QueryID:     queryID,
			EvidenceID:  evID,
			Timestamp:   NowMillis(),
			Description: fmt.Sprintf("Supporting evidence for %q", hypothesis.Claim),
		})
	}
	for _, evID := range contradictingIDs {
		provenanceChain = append(provenanceChain, ProvenanceEntry{
			GapID:       gapID,
			QueryID:     queryID,
			EvidenceID:  evID,
			Timestamp:   NowMillis(),
			Description: fmt.Sprintf("Contradicting evidence for %q", hypothesis.Claim),
		})
	}

	belief := &Belief{
		ID:                    beliefID,
		Claim:                 hypothesis.Claim,
		Confidence:            hypothesis.ConfidenceScore,
		SupportingEvidence:    uniqueTrimmedStrings(supportingIDs),
		ContradictingEvidence: uniqueTrimmedStrings(contradictingIDs),
		ProvenanceChain:       provenanceChain,
		Status:                BeliefStatusActive,
		CreatedAt:             NowMillis(),
		UpdatedAt:             NowMillis(),
	}

	bsm.state.AddBelief(belief)
	slog.Debug("Created belief from hypothesis",
		"beliefID", beliefID,
		"claim", belief.Claim,
		"confidence", belief.Confidence,
		"supportingEvidence", len(supportingIDs),
		"contradictions", len(contradictingIDs))

	return belief
}

// BuildBeliefsFromHypotheses seeds or refreshes beliefs from the current
// hypothesis set and returns the underlying belief state.
func (bsm *BeliefStateManager) BuildBeliefsFromHypotheses(
	hypotheses []*Hypothesis,
	evidence []EvidenceFinding,
	gap *LoopGapState,
	query string,
) *BeliefState {
	if bsm == nil {
		bsm = NewBeliefStateManager()
	}
	for _, hypothesis := range hypotheses {
		if hypothesis == nil || strings.TrimSpace(hypothesis.Claim) == "" {
			continue
		}
		belief := bsm.CreateBeliefFromHypothesis(hypothesis, evidence, gapIDForHypothesis(gap, hypothesis), query)
		if belief == nil {
			continue
		}
		switch {
		case strings.EqualFold(hypothesis.Status, "refuted"):
			bsm.state.RefuteBelief(belief.ID)
		case strings.EqualFold(hypothesis.Status, "revised"):
			bsm.state.UpdateBelief(belief.ID, func(b *Belief) {
				b.Status = BeliefStatusRevised
			})
		}
	}
	return bsm.GetState()
}

// UpdateBeliefWithNewEvidence updates a belief when new evidence is found
func (bsm *BeliefStateManager) UpdateBeliefWithNewEvidence(
	beliefID string,
	newEvidence []EvidenceFinding,
	gapID string,
	queryID string,
) {
	bsm.state.UpdateBelief(beliefID, func(b *Belief) {
		for _, ev := range newEvidence {
			evID := strings.TrimSpace(ev.ID)
			if evID == "" {
				continue
			}

			// Add to supporting or contradicting based on confidence
			if ev.Confidence >= 0.5 {
				if !contains(b.SupportingEvidence, evID) {
					b.SupportingEvidence = append(b.SupportingEvidence, evID)
				}
			} else {
				if !contains(b.ContradictingEvidence, evID) {
					b.ContradictingEvidence = append(b.ContradictingEvidence, evID)
				}
			}

			// Add provenance
			b.ProvenanceChain = append(b.ProvenanceChain, ProvenanceEntry{
				GapID:       gapID,
				QueryID:     queryID,
				EvidenceID:  evID,
				Timestamp:   NowMillis(),
				Description: fmt.Sprintf("New evidence from query %s addressing gap %s", queryID, gapID),
			})
		}

		// Recalculate confidence based on evidence balance
		bsm.recalculateBeliefConfidence(b)
	})

	slog.Debug("Updated belief with new evidence",
		"beliefID", beliefID,
		"newEvidenceCount", len(newEvidence))
}

// RecalculateConfidence recalculates confidence for all active beliefs
func (bsm *BeliefStateManager) RecalculateConfidence(allEvidence map[string]EvidenceFinding) {
	for _, belief := range bsm.state.GetActiveBeliefs() {
		// Gather supporting evidence confidence
		supportSum := 0.0
		supportCount := 0
		for _, evID := range belief.SupportingEvidence {
			if ev, exists := allEvidence[evID]; exists {
				supportSum += ev.Confidence
				supportCount++
			}
		}

		// Gather contradicting evidence confidence
		contradictSum := 0.0
		contradictCount := 0
		for _, evID := range belief.ContradictingEvidence {
			if ev, exists := allEvidence[evID]; exists {
				contradictSum += ev.Confidence
				contradictCount++
			}
		}

		// Compute new confidence
		newConfidence := 0.5 // Neutral baseline
		if supportCount > 0 {
			newConfidence = supportSum / float64(supportCount)
		}

		// Apply contradiction penalty
		if contradictCount > 0 {
			contradictPenalty := (contradictSum / float64(contradictCount)) * 0.3
			newConfidence -= contradictPenalty
		}

		// Clamp
		if newConfidence < 0 {
			newConfidence = 0
		}
		if newConfidence > 1 {
			newConfidence = 1
		}

		if newConfidence != belief.Confidence {
			bsm.state.UpdateBelief(belief.ID, func(b *Belief) {
				b.Confidence = newConfidence
			})
			slog.Debug("Recalculated belief confidence",
				"beliefID", belief.ID,
				"oldConfidence", belief.Confidence,
				"newConfidence", newConfidence)
		}
	}
}

func gapIDForHypothesis(gap *LoopGapState, hypothesis *Hypothesis) string {
	if gap == nil || hypothesis == nil {
		return ""
	}
	query := strings.ToLower(strings.TrimSpace(hypothesis.Query))
	for _, entry := range gap.Ledger {
		if !strings.EqualFold(strings.TrimSpace(entry.Status), coverageLedgerStatusOpen) {
			continue
		}
		if len(entry.SupportingQueries) == 0 {
			continue
		}
		for _, supportingQuery := range entry.SupportingQueries {
			if strings.EqualFold(strings.TrimSpace(supportingQuery), strings.TrimSpace(hypothesis.Query)) {
				return strings.TrimSpace(entry.ID)
			}
			if strings.Contains(strings.ToLower(strings.TrimSpace(supportingQuery)), query) ||
				strings.Contains(query, strings.ToLower(strings.TrimSpace(supportingQuery))) {
				return strings.TrimSpace(entry.ID)
			}
		}
	}
	if len(gap.Ledger) > 0 {
		return strings.TrimSpace(gap.Ledger[0].ID)
	}
	return ""
}

// recalculateBeliefConfidence recalculates confidence for a single belief (internal helper)
func (bsm *BeliefStateManager) recalculateBeliefConfidence(b *Belief) {
	totalEvidence := len(b.SupportingEvidence) + len(b.ContradictingEvidence)
	if totalEvidence == 0 {
		b.Confidence = 0.5
		return
	}

	prior := ClampFloat(b.Confidence, 0.05, 0.95)
	if prior == 0 {
		prior = 0.5
	}
	b.Confidence = bayesianPosterior(prior, len(b.SupportingEvidence), len(b.ContradictingEvidence), 0.72, 0.72)
}

// TriangulateBeliefs analyzes active beliefs to identify cross-source triangulation
func (bsm *BeliefStateManager) TriangulateBeliefs(allPapers []search.Paper) {
	paperByID := make(map[string]search.Paper, len(allPapers))
	for _, paper := range allPapers {
		paperByID[paper.ID] = paper
	}

	for _, belief := range bsm.state.GetActiveBeliefs() {
		sourceFamilies := make(map[string]struct{})
		for _, evID := range belief.SupportingEvidence {
			// Extract source ID from evidence ID (assuming format "finding:sourceID:...")
			parts := strings.Split(evID, ":")
			sourceID := evID
			if len(parts) >= 2 {
				sourceID = parts[1]
			}

			if paper, exists := paperByID[sourceID]; exists {
				family := resolveSourceFamily(paper)
				if family != "" {
					sourceFamilies[family] = struct{}{}
				}
			}
		}

		// Update belief source families
		families := make([]string, 0, len(sourceFamilies))
		for family := range sourceFamilies {
			families = append(families, family)
		}
		sort.Strings(families)

		bsm.state.UpdateBelief(belief.ID, func(b *Belief) {
			b.SourceFamilies = families
			b.Triangulated = len(families) >= 2 // R4: Promoted if supported by >= 2 families
			if b.Triangulated {
				// Triangulation boost to confidence
				b.Confidence = ClampFloat(b.Confidence+0.15, 0, 1)
			}
		})
	}
}

func resolveSourceFamily(paper search.Paper) string {
	source := strings.ToLower(strings.TrimSpace(paper.Source))
	switch {
	case strings.Contains(source, "arxiv"):
		return "arxiv"
	case strings.Contains(source, "pubmed") || strings.Contains(source, "pmc"):
		return "medicine"
	case strings.Contains(source, "semantic") || strings.Contains(source, "openalex"):
		return "academic_graph"
	case strings.Contains(source, "google"):
		return "general_scholar"
	case strings.Contains(source, "ieee") || strings.Contains(source, "acm"):
		return "computer_science"
	default:
		return source
	}
}

// RefuteBeliefsContradictedByEvidence marks beliefs as refuted if new evidence contradicts them
func (bsm *BeliefStateManager) RefuteBeliefsContradictedByEvidence(
	newEvidence []EvidenceFinding,
	contradictionThreshold float64,
) []string {
	if contradictionThreshold <= 0 {
		contradictionThreshold = 0.7 // Default: strong contradiction
	}

	refutedBeliefsIDs := make([]string, 0)

	for _, belief := range bsm.state.GetActiveBeliefs() {
		contradictionScore := 0.0
		contradictionCount := 0

		for _, ev := range newEvidence {
			// Check if evidence contradicts this belief
			if ev.Confidence < 0.3 { // Low confidence = potential contradiction
				contradictionScore += (0.3 - ev.Confidence)
				contradictionCount++
			}
		}

		if contradictionCount > 0 {
			avgContradiction := contradictionScore / float64(contradictionCount)
			if avgContradiction >= contradictionThreshold {
				bsm.state.RefuteBelief(belief.ID)
				refutedBeliefsIDs = append(refutedBeliefsIDs, belief.ID)
				slog.Info("Refuted belief due to contradicting evidence",
					"beliefID", belief.ID,
					"claim", belief.Claim,
					"contradictionScore", avgContradiction)
			}
		}
	}

	return refutedBeliefsIDs
}

// GetAverageConfidence returns the mean confidence across all active beliefs.
// Returns 0 if there are no active beliefs.
func (bsm *BeliefStateManager) GetAverageConfidence() float64 {
	if bsm == nil {
		return 0
	}
	active := bsm.state.GetActiveBeliefs()
	if len(active) == 0 {
		return 0
	}
	sum := 0.0
	for _, b := range active {
		sum += b.Confidence
	}
	return sum / float64(len(active))
}

// GetContradictionPressure returns the fraction of active beliefs that have
// at least one piece of contradicting evidence. Range [0, 1].
func (bsm *BeliefStateManager) GetContradictionPressure() float64 {
	if bsm == nil {
		return 0
	}
	active := bsm.state.GetActiveBeliefs()
	if len(active) == 0 {
		return 0
	}
	contradicted := 0
	for _, b := range active {
		if len(b.ContradictingEvidence) > 0 {
			contradicted++
		}
	}
	return float64(contradicted) / float64(len(active))
}

func (bsm *BeliefStateManager) GetUncertainBeliefs(threshold float64) []*Belief {
	if bsm == nil {
		return nil
	}
	var uncertain []*Belief
	for _, b := range bsm.state.GetActiveBeliefs() {
		if b.Confidence < threshold {
			uncertain = append(uncertain, b)
		}
	}
	return uncertain
}

// GetProvenanceChainForBelief retrieves the full provenance chain for a belief
func (bsm *BeliefStateManager) GetProvenanceChainForBelief(beliefID string) []ProvenanceEntry {
	if belief, exists := bsm.state.Beliefs[beliefID]; exists {
		return belief.ProvenanceChain
	}
	return nil
}

// GetMostProductiveGaps analyzes provenance chains to identify which gaps led to the most discoveries
func (bsm *BeliefStateManager) GetMostProductiveGaps() map[string]int {
	gapProductivity := make(map[string]int)

	for _, belief := range bsm.state.GetActiveBeliefs() {
		for _, prov := range belief.ProvenanceChain {
			if prov.GapID != "" {
				gapProductivity[prov.GapID]++
			}
		}
	}

	return gapProductivity
}

// ExportBeliefSummary creates a human-readable summary of the belief state
func (bsm *BeliefStateManager) ExportBeliefSummary() string {
	activeBeliefs := bsm.state.GetActiveBeliefs()
	if len(activeBeliefs) == 0 {
		return "No active beliefs."
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Active Beliefs: %d\n\n", len(activeBeliefs)))

	for idx, belief := range activeBeliefs {
		sb.WriteString(fmt.Sprintf("%d. [Confidence: %.2f] %s\n", idx+1, belief.Confidence, belief.Claim))
		sb.WriteString(fmt.Sprintf("   Supporting Evidence: %d, Contradicting: %d\n",
			len(belief.SupportingEvidence), len(belief.ContradictingEvidence)))
		sb.WriteString(fmt.Sprintf("   Provenance Chain: %d entries\n", len(belief.ProvenanceChain)))
		sb.WriteString("\n")
	}

	return sb.String()
}

// MergeBeliefStateIntoReasoningGraph augments the live reasoning graph with
// belief nodes and provenance edges.
func MergeBeliefStateIntoReasoningGraph(graph *ReasoningGraph, beliefs *BeliefState) *ReasoningGraph {
	if graph == nil || beliefs == nil || len(beliefs.Beliefs) == 0 {
		return graph
	}
	EnsureSessionArchitectureState(&AgentSession{ReasoningGraph: graph})
	if graph.NodesMap == nil {
		indexReasoningGraph(graph)
	}
	nodeSeen := map[string]struct{}{}
	for _, node := range graph.Nodes {
		nodeSeen[node.ID] = struct{}{}
	}
	edgeSeen := map[string]struct{}{}
	for _, edge := range graph.Edges {
		edgeSeen[edge.From+"|"+edge.To+"|"+edge.Label] = struct{}{}
	}
	appendNode := func(node ReasoningNode) {
		if strings.TrimSpace(node.ID) == "" || strings.TrimSpace(node.Label) == "" {
			return
		}
		if _, exists := nodeSeen[node.ID]; exists {
			return
		}
		nodeSeen[node.ID] = struct{}{}
		graph.Nodes = append(graph.Nodes, node)
	}
	appendEdge := func(edge ReasoningEdge) {
		if strings.TrimSpace(edge.From) == "" || strings.TrimSpace(edge.To) == "" {
			return
		}
		key := edge.From + "|" + edge.To + "|" + edge.Label
		if _, exists := edgeSeen[key]; exists {
			return
		}
		edgeSeen[key] = struct{}{}
		graph.Edges = append(graph.Edges, edge)
	}
	beliefList := make([]*Belief, 0, len(beliefs.Beliefs))
	for _, belief := range beliefs.Beliefs {
		if belief != nil && strings.TrimSpace(belief.Claim) != "" {
			beliefList = append(beliefList, belief)
		}
	}
	sort.SliceStable(beliefList, func(i, j int) bool {
		if beliefList[i].Confidence == beliefList[j].Confidence {
			return strings.ToLower(beliefList[i].Claim) < strings.ToLower(beliefList[j].Claim)
		}
		return beliefList[i].Confidence > beliefList[j].Confidence
	})
	for _, belief := range beliefList {
		nodeID := strings.TrimSpace(firstNonEmpty(belief.ID, stableWisDevID("belief", belief.Claim)))
		appendNode(ReasoningNode{
			ID:         nodeID,
			Text:       belief.Claim,
			Type:       ReasoningNodeClaim,
			Label:      belief.Claim,
			Depth:      2,
			Confidence: ClampFloat(belief.Confidence, 0, 1),
			Metadata: map[string]any{
				"beliefStatus":               belief.Status,
				"supportingEvidence":         append([]string(nil), belief.SupportingEvidence...),
				"contradictingEvidence":      append([]string(nil), belief.ContradictingEvidence...),
				"provenanceChain":            append([]ProvenanceEntry(nil), belief.ProvenanceChain...),
				"revisedFromBeliefId":        belief.RevisedFromBeliefID,
				"supersededByBeliefId":       belief.SupersededByBeliefID,
				"provenanceEntryCount":       len(belief.ProvenanceChain),
				"supportingEvidenceCount":    len(belief.SupportingEvidence),
				"contradictingEvidenceCount": len(belief.ContradictingEvidence),
				"triangulated":               belief.Triangulated,
				"sourceFamilies":             append([]string(nil), belief.SourceFamilies...),
			},
		})

		// Add provenance causal links
		for _, prov := range belief.ProvenanceChain {
			if strings.TrimSpace(prov.GapID) != "" {
				gapNodeID := "gap:" + strings.TrimSpace(prov.GapID)
				appendNode(ReasoningNode{
					ID:    gapNodeID,
					Type:  ReasoningNodeGap,
					Label: "Gap: " + strings.ReplaceAll(prov.GapID, "_", " "),
					Depth: 1,
				})
				appendEdge(ReasoningEdge{From: gapNodeID, To: nodeID, Label: "derived_from_gap"})

				// Link question to gap if it's a root-level gap
				if graph.Root != "" {
					appendEdge(ReasoningEdge{From: graph.Root, To: gapNodeID, Label: "uncovered_gap"})
				}
			}
		}

		edgeLabel := "supported_by"
		switch belief.Status {
		case BeliefStatusRevised:
			edgeLabel = "refined_to"
		case BeliefStatusRefuted:
			edgeLabel = "refuted_by"
		}
		if graph.Root != "" {
			appendEdge(ReasoningEdge{From: graph.Root, To: nodeID, Label: edgeLabel})
		}
		if strings.TrimSpace(belief.RevisedFromBeliefID) != "" {
			appendEdge(ReasoningEdge{From: belief.RevisedFromBeliefID, To: nodeID, Label: "superseded_by"})
		}
	}
	return indexReasoningGraph(graph)
}

// contains checks if a slice contains a string
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// RecalibrateEvidenceConfidence updates evidence confidence with a bounded
// Bayesian-style posterior from the current belief ledger.
func (bsm *BeliefStateManager) RecalibrateEvidenceConfidence(evidence []EvidenceFinding) []EvidenceFinding {
	if bsm == nil || bsm.state == nil || len(bsm.state.Beliefs) == 0 {
		return evidence
	}

	for i, ev := range evidence {
		if strings.TrimSpace(ev.ID) == "" {
			continue
		}
		posteriors := make([]float64, 0, 2)
		for _, belief := range bsm.state.Beliefs {
			if contains(belief.SupportingEvidence, ev.ID) {
				posteriors = append(posteriors, bayesianPosterior(
					ClampFloat(ev.Confidence, 0.05, 0.95),
					maxInt(1, len(belief.SupportingEvidence)),
					len(belief.ContradictingEvidence),
					ClampFloat(belief.Confidence, 0.55, 0.95),
					0.68,
				))
			}
			if contains(belief.ContradictingEvidence, ev.ID) {
				posteriors = append(posteriors, bayesianPosterior(
					ClampFloat(ev.Confidence, 0.05, 0.95),
					len(belief.SupportingEvidence),
					maxInt(1, len(belief.ContradictingEvidence)),
					0.62,
					ClampFloat(belief.Confidence, 0.55, 0.95),
				))
			}
		}
		if len(posteriors) > 0 {
			total := 0.0
			for _, posterior := range posteriors {
				total += posterior
			}
			evidence[i].Confidence = ClampFloat(total/float64(len(posteriors)), 0.05, 0.95)
		}
	}
	return evidence
}

func bayesianPosterior(prior float64, supportCount int, contradictCount int, supportLikelihood float64, contradictionLikelihood float64) float64 {
	prior = ClampFloat(prior, 0.05, 0.95)
	supportLikelihood = ClampFloat(supportLikelihood, 0.51, 0.95)
	contradictionLikelihood = ClampFloat(contradictionLikelihood, 0.51, 0.95)

	logOdds := math.Log(prior / (1 - prior))
	supportLR := math.Log(supportLikelihood / (1 - supportLikelihood))
	contradictionLR := math.Log(contradictionLikelihood / (1 - contradictionLikelihood))
	logOdds += float64(maxInt(supportCount, 0)) * supportLR
	logOdds -= float64(maxInt(contradictCount, 0)) * contradictionLR

	if logOdds > 20 {
		return 0.95
	}
	if logOdds < -20 {
		return 0.05
	}
	posterior := 1 / (1 + math.Exp(-logOdds))
	return ClampFloat(posterior, 0.05, 0.95)
}

type SaturationResult struct {
	IsSaturated     bool
	SaturationRatio float64
	DiversityScore  float64
	InformationGain float64
	Recommendation  string
}

func (bsm *BeliefStateManager) DetectEvidenceSaturation(newEvidence []EvidenceFinding) SaturationResult {
	if bsm == nil || bsm.state == nil || len(bsm.state.Beliefs) == 0 {
		return SaturationResult{Recommendation: "continue"}
	}

	priorEvidence := make(map[string]struct{})
	priorSources := make(map[string]struct{})
	for _, belief := range bsm.state.Beliefs {
		for _, id := range belief.SupportingEvidence {
			if trimmed := strings.TrimSpace(id); trimmed != "" {
				priorEvidence[trimmed] = struct{}{}
			}
		}
		for _, id := range belief.ContradictingEvidence {
			if trimmed := strings.TrimSpace(id); trimmed != "" {
				priorEvidence[trimmed] = struct{}{}
			}
		}
	}

	var clusters [][]EvidenceFinding
	totalNearestSimilarity := 0.0
	novelEvidence := 0
	novelSources := 0
	seenNewSources := make(map[string]struct{})
	for _, ev := range newEvidence {
		if strings.TrimSpace(ev.ID) != "" {
			if _, exists := priorEvidence[strings.TrimSpace(ev.ID)]; !exists {
				novelEvidence++
			}
		}
		if sourceID := strings.TrimSpace(ev.SourceID); sourceID != "" {
			if _, exists := priorSources[sourceID]; !exists {
				if _, seen := seenNewSources[sourceID]; !seen {
					novelSources++
					seenNewSources[sourceID] = struct{}{}
				}
			}
		}
		evTokens := evidenceFindingTokenSet(ev)
		placed := false
		nearest := 0.0
		for i, cluster := range clusters {
			clusterTokens := evidenceFindingTokenSet(cluster[0])
			similarity := tokenSetJaccard(evTokens, clusterTokens)
			if similarity > nearest {
				nearest = similarity
			}
			if similarity >= 0.62 {
				clusters[i] = append(clusters[i], ev)
				placed = true
				break
			}
		}
		totalNearestSimilarity += nearest
		if !placed {
			clusters = append(clusters, []EvidenceFinding{ev})
		}
	}

	diversityScore := 1.0
	if len(newEvidence) > 0 {
		diversityScore = float64(len(clusters)) / float64(len(newEvidence))
	}
	avgNearestSimilarity := 0.0
	informationGain := 0.0
	if len(newEvidence) > 0 {
		avgNearestSimilarity = totalNearestSimilarity / float64(len(newEvidence))
		informationGain = ClampFloat((float64(novelEvidence)+float64(novelSources))/(2*float64(len(newEvidence))), 0, 1)
	}

	isSaturated := len(newEvidence) >= 5 && avgNearestSimilarity >= 0.72 && informationGain <= 0.2
	saturationRatio := ClampFloat(avgNearestSimilarity*(1-informationGain), 0, 1)

	rec := "continue"
	if isSaturated {
		rec = "reasoning-only"
	} else if diversityScore < 0.35 || informationGain < 0.25 {
		rec = "expand-diversity"
	}

	return SaturationResult{
		IsSaturated:     isSaturated,
		SaturationRatio: saturationRatio,
		DiversityScore:  diversityScore,
		InformationGain: informationGain,
		Recommendation:  rec,
	}
}

func evidenceFindingTokenSet(ev EvidenceFinding) map[string]struct{} {
	return loopEvidenceTokenSet(ev.Claim, ev.Snippet, ev.PaperTitle, ev.SourceID)
}

func tokenSetJaccard(a map[string]struct{}, b map[string]struct{}) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	intersection := 0
	for token := range a {
		if _, exists := b[token]; exists {
			intersection++
		}
	}
	union := len(a) + len(b) - intersection
	if union <= 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func (bsm *BeliefStateManager) EvidenceForBelief(beliefID string, allEvidence []EvidenceFinding) []EvidenceFinding {
	if bsm == nil || bsm.state == nil {
		return nil
	}
	b, exists := bsm.state.Beliefs[beliefID]
	if !exists {
		return nil
	}
	var res []EvidenceFinding
	for _, ev := range allEvidence {
		if contains(b.SupportingEvidence, ev.ID) {
			res = append(res, ev)
		}
	}
	return res
}
