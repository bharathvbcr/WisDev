package api

// Search-decision route: exposes the Go-owned Quick Mode decision policy
// (quality mode, providers, filters, agentic flag, scope, multi-tab strategy)
// that previously ran in the browser (frontend/services/aiDecisionEngine.ts).
//
// Part of the "thin the frontend, consolidate orchestration in Go" migration
// (Phase 2: search coordination). The frontend calls POST /api/search/decisions
// and renders the returned decisions; on transport failure it proceeds without
// explicit decisions so /search/parallel applies server-side defaults. Policy
// is never recomputed client-side.
//
// SECURITY: read-only policy computation over the query text. Like the other
// /api/config/* routes, this must sit behind rust_gateway auth/rate-limiting;
// add the path to the gateway allowlist when deploying (see docs/ops).

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/policy"
)

const maxSearchDecisionQueryLen = 2000

// RegisterSearchDecisionRoutes registers the Go-owned search-decision endpoint.
func RegisterSearchDecisionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/search/decisions", handleSearchDecisions)
}

type searchDecisionsRequest struct {
	Query            string   `json:"query"`
	PreferredSources []string `json:"preferredSources,omitempty"`
	ResearchField    string   `json:"researchField,omitempty"`
	ResearchMode     string   `json:"researchMode,omitempty"`
	SubscriptionTier string   `json:"subscriptionTier,omitempty"`
	// IncludeMultiTab defaults to true; callers that only need the flat
	// decisions may set it to false to trim the payload.
	IncludeMultiTab *bool `json:"includeMultiTab,omitempty"`
}

// handleSearchDecisions serves POST /api/search/decisions.
func handleSearchDecisions(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	logAPIRouteLifecycle(r, "api.search", "search.decisions", "request_received", "", "result", "accepted")

	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", nil)
		return
	}

	var req searchDecisionsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "invalid JSON body", nil)
		return
	}

	query := strings.TrimSpace(req.Query)
	if query == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", nil)
		return
	}
	if len(query) > maxSearchDecisionQueryLen {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query exceeds maximum length", nil)
		return
	}

	prefs := policy.SearchDecisionPreferences{
		PreferredSources: req.PreferredSources,
		ResearchField:    req.ResearchField,
		ResearchMode:     req.ResearchMode,
		SubscriptionTier: req.SubscriptionTier,
	}
	decisions := policy.DecideSearchParameters(query, prefs)
	if req.IncludeMultiTab != nil && !*req.IncludeMultiTab {
		decisions.MultiTabStrategy = nil
	}

	logAPIRouteLifecycle(r, "api.search", "search.decisions", "response", query,
		"result", "ok",
		"provider", "policy",
		"quality_mode", string(decisions.QualityMode),
		"provider_count", len(decisions.Providers),
		"use_agentic", decisions.UseAgentic,
		"scope", decisions.Scope,
		"query_words", len(strings.Fields(query)),
		"latency_ms", time.Since(start).Milliseconds(),
	)

	writeEnvelope(w, "searchDecisions", decisions)
}
