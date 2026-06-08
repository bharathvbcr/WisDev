package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/paper"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/resilience"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/stackconfig"
)

type PaperProfiler interface {
	ExtractProfile(ctx context.Context, paper search.Paper) (*paper.Profile, error)
}

type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

type PaperHandler struct {
	profiler      PaperProfiler
	pythonBaseURL string
	httpClient    HTTPClient

	paperCountMu            sync.Mutex
	paperCountCache         map[string]paperCountCacheEntry
	paperCountBackoffUntil  time.Time
	paperCountBackoffStatus int
}

var (
	paperProfileRequestTimeout  = 45 * time.Second
	paperExternalRequestTimeout = 45 * time.Second
	paperCountRequestTimeout    = 4 * time.Second
)

const (
	paperCountSuccessCacheTTL = 5 * time.Minute
	paperCountDefaultBackoff  = 30 * time.Second
	paperCountMaxBackoff      = 5 * time.Minute
)

type paperCountCacheEntry struct {
	response  paperCountAPIResponse
	expiresAt time.Time
}

func NewPaperHandler(profiler PaperProfiler, pythonBaseURL string) *PaperHandler {
	return &PaperHandler{
		profiler:        profiler,
		pythonBaseURL:   pythonBaseURL,
		httpClient:      http.DefaultClient,
		paperCountCache: make(map[string]paperCountCacheEntry),
	}
}

func (h *PaperHandler) SetHTTPClient(client HTTPClient) {
	h.httpClient = client
}

func paperProfileContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if paperProfileRequestTimeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, paperProfileRequestTimeout)
}

func paperExternalContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if paperExternalRequestTimeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, paperExternalRequestTimeout)
}

func paperCountContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if paperCountRequestTimeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, paperCountRequestTimeout)
}

func (h *PaperHandler) HandleProfile(w http.ResponseWriter, r *http.Request) {
	if IsDegraded(r.Context()) {
		WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "Service is currently in degraded mode", nil)
		return
	}

	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "Method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	var paperData search.Paper
	if err := json.NewDecoder(r.Body).Decode(&paperData); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "Failed to parse request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	profileCtx, profileCancel := paperProfileContext(r.Context())
	defer profileCancel()

	profile, err := h.profiler.ExtractProfile(profileCtx, paperData)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, ErrDependencyFailed, "Extraction failed", map[string]any{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(profile)
}

func (h *PaperHandler) HandleExtractPDF(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	var pdfData []byte
	var fileName string
	var err error

	// 1. Check for file upload
	file, header, err := r.FormFile("file")
	if err == nil {
		defer file.Close()
		fileName = header.Filename
		pdfData, err = io.ReadAll(file)
	} else {
		// 2. Check for URL in body
		var req struct {
			URL string `json:"url"`
		}
		if decodeErr := json.NewDecoder(r.Body).Decode(&req); decodeErr == nil && req.URL != "" {
			fetchCtx, fetchCancel := paperExternalContext(r.Context())
			defer fetchCancel()
			fetchReq, _ := http.NewRequestWithContext(fetchCtx, "GET", req.URL, nil)
			resp, fetchErr := h.httpClient.Do(fetchReq)
			if fetchErr != nil {
				WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to fetch pdf", map[string]any{
					"error": fetchErr.Error(),
				})
				return
			}
			defer resp.Body.Close()
			fileName = "paper.pdf"
			pdfData, err = io.ReadAll(resp.Body)
		} else {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "provide file upload or url", nil)
			return
		}
	}

	if err != nil {
		WriteError(w, http.StatusInternalServerError, ErrInternal, "failed to read pdf data", map[string]any{
			"error": err.Error(),
		})
		return
	}

	// 3. Call Python sidecar compatibility upload endpoint for high-quality extraction.
	pythonURL := h.pythonBaseURL + "/extract-pdf"
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, ErrInternal, "failed to build extraction request", map[string]any{
			"error": err.Error(),
		})
		return
	}
	if _, err := part.Write(pdfData); err != nil {
		WriteError(w, http.StatusInternalServerError, ErrInternal, "failed to build extraction request", map[string]any{
			"error": err.Error(),
		})
		return
	}
	if err := writer.Close(); err != nil {
		WriteError(w, http.StatusInternalServerError, ErrInternal, "failed to finalize extraction request", map[string]any{
			"error": err.Error(),
		})
		return
	}

	pyCtx, pyCancel := paperExternalContext(r.Context())
	defer pyCancel()
	pyReq, _ := http.NewRequestWithContext(pyCtx, "POST", pythonURL, body)
	pyReq.Header.Set("Content-Type", writer.FormDataContentType())
	if key := stackconfig.ResolveInternalServiceKey(); key != "" {
		pyReq.Header.Set("X-Internal-Service-Key", key)
	}

	pyResp, pyErr := h.httpClient.Do(pyReq)
	if pyErr != nil {
		// Fallback to local extraction if Python is down
		text, fallbackErr := paper.ExtractPDFText(bytes.NewReader(pdfData), int64(len(pdfData)))
		if fallbackErr != nil {
			WriteError(w, http.StatusInternalServerError, ErrDependencyFailed, "pdf extraction failed", map[string]any{
				"pythonError":   pyErr.Error(),
				"fallbackError": fallbackErr.Error(),
			})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"text":       text,
			"char_count": len(text),
			"source":     "fallback-local",
		})
		return
	}
	defer pyResp.Body.Close()

	if pyResp.StatusCode != http.StatusOK {
		snippetBytes, _ := io.ReadAll(io.LimitReader(pyResp.Body, 512))
		WriteError(w, http.StatusBadGateway, ErrDependencyFailed, "python sidecar returned unexpected status", map[string]any{
			"status":              pyResp.StatusCode,
			"responseBodySnippet": strings.TrimSpace(string(snippetBytes)),
		})
		return
	}

	var result map[string]any
	if err := json.NewDecoder(pyResp.Body).Decode(&result); err != nil {
		WriteError(w, http.StatusBadGateway, ErrDependencyFailed, "failed to decode python response", map[string]any{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

type semanticScholarCountResponse struct {
	Total int `json:"total"`
}

type paperCountAPIResponse struct {
	OK                bool   `json:"ok"`
	Query             string `json:"query"`
	Source            string `json:"source"`
	Available         bool   `json:"available"`
	Count             int    `json:"count"`
	UnavailableReason string `json:"unavailableReason,omitempty"`
	UpstreamStatus    int    `json:"upstreamStatus,omitempty"`
	RetryAfterMs      int    `json:"retryAfterMs,omitempty"`
}

func parseRetryAfterMs(header string) int {
	value := strings.TrimSpace(header)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return seconds * 1000
	}
	if retryAt, err := http.ParseTime(value); err == nil {
		ms := int(time.Until(retryAt).Milliseconds())
		if ms > 0 {
			return ms
		}
	}
	return 0
}

func writePaperCountResponse(w http.ResponseWriter, response paperCountAPIResponse) {
	w.Header().Set("Content-Type", "application/json")
	if !response.Available {
		w.Header().Set("Cache-Control", "no-store")
	}
	_ = json.NewEncoder(w).Encode(response)
}

func newPaperCountUnavailableResponse(query string, reason string, upstreamStatus int, retryAfterMs int) paperCountAPIResponse {
	return paperCountAPIResponse{
		OK:                true,
		Query:             query,
		Source:            "semantic_scholar",
		Available:         false,
		UnavailableReason: reason,
		UpstreamStatus:    upstreamStatus,
		RetryAfterMs:      retryAfterMs,
	}
}

func writePaperCountUnavailable(w http.ResponseWriter, query string, reason string, upstreamStatus int, retryAfterMs int) {
	writePaperCountResponse(w, newPaperCountUnavailableResponse(query, reason, upstreamStatus, retryAfterMs))
}

func normalizePaperCountCacheKey(query string) string {
	return strings.ToLower(strings.Join(strings.Fields(query), " "))
}

func (h *PaperHandler) getPaperCountCached(query string, now time.Time) (paperCountAPIResponse, bool) {
	key := normalizePaperCountCacheKey(query)

	h.paperCountMu.Lock()
	defer h.paperCountMu.Unlock()

	if h.paperCountCache == nil {
		h.paperCountCache = make(map[string]paperCountCacheEntry)
	}
	if entry, ok := h.paperCountCache[key]; ok {
		if now.Before(entry.expiresAt) {
			response := entry.response
			response.Query = query
			return response, true
		}
		delete(h.paperCountCache, key)
	}

	if now.Before(h.paperCountBackoffUntil) {
		retryAfterMs := int(time.Until(h.paperCountBackoffUntil).Milliseconds())
		if retryAfterMs < 1 {
			retryAfterMs = 1
		}
		return newPaperCountUnavailableResponse(query, "upstream_backoff", h.paperCountBackoffStatus, retryAfterMs), true
	}

	return paperCountAPIResponse{}, false
}

func (h *PaperHandler) cachePaperCountSuccess(query string, response paperCountAPIResponse, now time.Time) {
	key := normalizePaperCountCacheKey(query)

	h.paperCountMu.Lock()
	defer h.paperCountMu.Unlock()

	if h.paperCountCache == nil {
		h.paperCountCache = make(map[string]paperCountCacheEntry)
	}
	h.paperCountCache[key] = paperCountCacheEntry{
		response:  response,
		expiresAt: now.Add(paperCountSuccessCacheTTL),
	}
	h.paperCountBackoffUntil = time.Time{}
	h.paperCountBackoffStatus = 0
}

func (h *PaperHandler) openPaperCountBackoff(upstreamStatus int, retryAfterMs int, now time.Time) {
	backoff := paperCountDefaultBackoff
	if retryAfterMs > 0 {
		backoff = time.Duration(retryAfterMs) * time.Millisecond
	}
	if backoff > paperCountMaxBackoff {
		backoff = paperCountMaxBackoff
	}

	h.paperCountMu.Lock()
	defer h.paperCountMu.Unlock()

	next := now.Add(backoff)
	if next.After(h.paperCountBackoffUntil) {
		h.paperCountBackoffUntil = next
		h.paperCountBackoffStatus = upstreamStatus
	}
}

func (h *PaperHandler) HandleCount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodGet,
		})
		return
	}

	query := strings.TrimSpace(r.URL.Query().Get("query"))
	if len(query) < 2 {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", map[string]any{
			"field": "query",
		})
		return
	}

	now := time.Now()
	if cached, ok := h.getPaperCountCached(query, now); ok {
		writePaperCountResponse(w, cached)
		return
	}

	countCtx, countCancel := paperCountContext(r.Context())
	defer countCancel()

	apiKey, _ := resilience.GetSecret(countCtx, "SEMANTIC_SCHOLAR_API_KEY")
	fetchURL := fmt.Sprintf("https://api.semanticscholar.org/graph/v1/paper/search?query=%s&limit=1&fields=paperId",
		url.QueryEscape(query))
	req, _ := http.NewRequestWithContext(countCtx, http.MethodGet, fetchURL, nil)
	if apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "WisDev/1.0 (paper-count)")

	start := time.Now()
	resp, err := h.httpClient.Do(req)
	latencyMs := time.Since(start).Milliseconds()
	if err != nil {
		slog.Warn("paper count: semantic scholar request failed",
			"component", "api.paper",
			"operation", "paper_count",
			"stage", "upstream_failed",
			"provider", "semantic_scholar",
			"latency_ms", latencyMs,
			"error", err.Error(),
		)
		h.openPaperCountBackoff(0, 0, now)
		writePaperCountUnavailable(w, query, "upstream_error", 0, 0)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		retryAfterMs := parseRetryAfterMs(resp.Header.Get("Retry-After"))
		level := slog.LevelWarn
		if resp.StatusCode == http.StatusTooManyRequests {
			level = slog.LevelInfo
		}
		slog.Log(r.Context(), level, "paper count: semantic scholar unavailable",
			"component", "api.paper",
			"operation", "paper_count",
			"stage", "upstream_unavailable",
			"provider", "semantic_scholar",
			"status", resp.StatusCode,
			"retry_after_ms", retryAfterMs,
			"latency_ms", latencyMs,
		)
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError {
			h.openPaperCountBackoff(resp.StatusCode, retryAfterMs, now)
		}
		writePaperCountUnavailable(w, query, "upstream_status", resp.StatusCode, retryAfterMs)
		return
	}

	var payload semanticScholarCountResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		slog.Warn("paper count: failed to decode semantic scholar response",
			"component", "api.paper",
			"operation", "paper_count",
			"stage", "decode_failed",
			"provider", "semantic_scholar",
			"latency_ms", latencyMs,
			"error", err.Error(),
		)
		writePaperCountUnavailable(w, query, "decode_error", http.StatusOK, 0)
		return
	}

	response := paperCountAPIResponse{
		OK:        true,
		Query:     query,
		Source:    "semantic_scholar",
		Available: true,
		Count:     payload.Total,
	}
	h.cachePaperCountSuccess(query, response, now)
	writePaperCountResponse(w, response)
}

func (h *PaperHandler) HandleExportMarkdown(w http.ResponseWriter, r *http.Request) {
	var req paper.ExportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "Invalid request", nil)
		return
	}
	data := paper.GenerateMarkdown(req)
	w.Header().Set("Content-Type", "text/markdown")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s.md", req.DraftID))
	w.Write([]byte(data))
}

func (h *PaperHandler) HandleExportHTML(w http.ResponseWriter, r *http.Request) {
	var req paper.ExportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "Invalid request", nil)
		return
	}
	data := paper.GenerateHTML(req)
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(data))
}

func (h *PaperHandler) HandleExportLaTeX(w http.ResponseWriter, r *http.Request) {
	var req paper.ExportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "Invalid request", nil)
		return
	}
	data := paper.GenerateLaTeX(req)
	w.Header().Set("Content-Type", "application/x-tex")
	w.Write([]byte(data))
}

func (h *PaperHandler) HandleGetPaper(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodGet,
		})
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" {
		id = r.URL.Query().Get("paperId")
	}
	if id == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "id or paperId required", nil)
		return
	}

	identifier := id
	if strings.Contains(id, "/") && !strings.HasPrefix(id, "DOI:") {
		identifier = "DOI:" + id
	}

	fetchCtx, fetchCancel := paperExternalContext(r.Context())
	defer fetchCancel()

	apiKey, _ := resilience.GetSecret(fetchCtx, "SEMANTIC_SCHOLAR_API_KEY")
	fetchUrl := fmt.Sprintf("https://api.semanticscholar.org/graph/v1/paper/%s?fields=paperId,externalIds,title,url,abstract,authors,year,venue,citationCount,influentialCitationCount,referenceCount,openAccessPdf,fieldsOfStudy",
		url.QueryEscape(identifier))

	req, _ := http.NewRequestWithContext(fetchCtx, http.MethodGet, fetchUrl, nil)
	if apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		WriteError(w, http.StatusBadGateway, ErrDependencyFailed, "semantic scholar request failed", map[string]any{
			"error": err.Error(),
		})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		WriteError(w, resp.StatusCode, ErrDependencyFailed, "semantic scholar returned unexpected status", map[string]any{
			"status": resp.StatusCode,
		})
		return
	}

	var paperData any
	json.NewDecoder(resp.Body).Decode(&paperData)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(paperData)
}

func (h *PaperHandler) HandleGetNetwork(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodGet,
		})
		return
	}

	paperId := r.URL.Query().Get("paperId")
	if paperId == "" {
		paperId = r.URL.Query().Get("id")
	}
	if paperId == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "paperId required", map[string]any{
			"field": "paperId",
		})
		return
	}

	fetchCtx, fetchCancel := paperExternalContext(r.Context())
	defer fetchCancel()

	apiKey, _ := resilience.GetSecret(fetchCtx, "SEMANTIC_SCHOLAR_API_KEY")
	fetchUrl := fmt.Sprintf("https://api.semanticscholar.org/graph/v1/paper/%s?fields=paperId,title,year,citationCount,citations.paperId,citations.title,citations.year,references.paperId,references.title,references.year",
		url.QueryEscape(paperId))

	req, _ := http.NewRequestWithContext(fetchCtx, http.MethodGet, fetchUrl, nil)
	if apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		WriteError(w, http.StatusBadGateway, ErrDependencyFailed, "semantic scholar request failed", map[string]any{
			"error": err.Error(),
		})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		WriteError(w, resp.StatusCode, ErrDependencyFailed, "semantic scholar returned unexpected status", map[string]any{
			"status": resp.StatusCode,
		})
		return
	}

	var data any
	json.NewDecoder(resp.Body).Decode(&data)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}
