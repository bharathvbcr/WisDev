package llm

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPTransport_HTTPTimeoutExpiredDeadline(t *testing.T) {
	client := &Client{timeout: 5 * time.Second}

	expiredCtx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Millisecond))
	defer cancel()
	require.Equal(t, time.Millisecond, client.httpTimeoutFor(expiredCtx))

	var nilClient *Client
	require.Equal(t, time.Duration(0), nilClient.httpTimeoutFor(context.Background()))
}

func TestHTTPTransport_GetJSONDecodeFailure(t *testing.T) {
	server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/llm/health", r.URL.Path)
		_, err := w.Write([]byte(`{`))
		require.NoError(t, err)
	}))
	defer server.Close()

	t.Setenv("PYTHON_SIDECAR_HTTP_URL", server.URL)
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	client := NewClient()

	var resp struct {
		Ok bool `json:"ok"`
	}
	err := client.getJSON(context.Background(), "/llm/health", &resp)
	require.Error(t, err)
}

func TestLLMHTTPStream_closeBodyTwice(t *testing.T) {
	stream := newLLMHTTPStream(context.Background(), &http.Response{
		Body: io.NopCloser(strings.NewReader(`{}`)),
	})

	require.NoError(t, stream.closeBody())
	require.NoError(t, stream.closeBody())
}

func TestHTTPTransport_HelperBranches(t *testing.T) {
	client := &Client{
		httpBaseURL: "https://python-sidecar.example.com/api/",
	}
	assert.Equal(t, "https://python-sidecar.example.com/api/llm/health", client.buildURL("/llm/health"))

	stream := newLLMHTTPStream(context.Background(), &http.Response{
		Body: io.NopCloser(strings.NewReader(`{"chunk":{"delta":"ok","done":true,"finishReason":"stop"}}`)),
	})
	require.NoError(t, stream.CloseSend())
	_, err := stream.Recv()
	assert.ErrorIs(t, err, io.EOF)
	require.NoError(t, stream.CloseSend())

	streamNoBody := newLLMHTTPStream(context.Background(), &http.Response{
		Trailer: http.Header{
			"X-Trace-Id": {"abc-123"},
		},
	})
	require.NoError(t, streamNoBody.CloseSend())
	assert.Equal(t, []string{"abc-123"}, streamNoBody.Trailer()["x-trace-id"])

	assert.Nil(t, cloneStringMap(map[string]string{}))

	cloned := cloneStringMap(map[string]string{"a": "1", "b": "2"})
	assert.Equal(t, map[string]string{"a": "1", "b": "2"}, cloned)
}

func TestHTTPTransport_ErrorHelperBranches(t *testing.T) {
	t.Run("firstNonEmptyHTTPErrorString ignores non-string and empty values", func(t *testing.T) {
		require.Equal(t, "", firstNonEmptyHTTPErrorString(nil, 12, "", "  ", map[string]any{}))
		require.Equal(t, "v1", firstNonEmptyHTTPErrorString("", " ", "v1", "v2"))
	})

	t.Run("httpHeaderToMetadata lower-cases and filters headers", func(t *testing.T) {
		md := httpHeaderToMetadata(http.Header{
			"X-Trace-Id": {"abc"},
			"  ":         {"bad"},
			"":           {"empty"},
			"X-List":     {"1", "2"},
		})
		assert.Equal(t, []string{"abc"}, md["x-trace-id"])
		assert.Equal(t, []string{"1", "2"}, md["x-list"])
		_, has := md[""]
		assert.False(t, has)
	})

	t.Run("decodeHTTPErrorValue covers nested fallback code and non-string", func(t *testing.T) {
		msg, code := decodeHTTPErrorValue(map[string]any{
			"error": map[string]any{
				"detail": map[string]any{
					"message": "rate limited",
				},
				"code": "RATE_LIMITED",
			},
		}, "fallback")
		require.Equal(t, "rate limited", msg)
		require.Equal(t, "RATE_LIMITED", code)

		msg, code = decodeHTTPErrorValue(map[string]any{
			"message": nil,
			"code":    "CODE",
			"detail":  "   ",
		}, "")
		require.Equal(t, "", msg)
		require.Equal(t, "", code)
	})
}
