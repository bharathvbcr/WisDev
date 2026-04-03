package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/paper"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/resilience"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/search"
)

type PaperProfiler interface {
	ExtractProfile(ctx context.Context, paper search.Paper) (*paper.Profile, error)
}

type PaperHandler struct {
	profiler      PaperProfiler
	pythonBaseURL string
}

func NewPaperHandler(profiler PaperProfiler, pythonBaseURL string) *PaperHandler {
	return &PaperHandler{
		profiler:      profiler,
		pythonBaseURL: pythonBaseURL,
	}
}

func (h *PaperHandler) HandleProfile(w http.ResponseWriter, r *http.Request) {
	if IsDegraded(r.Context()) {
		WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "LLM sidecar is currently unavailable. Paper profiling is disabled.", nil)
		return
	}
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	var req search.Paper
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	profile, err := h.profiler.ExtractProfile(r.Context(), req)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, ErrDependencyFailed, "paper profiling failed", map[string]any{
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
			resp, fetchErr := http.Get(req.URL)
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

	// 3. Call Python sidecar for high-quality extraction
	pythonURL := h.pythonBaseURL + "/ml/pdf"
	reqPayload := map[string]string{
		"file_base64": base64.StdEncoding.EncodeToString(pdfData),
		"file_name":   fileName,
	}
	body, _ := json.Marshal(reqPayload)
	
	pyReq, _ := http.NewRequestWithContext(r.Context(), "POST", pythonURL, bytes.NewReader(body))
	pyReq.Header.Set("Content-Type", "application/json")
	
	pyResp, pyErr := http.DefaultClient.Do(pyReq)
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
		WriteError(w, http.StatusBadGateway, ErrDependencyFailed, "python sidecar returned unexpected status", map[string]any{
			"status": pyResp.StatusCode,
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

func (h *PaperHandler) HandleExportMarkdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	var req paper.ExportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	content := paper.GenerateMarkdown(req)
	filename := fmt.Sprintf("%s.md", req.DraftID)

	w.Header().Set("Content-Type", "text/markdown")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Write([]byte(content))
}

func (h *PaperHandler) HandleExportHTML(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	var req paper.ExportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	content := paper.GenerateHTML(req)
	filename := fmt.Sprintf("%s.html", req.DraftID)

	w.Header().Set("Content-Type", "text/html")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Write([]byte(content))
}

func (h *PaperHandler) HandleExportLaTeX(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	var req paper.ExportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	content := paper.GenerateLaTeX(req)
	filename := fmt.Sprintf("%s.tex", req.DraftID)

	w.Header().Set("Content-Type", "application/x-tex")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Write([]byte(content))
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

	apiKey, _ := resilience.GetSecret(r.Context(), "SEMANTIC_SCHOLAR_API_KEY")
	fetchUrl := fmt.Sprintf("https://api.semanticscholar.org/graph/v1/paper/%s?fields=paperId,externalIds,title,url,abstract,authors,year,venue,citationCount,influentialCitationCount,referenceCount,openAccessPdf,fieldsOfStudy", 
		url.QueryEscape(identifier))

	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, fetchUrl, nil)
	if apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
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

	apiKey, _ := resilience.GetSecret(r.Context(), "SEMANTIC_SCHOLAR_API_KEY")
	fetchUrl := fmt.Sprintf("https://api.semanticscholar.org/graph/v1/paper/%s?fields=paperId,title,year,citationCount,citations.paperId,citations.title,citations.year,references.paperId,references.title,references.year",
		url.QueryEscape(paperId))

	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, fetchUrl, nil)
	if apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
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
