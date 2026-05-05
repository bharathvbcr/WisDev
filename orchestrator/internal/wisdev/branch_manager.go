package wisdev

import (
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"sort"
	"strings"
)

type ResearchBranch struct {
	ID              string
	ParentBranchID  string
	Hypothesis      *Hypothesis
	Papers          []search.Paper    // Branch-local evidence pool
	Evidence        []EvidenceFinding // Branch-local findings
	BeliefState     *BeliefState      // Branch-local beliefs
	Confidence      float64
	Budget          int    // Allocated query budget
	Status          string // "active", "pruned", "merged", "winner"
	PendingQueries  []string
	ExecutedQueries []string
}

type BranchManager struct {
	branches    map[string]*ResearchBranch
	maxBranches int
	pruneBelow  float64
}

func NewBranchManager(maxBranches int, pruneBelow float64) *BranchManager {
	return &BranchManager{
		branches:    make(map[string]*ResearchBranch),
		maxBranches: maxBranches,
		pruneBelow:  pruneBelow,
	}
}

func (bm *BranchManager) Fork(parentBranchID string, hypothesis *Hypothesis) *ResearchBranch {
	var branchBeliefs *BeliefState
	if parent := bm.branches[strings.TrimSpace(parentBranchID)]; parent != nil {
		branchBeliefs = cloneBeliefState(parent.BeliefState)
	}
	if branchBeliefs == nil {
		branchBeliefs = NewBeliefState()
	}
	branch := &ResearchBranch{
		ID:             stableWisDevID("branch", hypothesis.ID),
		ParentBranchID: parentBranchID,
		Hypothesis:     hypothesis,
		BeliefState:    branchBeliefs,
		Status:         "active",
	}
	if hypothesis != nil && strings.TrimSpace(hypothesis.Claim) != "" {
		branch.BeliefState.AddBelief(&Belief{
			ID:                    stableWisDevID("branch-belief", branch.ID, hypothesis.Claim),
			Claim:                 hypothesis.Claim,
			Confidence:            ClampFloat(hypothesis.ConfidenceScore, 0.05, 0.95),
			SupportingEvidence:    evidenceIDsFromPtrs(hypothesis.Evidence),
			ContradictingEvidence: evidenceIDsFromPtrs(hypothesis.Contradictions),
			Status:                BeliefStatusActive,
			CreatedAt:             NowMillis(),
			UpdatedAt:             NowMillis(),
		})
	}
	bm.branches[branch.ID] = branch
	return branch
}

func (bm *BranchManager) Score(branch *ResearchBranch) float64 {
	if branch == nil || branch.Hypothesis == nil {
		return 0
	}
	hypothesisScore := ClampFloat(branch.Hypothesis.ConfidenceScore, 0, 1)
	evidenceScore := averageBranchEvidenceConfidence(branch.Evidence)
	coverageScore := ClampFloat(float64(len(branch.Evidence))/5.0, 0, 1)
	diversityScore := branchEvidenceDiversity(branch)
	beliefScore := branchBeliefConfidence(branch.BeliefState)
	contradictionPenalty := branchContradictionPenalty(branch.BeliefState)

	score := hypothesisScore*0.25 + evidenceScore*0.25 + coverageScore*0.20 + diversityScore*0.15 + beliefScore*0.15 - contradictionPenalty
	return ClampFloat(score, 0, 1)
}

func (bm *BranchManager) Prune() {
	var active []*ResearchBranch
	for _, b := range bm.branches {
		if b.Status == "active" {
			active = append(active, b)
		}
	}

	sort.Slice(active, func(i, j int) bool {
		return bm.Score(active[i]) > bm.Score(active[j])
	})

	for i, b := range active {
		if i >= bm.maxBranches || bm.Score(b) < bm.pruneBelow {
			b.Status = "pruned"
		}
	}
}

func (bm *BranchManager) Merge(branches []*ResearchBranch) *ResearchBranch {
	if len(branches) == 0 {
		return nil
	}
	merged := &ResearchBranch{
		ID:          stableWisDevID("branch-merged"),
		Status:      "merged",
		BeliefState: NewBeliefState(),
	}
	seenPapers := make(map[string]struct{})
	seenEvidence := make(map[string]struct{})

	for _, b := range branches {
		for _, p := range b.Papers {
			if _, exists := seenPapers[p.ID]; !exists {
				merged.Papers = append(merged.Papers, p)
				seenPapers[p.ID] = struct{}{}
			}
		}
		for _, e := range b.Evidence {
			if _, exists := seenEvidence[e.ID]; !exists {
				merged.Evidence = append(merged.Evidence, e)
				seenEvidence[e.ID] = struct{}{}
			}
		}
		mergeBeliefState(merged.BeliefState, b.BeliefState)
	}
	return merged
}

func (bm *BranchManager) SelectWinner() *ResearchBranch {
	var best *ResearchBranch
	var maxScore float64
	for _, b := range bm.branches {
		if b.Status == "active" {
			score := bm.Score(b)
			if best == nil || score > maxScore {
				best = b
				maxScore = score
			}
		}
	}
	return best
}

func cloneBeliefState(in *BeliefState) *BeliefState {
	if in == nil {
		return nil
	}
	out := NewBeliefState()
	for id, belief := range in.Beliefs {
		if belief == nil {
			continue
		}
		copyBelief := *belief
		copyBelief.SupportingEvidence = append([]string(nil), belief.SupportingEvidence...)
		copyBelief.ContradictingEvidence = append([]string(nil), belief.ContradictingEvidence...)
		copyBelief.ProvenanceChain = append([]ProvenanceEntry(nil), belief.ProvenanceChain...)
		copyBelief.SourceFamilies = append([]string(nil), belief.SourceFamilies...)
		out.Beliefs[id] = &copyBelief
	}
	return out
}

func mergeBeliefState(dst *BeliefState, src *BeliefState) {
	if dst == nil || src == nil {
		return
	}
	if dst.Beliefs == nil {
		dst.Beliefs = make(map[string]*Belief)
	}
	for id, belief := range src.Beliefs {
		if belief == nil {
			continue
		}
		if existing := dst.Beliefs[id]; existing != nil {
			if belief.Confidence > existing.Confidence {
				existing.Confidence = belief.Confidence
			}
			existing.SupportingEvidence = uniqueTrimmedStrings(append(existing.SupportingEvidence, belief.SupportingEvidence...))
			existing.ContradictingEvidence = uniqueTrimmedStrings(append(existing.ContradictingEvidence, belief.ContradictingEvidence...))
			existing.ProvenanceChain = append(existing.ProvenanceChain, belief.ProvenanceChain...)
			continue
		}
		copyBelief := *belief
		copyBelief.SupportingEvidence = append([]string(nil), belief.SupportingEvidence...)
		copyBelief.ContradictingEvidence = append([]string(nil), belief.ContradictingEvidence...)
		copyBelief.ProvenanceChain = append([]ProvenanceEntry(nil), belief.ProvenanceChain...)
		copyBelief.SourceFamilies = append([]string(nil), belief.SourceFamilies...)
		dst.Beliefs[id] = &copyBelief
	}
}

func evidenceIDsFromPtrs(evidence []*EvidenceFinding) []string {
	ids := make([]string, 0, len(evidence))
	for _, ev := range evidence {
		if ev != nil && strings.TrimSpace(ev.ID) != "" {
			ids = append(ids, strings.TrimSpace(ev.ID))
		}
	}
	return uniqueTrimmedStrings(ids)
}

func averageBranchEvidenceConfidence(evidence []EvidenceFinding) float64 {
	if len(evidence) == 0 {
		return 0.35
	}
	total := 0.0
	for _, ev := range evidence {
		total += ClampFloat(ev.Confidence, 0, 1)
	}
	return total / float64(len(evidence))
}

func branchEvidenceDiversity(branch *ResearchBranch) float64 {
	if branch == nil || len(branch.Evidence) == 0 {
		return 0
	}
	sources := make(map[string]struct{}, len(branch.Evidence))
	for _, ev := range branch.Evidence {
		key := strings.TrimSpace(firstNonEmpty(ev.SourceID, ev.PaperTitle, ev.ID))
		if key != "" {
			sources[strings.ToLower(key)] = struct{}{}
		}
	}
	return ClampFloat(float64(len(sources))/float64(len(branch.Evidence)), 0, 1)
}

func branchBeliefConfidence(bs *BeliefState) float64 {
	if bs == nil {
		return 0.35
	}
	active := bs.GetActiveBeliefs()
	if len(active) == 0 {
		return 0.35
	}
	total := 0.0
	for _, belief := range active {
		total += ClampFloat(belief.Confidence, 0, 1)
	}
	return total / float64(len(active))
}

func branchContradictionPenalty(bs *BeliefState) float64 {
	if bs == nil {
		return 0
	}
	active := bs.GetActiveBeliefs()
	if len(active) == 0 {
		return 0
	}
	contradicted := 0
	for _, belief := range active {
		if len(belief.ContradictingEvidence) > 0 {
			contradicted++
		}
	}
	return 0.2 * float64(contradicted) / float64(len(active))
}

func attachBranchEvidence(branch *ResearchBranch) {
	if branch == nil || branch.BeliefState == nil || branch.Hypothesis == nil {
		return
	}
	branchBeliefID := stableWisDevID("branch-belief", branch.ID, branch.Hypothesis.Claim)
	ids := make([]string, 0, len(branch.Evidence))
	for _, ev := range branch.Evidence {
		if strings.TrimSpace(ev.ID) != "" {
			ids = append(ids, strings.TrimSpace(ev.ID))
		}
	}
	if len(ids) == 0 {
		return
	}
	branch.BeliefState.UpdateBelief(branchBeliefID, func(b *Belief) {
		b.SupportingEvidence = uniqueTrimmedStrings(append(b.SupportingEvidence, ids...))
		b.Confidence = bayesianPosterior(ClampFloat(b.Confidence, 0.05, 0.95), len(ids), len(b.ContradictingEvidence), averageBranchEvidenceConfidence(branch.Evidence), 0.68)
	})
}

func (bm *BranchManager) ActiveBranches() []*ResearchBranch {
	var active []*ResearchBranch
	for _, b := range bm.branches {
		if b.Status == "active" {
			active = append(active, b)
		}
	}
	return active
}
