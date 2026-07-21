package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// allowedExternalProxyHosts is the Go-owned allowlist for academic/tool
// domains that may be proxied.
var allowedExternalProxyHosts = []string{
	"dblp.org",
	"paperswithcode.com",
	"api.openalex.org",
	"api.crossref.org",
	"api.unpaywall.org",
	"api.openretractions.com",
	"export.arxiv.org",
	"api.base-search.net",
	"api.semanticscholar.org",
	"api.biorxiv.org",
	"api.medrxiv.org",
	"www.ebi.ac.uk",
	"eutils.ncbi.nlm.nih.gov",
	"api.zerogpt.com",
	"api.languagetool.org",
}

// safeForwardHeaders are the only request headers forwarded to external APIs.
// Content-Length is omitted: the request body is wrapped in a LimitReader,
// so the original Content-Length may be inaccurate.  net/http recalculates
// it from the actual body when sending.
var safeForwardHeaders = []string{
	"Accept",
	"Accept-Language",
	"Cache-Control",
	"Content-Type",
	"If-Modified-Since",
	"If-None-Match",
	"Pragma",
	"Range",
}

const (
	externalProxyTimeout      = 60 * time.Second
	externalMaxRequestBody    = 32 * 1024 * 1024
	externalMaxResponseLen    = 16 * 1024 * 1024 // 16 MiB
	externalMaxRedirects      = 3
	externalProxyMaxAttempts  = 2
	externalProxyRetryBackoff = 150 * time.Millisecond
	defaultAcademicProxyEmail = "api@wisdev.local"
)

// domainTimeoutOverrides caps upstream latency for domains known to be slow
// or intermittently unreachable, keeping them from blocking the 60 s global
// proxy timeout. The frontend circuit-breaker for OpenRetractions fires after
// ~3.5 s × 2 retries = 7 s, so 8 s gives the proxy time to surface the error
// cleanly without the 60 s wait causing context-canceled noise in logs.
var domainTimeoutOverrides = map[string]time.Duration{
	"api.openretractions.com": 8 * time.Second,
}

var externalProxyHTTPClient = &http.Client{
	Timeout: externalProxyTimeout,
	Transport: &http.Transport{
		MaxIdleConns:        20,
		MaxIdleConnsPerHost: 5,
		IdleConnTimeout:     90 * time.Second,
	},
	// Validate every redirect target against the domain allowlist to prevent
	// SSRF via open-redirect on an allowed domain.
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= externalMaxRedirects {
			return http.ErrUseLastResponse
		}
		redirectHost := strings.ToLower(req.URL.Hostname())
		if !isAllowedExternalProxyHost(redirectHost) {
			slog.Warn("external proxy: blocked redirect to non-allowed host",
				"redirect_host", redirectHost,
				"original_url", via[0].URL.String(),
			)
			return fmt.Errorf("redirect to non-allowed host %q", redirectHost)
		}
		// Block redirects to private/loopback IPs regardless of hostname.
		if isPrivateOrLoopback(redirectHost) {
			slog.Warn("external proxy: blocked redirect to private IP",
				"redirect_host", redirectHost,
			)
			return fmt.Errorf("redirect to private address %q blocked", redirectHost)
		}
		return nil
	},
}

// lookupIP is a narrow seam so tests can exercise private-address and
// resolver-failure branches without touching the network.
var lookupIP = net.LookupIP

// isPrivateOrLoopback returns true if the host resolves to a loopback or
// RFC-1918 private address. This prevents SSRF via DNS rebinding or
// redirects that land on internal infrastructure.
func isPrivateOrLoopback(host string) bool {
	ips, err := lookupIP(host)
	if err != nil {
		return false // can't resolve → let the request fail naturally
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return true
		}
	}
	return false
}

// HandleExternalProxy is the Go-side handler for /api/proxy/external.
// Given a ?url= parameter, it validates the target host against an allowlist
// and proxies the request, returning the upstream response to the caller.
func HandleExternalProxy(w http.ResponseWriter, r *http.Request) {
	targetURLStr := r.URL.Query().Get("url")
	if targetURLStr == "" {
		readinessProbe := r.URL.Query().Get("readiness") == "1"
		// Support lightweight HEAD/OPTIONS probes and an explicit readiness query
		// so local proxies, browser tooling, and dev bootstrap checks do not emit
		// noisy 400s when verifying that the route exists.
		if r.URL.Query().Get("provider") == "" && (r.Method == http.MethodHead || r.Method == http.MethodOptions || readinessProbe) {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// The frontend also uses a POST {provider, action, payload} pattern
		// for named provider proxies (Scopus, IEEE, etc.). The Go
		// orchestrator does not implement this path.
		if r.URL.Query().Get("provider") != "" || r.Method == http.MethodPost {
			WriteError(w, http.StatusNotImplemented, ErrServiceUnavailable,
				"named-provider proxy is not available in the Go open-source runtime", map[string]any{
					"hint": "only the ?url= GET pattern is supported by the Go orchestrator",
				})
			return
		}
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "missing url parameter", nil)
		return
	}

	parsed, err := url.Parse(targetURLStr)
	if err != nil || parsed.Host == "" {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid url parameter", map[string]any{
			"url": firstNChars(targetURLStr, 120),
		})
		return
	}

	host := strings.ToLower(parsed.Hostname())
	if !isAllowedExternalProxyHost(host) {
		slog.Warn("external proxy: domain not in allowlist",
			"host", host,
			"url_preview", firstNChars(targetURLStr, 120),
		)
		WriteError(w, http.StatusForbidden, ErrForbidden, "domain not allowed", map[string]any{
			"host": host,
		})
		return
	}

	// Block requests to private/loopback IPs even if the hostname is allowed
	// (prevents DNS rebinding attacks).
	if isPrivateOrLoopback(host) {
		slog.Warn("external proxy: allowed domain resolves to private IP",
			"host", host,
		)
		WriteError(w, http.StatusForbidden, ErrForbidden, "target resolves to private address", nil)
		return
	}

	forwardExternalProxyRequest(w, r, targetURLStr)
}

func isAllowedExternalProxyHost(host string) bool {
	for _, domain := range allowedExternalProxyHosts {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

func applyAcademicPolitePool(targetURL string) (string, string) {
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return targetURL, ""
	}

	host := strings.ToLower(parsed.Hostname())
	var defaultEmail string
	switch host {
	case "api.crossref.org":
		defaultEmail = strings.TrimSpace(os.Getenv("CROSSREF_POLITE_EMAIL"))
	case "api.openalex.org":
		defaultEmail = strings.TrimSpace(os.Getenv("OPENALEX_EMAIL"))
	default:
		return targetURL, ""
	}
	if defaultEmail == "" {
		defaultEmail = defaultAcademicProxyEmail
	}

	query := parsed.Query()
	email := strings.TrimSpace(query.Get("mailto"))
	if email == "" {
		email = defaultEmail
		query.Set("mailto", email)
		parsed.RawQuery = query.Encode()
	}

	return parsed.String(), email
}

func forwardExternalProxyRequest(w http.ResponseWriter, originalReq *http.Request, targetURL string) {
	// Limit inbound request body to prevent abuse.
	var body io.Reader
	if originalReq.Body != nil {
		body = io.LimitReader(originalReq.Body, externalMaxRequestBody)
	}

	targetURL, politeEmail := applyAcademicPolitePool(targetURL)

	// Apply a per-domain timeout override if the upstream is known to be
	// slow or intermittently unavailable (e.g. api.openretractions.com).
	reqCtx := originalReq.Context()
	parsed, parseErr := url.Parse(targetURL)
	if parseErr == nil {
		host := strings.ToLower(parsed.Hostname())
		if domainTimeout, ok := domainTimeoutOverrides[host]; ok {
			var cancel context.CancelFunc
			reqCtx, cancel = context.WithTimeout(reqCtx, domainTimeout)
			defer cancel()
		}
	}

	var resp *http.Response
	var err error
	var latencyMs int64
	maxAttempts := 1
	if originalReq.Method == http.MethodGet || originalReq.Method == http.MethodHead || originalReq.Method == http.MethodOptions {
		maxAttempts = externalProxyMaxAttempts
	}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Build the outbound request using the same HTTP method as the original.
		outReq, buildErr := http.NewRequestWithContext(reqCtx, originalReq.Method, targetURL, body)
		if buildErr != nil {
			WriteError(w, http.StatusBadGateway, ErrDependencyFailed, "failed to build upstream request", map[string]any{
				"error": buildErr.Error(),
			})
			return
		}

		// Forward only safe headers.
		for _, hdr := range safeForwardHeaders {
			if v := originalReq.Header.Get(hdr); v != "" {
				outReq.Header.Set(hdr, v)
			}
		}
		if politeEmail != "" {
			outReq.Header.Set("User-Agent", fmt.Sprintf("WisDev/1.0 (mailto:%s)", politeEmail))
		} else {
			outReq.Header.Set("User-Agent", "WisDev/1.0 (Go Orchestrator)")
		}

		start := time.Now()
		resp, err = externalProxyHTTPClient.Do(outReq)
		latencyMs = time.Since(start).Milliseconds()
		if err == nil {
			break
		}
		if attempt < maxAttempts && shouldRetryExternalProxyError(originalReq.Method, err) {
			slog.Warn("external proxy: transient upstream failure, retrying",
				"url_preview", firstNChars(targetURL, 120),
				"error", err.Error(),
				"attempt", attempt,
				"latency_ms", latencyMs,
			)
			time.Sleep(externalProxyRetryBackoff * time.Duration(attempt))
			continue
		}
		// Log context-cancellation at debug level: it happens when the
		// frontend's AbortController fires or the per-domain timeout is
		// reached, both of which are expected and handled by the caller.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			slog.Debug("external proxy: upstream request canceled or timed out",
				"url_preview", firstNChars(targetURL, 120),
				"error", err.Error(),
				"latency_ms", latencyMs,
			)
		} else {
			slog.Warn("external proxy: upstream request failed",
				"url_preview", firstNChars(targetURL, 120),
				"error", err.Error(),
				"latency_ms", latencyMs,
			)
		}
		WriteError(w, http.StatusBadGateway, ErrDependencyFailed, "upstream request failed", map[string]any{
			"error": err.Error(),
		})
		return
	}
	defer resp.Body.Close()

	if isSemanticScholarPaperCountProbe(targetURL) && (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError) {
		retryAfterMs := parseRetryAfterMs(resp.Header.Get("Retry-After"))
		slog.Info("external proxy: semantic scholar count probe degraded",
			"url_preview", firstNChars(targetURL, 120),
			"status", resp.StatusCode,
			"retry_after_ms", retryAfterMs,
			"latency_ms", latencyMs,
		)
		writeSemanticScholarCountProbeUnavailable(w, resp.StatusCode, retryAfterMs)
		return
	}

	slog.Info("external proxy: upstream response",
		"url_preview", firstNChars(targetURL, 120),
		"status", resp.StatusCode,
		"latency_ms", latencyMs,
	)

	// Copy safe response headers.
	hopByHop := map[string]bool{
		"Connection":          true,
		"Keep-Alive":          true,
		"Proxy-Authenticate":  true,
		"Proxy-Authorization": true,
		"Te":                  true,
		"Trailer":             true,
		"Transfer-Encoding":   true,
		"Upgrade":             true,
	}
	for key, values := range resp.Header {
		if hopByHop[key] {
			continue
		}
		for _, v := range values {
			w.Header().Add(key, v)
		}
	}

	// Enforce response body size limit.
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, externalMaxResponseLen))
}

func shouldRetryExternalProxyError(method string, err error) bool {
	if err == nil {
		return false
	}
	if !(method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions) {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary()) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "socket hang up") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "unexpected eof") ||
		strings.Contains(msg, "server closed idle connection") ||
		strings.Contains(msg, "use of closed network connection")
}

func isSemanticScholarPaperCountProbe(targetURL string) bool {
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return false
	}
	if strings.ToLower(parsed.Hostname()) != "api.semanticscholar.org" {
		return false
	}
	if parsed.EscapedPath() != "/graph/v1/paper/search" {
		return false
	}
	query := parsed.Query()
	return strings.TrimSpace(query.Get("query")) != "" &&
		query.Get("limit") == "1" &&
		strings.TrimSpace(query.Get("fields")) == "paperId"
}

func writeSemanticScholarCountProbeUnavailable(w http.ResponseWriter, upstreamStatus int, retryAfterMs int) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"total":             0,
		"data":              []any{},
		"ok":                true,
		"available":         false,
		"unavailableReason": "upstream_status",
		"upstreamStatus":    upstreamStatus,
		"retryAfterMs":      retryAfterMs,
	})
}
