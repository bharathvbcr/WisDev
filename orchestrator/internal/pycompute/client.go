package pycompute

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/resilience"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/stackconfig"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"golang.org/x/oauth2"
	"google.golang.org/api/idtoken"
)

// Client handles communication with the Python compute plane.
type Client struct {
	baseURL     string
	client      *http.Client
	breaker     *resilience.CircuitBreaker
	tokenSource oauth2.TokenSource
}

type DoclingParseResult struct {
	Paper          map[string]any   `json:"paper,omitempty"`
	FullText       string           `json:"fullText,omitempty"`
	FullTextSnake  string           `json:"full_text,omitempty"`
	Sections       []map[string]any `json:"sections,omitempty"`
	Figures        []map[string]any `json:"figures,omitempty"`
	Tables         []map[string]any `json:"tables,omitempty"`
	References     []map[string]any `json:"references,omitempty"`
	StructureMap   []any            `json:"structureMap,omitempty"`
	StructureSnake []any            `json:"structure_map,omitempty"`
	DoclingMeta    map[string]any   `json:"doclingMeta,omitempty"`
	DoclingSnake   map[string]any   `json:"docling_meta,omitempty"`
	ExtractionInfo map[string]any   `json:"extractionInfo,omitempty"`
}

func resolveBaseURL() string {
	if explicit := strings.TrimSpace(stackconfig.ResolveEnv("PYTHON_SIDECAR_HTTP_URL")); explicit != "" {
		return strings.TrimSuffix(explicit, "/")
	}
	return strings.TrimSuffix(stackconfig.ResolveBaseURL("python_sidecar"), "/")
}

func newTokenSource() oauth2.TokenSource {
	aud := strings.TrimSpace(os.Getenv("GOOGLE_OIDC_AUDIENCE"))
	if aud == "" {
		return nil
	}
	ts, err := idtoken.NewTokenSource(context.Background(), aud)
	if err != nil {
		return nil
	}
	return ts
}

// NewClient creates a new client for the Python sidecar.
func NewClient() *Client {
	return NewClientWithBaseURL(resolveBaseURL())
}

func NewClientWithBaseURL(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
		breaker:     resilience.NewCircuitBreaker("python-compute"),
		tokenSource: newTokenSource(),
	}
}

// DelegateBM25Index requests the Python plane to index documents for lexical search.
func (c *Client) DelegateBM25Index(ctx context.Context, documents []string, docIDs []string) error {
	url := fmt.Sprintf("%s/ml/bm25/index", c.baseURL)
	payload := map[string]any{
		"documents": documents,
		"docIds":    docIDs,
	}

	return c.breaker.Call(ctx, func(innerCtx context.Context) error {
		_, err := c.post(innerCtx, url, payload)
		return err
	})
}

// DelegateBM25Search requests the Python plane to perform lexical ranking.
func (c *Client) DelegateBM25Search(ctx context.Context, query string, topK int) ([]map[string]any, error) {
	url := fmt.Sprintf("%s/ml/bm25/search", c.baseURL)
	payload := map[string]any{
		"query": query,
		"topK":  topK,
	}

	var out []map[string]any
	err := c.breaker.Call(ctx, func(innerCtx context.Context) error {
		resp, err := c.post(innerCtx, url, payload)
		if err != nil {
			return err
		}

		if results, ok := resp["results"].([]any); ok {
			out = make([]map[string]any, 0, len(results))
			for _, result := range results {
				if mapped, ok := result.(map[string]any); ok {
					out = append(out, mapped)
				}
			}
		}
		return nil
	})

	return out, err
}

// GenerateResearchIdeas requests dedicated research-idea generation from the Python compute plane.
func (c *Client) GenerateResearchIdeas(ctx context.Context, payload map[string]any) (map[string]any, error) {
	url := fmt.Sprintf("%s/ml/research/generate-ideas", c.baseURL)

	var result map[string]any
	err := c.breaker.Call(ctx, func(innerCtx context.Context) error {
		resp, err := c.post(innerCtx, url, payload)
		if err != nil {
			return err
		}
		result = resp
		return nil
	})
	return result, err
}

// EmbedTextBatch requests batched embeddings from the Python compute plane.
func (c *Client) EmbedTextBatch(ctx context.Context, texts []string) ([][]float64, error) {
	url := fmt.Sprintf("%s/ml/embed/batch", c.baseURL)
	payload := map[string]any{
		"texts": texts,
	}

	var out [][]float64
	err := c.breaker.Call(ctx, func(innerCtx context.Context) error {
		resp, err := c.post(innerCtx, url, payload)
		if err != nil {
			return err
		}
		rawEmbeddings, ok := resp["embeddings"].([]any)
		if !ok {
			return nil
		}
		out = make([][]float64, 0, len(rawEmbeddings))
		for _, rawEmbedding := range rawEmbeddings {
			vector, ok := rawEmbedding.([]any)
			if !ok {
				continue
			}
			parsed := make([]float64, 0, len(vector))
			for _, item := range vector {
				switch value := item.(type) {
				case float64:
					parsed = append(parsed, value)
				case float32:
					parsed = append(parsed, float64(value))
				case int:
					parsed = append(parsed, float64(value))
				}
			}
			out = append(out, parsed)
		}
		return nil
	})

	return out, err
}

// DoclingParsePDF requests normalized Docling extraction from the Python plane.
func (c *Client) DoclingParsePDF(ctx context.Context, fileName string, fileBytes []byte) (*DoclingParseResult, error) {
	url := fmt.Sprintf("%s/ml/docling/parse", c.baseURL)
	payload := map[string]any{
		"fileBase64": base64.StdEncoding.EncodeToString(fileBytes),
		"fileName":   fileName,
	}

	var result *DoclingParseResult
	err := c.breaker.Call(ctx, func(innerCtx context.Context) error {
		resp, err := c.post(innerCtx, url, payload)
		if err != nil {
			return err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return err
		}
		var parsed DoclingParseResult
		if err := json.Unmarshal(data, &parsed); err != nil {
			return err
		}
		if strings.TrimSpace(parsed.FullText) == "" {
			parsed.FullText = strings.TrimSpace(parsed.FullTextSnake)
		}
		if len(parsed.StructureMap) == 0 {
			parsed.StructureMap = append([]any(nil), parsed.StructureSnake...)
		}
		if len(parsed.DoclingMeta) == 0 {
			parsed.DoclingMeta = cloneAnyMap(parsed.DoclingSnake)
		}
		result = &parsed
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Client) post(ctx context.Context, url string, payload any) (map[string]any, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Caller-Service", "go_orchestrator")

	if c.tokenSource != nil {
		token, err := c.tokenSource.Token()
		if err == nil {
			req.Header.Set("Authorization", "Bearer "+token.AccessToken)
		}
	}
	if key := stackconfig.ResolveInternalServiceKey(); key != "" {
		req.Header.Set("X-Internal-Service-Key", key)
	}

	// Propagate W3C TraceContext.
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("python compute returned %d: %s", resp.StatusCode, string(body))
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	return result, nil
}

func cloneAnyMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}
