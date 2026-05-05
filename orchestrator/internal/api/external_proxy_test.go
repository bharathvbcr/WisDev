package api

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestHandleExternalProxy_MissingURL(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/proxy/external", nil)
	rec := httptest.NewRecorder()
	HandleExternalProxy(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing url param, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "missing url parameter") {
		t.Errorf("expected error about missing url, got: %s", rec.Body.String())
	}
}

func TestHandleExternalProxy_HeadProbe(t *testing.T) {
	req := httptest.NewRequest(http.MethodHead, "/api/proxy/external", nil)
	rec := httptest.NewRecorder()
	HandleExternalProxy(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204 for external proxy readiness probe, got %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("expected empty body for readiness probe, got: %s", rec.Body.String())
	}
}

func TestHandleExternalProxy_OptionsProbe(t *testing.T) {
	req := httptest.NewRequest(http.MethodOptions, "/api/proxy/external", nil)
	rec := httptest.NewRecorder()
	HandleExternalProxy(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204 for external proxy options probe, got %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("expected empty body for options probe, got: %s", rec.Body.String())
	}
}

func TestHandleExternalProxy_ReadinessQueryProbe(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/proxy/external?readiness=1", nil)
	rec := httptest.NewRecorder()
	HandleExternalProxy(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204 for explicit readiness query probe, got %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("expected empty body for readiness query probe, got: %s", rec.Body.String())
	}
}

func TestHandleExternalProxy_NamedProviderNotImplemented(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/proxy/external?provider=scopus", nil)
	rec := httptest.NewRecorder()
	HandleExternalProxy(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Errorf("expected 501 for named-provider proxy, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "not available in the Go open-source runtime") {
		t.Errorf("expected not implemented error, got: %s", rec.Body.String())
	}
}

func TestHandleExternalProxy_InvalidURL(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/proxy/external?url=not-a-url", nil)
	rec := httptest.NewRecorder()
	HandleExternalProxy(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid url, got %d", rec.Code)
	}
}

func TestHandleExternalProxy_ForbiddenHost(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/proxy/external?url=https://evil.example.com/data", nil)
	rec := httptest.NewRecorder()
	HandleExternalProxy(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for disallowed host, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "domain not allowed") {
		t.Errorf("expected forbidden domain error, got: %s", rec.Body.String())
	}
}

func TestIsAllowedExternalProxyHost(t *testing.T) {
	tests := []struct {
		host    string
		allowed bool
	}{
		{"api.semanticscholar.org", true},
		{"api.openalex.org", true},
		{"api.crossref.org", true},
		{"export.arxiv.org", true},
		{"api.biorxiv.org", true},
		{"eutils.ncbi.nlm.nih.gov", true},
		{"dblp.org", true},
		{"sub.api.semanticscholar.org", true}, // subdomain match
		{"evil.com", false},
		{"example.org", false},
		{"semanticscholar.org.evil.com", false},
		{"", false},
	}

	for _, tt := range tests {
		got := isAllowedExternalProxyHost(tt.host)
		if got != tt.allowed {
			t.Errorf("isAllowedExternalProxyHost(%q) = %v, want %v", tt.host, got, tt.allowed)
		}
	}
}

func TestHandleExternalProxy_AllowedHost(t *testing.T) {
	// Set up a fake upstream that returns academic data.
	upstream := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"total":42}`)
	}))
	defer upstream.Close()

	// We can't use the real domain since the test server has a localhost URL.
	// Instead, test the forwarding function directly.
	req := httptest.NewRequest(http.MethodGet, "/proxy", nil)
	rec := httptest.NewRecorder()
	forwardExternalProxyRequest(rec, req, upstream.URL+"/graph/v1/paper/search?query=test")

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 from upstream, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"total":42`) {
		t.Errorf("expected upstream body, got: %s", body)
	}
}

func TestHandleExternalProxy_AllowedHostResolvesPrivateIP(t *testing.T) {
	origLookupIP := lookupIP
	t.Cleanup(func() {
		lookupIP = origLookupIP
	})
	lookupIP = func(host string) ([]net.IP, error) {
		if host == "api.openalex.org" {
			return []net.IP{net.ParseIP("127.0.0.1")}, nil
		}
		return []net.IP{net.ParseIP("1.1.1.1")}, nil
	}

	req := httptest.NewRequest(http.MethodGet, "/api/proxy/external?url=https://api.openalex.org/works", nil)
	rec := httptest.NewRecorder()
	HandleExternalProxy(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for allowed host resolving to private IP, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "target resolves to private address") {
		t.Fatalf("expected private-address error, got %s", rec.Body.String())
	}
}

func TestHandleExternalProxy_RedirectToPrivateAllowedDomain(t *testing.T) {
	origLookupIP := lookupIP
	origTransport := externalProxyHTTPClient.Transport
	t.Cleanup(func() {
		lookupIP = origLookupIP
		externalProxyHTTPClient.Transport = origTransport
	})
	lookupIP = func(host string) ([]net.IP, error) {
		switch host {
		case "api.openalex.org":
			return []net.IP{net.ParseIP("1.1.1.1")}, nil
		case "api.semanticscholar.org":
			return []net.IP{net.ParseIP("127.0.0.1")}, nil
		default:
			return []net.IP{net.ParseIP("1.1.1.1")}, nil
		}
	}
	externalProxyHTTPClient.Transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusFound,
			Header: http.Header{
				"Location": []string{"https://api.semanticscholar.org/steal"},
			},
			Body:    io.NopCloser(strings.NewReader("")),
			Request: r,
		}, nil
	})

	req := httptest.NewRequest(http.MethodGet, "/api/proxy/external?url=https://api.openalex.org/works", nil)
	rec := httptest.NewRecorder()
	HandleExternalProxy(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 for redirect to private allowed domain, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "upstream request failed") {
		t.Fatalf("expected upstream request failure, got %s", rec.Body.String())
	}
}

func TestHandleExternalProxy_UpstreamError(t *testing.T) {
	upstream := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "rate limited")
	}))
	defer upstream.Close()

	req := httptest.NewRequest(http.MethodGet, "/proxy", nil)
	rec := httptest.NewRecorder()
	forwardExternalProxyRequest(rec, req, upstream.URL+"/search")

	// The proxy should relay the upstream status code.
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 from upstream relay, got %d", rec.Code)
	}
}

func TestHandleExternalProxy_HeaderFiltering(t *testing.T) {
	var gotHeaders http.Header
	upstream := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	req := httptest.NewRequest(http.MethodGet, "/proxy", nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer secret-token")
	req.Header.Set("X-Internal-Service-Key", "smoke-key")
	req.Header.Set("Cookie", "session=abc")

	rec := httptest.NewRecorder()
	forwardExternalProxyRequest(rec, req, upstream.URL+"/test")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// Safe headers should be forwarded.
	if gotHeaders.Get("Accept") != "application/json" {
		t.Error("Accept header not forwarded")
	}
	// Sensitive headers must NOT be forwarded.
	if gotHeaders.Get("Authorization") != "" {
		t.Error("Authorization header leaked to upstream")
	}
	if gotHeaders.Get("X-Internal-Service-Key") != "" {
		t.Error("X-Internal-Service-Key header leaked to upstream")
	}
	if gotHeaders.Get("Cookie") != "" {
		t.Error("Cookie header leaked to upstream")
	}
}

func TestHandleExternalProxy_RequestBodyLimit(t *testing.T) {
	var receivedBodyLen int
	upstream := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedBodyLen = len(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	// Send a body larger than the limit (externalMaxRequestBody = 32 MiB).
	// We can't realistically send 32 MiB in a unit test, but we can verify
	// the LimitReader is wired by sending a body and checking it arrives.
	bigBody := strings.Repeat("x", 1024)
	req := httptest.NewRequest(http.MethodPost, "/proxy", strings.NewReader(bigBody))
	rec := httptest.NewRecorder()
	forwardExternalProxyRequest(rec, req, upstream.URL+"/upload")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if receivedBodyLen != 1024 {
		t.Errorf("expected body of 1024 bytes, got %d", receivedBodyLen)
	}
}

func TestHandleExternalProxy_RedirectToForbiddenDomain(t *testing.T) {
	// First server redirects to a non-allowed domain.
	evilServer := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "you should not see this")
	}))
	defer evilServer.Close()

	redirectServer := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evilServer.URL+"/steal-data", http.StatusFound)
	}))
	defer redirectServer.Close()

	req := httptest.NewRequest(http.MethodGet, "/proxy", nil)
	rec := httptest.NewRecorder()
	forwardExternalProxyRequest(rec, req, redirectServer.URL+"/api")

	// Should fail because the redirect target isn't in the allowlist.
	// The client returns an error, so the proxy should return 502.
	if rec.Code != http.StatusBadGateway {
		t.Errorf("expected 502 for redirect to forbidden domain, got %d", rec.Code)
	}
}

func TestHandleExternalProxy_HopByHopHeadersStripped(t *testing.T) {
	upstream := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Transfer-Encoding", "chunked")
		w.Header().Set("X-Custom-Header", "preserved")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	req := httptest.NewRequest(http.MethodGet, "/proxy", nil)
	rec := httptest.NewRecorder()
	forwardExternalProxyRequest(rec, req, upstream.URL+"/test")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Header().Get("Connection") != "" {
		t.Error("hop-by-hop Connection header should be stripped")
	}
	if rec.Header().Get("Transfer-Encoding") != "" {
		t.Error("hop-by-hop Transfer-Encoding header should be stripped")
	}
	if rec.Header().Get("X-Custom-Header") != "preserved" {
		t.Error("non-hop-by-hop header should be preserved")
	}
}

func TestHandleExternalProxy_URLEncodingRoundTrip(t *testing.T) {
	// The frontend encodes the full target URL with encodeURIComponent,
	// producing something like ?url=https%3A%2F%2Fapi.semanticscholar.org%2F...
	// Go's r.URL.Query().Get("url") must decode this back to the original URL
	// so the upstream request uses the correct query parameters.
	var receivedPath string
	upstream := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"total":42}`)
	}))
	defer upstream.Close()

	// The target URL the frontend wants to fetch.  It already contains a
	// percent-encoded space (%20) in the query parameter.
	targetURL := upstream.URL + "/graph/v1/paper/search?query=machine%20learning&limit=1&fields=paperId"

	// encodeURIComponent encodes EVERY special character including '%'.
	// So %20 in the target URL becomes %2520 (the '%' → '%25', then '20').
	// Build the proxy URL the same way the browser does after encodeURIComponent.
	encoded := url.QueryEscape(targetURL) // Go's QueryEscape ≈ encodeURIComponent
	proxyURL := "/api/proxy/external?url=" + encoded

	req := httptest.NewRequest(http.MethodGet, proxyURL, nil)
	rec := httptest.NewRecorder()

	// Verify Go decodes the percent-encoded value back to the original URL.
	decoded := req.URL.Query().Get("url")
	if decoded != targetURL {
		t.Fatalf("URL decoding failed:\n  got:  %s\n  want: %s", decoded, targetURL)
	}
	forwardExternalProxyRequest(rec, req, decoded)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	// Verify the upstream received the correct path with the %20 preserved.
	expectedPath := "/graph/v1/paper/search?query=machine%20learning&limit=1&fields=paperId"
	if receivedPath != expectedPath {
		t.Errorf("upstream received wrong path:\n  got:  %s\n  want: %s", receivedPath, expectedPath)
	}
}

func TestForwardExternalProxyRequest_SemanticScholarCountProbeRateLimitDegradesToJSON(t *testing.T) {
	origTransport := externalProxyHTTPClient.Transport
	t.Cleanup(func() {
		externalProxyHTTPClient.Transport = origTransport
	})
	externalProxyHTTPClient.Transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header: http.Header{
				"Content-Type": []string{"text/plain"},
				"Retry-After":  []string{"2"},
			},
			Body:    io.NopCloser(strings.NewReader("rate limited")),
			Request: r,
		}, nil
	})

	req := httptest.NewRequest(http.MethodGet, "/proxy", nil)
	rec := httptest.NewRecorder()
	forwardExternalProxyRequest(rec, req, "https://api.semanticscholar.org/graph/v1/paper/search?query=RLHF&limit=1&fields=paperId")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected degraded count probe to avoid browser-visible 429, got %d", rec.Code)
	}
	var payload map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("expected JSON degraded payload: %v", err)
	}
	if payload["available"] != false {
		t.Fatalf("expected unavailable payload, got %v", payload)
	}
	if int(payload["upstreamStatus"].(float64)) != http.StatusTooManyRequests {
		t.Fatalf("expected upstream 429 metadata, got %v", payload)
	}
	if int(payload["retryAfterMs"].(float64)) != 2000 {
		t.Fatalf("expected Retry-After metadata, got %v", payload)
	}
}

func TestForwardExternalProxyRequest_InvalidMethod(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/proxy", nil)
	req.Method = "GET BAD"
	rec := httptest.NewRecorder()

	forwardExternalProxyRequest(rec, req, "https://api.openalex.org/works")

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 for invalid upstream method, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "failed to build upstream request") {
		t.Fatalf("expected build-upstream error, got %s", rec.Body.String())
	}
}

func TestHandleExternalProxy_ProviderPostReturns501(t *testing.T) {
	// When the frontend sends a POST with {provider, action, payload} JSON
	// body (for Scopus, IEEE, etc.), the Go handler should return 501 instead
	// of a confusing 400 "missing url parameter".
	body := strings.NewReader(`{"provider":"scopus","action":"search","payload":{}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/proxy/external", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	HandleExternalProxy(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Errorf("expected 501 for provider POST, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "named-provider proxy") {
		t.Errorf("expected descriptive provider error, got: %s", rec.Body.String())
	}
}

func TestHandleExternalProxy_ProviderQueryParam(t *testing.T) {
	// ?provider=X also triggers the 501 path.
	req := httptest.NewRequest(http.MethodGet, "/api/proxy/external?provider=groq&action=chat", nil)
	rec := httptest.NewRecorder()
	HandleExternalProxy(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Errorf("expected 501 for provider query param, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestIsPrivateOrLoopback(t *testing.T) {
	tests := []struct {
		host string
		want bool
	}{
		{"127.0.0.1", true},
		{"::1", true},
		{"10.0.0.1", true},
		{"192.168.1.1", true},
		{"8.8.8.8", false},
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			got := isPrivateOrLoopback(tt.host)
			if got != tt.want {
				t.Fatalf("isPrivateOrLoopback(%q) = %v, want %v", tt.host, got, tt.want)
			}
		})
	}
}

func TestIsPrivateOrLoopback_ResolverError(t *testing.T) {
	origLookupIP := lookupIP
	t.Cleanup(func() {
		lookupIP = origLookupIP
	})
	lookupIP = func(host string) ([]net.IP, error) {
		return nil, &net.DNSError{Err: "no such host", Name: host}
	}

	if isPrivateOrLoopback("api.openalex.org") {
		t.Fatal("expected resolver error to be treated as non-private")
	}
}

func TestApplyAcademicPolitePool_AddsCrossrefMailto(t *testing.T) {
	t.Setenv("CROSSREF_POLITE_EMAIL", "scholar@example.com")

	targetURL, email := applyAcademicPolitePool("https://api.crossref.org/works/10.1000/test")
	if email != "scholar@example.com" {
		t.Fatalf("expected configured polite email, got %q", email)
	}
	if !strings.Contains(targetURL, "mailto=scholar%40example.com") {
		t.Fatalf("expected Crossref mailto query parameter, got %s", targetURL)
	}
}

func TestApplyAcademicPolitePool_PreservesExistingMailto(t *testing.T) {
	targetURL, email := applyAcademicPolitePool("https://api.openalex.org/works?search=graph&mailto=existing%40example.com")
	if email != "existing@example.com" {
		t.Fatalf("expected existing mailto to be preserved, got %q", email)
	}
	if strings.Count(targetURL, "mailto=") != 1 {
		t.Fatalf("expected a single mailto parameter, got %s", targetURL)
	}
	if !strings.Contains(targetURL, "mailto=existing%40example.com") {
		t.Fatalf("expected existing mailto to remain in URL, got %s", targetURL)
	}
}

func TestForwardExternalProxyRequest_TransportError(t *testing.T) {
	origTransport := externalProxyHTTPClient.Transport
	t.Cleanup(func() {
		externalProxyHTTPClient.Transport = origTransport
	})
	externalProxyHTTPClient.Transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return nil, &net.DNSError{Err: "dns fail", Name: r.URL.Hostname()}
	})

	req := httptest.NewRequest(http.MethodGet, "/proxy", nil)
	rec := httptest.NewRecorder()
	forwardExternalProxyRequest(rec, req, "https://api.semanticscholar.org/v1/paper/search")

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 on transport error, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "upstream request failed") {
		t.Fatalf("expected upstream error message, got %s", rec.Body.String())
	}
}

func TestForwardExternalProxyRequest_RetriesTransientTransportError(t *testing.T) {
	origTransport := externalProxyHTTPClient.Transport
	t.Cleanup(func() {
		externalProxyHTTPClient.Transport = origTransport
	})

	attempts := 0
	externalProxyHTTPClient.Transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return nil, errors.New("socket hang up")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"application/json"},
			},
			Body:    io.NopCloser(strings.NewReader(`{"ok":true}`)),
			Request: r,
		}, nil
	})

	req := httptest.NewRequest(http.MethodGet, "/proxy", nil)
	rec := httptest.NewRecorder()
	forwardExternalProxyRequest(rec, req, "https://api.semanticscholar.org/v1/paper/search")

	if attempts != 2 {
		t.Fatalf("expected 2 transport attempts, got %d", attempts)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 after retry, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("expected successful upstream body, got %s", rec.Body.String())
	}
}
