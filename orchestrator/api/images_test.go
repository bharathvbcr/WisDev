package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/genai"
)

type stubImageGenerator struct {
	images []genai.Image
	err    error
}

func (s stubImageGenerator) GenerateImages(_ context.Context, _ string, _ string, _ int, _ string) ([]genai.Image, error) {
	return s.images, s.err
}

func TestImageHandlerRejectsMissingPrompt(t *testing.T) {
	handler := NewImageHandler(stubImageGenerator{})
	req := httptest.NewRequest(http.MethodPost, "/v2/images/generate", bytes.NewBufferString(`{"prompt":"   "}`))
	rec := httptest.NewRecorder()

	handler.HandleGenerate(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}

	var resp APIError
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if resp.Error.Code != ErrInvalidParameters {
		t.Fatalf("expected %q, got %q", ErrInvalidParameters, resp.Error.Code)
	}
}

func TestImageHandlerReturnsInlineGeneratedImages(t *testing.T) {
	handler := NewImageHandler(stubImageGenerator{
		images: []genai.Image{
			{
				ImageBytes: []byte("png-bytes"),
				MIMEType:   "image/png",
			},
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/v2/images/generate", bytes.NewBufferString(`{"prompt":"diagram of mitosis","suggestion_id":"sg1","aspect_ratio":"1:1","count":1}`))
	rec := httptest.NewRecorder()

	handler.HandleGenerate(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp GenerateImageResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode success response: %v", err)
	}

	if !resp.OK {
		t.Fatalf("expected ok response")
	}
	if resp.Count != 1 || len(resp.Images) != 1 {
		t.Fatalf("expected one generated image, got count=%d images=%d", resp.Count, len(resp.Images))
	}
	if !strings.HasPrefix(resp.ImageURL, "data:image/png;base64,") {
		t.Fatalf("expected inline data url, got %q", resp.ImageURL)
	}
	if resp.Images[0].Source != "inline" {
		t.Fatalf("expected inline source, got %q", resp.Images[0].Source)
	}
	if resp.Images[0].SuggestionID != "sg1" {
		t.Fatalf("expected suggestion_id to be preserved")
	}
}
