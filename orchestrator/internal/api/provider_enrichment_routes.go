package api

// Provider enrichment routes: Go-owned open-access (Unpaywall), retraction batch
// checks, and OpenAlex bibliometrics — so active frontend callers stop hitting
// provider hosts directly. Part of frontend-thinning Priority 2.
//
// WIRING: RegisterProviderEnrichmentRoutes(mux) from router.go.
// SECURITY: register behind rust_gateway auth/rate-limit like other /provider routes.

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
)

// RegisterProviderEnrichmentRoutes registers open-access, retraction, and bibliometrics endpoints.
func RegisterProviderEnrichmentRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/provider/open-access", handleProviderOpenAccess)
	mux.HandleFunc("/provider/retractions", handleProviderRetractions)
	mux.HandleFunc("/provider/bibliometrics/author", handleProviderAuthorMetrics)
	mux.HandleFunc("/provider/bibliometrics/source", handleProviderSourceMetrics)
}

func handleProviderOpenAccess(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	logAPIRouteLifecycle(r, "api.search", "provider.open_access", "request_received", "", "result", "accepted")

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

	info, err := search.LookupOpenAccess(r.Context(), doi)
	if err != nil {
		slog.Error("provider open-access failed",
			"service", "go_orchestrator",
			"runtime", "go",
			"component", "api",
			"operation", "provider.open_access",
			"stage", "upstream",
			"provider", "unpaywall",
			"result", "error",
			"latency_ms", time.Since(start).Milliseconds(),
			"error", err.Error(),
		)
		status := http.StatusBadGateway
		if strings.Contains(err.Error(), "429") || strings.Contains(err.Error(), "rate limit") {
			status = http.StatusTooManyRequests
		}
		WriteError(w, status, ErrDependencyFailed, err.Error(), nil)
		return
	}
	if info == nil {
		WriteError(w, http.StatusNotFound, ErrNotFound, "no open-access record for doi", map[string]any{"doi": search.NormalizeDOI(doi)})
		return
	}

	logAPIRouteLifecycle(r, "api.search", "provider.open_access", "response", "",
		"result", "ok",
		"provider", info.Source,
		"is_oa", info.IsOA,
		"latency_ms", time.Since(start).Milliseconds(),
	)
	writeEnvelope(w, "openAccess", info)
}

func handleProviderRetractions(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	logAPIRouteLifecycle(r, "api.search", "provider.retractions", "request_received", "", "result", "accepted")

	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	var body struct {
		DOIs []string `json:"dois"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "Failed to parse request body", map[string]any{
			"error": err.Error(),
		})
		return
	}
	if len(body.DOIs) == 0 {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "dois is required", nil)
		return
	}

	results, err := search.CheckRetractionsBatch(r.Context(), body.DOIs)
	if err != nil {
		slog.Error("provider retractions failed",
			"service", "go_orchestrator",
			"runtime", "go",
			"component", "api",
			"operation", "provider.retractions",
			"stage", "validation",
			"result", "error",
			"latency_ms", time.Since(start).Milliseconds(),
			"error", err.Error(),
		)
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, err.Error(), nil)
		return
	}

	logAPIRouteLifecycle(r, "api.search", "provider.retractions", "response", "",
		"result", "ok",
		"count", len(results),
		"latency_ms", time.Since(start).Milliseconds(),
	)
	writeEnvelope(w, "retractions", results)
}

func handleProviderAuthorMetrics(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	logAPIRouteLifecycle(r, "api.search", "provider.bibliometrics.author", "request_received", "", "result", "accepted")

	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", nil)
		return
	}

	authorID := strings.TrimSpace(firstNonEmptyTrimmed(r.URL.Query().Get("authorId"), r.URL.Query().Get("id")))
	if authorID == "" && r.Method == http.MethodPost && r.Body != nil {
		var body struct {
			AuthorID string `json:"authorId"`
			ID       string `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			authorID = strings.TrimSpace(firstNonEmptyTrimmed(body.AuthorID, body.ID))
		}
	}
	if authorID == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "authorId is required", nil)
		return
	}

	metrics := search.LookupAuthorMetrics(r.Context(), authorID)
	if !metrics.Success {
		slog.Warn("provider author metrics lookup failed",
			"service", "go_orchestrator",
			"runtime", "go",
			"component", "api",
			"operation", "provider.bibliometrics.author",
			"stage", "upstream",
			"provider", "openalex",
			"result", "error",
			"latency_ms", time.Since(start).Milliseconds(),
			"error", metrics.Error,
		)
	}
	logAPIRouteLifecycle(r, "api.search", "provider.bibliometrics.author", "response", "",
		"result", "ok",
		"success", metrics.Success,
		"latency_ms", time.Since(start).Milliseconds(),
	)
	writeEnvelope(w, "authorMetrics", metrics)
}

func handleProviderSourceMetrics(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	logAPIRouteLifecycle(r, "api.search", "provider.bibliometrics.source", "request_received", "", "result", "accepted")

	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", nil)
		return
	}

	sourceID := strings.TrimSpace(firstNonEmptyTrimmed(r.URL.Query().Get("sourceId"), r.URL.Query().Get("id")))
	if sourceID == "" && r.Method == http.MethodPost && r.Body != nil {
		var body struct {
			SourceID string `json:"sourceId"`
			ID       string `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			sourceID = strings.TrimSpace(firstNonEmptyTrimmed(body.SourceID, body.ID))
		}
	}
	if sourceID == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "sourceId is required", nil)
		return
	}

	metrics := search.LookupSourceMetrics(r.Context(), sourceID)
	if !metrics.Success {
		slog.Warn("provider source metrics lookup failed",
			"service", "go_orchestrator",
			"runtime", "go",
			"component", "api",
			"operation", "provider.bibliometrics.source",
			"stage", "upstream",
			"provider", "openalex",
			"result", "error",
			"latency_ms", time.Since(start).Milliseconds(),
			"error", metrics.Error,
		)
	}
	logAPIRouteLifecycle(r, "api.search", "provider.bibliometrics.source", "response", "",
		"result", "ok",
		"success", metrics.Success,
		"latency_ms", time.Since(start).Milliseconds(),
	)
	writeEnvelope(w, "sourceMetrics", metrics)
}
