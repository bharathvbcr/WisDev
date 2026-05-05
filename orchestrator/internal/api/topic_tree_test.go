package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-redis/redismock/v9"
	"github.com/stretchr/testify/assert"
)

func TestTopicTreeHandler_HandleTopicTreeGenerate(t *testing.T) {
	h := NewTopicTreeHandler(nil)

	reqBody := `{"query": "quantum computing", "domain": "physics"}`
	req := httptest.NewRequest(http.MethodPost, "/topic-tree/generate", bytes.NewBufferString(reqBody))
	w := httptest.NewRecorder()

	h.HandleTopicTreeGenerate(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp topicTreeGenerateResponse
	json.NewDecoder(w.Body).Decode(&resp)
	assert.Equal(t, "physics", resp.Domain)
	assert.Equal(t, "quantum computing", resp.RootNode.Label)
	assert.NotEmpty(t, resp.RootNode.Children)
}

func TestBuildTopicTreeGeneration_UsesEvidenceAndFocusSignals(t *testing.T) {
	resp := buildTopicTreeGeneration(topicTreeGenerateRequest{
		Query:             "LLM evaluation",
		Domain:            "cs",
		SelectedSubtopics: []string{"method comparison"},
		OutputFocus:       "method_comparison",
		EvidenceQuality:   []string{"peer_reviewed"},
		MaxNodesPerLevel:  3,
	})

	assert.NotEmpty(t, resp.RootNode.Children)
	methodNode := resp.RootNode.Children[0]
	assert.Equal(t, "Method Comparison", methodNode.Label)
	assert.NotNil(t, methodNode.Metadata)
	assert.Equal(t, "high", methodNode.Metadata.Priority)
}

func TestTopicTreeHandler_HandleTopicTreeChildren(t *testing.T) {
	h := NewTopicTreeHandler(nil)

	reqBody := `{"query": "quantum computing", "subtopic": "algorithms", "count": 3}`
	req := httptest.NewRequest(http.MethodPost, "/topic-tree/children", bytes.NewBufferString(reqBody))
	w := httptest.NewRecorder()

	h.HandleTopicTreeChildren(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	labels := resp["labels"].([]any)
	assert.Len(t, labels, 3)
}

func TestTopicTreeHandler_HandleTopicTreeRefineQueries(t *testing.T) {
	db, _ := redismock.NewClientMock()
	h := NewTopicTreeHandler(db)

	reqBody := `{"query": "quantum computing", "domain": "physics", "maxQueries": 5}`
	req := httptest.NewRequest(http.MethodPost, "/topic-tree/refine-queries", bytes.NewBufferString(reqBody))
	w := httptest.NewRecorder()

	h.HandleTopicTreeRefineQueries(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp topicTreeRefineQueriesResponse
	json.NewDecoder(w.Body).Decode(&resp)
	assert.NotEmpty(t, resp.Queries)
	assert.LessOrEqual(t, len(resp.Queries), 5)
}

func TestTopicTreeHandler_HandleTopicTreeQueries(t *testing.T) {
	db, _ := redismock.NewClientMock()
	h := NewTopicTreeHandler(db)

	reqBody := `{
		"query": "sleep memory consolidation",
		"domain": "neuro",
		"maxQueries": 6,
		"strategy": "balanced",
		"rootNode": {
			"id": "root",
			"label": "sleep memory consolidation",
			"children": [
				{
					"id": "n1",
					"label": "hippocampal replay",
					"children": [],
					"isSelected": true,
					"isExpanded": true,
					"depth": 1,
					"nodeType": "subtopic"
				}
			],
			"isSelected": true,
			"isExpanded": true,
			"depth": 0,
			"nodeType": "root"
		}
	}`
	req := httptest.NewRequest(http.MethodPost, "/topic-tree/queries", bytes.NewBufferString(reqBody))
	w := httptest.NewRecorder()

	h.HandleTopicTreeQueries(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp topicTreeQueriesResponse
	json.NewDecoder(w.Body).Decode(&resp)
	assert.NotEmpty(t, resp.Queries)
	assert.Equal(t, "heuristic", resp.Source)
	assert.Contains(t, resp.Queries[0], "sleep memory consolidation")
}

func TestTopicTreeHandler_HandleTopicTreeQueries_Refine(t *testing.T) {
	db, _ := redismock.NewClientMock()
	h := NewTopicTreeHandler(db)

	reqBody := `{
		"query": "sleep memory consolidation",
		"domain": "neuro",
		"maxQueries": 6,
		"strategy": "balanced",
		"refine": true,
		"rootNode": {
			"id": "root",
			"label": "sleep memory consolidation",
			"children": [
				{
					"id": "n1",
					"label": "hippocampal replay",
					"children": [],
					"isSelected": true,
					"isExpanded": true,
					"depth": 1,
					"nodeType": "subtopic"
				}
			],
			"isSelected": true,
			"isExpanded": true,
			"depth": 0,
			"nodeType": "root"
		}
	}`
	req := httptest.NewRequest(http.MethodPost, "/topic-tree/queries", bytes.NewBufferString(reqBody))
	w := httptest.NewRecorder()

	h.HandleTopicTreeQueries(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp topicTreeQueriesResponse
	json.NewDecoder(w.Body).Decode(&resp)
	assert.NotEmpty(t, resp.Queries)
	assert.Equal(t, "ai", resp.Source)
	assert.LessOrEqual(t, len(resp.Queries), 6)
}

func TestTopicTree_Helpers(t *testing.T) {
	assert.Equal(t, "Quantum Computing", titleizeTopicPhrase("quantum_computing"))
	assert.Equal(t, "focused", normalizeTopicTreeScope("focused"))
	assert.Equal(t, "aggressive", normalizeTopicTreeStrategy("aggressive"))

	id := topicTreeGenerateID("s1", "query")
	assert.Contains(t, id, "tree_s1")
}

func TestTopicTree_ChildCount(t *testing.T) {
	count := topicTreeChildCount("exhaustive", 10, nil, "sub", "aggressive")
	assert.Equal(t, 7, count)

	count2 := topicTreeChildCount("focused", 4, nil, "sub", "conservative")
	assert.Equal(t, 2, count2)
}

func TestTopicTreeHandler_HandleTopicTreeEdges(t *testing.T) {
	reqBody := `{"query": "quantum computing", "existingSubtopics": ["n1", "n2"]}`
	req := httptest.NewRequest(http.MethodPost, "/topic-tree/edges", bytes.NewBufferString(reqBody))
	w := httptest.NewRecorder()

	handleTopicTreeEdges(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	assert.NotNil(t, resp["labels"])
}

func TestTopicTree_FallbackAndMerge(t *testing.T) {
	t.Run("generateTopicTreeChildFallback", func(t *testing.T) {
		res := generateTopicTreeChildFallback("quantum", 3)
		assert.Len(t, res, 3)
	})

	t.Run("mergeTopicTreeStrings", func(t *testing.T) {
		res := mergeTopicTreeStrings([]string{"a", "b"}, []string{"b", "c"})
		assert.Len(t, res, 3)
		assert.Contains(t, res, "a")
		assert.Contains(t, res, "b")
		assert.Contains(t, res, "c")
	})

	t.Run("containsExcludedTerm", func(t *testing.T) {
		assert.True(t, containsExcludedTerm("test slop", []string{"slop"}))
		assert.False(t, containsExcludedTerm("good query", []string{"slop"}))
	})
}

func TestTopicTree_Priority(t *testing.T) {
	assert.Equal(t, "high", topicTreePriority("Clinical Trial", []string{"clinical trial"}))
	assert.Equal(t, "medium", topicTreePriority("random", nil))
}

func TestTopicTree_QueryNormalizationAvoidsDuplicateSegments(t *testing.T) {
	assert.Equal(t, "Methods", composeTopicTreeChildLabel("Methods", "methods"))
	assert.Equal(t, "RLHF reinforcement learning Background overview", composeTopicTreePathQuery("RLHF reinforcement learning", []string{
		"RLHF reinforcement learning",
		"Background",
		"Background overview",
	}))

	queries := buildTopicTreeBaselineQueries(topicTreeQueriesRequest{
		Query:      "RLHF reinforcement learning",
		MaxQueries: 10,
		RootNode: topicTreeNode{
			ID:         "root",
			Label:      "RLHF reinforcement learning",
			IsSelected: true,
			IsExpanded: true,
			Depth:      0,
			NodeType:   "root",
			Children: []topicTreeNode{
				{
					ID:         "background",
					Label:      "Background",
					IsSelected: true,
					IsExpanded: true,
					Depth:      1,
					NodeType:   "category",
					Children: []topicTreeNode{
						{
							ID:         "overview",
							Label:      "Background overview",
							IsSelected: true,
							IsExpanded: false,
							Depth:      2,
							NodeType:   "subtopic",
						},
					},
				},
			},
		},
	}, 10)

	assert.NotEmpty(t, queries)
	for _, query := range queries {
		assert.NotContains(t, query, "Background Background")
		assert.NotContains(t, query, "Methods methods")
	}
}

func TestTopicTree_StructuralLabelStripping(t *testing.T) {
	// The bug: when all tree leaf nodes have structural labels like "Background",
	// "Methods", "Overview", the heuristic query generator was producing garbage
	// queries like "RLHF reinforcement learning Background Background overview"
	// that return zero results from every search backend.
	//
	// After the fix, structural section-header labels are stripped from path
	// segments so only the root query (and any meaningful leaf concepts) survive.

	rootQuery := "RLHF reinforcement learning"

	// Structural-only tree: all subtopics are generic section headers.
	req := topicTreeQueriesRequest{
		Query:      rootQuery,
		MaxQueries: 20,
		RootNode: topicTreeNode{
			ID:         "root",
			Label:      rootQuery,
			IsSelected: true,
			IsExpanded: true,
			Depth:      0,
			NodeType:   "root",
			Children: []topicTreeNode{
				{
					ID: "bg", Label: "Background", IsSelected: true, IsExpanded: true, Depth: 1, NodeType: "category",
					Children: []topicTreeNode{
						{ID: "bg-ov", Label: "overview", IsSelected: true, Depth: 2, NodeType: "subtopic"},
						{ID: "bg-me", Label: "methods", IsSelected: true, Depth: 2, NodeType: "subtopic"},
						{ID: "bg-ap", Label: "applications", IsSelected: true, Depth: 2, NodeType: "subtopic"},
					},
				},
				{
					ID: "me", Label: "Methods", IsSelected: true, IsExpanded: true, Depth: 1, NodeType: "category",
					Children: []topicTreeNode{
						{ID: "me-ov", Label: "overview", IsSelected: true, Depth: 2, NodeType: "subtopic"},
						{ID: "me-me", Label: "methods", IsSelected: true, Depth: 2, NodeType: "subtopic"},
						{ID: "me-ap", Label: "applications", IsSelected: true, Depth: 2, NodeType: "subtopic"},
					},
				},
			},
		},
	}

	queries := buildTopicTreeBaselineQueries(req, 20)

	// All generated queries should start with the root query.
	for _, q := range queries {
		assert.True(t, len(q) >= len(rootQuery), "query %q is shorter than rootQuery", q)
		assert.Contains(t, strings.ToLower(q), strings.ToLower(rootQuery), "query %q does not contain root query", q)
	}

	// No query should be exactly the root query with ONLY a structural label appended.
	// These are the garbage patterns we saw in production.
	badPatterns := []string{
		rootQuery + " Background",
		rootQuery + " Methods",
		rootQuery + " overview",
		rootQuery + " methods",
		rootQuery + " applications",
		rootQuery + " Background Background overview",
		rootQuery + " Methods Methods methods",
		rootQuery + " Background Background methods",
	}
	for _, bad := range badPatterns {
		for _, q := range queries {
			assert.NotEqual(t, strings.ToLower(q), strings.ToLower(bad),
				"query %q matches forbidden pattern %q", q, bad)
		}
	}

	// The root query itself must always be present as the anchor query.
	assert.Contains(t, queries, rootQuery)

	// isStructuralTopicLabel helper correctness
	assert.True(t, isStructuralTopicLabel("Background"))
	assert.True(t, isStructuralTopicLabel("METHODS"))
	assert.True(t, isStructuralTopicLabel("  overview  "))
	assert.True(t, isStructuralTopicLabel("applications"))
	assert.True(t, isStructuralTopicLabel("review"))
	assert.True(t, isStructuralTopicLabel("comparative analysis"))
	assert.True(t, isStructuralTopicLabel("emerging directions"))
	assert.True(t, isStructuralTopicLabel("recent research"))
	assert.True(t, isStructuralTopicLabel("recent advances"))
	assert.True(t, isStructuralTopicLabel("empirical study"))
	assert.False(t, isStructuralTopicLabel("reward modeling"))
	assert.False(t, isStructuralTopicLabel("policy gradient"))
	assert.False(t, isStructuralTopicLabel("Background overview")) // phrase, not bare label
}

func TestTopicTree_StrategyVariantsNolongerProduceGarbage(t *testing.T) {
	// topicTreeQueryStrategyVariants previously appended structural suffixes
	// ("overview", "review", "emerging directions", "comparative analysis").
	// After the fix it returns just the base query, letting the AI refine path
	// generate semantic variants instead.
	base := "transformer attention mechanisms"
	for _, strategy := range []string{"aggressive", "conservative", "balanced", "default"} {
		variants := topicTreeQueryStrategyVariants(base, strategy)
		assert.Equal(t, []string{base}, variants,
			"strategy=%q should return only the base query", strategy)
	}
}

func TestTopicTree_ContextualVariantsSkipStructuralStudyTypes(t *testing.T) {
	// buildContextualQueryVariants should skip study types that are structural
	// labels ("review", "comparative analysis", "empirical study").
	req := topicTreeRefineQueriesRequest{
		Query:      "deep learning",
		StudyTypes: []string{"review", "comparative analysis", "randomized trial", "benchmark"},
	}
	variants := buildContextualQueryVariants("deep learning", req)

	// "review" and "comparative analysis" should be excluded.
	for _, v := range variants {
		assert.NotEqual(t, "deep learning review", v, "structural study type 'review' should be stripped")
		assert.NotEqual(t, "deep learning comparative analysis", v, "structural study type 'comparative analysis' should be stripped")
		assert.NotEqual(t, "deep learning empirical study", v, "structural study type 'empirical study' should be stripped")
	}

	// Meaningful study types should still produce query variants.
	assert.Contains(t, variants, "deep learning randomized trial")
	assert.Contains(t, variants, "deep learning benchmark")
}

func TestTopicTree_BaselineQueriesSkipStructuralStudyTypes(t *testing.T) {
	// buildTopicTreeBaselineQueries should skip structural labels in StudyTypes.
	req := topicTreeQueriesRequest{
		Query:      "CRISPR gene editing",
		MaxQueries: 20,
		StudyTypes: []string{"review", "systematic review", "randomized trial"},
		RootNode: topicTreeNode{
			ID:         "root",
			Label:      "CRISPR gene editing",
			IsSelected: true,
			Depth:      0,
			NodeType:   "root",
		},
	}

	queries := buildTopicTreeBaselineQueries(req, 20)

	for _, q := range queries {
		assert.NotEqual(t, "CRISPR gene editing review", q,
			"structural study type 'review' should be stripped from query")
	}
	// "systematic review" is NOT in structuralTopicLabels (it's a specific study design phrase).
	assert.Contains(t, queries, "CRISPR gene editing systematic review")
	assert.Contains(t, queries, "CRISPR gene editing randomized trial")
}

func TestTopicTree_BaselineQueriesUseEvidenceAndOutputFocus(t *testing.T) {
	req := topicTreeQueriesRequest{
		Query:           "LLM evaluation",
		MaxQueries:      10,
		EvidenceQuality: []string{"peer_reviewed", "methods_transparency"},
		OutputFocus:     "method_comparison",
		RootNode: topicTreeNode{
			ID:         "root",
			Label:      "LLM evaluation",
			IsSelected: true,
			Depth:      0,
			NodeType:   "root",
			Children: []topicTreeNode{
				{
					ID:         "reward-modeling",
					Label:      "reward modeling",
					IsSelected: true,
					Depth:      1,
					NodeType:   "subtopic",
				},
			},
		},
	}

	queries := buildTopicTreeBaselineQueries(req, 10)

	assert.Contains(t, queries, "LLM evaluation peer reviewed")
	assert.Contains(t, queries, "LLM evaluation methods transparency")
	assert.Contains(t, queries, "LLM evaluation method comparison")
	assert.Contains(t, queries, "LLM evaluation reward modeling method comparison")
}

func TestTopicTree_ContextualVariantsUseEvidenceAndOutputFocus(t *testing.T) {
	req := topicTreeRefineQueriesRequest{
		Query:           "LLM evaluation",
		EvidenceQuality: []string{"peer_reviewed"},
		OutputFocus:     "evidence_gaps",
	}

	variants := buildContextualQueryVariants("LLM evaluation", req)

	assert.Contains(t, variants, "LLM evaluation peer reviewed")
	assert.Contains(t, variants, "LLM evaluation evidence gaps")
}

func TestTopicTree_TimeframeVariantsUseYearRanges(t *testing.T) {
	// topicTreeTimeframeQuerySuffix should return explicit year ranges for
	// "recent" instead of the structural phrase "recent research".
	assert.Equal(t, "last 5 years", topicTreeTimeframeQuerySuffix("recent"))
	assert.Equal(t, "last 3 years", topicTreeTimeframeQuerySuffix("3years"))
	assert.Equal(t, "last 10 years", topicTreeTimeframeQuerySuffix("10years"))
	assert.Equal(t, "", topicTreeTimeframeQuerySuffix("alltime"))
	assert.Equal(t, "", topicTreeTimeframeQuerySuffix(""))
}
