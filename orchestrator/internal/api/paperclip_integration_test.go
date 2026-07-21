package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPaperclipStatusRouteProbesConfiguredInstance(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/health" {
			t.Fatalf("unexpected upstream path %s", r.URL.Path)
		}
		writeJSONResponse(w, http.StatusOK, map[string]any{
			"status":          "ok",
			"version":         "v2026.428.0",
			"deploymentMode":  "local_trusted",
			"bootstrapStatus": "ready",
		})
	}))
	defer upstream.Close()
	t.Setenv("PAPERCLIP_BASE_URL", upstream.URL)

	mux := http.NewServeMux()
	RegisterPaperclipIntegrationRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/integrations/paperclip/status", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got paperclipStatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !got.Reachable || got.Status != "ok" || got.Version != "v2026.428.0" {
		t.Fatalf("unexpected status response: %#v", got)
	}
}

func TestPaperclipCreateIssueRouteMapsScholarLMRequest(t *testing.T) {
	var upstreamPayload map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method %s", r.Method)
		}
		if r.URL.Path != "/api/companies/company-1/issues" {
			t.Fatalf("unexpected upstream path %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&upstreamPayload); err != nil {
			t.Fatalf("decode upstream payload: %v", err)
		}
		writeJSONResponse(w, http.StatusCreated, map[string]any{
			"id":         "issue-1",
			"identifier": "PAP-1",
			"title":      upstreamPayload["title"],
			"status":     "backlog",
			"priority":   upstreamPayload["priority"],
		})
	}))
	defer upstream.Close()
	t.Setenv("PAPERCLIP_BASE_URL", upstream.URL)

	mux := http.NewServeMux()
	RegisterPaperclipIntegrationRoutes(mux)

	body := strings.NewReader(`{"companyId":"company-1","title":"Run evidence audit","description":"Check ScholarLM context","priority":"high"}`)
	req := httptest.NewRequest(http.MethodPost, "/integrations/paperclip/issues", body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if upstreamPayload["description"] != "Check ScholarLM context" || upstreamPayload["priority"] != "high" {
		t.Fatalf("unexpected upstream payload: %#v", upstreamPayload)
	}

	var got paperclipCreateIssueResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !got.OK || got.Issue.ID != "issue-1" || got.Issue.Identifier != "PAP-1" {
		t.Fatalf("unexpected response: %#v", got)
	}
}

func TestPaperclipCreateIssueRejectsMissingCompany(t *testing.T) {
	t.Setenv("PAPERCLIP_BASE_URL", "http://127.0.0.1:3100")
	mux := http.NewServeMux()
	RegisterPaperclipIntegrationRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/integrations/paperclip/issues", strings.NewReader(`{"title":"Missing company"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPaperclipResearchPlanBuildsEmbeddedControlPlaneLogic(t *testing.T) {
	mux := http.NewServeMux()
	RegisterPaperclipIntegrationRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/integrations/paperclip/research-plan", strings.NewReader(`{
		"objective":"Map evidence gaps in AI literature review tools",
		"domain":"academic AI",
		"depth":"deep",
		"governance":"approval_required",
		"modelTier":"heavy",
		"maxSpendHint":"$5"
	}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got paperclipResearchPlanResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Mission == "" || got.Depth != "deep" || got.ModelTier != "heavy" {
		t.Fatalf("unexpected plan identity: %#v", got)
	}
	if len(got.Agents) != 4 || len(got.Issues) != 4 {
		t.Fatalf("expected deep plan with 4 agents and 4 issues, got %d agents %d issues", len(got.Agents), len(got.Issues))
	}
	if got.Issues[1].DependsOn[0] != "scope" {
		t.Fatalf("expected task dependencies to be encoded: %#v", got.Issues[1])
	}
	if !strings.Contains(got.BudgetPolicy, "$5") {
		t.Fatalf("expected spend hint in budget policy, got %q", got.BudgetPolicy)
	}
}

func TestPaperclipResearchPlanRejectsMissingObjective(t *testing.T) {
	mux := http.NewServeMux()
	RegisterPaperclipIntegrationRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/integrations/paperclip/research-plan", strings.NewReader(`{"domain":"biology"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPaperclipResearchPlanExportCreatesEachIssue(t *testing.T) {
	var upstreamPayloads []map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method %s", r.Method)
		}
		if r.URL.Path != "/api/companies/company-1/issues" {
			t.Fatalf("unexpected upstream path %s", r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode upstream payload: %v", err)
		}
		upstreamPayloads = append(upstreamPayloads, payload)
		writeJSONResponse(w, http.StatusCreated, map[string]any{
			"id":       "issue-" + payload["title"].(string),
			"title":    payload["title"],
			"status":   "backlog",
			"priority": payload["priority"],
		})
	}))
	defer upstream.Close()
	t.Setenv("PAPERCLIP_BASE_URL", upstream.URL)

	mux := http.NewServeMux()
	RegisterPaperclipIntegrationRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/integrations/paperclip/research-plan/export", strings.NewReader(`{
		"companyId":"company-1",
		"issues":[
			{
				"clientKey":"scope",
				"title":"Scope work",
				"description":"Define scope",
				"priority":"high",
				"ownerAgentId":"research-director",
				"acceptanceCriteria":["Scope is approved"]
			},
			{
				"clientKey":"retrieve",
				"title":"Retrieve evidence",
				"description":"Collect sources",
				"priority":"medium",
				"ownerAgentId":"evidence-specialist",
				"dependsOn":["scope"],
				"acceptanceCriteria":["Sources include provenance"]
			}
		]
	}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got paperclipExportPlanResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !got.OK || got.Created != 2 || got.Failed != 0 || len(got.Results) != 2 {
		t.Fatalf("unexpected export response: %#v", got)
	}
	if len(upstreamPayloads) != 2 {
		t.Fatalf("expected two upstream calls, got %d", len(upstreamPayloads))
	}
	if !strings.Contains(upstreamPayloads[1]["description"].(string), "Depends on:") {
		t.Fatalf("expected dependency notes in exported description: %#v", upstreamPayloads[1])
	}
	if !strings.Contains(upstreamPayloads[1]["description"].(string), "Acceptance criteria:") {
		t.Fatalf("expected acceptance criteria in exported description: %#v", upstreamPayloads[1])
	}
}

func TestPaperclipResearchPlanExportReturnsPartialFailures(t *testing.T) {
	callCount := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 2 {
			http.Error(w, "upstream rejected task", http.StatusBadGateway)
			return
		}
		writeJSONResponse(w, http.StatusCreated, map[string]any{
			"id":       "issue-ok",
			"title":    "Accepted",
			"status":   "backlog",
			"priority": "high",
		})
	}))
	defer upstream.Close()
	t.Setenv("PAPERCLIP_BASE_URL", upstream.URL)

	mux := http.NewServeMux()
	RegisterPaperclipIntegrationRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/integrations/paperclip/research-plan/export", strings.NewReader(`{
		"companyId":"company-1",
		"issues":[
			{"clientKey":"one","title":"Accepted","description":"ok","priority":"high","ownerAgentId":"research-director","acceptanceCriteria":["ok"]},
			{"clientKey":"two","title":"Rejected","description":"fail","priority":"high","ownerAgentId":"research-director","acceptanceCriteria":["ok"]}
		]
	}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for partial export result, got %d: %s", rec.Code, rec.Body.String())
	}
	var got paperclipExportPlanResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.OK || got.Created != 1 || got.Failed != 1 {
		t.Fatalf("expected partial failure response, got %#v", got)
	}
	if got.Results[1].ClientKey != "two" || got.Results[1].Error == "" {
		t.Fatalf("expected second issue failure to be reported: %#v", got.Results[1])
	}
}

func TestPaperclipResearchPlanExportRejectsUnboundedBatch(t *testing.T) {
	t.Setenv("PAPERCLIP_BASE_URL", "http://127.0.0.1:3100")
	mux := http.NewServeMux()
	RegisterPaperclipIntegrationRoutes(mux)

	var builder strings.Builder
	builder.WriteString(`{"companyId":"company-1","issues":[`)
	for i := 0; i < paperclipMaxExportIssues+1; i++ {
		if i > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(`{"clientKey":"task","title":"Task","description":"Do work","priority":"medium","ownerAgentId":"research-director","acceptanceCriteria":["done"]}`)
	}
	builder.WriteString(`]}`)

	req := httptest.NewRequest(http.MethodPost, "/integrations/paperclip/research-plan/export", strings.NewReader(builder.String()))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}
