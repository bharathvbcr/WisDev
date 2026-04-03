package wisdev

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	llmv1 "github.com/wisdev-agent/wisdev-agent-os/orchestrator/proto/llm/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestNewPaper2SkillCompiler(t *testing.T) {
	t.Setenv("PYTHON_SIDECAR_URL", "http://python-sidecar:8080")
	mLLM := new(mockLLM)
	compiler := NewPaper2SkillCompiler(mLLM)
	assert.NotNil(t, compiler)
	assert.Equal(t, mLLM, compiler.LLM)
	assert.NotNil(t, compiler.HTTPClient)
	assert.Equal(t, "https://arxiv.org/pdf/", compiler.PDFSourceBaseURL)
	assert.Equal(t, "http://python-sidecar:8080/skills/register", compiler.RegistryURL)
	assert.Equal(t, "http://python-sidecar:8080/ml/pdf", compiler.PDFWorkerURL)
}

func TestPaper2SkillCompiler_CompileArxivID_DegradedOnPDFFetchFail(t *testing.T) {
	mLLM := new(mockLLM)

	// Server that returns error on the PDF source path.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/pdf/2401.99999.pdf":
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
		case "/ml/pdf":
			t.Fatal("pdf worker should not be called when the source fetch fails")
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	compiler := &Paper2SkillCompiler{
		LLM:              mLLM,
		HTTPClient:       ts.Client(),
		PDFSourceBaseURL: ts.URL + "/pdf/",
		RegistryURL:      ts.URL + "/skills/register",
		PDFWorkerURL:     ts.URL + "/ml/pdf",
	}

	schema, err := compiler.CompileArxivID(context.Background(), "2401.99999")

	assert.NoError(t, err)
	assert.Contains(t, schema.Name, "degraded_skill_")
	assert.Equal(t, "2401.99999", schema.SourcePaper.ArxivID)
	mLLM.AssertNotCalled(t, "StructuredOutput")
}

func TestPaper2SkillCompiler_CompileArxivID_DegradedOnEmptyPDFResponse(t *testing.T) {
	mLLM := new(mockLLM)

	sourcePDF := []byte("%PDF-1.4\nempty response test\n%%EOF")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/pdf/2401.88888.pdf":
			_, _ = w.Write(sourcePDF)
		case "/ml/pdf":
			body, _ := io.ReadAll(r.Body)
			var payload map[string]string
			_ = json.Unmarshal(body, &payload)
			decoded, _ := base64.StdEncoding.DecodeString(payload["file_base64"])
			assert.True(t, bytes.Equal(sourcePDF, decoded))
			json.NewEncoder(w).Encode(map[string]any{"full_text": ""})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	compiler := &Paper2SkillCompiler{
		LLM:              mLLM,
		HTTPClient:       ts.Client(),
		PDFSourceBaseURL: ts.URL + "/pdf/",
		RegistryURL:      ts.URL + "/skills/register",
		PDFWorkerURL:     ts.URL + "/ml/pdf",
	}

	schema, err := compiler.CompileArxivID(context.Background(), "2401.88888")

	assert.NoError(t, err)
	assert.Contains(t, schema.Name, "degraded_skill_")
	mLLM.AssertNotCalled(t, "StructuredOutput")
}

func TestPaper2SkillCompiler_CompileArxivID_FullFlow(t *testing.T) {
	mLLM := new(mockLLM)

	sourcePDF := []byte("%PDF-1.4\nfull flow test\n%%EOF")
	// Mock HTTP server handles both /ml/pdf and /skills/register
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/pdf/2401.00001.pdf":
			_, _ = w.Write(sourcePDF)
		case "/ml/pdf":
			body, _ := io.ReadAll(r.Body)
			var payload map[string]string
			_ = json.Unmarshal(body, &payload)
			decoded, _ := base64.StdEncoding.DecodeString(payload["file_base64"])
			assert.True(t, bytes.Equal(sourcePDF, decoded))
			json.NewEncoder(w).Encode(map[string]any{
				"paper": map[string]any{
					"title":       "Sparse Attention Paper",
					"abstract":    "This paper explores sparse attention.",
					"doi":         "10.1000/sparse",
					"authors":     []string{"Ada Lovelace"},
					"publishDate": map[string]any{"year": 2024},
				},
				"full_text": "Abstract: We propose a sparse attention mechanism.\n\nMethodology: Use sliding window attention with stride 2.",
			})
		case "/skills/register":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	compiler := &Paper2SkillCompiler{
		LLM:              mLLM,
		HTTPClient:       ts.Client(),
		PDFSourceBaseURL: ts.URL + "/pdf/",
		RegistryURL:      ts.URL + "/skills/register",
		PDFWorkerURL:     ts.URL + "/ml/pdf",
	}

	methodologyJSON := `{"methodology":"sliding window attention with stride 2"}`
	skillJSON := `{"name":"sparse_attention_v1","description":"Sparse attention mechanism","inputs":[],"outputs":[],"steps":["apply sparse mask"],"code_template":"","source_paper":{"arxiv_id":"2401.00001","title":"","authors":null,"year":0,"doi":"","abstract":""}}`

	mLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.Prompt, "methodology")
	})).Return(&llmv1.StructuredResponse{JsonResult: methodologyJSON}, nil).Once()

	mLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.Prompt, "SkillSchema")
	})).Return(&llmv1.StructuredResponse{JsonResult: skillJSON}, nil).Once()

	schema, err := compiler.CompileArxivID(context.Background(), "2401.00001")

	assert.NoError(t, err)
	assert.Equal(t, "sparse_attention_v1", schema.Name)
	mLLM.AssertExpectations(t)
}

func TestPaper2SkillCompiler_CompileArxivID_AcceptsCamelCaseFullText(t *testing.T) {
	mLLM := new(mockLLM)

	sourcePDF := []byte("%PDF-1.4\ncamel case response test\n%%EOF")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/pdf/2401.00005.pdf":
			_, _ = w.Write(sourcePDF)
		case "/ml/pdf":
			body, _ := io.ReadAll(r.Body)
			var payload map[string]string
			_ = json.Unmarshal(body, &payload)
			decoded, _ := base64.StdEncoding.DecodeString(payload["file_base64"])
			assert.True(t, bytes.Equal(sourcePDF, decoded))
			json.NewEncoder(w).Encode(map[string]any{
				"paper": map[string]any{
					"title":       "Camel Case Worker Paper",
					"abstract":    "Worker response uses normalized keys.",
					"publishDate": map[string]any{"year": 2024},
				},
				"fullText": "Methodology: normalized fullText should be accepted.",
			})
		case "/skills/register":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	compiler := &Paper2SkillCompiler{
		LLM:              mLLM,
		HTTPClient:       ts.Client(),
		PDFSourceBaseURL: ts.URL + "/pdf/",
		RegistryURL:      ts.URL + "/skills/register",
		PDFWorkerURL:     ts.URL + "/ml/pdf",
	}

	mLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.Prompt, "methodology") &&
			strings.Contains(req.Prompt, "normalized fullText should be accepted")
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"methodology":"normalized worker text"}`}, nil).Once()

	mLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.Prompt, "SkillSchema")
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"name":"camel_case_worker","description":"Uses normalized worker text","inputs":[],"outputs":[],"steps":["extract normalized text"],"code_template":"","source_paper":{"arxiv_id":"2401.00005"}}`}, nil).Once()

	schema, err := compiler.CompileArxivID(context.Background(), "2401.00005")

	assert.NoError(t, err)
	assert.Equal(t, "camel_case_worker", schema.Name)
	mLLM.AssertExpectations(t)
}

func TestPaper2SkillCompiler_CompileArxivID_DegradedOnMethodologyLLMFail(t *testing.T) {
	mLLM := new(mockLLM)
	sourcePDF := []byte("%PDF-1.4\nmethodology fail test\n%%EOF")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/pdf/2401.00002.pdf":
			_, _ = w.Write(sourcePDF)
		case "/ml/pdf":
			body, _ := io.ReadAll(r.Body)
			var payload map[string]string
			_ = json.Unmarshal(body, &payload)
			decoded, _ := base64.StdEncoding.DecodeString(payload["file_base64"])
			assert.True(t, bytes.Equal(sourcePDF, decoded))
			json.NewEncoder(w).Encode(map[string]any{"full_text": "Some paper text"})
		}
	}))
	defer ts.Close()

	compiler := &Paper2SkillCompiler{
		LLM:              mLLM,
		HTTPClient:       ts.Client(),
		PDFSourceBaseURL: ts.URL + "/pdf/",
		RegistryURL:      ts.URL + "/skills/register",
		PDFWorkerURL:     ts.URL + "/ml/pdf",
	}

	mLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.Prompt, "methodology")
	})).Return(nil, errors.New("LLM unavailable"))

	schema, err := compiler.CompileArxivID(context.Background(), "2401.00002")
	assert.NoError(t, err)
	assert.Contains(t, schema.Name, "degraded_skill_")
}

func TestPaper2SkillCompiler_CompileArxivID_DegradedOnSkillSchemaLLMFail(t *testing.T) {
	mLLM := new(mockLLM)
	sourcePDF := []byte("%PDF-1.4\nskill schema fail test\n%%EOF")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/pdf/2401.00003.pdf":
			_, _ = w.Write(sourcePDF)
		case "/ml/pdf":
			body, _ := io.ReadAll(r.Body)
			var payload map[string]string
			_ = json.Unmarshal(body, &payload)
			decoded, _ := base64.StdEncoding.DecodeString(payload["file_base64"])
			assert.True(t, bytes.Equal(sourcePDF, decoded))
			json.NewEncoder(w).Encode(map[string]any{"full_text": "Some paper text"})
		}
	}))
	defer ts.Close()

	compiler := &Paper2SkillCompiler{
		LLM:              mLLM,
		HTTPClient:       ts.Client(),
		PDFSourceBaseURL: ts.URL + "/pdf/",
		RegistryURL:      ts.URL + "/skills/register",
		PDFWorkerURL:     ts.URL + "/ml/pdf",
	}

	mLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.Prompt, "methodology")
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"methodology":"sliding window attention"}`}, nil).Once()

	mLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.Prompt, "SkillSchema")
	})).Return(nil, errors.New("LLM timeout"))

	schema, err := compiler.CompileArxivID(context.Background(), "2401.00003")
	assert.NoError(t, err)
	assert.Contains(t, schema.Name, "degraded_skill_")
}

func TestPaper2SkillCompiler_CompileArxivID_DegradedOnSkillSchemaBadJSON(t *testing.T) {
	mLLM := new(mockLLM)
	sourcePDF := []byte("%PDF-1.4\nskill schema bad json test\n%%EOF")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/pdf/2401.00004.pdf":
			_, _ = w.Write(sourcePDF)
		case "/ml/pdf":
			body, _ := io.ReadAll(r.Body)
			var payload map[string]string
			_ = json.Unmarshal(body, &payload)
			decoded, _ := base64.StdEncoding.DecodeString(payload["file_base64"])
			assert.True(t, bytes.Equal(sourcePDF, decoded))
			json.NewEncoder(w).Encode(map[string]any{"full_text": "Some paper text"})
		}
	}))
	defer ts.Close()

	compiler := &Paper2SkillCompiler{
		LLM:              mLLM,
		HTTPClient:       ts.Client(),
		PDFSourceBaseURL: ts.URL + "/pdf/",
		RegistryURL:      ts.URL + "/skills/register",
		PDFWorkerURL:     ts.URL + "/ml/pdf",
	}

	mLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.Prompt, "methodology")
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"methodology":"sliding window attention"}`}, nil).Once()

	mLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.Prompt, "SkillSchema")
	})).Return(&llmv1.StructuredResponse{JsonResult: `{invalid json`}, nil)

	schema, err := compiler.CompileArxivID(context.Background(), "2401.00004")
	assert.NoError(t, err)
	assert.Contains(t, schema.Name, "degraded_skill_")
}
