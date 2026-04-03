package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
)

type TopicTreeLabel struct {
	Label     string `json:"label"`
	Reasoning string `json:"reasoning,omitempty"`
}

type topicTreeNodeMetadata struct {
	PaperCount     int     `json:"paperCount,omitempty"`
	RelevanceScore float64 `json:"relevanceScore,omitempty"`
	Source         string  `json:"source,omitempty"`
	Timeframe      string  `json:"timeframe,omitempty"`
	Priority       string  `json:"priority,omitempty"`
	AIReasoning    string  `json:"aiReasoning,omitempty"`
}

type topicTreeNode struct {
	ID         string                 `json:"id"`
	Label      string                 `json:"label"`
	Children   []topicTreeNode        `json:"children"`
	IsSelected bool                   `json:"isSelected"`
	IsExpanded bool                   `json:"isExpanded"`
	Depth      int                    `json:"depth"`
	NodeType   string                 `json:"nodeType"`
	Metadata   *topicTreeNodeMetadata `json:"metadata,omitempty"`
}

var topicTreeNodeCounter uint64

type topicTreeAuditLog struct {
	Timestamp  time.Time `json:"timestamp"`
	Action     string    `json:"action"`
	Details    string    `json:"details"`
	AIModel    string    `json:"aiModel,omitempty"`
	Confidence float64   `json:"confidence,omitempty"`
}

type topicTreeGenerateRequest struct {
	Query                string   `json:"query"`
	CorrectedQuery       string   `json:"correctedQuery,omitempty"`
	Domain               string   `json:"domain,omitempty"`
	DetectedDomain       string   `json:"detectedDomain,omitempty"`
	SelectedSubtopics    []string `json:"selectedSubtopics,omitempty"`
	Subtopics            []string `json:"subtopics,omitempty"`
	Scope                string   `json:"scope,omitempty"`
	ExpansionStrategy    string   `json:"expansionStrategy,omitempty"`
	UserPriorities       []string `json:"userPriorities,omitempty"`
	ExpertiseLevel       string   `json:"expertiseLevel,omitempty"`
	Timeframe            string   `json:"timeframe,omitempty"`
	StudyTypes           []string `json:"studyTypes,omitempty"`
	Exclusions           []string `json:"exclusions,omitempty"`
	MaxDepth             int      `json:"maxDepth,omitempty"`
	MaxNodesPerLevel     int      `json:"maxNodesPerLevel,omitempty"`
	IncludeTemplateNodes *bool    `json:"includeTemplateNodes,omitempty"`
	SessionID            string   `json:"sessionId,omitempty"`
}

type topicTreeGenerateResponse struct {
	ID            string              `json:"id"`
	Name          string              `json:"name"`
	RootNode      topicTreeNode       `json:"rootNode"`
	Domain        string              `json:"domain"`
	Query         string              `json:"query"`
	CreatedAt     time.Time           `json:"createdAt"`
	UpdatedAt     time.Time           `json:"updatedAt"`
	TotalNodes    int                 `json:"totalNodes"`
	SelectedNodes int                 `json:"selectedNodes"`
	AuditTrail    []topicTreeAuditLog `json:"auditTrail"`
	Strategy      string              `json:"_strategy,omitempty"`
}

type topicTreeChildrenRequest struct {
	Query          string   `json:"query"`
	Subtopic       string   `json:"subtopic"`
	Count          int      `json:"count"`
	Domain         string   `json:"domain,omitempty"`
	Strategy       string   `json:"strategy,omitempty"`
	Priorities     []string `json:"priorities,omitempty"`
	ExpertiseLevel string   `json:"expertiseLevel,omitempty"`
}

type topicTreeEdgesRequest struct {
	Query             string   `json:"query"`
	ExistingSubtopics []string `json:"existingSubtopics"`
	Domain            string   `json:"domain,omitempty"`
}

type topicTreeRefineQueriesRequest struct {
	Query           string   `json:"query"`
	Domain          string   `json:"domain,omitempty"`
	BaselineQueries []string `json:"baselineQueries"`
	MaxQueries      int      `json:"maxQueries"`
	Timeframe       string   `json:"timeframe,omitempty"`
	StudyTypes      []string `json:"studyTypes,omitempty"`
	Exclusions      []string `json:"exclusions,omitempty"`
	Strategy        string   `json:"strategy,omitempty"`
}

type topicTreeEdgesResponse struct {
	Labels    []TopicTreeLabel `json:"labels"`
	Reasoning string           `json:"reasoning"`
}

type topicTreeRefineQueriesResponse struct {
	Queries []string `json:"queries"`
}

type TopicTreeHandler struct {
	rdb redis.UniversalClient
}

func NewTopicTreeHandler(rdb redis.UniversalClient) *TopicTreeHandler {
	return &TopicTreeHandler{rdb: rdb}
}

func (h *TopicTreeHandler) HandleTopicTreeGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "Method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	req, err := decodeTopicTreeGenerateRequest(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "Failed to parse request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	result := buildTopicTreeGeneration(req)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func (h *TopicTreeHandler) HandleTopicTreeChildren(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "Method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	req, err := decodeTopicTreeChildrenRequest(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "Failed to parse request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	count := req.Count
	if count <= 0 {
		count = 6
	}

	labels := buildTopicTreeChildren(req, count)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"labels": labels,
	})
}

func handleTopicTreeEdges(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "Method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	req, err := decodeTopicTreeEdgesRequest(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "Failed to parse request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	result := buildTopicTreeEdges(req)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func (h *TopicTreeHandler) HandleTopicTreeRefineQueries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "Method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	req, err := decodeTopicTreeRefineQueriesRequest(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "Failed to parse request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	result := buildTopicTreeRefinedQueries(h.rdb, req)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func decodeTopicTreeChildrenRequest(r *http.Request) (topicTreeChildrenRequest, error) {
	var req topicTreeChildrenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return req, err
	}
	return req, nil
}

func decodeTopicTreeEdgesRequest(r *http.Request) (topicTreeEdgesRequest, error) {
	var req topicTreeEdgesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return req, err
	}
	return req, nil
}

func decodeTopicTreeRefineQueriesRequest(r *http.Request) (topicTreeRefineQueriesRequest, error) {
	var req topicTreeRefineQueriesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return req, err
	}
	return req, nil
}

func decodeTopicTreeGenerateRequest(r *http.Request) (topicTreeGenerateRequest, error) {
	var req topicTreeGenerateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return req, err
	}
	return req, nil
}

func buildTopicTreeGeneration(req topicTreeGenerateRequest) topicTreeGenerateResponse {
	query := normalizeQueryString(req.CorrectedQuery)
	if query == "" {
		query = normalizeQueryString(req.Query)
	}
	if query == "" {
		query = "Research Topic"
	}

	domain := strings.ToLower(strings.TrimSpace(req.DetectedDomain))
	if domain == "" {
		domain = strings.ToLower(strings.TrimSpace(req.Domain))
	}
	scope := normalizeTopicTreeScope(req.Scope)
	strategy := normalizeTopicTreeStrategy(req.ExpansionStrategy)
	timeframe := normalizeQueryString(req.Timeframe)
	selectedSubtopics := resolveTopicTreeSubtopics(req)
	userPriorities := normalizeTopicTerms(req.UserPriorities)
	includeTemplateNodes := true
	if req.IncludeTemplateNodes != nil {
		includeTemplateNodes = *req.IncludeTemplateNodes
	}

	maxDepth := req.MaxDepth
	if maxDepth <= 0 {
		maxDepth = 3
	}
	maxNodesPerLevel := req.MaxNodesPerLevel
	if maxNodesPerLevel <= 0 {
		maxNodesPerLevel = 6
	}
	if scope == "focused" {
		maxDepth = wisdev.MinInt(maxDepth, 2)
		maxNodesPerLevel = wisdev.MinInt(maxNodesPerLevel, 4)
	} else if scope == "exhaustive" {
		maxDepth = wisdev.MaxInt(maxDepth, 4)
		maxNodesPerLevel = wisdev.MaxInt(maxNodesPerLevel, 12)
	}
	if strategy == "aggressive" {
		maxNodesPerLevel += 2
	} else if strategy == "conservative" {
		maxNodesPerLevel = wisdev.MaxInt(3, maxNodesPerLevel-2)
	}

	auditTrail := []topicTreeAuditLog{
		{
			Timestamp: time.Now().UTC(),
			Action:    "start_generation",
			Details:   fmt.Sprintf("Started generating tree for domain: %s with scope: %s", domain, scope),
		},
	}

	rootNode := topicTreeNode{
		ID:         topicTreeNodeID(),
		Label:      query,
		Children:   []topicTreeNode{},
		IsSelected: true,
		IsExpanded: true,
		Depth:      0,
		NodeType:   "root",
		Metadata: &topicTreeNodeMetadata{
			Source:      "generated",
			Timeframe:   timeframe,
			Priority:    "high",
			AIReasoning: "Root query derived from user intent",
		},
	}

	treeSubtopics := append([]string{}, selectedSubtopics...)
	if len(treeSubtopics) == 0 {
		treeSubtopics = defaultTopicTreeSubtopics(domain, scope)
	} else if includeTemplateNodes {
		treeSubtopics = mergeTopicTreeStrings(treeSubtopics, defaultTopicTreeSubtopics(domain, scope))
	}

	for _, subtopic := range treeSubtopics {
		subtopicLabel := titleizeTopicPhrase(subtopic)
		if subtopicLabel == "" {
			continue
		}

		childCount := topicTreeChildCount(scope, maxNodesPerLevel, userPriorities, subtopic, strategy)
		childResult := buildTopicTreeChildren(topicTreeChildrenRequest{
			Query:          query,
			Subtopic:       subtopic,
			Count:          childCount,
			Domain:         domain,
			Strategy:       strategy,
			Priorities:     req.UserPriorities,
			ExpertiseLevel: req.ExpertiseLevel,
		}, childCount)

		children := make([]topicTreeNode, 0, len(childResult))
		for _, item := range childResult {
			childPriority := "medium"
			if containsTopicPriority(item.Label, userPriorities) {
				childPriority = "high"
			}
			children = append(children, topicTreeNode{
				ID:         topicTreeNodeID(),
				Label:      item.Label,
				Children:   []topicTreeNode{},
				IsSelected: true,
				IsExpanded: false,
				Depth:      2,
				NodeType:   "subtopic",
				Metadata: &topicTreeNodeMetadata{
					Source:      "generated",
					Priority:    childPriority,
					AIReasoning: item.Reasoning,
				},
			})
		}

		rootNode.Children = append(rootNode.Children, topicTreeNode{
			ID:         topicTreeNodeID(),
			Label:      subtopicLabel,
			Children:   children,
			IsSelected: true,
			IsExpanded: true,
			Depth:      1,
			NodeType:   "category",
			Metadata: &topicTreeNodeMetadata{
				RelevanceScore: topicTreeRelevanceScore(subtopic, userPriorities),
				Source:         "generated",
				Priority:       topicTreePriority(subtopic, userPriorities),
				AIReasoning:    topicTreeSubtopicReasoning(subtopic, userPriorities),
			},
		})

		auditTrail = append(auditTrail, topicTreeAuditLog{
			Timestamp:  time.Now().UTC(),
			Action:     "generate_children",
			Details:    fmt.Sprintf("Generated %d children for %q", len(children), subtopicLabel),
			AIModel:    "Go heuristic",
			Confidence: 0.85,
		})
	}

	if scope == "comprehensive" || scope == "exhaustive" {
		existingLabels := make([]string, 0, len(rootNode.Children))
		for _, child := range rootNode.Children {
			existingLabels = append(existingLabels, child.Label)
		}
		edgeResult := buildTopicTreeEdges(topicTreeEdgesRequest{
			Query:             query,
			ExistingSubtopics: existingLabels,
			Domain:            domain,
		})
		if len(edgeResult.Labels) > 0 {
			edgeChildren := make([]topicTreeNode, 0, len(edgeResult.Labels))
			for _, item := range edgeResult.Labels {
				edgeChildren = append(edgeChildren, topicTreeNode{
					ID:         topicTreeNodeID(),
					Label:      item.Label,
					Children:   []topicTreeNode{},
					IsSelected: true,
					IsExpanded: false,
					Depth:      2,
					NodeType:   "subtopic",
					Metadata: &topicTreeNodeMetadata{
						Source:      "generated",
						Priority:    "medium",
						AIReasoning: item.Reasoning,
					},
				})
			}

			rootNode.Children = append(rootNode.Children, topicTreeNode{
				ID:         topicTreeNodeID(),
				Label:      "Related & Cross-Cutting Topics",
				Children:   edgeChildren,
				IsSelected: true,
				IsExpanded: true,
				Depth:      1,
				NodeType:   "category",
				Metadata: &topicTreeNodeMetadata{
					Source:      "generated",
					Priority:    "medium",
					AIReasoning: edgeResult.Reasoning,
				},
			})

			auditTrail = append(auditTrail, topicTreeAuditLog{
				Timestamp: time.Now().UTC(),
				Action:    "generate_edges",
				Details:   fmt.Sprintf("Generated %d edge topics", len(edgeChildren)),
				AIModel:   "Go heuristic",
			})
		}
	}

	totalNodes := countTopicTreeNodes(rootNode)
	selectedNodes := countSelectedTopicTreeNodes(rootNode)
	auditTrail = append(auditTrail, topicTreeAuditLog{
		Timestamp: time.Now().UTC(),
		Action:    "complete_generation",
		Details:   fmt.Sprintf("Generated tree with %d nodes", totalNodes),
		AIModel:   "Go heuristic",
	})

	return topicTreeGenerateResponse{
		ID:            topicTreeGenerateID(req.SessionID, query),
		Name:          fmt.Sprintf("Topic Tree: %s", truncateTopicTreeName(query, 50)),
		RootNode:      rootNode,
		Domain:        domain,
		Query:         query,
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
		TotalNodes:    totalNodes,
		SelectedNodes: selectedNodes,
		AuditTrail:    auditTrail,
		Strategy:      strategy,
	}
}

func buildTopicTreeChildren(req topicTreeChildrenRequest, count int) []TopicTreeLabel {
	subtopic := titleizeTopicPhrase(req.Subtopic)
	if subtopic == "" {
		subtopic = titleizeTopicPhrase(req.Query)
	}
	if subtopic == "" {
		subtopic = "Research Topic"
	}

	strategy := normalizeTopicTreeStrategy(req.Strategy)
	domain := strings.ToLower(strings.TrimSpace(req.Domain))
	expertise := strings.ToLower(strings.TrimSpace(req.ExpertiseLevel))
	candidateReasons := map[string]string{
		"overview":                "Core background and framing for the selected topic.",
		"methods":                 "Methodological approaches that support the selected topic.",
		"applications":            "Practical uses and downstream applications.",
		"recent advances":         "Recent progress and current state of the art.",
		"challenges":              "Open problems and limitations.",
		"future directions":       "Likely next-step research directions.",
		"theoretical foundations": "Underlying theory and conceptual framing.",
		"implementation details":  "Concrete system or protocol details.",
		"evaluation":              "How results are measured and validated.",
		"benchmarks":              "Comparative baseline and benchmark contexts.",
		"failure modes":           "Known weaknesses and edge-case behavior.",
		"translational impact":    "Path from research to practice or deployment.",
		"ethical considerations":  "Safety, fairness, and responsible-use concerns.",
	}

	patterns := []string{
		"overview",
		"methods",
		"applications",
		"recent advances",
		"challenges",
		"future directions",
	}

	for _, priority := range req.Priorities {
		priority = normalizeQueryString(priority)
		if priority != "" {
			patterns = append([]string{priority}, patterns...)
		}
	}

	switch expertise {
	case "expert":
		patterns = append([]string{"theoretical foundations", "implementation details", "evaluation", "benchmarks"}, patterns...)
	case "intermediate":
		patterns = append([]string{"evaluation"}, patterns...)
	}

	switch strategy {
	case "aggressive":
		patterns = append(patterns, "emerging directions", "failure modes", "translational impact")
	case "conservative":
		patterns = patterns[:wisdev.MinInt(4, len(patterns))]
	}

	if domain == "medicine" {
		patterns = append(patterns, "clinical implications", "patient outcomes", "safety profile")
	} else if domain == "cs" {
		patterns = append(patterns, "scalability", "system architecture", "ablation analysis")
	}

	seen := make(map[string]bool)
	labels := make([]TopicTreeLabel, 0, count)
	for _, suffix := range patterns {
		if len(labels) >= count {
			break
		}
		label := strings.TrimSpace(fmt.Sprintf("%s %s", subtopic, suffix))
		key := strings.ToLower(label)
		if seen[key] {
			continue
		}
		seen[key] = true
		reasoning := candidateReasons[suffix]
		if reasoning == "" {
			reasoning = "Heuristic child topic derived from the selected subtopic."
		}
		labels = append(labels, TopicTreeLabel{
			Label:     label,
			Reasoning: reasoning,
		})
	}

	if len(labels) < count {
		fallback := generateTopicTreeChildFallback(subtopic, count)
		for _, item := range fallback {
			if len(labels) >= count {
				break
			}
			label := strings.TrimSpace(titleizeTopicPhrase(item))
			key := strings.ToLower(label)
			if seen[key] {
				continue
			}
			seen[key] = true
			labels = append(labels, TopicTreeLabel{
				Label:     label,
				Reasoning: "Deterministic fallback pattern.",
			})
		}
	}

	return labels
}

func generateTopicTreeChildFallback(subtopic string, count int) []string {
	patterns := []string{
		"overview",
		"methods",
		"applications",
		"recent advances",
		"challenges",
		"future directions",
	}
	limit := wisdev.MinInt(count, len(patterns))
	results := make([]string, 0, limit)
	for _, suffix := range patterns[:limit] {
		results = append(results, fmt.Sprintf("%s %s", subtopic, suffix))
	}
	return results
}

func buildTopicTreeEdges(req topicTreeEdgesRequest) topicTreeEdgesResponse {
	existing := normalizeTopicSet(req.ExistingSubtopics)
	domain := strings.ToLower(strings.TrimSpace(req.Domain))

	candidates := []TopicTreeLabel{
		{Label: "Cross-Disciplinary Perspectives", Reasoning: "Research angles that cut across current subtopics."},
		{Label: "Methodological Alternatives", Reasoning: "Alternative methods not yet covered by the current tree."},
		{Label: "Historical Foundations", Reasoning: "Background and origin topics that fill context gaps."},
		{Label: "Limitations and Critiques", Reasoning: "Critical perspectives and open weaknesses."},
		{Label: "Emerging Directions", Reasoning: "New areas of exploration beyond the current coverage."},
		{Label: "Data and Evaluation Gaps", Reasoning: "Missing empirical or benchmark coverage."},
		{Label: "Implementation and Deployment", Reasoning: "How the topic is operationalized in practice."},
		{Label: "Ethical and Societal Implications", Reasoning: "Responsible-use and downstream impact topics."},
		{Label: "Reproducibility and Benchmarking", Reasoning: "Validation and comparison coverage gaps."},
		{Label: "Adjacent Domains", Reasoning: "Neighboring fields that could widen the search tree."},
	}

	if domain == "medicine" {
		candidates = append([]TopicTreeLabel{
			{Label: "Clinical Translation", Reasoning: "How the topic moves toward patient care."},
			{Label: "Safety and Efficacy", Reasoning: "Treatment and intervention quality considerations."},
		}, candidates...)
	} else if domain == "cs" {
		candidates = append([]TopicTreeLabel{
			{Label: "Scalability and Efficiency", Reasoning: "Computational cost and throughput concerns."},
			{Label: "Ablation and Baselines", Reasoning: "Comparative evaluation and experimental controls."},
		}, candidates...)
	}

	labels := make([]TopicTreeLabel, 0, 6)
	for _, candidate := range candidates {
		key := strings.ToLower(candidate.Label)
		if existing[key] {
			continue
		}
		labels = append(labels, candidate)
		if len(labels) >= 6 {
			break
		}
	}

	if len(labels) == 0 {
		labels = append(labels, TopicTreeLabel{
			Label:     "Cross-Disciplinary Perspectives",
			Reasoning: "Fallback edge topic when coverage gaps are unclear.",
		})
	}

	return topicTreeEdgesResponse{
		Labels:    labels,
		Reasoning: "Heuristic gap analysis over the current topic coverage.",
	}
}

func buildTopicTreeRefinedQueries(rdb redis.UniversalClient, req topicTreeRefineQueriesRequest) topicTreeRefineQueriesResponse {
	maxQueries := req.MaxQueries
	if maxQueries <= 0 {
		maxQueries = 20
	}
	domain := strings.ToLower(strings.TrimSpace(req.Domain))

	seen := make(map[string]bool)
	queries := make([]string, 0, maxQueries)
	addQuery := func(q string) {
		normalized := normalizeQueryString(q)
		if normalized == "" {
			return
		}
		key := strings.ToLower(normalized)
		if seen[key] || containsExcludedTerm(normalized, req.Exclusions) {
			return
		}
		seen[key] = true
		queries = append(queries, normalized)
	}

	for _, baseline := range req.BaselineQueries {
		addQuery(baseline)
	}

	if len(queries) < maxQueries && strings.TrimSpace(req.Query) != "" {
		query := strings.TrimSpace(req.Query)
		for _, candidate := range buildContextualQueryVariants(query, req) {
			addQuery(candidate)
			if len(queries) >= maxQueries {
				break
			}
		}

		if len(queries) < maxQueries {
			expansion := wisdev.GenerateAggressiveExpansion(
				rdb,
				query,
				wisdev.MaxInt(8, maxQueries*2),
				domain == "medicine",
				true,
				true,
				topicTreeTargetAPIs(domain),
			)
			for _, variation := range expansion.Variations {
				addQuery(variation.Query)
				if len(queries) >= maxQueries {
					break
				}
			}
		}
	}

	if len(queries) == 0 && strings.TrimSpace(req.Query) != "" {
		addQuery(req.Query)
	}

	if len(queries) > maxQueries {
		queries = queries[:maxQueries]
	}

	return topicTreeRefineQueriesResponse{Queries: queries}
}

func buildContextualQueryVariants(query string, req topicTreeRefineQueriesRequest) []string {
	variants := []string{query}
	timeframe := strings.ToLower(strings.TrimSpace(req.Timeframe))
	switch timeframe {
	case "recent":
		variants = append(variants, query+" recent advances")
	case "3years":
		variants = append(variants, query+" last 3 years")
	case "5years":
		variants = append(variants, query+" recent research")
	case "10years":
		variants = append(variants, query+" long-term trends")
	}

	for _, studyType := range req.StudyTypes {
		studyType = strings.TrimSpace(studyType)
		if studyType != "" {
			variants = append(variants, fmt.Sprintf("%s %s", query, studyType))
		}
	}

	switch strings.ToLower(strings.TrimSpace(req.Strategy)) {
	case "aggressive":
		variants = append(variants, query+" emerging directions", query+" comparative analysis")
	case "conservative":
		variants = append(variants, query+" methods", query+" review")
	}

	return variants
}

func containsExcludedTerm(query string, exclusions []string) bool {
	if len(exclusions) == 0 {
		return false
	}
	lower := strings.ToLower(query)
	for _, exclusion := range exclusions {
		exclusion = strings.ToLower(strings.TrimSpace(exclusion))
		if exclusion == "" {
			continue
		}
		if strings.Contains(lower, exclusion) {
			return true
		}
	}
	return false
}

func normalizeQueryString(value string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(value), " "))
}

func normalizeTopicTerms(values []string) []string {
	normalized := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		key := strings.ToLower(strings.TrimSpace(value))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		normalized = append(normalized, key)
	}
	return normalized
}

func topicTreeNodeID() string {
	counter := atomic.AddUint64(&topicTreeNodeCounter, 1)
	return fmt.Sprintf("node_%d_%d", time.Now().UTC().UnixNano(), counter)
}

func resolveTopicTreeSubtopics(req topicTreeGenerateRequest) []string {
	if len(req.SelectedSubtopics) > 0 {
		return req.SelectedSubtopics
	}
	if len(req.Subtopics) > 0 {
		return req.Subtopics
	}
	return nil
}

func containsTopicPriority(label string, priorities []string) bool {
	if len(priorities) == 0 {
		return false
	}
	lower := strings.ToLower(label)
	for _, priority := range priorities {
		if priority != "" && strings.Contains(lower, priority) {
			return true
		}
	}
	return false
}

func topicTreePriority(subtopic string, priorities []string) string {
	if containsTopicPriority(subtopic, priorities) {
		return "high"
	}
	return "medium"
}

func topicTreeSubtopicReasoning(subtopic string, priorities []string) string {
	if containsTopicPriority(subtopic, priorities) {
		return "Explicitly prioritized by user"
	}
	return "Selected from topic tree inputs"
}

func topicTreeRelevanceScore(subtopic string, priorities []string) float64 {
	if containsTopicPriority(subtopic, priorities) {
		return 1.0
	}
	return 0.8
}

func topicTreeChildCount(scope string, maxNodesPerLevel int, priorities []string, subtopic string, strategy string) int {
	childCount := 3
	if scope == "exhaustive" {
		childCount = wisdev.MinInt(5, maxNodesPerLevel)
	} else if maxNodesPerLevel > 0 {
		childCount = wisdev.MinInt(3, maxNodesPerLevel)
	}
	if containsTopicPriority(subtopic, priorities) {
		childCount += 2
	}
	if strategy == "aggressive" {
		childCount += 2
	} else if strategy == "conservative" {
		childCount = wisdev.MaxInt(2, childCount-1)
	}
	if childCount > maxNodesPerLevel {
		childCount = maxNodesPerLevel
	}
	if childCount < 2 {
		childCount = 2
	}
	return childCount
}

func defaultTopicTreeSubtopics(domain string, scope string) []string {
	switch domain {
	case "medicine":
		return []string{"Clinical Applications", "Methods", "Outcomes", "Safety", "Future Directions"}
	case "cs":
		return []string{"Algorithms", "Benchmarks", "Applications", "Systems", "Future Directions"}
	case "biology":
		return []string{"Mechanisms", "Methods", "Data Analysis", "Applications", "Future Directions"}
	case "physics":
		return []string{"Theory", "Methods", "Experiments", "Applications", "Future Directions"}
	default:
		if scope == "focused" {
			return []string{"Background", "Methods", "Applications"}
		}
		return []string{"Background", "Methods", "Applications", "Findings", "Gaps & Future Work"}
	}
}

func mergeTopicTreeStrings(primary []string, secondary []string) []string {
	seen := make(map[string]bool, len(primary)+len(secondary))
	merged := make([]string, 0, len(primary)+len(secondary))
	for _, value := range primary {
		key := strings.ToLower(strings.TrimSpace(value))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		merged = append(merged, value)
	}
	for _, value := range secondary {
		key := strings.ToLower(strings.TrimSpace(value))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		merged = append(merged, value)
	}
	return merged
}

func topicTreeGenerateID(sessionID string, query string) string {
	base := strings.TrimSpace(sessionID)
	if base == "" {
		base = strings.TrimSpace(query)
	}
	if base == "" {
		base = "topic-tree"
	}
	base = strings.ToLower(strings.ReplaceAll(base, " ", "-"))
	return fmt.Sprintf("tree_%s", base)
}

func truncateTopicTreeName(query string, limit int) string {
	query = normalizeQueryString(query)
	if len(query) <= limit {
		return query
	}
	return strings.TrimSpace(query[:limit])
}

func countTopicTreeNodes(node topicTreeNode) int {
	total := 1
	for _, child := range node.Children {
		total += countTopicTreeNodes(child)
	}
	return total
}

func countSelectedTopicTreeNodes(node topicTreeNode) int {
	total := 0
	if node.IsSelected {
		total = 1
	}
	for _, child := range node.Children {
		total += countSelectedTopicTreeNodes(child)
	}
	return total
}

func normalizeTopicSet(values []string) map[string]bool {
	normalized := make(map[string]bool, len(values))
	for _, value := range values {
		key := strings.ToLower(strings.TrimSpace(value))
		if key != "" {
			normalized[key] = true
		}
	}
	return normalized
}

func normalizeTopicTreeStrategy(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "aggressive":
		return "aggressive"
	case "conservative":
		return "conservative"
	default:
		return "balanced"
	}
}

func normalizeTopicTreeScope(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "focused":
		return "focused"
	case "exhaustive":
		return "exhaustive"
	default:
		return "comprehensive"
	}
}

func titleizeTopicPhrase(value string) string {
	value = normalizeQueryString(value)
	if value == "" {
		return ""
	}
	words := strings.Fields(strings.ReplaceAll(value, "_", " "))
	for i, word := range words {
		if word == "" {
			continue
		}
		lower := strings.ToLower(word)
		words[i] = strings.ToUpper(lower[:1]) + lower[1:]
	}
	return strings.Join(words, " ")
}

func resolveSourceProviders(domain string) []string {
	switch strings.ToLower(strings.TrimSpace(domain)) {
	case "medicine":
		return []string{"pubmed", "semanticscholar"}
	case "biology":
		return []string{"pubmed", "semanticscholar", "openalex"}
	case "physics":
		return []string{"arxiv", "openalex", "semanticscholar"}
	default:
		return []string{"semanticscholar", "openalex", "arxiv"}
	}
}

func topicTreeTargetAPIs(domain string) []string {
	return resolveSourceProviders(domain)
}

