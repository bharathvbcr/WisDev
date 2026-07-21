package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	defaultPaperclipBaseURL       = "http://127.0.0.1:3100"
	paperclipProbeTimeout         = 2500 * time.Millisecond
	paperclipCreateTimeout        = 10 * time.Second
	paperclipMaxExportIssues      = 20
	paperclipMaxErrorBodyBytes    = 4096
	paperclipMaxErrorSnippetRunes = 512
)

type paperclipClient struct {
	baseURL    string
	httpClient *http.Client
}

type paperclipHealthResponse struct {
	Status             string         `json:"status"`
	Version            string         `json:"version,omitempty"`
	DeploymentMode     string         `json:"deploymentMode,omitempty"`
	DeploymentExposure string         `json:"deploymentExposure,omitempty"`
	BootstrapStatus    string         `json:"bootstrapStatus,omitempty"`
	Features           map[string]any `json:"features,omitempty"`
}

type paperclipStatusResponse struct {
	Configured      bool                     `json:"configured"`
	BaseURL         string                   `json:"baseUrl"`
	Reachable       bool                     `json:"reachable"`
	Status          string                   `json:"status"`
	Version         string                   `json:"version,omitempty"`
	DeploymentMode  string                   `json:"deploymentMode,omitempty"`
	BootstrapStatus string                   `json:"bootstrapStatus,omitempty"`
	ErrorCode       string                   `json:"errorCode,omitempty"`
	ErrorMessage    string                   `json:"errorMessage,omitempty"`
	Health          *paperclipHealthResponse `json:"health,omitempty"`
}

type paperclipCreateIssueRequest struct {
	CompanyID       string `json:"companyId"`
	ProjectID       string `json:"projectId,omitempty"`
	GoalID          string `json:"goalId,omitempty"`
	AssigneeAgentID string `json:"assigneeAgentId,omitempty"`
	Title           string `json:"title"`
	Description     string `json:"description,omitempty"`
	Priority        string `json:"priority,omitempty"`
	Status          string `json:"status,omitempty"`
}

type paperclipIssueResponse struct {
	ID         string         `json:"id,omitempty"`
	Identifier string         `json:"identifier,omitempty"`
	Title      string         `json:"title,omitempty"`
	Status     string         `json:"status,omitempty"`
	Priority   string         `json:"priority,omitempty"`
	Raw        map[string]any `json:"raw,omitempty"`
}

type paperclipCreateIssueResponse struct {
	OK      bool                   `json:"ok"`
	BaseURL string                 `json:"baseUrl"`
	Issue   paperclipIssueResponse `json:"issue"`
}

type paperclipResearchPlanRequest struct {
	Objective    string `json:"objective"`
	Domain       string `json:"domain,omitempty"`
	Depth        string `json:"depth,omitempty"`
	Governance   string `json:"governance,omitempty"`
	ModelTier    string `json:"modelTier,omitempty"`
	MaxSpendHint string `json:"maxSpendHint,omitempty"`
}

type paperclipPlanAgent struct {
	ID               string   `json:"id"`
	Title            string   `json:"title"`
	Role             string   `json:"role"`
	ReportsTo        string   `json:"reportsTo,omitempty"`
	Responsibilities []string `json:"responsibilities"`
	ModelTier        string   `json:"modelTier"`
	Heartbeat        string   `json:"heartbeat"`
}

type paperclipPlanIssue struct {
	ClientKey          string   `json:"clientKey"`
	Title              string   `json:"title"`
	Description        string   `json:"description"`
	Priority           string   `json:"priority"`
	OwnerAgentID       string   `json:"ownerAgentId"`
	DependsOn          []string `json:"dependsOn,omitempty"`
	AcceptanceCriteria []string `json:"acceptanceCriteria"`
}

type paperclipResearchPlanResponse struct {
	Mission       string               `json:"mission"`
	Domain        string               `json:"domain"`
	Depth         string               `json:"depth"`
	Governance    string               `json:"governance"`
	ModelTier     string               `json:"modelTier"`
	Heartbeat     string               `json:"heartbeat"`
	BudgetPolicy  string               `json:"budgetPolicy"`
	Agents        []paperclipPlanAgent `json:"agents"`
	Issues        []paperclipPlanIssue `json:"issues"`
	ReviewGates   []string             `json:"reviewGates"`
	ExportSummary string               `json:"exportSummary"`
}

type paperclipExportPlanRequest struct {
	CompanyID string               `json:"companyId"`
	ProjectID string               `json:"projectId,omitempty"`
	GoalID    string               `json:"goalId,omitempty"`
	Issues    []paperclipPlanIssue `json:"issues"`
}

type paperclipExportIssueResult struct {
	ClientKey string                  `json:"clientKey"`
	OK        bool                    `json:"ok"`
	Status    int                     `json:"status"`
	Issue     *paperclipIssueResponse `json:"issue,omitempty"`
	Error     string                  `json:"error,omitempty"`
}

type paperclipExportPlanResponse struct {
	OK      bool                         `json:"ok"`
	BaseURL string                       `json:"baseUrl"`
	Created int                          `json:"created"`
	Failed  int                          `json:"failed"`
	Results []paperclipExportIssueResult `json:"results"`
}

func newPaperclipClientFromEnv() paperclipClient {
	base := strings.TrimSpace(os.Getenv("PAPERCLIP_BASE_URL"))
	if base == "" {
		base = defaultPaperclipBaseURL
	}
	return paperclipClient{
		baseURL:    trimTrailingSlash(base),
		httpClient: &http.Client{},
	}
}

func trimTrailingSlash(value string) string {
	return strings.TrimRight(strings.TrimSpace(value), "/")
}

func validatePaperclipBaseURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("unsupported scheme %q", parsed.Scheme)
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return errors.New("host is required")
	}
	return nil
}

func (c paperclipClient) endpoint(path string) (string, error) {
	if err := validatePaperclipBaseURL(c.baseURL); err != nil {
		return "", err
	}
	return c.baseURL + path, nil
}

func (c paperclipClient) checkHealth(ctx context.Context) paperclipStatusResponse {
	status := paperclipStatusResponse{
		Configured: true,
		BaseURL:    c.baseURL,
		Reachable:  false,
		Status:     "unreachable",
	}
	endpoint, err := c.endpoint("/api/health")
	if err != nil {
		status.ErrorCode = "invalid_base_url"
		status.ErrorMessage = err.Error()
		return status
	}

	ctx, cancel := context.WithTimeout(ctx, paperclipProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		status.ErrorCode = "request_build_failed"
		status.ErrorMessage = err.Error()
		return status
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		status.ErrorCode = "request_failed"
		status.ErrorMessage = err.Error()
		return status
	}
	defer resp.Body.Close()

	var health paperclipHealthResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&health); err != nil {
		status.ErrorCode = "invalid_health_response"
		status.ErrorMessage = err.Error()
		return status
	}

	status.Health = &health
	status.Status = health.Status
	status.Version = health.Version
	status.DeploymentMode = health.DeploymentMode
	status.BootstrapStatus = health.BootstrapStatus
	status.Reachable = resp.StatusCode >= 200 && resp.StatusCode < 300 && strings.EqualFold(health.Status, "ok")
	if !status.Reachable && status.ErrorCode == "" {
		status.ErrorCode = "unhealthy"
		status.ErrorMessage = fmt.Sprintf("Paperclip health returned HTTP %d with status %q", resp.StatusCode, health.Status)
	}
	return status
}

func (c paperclipClient) createIssue(ctx context.Context, req paperclipCreateIssueRequest) (paperclipCreateIssueResponse, int, error) {
	companyID := strings.TrimSpace(req.CompanyID)
	if companyID == "" {
		return paperclipCreateIssueResponse{}, http.StatusBadRequest, errors.New("companyId is required")
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		return paperclipCreateIssueResponse{}, http.StatusBadRequest, errors.New("title is required")
	}

	payload := map[string]any{
		"title":       title,
		"description": strings.TrimSpace(req.Description),
		"priority":    normalizePaperclipPriority(req.Priority),
	}
	if status := normalizePaperclipIssueStatus(req.Status); status != "" {
		payload["status"] = status
	}
	if projectID := strings.TrimSpace(req.ProjectID); projectID != "" {
		payload["projectId"] = projectID
	}
	if goalID := strings.TrimSpace(req.GoalID); goalID != "" {
		payload["goalId"] = goalID
	}
	if assigneeAgentID := strings.TrimSpace(req.AssigneeAgentID); assigneeAgentID != "" {
		payload["assigneeAgentId"] = assigneeAgentID
	}

	endpoint, err := c.endpoint("/api/companies/" + url.PathEscape(companyID) + "/issues")
	if err != nil {
		return paperclipCreateIssueResponse{}, http.StatusBadRequest, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return paperclipCreateIssueResponse{}, http.StatusInternalServerError, err
	}

	ctx, cancel := context.WithTimeout(ctx, paperclipCreateTimeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return paperclipCreateIssueResponse{}, http.StatusInternalServerError, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return paperclipCreateIssueResponse{}, http.StatusBadGateway, err
	}
	defer resp.Body.Close()

	rawBody, _ := io.ReadAll(io.LimitReader(resp.Body, paperclipMaxErrorBodyBytes))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return paperclipCreateIssueResponse{}, http.StatusBadGateway, fmt.Errorf("Paperclip returned HTTP %d: %s", resp.StatusCode, paperclipErrorSnippet(rawBody))
	}

	var raw map[string]any
	if err := json.Unmarshal(rawBody, &raw); err != nil {
		return paperclipCreateIssueResponse{}, http.StatusBadGateway, err
	}
	return paperclipCreateIssueResponse{
		OK:      true,
		BaseURL: c.baseURL,
		Issue:   normalizePaperclipIssue(raw),
	}, http.StatusOK, nil
}

func (c paperclipClient) exportPlan(ctx context.Context, req paperclipExportPlanRequest) (paperclipExportPlanResponse, int, error) {
	companyID := strings.TrimSpace(req.CompanyID)
	if companyID == "" {
		return paperclipExportPlanResponse{}, http.StatusBadRequest, errors.New("companyId is required")
	}
	if len(req.Issues) == 0 {
		return paperclipExportPlanResponse{}, http.StatusBadRequest, errors.New("at least one issue is required")
	}
	if len(req.Issues) > paperclipMaxExportIssues {
		return paperclipExportPlanResponse{}, http.StatusBadRequest, fmt.Errorf("at most %d issues can be exported at once", paperclipMaxExportIssues)
	}

	response := paperclipExportPlanResponse{
		OK:      true,
		BaseURL: c.baseURL,
		Results: make([]paperclipExportIssueResult, 0, len(req.Issues)),
	}
	for _, issue := range req.Issues {
		clientKey := strings.TrimSpace(issue.ClientKey)
		if clientKey == "" {
			clientKey = strings.TrimSpace(issue.Title)
		}
		createReq := paperclipCreateIssueRequest{
			CompanyID:   companyID,
			ProjectID:   req.ProjectID,
			GoalID:      req.GoalID,
			Title:       issue.Title,
			Description: formatPaperclipPlanIssueDescription(issue),
			Priority:    issue.Priority,
			Status:      "backlog",
		}
		created, status, err := c.createIssue(ctx, createReq)
		result := paperclipExportIssueResult{
			ClientKey: clientKey,
			Status:    status,
		}
		if err != nil {
			result.Error = err.Error()
			response.OK = false
			response.Failed++
		} else {
			result.OK = true
			result.Issue = &created.Issue
			response.Created++
		}
		response.Results = append(response.Results, result)
	}
	return response, http.StatusOK, nil
}

func formatPaperclipPlanIssueDescription(issue paperclipPlanIssue) string {
	var builder strings.Builder
	builder.WriteString(strings.TrimSpace(issue.Description))
	if len(issue.DependsOn) > 0 {
		builder.WriteString("\n\nDepends on:\n")
		for _, dependency := range issue.DependsOn {
			if trimmed := strings.TrimSpace(dependency); trimmed != "" {
				builder.WriteString("- ")
				builder.WriteString(trimmed)
				builder.WriteByte('\n')
			}
		}
	}
	if len(issue.AcceptanceCriteria) > 0 {
		builder.WriteString("\nAcceptance criteria:\n")
		for _, criterion := range issue.AcceptanceCriteria {
			if trimmed := strings.TrimSpace(criterion); trimmed != "" {
				builder.WriteString("- ")
				builder.WriteString(trimmed)
				builder.WriteByte('\n')
			}
		}
	}
	return strings.TrimSpace(builder.String())
}

func paperclipErrorSnippet(raw []byte) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return "empty response body"
	}
	runes := []rune(trimmed)
	if len(runes) <= paperclipMaxErrorSnippetRunes {
		return trimmed
	}
	return string(runes[:paperclipMaxErrorSnippetRunes]) + "... (truncated)"
}

func normalizePaperclipPriority(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "low", "medium", "high", "urgent":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return "medium"
	}
}

func normalizePaperclipIssueStatus(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "backlog", "todo", "in_progress", "in_review", "blocked", "done", "cancelled":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return ""
	}
}

func normalizePaperclipIssue(raw map[string]any) paperclipIssueResponse {
	return paperclipIssueResponse{
		ID:         stringField(raw, "id"),
		Identifier: stringField(raw, "identifier"),
		Title:      stringField(raw, "title"),
		Status:     stringField(raw, "status"),
		Priority:   stringField(raw, "priority"),
		Raw:        raw,
	}
}

func stringField(raw map[string]any, key string) string {
	if value, ok := raw[key].(string); ok {
		return value
	}
	return ""
}

func buildPaperclipResearchPlan(req paperclipResearchPlanRequest) (paperclipResearchPlanResponse, error) {
	objective := strings.TrimSpace(req.Objective)
	if objective == "" {
		return paperclipResearchPlanResponse{}, errors.New("objective is required")
	}
	domain := strings.TrimSpace(req.Domain)
	if domain == "" {
		domain = "general research"
	}
	depth := normalizePaperclipDepth(req.Depth)
	governance := normalizePaperclipGovernance(req.Governance)
	modelTier := normalizePaperclipModelTier(req.ModelTier)
	heartbeat := "Every 4 hours while active"
	if depth == "deep" {
		heartbeat = "Every 90 minutes while active"
	}
	budgetPolicy := "Use standard tier for planning and verification; keep retrieval bounded by acceptance criteria."
	if req.MaxSpendHint = strings.TrimSpace(req.MaxSpendHint); req.MaxSpendHint != "" {
		budgetPolicy = fmt.Sprintf("Keep total run spend under %s; pause and request approval before exceeding it.", req.MaxSpendHint)
	}

	agents := []paperclipPlanAgent{
		{
			ID:        "research-director",
			Title:     "Research Director",
			Role:      "owner",
			ModelTier: modelTier,
			Heartbeat: heartbeat,
			Responsibilities: []string{
				"Translate the objective into bounded research tasks",
				"Resolve blockers and decide when evidence is sufficient",
				"Escalate scope or spend changes for approval",
			},
		},
		{
			ID:        "evidence-specialist",
			Title:     "Evidence Specialist",
			Role:      "retrieval",
			ReportsTo: "research-director",
			ModelTier: "standard",
			Heartbeat: heartbeat,
			Responsibilities: []string{
				"Run ScholarLM searches and capture source provenance",
				"Separate primary evidence from commentary",
				"Flag missing PDFs, weak abstracts, and provider failures",
			},
		},
		{
			ID:        "verification-reviewer",
			Title:     "Verification Reviewer",
			Role:      "review",
			ReportsTo: "research-director",
			ModelTier: "standard",
			Heartbeat: heartbeat,
			Responsibilities: []string{
				"Check claims against cited sources",
				"Identify contradictions and unsupported conclusions",
				"Approve or return final synthesis for revision",
			},
		},
	}
	if depth == "deep" {
		agents = append(agents, paperclipPlanAgent{
			ID:        "synthesis-writer",
			Title:     "Synthesis Writer",
			Role:      "synthesis",
			ReportsTo: "research-director",
			ModelTier: modelTier,
			Heartbeat: heartbeat,
			Responsibilities: []string{
				"Convert verified evidence into a structured research brief",
				"Preserve citation traceability in every conclusion",
				"Prepare follow-up questions for unresolved evidence gaps",
			},
		})
	}

	issues := []paperclipPlanIssue{
		{
			ClientKey:    "scope",
			Title:        "Define research scope and success criteria",
			Priority:     "high",
			OwnerAgentID: "research-director",
			Description: fmt.Sprintf("Objective: %s\n\nDomain: %s\n\nDecide exact inclusion boundaries, expected outputs, and stop conditions before retrieval starts.",
				objective, domain),
			AcceptanceCriteria: []string{
				"Objective is decomposed into 3-6 research questions",
				"Inclusion and exclusion criteria are explicit",
				"Stop conditions are measurable",
			},
		},
		{
			ClientKey:    "retrieve",
			Title:        "Retrieve and normalize evidence",
			Priority:     "high",
			OwnerAgentID: "evidence-specialist",
			DependsOn:    []string{"scope"},
			Description:  "Run the bounded ScholarLM retrieval plan, normalize result metadata, and retain source provenance for every candidate.",
			AcceptanceCriteria: []string{
				"Every retained source has title, authors, year, provider, and stable identifier when available",
				"Weak or missing metadata is reported separately",
				"Provider failures and fallback paths are logged",
			},
		},
		{
			ClientKey:    "verify",
			Title:        "Verify claims and evidence strength",
			Priority:     "medium",
			OwnerAgentID: "verification-reviewer",
			DependsOn:    []string{"retrieve"},
			Description:  "Review the retrieved evidence for contradictions, unsupported claims, stale sources, and citation quality.",
			AcceptanceCriteria: []string{
				"Each major claim maps to at least one source",
				"Contradictory evidence is summarized instead of discarded",
				"Known limitations are recorded",
			},
		},
		{
			ClientKey:    "synthesize",
			Title:        "Produce verified research brief",
			Priority:     "medium",
			OwnerAgentID: "research-director",
			DependsOn:    []string{"verify"},
			Description:  "Produce the final ScholarLM-facing synthesis with traceable conclusions, evidence gaps, and recommended next searches.",
			AcceptanceCriteria: []string{
				"Final brief includes conclusions, citations, limitations, and next actions",
				"No uncited factual conclusion remains",
				"Reviewer approval gate is complete",
			},
		},
	}
	if depth == "deep" {
		issues[len(issues)-1].OwnerAgentID = "synthesis-writer"
	}

	reviewGates := []string{"Research Director approves scope before retrieval", "Verification Reviewer approves final brief before done"}
	if governance == "approval_required" {
		reviewGates = append(reviewGates, "Human board approval required before external publication or budget increase")
	}

	return paperclipResearchPlanResponse{
		Mission:       objective,
		Domain:        domain,
		Depth:         depth,
		Governance:    governance,
		ModelTier:     modelTier,
		Heartbeat:     heartbeat,
		BudgetPolicy:  budgetPolicy,
		Agents:        agents,
		Issues:        issues,
		ReviewGates:   reviewGates,
		ExportSummary: fmt.Sprintf("%d agents, %d tasks, %d review gates", len(agents), len(issues), len(reviewGates)),
	}, nil
}

func normalizePaperclipDepth(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "quick", "deep":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return "deep"
	}
}

func normalizePaperclipGovernance(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "advisory", "approval_required":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return "approval_required"
	}
}

func normalizePaperclipModelTier(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "light", "standard", "heavy":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return "standard"
	}
}

func RegisterPaperclipIntegrationRoutes(mux *http.ServeMux) {
	client := newPaperclipClientFromEnv()

	mux.HandleFunc("/integrations/paperclip/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodGet,
			})
			return
		}
		status := client.checkHealth(r.Context())
		slog.Info("paperclip health probe completed",
			"component", "paperclip_integration",
			"operation", "health_probe",
			"stage", "finish",
			"provider", "paperclip",
			"result", status.Status,
			"reachable", status.Reachable,
		)
		writeJSONResponse(w, http.StatusOK, status)
	})

	mux.HandleFunc("/integrations/paperclip/issues", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req paperclipCreateIssueRequest
		if err := decodeStrictJSONBody(r.Body, &req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{"error": err.Error()})
			return
		}
		resp, status, err := client.createIssue(r.Context(), req)
		if err != nil {
			code := ErrDependencyFailed
			if status == http.StatusBadRequest {
				code = ErrInvalidParameters
			}
			WriteError(w, status, code, "paperclip issue creation failed", map[string]any{"error": err.Error()})
			return
		}
		writeJSONResponse(w, status, resp)
	})

	mux.HandleFunc("/integrations/paperclip/research-plan", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req paperclipResearchPlanRequest
		if err := decodeStrictJSONBody(r.Body, &req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{"error": err.Error()})
			return
		}
		plan, err := buildPaperclipResearchPlan(req)
		if err != nil {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "paperclip research plan failed", map[string]any{"error": err.Error()})
			return
		}
		slog.Info("paperclip research plan generated",
			"component", "paperclip_integration",
			"operation", "research_plan",
			"stage", "finish",
			"provider", "paperclip",
			"agent_count", len(plan.Agents),
			"issue_count", len(plan.Issues),
			"model_tier", plan.ModelTier,
			"governance", plan.Governance,
		)
		writeJSONResponse(w, http.StatusOK, plan)
	})

	mux.HandleFunc("/integrations/paperclip/research-plan/export", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req paperclipExportPlanRequest
		if err := decodeStrictJSONBody(r.Body, &req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{"error": err.Error()})
			return
		}
		exported, status, err := client.exportPlan(r.Context(), req)
		if err != nil {
			WriteError(w, status, ErrInvalidParameters, "paperclip plan export failed", map[string]any{"error": err.Error()})
			return
		}
		slog.Info("paperclip research plan exported",
			"component", "paperclip_integration",
			"operation", "research_plan_export",
			"stage", "finish",
			"provider", "paperclip",
			"created", exported.Created,
			"failed", exported.Failed,
		)
		writeJSONResponse(w, status, exported)
	})
}
