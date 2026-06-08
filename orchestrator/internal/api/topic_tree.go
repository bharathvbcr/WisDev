package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/telemetry"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
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
	EvidenceQuality      []string `json:"evidenceQuality,omitempty"`
	OutputFocus          string   `json:"outputFocus,omitempty"`
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
	EvidenceQuality []string `json:"evidenceQuality,omitempty"`
	OutputFocus     string   `json:"outputFocus,omitempty"`
	Strategy        string   `json:"strategy,omitempty"`
}

type topicTreeQueriesRequest struct {
	Query           string        `json:"query"`
	Domain          string        `json:"domain,omitempty"`
	RootNode        topicTreeNode `json:"rootNode"`
	MaxQueries      int           `json:"maxQueries"`
	Timeframe       string        `json:"timeframe,omitempty"`
	StudyTypes      []string      `json:"studyTypes,omitempty"`
	Exclusions      []string      `json:"exclusions,omitempty"`
	EvidenceQuality []string      `json:"evidenceQuality,omitempty"`
	OutputFocus     string        `json:"outputFocus,omitempty"`
	EnableSynonyms  bool          `json:"enableSynonyms,omitempty"`
	Strategy        string        `json:"strategy,omitempty"`
	Refine          bool          `json:"refine,omitempty"`
}

type topicTreeEdgesResponse struct {
	Labels    []TopicTreeLabel `json:"labels"`
	Reasoning string           `json:"reasoning"`
}

type topicTreeRefineQueriesResponse struct {
	Queries []string `json:"queries"`
}

type topicTreeQueriesResponse struct {
	Queries []string `json:"queries"`
	Source  string   `json:"source"`
}

type TopicTreeHandler struct {
	rdb          redis.UniversalClient
	intelligence *search.SearchIntelligence
}

func NewTopicTreeHandler(rdb redis.UniversalClient, intelligence ...*search.SearchIntelligence) *TopicTreeHandler {
	var si *search.SearchIntelligence
	if len(intelligence) > 0 {
		si = intelligence[0]
	}
	return &TopicTreeHandler{rdb: rdb, intelligence: si}
}

func logTopicTreeLifecycle(r *http.Request, operation string, stage string, query string, attrs ...any) {
	base := []any{
		"service", "go_orchestrator",
		"runtime", "go",
		"component", "api.topic_tree",
		"operation", operation,
		"stage", stage,
		"path", r.URL.Path,
		"method", r.Method,
		"user_id", strings.TrimSpace(GetUserID(r)),
		"query_preview", wisdev.QueryPreview(query),
		"query_length", len(normalizeQueryString(query)),
		"query_hash", searchQueryFingerprint(query),
	}
	telemetry.FromCtx(r.Context()).InfoContext(r.Context(), "topic tree lifecycle", append(base, attrs...)...)
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

	result := buildTopicTreeRefinedQueries(r.Context(), h.rdb, h.intelligence, req)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func (h *TopicTreeHandler) HandleTopicTreeQueries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "Method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	req, err := decodeTopicTreeQueriesRequest(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "Failed to parse request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	query := normalizeQueryString(req.Query)
	if query == "" {
		query = normalizeQueryString(req.RootNode.Label)
	}
	if query == "" {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "Topic-tree query generation requires a query or root node label", map[string]any{
			"field": "query",
		})
		return
	}
	req.Query = query

	logTopicTreeLifecycle(r, "topic_tree_queries", "request_received", query,
		"max_queries", req.MaxQueries,
		"selected_node_count", countSelectedTopicTreeNodes(req.RootNode),
		"strategy", normalizeTopicTreeStrategy(req.Strategy),
		"refine", req.Refine,
	)

	result := buildTopicTreeQueries(r.Context(), h.rdb, h.intelligence, req)

	logTopicTreeLifecycle(r, "topic_tree_queries", "response_ready", query,
		"max_queries", req.MaxQueries,
		"selected_node_count", countSelectedTopicTreeNodes(req.RootNode),
		"query_count", len(result.Queries),
		"source", result.Source,
		"refine", req.Refine,
	)

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

func decodeTopicTreeQueriesRequest(r *http.Request) (topicTreeQueriesRequest, error) {
	var req topicTreeQueriesRequest
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
	prioritySignals := append([]string{}, req.UserPriorities...)
	prioritySignals = append(prioritySignals, req.EvidenceQuality...)
	if strings.TrimSpace(req.OutputFocus) != "" {
		prioritySignals = append(prioritySignals, req.OutputFocus)
	}
	userPriorities := normalizeTopicTerms(prioritySignals)
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
			Priorities:     prioritySignals,
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
		label := composeTopicTreeChildLabel(subtopic, suffix)
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

func buildTopicTreeRefinedQueries(ctx context.Context, rdb redis.UniversalClient, intelligence *search.SearchIntelligence, req topicTreeRefineQueriesRequest) topicTreeRefineQueriesResponse {
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
			expansion := wisdev.GenerateAdaptiveExpansion(
				ctx,
				intelligence,
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

func buildTopicTreeQueries(ctx context.Context, rdb redis.UniversalClient, intelligence *search.SearchIntelligence, req topicTreeQueriesRequest) topicTreeQueriesResponse {
	maxQueries := req.MaxQueries
	if maxQueries <= 0 {
		maxQueries = 20
	}

	baselineQueries := buildTopicTreeBaselineQueries(req, maxQueries)
	if req.Refine {
		refined := buildTopicTreeRefinedQueries(ctx, rdb, intelligence, topicTreeRefineQueriesRequest{
			Query:           req.Query,
			Domain:          req.Domain,
			BaselineQueries: baselineQueries,
			MaxQueries:      maxQueries,
			Timeframe:       req.Timeframe,
			StudyTypes:      req.StudyTypes,
			Exclusions:      req.Exclusions,
			EvidenceQuality: req.EvidenceQuality,
			OutputFocus:     req.OutputFocus,
			Strategy:        req.Strategy,
		})
		if len(refined.Queries) > 0 {
			return topicTreeQueriesResponse{
				Queries: refined.Queries,
				Source:  "ai",
			}
		}
	}

	return topicTreeQueriesResponse{
		Queries: baselineQueries,
		Source:  "heuristic",
	}
}

func buildTopicTreeBaselineQueries(req topicTreeQueriesRequest, maxQueries int) []string {
	rootQuery := normalizeQueryString(req.Query)
	if rootQuery == "" {
		rootQuery = normalizeQueryString(req.RootNode.Label)
	}
	if rootQuery == "" {
		return nil
	}

	seen := make(map[string]bool, maxQueries)
	queries := make([]string, 0, maxQueries)
	addQuery := func(candidate string) {
		normalized := normalizeQueryString(candidate)
		if normalized == "" || containsExcludedTerm(normalized, req.Exclusions) {
			return
		}
		key := strings.ToLower(normalized)
		if seen[key] {
			return
		}
		seen[key] = true
		queries = append(queries, normalized)
	}
	addQueryVariants := func(base string) {
		if len(queries) >= maxQueries {
			return
		}
		addQuery(base)
		if len(queries) >= maxQueries {
			return
		}

		if timeframeSuffix := topicTreeTimeframeQuerySuffix(req.Timeframe); timeframeSuffix != "" {
			addQuery(fmt.Sprintf("%s %s", base, timeframeSuffix))
			if len(queries) >= maxQueries {
				return
			}
		}

		for _, studyType := range req.StudyTypes {
			studyType = normalizePlanningAnswerTerm(studyType)
			if studyType == "" || isStructuralTopicLabel(studyType) {
				// Skip structural labels ("review", "comparative analysis",
				// "empirical study", etc.) — they produce zero-result queries.
				continue
			}
			addQuery(fmt.Sprintf("%s %s", base, studyType))
			if len(queries) >= maxQueries {
				return
			}
		}

		for _, planningTerm := range topicTreePlanningTerms(req.EvidenceQuality, req.OutputFocus) {
			addQuery(fmt.Sprintf("%s %s", base, planningTerm))
			if len(queries) >= maxQueries {
				return
			}
		}

		if req.EnableSynonyms {
			for _, variant := range topicTreeQueryStrategyVariants(base, req.Strategy) {
				addQuery(variant)
				if len(queries) >= maxQueries {
					return
				}
			}
		}
	}

	addQueryVariants(rootQuery)

	for _, path := range collectSelectedTopicTreePaths(req.RootNode, nil) {
		if len(queries) >= maxQueries {
			break
		}
		candidate := composeTopicTreePathQuery(rootQuery, path)
		if candidate == "" {
			continue
		}
		addQueryVariants(candidate)
	}

	if len(queries) > maxQueries {
		return queries[:maxQueries]
	}
	return queries
}

func collectSelectedTopicTreePaths(node topicTreeNode, ancestors []string) [][]string {
	label := normalizeQueryString(node.Label)
	currentPath := append([]string{}, ancestors...)
	if label != "" {
		currentPath = append(currentPath, label)
	}

	paths := make([][]string, 0)
	if node.IsSelected && len(currentPath) > 0 {
		paths = append(paths, currentPath)
	}

	for _, child := range node.Children {
		paths = append(paths, collectSelectedTopicTreePaths(child, currentPath)...)
	}
	return paths
}

// structuralTopicLabels is the set of generic section-header and hollow-suffix
// labels that add no semantic search value when appended to a query string.
//
// Rules:
//   - Bare structural words produce garbage queries like
//     "RLHF reinforcement learning Background" that match nothing in academic
//     databases.
//   - Any value in this set that appears as a raw path segment or strategy
//     suffix is either stripped (in composeTopicTreePathQuery) or skipped
//     (in buildTopicTreeBaselineQueries study-type loop) before building the
//     final query string.
//   - "review" is included because appending it produces filter-like phrasing
//     ("X review") but these queries consistently return fewer relevant results
//     than the bare query; domain-specific review filtering should use the
//     studyType mechanism instead.
var structuralTopicLabels = map[string]bool{
	"background":            true,
	"methods":               true,
	"applications":          true,
	"findings":              true,
	"gaps & future work":    true,
	"future directions":     true,
	"clinical applications": true,
	"outcomes":              true,
	"safety":                true,
	"algorithms":            true,
	"benchmarks":            true,
	"systems":               true,
	"mechanisms":            true,
	"data analysis":         true,
	"theory":                true,
	"experiments":           true,
	"overview":              true,
	"introduction":          true,
	"discussion":            true,
	"results":               true,
	"conclusion":            true,
	// Additional hollow suffixes produced by strategy-variant helpers.
	"review":               true,
	"comparative analysis": true,
	"emerging directions":  true,
	"recent research":      true,
	"long-term trends":     true,
	"recent advances":      true,
	"empirical study":      true,
}

func isStructuralTopicLabel(s string) bool {
	return structuralTopicLabels[strings.ToLower(strings.TrimSpace(s))]
}

func composeTopicTreePathQuery(rootQuery string, path []string) string {
	rootQuery = normalizeQueryString(rootQuery)
	if rootQuery == "" {
		return ""
	}

	segments := make([]string, 0, len(path))
	for _, segment := range path {
		normalized := normalizeQueryString(segment)
		if normalized == "" {
			continue
		}
		if strings.EqualFold(normalized, rootQuery) {
			continue
		}
		// Strip structural section-header labels that add no search value.
		// e.g. "Background", "Methods", "Overview" produce garbage queries
		// like "RLHF reinforcement learning Background" that match nothing.
		if isStructuralTopicLabel(normalized) {
			continue
		}
		segments = append(segments, normalized)
	}

	segments = collapseTopicTreePathSegments(segments)
	if len(segments) == 0 {
		return rootQuery
	}

	return fmt.Sprintf("%s %s", rootQuery, strings.Join(segments, " "))
}

func composeTopicTreeChildLabel(subtopic string, suffix string) string {
	subtopic = normalizeQueryString(subtopic)
	suffix = normalizeQueryString(suffix)
	switch {
	case subtopic == "":
		return suffix
	case suffix == "":
		return subtopic
	case strings.EqualFold(subtopic, suffix):
		return subtopic
	case strings.HasPrefix(strings.ToLower(suffix), strings.ToLower(subtopic+" ")):
		return suffix
	default:
		return strings.TrimSpace(fmt.Sprintf("%s %s", subtopic, suffix))
	}
}

func collapseTopicTreePathSegments(segments []string) []string {
	if len(segments) <= 1 {
		return segments
	}

	collapsed := make([]string, 0, len(segments))
	for _, segment := range segments {
		if len(collapsed) == 0 {
			collapsed = append(collapsed, segment)
			continue
		}

		last := collapsed[len(collapsed)-1]
		switch {
		case strings.EqualFold(last, segment):
			continue
		case strings.HasPrefix(strings.ToLower(segment), strings.ToLower(last+" ")):
			collapsed[len(collapsed)-1] = segment
		default:
			collapsed = append(collapsed, segment)
		}
	}

	return collapsed
}

func topicTreeTimeframeQuerySuffix(timeframe string) string {
	switch strings.ToLower(strings.TrimSpace(timeframe)) {
	case "1year":
		return "last year"
	case "3years":
		return "last 3 years"
	case "5years":
		return "last 5 years"
	case "10years":
		return "last 10 years"
	case "recent":
		// "recent research" and "recent advances" were moved to structuralTopicLabels.
		// Use the explicit year-range form instead.
		return "last 5 years"
	case "alltime":
		return ""
	default:
		return ""
	}
}

func topicTreeQueryStrategyVariants(base string, strategy string) []string {
	// Previously this appended structural hollow suffixes like "overview",
	// "review", "comparative analysis", and "emerging directions" to the base
	// query. Those phrases all landed in structuralTopicLabels and produced
	// zero-result queries in academic databases. The meaningful strategy-aware
	// variants are now generated by the AI expansion path (GenerateAdaptiveExpansion)
	// when refine:true is requested. Returning just the base here avoids producing
	// garbage while keeping the call site's deduplication logic intact.
	_ = strategy // strategy is handled upstream by the AI refine path
	return []string{base}
}

func buildContextualQueryVariants(query string, req topicTreeRefineQueriesRequest) []string {
	variants := []string{query}
	timeframe := strings.ToLower(strings.TrimSpace(req.Timeframe))
	switch timeframe {
	case "recent":
		// "recent advances" is now in structuralTopicLabels; use an explicit
		// year-range instead which academic databases understand correctly.
		variants = append(variants, query+" last 5 years")
	case "3years":
		variants = append(variants, query+" last 3 years")
	case "5years":
		variants = append(variants, query+" last 5 years")
	case "10years":
		variants = append(variants, query+" last 10 years")
	}

	for _, studyType := range req.StudyTypes {
		studyType = normalizePlanningAnswerTerm(studyType)
		// Skip study types that are structural labels — they produce zero-result
		// queries of the form "X review" or "X empirical study".
		if studyType == "" || isStructuralTopicLabel(studyType) {
			continue
		}
		variants = append(variants, fmt.Sprintf("%s %s", query, studyType))
	}

	for _, planningTerm := range topicTreePlanningTerms(req.EvidenceQuality, req.OutputFocus) {
		variants = append(variants, fmt.Sprintf("%s %s", query, planningTerm))
	}

	// Strategy-based suffix appending was removed.
	// "X methods", "X review", "X emerging directions", and
	// "X comparative analysis" are all structural phrases in
	// structuralTopicLabels that produce near-zero results. The AI expansion
	// path (GenerateAdaptiveExpansion) produces strategy-aware semantic
	// variants instead when refine:true is requested.

	return variants
}

func containsExcludedTerm(query string, exclusions []string) bool {
	if len(exclusions) == 0 {
		return false
	}
	lower := strings.ToLower(query)
	for _, exclusion := range exclusions {
		exclusion = strings.ToLower(normalizePlanningAnswerTerm(exclusion))
		if exclusion == "" || exclusion == "none" || exclusion == "no exclusions" {
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

func normalizePlanningAnswerTerm(value string) string {
	return normalizeQueryString(strings.ReplaceAll(value, "_", " "))
}

func topicTreePlanningTerms(evidenceQuality []string, outputFocus string) []string {
	return normalizeTopicTerms(append(append([]string{}, evidenceQuality...), outputFocus))
}

func normalizeTopicTerms(values []string) []string {
	normalized := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		key := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(value, "_", " ")))
		key = strings.Join(strings.Fields(key), " ")
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
