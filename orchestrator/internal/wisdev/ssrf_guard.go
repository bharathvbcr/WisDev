package wisdev

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// wisdevLookupIP is a seam so tests can exercise private-address branches
// without performing real DNS lookups.
var wisdevLookupIP = net.LookupIP

// maxPDFFetchRedirects caps the redirect chain when fetching a paper/PDF.
const maxPDFFetchRedirects = 5

// isPrivateOrLoopbackHost reports whether host (a hostname or IP literal)
// refers to a loopback, private (RFC-1918), link-local, or unspecified
// address. It is the SSRF guard that blocks server-side fetches to cloud
// metadata (169.254.169.254), localhost, and internal infrastructure.
func isPrivateOrLoopbackHost(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return isBlockedIP(ip)
	}
	ips, err := wisdevLookupIP(host)
	if err != nil {
		// Cannot resolve → let the request fail naturally rather than allow it
		// to reach an internal target. We only block on a positive match.
		return false
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return true
		}
	}
	return false
}

func isBlockedIP(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified()
}

// validateOutboundFetchURL parses raw and rejects it unless it is an http(s)
// URL whose host does not resolve to a private/loopback/link-local address.
// It returns the normalized URL string on success. This is the chokepoint that
// prevents SSRF via attacker-controlled paper/arxiv source URLs.
func validateOutboundFetchURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("invalid fetch url: %w", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("disallowed fetch url scheme %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("fetch url missing host")
	}
	if isPrivateOrLoopbackHost(parsed.Hostname()) {
		return "", fmt.Errorf("fetch url resolves to a private or loopback address")
	}
	return parsed.String(), nil
}

// secureRedirectPolicy blocks redirects to private/loopback/link-local hosts
// (open-redirect SSRF) and caps the redirect chain. Apply it as the
// CheckRedirect of any HTTP client used to fetch untrusted external URLs.
func secureRedirectPolicy(req *http.Request, via []*http.Request) error {
	if len(via) >= maxPDFFetchRedirects {
		return fmt.Errorf("stopped after %d redirects", maxPDFFetchRedirects)
	}
	scheme := strings.ToLower(req.URL.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("disallowed redirect scheme %q", req.URL.Scheme)
	}
	if isPrivateOrLoopbackHost(req.URL.Hostname()) {
		return fmt.Errorf("blocked redirect to private or loopback address")
	}
	return nil
}
