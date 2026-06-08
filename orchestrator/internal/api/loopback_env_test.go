package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func isLoopbackProviderUnavailableMessage(message string) bool {
	normalized := strings.ToLower(strings.TrimSpace(message))
	return strings.Contains(normalized, "requested service provider could not be loaded or initialized")
}

func skipOnLoopbackProviderPanic(t *testing.T, recovered any) {
	t.Helper()
	if recovered == nil {
		return
	}
	if isLoopbackProviderUnavailableMessage(fmt.Sprint(recovered)) {
		t.Skipf("loopback sockets unavailable on this machine: %v", recovered)
	}
	panic(recovered)
}

func newLoopbackTestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()

	var server *httptest.Server
	defer func() {
		skipOnLoopbackProviderPanic(t, recover())
	}()

	server = httptest.NewServer(handler)
	return server
}
