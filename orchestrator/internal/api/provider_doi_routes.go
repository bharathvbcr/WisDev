package api

// Provider DOI-resolution route: Go-owned DOI lookup so the (thin) frontend resolves
// DOIs via the backend instead of calling api.openalex.org directly. Part of
// frontend-thinning Phase 2 — replaces openAlexService.getOpenAlexByDoi /
// documentRetrievalService direct OpenAlex DOI calls.
//
// WIRING: call RegisterProviderDOIRoutes(mux) from the server bootstrap alongside the
// other Register*Routes(mux) helpers (see router.go). It takes only *http.ServeMux.
//
// SECURITY: read-only point lookup; register behind rust_gateway auth/rate-limit like
// the other /provider and /search routes.

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
)

// RegisterProviderDOIRoutes registers the Go-owned provider DOI-resolution endpoint.
func RegisterProviderDOIRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/provider/by-doi", handleProviderByDOI)
}

// handleProviderByDOI serves GET /provider/by-doi?doi=<doi> (or POST {"doi":"..."}).
// It resolves a single work through the OpenAlex provider and returns the unified Paper,
// 404 when the DOI is unknown, 400 when no DOI is supplied.
func handleProviderByDOI(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	logAPIRouteLifecycle(r, "api.search", "provider.by_doi", "request_received", "", "result", "accepted")

	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", nil)
		return
	}

	doi := strings.TrimSpace(r.URL.Query().Get("doi"))
	if doi == "" && r.Method == http.MethodPost && r.Body != nil {
		var body struct {
			DOI string `json:"doi"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			doi = strings.TrimSpace(body.DOI)
		}
	}
	if doi == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "doi is required", nil)
		return
	}

	paper, err := search.NewOpenAlexProvider().GetByDOI(r.Context(), doi)
	if err != nil {
		slog.Error("provider doi resolution failed",
			"service", "go_orchestrator",
			"runtime", "go",
			"component", "api",
			"operation", "provider.by_doi",
			"stage", "upstream",
			"provider", "openalex",
			"result", "error",
			"latency_ms", time.Since(start).Milliseconds(),
			"error", err.Error(),
		)
		WriteError(w, http.StatusBadGateway, ErrDependencyFailed, err.Error(), nil)
		return
	}
	if paper == nil {
		WriteError(w, http.StatusNotFound, ErrNotFound, "no work found for doi", map[string]any{"doi": doi})
		return
	}

	logAPIRouteLifecycle(r, "api.search", "provider.by_doi", "response", "",
		"result", "ok",
		"provider", "openalex",
		"latency_ms", time.Since(start).Milliseconds(),
	)
	writeEnvelope(w, "paper", paper)
}
