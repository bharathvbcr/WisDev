package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/llm"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/telemetry"
	llmv1 "github.com/bharathvbcr/wisdev-arc/orchestrator/proto/llm"
)

type paperExtractionColumn struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	DataType    string `json:"dataType"`
	Required    bool   `json:"required"`
}

type paperExtractionRequest struct {
	Paper struct {
		PaperID     string `json:"paperId"`
		ID          string `json:"id"`
		DOI         string `json:"doi"`
		Title       string `json:"title"`
		Abstract    string `json:"abstract"`
		Summary     string `json:"summary"`
		FullText    string `json:"fullText"`
		Link        string `json:"link"`
		PublishDate struct {
			Year int `json:"year"`
		} `json:"publishDate"`
		Authors []struct {
			Name     string `json:"name"`
			AuthorID string `json:"authorId"`
		} `json:"authors"`
	} `json:"paper"`
	Columns   []paperExtractionColumn `json:"columns"`
	RAGChunks []struct {
		Content        string  `json:"content"`
		RelevanceScore float64 `json:"relevanceScore"`
		SectionType    string  `json:"sectionType"`
	} `json:"ragChunks"`
}

func (h *LLMHandler) HandlePaperExtraction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeGenerateError(w, http.StatusMethodNotAllowed, generateErrPermanent, "Method not allowed")
		return
	}
	structuredClient, ok := h.llmClient.(structuredOutputClient)
	if !ok || structuredClient == nil {
		writeGenerateError(w, http.StatusServiceUnavailable, generateErrPermanent, "Structured generation backend unavailable")
		return
	}

	var body paperExtractionRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeGenerateError(w, http.StatusBadRequest, generateErrPermanent, "Invalid request body")
		return
	}
	if strings.TrimSpace(body.Paper.Title) == "" {
		writeGenerateError(w, http.StatusBadRequest, generateErrPermanent, "paper.title is required")
		return
	}
	if len(body.Columns) == 0 {
		writeGenerateError(w, http.StatusBadRequest, generateErrPermanent, "columns are required")
		return
	}

	schema, err := buildPaperExtractionSchema(body.Columns)
	if err != nil {
		writeGenerateError(w, http.StatusBadRequest, generateErrPermanent, err.Error())
		return
	}

	policy := llm.ResolveRequestPolicy(llm.RequestPolicyInput{
		RequestedTier:   "light",
		TaskType:        "paper_extraction",
		Structured:      true,
		HighValue:       true,
		LatencyBudgetMs: 12_000,
	})
	ctx, cancel := context.WithTimeout(r.Context(), policy.OuterDeadline)
	defer cancel()
	logger := telemetry.FromCtx(ctx)
	prompt := buildPaperExtractionPrompt(body)

	logger.InfoContext(ctx, "paper extraction request accepted",
		"component", "api.paper_extraction",
		"operation", "extract_paper_table_cells",
		"stage", "request_start",
		"paper_id", firstNonEmpty(body.Paper.PaperID, body.Paper.ID, body.Paper.DOI),
		"column_count", len(body.Columns),
		"rag_chunk_count", len(body.RAGChunks),
		"latency_budget_ms", policy.LatencyBudgetMs,
	)

	attemptCtx, attemptCancel := context.WithTimeout(ctx, time.Duration(policy.LatencyBudgetMs)*time.Millisecond)
	resp, err := structuredClient.StructuredOutput(attemptCtx, llm.ApplyStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:          prompt,
		JsonSchema:      string(schema),
		Model:           h.resolveModel(policy.InitialTier),
		Temperature:     0.2,
		MaxTokens:       1536,
		ServiceTier:     policy.ServiceTier,
		RetryProfile:    string(policy.RetryProfile),
		RequestClass:    string(policy.RequestClass),
		LatencyBudgetMs: int32(policy.LatencyBudgetMs),
	}, policy))
	attemptCancel()
	if err != nil {
		logger.WarnContext(ctx, "paper extraction structured output failed",
			"component", "api.paper_extraction",
			"operation", "extract_paper_table_cells",
			"stage", "llm_failed",
			"error", err.Error(),
		)
		writeGenerateError(w, http.StatusBadGateway, classifyLLMError(err), "paper extraction failed")
		return
	}
	if resp == nil || !resp.SchemaValid || strings.TrimSpace(resp.JsonResult) == "" {
		writeGenerateError(w, http.StatusBadGateway, generateErrTransient, "paper extraction returned invalid structured output")
		return
	}

	var parsed struct {
		Extractions map[string]any `json:"extractions"`
	}
	if err := json.Unmarshal([]byte(resp.JsonResult), &parsed); err != nil || parsed.Extractions == nil {
		writeGenerateError(w, http.StatusBadGateway, generateErrTransient, "paper extraction returned malformed JSON")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(parsed)
}

func buildPaperExtractionSchema(columns []paperExtractionColumn) (json.RawMessage, error) {
	properties := map[string]any{}
	required := make([]string, 0, len(columns))
	for _, column := range columns {
		name := strings.TrimSpace(column.Name)
		if name == "" {
			return nil, fmt.Errorf("column.name is required")
		}
		required = append(required, name)
		properties[name] = map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"value": map[string]any{
					"anyOf": []map[string]any{
						{"type": "string"},
						{"type": "number"},
						{"type": "boolean"},
						{"type": "array", "items": map[string]any{"type": "string"}},
						{"type": "null"},
					},
				},
				"confidence": map[string]any{"type": "number", "minimum": 0, "maximum": 1},
				"source":     map[string]any{"type": "string"},
			},
			"required": []string{"value", "confidence", "source"},
		}
	}
	return json.Marshal(map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"extractions": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties":           properties,
				"required":             required,
			},
		},
		"required": []string{"extractions"},
	})
}

func buildPaperExtractionPrompt(req paperExtractionRequest) string {
	columnLines := make([]string, 0, len(req.Columns))
	for _, column := range req.Columns {
		columnLines = append(columnLines, fmt.Sprintf("- %q (%s): %s", column.Name, column.DataType, column.Description))
	}
	authorNames := make([]string, 0, len(req.Paper.Authors))
	for _, author := range req.Paper.Authors {
		if name := strings.TrimSpace(author.Name); name != "" {
			authorNames = append(authorNames, name)
		}
	}
	year := "Unknown"
	if req.Paper.PublishDate.Year > 0 {
		year = fmt.Sprintf("%d", req.Paper.PublishDate.Year)
	}
	contextParts := []string{
		"Abstract: " + fallbackText(req.Paper.Abstract, "No abstract available"),
		"Summary: " + fallbackText(req.Paper.Summary, "No summary available"),
	}
	if fullText := trimForPrompt(req.Paper.FullText, 12000); fullText != "" {
		contextParts = append(contextParts, "Full text excerpt: "+fullText)
	}
	for i, chunk := range req.RAGChunks {
		if content := trimForPrompt(chunk.Content, 2500); content != "" {
			contextParts = append(contextParts, fmt.Sprintf("Retrieved excerpt %d (%s, %.2f): %s", i+1, chunk.SectionType, chunk.RelevanceScore, content))
		}
	}

	return fmt.Sprintf(`You are extracting structured comparison-table data from one academic paper.

PAPER METADATA:
Title: %s
Authors: %s
Year: %s
DOI: %s
URL: %s

PAPER CONTEXT:
%s

EXTRACTION COLUMNS:
%s

RULES:
1. Extract only values explicitly stated or clearly implied by the paper context.
2. Return null when the context does not support the requested value.
3. Use concise source text from the provided context for each non-null value.
4. Use the supplied structured output schema exactly.`,
		req.Paper.Title,
		fallbackText(strings.Join(authorNames, ", "), "Unknown"),
		year,
		fallbackText(req.Paper.DOI, "Unknown"),
		fallbackText(req.Paper.Link, "Unknown"),
		strings.Join(contextParts, "\n\n"),
		strings.Join(columnLines, "\n"),
	)
}

func trimForPrompt(value string, limit int) string {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) <= limit {
		return trimmed
	}
	return trimmed[:limit] + "\n[truncated]"
}

func fallbackText(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}
