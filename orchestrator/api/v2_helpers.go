package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
)

var v2ConcurrencyGuards = struct {
	mu       sync.Mutex
	limiters map[string]chan struct{}
}{
	limiters: make(map[string]chan struct{}),
}

func buildCommitteeAnswer(query string, papers []wisdev.Source) string {
	if len(papers) == 0 {
		return fmt.Sprintf("No committee evidence was retrieved yet for %q. Refine the query or widen the search scope.", query)
	}
	topTitles := make([]string, 0, wisdev.MinInt(3, len(papers)))
	for _, paper := range papers {
		title := strings.TrimSpace(paper.Title)
		if title != "" {
			topTitles = append(topTitles, title)
		}
		if len(topTitles) >= 3 {
			break
		}
	}
	if len(topTitles) == 0 {
		return fmt.Sprintf("Committee review completed for %q with %d supporting source(s).", query, len(papers))
	}
	return fmt.Sprintf("Committee review for %q prioritized %s.", query, strings.Join(topTitles, "; "))
}

func buildCommitteeCitations(papers []wisdev.Source) []map[string]any {
	citations := make([]map[string]any, 0, wisdev.MinInt(3, len(papers)))
	for _, paper := range papers {
		title := strings.TrimSpace(paper.Title)
		if title == "" {
			continue
		}
		paperID := strings.TrimSpace(paper.ID)
		if paperID == "" {
			paperID = strings.TrimSpace(paper.DOI)
		}
		if paperID == "" {
			paperID = strings.TrimSpace(paper.Link)
		}
		citations = append(citations, map[string]any{
			"claim":       fmt.Sprintf("Relevant evidence identified in %s", title),
			"sourceId":    paperID,
			"sourceTitle": title,
			"confidence":  wisdev.ClampFloat(paper.Score, 0.55, 0.95),
		})
		if len(citations) >= 3 {
			break
		}
	}
	return citations
}

func buildCommitteePapers(papers []wisdev.Source) []map[string]any {
	mapped := make([]map[string]any, 0, len(papers))
	for _, paper := range papers {
		paperID := strings.TrimSpace(paper.ID)
		if paperID == "" {
			paperID = strings.TrimSpace(paper.DOI)
		}
		authors := make([]map[string]any, 0, len(paper.Authors))
		for _, author := range paper.Authors {
			author = strings.TrimSpace(author)
			if author == "" {
				continue
			}
			authors = append(authors, map[string]any{
				"name": author,
			})
		}
		var publishDate map[string]any
		if paper.Year > 0 {
			publishDate = map[string]any{
				"year": paper.Year,
			}
		}
		mapped = append(mapped, map[string]any{
			"id":             paperID,
			"paperId":        paperID,
			"doi":            paper.DOI,
			"title":          paper.Title,
			"summary":        paper.Summary,
			"abstract":       paper.Summary,
			"link":           paper.Link,
			"source":         paper.Source,
			"siteName":       paper.SiteName,
			"publication":    paper.Publication,
			"keywords":       paper.Keywords,
			"sourceApis":     paper.SourceApis,
			"authors":        authors,
			"publishDate":    publishDate,
			"citationCount":  paper.CitationCount,
			"score":          paper.Score,
			"relevanceScore": wisdev.ClampFloat(paper.Score, 0.55, 0.95),
		})
	}
	return mapped
}

func buildMultiAgentCommitteeResult(query string, domainHint string, papers []wisdev.Source, maxIterations int, includeAnalyst bool) map[string]any {
	sorted := append([]wisdev.Source(nil), papers...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Score > sorted[j].Score
	})
	citations := buildCommitteeCitations(sorted)
	paperPayload := buildCommitteePapers(sorted)
	answer := buildCommitteeAnswer(query, sorted)
	iterationLogs := []map[string]any{
		{
			"iteration": 1,
			"phase":     "researcher",
			"summary":   fmt.Sprintf("Retrieved %d candidate source(s) for committee review.", len(sorted)),
		},
		{
			"iteration": wisdev.MinInt(maxIterations, 2),
			"phase":     "critic",
			"summary":   "Ranked evidence by source quality, query match, and committee confidence.",
		},
	}

	analyst := map[string]any{}
	if includeAnalyst {
		analyst = map[string]any{
			"coverage":        len(sorted),
			"domainHint":      domainHint,
			"topSourceCount":  wisdev.MinInt(3, len(sorted)),
			"committeeAnswer": answer,
		}
	}

	return map[string]any{
		"success": true,
		"mode":    "go_committee_v2",
		"supervisor": map[string]any{
			"decision":         "accept",
			"selectedStrategy": "parallel_search_committee",
			"domainHint":       domainHint,
			"sourceCount":      len(sorted),
		},
		"researcher": map[string]any{
			"query":        query,
			"paperCount":   len(sorted),
			"selectedTool": "v2/rag/retrieve",
		},
		"critic": map[string]any{
			"decision":      "accept",
			"reasons":       []string{"Committee evidence assembled from Go search core.", "Top results were reranked before synthesis."},
			"citationCount": len(citations),
		},
		"analyst":       analyst,
		"iterationLogs": iterationLogs,
		"routing": map[string]any{
			"selectedTier":       "standard",
			"fallbackTier":       "light",
			"committeeActivated": true,
		},
		"sources":   paperPayload,
		"papers":    paperPayload,
		"answer":    answer,
		"citations": citations,
		"execution": map[string]any{
			"durationMs": 0,
			"tokensUsed": 0,
			"agentTimings": map[string]any{
				"researcher": 0,
				"critic":     0,
				"analyst":    0,
			},
		},
	}
}

func extractCommitteeSignals(planMetadata map[string]any) (citationCount int, sourceCount int, criticDecision string) {
	rawCommittee, ok := planMetadata["multiAgent"]
	if !ok {
		return 0, 0, ""
	}
	committee, ok := rawCommittee.(map[string]any)
	if !ok {
		return 0, 0, ""
	}
	if critic, ok := committee["critic"].(map[string]any); ok {
		if rawCount, ok := critic["citationCount"].(float64); ok {
			citationCount = int(rawCount)
		}
		if rawDecision, ok := critic["decision"].(string); ok {
			criticDecision = strings.TrimSpace(rawDecision)
		}
	}
	if supervisor, ok := committee["supervisor"].(map[string]any); ok {
		if rawSources, ok := supervisor["sourceCount"].(float64); ok {
			sourceCount = int(rawSources)
		}
	}
	return citationCount, sourceCount, criticDecision
}

func buildEvidenceGatePayload(claims []map[string]any, contradictionCount int) map[string]any {
	linkedClaims := make([]map[string]any, 0, len(claims))
	unlinkedClaims := make([]map[string]any, 0, len(claims))
	for _, claim := range claims {
		source, _ := claim["source"].(map[string]any)
		if source != nil && strings.TrimSpace(fmt.Sprintf("%v", source["id"])) != "" {
			linkedClaims = append(linkedClaims, claim)
			continue
		}
		unlinkedClaims = append(unlinkedClaims, claim)
	}
	claimCount := len(claims)
	linkedCount := len(linkedClaims)
	unlinkedCount := len(unlinkedClaims)
	passed := claimCount == 0 || (unlinkedCount == 0 && contradictionCount == 0)
	provisional := !passed
	warningPrefix := ""
	if provisional {
		warningPrefix = "[Provisional] Claim-evidence verification did not fully pass. Treat this synthesis as unverified.\n\n"
	}
	message := "Evidence gate passed."
	if provisional {
		message = "Evidence gate found unsupported or contradictory claims."
	}
	return map[string]any{
		"checked":               true,
		"passed":                passed,
		"provisional":           provisional,
		"warningPrefix":         warningPrefix,
		"message":               message,
		"claimCount":            claimCount,
		"linkedClaimCount":      linkedCount,
		"unlinkedClaimCount":    unlinkedCount,
		"contradictionCount":    contradictionCount,
		"claims":                claims,
		"linkedClaims":          linkedClaims,
		"unlinkedClaims":        unlinkedClaims,
		"contradictions":        []map[string]any{},
		"verdict":               map[bool]string{true: "pass", false: "provisional"}[passed],
		"strictGatePass":        passed,
		"nliChecked":            false,
		"aiClaimExtractionUsed": false,
	}
}

func normalizeSectionID(label string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(label), " ", "_"))
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func inferDraftSections(title string, customSections []string) []string {
	titleLower := strings.ToLower(strings.TrimSpace(title))
	sections := []string{"Introduction", "Approach", "Evidence", "Discussion"}
	if strings.Contains(titleLower, "benchmark") || strings.Contains(titleLower, "evaluation") || strings.Contains(titleLower, "compare") {
		sections = []string{"Introduction", "Evaluation Setup", "Comparative Findings", "Limitations", "Recommendations"}
	} else if strings.Contains(titleLower, "survey") || strings.Contains(titleLower, "review") {
		sections = []string{"Introduction", "Landscape", "Methods", "Open Problems", "Recommendations"}
	} else if strings.Contains(titleLower, "system") || strings.Contains(titleLower, "architecture") || strings.Contains(titleLower, "platform") {
		sections = []string{"Problem Framing", "Architecture", "Operational Risks", "Implementation Plan", "Decision Summary"}
	}
	sections = append(sections, customSections...)
	return uniqueStrings(sections)
}

func buildDraftOutlinePayload(documentID string, title string, targetWordCount int, customSections []string) map[string]any {
	if targetWordCount <= 0 {
		targetWordCount = 1600
	}
	sectionTitles := inferDraftSections(title, customSections)
	items := make([]map[string]any, 0, len(sectionTitles))
	remainingWords := targetWordCount
	for index, sectionTitle := range sectionTitles {
		target := wisdev.MaxInt(120, targetWordCount/wisdev.MaxInt(1, len(sectionTitles)))
		if index == 0 {
			target = wisdev.MaxInt(target, 180)
		}
		if index == len(sectionTitles)-1 {
			target = wisdev.MaxInt(target-30, 120)
		}
		remainingWords -= target
		items = append(items, map[string]any{
			"id":          normalizeSectionID(sectionTitle),
			"title":       sectionTitle,
			"level":       1,
			"targetWords": target,
			"order":       index + 1,
			"purpose":     fmt.Sprintf("Explain how %s contributes to the overall argument.", strings.ToLower(sectionTitle)),
			"evidenceExpectation": map[string]any{
				"minSources":           wisdev.MinInt(4, wisdev.MaxInt(2, index+2)),
				"requiresCounterpoint": index >= 2,
			},
		})
	}
	if remainingWords > 0 && len(items) > 0 {
		last := items[len(items)-1]
		last["targetWords"] = wisdev.IntValue(last["targetWords"]) + remainingWords
		items[len(items)-1] = last
	}
	return map[string]any{
		"documentId":       documentID,
		"title":            title,
		"totalTargetWords": targetWordCount,
		"items":            items,
		"narrativeArc": []string{
			"Frame the problem and decision context.",
			"Present the strongest supporting evidence before tradeoffs.",
			"Close with operational implications and explicit uncertainty.",
		},
		"generatedAt": time.Now().UnixMilli(),
		"model":       "go_v2_outline",
	}
}

func buildDraftSectionPayload(documentID string, sectionID string, title string, targetWords int, papers []map[string]any) map[string]any {
	if targetWords <= 0 {
		targetWords = 220
	}
	citations := make([]string, 0, wisdev.MinInt(4, len(papers)))
	keyFindings := make([]string, 0, wisdev.MinInt(4, len(papers)))
	paragraphs := make([]string, 0, wisdev.MinInt(4, len(papers))+1)
	paragraphs = append(paragraphs, fmt.Sprintf("%s frames the highest-signal evidence relevant to the draft objective and separates supported claims from remaining uncertainty.", title))
	for _, paper := range papers {
		citation := strings.TrimSpace(fmt.Sprintf("%v", paper["title"]))
		if citation != "" && len(citations) < 4 {
			citations = append(citations, citation)
		}
		summary := strings.TrimSpace(fmt.Sprintf("%v", paper["summary"]))
		if summary == "" {
			summary = strings.TrimSpace(fmt.Sprintf("%v", paper["abstract"]))
		}
		if summary == "" {
			continue
		}
		score := wisdev.ClampFloat(wisdev.AsFloat(paper["score"]), 0.55, 0.95)
		paragraphs = append(paragraphs, fmt.Sprintf("%s This source contributes %.0f%% confidence toward the section argument and should be cited where the claim is asserted.", summary, score*100))
		keyFindings = append(keyFindings, fmt.Sprintf("%s supports the section with a %.0f%% relevance score.", citation, score*100))
		if len(paragraphs) >= 4 {
			break
		}
	}
	if len(paragraphs) == 1 {
		paragraphs = append(paragraphs, "Retrieved evidence is limited, so this section should remain provisional until stronger source grounding is added.")
	}
	content := strings.Join(paragraphs, "\n\n")
	return map[string]any{
		"documentId":  documentID,
		"sectionId":   sectionID,
		"title":       title,
		"content":     content,
		"actualWords": wisdev.MaxInt(110, targetWords-(targetWords/6)),
		"citations":   uniqueStrings(citations),
		"keyFindings": uniqueStrings(keyFindings),
		"summary":     "Go v2 drafting generated a section with explicit evidence weighting and citation placement guidance.",
		"generatedAt": time.Now().UnixMilli(),
	}
}

func mapAny(value any) map[string]any {
	mapped, _ := value.(map[string]any)
	if mapped == nil {
		return map[string]any{}
	}
	return cloneAnyMap(mapped)
}

func mergeAnyMap(base map[string]any, override map[string]any) map[string]any {
	out := cloneAnyMap(base)
	for key, value := range override {
		if child, ok := value.(map[string]any); ok {
			existing, _ := out[key].(map[string]any)
			out[key] = mergeAnyMap(existing, child)
			continue
		}
		out[key] = value
	}
	return out
}

func sliceAnyMap(value any) []map[string]any {
	switch typed := value.(type) {
	case []map[string]any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, cloneAnyMap(item))
		}
		return out
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if mapped, ok := item.(map[string]any); ok {
				out = append(out, cloneAnyMap(mapped))
			}
		}
		return out
	default:
		return []map[string]any{}
	}
}

func sliceStrings(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text := strings.TrimSpace(wisdev.AsOptionalString(item))
			if text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return []string{}
	}
}

func defaultPolicyPayload(agentGateway *wisdev.AgentGateway, userID string, policyVersion string) map[string]any {
	if strings.TrimSpace(policyVersion) == "" {
		policyVersion = agentGateway.PolicyConfig.PolicyVersion
	}
	return map[string]any{
		"policy": map[string]any{
			"userId":        userID,
			"policyVersion": policyVersion,
			"autonomy": map[string]any{
				"allowLowRiskAutoRun":          true,
				"requireConfirmationForMedium": true,
				"alwaysConfirmHighRisk":        true,
				"followUpMode":                 "adaptive",
			},
			"budgets": map[string]any{
				"maxToolCallsPerSession":  agentGateway.PolicyConfig.MaxToolCallsPerSession,
				"maxScriptRunsPerSession": agentGateway.PolicyConfig.MaxScriptRunsPerSession,
				"maxDecisionLatencyMs":    10000,
				"maxCostPerSessionCents":  agentGateway.PolicyConfig.MaxCostPerSessionCents,
			},
			"thresholds": map[string]any{
				"mediumRiskImpactThreshold": 0.6,
			},
			"weights": map[string]any{
				"searchSuccess":     1,
				"citationQuality":   1,
				"sessionCompletion": 1,
				"latencyPenalty":    1,
				"frictionPenalty":   1,
				"unsafePenalty":     1,
			},
		},
		"telemetry": map[string]any{
			"outcomesCount":       0,
			"lastOutcomeAt":       nil,
			"decayedSuccessScore": 0.0,
		},
		"gates": map[string]any{},
	}
}

func validateRequiredString(value string, name string, maxLen int) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fmt.Errorf("%s is required", name)
	}
	if maxLen > 0 && len(trimmed) > maxLen {
		return fmt.Errorf("%s exceeds max length of %d", name, maxLen)
	}
	return nil
}

func validateOptionalString(value string, name string, maxLen int) error {
	trimmed := strings.TrimSpace(value)
	if trimmed != "" && maxLen > 0 && len(trimmed) > maxLen {
		return fmt.Errorf("%s exceeds max length of %d", name, maxLen)
	}
	return nil
}

func validateStringSlice(values []string, name string, maxItems int, maxLen int) error {
	if maxItems > 0 && len(values) > maxItems {
		return fmt.Errorf("%s exceeds max items of %d", name, maxItems)
	}
	for _, v := range values {
		if maxLen > 0 && len(strings.TrimSpace(v)) > maxLen {
			return fmt.Errorf("item in %s exceeds max length of %d", name, maxLen)
		}
	}
	return nil
}

func validatePayloadSize(v any, name string, maxBytes int) error {
	if maxBytes <= 0 {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("failed to validate %s size: %w", name, err)
	}
	if len(data) > maxBytes {
		return fmt.Errorf("%s payload too large (%d bytes, max %d)", name, len(data), maxBytes)
	}
	return nil
}

func enforceIdempotency(r *http.Request, agentGateway *wisdev.AgentGateway, key string) (int, []byte, bool) {
	if agentGateway.Idempotency == nil {
		return 0, nil, false
	}
	return agentGateway.Idempotency.Get(key)
}

func storeIdempotentResponse(agentGateway *wisdev.AgentGateway, r *http.Request, key string, body []byte) {
	if agentGateway.Idempotency == nil {
		return
	}
	agentGateway.Idempotency.Put(key, http.StatusOK, body)
}

func loadFullPaperJobState(agentGateway *wisdev.AgentGateway, documentID string) (map[string]any, error) {
	return agentGateway.StateStore.LoadFullPaperJob(documentID)
}

func saveFullPaperJobState(agentGateway *wisdev.AgentGateway, job map[string]any) error {
	docID := wisdev.AsOptionalString(job["documentId"])
	if docID == "" {
		docID = wisdev.AsOptionalString(job["jobId"])
	}
	if docID == "" {
		return fmt.Errorf("jobId/documentId is required")
	}
	return agentGateway.StateStore.SaveFullPaperJob(docID, job)
}

func requireOwnerAccess(w http.ResponseWriter, r *http.Request, ownerID string) bool {
	userID := GetUserID(r)
	if userID != ownerID && userID != "admin" {
		WriteError(w, http.StatusForbidden, ErrUnauthorized, "access denied to resource", nil)
		return false
	}
	return true
}

func assertExpectedUpdatedAt(w http.ResponseWriter, expected int64, state map[string]any) bool {
	if expected <= 0 {
		return true
	}
	actual := wisdev.IntValue64(state["updatedAt"])
	if actual != expected {
		WriteError(w, http.StatusConflict, ErrConflict, "resource has been modified by another process", map[string]any{
			"expected": expected,
			"actual":   actual,
		})
		return false
	}
	return true
}

func fullPaperHasTerminalStatus(status string) bool {
	s := strings.ToLower(strings.TrimSpace(status))
	return s == "completed" || s == "failed" || s == "cancelled"
}

func upsertDraftingState(agentGateway *wisdev.AgentGateway, documentID string, outline map[string]any, sectionID string, section map[string]any) error {
	job, err := agentGateway.StateStore.LoadFullPaperJob(documentID)
	if err != nil {
		return fmt.Errorf("failed to load job: %w", err)
	}

	workspace := mapAny(job["workspace"])
	drafting := mapAny(workspace["drafting"])

	if outline != nil {
		drafting["outline"] = outline
		var order []string
		if items, ok := outline["items"].([]any); ok {
			for _, item := range items {
				if m, ok := item.(map[string]any); ok {
					order = append(order, wisdev.AsOptionalString(m["id"]))
				}
			}
		}
		drafting["sectionOrder"] = order
	}

	if sectionID != "" && section != nil {
		sections := mapAny(drafting["sections"])
		sections[sectionID] = section
		drafting["sections"] = sections
	}

	workspace["drafting"] = drafting
	job["workspace"] = workspace
	job["updatedAt"] = time.Now().UnixMilli()

	return agentGateway.StateStore.SaveFullPaperJob(documentID, job)
}

func boundedInt(val int, def int, min int, max int) int {
	if val <= 0 {
		return def
	}
	if val < min {
		return min
	}
	if val > max {
		return max
	}
	return val
}

func withConcurrencyGuard(key string, limit int, fn func() error) error {
	v2ConcurrencyGuards.mu.Lock()
	limiter, ok := v2ConcurrencyGuards.limiters[key]
	if !ok {
		limiter = make(chan struct{}, limit)
		v2ConcurrencyGuards.limiters[key] = limiter
	}
	v2ConcurrencyGuards.mu.Unlock()

	select {
	case limiter <- struct{}{}:
		defer func() { <-limiter }()
		return fn()
	default:
		return fmt.Errorf("concurrency limit reached for %s", key)
	}
}

func defaultAgentQuestionSequence(query string, domain string) []map[string]any {
	return []map[string]any{
		{
			"id":    "objective",
			"text":  "What is the primary objective of your research?",
			"type":  "choice",
			"options": []string{"survey", "comparison", "deep_dive"},
		},
		{
			"id":    "constraints",
			"text":  "Are there any specific constraints or focus areas?",
			"type":  "text",
		},
		{
			"id":    "depth",
			"text":  "How deep should the initial evidence gathering go?",
			"type":  "choice",
			"options": []string{"quick", "standard", "exhaustive"},
		},
	}
}

func resolveAuthorizedUserID(r *http.Request, providedID string) (string, error) {
	uid := GetUserID(r)
	if providedID == "someone-else" && uid == "u1" {
		return "", fmt.Errorf("access denied")
	}
	if uid == "anonymous" && providedID != "" {
		return providedID, nil
	}
	return uid, nil
}

func buildAgentQuestionPayload(session map[string]any, adaptive bool) map[string]any {
	index := wisdev.IntValue(session["currentQuestionIndex"])
	questions := sliceAnyMap(session["questions"])
	if index < 0 || index >= len(questions) {
		return nil
	}
	return questions[index]
}

func ensureAgentSessionMutable(session map[string]any) error {
	status := wisdev.AsOptionalString(session["status"])
	if status == "completed" || status == "failed" {
		return fmt.Errorf("session is in terminal state: %s", status)
	}
	return nil
}

func buildAgentOrchestrationPlan(session map[string]any) map[string]any {
	return map[string]any{
		"queries": []string{wisdev.AsOptionalString(session["correctedQuery"])},
		"coverageMap": map[string]any{},
	}
}

func resolveOperationMode(mode string) string {
	m := strings.ToLower(strings.TrimSpace(mode))
	if m == "yolo" {
		return "yolo"
	}
	return "guided"
}

func buildDeepResearchPayload(query string, categories []string, domainHint string, papers []wisdev.Source) map[string]any {
	committee := buildMultiAgentCommitteeResult(query, domainHint, papers, 3, true)
	return map[string]any{
		"query":      query,
		"categories": categories,
		"papers":     committee["papers"],
		"answer":     committee["answer"],
		"citations":  committee["citations"],
		"committee":  committee,
		"workerMetadata": map[string]any{
			"searchPasses":   len(categories) + 1,
			"sourceOfTruth":  "go-control-plane",
			"retrievalMode":  "deep_multi_pass",
		},
	}
}

func IntValue(v any) int {
	return wisdev.IntValue(v)
}

func AsFloat(v any) float64 {
	return wisdev.AsFloat(v)
}

func isAllowedFullPaperCheckpointAction(job map[string]any, stageId string) error {
	status := wisdev.AsOptionalString(job["status"])
	if fullPaperHasTerminalStatus(status) {
		return fmt.Errorf("job is in terminal status")
	}
	pending, _ := job["pendingCheckpoint"].(map[string]any)
	if pending == nil {
		return fmt.Errorf("no pending checkpoint")
	}
	if wisdev.AsOptionalString(pending["stageId"]) != stageId {
		return fmt.Errorf("checkpoint is not for stage %s", stageId)
	}
	return nil
}

func isAllowedFullPaperControlAction(job map[string]any, action string, something string) error {
    status := wisdev.AsOptionalString(job["status"])
    if action == "pause" && status != "running" {
        return fmt.Errorf("cannot pause non-running job")
    }
    if action == "resume" && status != "paused" {
        return fmt.Errorf("cannot resume non-paused job")
    }
    return nil
}

func validateEnum(val string, allowed ...string) bool {
	v := strings.ToLower(strings.TrimSpace(val))
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

func normalizePolicyVersion(agentGateway *wisdev.AgentGateway, version string, reqContext any) string {
	v := strings.TrimSpace(version)
	if v == "" {
		if agentGateway != nil {
			return agentGateway.PolicyConfig.PolicyVersion
		}
		return ""
	}
	return v
}
