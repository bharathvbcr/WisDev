package wisdev

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"sort"
	"strings"
	"time"
)

const (
	uctExploreConstant      = 2.0 // was 1.2; √2≈1.414 is standard; 2.0 for high-variance academic search
	minIterationsBeforeStop = 3   // never stop before 3 full iterations
	stagnationThreshold     = 3   // was 2; academic search needs more iterations to converge
)

const (
	verifierWeightWhenPresent = 0.60
	scoreWeightWithVerifier   = 0.40
	scoreWeightAlone          = 1.00
)

const (
	llmExpandTimeout    = 8 * time.Second
	llmExpandMaxRetries = 2
)

type treeLoopIteration struct {
	Iteration  int            `json:"iteration"`
	BranchID   int            `json:"branchId"`
	Success    bool           `json:"success"`
	Score      float64        `json:"score,omitempty"`
	Confidence float64        `json:"confidence,omitempty"`
	Status     string         `json:"status"`
	Reason     string         `json:"reason,omitempty"`
	Output     map[string]any `json:"output,omitempty"`
	Error      error          `json:"-"`
}

type treeLoopResult struct {
	Iterations     []treeLoopIteration `json:"iterations"`
	Final          map[string]any      `json:"final"`
	BestConfidence float64             `json:"bestConfidence"`
	Completed      bool                `json:"completed"`
	VoteSummary    map[string]int      `json:"voteSummary,omitempty"`
}

type branchState struct {
	ID         int
	Payload    map[string]any
	Score      float64
	Confidence float64
}

type completedBranch struct {
	BranchID     int
	Output       map[string]any
	Score        float64
	Confidence   float64
	Verifier     float64
	ConsensusKey string
}

type mctsNode struct {
	ID       int
	Payload  map[string]any
	ParentID int
	Depth    int
	Visits   int
	Value    float64
	Expanded bool
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	raw, _ := json.Marshal(input)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out
}

func anyToString(value any) string {
	if raw, ok := value.(string); ok {
		return raw
	}
	return fmt.Sprintf("%v", value)
}

func extractConfidenceScore(result map[string]any) float64 {
	if result == nil {
		return 0
	}
	if v, ok := result["confidence"].(float64); ok {
		return ClampFloat(v, 0, 1)
	}
	if report, ok := result["confidence_report"].(map[string]any); ok {
		if v, ok := report["overall_confidence"].(float64); ok {
			return ClampFloat(v, 0, 1)
		}
		if v, ok := report["calibrated_score"].(float64); ok {
			return ClampFloat(v, 0, 1)
		}
	}
	return 0.55
}

func extractGroundingScore(result map[string]any) float64 {
	if result == nil {
		return 0
	}
	if v, ok := result["grounding_ratio"].(float64); ok {
		return ClampFloat(v, 0, 1)
	}
	if report, ok := result["confidence_report"].(map[string]any); ok {
		if v, ok := report["grounding_ratio"].(float64); ok {
			return ClampFloat(v, 0, 1)
		}
	}
	return 0.5
}

func scoreBranchResult(result map[string]any) (float64, float64) {
	confidence := extractConfidenceScore(result)
	grounding := extractGroundingScore(result)
	score := ClampFloat((confidence*0.7)+(grounding*0.3), 0, 1)
	return score, confidence
}

func outputConsensusKey(output map[string]any) string {
	if output == nil {
		return "empty"
	}
	candidates := []string{}
	if v, ok := output["summary"].(string); ok {
		candidates = append(candidates, v)
	}
	if v, ok := output["final_answer"].(string); ok {
		candidates = append(candidates, v)
	}
	if v, ok := output["reasoning"].(string); ok {
		candidates = append(candidates, v)
	}
	if len(candidates) == 0 {
		raw, _ := json.Marshal(output)
		candidates = append(candidates, string(raw))
	}
	text := strings.ToLower(strings.Join(candidates, " | "))
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 180 {
		text = text[:180]
	}
	return text
}

func deriveBranchVariants(payload map[string]any, branchID int) []map[string]any {
	variants := make([]map[string]any, 0, 2)

	evidenceFirst := cloneMap(payload)
	evidenceFirst["reasoning_strategy"] = "evidence_first"
	evidenceFirst["branch_parent"] = branchID
	variants = append(variants, evidenceFirst)

	counterFactual := cloneMap(payload)
	counterFactual["reasoning_strategy"] = "counter_argument"
	counterFactual["branch_parent"] = branchID
	variants = append(variants, counterFactual)

	return variants
}

// extractVariantsFromThoughts maps ThoughtGenerationResponse.branches[].nodes[0]
// to Go variant payloads. Takes the first node from each branch as the immediate child.
func extractVariantsFromThoughts(result map[string]any, basePayload map[string]any) []map[string]any {
	branches, ok := result["branches"].([]any)
	if !ok || len(branches) == 0 {
		return nil
	}
	var out []map[string]any
	for _, b := range branches {
		branch, ok := b.(map[string]any)
		if !ok {
			continue
		}
		nodes, ok := branch["nodes"].([]any)
		if !ok || len(nodes) == 0 {
			continue
		}
		nodeItem, ok := nodes[0].(map[string]any)
		if !ok {
			continue
		}
		variant := cloneMap(basePayload)
		if label, ok := nodeItem["label"].(string); ok && label != "" {
			variant["label"] = label
		}
		if reasoning, ok := nodeItem["reasoning"].(string); ok {
			variant["reasoning"] = reasoning
		}
		if sw, ok := nodeItem["search_weight"].(float64); ok {
			variant["search_weight"] = sw
		}
		if hyp, ok := branch["hypothesis"].(string); ok {
			variant["branch_hypothesis"] = hyp
		}
		out = append(out, variant)
	}
	return out
}

func expandNodeWithLLM(
	ctx context.Context,
	execFn func(context.Context, string, map[string]any, *AgentSession) (map[string]any, error),
	session *AgentSession,
	node *mctsNode,
	basePayload map[string]any,
	siblingLabels []string,
) ([]map[string]any, error) {
	callCtx, cancel := context.WithTimeout(ctx, llmExpandTimeout)
	defer cancel()

	expandPayload := map[string]any{
		"query":              basePayload["query"],
		"current_node_label": node.Payload["label"],
		"domain":             basePayload["domain"],
		"depth":              node.Depth,
		"sibling_labels":     siblingLabels,
		"n_branches":         2,
	}
	if rawHypo, ok := basePayload["hypothesis"].(map[string]any); ok {
		expandPayload["falsifiability_condition"] = rawHypo["falsifiabilityCondition"]
		expandPayload["primary_claim"] = rawHypo["claim"]
	}

	var lastErr error
	for attempt := 0; attempt <= llmExpandMaxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff with jitter
			base := time.Duration(1<<uint(attempt)) * 200 * time.Millisecond
			jitter := time.Duration(rand.Int63n(int64(base / 2)))
			select {
			case <-time.After(base + jitter):
			case <-callCtx.Done():
				goto fallback
			}
		}
		result, err := execFn(callCtx, "research.generateThoughts", expandPayload, session)
		if err == nil {
			variants := extractVariantsFromThoughts(result, basePayload)
			pruned := PruneRedundantBranches(variants, siblingLabels, 0.7)
			if len(pruned) > 0 {
				slog.Info("expandNodeWithLLM success",
					"nodeID", node.ID, "depth", node.Depth,
					"variants", len(variants), "afterPrune", len(pruned))
				return pruned, nil
			}
			slog.Warn("expandNodeWithLLM: all variants pruned as redundant",
				"nodeID", node.ID, "siblingCount", len(siblingLabels))
		}
		lastErr = err
		slog.Warn("expandNodeWithLLM attempt failed",
			"attempt", attempt+1, "error", err,
			"nodeID", node.ID, "depth", node.Depth)
	}

fallback:
	slog.Info("expandNodeWithLLM falling back to deriveBranchVariants",
		"nodeID", node.ID, "cause", lastErr)
	return deriveBranchVariants(basePayload, node.ID), nil
}

func maybeVerifierScore(
	ctx context.Context,
	exec func(context.Context, string, map[string]any, *AgentSession) (map[string]any, error),
	session *AgentSession,
	output map[string]any,
	basePayload map[string]any,
) float64 {
	verifyAction := "research.verifyClaims"
	if raw, ok := basePayload["verifierAction"].(string); ok && strings.TrimSpace(raw) != "" {
		verifyAction = strings.TrimSpace(raw)
	}
	verifyPayload := map[string]any{
		"candidateOutput": output,
		"mode":            "rerank",
	}
	verified, err := exec(ctx, verifyAction, verifyPayload, session)
	if err != nil {
		return 0
	}
	if v, ok := verified["score"].(float64); ok {
		return ClampFloat(v, 0, 1)
	}
	if report, ok := verified["confidence_report"].(map[string]any); ok {
		if v, ok := report["calibrated_score"].(float64); ok {
			return ClampFloat(v, 0, 1)
		}
	}
	if v, ok := verified["confidence"].(float64); ok {
		return ClampFloat(v, 0, 1)
	}
	return 0
}

func uctScore(node *mctsNode, parentVisits int) float64 {
	if node == nil {
		return -1
	}
	if node.Visits == 0 {
		return math.Inf(1) // always explore unvisited nodes first
	}
	exploit := node.Value / float64(node.Visits)
	explore := uctExploreConstant * math.Sqrt(math.Log(float64(parentVisits+1))/float64(node.Visits))
	return exploit + explore
}

func selectNodeByUCT(active []*mctsNode, rootVisits int) *mctsNode {
	if len(active) == 0 {
		return nil
	}
	best := active[0]
	bestScore := uctScore(best, rootVisits)
	for i := 1; i < len(active); i++ {
		score := uctScore(active[i], rootVisits)
		if score > bestScore {
			bestScore = score
			best = active[i]
		}
	}
	return best
}

func backpropagate(nodes map[int]*mctsNode, startID int, reward float64) {
	curID := startID
	for curID != 0 {
		node, ok := nodes[curID]
		if !ok {
			break
		}
		node.Visits++
		node.Value += reward
		curID = node.ParentID
	}
}

func selectWinnerByConsensus(candidates []completedBranch) (completedBranch, map[string]int, bool) {
	if len(candidates) == 0 {
		return completedBranch{}, nil, false
	}
	voteCounts := make(map[string]int)
	for _, c := range candidates {
		voteCounts[c.ConsensusKey]++
	}
	best := candidates[0]
	bestVotes := voteCounts[best.ConsensusKey]
	for i := 1; i < len(candidates); i++ {
		cur := candidates[i]
		curVotes := voteCounts[cur.ConsensusKey]
		if curVotes > bestVotes {
			best = cur
			bestVotes = curVotes
			continue
		}
		if curVotes == bestVotes && cur.Verifier > best.Verifier {
			best = cur
			bestVotes = curVotes
			continue
		}
		if curVotes == bestVotes && cur.Verifier == best.Verifier && cur.Score > best.Score {
			best = cur
			bestVotes = curVotes
		}
	}
	return best, voteCounts, true
}

func RunProgrammaticTreeLoop(
	ctx context.Context,
	exec func(context.Context, string, map[string]any, *AgentSession) (map[string]any, error),
	session *AgentSession,
	action string,
	basePayload map[string]any,
	maxIterations int,
	streamFn func(map[string]any), // optional SSE emitter; nil = no streaming
) treeLoopResult {
	iterations := maxIterations
	if iterations <= 0 {
		iterations = 1
	}
	if iterations > 8 {
		iterations = 8
	}

	branchWidth := 2
	if raw, ok := basePayload["branchWidth"].(float64); ok && int(raw) > 0 {
		branchWidth = int(raw)
	}
	if branchWidth > 4 {
		branchWidth = 4
	}
	if branchWidth <= 0 {
		branchWidth = 2
	}

	root := &mctsNode{
		ID:       1,
		Payload:  cloneMap(basePayload),
		ParentID: 0,
		Depth:    0,
	}
	nodes := map[int]*mctsNode{
		1: root,
	}
	active := []*mctsNode{root}
	nextBranchID := 2

	events := make([]treeLoopIteration, 0, iterations*branchWidth)
	completedBranches := make([]completedBranch, 0, iterations*branchWidth)
	var bestOutput map[string]any
	bestScore := 0.0
	bestConfidence := 0.0
	stagnation := 0

	for iteration := 1; iteration <= iterations; iteration++ {
		if len(active) == 0 {
			break
		}

		selectedNode := selectNodeByUCT(active, root.Visits+1)
		if selectedNode == nil {
			break
		}

		payload := cloneMap(selectedNode.Payload)
		payload["iteration"] = iteration
		payload["maxIterations"] = iterations
		payload["branchId"] = selectedNode.ID
		payload["tree_search"] = true
		payload["mcts"] = true

		out, err := exec(ctx, action, payload, session)
		if err != nil {
			events = append(events, treeLoopIteration{
				Iteration: iteration,
				BranchID:  selectedNode.ID,
				Success:   false,
				Status:    "failed",
				Reason:    err.Error(),
				Error:     err,
			})
			// Mark visit with zero reward to discourage repeatedly failing branch.
			backpropagate(nodes, selectedNode.ID, 0)
			if len(active) > 1 {
				next := make([]*mctsNode, 0, len(active)-1)
				for _, n := range active {
					if n.ID != selectedNode.ID {
						next = append(next, n)
					}
				}
				active = next
			}
			continue
		}

		score, confidence := scoreBranchResult(out)
		verifier := maybeVerifierScore(ctx, exec, session, out, basePayload)
		emitEvent(streamFn, StreamEvent{
			Type:          EventVerifierScored,
			NodeID:        selectedNode.ID,
			VerifierScore: verifier,
		})
		var finalReward float64
		if verifier > 0 {
			finalReward = (score * scoreWeightWithVerifier) + (verifier * verifierWeightWhenPresent)
		} else {
			finalReward = score * scoreWeightAlone
		}
		finalReward = math.Max(0.0, math.Min(1.0, finalReward))
		backpropagate(nodes, selectedNode.ID, finalReward)

		events = append(events, treeLoopIteration{
			Iteration:  iteration,
			BranchID:   selectedNode.ID,
			Success:    true,
			Score:      finalReward,
			Confidence: confidence,
			Status:     "completed",
			Output:     out,
			Reason:     "mcts_rollout_complete",
		})
		completedBranches = append(completedBranches, completedBranch{
			BranchID:     selectedNode.ID,
			Output:       out,
			Score:        finalReward,
			Confidence:   confidence,
			Verifier:     verifier,
			ConsensusKey: outputConsensusKey(out),
		})

		if finalReward > bestScore {
			bestScore = finalReward
			bestConfidence = confidence
			bestOutput = out
		}

		emitEvent(streamFn, StreamEvent{
			Type:          EventIterationComplete,
			Iteration:     iteration,
			CoverageScore: score,
			PRMReward:     finalReward,
		})
		if finalReward < 0.45 {
			streamFn(map[string]any{
				"type":      "escalation_triggered",
				"iteration": iteration,
				"reason":    "low_reward_branch",
			})
		}

		topScore := finalReward
		if topScore <= bestScore+0.01 {
			stagnation++
		} else {
			stagnation = 0
		}

		confidenceTarget := 0.92
		if rawHypo, ok := basePayload["hypothesis"].(map[string]any); ok {
			if v, ok := rawHypo["confidenceThreshold"].(float64); ok && v > 0 {
				confidenceTarget = v
			}
		}

		if iteration >= minIterationsBeforeStop && bestConfidence >= confidenceTarget {
			slog.Info("MCTS early stop: hypothesis confidence target reached", "confidence", bestConfidence, "target", confidenceTarget)
			events = append(events, treeLoopIteration{
				Iteration: iteration,
				BranchID:  selectedNode.ID,
				Success:   true,
				Status:    "early_stop",
				Reason:    "confidence_target_reached",
			})
			break
		}
		if iteration >= minIterationsBeforeStop && stagnation >= stagnationThreshold {
			slog.Info("MCTS early stop: stagnation", "stagnation", stagnation, "threshold", stagnationThreshold, "iteration", iteration)
			events = append(events, treeLoopIteration{
				Iteration: iteration,
				BranchID:  selectedNode.ID,
				Success:   true,
				Status:    "early_stop",
				Reason:    "branch_score_stagnation",
			})
			break
		}

		// Expand selected node if possible and add children to active set.
		if iteration < iterations && !selectedNode.Expanded {
			siblingLabels := []string{}
			for _, n := range active {
				if n.ParentID == selectedNode.ID {
					if l, ok := n.Payload["label"].(string); ok {
						siblingLabels = append(siblingLabels, l)
					}
				}
			}

			prePruneVariants, _ := expandNodeWithLLM(ctx, exec, session, selectedNode, basePayload, siblingLabels)
			variants := prePruneVariants
			if len(variants) == 0 {
				variants = deriveBranchVariants(selectedNode.Payload, selectedNode.ID)
			} else {
				// Emit branch_pruned events for any variants removed by expandNodeWithLLM's internal pruning.
				// Re-run pruning here only to detect which labels were pruned, for streaming purposes.
				postPrune := PruneRedundantBranches(prePruneVariants, siblingLabels, 0.7)
				if len(postPrune) != len(prePruneVariants) {
					kept := make(map[string]bool, len(postPrune))
					for _, v := range postPrune {
						if l, ok := v["label"].(string); ok {
							kept[l] = true
						}
					}
					for _, v := range prePruneVariants {
						if l, ok := v["label"].(string); ok && !kept[l] {
							emitEvent(streamFn, StreamEvent{
								Type:   EventBranchPruned,
								NodeID: selectedNode.ID,
								Label:  l,
								Reason: "jaccard_similarity",
							})
						}
					}
					variants = postPrune
				}
			}

			for _, variant := range variants {
				if len(active) >= branchWidth {
					break
				}
				child := &mctsNode{
					ID:       nextBranchID,
					Payload:  variant,
					ParentID: selectedNode.ID,
					Depth:    selectedNode.Depth + 1,
				}
				nodes[child.ID] = child
				active = append(active, child)
				hypothesis, _ := variant["branch_hypothesis"].(string)
				label, _ := variant["label"].(string)
				emitEvent(streamFn, StreamEvent{
					Type:       EventThoughtGenerated,
					NodeID:     child.ID,
					Hypothesis: hypothesis,
					Depth:      child.Depth,
					Label:      label,
				})
				nextBranchID++
			}
			selectedNode.Expanded = true
		}

		// Keep top active nodes by current UCT proxy.
		sort.SliceStable(active, func(i, j int) bool {
			return uctScore(active[i], root.Visits+1) > uctScore(active[j], root.Visits+1)
		})
		if len(active) > branchWidth {
			active = active[:branchWidth]
		}
	}

	completed := true
	for _, event := range events {
		if !event.Success && event.Status == "failed" {
			completed = false
		}
	}
	if bestOutput == nil {
		bestOutput = map[string]any{
			"status": "failed",
			"error":  "no_successful_branch",
		}
		completed = false
	}

	voteSummary := map[string]int{}
	if streamFn != nil && len(completedBranches) > 1 {
		streamFn(map[string]any{
			"type":           "committee_started",
			"candidateCount": len(completedBranches),
			"method":         "self_consistency_plus_verifier",
		})
	}
	if winner, votes, ok := selectWinnerByConsensus(completedBranches); ok {
		voteSummary = votes
		bestOutput = cloneMap(winner.Output)
		bestOutput["selection_meta"] = map[string]any{
			"method":         "self_consistency_plus_verifier",
			"winnerBranchId": winner.BranchID,
			"winnerScore":    winner.Score,
			"winnerVerifier": winner.Verifier,
			"winnerVotes":    votes[winner.ConsensusKey],
		}
		bestConfidence = winner.Confidence
		if streamFn != nil {
			streamFn(map[string]any{
				"type":         "committee_resolved",
				"winnerBranch": winner.BranchID,
				"winnerScore":  winner.Score,
			})
		}
	}

	// Tree self-critique metadata for auditability.
	labelSet := make([]string, 0, len(nodes))
	depthCounts := map[int]int{}
	maxDepth := 0
	minDepth := 0
	firstDepth := true
	for _, node := range nodes {
		if node == nil {
			continue
		}
		depthCounts[node.Depth]++
		if firstDepth {
			firstDepth = false
			maxDepth = node.Depth
			minDepth = node.Depth
		} else {
			if node.Depth > maxDepth {
				maxDepth = node.Depth
			}
			if node.Depth < minDepth {
				minDepth = node.Depth
			}
		}
		if label, ok := node.Payload["label"].(string); ok && strings.TrimSpace(label) != "" {
			labelSet = append(labelSet, strings.ToLower(strings.TrimSpace(label)))
		}
	}

	duplicatePairs := make([]string, 0)
	for i := 0; i < len(labelSet); i++ {
		for j := i + 1; j < len(labelSet); j++ {
			if JaccardSimilarity(labelSet[i], labelSet[j]) > 0.7 {
				duplicatePairs = append(duplicatePairs, labelSet[i]+" || "+labelSet[j])
			}
		}
	}

	missingPriorities := []string{}
	if raw, ok := basePayload["prioritySubtopics"].([]any); ok {
		allLabelsText := strings.Join(labelSet, " ")
		for _, topicRaw := range raw {
			topic := strings.ToLower(strings.TrimSpace(anyToString(topicRaw)))
			if topic == "" {
				continue
			}
			if !strings.Contains(allLabelsText, topic) {
				missingPriorities = append(missingPriorities, topic)
			}
		}
	}

	unbalancedDepth := (maxDepth - minDepth) > 2
	bestOutput["treeSelfCritique"] = map[string]any{
		"duplicateNodePairs": duplicatePairs,
		"unbalancedDepth":    unbalancedDepth,
		"depthSpan":          maxDepth - minDepth,
		"missingPriorities":  missingPriorities,
	}

	return treeLoopResult{
		Iterations:     events,
		Final:          bestOutput,
		BestConfidence: bestConfidence,
		Completed:      completed,
		VoteSummary:    voteSummary,
	}
}

func TreeLoopIterationsToHTTP(events []treeLoopIteration) []map[string]any {
	out := make([]map[string]any, 0, len(events))
	for _, event := range events {
		entry := map[string]any{
			"iteration": event.Iteration,
			"branchId":  event.BranchID,
			"status":    event.Status,
			"success":   event.Success,
		}
		if event.Score > 0 {
			entry["score"] = event.Score
		}
		if event.Confidence > 0 {
			entry["confidence"] = event.Confidence
		}
		if strings.TrimSpace(event.Reason) != "" {
			entry["reason"] = event.Reason
		}
		out = append(out, entry)
	}
	return out
}
