package api

// Provider citation-graph route: Go-owned forward (citing) and backward (references)
// lookup so the citation-network UI can stop calling api.semanticscholar.org directly
// through fetchSemanticScholarJson. Part of frontend-thinning Phase 2 (Semantic Scholar cutover).
// Backed by ProviderRegistry.GetCitations / GetReferences (Semantic Scholar preferred;
// healthy-provider fallback).
//
// WIRING: registered in router.go alongside the other searchHandler routes (it is a SearchHandler
// method so it reuses the shared registry via h.resolveRegistry()). SECURITY: register behind
// rust_gateway auth/rate-limit like the other /provider and /search routes.

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
)

// HandleCitations serves GET /provider/citations?paperId=<id>&limit=<n>&direction=<forward|backward>
// (or POST {"paperId","limit","direction"}).
// direction defaults to forward (papers that cite paperId). backward returns papers referenced by paperId.
func (h *SearchHandler) HandleCitations(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	logAPIRouteLifecycle(r, "api.search", "provider.citations", "request_received", "", "result", "accepted")

	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", nil)
		return
	}

	paperID := strings.TrimSpace(firstNonEmptyTrimmed(r.URL.Query().Get("paperId"), r.URL.Query().Get("paperID")))
	direction := strings.TrimSpace(r.URL.Query().Get("direction"))
	limit := 20
	if r.Method == http.MethodPost && r.Body != nil {
		var body struct {
			PaperID   string `json:"paperId"`
			Limit     int    `json:"limit"`
			Direction string `json:"direction"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			if paperID == "" {
				paperID = strings.TrimSpace(body.PaperID)
			}
			if body.Limit > 0 {
				limit = body.Limit
			}
			if direction == "" {
				direction = strings.TrimSpace(body.Direction)
			}
		}
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	if paperID == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "paperId is required", nil)
		return
	}
	if limit > 200 {
		limit = 200
	}

	forward, ok := parseCitationDirection(direction)
	if !ok {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "direction must be forward or backward", map[string]any{
			"direction": direction,
		})
		return
	}

	registry := h.resolveRegistry()
	if registry == nil {
		WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "search registry unavailable", nil)
		return
	}

	var (
		papers []search.Paper
		err    error
	)
	op := "provider.citations"
	graphKind := "forward"
	if forward {
		papers, err = registry.GetCitations(r.Context(), paperID, limit)
	} else {
		op = "provider.references"
		graphKind = "backward"
		papers, err = registry.GetReferences(r.Context(), paperID, limit)
	}
	if err != nil {
		slog.Error("provider citation graph failed",
			"service", "go_orchestrator",
			"runtime", "go",
			"component", "api",
			"operation", op,
			"stage", "upstream",
			"provider", "citation_graph",
			"direction", graphKind,
			"result", "error",
			"latency_ms", time.Since(start).Milliseconds(),
			"error", err.Error(),
		)
		WriteError(w, http.StatusBadGateway, ErrDependencyFailed, err.Error(), nil)
		return
	}

	logAPIRouteLifecycle(r, "api.search", op, "response", "",
		"result", "ok",
		"provider", "citation_graph",
		"direction", graphKind,
		"count", len(papers),
		"latency_ms", time.Since(start).Milliseconds(),
	)
	writeEnvelope(w, "papers", papers)
}

// parseCitationDirection maps query/body direction aliases to forward=true/false.
// Empty direction defaults to forward for backward-compatible clients.
func parseCitationDirection(raw string) (forward bool, ok bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "forward", "citing", "citations", "cite":
		return true, true
	case "backward", "references", "reference", "cited", "refs":
		return false, true
	default:
		return false, false
	}
}
