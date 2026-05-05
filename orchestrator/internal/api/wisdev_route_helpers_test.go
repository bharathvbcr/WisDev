package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

type staticStructuredLLMClient struct {
	structuredResp *llmv1.StructuredResponse
	structuredErr  error
}

func (c *staticStructuredLLMClient) Generate(context.Context, *llmv1.GenerateRequest, ...grpc.CallOption) (*llmv1.GenerateResponse, error) {
	return nil, nil
}

func (c *staticStructuredLLMClient) GenerateStream(context.Context, *llmv1.GenerateRequest, ...grpc.CallOption) (llmv1.LLMService_GenerateStreamClient, error) {
	return nil, nil
}

func (c *staticStructuredLLMClient) StructuredOutput(context.Context, *llmv1.StructuredRequest, ...grpc.CallOption) (*llmv1.StructuredResponse, error) {
	return c.structuredResp, c.structuredErr
}

func (c *staticStructuredLLMClient) Embed(context.Context, *llmv1.EmbedRequest, ...grpc.CallOption) (*llmv1.EmbedResponse, error) {
	return nil, nil
}

func (c *staticStructuredLLMClient) EmbedBatch(context.Context, *llmv1.EmbedBatchRequest, ...grpc.CallOption) (*llmv1.EmbedBatchResponse, error) {
	return nil, nil
}

func (c *staticStructuredLLMClient) Health(context.Context, *llmv1.HealthRequest, ...grpc.CallOption) (*llmv1.HealthResponse, error) {
	return nil, nil
}

func TestWisDev_MathHelpers(t *testing.T) {
	t.Run("resolveOperationMode", func(t *testing.T) {
		assert.Equal(t, "yolo", resolveOperationMode("YOLO"))
		assert.Equal(t, "guided", resolveOperationMode("something"))
		assert.Equal(t, "guided", resolveOperationMode(""))
	})
}

func TestClassifyWisdevResearchLoopError(t *testing.T) {
	rateLimited := classifyWisdevResearchLoopError(errors.New("generate structured content failed: Error 429, Message: Resource exhausted"))
	assert.Equal(t, http.StatusTooManyRequests, rateLimited.status)
	assert.Equal(t, ErrRateLimit, rateLimited.code)
	assert.Equal(t, "rate_limit", rateLimited.kind)
	assert.True(t, rateLimited.retryable)

	timedOut := classifyWisdevResearchLoopError(context.DeadlineExceeded)
	assert.Equal(t, http.StatusGatewayTimeout, timedOut.status)
	assert.Equal(t, ErrDependencyFailed, timedOut.code)
	assert.Equal(t, "timeout", timedOut.kind)
	assert.True(t, timedOut.retryable)

	cancelled := classifyWisdevResearchLoopError(context.Canceled)
	assert.Equal(t, 499, cancelled.status)
	assert.Equal(t, ErrServiceUnavailable, cancelled.code)
	assert.Equal(t, "context_canceled", cancelled.kind)
	assert.False(t, cancelled.retryable)

	generic := classifyWisdevResearchLoopError(errors.New("loop execution failed"))
	assert.Equal(t, http.StatusInternalServerError, generic.status)
	assert.Equal(t, ErrWisdevFailed, generic.code)
	assert.Equal(t, "runtime_failure", generic.kind)
	assert.False(t, generic.retryable)
}

func TestWisDev_CommitteeHelpers(t *testing.T) {
	papers := []wisdev.Source{
		{ID: "p1", Title: "Title 1", Score: 0.9},
		{ID: "p2", Title: "Title 2", Score: 0.8},
	}

	t.Run("buildCommitteeAnswer", func(t *testing.T) {
		ans := buildCommitteeAnswer("q", papers)
		assert.Contains(t, ans, "Title 1")
		assert.Contains(t, ans, "Title 2")

		ansEmpty := buildCommitteeAnswer("q", nil)
		assert.Contains(t, ansEmpty, "No committee evidence")

		ansNoTitle := buildCommitteeAnswer("q", []wisdev.Source{{ID: "1"}})
		assert.Contains(t, ansNoTitle, "Committee review completed")
	})

	t.Run("buildCommitteeCitations", func(t *testing.T) {
		citations := buildCommitteeCitations(papers)
		assert.Len(t, citations, 2)
		assert.Equal(t, "p1", citations[0]["sourceId"])

		papersNoID := []wisdev.Source{{Title: "T1", DOI: "D1"}}
		c2 := buildCommitteeCitations(papersNoID)
		assert.Equal(t, "D1", c2[0]["sourceId"])
	})

	t.Run("buildCommitteePapers", func(t *testing.T) {
		mapped := buildCommitteePapers(papers)
		assert.Len(t, mapped, 2)
		assert.Equal(t, "p1", mapped[0]["id"])
	})

	t.Run("buildMultiAgentCommitteeResult", func(t *testing.T) {
		res := buildMultiAgentCommitteeResult("q", "cs", papers, 5, true)
		assert.True(t, res["success"].(bool))
		assert.Equal(t, "go_committee", res["mode"])
		assert.NotEmpty(t, res["analyst"])
	})
}

func TestWisDev_GateHelpers(t *testing.T) {
	t.Run("extractCommitteeSignals", func(t *testing.T) {
		meta := map[string]any{
			"multiAgent": map[string]any{
				"critic": map[string]any{
					"citationCount": 5.0,
					"decision":      "accept",
				},
				"supervisor": map[string]any{
					"sourceCount": 10.0,
				},
			},
		}
		cc, sc, dec := extractCommitteeSignals(meta)
		assert.Equal(t, 5, cc)
		assert.Equal(t, 10, sc)
		assert.Equal(t, "accept", dec)

		_, _, decEmpty := extractCommitteeSignals(nil)
		assert.Empty(t, decEmpty)
	})

	t.Run("buildEvidenceGatePayload", func(t *testing.T) {
		claims := []map[string]any{
			{"source": map[string]any{"id": "1"}},
			{"source": nil},
		}
		res := buildEvidenceGatePayload(claims, 0)
		assert.False(t, res["passed"].(bool))
		assert.True(t, res["provisional"].(bool))

		res2 := buildEvidenceGatePayload(nil, 0)
		assert.True(t, res2["passed"].(bool))
	})
}

func TestWisDev_DraftHelpers(t *testing.T) {
	t.Run("normalizeSectionID", func(t *testing.T) {
		assert.Equal(t, "intro_section", normalizeSectionID("Intro Section "))
	})

	t.Run("uniqueStrings", func(t *testing.T) {
		assert.Equal(t, []string{"a", "b"}, uniqueStrings([]string{" a", "b", "a "}))
	})

	t.Run("inferDraftSections", func(t *testing.T) {
		assert.Contains(t, inferDraftSections("Survey of AI", nil), "Landscape")
		assert.Contains(t, inferDraftSections("Benchmark of models", nil), "Comparative Findings")
		assert.Contains(t, inferDraftSections("Architecture of X", nil), "Operational Risks")
	})

	t.Run("buildDraftOutlinePayload", func(t *testing.T) {
		res := buildDraftOutlinePayload("d1", "Title", 1000, []string{"Custom"})
		assert.Equal(t, "d1", res["documentId"])
		items := res["items"].([]map[string]any)
		assert.NotEmpty(t, items)
	})

	t.Run("buildDraftSectionPayload", func(t *testing.T) {
		papers := []map[string]any{
			{"title": "T1", "summary": "S1", "score": 0.9},
		}
		res := buildDraftSectionPayload("d1", "s1", "Title", 200, papers)
		assert.Equal(t, "d1", res["documentId"])
		assert.Contains(t, res["content"].(string), "S1")
	})
}

func TestWisDev_MapHelpers(t *testing.T) {
	t.Run("mapAny", func(t *testing.T) {
		assert.Empty(t, mapAny(nil))
		assert.NotEmpty(t, mapAny(map[string]any{"a": 1}))
	})

	t.Run("mergeAnyMap", func(t *testing.T) {
		base := map[string]any{"a": 1, "b": map[string]any{"c": 2}}
		override := map[string]any{"b": map[string]any{"c": 3, "d": 4}}
		merged := mergeAnyMap(base, override)
		b := merged["b"].(map[string]any)
		assert.Equal(t, 3, b["c"])
		assert.Equal(t, 4, b["d"])
	})

	t.Run("sliceAnyMap", func(t *testing.T) {
		assert.Empty(t, sliceAnyMap(nil))
		in := []any{map[string]any{"a": 1}}
		assert.Len(t, sliceAnyMap(in), 1)
	})

	t.Run("sliceStrings", func(t *testing.T) {
		assert.Empty(t, sliceStrings(nil))
		in := []any{"a", "b"}
		assert.Equal(t, []string{"a", "b"}, sliceStrings(in))
	})

	t.Run("questionOptionPayloads", func(t *testing.T) {
		options := questionOptionPayloads([]any{
			"medicine",
			map[string]any{"value": "cs", "label": "Computer Science & AI"},
		})
		if assert.Len(t, options, 2) {
			assert.Equal(t, "medicine", options[0]["value"])
			assert.Equal(t, "medicine", options[0]["label"])
			assert.Equal(t, "cs", options[1]["value"])
			assert.Equal(t, "Computer Science & AI", options[1]["label"])
		}
		assert.Equal(t, []string{"medicine", "cs"}, questionOptionValues(options))
	})
}

func TestWisDev_AgentHelpers(t *testing.T) {
	t.Run("defaultAgentQuestionSequence", func(t *testing.T) {
		questions := defaultAgentQuestionSequence("systematic review of sleep interventions and memory outcomes in adults", "medicine")
		if assert.NotEmpty(t, questions) {
			assert.Equal(t, "q1_domain", questions[0]["id"])
			assert.Equal(t, "domain", questions[0]["type"])
			assert.NotEmpty(t, questions[0]["question"])
			options := questionOptionPayloads(questions[0]["options"])
			if assert.NotEmpty(t, options) {
				assert.NotEmpty(t, options[0]["value"])
				assert.NotEmpty(t, options[0]["label"])
			}
		}
	})

	t.Run("defaultAgentQuestionPlan uses six-question baseline", func(t *testing.T) {
		questions, sequence, minQuestions, maxQuestions := defaultAgentQuestionPlan(
			"sleep interventions adults",
			"social",
			nil,
		)
		require.NotEmpty(t, questions)
		assert.Equal(t, []string{"q1_domain", "q2_scope", "q3_timeframe", "q4_subtopics", "q5_study_types", "q6_exclusions"}, sequence)
		assert.Equal(t, 6, minQuestions)
		assert.Equal(t, 6, maxQuestions)
	})

	t.Run("makeAgentAnswerIdempotencyKey changes when answer payload changes", func(t *testing.T) {
		first := makeAgentAnswerIdempotencyKey(
			"session-1",
			"q1_domain",
			[]string{"biology"},
			[]string{"Biology"},
			false,
			0,
		)
		repeated := makeAgentAnswerIdempotencyKey(
			"session-1",
			"q1_domain",
			[]string{"biology"},
			[]string{"Biology"},
			false,
			0,
		)
		changed := makeAgentAnswerIdempotencyKey(
			"session-1",
			"q1_domain",
			[]string{"medicine"},
			[]string{"Medicine"},
			false,
			0,
		)
		versioned := makeAgentAnswerIdempotencyKey(
			"session-1",
			"q1_domain",
			[]string{"biology"},
			[]string{"Biology"},
			false,
			123,
		)

		assert.Equal(t, first, repeated)
		assert.NotEqual(t, first, changed)
		assert.NotEqual(t, first, versioned)
	})

	t.Run("makeAgentAnswerIdempotencyKey normalizes multi-select ordering", func(t *testing.T) {
		first := makeAgentAnswerIdempotencyKey(
			"session-1",
			"q1_domain",
			[]string{"biology", "medicine"},
			[]string{"Biology", "Medicine"},
			false,
			321,
		)
		reordered := makeAgentAnswerIdempotencyKey(
			"session-1",
			"q1_domain",
			[]string{"medicine", "biology"},
			[]string{"Medicine", "Biology"},
			false,
			321,
		)

		assert.Equal(t, first, reordered)
	})

	t.Run("drafting idempotency keys include payload and expected version", func(t *testing.T) {
		outline := makeDraftOutlineIdempotencyKey(
			"doc-1",
			"Sleep Review",
			1200,
			[]string{"Methods", "Results"},
			777,
		)
		repeatedOutline := makeDraftOutlineIdempotencyKey(
			"doc-1",
			"Sleep Review",
			1200,
			[]string{"Results", "Methods"},
			777,
		)
		changedOutline := makeDraftOutlineIdempotencyKey(
			"doc-1",
			"Sleep Review Revised",
			1200,
			[]string{"Methods", "Results"},
			777,
		)
		versionedOutline := makeDraftOutlineIdempotencyKey(
			"doc-1",
			"Sleep Review",
			1200,
			[]string{"Methods", "Results"},
			778,
		)

		assert.Equal(t, outline, repeatedOutline)
		assert.NotEqual(t, outline, changedOutline)
		assert.NotEqual(t, outline, versionedOutline)

		papers := []map[string]any{{"id": "p1", "title": "Paper 1"}}
		section := makeDraftSectionIdempotencyKey("doc-1", "intro", "Introduction", 650, papers, 777)
		repeatedSection := makeDraftSectionIdempotencyKey("doc-1", "intro", "Introduction", 650, papers, 777)
		changedSection := makeDraftSectionIdempotencyKey("doc-1", "intro", "Background", 650, papers, 777)
		versionedSection := makeDraftSectionIdempotencyKey("doc-1", "intro", "Introduction", 650, papers, 778)

		assert.Equal(t, section, repeatedSection)
		assert.NotEqual(t, section, changedSection)
		assert.NotEqual(t, section, versionedSection)
	})

	t.Run("replanAgentSessionForDomainAnswer updates canonical domain and question plan", func(t *testing.T) {
		session := map[string]any{
			"query":          "sleep interventions adults",
			"correctedQuery": "sleep interventions adults",
			"detectedDomain": "general",
			"answers": map[string]any{
				"q1_domain": map[string]any{
					"questionId":    "q1_domain",
					"values":        []string{"medicine", "biology"},
					"displayValues": []string{"Medicine", "Biology"},
				},
			},
		}

		require.True(t, replanAgentSessionForDomainAnswer(session))
		assert.Equal(t, "medicine", wisdev.AsOptionalString(session["detectedDomain"]))
		assert.Equal(t, []string{"biology"}, sliceStrings(session["secondaryDomains"]))
		assert.Equal(t, "advanced", wisdev.AsOptionalString(session["expertiseLevel"]))
		assert.Equal(t, []string{"q1_domain", "q2_scope", "q3_timeframe", "q4_subtopics", "q5_study_types", "q6_exclusions", "q7_evidence_quality"}, sliceStrings(session["questionSequence"]))
		assert.Equal(t, 7, wisdev.IntValue(session["maxQuestions"]))
	})

	t.Run("AnswersFromState clamps stale single-select answers", func(t *testing.T) {
		answers := AnswersFromState(map[string]any{
			"q3_timeframe": map[string]any{
				"questionId":    "q3_timeframe",
				"values":        []string{"1year", "5years"},
				"displayValues": []string{"Last Year", "Last 5 Years"},
			},
			"q4_subtopics": map[string]any{
				"questionId":    "q4_subtopics",
				"values":        []string{"retrieval", "reranking"},
				"displayValues": []string{"Retrieval", "Reranking"},
			},
		})

		assert.Equal(t, []string{"1year"}, answers["q3_timeframe"].Values)
		assert.Equal(t, []string{"Last Year"}, answers["q3_timeframe"].DisplayValues)
		assert.Equal(t, []string{"retrieval", "reranking"}, answers["q4_subtopics"].Values)
		assert.Equal(t, []string{"Retrieval", "Reranking"}, answers["q4_subtopics"].DisplayValues)
	})

	t.Run("buildAgentQuestionPayload", func(t *testing.T) {
		session := map[string]any{
			"currentQuestionIndex": 0.0,
			"questionSequence":     []string{"q1"},
			"answers":              map[string]any{},
			"questions": []any{
				map[string]any{"id": "q1", "question": "What is X?", "text": "What is X?"},
			},
		}
		res := buildAgentQuestionPayload(session, true)
		assert.Equal(t, "q1", res["id"])
		assert.Equal(t, "What is X?", res["question"])
	})

	t.Run("buildAgentQuestionPayload returns nil when session is ready", func(t *testing.T) {
		session := map[string]any{
			"status":               "ready",
			"currentQuestionIndex": 0.0,
			"questionSequence":     []string{"q1"},
			"answers":              map[string]any{},
			"questions": []any{
				map[string]any{"id": "q1", "question": "What is X?", "text": "What is X?"},
			},
		}
		assert.Nil(t, buildAgentQuestionPayload(session, true))
	})

	t.Run("buildAgentQuestionPayload prefers pending follow-up question", func(t *testing.T) {
		session := map[string]any{
			"status":               "questioning",
			"currentQuestionIndex": 3.0,
			"questionSequence":     []string{"q1", "q2", "q3", "q4_subtopics"},
			"answers": map[string]any{
				"q1": map[string]any{"questionId": "q1", "values": []string{"a"}},
				"q2": map[string]any{"questionId": "q2", "values": []string{"b"}},
				"q3": map[string]any{"questionId": "q3", "values": []string{"c"}},
			},
			"questions": []any{
				map[string]any{"id": "q4_subtopics", "question": "What comes next?", "text": "What comes next?"},
			},
			"pendingFollowUpQuestion": map[string]any{
				"id":               "follow_up_refinement",
				"type":             "clarification",
				"question":         "Which focus should the next search pass prioritize?",
				"targetQuestionId": "q4_subtopics",
			},
		}
		res := buildAgentQuestionPayload(session, true)
		assert.Equal(t, "follow_up_refinement", res["id"])
	})

	t.Run("buildAgentQuestioningEnvelopeBody annotates pending follow-up state", func(t *testing.T) {
		session := map[string]any{
			"sessionId":            "ws_1",
			"userId":               "u1",
			"originalQuery":        "systematic review of sleep interventions and memory outcomes in adults",
			"correctedQuery":       "systematic review of sleep interventions and memory outcomes in adults",
			"status":               "questioning",
			"currentQuestionIndex": 3.0,
			"questionSequence":     []string{"q1_domain", "q2_scope", "q3_timeframe", "q4_subtopics"},
			"answers": map[string]any{
				"q1_domain":    map[string]any{"questionId": "q1_domain", "values": []string{"medicine"}},
				"q2_scope":     map[string]any{"questionId": "q2_scope", "values": []string{"focused"}},
				"q3_timeframe": map[string]any{"questionId": "q3_timeframe", "values": []string{"5years"}},
			},
			"questions": []any{
				map[string]any{"id": "q4_subtopics", "question": "Which subtopics matter most?", "text": "Which subtopics matter most?"},
			},
			"minQuestions":    4,
			"maxQuestions":    5,
			"complexityScore": 0.8,
			"pendingFollowUpQuestion": map[string]any{
				"id":               "follow_up_refinement",
				"type":             "clarification",
				"question":         "Which focus should the next search pass prioritize?",
				"targetQuestionId": "q4_subtopics",
			},
		}
		body := buildAgentQuestioningEnvelopeBody("trace_1", session, false)
		questioning := mapAny(body["questioning"])
		assert.Equal(t, "follow_up_refinement", wisdev.AsOptionalString(questioning["pendingQuestionId"]))
		assert.Contains(t, sliceStrings(questioning["remainingQuestionIds"]), "follow_up_refinement")
	})

	t.Run("buildAgentOrchestrationPlan", func(t *testing.T) {
		session := map[string]any{
			"correctedQuery": "test query",
		}
		res := buildAgentOrchestrationPlan(session)
		assert.Equal(t, []string{"test query"}, res["queries"])
		assert.Equal(t, false, res["generatedFromTree"])
		assert.NotNil(t, res["generatedAt"])
	})

	t.Run("buildAgentOrchestrationPlanWithQueries", func(t *testing.T) {
		session := map[string]any{
			"correctedQuery": "fallback query",
		}
		res := buildAgentOrchestrationPlanWithQueries(session, []string{" alpha ", "beta", "alpha", ""}, map[string]any{"focused": []string{"alpha"}}, true)
		assert.Equal(t, []string{"alpha", "beta"}, res["queries"])
		assert.Equal(t, true, res["generatedFromTree"])
		assert.Equal(t, map[string]any{"focused": []string{"alpha"}}, res["coverageMap"])
	})

	t.Run("buildAgentOrchestrationPlanWithQueries falls back to originalQuery", func(t *testing.T) {
		session := map[string]any{
			"correctedQuery": "   ",
			"originalQuery":  "  seed query survives  ",
		}
		res := buildAgentOrchestrationPlanWithQueries(session, nil, nil, false)
		assert.Equal(t, []string{"seed query survives"}, res["queries"])
	})

	t.Run("buildDeepResearchPayload", func(t *testing.T) {
		papers := []wisdev.Source{{Title: "T1"}}
		res := buildDeepResearchPayload("q", []string{"cat1"}, "cs", papers)
		assert.Equal(t, "q", res["query"])
		assert.Len(t, res["categories"], 1)
		assert.Len(t, res["categorizedSources"], 1)
		categorized := res["categorizedSources"].([]map[string]any)
		assert.Equal(t, "cat1", categorized[0]["category"])
		assert.Len(t, categorized[0]["sources"], 1)
		assert.Len(t, res["paperPools"], 1)
	})
}

func TestWisDev_ValidationHelpers(t *testing.T) {
	t.Run("validateRequiredString", func(t *testing.T) {
		assert.NoError(t, validateRequiredString("val", "field", 10))
		assert.Error(t, validateRequiredString("", "field", 10))
		assert.Error(t, validateRequiredString("too long string", "field", 5))
	})

	t.Run("validateStringSlice", func(t *testing.T) {
		assert.NoError(t, validateStringSlice([]string{"a"}, "field", 5, 10))
		assert.Error(t, validateStringSlice([]string{"a", "b"}, "field", 1, 10))
		assert.Error(t, validateStringSlice([]string{"too long"}, "field", 5, 2))
		// Empty and whitespace-only items must be rejected.
		assert.Error(t, validateStringSlice([]string{""}, "field", 5, 10))
		assert.Error(t, validateStringSlice([]string{"  "}, "field", 5, 10))
		assert.Error(t, validateStringSlice([]string{"ok", ""}, "field", 5, 10))
	})

	t.Run("validateEnum", func(t *testing.T) {
		assert.True(t, validateEnum("a", "a", "b"))
		assert.False(t, validateEnum("c", "a", "b"))
	})
}

func TestBuildAnalyzeQueryPayload_HeuristicFallback(t *testing.T) {
	// With nil gateway (no LLM), buildAnalyzeQueryPayload must return the
	// heuristic defaults without panicking.
	payload := buildAnalyzeQueryPayload("RLHF reinforcement learning", "trace-1")
	assert.Equal(t, "general", payload["suggested_domains"].([]string)[0])
	assert.Equal(t, "moderate", payload["complexity"])
	assert.Equal(t, "broad_topic", payload["intent"])
	assert.NotEmpty(t, payload["entities"])
	assert.Equal(t, "trace-1", payload["traceId"])
	assert.Equal(t, []string{}, payload["methodology_hints"])
	// Heuristic fallback must always emit fallbackTriggered=true so the
	// frontend's warn gate fires correctly for Go-side degradation.
	assert.Equal(t, true, payload["fallbackTriggered"])
	assert.Equal(t, "llm_unavailable", payload["fallbackReason"])
}

func TestBuildAnalyzeQueryPayloadWithAI_NilGateway(t *testing.T) {
	// With nil agentGateway, the AI path must be skipped and heuristic
	// defaults returned — with fallbackTriggered=true.
	payload := buildAnalyzeQueryPayloadWithAI(nil, nil, "machine learning safety", "trace-2")
	domains := payload["suggested_domains"].([]string)
	assert.NotEmpty(t, domains)
	assert.Equal(t, "trace-2", payload["traceId"])
	assert.Equal(t, true, payload["fallbackTriggered"])
	// Complexity must be one of the valid values.
	complexity, _ := payload["complexity"].(string)
	assert.Contains(t, []string{"simple", "moderate", "complex"}, complexity)
}

func TestBuildAnalyzeQueryPayloadWithAI_EmptyQuery(t *testing.T) {
	// An empty query must not panic and must return sane defaults.
	payload := buildAnalyzeQueryPayloadWithAI(nil, nil, "", "trace-3")
	assert.NotNil(t, payload["suggested_domains"])
	assert.NotNil(t, payload["complexity"])
	assert.Equal(t, true, payload["fallbackTriggered"])
}

func TestAnalyzeQueryWithLLM_ValidationBranches(t *testing.T) {
	t.Run("normalizes and defaults invalid structured fields", func(t *testing.T) {
		client := llm.NewClient()
		client.SetClient(&staticStructuredLLMClient{
			structuredResp: &llmv1.StructuredResponse{
				JsonResult: `{"suggestedDomains":["CS","bogus","biology"],"complexity":"wild","intent":"unexpected","methodologyHints":["m1","m2","m3"],"reasoning":"because"}`,
			},
		})

		domains, complexity, intent, hints, reasoning, err := analyzeQueryWithLLM(context.Background(), client, "quantum gravity", "trace-validation")
		require.NoError(t, err)
		assert.Equal(t, []string{"cs", "biology"}, domains)
		assert.Equal(t, "moderate", complexity)
		assert.Equal(t, "broad_topic", intent)
		assert.Equal(t, []string{"m1", "m2"}, hints)
		assert.Equal(t, "because", reasoning)
	})

	t.Run("propagates client error", func(t *testing.T) {
		client := llm.NewClient()
		client.SetClient(&staticStructuredLLMClient{structuredErr: errors.New("boom")})

		_, _, _, _, _, err := analyzeQueryWithLLM(context.Background(), client, "quantum gravity", "trace-error")
		require.Error(t, err)
	})

	t.Run("propagates decode error", func(t *testing.T) {
		client := llm.NewClient()
		client.SetClient(&staticStructuredLLMClient{
			structuredResp: &llmv1.StructuredResponse{JsonResult: "{"},
		})

		_, _, _, _, _, err := analyzeQueryWithLLM(context.Background(), client, "quantum gravity", "trace-decode")
		require.Error(t, err)
	})
}

func TestBuildAnalyzeQueryPayloadWithAI_LLMPath(t *testing.T) {
	t.Run("success path uses structured output result", func(t *testing.T) {
		t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
		var captured structuredRequestCapture
		server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/llm/structured-output":
				require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jsonResult":  `{"suggestedDomains":["CS","biology","bogus"],"complexity":"simple","intent":"review","methodologyHints":["m1"],"reasoning":"model-based"}`,
					"modelUsed":   "test-model",
					"schemaValid": true,
				})
			default:
				http.NotFound(w, r)
			}
		}))
		t.Cleanup(server.Close)
		t.Setenv("PYTHON_SIDECAR_HTTP_URL", server.URL)

		gateway := &wisdev.AgentGateway{LLMClient: llm.NewClient()}
		payload := buildAnalyzeQueryPayloadWithAI(context.Background(), gateway, " machine learning safety ", "trace-ai")

		assert.Equal(t, false, payload["fallbackTriggered"])
		assert.Equal(t, "", payload["fallbackReason"])
		assert.Equal(t, "trace-ai", payload["traceId"])
		assert.Equal(t, []string{"cs", "biology"}, payload["suggested_domains"])
		assert.Equal(t, "simple", payload["complexity"])
		assert.Equal(t, "review", payload["intent"])
		assert.NotEmpty(t, payload["entities"])
		assert.Equal(t, "machine learning safety", payload["queryUsed"])
		assert.Equal(t, llm.ResolveLightModel(), captured.Model)
		assert.Equal(t, "light", captured.RequestClass)
		assert.Equal(t, "conservative", captured.RetryProfile)
		assert.Equal(t, "standard", captured.ServiceTier)
		assert.Greater(t, captured.LatencyBudgetMs, int32(0))
		assertStructuredPromptHygiene(t, captured.Prompt)
	})

	t.Run("timeout path falls back when context is canceled", func(t *testing.T) {
		t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
		server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/llm/structured-output" {
				http.NotFound(w, r)
				return
			}
			time.Sleep(150 * time.Millisecond)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonResult": `{"suggestedDomains":["cs"],"complexity":"simple","intent":"review","methodologyHints":[],"reasoning":"delayed"}`,
			})
		}))
		t.Cleanup(server.Close)
		t.Setenv("PYTHON_SIDECAR_HTTP_URL", server.URL)

		gateway := &wisdev.AgentGateway{LLMClient: llm.NewClient()}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		payload := buildAnalyzeQueryPayloadWithAI(ctx, gateway, "quantum gravity", "trace-timeout")

		assert.Equal(t, true, payload["fallbackTriggered"])
		assert.Equal(t, "handler_timeout", payload["fallbackReason"])
		require.NotNil(t, payload["fallbackDetail"])
		detail := payload["fallbackDetail"].(map[string]any)
		assert.Equal(t, "handler_context_done", detail["stage"])
		assert.Equal(t, llm.ResolveLightModel(), detail["model"])
	})

	t.Run("http error path falls back", func(t *testing.T) {
		t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
		server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/llm/structured-output":
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte(`{"error":"upstream unavailable"}`))
			case "/health":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"service":   "python_sidecar",
					"status":    "ok",
					"transport": "http-json+grpc-protobuf",
					"dependencies": []map[string]any{
						{
							"name":      "gemini_runtime",
							"transport": "vertex-sdk-or-proxy",
							"status":    "configured",
							"source":    "native",
							"detail":    "",
						},
					},
				})
			default:
				http.NotFound(w, r)
			}
		}))
		t.Cleanup(server.Close)
		t.Setenv("PYTHON_SIDECAR_HTTP_URL", server.URL)

		gateway := &wisdev.AgentGateway{LLMClient: llm.NewClient()}
		payload := buildAnalyzeQueryPayloadWithAI(context.Background(), gateway, "quantum gravity", "trace-error")

		assert.Equal(t, true, payload["fallbackTriggered"])
		assert.Equal(t, "sidecar_unavailable", payload["fallbackReason"])
		require.NotNil(t, payload["fallbackDetail"])
		detail := payload["fallbackDetail"].(map[string]any)
		assert.Equal(t, "llm_result_error", detail["stage"])
		assert.Equal(t, llm.ResolveLightModel(), detail["model"])
		assert.Equal(t, "http-json", detail["goToSidecarTransport"])
		assert.Equal(t, "native", detail["sidecarGeminiRuntimeSource"])
	})

	t.Run("invalid structured output falls back with explicit output classification", func(t *testing.T) {
		t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
		server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/llm/structured-output":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jsonResult":  "{\"suggestedDomains\":[",
					"schemaValid": false,
					"error":       "model response is not valid JSON",
				})
			case "/health":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"service":   "python_sidecar",
					"status":    "ok",
					"transport": "http-json+grpc-protobuf",
				})
			default:
				http.NotFound(w, r)
			}
		}))
		t.Cleanup(server.Close)
		t.Setenv("PYTHON_SIDECAR_HTTP_URL", server.URL)

		gateway := &wisdev.AgentGateway{LLMClient: llm.NewClient()}
		payload := buildAnalyzeQueryPayloadWithAI(context.Background(), gateway, "quantum gravity", "trace-invalid-json")

		assert.Equal(t, true, payload["fallbackTriggered"])
		assert.Equal(t, "llm_invalid_output", payload["fallbackReason"])
		require.NotNil(t, payload["fallbackDetail"])
		detail := payload["fallbackDetail"].(map[string]any)
		assert.Equal(t, "llm_result_error", detail["stage"])
		assert.Equal(t, "http-json", detail["goToSidecarTransport"])
	})

	t.Run("sidecar client backstop timeout is classified as sidecar_timeout", func(t *testing.T) {
		t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
		originalSidecarBackstopTimeout := wisdevAnalyzeQuerySidecarBackstopTimeout
		wisdevAnalyzeQuerySidecarBackstopTimeout = 50 * time.Millisecond
		t.Cleanup(func() {
			wisdevAnalyzeQuerySidecarBackstopTimeout = originalSidecarBackstopTimeout
		})
		server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/llm/structured-output" {
				http.NotFound(w, r)
				return
			}
			time.Sleep(150 * time.Millisecond)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonResult": `{"suggestedDomains":["cs"],"complexity":"simple","intent":"review","methodologyHints":[],"reasoning":"delayed"}`,
			})
		}))
		t.Cleanup(server.Close)
		t.Setenv("PYTHON_SIDECAR_HTTP_URL", server.URL)

		gateway := &wisdev.AgentGateway{LLMClient: llm.NewClient()}
		payload := buildAnalyzeQueryPayloadWithAI(context.Background(), gateway, "quantum gravity", "trace-http-timeout")

		assert.Equal(t, true, payload["fallbackTriggered"])
		assert.Equal(t, "sidecar_timeout", payload["fallbackReason"])
		require.NotNil(t, payload["fallbackDetail"])
		detail := payload["fallbackDetail"].(map[string]any)
		assert.Equal(t, "llm_result_error", detail["stage"])
	})
}
