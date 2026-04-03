package wisdev

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/llm"
	llmv1 "github.com/wisdev-agent/wisdev-agent-os/orchestrator/proto/llm/v1"
)

// Paper2SkillCompiler compiles an arXiv paper into a SkillSchema via PDF extraction + LLM.
type Paper2SkillCompiler struct {
	LLM              LLMRequester
	HTTPClient       *http.Client
	PDFSourceBaseURL string
	RegistryURL      string
	PDFWorkerURL     string // URL of the Python /ml/pdf endpoint
}

// NewPaper2SkillCompiler creates a Paper2SkillCompiler with default HTTP timeouts and sidecar URLs.
// Sidecar base URL is resolved via ResolvePythonBase() so that the PYTHON_SIDECAR_URL
// environment variable is respected in all deployment environments.
func NewPaper2SkillCompiler(llm LLMRequester) *Paper2SkillCompiler {
	base := ResolvePythonBase()
	return &Paper2SkillCompiler{
		LLM:              llm,
		HTTPClient:       &http.Client{Timeout: 30 * time.Second},
		PDFSourceBaseURL: "https://arxiv.org/pdf/",
		RegistryURL:      base + "/skills/register",
		PDFWorkerURL:     base + "/ml/pdf",
	}
}

// CompileArxivID fetches the paper PDF, extracts the methodology via LLM, compiles a SkillSchema,
// and registers the skill. On any intermediate failure it returns a degraded schema (non-nil, no error).
func (c *Paper2SkillCompiler) CompileArxivID(ctx context.Context, arxivID string) (SkillSchema, error) {
	extracted, err := c.fetchPDFExtraction(ctx, arxivID)
	degraded := SkillSchema{
		Name:        "degraded_skill_" + arxivID,
		Description: "Degraded: could not compile methodology from " + arxivID,
		SourcePaper: extracted.SourcePaper(arxivID),
	}

	if err != nil {
		slog.Warn("paper2skill: PDF fetch failed, using degraded schema", "arxiv_id", arxivID, "error", err)
		return degraded, nil
	}

	paperText := extracted.FullText()
	if paperText == "" {
		paperText = extracted.Paper.Abstract
	}
	if paperText == "" {
		slog.Warn("paper2skill: extracted paper text was empty, using degraded schema", "arxiv_id", arxivID)
		return degraded, nil
	}

	// Step 2: Extract methodology section
	methResp, err := c.LLM.StructuredOutput(ctx, &llmv1.StructuredRequest{
		Prompt: fmt.Sprintf(`Extract the methodology section from this academic paper.
Return JSON: {"methodology": "<description, max 500 words>"}

Title: %s
Abstract: %s

Paper text (first 4000 chars):
%s`, extracted.TitleOrFilename(arxivID), extracted.Paper.Abstract, truncate(paperText, 4000)),
		JsonSchema: `{"type":"object","properties":{"methodology":{"type":"string"}},"required":["methodology"]}`,
		Model:      llm.ResolveHeavyModel(),
	})
	if err != nil {
		slog.Warn("paper2skill: methodology extraction failed", "error", err)
		return degraded, nil
	}
	var methResult struct {
		Methodology string `json:"methodology"`
	}
	if err := json.Unmarshal([]byte(methResp.JsonResult), &methResult); err != nil || methResult.Methodology == "" {
		slog.Warn("paper2skill: methodology parse failed, using degraded schema", "error", err)
		return degraded, nil
	}

	// Step 3: Compile to SkillSchema
	skillResp, err := c.LLM.StructuredOutput(ctx, &llmv1.StructuredRequest{
		Prompt: fmt.Sprintf(`Compile a research methodology into a reusable agent SkillSchema.
Paper title: %s
Methodology: %s
ArXiv ID: %s

Return JSON matching SkillSchema: {"name":"<snake_case>","description":"<one sentence>","inputs":[],"outputs":[],"steps":["..."],"code_template":"","source_paper":{"arxiv_id":"%s"}}`,
			extracted.TitleOrFilename(arxivID), methResult.Methodology, arxivID, arxivID),
		JsonSchema: `{"type":"object","properties":{"name":{"type":"string"},"description":{"type":"string"},"inputs":{"type":"array"},"outputs":{"type":"array"},"steps":{"type":"array","items":{"type":"string"}},"code_template":{"type":"string"},"source_paper":{"type":"object"}},"required":["name","description","steps"]}`,
		Model:      llm.ResolveHeavyModel(),
	})
	if err != nil {
		slog.Warn("paper2skill: skill compilation failed", "error", err)
		return degraded, nil
	}

	var schema SkillSchema
	if jsonErr := json.Unmarshal([]byte(skillResp.JsonResult), &schema); jsonErr != nil {
		return degraded, nil
	}

	if strings.TrimSpace(schema.SourcePaper.ArxivID) == "" {
		schema.SourcePaper = extracted.SourcePaper(arxivID)
	}
	if strings.TrimSpace(schema.Name) == "" {
		schema.Name = "skill_" + strings.ReplaceAll(strings.ToLower(arxivID), "/", "_")
	}
	if strings.TrimSpace(schema.Description) == "" {
		schema.Description = "Compiled from methodology extracted from " + arxivID
	}

	_ = c.RegisterSkill(ctx, schema) // Non-fatal if registry is down
	return schema, nil
}

type pdfExtractionPaper struct {
	Title       string   `json:"title"`
	DOI         string   `json:"doi"`
	Abstract    string   `json:"abstract"`
	Authors     []string `json:"authors"`
	SourceApis  []string `json:"sourceApis"`
	PublishDate struct {
		Year int `json:"year"`
	} `json:"publishDate"`
}

type pdfExtractionResponse struct {
	Paper         pdfExtractionPaper `json:"paper"`
	FullTextJSON  string             `json:"fullText"`
	FullTextSnake string             `json:"full_text"`
}

func (r pdfExtractionResponse) FullText() string {
	text := strings.TrimSpace(r.FullTextJSON)
	if text != "" {
		return text
	}
	return strings.TrimSpace(r.FullTextSnake)
}

func (r pdfExtractionResponse) SourcePaper(arxivID string) CitationRecord {
	return CitationRecord{
		ArxivID:  arxivID,
		Title:    strings.TrimSpace(r.Paper.Title),
		Authors:  append([]string(nil), r.Paper.Authors...),
		Year:     r.Paper.PublishDate.Year,
		DOI:      strings.TrimSpace(r.Paper.DOI),
		Abstract: strings.TrimSpace(r.Paper.Abstract),
	}
}

func (r pdfExtractionResponse) TitleOrFilename(arxivID string) string {
	if title := strings.TrimSpace(r.Paper.Title); title != "" {
		return title
	}
	return arxivID
}

func (c *Paper2SkillCompiler) fetchPDFExtraction(ctx context.Context, arxivID string) (pdfExtractionResponse, error) {
	sourceURL, err := c.resolvePDFSourceURL(arxivID)
	if err != nil {
		return pdfExtractionResponse{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return pdfExtractionResponse{}, err
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return pdfExtractionResponse{}, err
	}
	defer func() { io.Copy(io.Discard, resp.Body); resp.Body.Close() }() //nolint:errcheck
	if resp.StatusCode >= 300 {
		return pdfExtractionResponse{}, fmt.Errorf("pdf source returned status %d", resp.StatusCode)
	}

	pdfBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return pdfExtractionResponse{}, err
	}
	if len(pdfBytes) == 0 {
		return pdfExtractionResponse{}, fmt.Errorf("pdf source returned empty body")
	}

	workerURL := c.PDFWorkerURL
	if workerURL == "" {
		workerURL = "http://python-sidecar:8080/ml/pdf"
	}

	payload, err := json.Marshal(map[string]string{
		"file_base64": base64.StdEncoding.EncodeToString(pdfBytes),
		"file_name":   arxivID + ".pdf",
	})
	if err != nil {
		return pdfExtractionResponse{}, err
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, workerURL, bytes.NewBuffer(payload))
	if err != nil {
		return pdfExtractionResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err = c.HTTPClient.Do(req)
	if err != nil {
		return pdfExtractionResponse{}, err
	}
	defer func() { io.Copy(io.Discard, resp.Body); resp.Body.Close() }() //nolint:errcheck
	if resp.StatusCode >= 300 {
		return pdfExtractionResponse{}, fmt.Errorf("pdf worker returned status %d", resp.StatusCode)
	}

	var result pdfExtractionResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return pdfExtractionResponse{}, err
	}
	if result.FullText() == "" && strings.TrimSpace(result.Paper.Abstract) == "" {
		return pdfExtractionResponse{}, fmt.Errorf("pdf worker returned no text content")
	}
	return result, nil
}

func (c *Paper2SkillCompiler) resolvePDFSourceURL(arxivID string) (string, error) {
	raw := strings.TrimSpace(arxivID)
	if raw == "" {
		return "", fmt.Errorf("arxiv id is required")
	}

	if parsed, err := url.Parse(raw); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		return parsed.String(), nil
	}

	normalized := strings.TrimPrefix(raw, "https://arxiv.org/abs/")
	normalized = strings.TrimPrefix(normalized, "http://arxiv.org/abs/")
	normalized = strings.TrimPrefix(normalized, "https://arxiv.org/pdf/")
	normalized = strings.TrimPrefix(normalized, "http://arxiv.org/pdf/")
	normalized = strings.TrimSuffix(normalized, ".pdf")
	normalized = strings.TrimSpace(normalized)
	if normalized == "" {
		return "", fmt.Errorf("arxiv id is required")
	}

	base := strings.TrimSpace(c.PDFSourceBaseURL)
	if base == "" {
		base = "https://arxiv.org/pdf/"
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	return base + normalized + ".pdf", nil
}

// RegisterSkill posts the compiled skill to the registry sidecar.
func (c *Paper2SkillCompiler) RegisterSkill(ctx context.Context, schema SkillSchema) error {
	registryURL := c.RegistryURL
	if registryURL == "" {
		registryURL = "http://python-sidecar:8080/skills/register"
	}
	payload, err := json.Marshal(schema)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", registryURL, bytes.NewBuffer(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { io.Copy(io.Discard, resp.Body); resp.Body.Close() }() //nolint:errcheck
	if resp.StatusCode >= 300 {
		return fmt.Errorf("registry returned status %d", resp.StatusCode)
	}
	return nil
}

// truncate returns the first n runes of s, or all of s if len(runes) <= n.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}
