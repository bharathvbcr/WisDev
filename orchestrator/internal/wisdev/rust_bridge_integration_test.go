package wisdev

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
)

// closedBridgeURL reserves a loopback port and closes it so the bridge dial
// is refused deterministically — fixed low ports (e.g. :1) can be bound,
// firewalled, or sit in a Windows excluded range and answer differently.
func closedBridgeURL(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve closed port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return "http://" + addr
}

func TestVerifyCitationRecordsSecurelyReturnsTrustAwareRecords(t *testing.T) {
	server := startMockCitationResolveServer(t)
	defer server.Close()
	t.Setenv("RUST_GATEWAY_INTERNAL_URL", server.URL)

	papers := []Source{
		{ID: "p1", Title: "Paper 1", DOI: "10.1000/test-1"},
		{ID: "p2", Title: "Paper 1", DOI: "10.1000/test-1"},
		{ID: "p3", Title: "Paper 3"},
	}

	result, err := VerifyCitationRecordsSecurely(papers)
	assert.NoError(t, err)
	assert.Equal(t, 1, result["validCount"])
	assert.Equal(t, 2, result["invalidCount"])
	assert.Equal(t, 1, result["duplicateCount"])
	assert.Equal(t, 1, result["verifiedCount"])
	assert.Equal(t, 1, result["ambiguousCount"])
	assert.Equal(t, 1, result["rejectedCount"])
	assert.False(t, result["promotionEligible"].(bool))
	assert.NotEmpty(t, result["blockingIssues"])
	assert.NotEmpty(t, result["resolverTrace"])

	records, ok := result["verifiedRecords"].([]map[string]any)
	assert.True(t, ok)
	assert.Len(t, records, 3)
	assert.NotEmpty(t, records[0]["verificationStatus"])
	assert.NotEmpty(t, records[0]["sourceAuthority"])
	assert.NotEmpty(t, records[0]["resolutionEngine"])
	assert.NotEmpty(t, records[0]["provenanceHash"])
	assert.NotNil(t, result["citationTrustBundle"])
}

func TestVerifyCitationRecordsSecurelyRejectsImplicitGoFallback(t *testing.T) {
	t.Setenv("RUST_GATEWAY_INTERNAL_URL", closedBridgeURL(t))
	t.Setenv(allowGoCitationFallbackEnv, "false")

	_, err := VerifyCitationRecordsSecurely([]Source{{ID: "p1", Title: "Paper 1", DOI: "10.1000/test-1"}})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "go fallback disabled")
}

func TestVerifyCitationRecordsSecurelyAllowsExplicitGoFallback(t *testing.T) {
	t.Setenv("RUST_GATEWAY_INTERNAL_URL", closedBridgeURL(t))
	t.Setenv(allowGoCitationFallbackEnv, "true")

	result, err := VerifyCitationRecordsSecurely([]Source{{ID: "p1", Title: "Paper 1", DOI: "10.1000/test-1"}})
	assert.NoError(t, err)
	assert.Equal(t, "go-fallback", result["engine"])
	assert.Equal(t, 1, result["validCount"])
}

func TestResolveCitationBrokerGateConfig(t *testing.T) {
	t.Setenv("RUST_GATEWAY_INTERNAL_URL", "http://127.0.0.1:8787")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")
	t.Setenv(allowGoCitationFallbackEnv, "false")

	cfg := ResolveCitationBrokerGateConfig()
	assert.Equal(t, "http://127.0.0.1:8787", cfg.GatewayURL)
	assert.Equal(t, "strict", cfg.Mode)
	assert.False(t, cfg.AllowGoFallback)
	assert.True(t, cfg.InternalServiceKeyConfigured)

	t.Setenv(allowGoCitationFallbackEnv, "true")
	cfg = ResolveCitationBrokerGateConfig()
	assert.Equal(t, "emergency_go_fallback", cfg.Mode)
	assert.True(t, cfg.AllowGoFallback)
	assert.NotEmpty(t, cfg.Warnings)
}

func TestRustBridgeMainBaseURLAcceptsGatewayOrBridgeBase(t *testing.T) {
	assert.Equal(t,
		"http://127.0.0.1:8080/internal/wisdev-bridge",
		normalizeRustBridgeBaseURL("http://127.0.0.1:8080"),
	)
	assert.Equal(t,
		"http://127.0.0.1:8080/internal/wisdev-bridge",
		normalizeRustBridgeBaseURL("http://127.0.0.1:8080/internal/wisdev-bridge/"),
	)
}

func TestRunRustBridgeMainRejectsEmptyCommand(t *testing.T) {
	err := runRustBridgeMain(" / ", map[string]string{}, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "command is required")
}
