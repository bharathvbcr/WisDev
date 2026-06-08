package wisdev

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

// trimStrings filters a slice of strings, keeping only the first n non-empty
// trimmed entries. Used when constructing node source-ID lists.
func trimStrings(ss []string, n int) []string {
	out := make([]string, 0, n)
	for _, s := range ss {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
			if len(out) >= n {
				break
			}
		}
	}
	return out
}

func uniqueTrimmedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func stableWisDevID(prefix string, parts ...string) string {
	normalized := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			normalized = append(normalized, trimmed)
		}
	}
	if len(normalized) == 0 {
		return prefix
	}
	sum := sha256.Sum256([]byte(strings.Join(normalized, "|")))
	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(sum[:6]))
}

func hypothesisNodeID(hypothesis Hypothesis) string {
	return stableWisDevID("hypothesis", hypothesis.Claim, hypothesis.FalsifiabilityCondition)
}

func evidenceLabel(item EvidenceFinding) string {
	for _, candidate := range []string{item.Claim, item.PaperTitle, item.Snippet} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func evidenceNodeID(item EvidenceFinding) string {
	return stableWisDevID("evidence", item.SourceID, item.PaperTitle, evidenceLabel(item), item.Snippet, item.ID)
}

func NormalizeWisDevMode(raw string) WisDevMode {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(WisDevModeYOLO), "autonomous":
		return WisDevModeYOLO
	default:
		return WisDevModeGuided
	}
}

func ResolveServiceTier(mode WisDevMode, interactive bool) ServiceTier {
	if interactive {
		return ServiceTierPriority
	}
	if mode == WisDevModeYOLO {
		return ServiceTierFlex
	}
	return ServiceTierPriority
}

func EnsureSessionArchitectureState(session *AgentSession) {
	if session == nil {
		return
	}
	if session.Mode == "" {
		session.Mode = WisDevModeGuided
	}
	if session.ServiceTier == "" {
		session.ServiceTier = ResolveServiceTier(session.Mode, session.Status == SessionQuestioning || session.Status == SessionPaused)
	}
	if session.MemoryTiers == nil {
		session.MemoryTiers = &MemoryTierState{}
	}
	if session.ReasoningGraph == nil {
		session.ReasoningGraph = &ReasoningGraph{Query: resolveSessionQuery(session)}
	}
	EnsureSessionModeManifest(session)
}

func appendUniqueMemoryEntry(entries []MemoryEntry, entry MemoryEntry) []MemoryEntry {
	for _, existing := range entries {
		if existing.ID == entry.ID {
			return entries
		}
	}
	return append(entries, entry)
}

func existingMemoryEntryCreatedAt(tiers *MemoryTierState, entryID string) int64 {
	if tiers == nil || strings.TrimSpace(entryID) == "" {
		return 0
	}

	for _, entries := range [][]MemoryEntry{
		tiers.ShortTermWorking,
		tiers.LongTermVector,
		tiers.UserPersonalized,
		tiers.ArtifactMemory,
	} {
		for _, entry := range entries {
			if entry.ID == entryID && entry.CreatedAt > 0 {
				return entry.CreatedAt
			}
		}
	}
	return 0
}

func memoryEntryTimestamp(tiers *MemoryTierState, entryID string, fallback int64) int64 {
	if existing := existingMemoryEntryCreatedAt(tiers, entryID); existing > 0 {
		return existing
	}
	return fallback
}

func specialistVerificationStatus(item EvidenceFinding) string {
	if len(item.SpecialistNotes) == 0 && item.Specialist.Type == "" {
		return "not-reviewed"
	}
	score := item.Specialist.Verification
	if len(item.SpecialistNotes) > 0 {
		score = 0
		for _, note := range item.SpecialistNotes {
			score += note.Verification
		}
	}
	if score > 0 {
		return "specialist-verified"
	}
	if score < 0 {
		return "specialist-flagged"
	}
	return "specialist-neutral"
}

func UpdateSessionMemoryTiers(session *AgentSession, hypotheses []Hypothesis, evidence []EvidenceFinding) {
	EnsureSessionArchitectureState(session)
	if session == nil || session.MemoryTiers == nil {
		return
	}

	existingTiers := &MemoryTierState{
		ShortTermWorking: append([]MemoryEntry(nil), session.MemoryTiers.ShortTermWorking...),
		LongTermVector:   append([]MemoryEntry(nil), session.MemoryTiers.LongTermVector...),
		UserPersonalized: append([]MemoryEntry(nil), session.MemoryTiers.UserPersonalized...),
		ArtifactMemory:   append([]MemoryEntry(nil), session.MemoryTiers.ArtifactMemory...),
	}
	now := NowMillis()
	scoreByID := make(map[string]float64, len(hypotheses))
	scoreByClaim := make(map[string]float64, len(hypotheses))

	// Cap ShortTermWorking memory to recent contextual items
	const maxWorkingMemory = 15
	working := make([]MemoryEntry, 0, min(len(hypotheses), maxWorkingMemory))

	for _, hypothesis := range hypotheses {
		claim := strings.TrimSpace(hypothesis.Claim)
		if claim == "" {
			continue
		}
		entryID := stableWisDevID("hyp", session.SessionID, claim, hypothesis.FalsifiabilityCondition)
		baseScore := ClampFloat(firstNonEmptyFloat(hypothesis.ConfidenceScore, hypothesis.ConfidenceThreshold, 0), 0, 1)
		if hypothesis.ConfidenceScore == 0 {
			var totalEvidenceConf float64
			var evidenceCount int
			for _, ev := range hypothesis.Evidence {
				if ev != nil && ev.Confidence > 0 {
					totalEvidenceConf += ev.Confidence
					evidenceCount++
				}
			}
			if evidenceCount > 0 {
				avgEvConf := totalEvidenceConf / float64(evidenceCount)
				if avgEvConf > baseScore {
					baseScore = avgEvConf
				}
			}
			if baseScore < 0.1 {
				baseScore = 0.1
			}
		}

		entry := MemoryEntry{
			ID:              entryID,
			Type:            "hypothesis",
			Content:         claim,
			CreatedAt:       memoryEntryTimestamp(existingTiers, entryID, now),
			EvaluationScore: baseScore,
		}
		scoreByID[entryID] = entry.EvaluationScore
		scoreByClaim[strings.ToLower(claim)] = entry.EvaluationScore

		// Short-term working memory is the active hypothesis scratchpad only.
		// Evidence is retained separately in artifact/long-term tiers so the
		// immediate working set does not bloat across repeated graph updates.
		if !hypothesis.IsTerminated {
			working = appendUniqueMemoryEntry(working, entry)
		}
	}

	for _, item := range evidence {
		label := evidenceLabel(item)
		if label == "" {
			continue
		}
		entryID := stableWisDevID("ev", item.SourceID, item.PaperTitle, label, item.Snippet, item.ID)
		entry := MemoryEntry{
			ID:              entryID,
			Type:            "evidence",
			Content:         label,
			CreatedAt:       memoryEntryTimestamp(existingTiers, entryID, now),
			EvaluationScore: item.Confidence, // R4: Evidence value is its confidence
		}

		// Artifact Store: all found evidence
		session.MemoryTiers.ArtifactMemory = appendUniqueMemoryEntry(session.MemoryTiers.ArtifactMemory, entry)

		// Long-term: Verified evidence
		if item.Confidence >= 0.55 || session.Mode == WisDevModeYOLO {
			session.MemoryTiers.LongTermVector = appendUniqueMemoryEntry(session.MemoryTiers.LongTermVector, entry)
		}
	}

	// R4: Value-based memory pruning - sort by evaluation score, keep top-k
	if len(working) > maxWorkingMemory {
		for i := range working {
			if working[i].EvaluationScore != 0 {
				continue
			}
			if score, ok := scoreByID[working[i].ID]; ok {
				working[i].EvaluationScore = score
				continue
			}
			if score, ok := scoreByClaim[strings.ToLower(strings.TrimSpace(working[i].Content))]; ok {
				working[i].EvaluationScore = score
			}
		}
		// Sort by evaluation score descending, breaking ties by recency
		sortMemoryByValue(working)

		// Keep top maxWorkingMemory entries
		working = working[:maxWorkingMemory]
	}
	session.MemoryTiers.ShortTermWorking = working

	if strings.TrimSpace(session.UserID) != "" {
		entryID := "user_preferences"
		session.MemoryTiers.UserPersonalized = appendUniqueMemoryEntry(session.MemoryTiers.UserPersonalized, MemoryEntry{
			ID:        entryID,
			Type:      "user",
			Content:   "Preferred mode=" + string(session.Mode),
			CreatedAt: memoryEntryTimestamp(existingTiers, entryID, now),
		})
	}
}

func BuildReasoningGraph(query string, hypotheses []Hypothesis, evidence []EvidenceFinding) *ReasoningGraph {
	graph := &ReasoningGraph{
		Query: strings.TrimSpace(query),
		Nodes: []ReasoningNode{},
		Edges: []ReasoningEdge{},
		Root:  "",
	}
	if graph.Query != "" {
		graph.Root = "question_root"
		graph.Nodes = append(graph.Nodes, ReasoningNode{
			ID:    "question_root",
			Type:  ReasoningNodeQuestion,
			Label: graph.Query,
		})
	}
	nodeSeen := map[string]struct{}{}
	edgeSeen := map[string]struct{}{}
	if graph.Query != "" {
		nodeSeen["question_root"] = struct{}{}
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
	type hypothesisSupport struct {
		SourceIDs   []string
		EvidenceIDs []string
	}
	hypothesisIDs := make([]string, 0, len(hypotheses))
	hypothesisSupports := make(map[string]hypothesisSupport, len(hypotheses))
	for _, hypothesis := range hypotheses {
		label := strings.TrimSpace(hypothesis.Claim)
		if label == "" {
			continue
		}
		nodeID := hypothesisNodeID(hypothesis)
		sourceIDs, evidenceIDs := hypothesisSupportIDs(hypothesis)
		metadata := map[string]any{
			"falsifiabilityCondition": hypothesis.FalsifiabilityCondition,
			"isTerminated":            hypothesis.IsTerminated,
		}
		if len(evidenceIDs) > 0 {
			metadata["evidenceIds"] = evidenceIDs
		}
		hypothesisIDs = append(hypothesisIDs, nodeID)
		hypothesisSupports[nodeID] = hypothesisSupport{
			SourceIDs:   sourceIDs,
			EvidenceIDs: evidenceIDs,
		}
		appendNode(ReasoningNode{
			ID:         nodeID,
			Type:       ReasoningNodeHypothesis,
			Label:      label,
			Confidence: hypothesis.ConfidenceThreshold,
			SourceIDs:  sourceIDs,
			Metadata:   metadata,
		})
		if graph.Query != "" {
			appendEdge(ReasoningEdge{From: "question_root", To: nodeID, Label: "generates"})
		}
	}
	evidenceOrder := make([]string, 0, len(evidence))
	evidenceNodeIDsByRef := make(map[string]string, len(evidence)*2)
	evidenceNodeIDsBySource := make(map[string][]string, len(evidence))
	attachedEvidence := make(map[string]struct{}, len(evidence))
	for _, item := range evidence {
		label := evidenceLabel(item)
		if label == "" {
			continue
		}
		evidenceID := evidenceNodeID(item)
		evidenceOrder = append(evidenceOrder, evidenceID)
		attachedEvidence[evidenceID] = struct{}{}
		if trimmed := strings.TrimSpace(item.ID); trimmed != "" {
			evidenceNodeIDsByRef[trimmed] = evidenceID
		}
		evidenceNodeIDsByRef[evidenceID] = evidenceID
		if trimmed := strings.TrimSpace(item.SourceID); trimmed != "" {
			evidenceNodeIDsBySource[trimmed] = appendUniqueString(evidenceNodeIDsBySource[trimmed], evidenceID)
		}
		appendNode(ReasoningNode{
			ID:         evidenceID,
			Type:       ReasoningNodeEvidence,
			Label:      label,
			Confidence: ClampFloat(item.Confidence, 0, 1),
			SourceIDs:  trimStrings([]string{strings.TrimSpace(item.SourceID)}, 1),
			Metadata: map[string]any{
				"paperTitle":       strings.TrimSpace(item.PaperTitle),
				"snippet":          strings.TrimSpace(item.Snippet),
				"specialistType":   item.Specialist.Type,
				"deepAnalysis":     item.Specialist.DeepAnalysis,
				"verification":     item.Specialist.Verification,
				"specialistNotes":  item.SpecialistNotes,
				"specialistStatus": specialistVerificationStatus(item),
			},
		})
	}
	for _, hypothesisID := range hypothesisIDs {
		support := hypothesisSupports[hypothesisID]
		linked := false
		for _, evidenceID := range support.EvidenceIDs {
			if nodeID := evidenceNodeIDsByRef[strings.TrimSpace(evidenceID)]; nodeID != "" {
				appendEdge(ReasoningEdge{From: hypothesisID, To: nodeID, Label: "supported_by"})
				delete(attachedEvidence, nodeID)
				linked = true
			}
		}
		if linked {
			continue
		}
		for _, sourceID := range support.SourceIDs {
			for _, nodeID := range evidenceNodeIDsBySource[strings.TrimSpace(sourceID)] {
				appendEdge(ReasoningEdge{From: hypothesisID, To: nodeID, Label: "supported_by"})
				delete(attachedEvidence, nodeID)
				linked = true
			}
		}
	}
	if graph.Query != "" {
		for _, evidenceID := range evidenceOrder {
			if _, exists := attachedEvidence[evidenceID]; exists {
				appendEdge(ReasoningEdge{From: "question_root", To: evidenceID, Label: "supported_by"})
			}
		}
	}
	return graph
}

func hypothesisSupportIDs(hypothesis Hypothesis) ([]string, []string) {
	if len(hypothesis.Evidence) == 0 {
		return nil, nil
	}
	sourceIDs := make([]string, 0, len(hypothesis.Evidence))
	evidenceIDs := make([]string, 0, len(hypothesis.Evidence))
	for _, finding := range hypothesis.Evidence {
		if finding == nil {
			continue
		}
		if trimmed := strings.TrimSpace(finding.SourceID); trimmed != "" {
			sourceIDs = append(sourceIDs, trimmed)
		}
		if trimmed := strings.TrimSpace(finding.ID); trimmed != "" {
			evidenceIDs = append(evidenceIDs, trimmed)
		}
	}
	return uniqueTrimmedStrings(sourceIDs), uniqueTrimmedStrings(evidenceIDs)
}

func appendUniqueString(existing []string, value string) []string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return existing
	}
	for _, candidate := range existing {
		if strings.EqualFold(strings.TrimSpace(candidate), trimmed) {
			return existing
		}
	}
	return append(existing, trimmed)
}

func SeedFindingsFromPapers(papers []search.Paper, maxSeeds int) []EvidenceFinding {
	if len(papers) == 0 {
		return nil
	}
	if maxSeeds <= 0 {
		maxSeeds = 3
	}
	seeds := make([]EvidenceFinding, 0, min(len(papers), maxSeeds))
	for idx, paper := range papers {
		if len(seeds) >= maxSeeds {
			break
		}
		sourceID := strings.TrimSpace(paper.ID)
		if sourceID == "" {
			sourceID = strings.TrimSpace(paper.DOI)
		}
		claim := strings.TrimSpace(paper.Title)
		if claim == "" {
			claim = strings.TrimSpace(paper.Abstract)
		}
		if claim == "" {
			continue
		}
		seeds = append(seeds, EvidenceFinding{
			ID:         fmt.Sprintf("seed_%d", idx+1),
			Claim:      claim,
			Snippet:    strings.TrimSpace(paper.Abstract),
			PaperTitle: strings.TrimSpace(paper.Title),
			SourceID:   sourceID,
			Confidence: 0.35,
		})
	}
	return seeds
}

// UpdateReasoningGraphIncrementally merges new hypotheses and evidence into an existing
// graph rather than rebuilding from scratch. Preserves node history and adds hypothesis
// evolution edges (branched_into). On cold start (nil existing), delegates to BuildReasoningGraph.
func UpdateReasoningGraphIncrementally(existing *ReasoningGraph, query string, newHypotheses []Hypothesis, newEvidence []EvidenceFinding, beliefs *BeliefState) *ReasoningGraph {
	if existing == nil || len(existing.Nodes) == 0 {
		graph := BuildReasoningGraph(query, newHypotheses, newEvidence)
		addHypothesisEvolutionEdges(graph, newHypotheses)
		if beliefs != nil {
			graph = MergeBeliefStateIntoReasoningGraph(graph, beliefs)
		}
		return indexReasoningGraph(graph)
	}

	if existing.NodesMap == nil {
		indexReasoningGraph(existing)
	}

	nodeSeen := make(map[string]struct{}, len(existing.Nodes))
	for _, node := range existing.Nodes {
		nodeSeen[node.ID] = struct{}{}
	}
	edgeSeen := make(map[string]struct{}, len(existing.Edges))
	for _, edge := range existing.Edges {
		edgeSeen[edge.From+"|"+edge.To+"|"+edge.Label] = struct{}{}
	}

	appendNode := func(node ReasoningNode) {
		if strings.TrimSpace(node.ID) == "" || strings.TrimSpace(node.Label) == "" {
			return
		}
		if _, exists := nodeSeen[node.ID]; exists {
			if ptr, ok := existing.NodesMap[node.ID]; ok {
				ptr.Confidence = node.Confidence
				if node.Metadata != nil {
					if ptr.Metadata == nil {
						ptr.Metadata = make(map[string]any)
					}
					for k, v := range node.Metadata {
						ptr.Metadata[k] = v
					}
				}
			}
			return
		}
		nodeSeen[node.ID] = struct{}{}
		existing.Nodes = append(existing.Nodes, node)
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
		existing.Edges = append(existing.Edges, edge)
	}

	for _, hypothesis := range newHypotheses {
		label := strings.TrimSpace(hypothesis.Claim)
		if label == "" {
			continue
		}
		nodeID := hypothesisNodeID(hypothesis)
		sourceIDs, evidenceIDs := hypothesisSupportIDs(hypothesis)
		metadata := map[string]any{
			"falsifiabilityCondition": hypothesis.FalsifiabilityCondition,
			"isTerminated":            hypothesis.IsTerminated,
		}
		if len(evidenceIDs) > 0 {
			metadata["evidenceIds"] = evidenceIDs
		}
		appendNode(ReasoningNode{
			ID:         nodeID,
			Type:       ReasoningNodeHypothesis,
			Label:      label,
			Confidence: hypothesis.ConfidenceThreshold,
			SourceIDs:  sourceIDs,
			Metadata:   metadata,
		})
		if existing.Root != "" {
			appendEdge(ReasoningEdge{From: existing.Root, To: nodeID, Label: "generates"})
		}

		for _, ev := range hypothesis.Evidence {
			if ev == nil {
				continue
			}
			evNodeID := evidenceNodeID(*ev)
			if _, exists := nodeSeen[evNodeID]; exists {
				appendEdge(ReasoningEdge{From: nodeID, To: evNodeID, Label: "supported_by"})
			}
		}
	}

	for _, item := range newEvidence {
		label := evidenceLabel(item)
		if label == "" {
			continue
		}
		evID := evidenceNodeID(item)
		appendNode(ReasoningNode{
			ID:         evID,
			Type:       ReasoningNodeEvidence,
			Label:      label,
			Confidence: ClampFloat(item.Confidence, 0, 1),
			SourceIDs:  trimStrings([]string{strings.TrimSpace(item.SourceID)}, 1),
			Metadata: map[string]any{
				"paperTitle": strings.TrimSpace(item.PaperTitle),
				"snippet":    strings.TrimSpace(item.Snippet),
			},
		})
	}

	// P6: Hypothesis evolution edges
	for _, hypothesis := range newHypotheses {
		if hypothesis.ParentID == "" {
			continue
		}
		childID := hypothesisNodeID(hypothesis)
		for _, parent := range newHypotheses {
			if parent.ID == hypothesis.ParentID {
				parentID := hypothesisNodeID(parent)
				if _, exists := nodeSeen[parentID]; exists {
					appendEdge(ReasoningEdge{From: parentID, To: childID, Label: "branched_into"})
				}
				break
			}
		}
	}

	if beliefs != nil {
		existing = MergeBeliefStateIntoReasoningGraph(existing, beliefs)
	}
	return indexReasoningGraph(existing)
}

// addHypothesisEvolutionEdges adds branched_into edges for hypotheses with ParentID set.
func addHypothesisEvolutionEdges(graph *ReasoningGraph, hypotheses []Hypothesis) {
	if graph == nil {
		return
	}
	nodeSeen := make(map[string]struct{}, len(graph.Nodes))
	for _, node := range graph.Nodes {
		nodeSeen[node.ID] = struct{}{}
	}
	edgeSeen := make(map[string]struct{}, len(graph.Edges))
	for _, edge := range graph.Edges {
		edgeSeen[edge.From+"|"+edge.To+"|"+edge.Label] = struct{}{}
	}
	for _, hypothesis := range hypotheses {
		if hypothesis.ParentID == "" {
			continue
		}
		childID := hypothesisNodeID(hypothesis)
		for _, parent := range hypotheses {
			if parent.ID == hypothesis.ParentID {
				parentID := hypothesisNodeID(parent)
				if _, exists := nodeSeen[parentID]; exists {
					key := parentID + "|" + childID + "|branched_into"
					if _, exists := edgeSeen[key]; !exists {
						edgeSeen[key] = struct{}{}
						graph.Edges = append(graph.Edges, ReasoningEdge{From: parentID, To: childID, Label: "branched_into"})
					}
				}
				break
			}
		}
	}
}

// IdentifyUnderSupportedHypotheses returns hypothesis node IDs with fewer than
// minEvidenceEdges "supported_by" edges, sorted by support count ascending.
func IdentifyUnderSupportedHypotheses(graph *ReasoningGraph, minEvidenceEdges int) []string {
	if graph == nil || minEvidenceEdges <= 0 {
		return nil
	}
	supportCount := make(map[string]int)
	for _, node := range graph.Nodes {
		if node.Type == ReasoningNodeHypothesis {
			supportCount[node.ID] = 0
		}
	}
	for _, edge := range graph.Edges {
		if edge.Label == "supported_by" {
			if _, isHyp := supportCount[edge.From]; isHyp {
				supportCount[edge.From]++
			}
		}
	}
	type idCount struct {
		id    string
		count int
	}
	var under []idCount
	for id, count := range supportCount {
		if count < minEvidenceEdges {
			under = append(under, idCount{id, count})
		}
	}
	sort.SliceStable(under, func(i, j int) bool {
		return under[i].count < under[j].count
	})
	result := make([]string, len(under))
	for i, item := range under {
		result[i] = item.id
	}
	return result
}

// SuggestExplorationTargets returns non-terminated hypotheses that are under-supported
// in the reasoning graph, sorted by uncertainty (1-confidence) descending, capped at maxTargets.
func SuggestExplorationTargets(graph *ReasoningGraph, hypotheses []Hypothesis, maxTargets int) []Hypothesis {
	if graph == nil || maxTargets <= 0 {
		return nil
	}
	underIDs := IdentifyUnderSupportedHypotheses(graph, 2)
	if len(underIDs) == 0 {
		return nil
	}
	underSet := make(map[string]struct{}, len(underIDs))
	for _, id := range underIDs {
		underSet[id] = struct{}{}
	}
	var targets []Hypothesis
	for _, h := range hypotheses {
		if h.IsTerminated {
			continue
		}
		if _, exists := underSet[hypothesisNodeID(h)]; exists {
			targets = append(targets, h)
		}
	}
	sort.SliceStable(targets, func(i, j int) bool {
		return (1 - targets[i].ConfidenceScore) > (1 - targets[j].ConfidenceScore)
	})
	if len(targets) > maxTargets {
		targets = targets[:maxTargets]
	}
	return targets
}

func BuildReasoningGraphWithPaperSeeds(query string, hypotheses []Hypothesis, evidence []EvidenceFinding, papers []search.Paper) *ReasoningGraph {
	graph := BuildReasoningGraph(query, hypotheses, evidence)
	if len(graph.Nodes) > 0 {
		return graph
	}
	seedEvidence := SeedFindingsFromPapers(papers, 3)
	if len(seedEvidence) == 0 {
		return graph
	}
	return BuildReasoningGraph(query, hypotheses, seedEvidence)
}

func BuildArchitectureProgressPayload(session *AgentSession) map[string]any {
	if session == nil {
		return map[string]any{}
	}
	EnsureSessionArchitectureState(session)

	// If reasoning graph is empty (only root node), try to populate from available artifacts
	if session.ReasoningGraph != nil && len(session.ReasoningGraph.Nodes) <= 1 {
		RefreshSessionArchitectureFromArtifacts(session, StepArtifactSet{}, nil)
	}

	return map[string]any{
		"mode":           session.Mode,
		"serviceTier":    session.ServiceTier,
		"modeManifest":   session.ModeManifest,
		"reasoningGraph": session.ReasoningGraph,
		"memoryTiers":    session.MemoryTiers,
	}
}

func PersistSessionArchitectureState(
	ctx context.Context,
	store SessionStore,
	session *AgentSession,
	hypotheses []Hypothesis,
	evidence []EvidenceFinding,
	papers []search.Paper,
	ttl time.Duration,
) error {
	if store == nil || session == nil {
		return nil
	}
	UpdateSessionReasoningGraph(session, hypotheses, evidence, papers...)
	session.UpdatedAt = NowMillis()
	return store.Put(ctx, session, ttl)
}

func UpdateSessionReasoningGraph(session *AgentSession, hypotheses []Hypothesis, evidence []EvidenceFinding, papers ...search.Paper) {
	EnsureSessionArchitectureState(session)
	if session == nil {
		return
	}
	if session.ReasoningGraph != nil && len(session.ReasoningGraph.Nodes) > 1 {
		session.ReasoningGraph = UpdateReasoningGraphIncrementally(
			session.ReasoningGraph,
			resolveSessionQuery(session),
			hypotheses, evidence, session.BeliefState,
		)
	} else {
		session.ReasoningGraph = BuildReasoningGraphWithPaperSeeds(resolveSessionQuery(session), hypotheses, evidence, papers)
		addHypothesisEvolutionEdges(session.ReasoningGraph, hypotheses)
		if session.BeliefState != nil {
			session.ReasoningGraph = MergeBeliefStateIntoReasoningGraph(session.ReasoningGraph, session.BeliefState)
		}
	}
	UpdateSessionMemoryTiers(session, hypotheses, evidence)
}

func hypothesesFromGraph(graph *ReasoningGraph) []Hypothesis {
	if graph == nil {
		return nil
	}
	out := make([]Hypothesis, 0, len(graph.Nodes))
	for _, node := range graph.Nodes {
		if node.Type != ReasoningNodeHypothesis {
			continue
		}
		hypothesis := Hypothesis{
			Claim:               strings.TrimSpace(node.Label),
			ConfidenceThreshold: ClampFloat(node.Confidence, 0, 1),
		}
		if node.Metadata != nil {
			if value, ok := node.Metadata["falsifiabilityCondition"].(string); ok {
				hypothesis.FalsifiabilityCondition = strings.TrimSpace(value)
			}
			if value, ok := node.Metadata["isTerminated"].(bool); ok {
				hypothesis.IsTerminated = value
			}
		}
		if hypothesis.Claim != "" {
			out = append(out, hypothesis)
		}
	}
	return out
}

func evidenceFromGraph(graph *ReasoningGraph) []EvidenceFinding {
	if graph == nil {
		return nil
	}
	out := make([]EvidenceFinding, 0, len(graph.Nodes))
	for idx, node := range graph.Nodes {
		if node.Type != ReasoningNodeEvidence && node.Type != ReasoningNodeClaim {
			continue
		}
		finding := EvidenceFinding{
			ID:         strings.TrimSpace(node.ID),
			Claim:      strings.TrimSpace(node.Label),
			Confidence: ClampFloat(node.Confidence, 0, 1),
		}
		if len(node.SourceIDs) > 0 {
			finding.SourceID = strings.TrimSpace(node.SourceIDs[0])
		}
		if node.Metadata != nil {
			if value, ok := node.Metadata["paperTitle"].(string); ok {
				finding.PaperTitle = strings.TrimSpace(value)
			}
			if value, ok := node.Metadata["snippet"].(string); ok {
				finding.Snippet = strings.TrimSpace(value)
			}
		}
		if finding.ID == "" {
			finding.ID = fmt.Sprintf("graph_evidence_%d", idx+1)
		}
		if finding.Claim != "" {
			out = append(out, finding)
		}
	}
	return out
}

func hypothesesFromBranches(branches []ReasoningBranch) []Hypothesis {
	out := make([]Hypothesis, 0, len(branches))
	for _, branch := range branches {
		claim := strings.TrimSpace(branch.Claim)
		if claim == "" {
			continue
		}
		out = append(out, Hypothesis{
			Claim:                   claim,
			FalsifiabilityCondition: strings.TrimSpace(branch.FalsifiabilityCondition),
			ConfidenceThreshold:     ClampFloat(branch.SupportScore, 0, 1),
			IsTerminated:            branch.IsTerminated,
		})
	}
	return out
}

func evidenceFindingsFromSources(sources []Source) []EvidenceFinding {
	out := make([]EvidenceFinding, 0, len(sources))
	for idx, source := range sources {
		claim := strings.TrimSpace(source.Title)
		if claim == "" {
			claim = strings.TrimSpace(source.Summary)
		}
		if claim == "" {
			continue
		}
		sourceID := strings.TrimSpace(source.ID)
		if sourceID == "" {
			sourceID = strings.TrimSpace(source.DOI)
		}
		out = append(out, EvidenceFinding{
			ID:         fmt.Sprintf("stream_evidence_%d", idx+1),
			Claim:      claim,
			Snippet:    strings.TrimSpace(source.Summary),
			PaperTitle: strings.TrimSpace(source.Title),
			SourceID:   sourceID,
			Confidence: ClampFloat(source.Score, 0, 1),
		})
	}
	return out
}

func mergeEvidenceFindings(existing []EvidenceFinding, incoming []EvidenceFinding) []EvidenceFinding {
	if len(existing) == 0 {
		return append([]EvidenceFinding(nil), incoming...)
	}
	merged := append([]EvidenceFinding(nil), existing...)
	seen := make(map[string]struct{}, len(existing))
	for _, item := range existing {
		seen[strings.TrimSpace(item.SourceID)+"|"+strings.TrimSpace(item.Claim)] = struct{}{}
	}
	for _, item := range incoming {
		key := strings.TrimSpace(item.SourceID) + "|" + strings.TrimSpace(item.Claim)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, item)
	}
	return merged
}

func RefreshSessionArchitectureFromArtifacts(session *AgentSession, artifactSet StepArtifactSet, sources []Source) {
	EnsureSessionArchitectureState(session)
	if session == nil {
		return
	}

	hypotheses := hypothesesFromGraph(session.ReasoningGraph)
	if artifactSet.ReasoningBundle != nil && len(artifactSet.ReasoningBundle.Branches) > 0 {
		hypotheses = hypothesesFromBranches(artifactSet.ReasoningBundle.Branches)
	}

	evidence := evidenceFromGraph(session.ReasoningGraph)
	if len(sources) > 0 {
		evidence = mergeEvidenceFindings(evidence, evidenceFindingsFromSources(sources))
	}

	UpdateSessionReasoningGraph(session, hypotheses, evidence)
}

// sortMemoryByValue sorts memory entries by evaluation score (desc), then by recency (desc)
func sortMemoryByValue(entries []MemoryEntry) {
	// Use a stable sort to preserve recency order for equal scores
	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			// Sort by score descending, then by timestamp descending
			if entries[i].EvaluationScore < entries[j].EvaluationScore ||
				(entries[i].EvaluationScore == entries[j].EvaluationScore && entries[i].CreatedAt < entries[j].CreatedAt) {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}
}
