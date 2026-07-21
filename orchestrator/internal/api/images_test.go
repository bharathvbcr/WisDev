package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	"github.com/stretchr/testify/assert"
)

type stubImageGenerator struct {
	images     []genai.Image
	err        error
	generateFn func(context.Context, string, string, int, string) ([]genai.Image, error)
}

func (s stubImageGenerator) GenerateImages(ctx context.Context, modelID string, prompt string, count int, aspectRatio string) ([]genai.Image, error) {
	if s.generateFn != nil {
		return s.generateFn(ctx, modelID, prompt, count, aspectRatio)
	}
	return s.images, s.err
}

func TestImageHandlerRejectsMissingPrompt(t *testing.T) {
	handler := NewImageHandler(stubImageGenerator{})
	req := httptest.NewRequest(http.MethodPost, "/images/generate", bytes.NewBufferString(`{"prompt":"   "}`))
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

	req := httptest.NewRequest(http.MethodPost, "/images/generate", bytes.NewBufferString(`{"prompt":"diagram of mitosis","suggestion_id":"sg1","aspect_ratio":"1:1","count":1}`))
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

func TestImageHandlerErrorEnvelopes(t *testing.T) {
	handler := NewImageHandler(stubImageGenerator{})

	t.Run("method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/images/generate", nil)
		rec := httptest.NewRecorder()

		handler.HandleGenerate(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected 405, got %d", rec.Code)
		}
	})

	t.Run("service unavailable", func(t *testing.T) {
		unavailable := &ImageHandler{vertex: nil}
		req := httptest.NewRequest(http.MethodPost, "/images/generate", bytes.NewBufferString(`{"prompt":"x"}`))
		rec := httptest.NewRecorder()

		unavailable.HandleGenerate(rec, req)

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503, got %d", rec.Code)
		}
	})

	t.Run("count out of range", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/images/generate", bytes.NewBufferString(`{"prompt":"x","count":9}`))
		rec := httptest.NewRecorder()

		handler.HandleGenerate(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", rec.Code)
		}
	})

	t.Run("unsupported aspect ratio", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/images/generate", bytes.NewBufferString(`{"prompt":"x","aspect_ratio":"10:10"}`))
		rec := httptest.NewRecorder()

		handler.HandleGenerate(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", rec.Code)
		}
	})

	t.Run("invalid count uses default", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/images/generate", bytes.NewBufferString(`{"prompt":"x","count":0,"aspect_ratio":"16:9"}`))
		req = req.WithContext(req.Context())
		handler := NewImageHandler(stubImageGenerator{
			images: []genai.Image{
				{
					ImageBytes: []byte("x"),
					MIMEType:   "image/png",
				},
			},
		})
		rec := httptest.NewRecorder()

		handler.HandleGenerate(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		var resp GenerateImageResponse
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode success response: %v", err)
		}
		if resp.Count != 1 {
			t.Fatalf("expected one image after defaulting count to 1")
		}
		if resp.Width <= resp.Height {
			t.Fatalf("expected wide image dimensions for 16:9, got %dx%d", resp.Width, resp.Height)
		}
	})

	t.Run("no usable payloads", func(t *testing.T) {
		handler := NewImageHandler(stubImageGenerator{
			images: []genai.Image{
				{ImageBytes: nil},
			},
		})
		req := httptest.NewRequest(http.MethodPost, "/images/generate", bytes.NewBufferString(`{"prompt":"x","count":1}`))
		rec := httptest.NewRecorder()

		handler.HandleGenerate(rec, req)

		if rec.Code != http.StatusBadGateway {
			t.Fatalf("expected 502, got %d", rec.Code)
		}
	})

	t.Run("generator failure", func(t *testing.T) {
		handler := NewImageHandler(stubImageGenerator{err: assert.AnError})
		req := httptest.NewRequest(http.MethodPost, "/images/generate", bytes.NewBufferString(`{"prompt":"x","count":1}`))
		rec := httptest.NewRecorder()

		handler.HandleGenerate(rec, req)

		if rec.Code != http.StatusBadGateway {
			t.Fatalf("expected 502, got %d", rec.Code)
		}
	})

	t.Run("empty image list", func(t *testing.T) {
		handler := NewImageHandler(stubImageGenerator{images: []genai.Image{}})
		req := httptest.NewRequest(http.MethodPost, "/images/generate", bytes.NewBufferString(`{"prompt":"x","count":1}`))
		rec := httptest.NewRecorder()

		handler.HandleGenerate(rec, req)

		if rec.Code != http.StatusBadGateway {
			t.Fatalf("expected 502, got %d", rec.Code)
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/images/generate", bytes.NewBufferString(`{"prompt":`))
		rec := httptest.NewRecorder()

		handler.HandleGenerate(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", rec.Code)
		}
	})
}

func TestImageHandlerDefaults(t *testing.T) {
	handler := NewImageHandler(stubImageGenerator{
		images: []genai.Image{
			{
				ImageBytes: []byte("inline-payload"),
				MIMEType:   "image/png",
			},
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/images/generate", bytes.NewBufferString(`{"prompt":"  square image  "}`))
	rec := httptest.NewRecorder()

	handler.HandleGenerate(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp GenerateImageResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode success response: %v", err)
	}
	if resp.Count != 1 {
		t.Fatalf("expected default count to be 1, got %d", resp.Count)
	}
	if resp.Width != 1024 || resp.Height != 1024 {
		t.Fatalf("expected default 1:1 dimensions, got %dx%d", resp.Width, resp.Height)
	}
}

func TestImageHandlerReturnsGCSImage(t *testing.T) {
	handler := NewImageHandler(stubImageGenerator{
		images: []genai.Image{
			{
				GCSURI:   "gs://bucket/example.png",
				MIMEType: "image/png",
			},
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/images/generate", bytes.NewBufferString(`{"prompt":"diagram","count":1}`))
	rec := httptest.NewRecorder()

	handler.HandleGenerate(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp GenerateImageResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode success response: %v", err)
	}

	if len(resp.Images) != 1 {
		t.Fatalf("expected one image")
	}
	if resp.Images[0].Source != "gcs" || resp.Images[0].ImageURL != "gs://bucket/example.png" {
		t.Fatalf("expected gcs image source, got %+v", resp.Images[0])
	}
}

func TestImageHandlerDeadlineExceededFailsQuickly(t *testing.T) {
	previousTimeout := imageGenerationRequestTimeout
	imageGenerationRequestTimeout = 50 * time.Millisecond
	defer func() { imageGenerationRequestTimeout = previousTimeout }()

	handler := NewImageHandler(stubImageGenerator{
		generateFn: func(ctx context.Context, _ string, _ string, _ int, _ string) ([]genai.Image, error) {
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("expected image generation call to carry a deadline")
			}
			select {
			case <-ctx.Done():
				if ctx.Err() != context.DeadlineExceeded {
					t.Fatalf("expected deadline exceeded, got %v", ctx.Err())
				}
				return nil, ctx.Err()
			case <-time.After(1 * time.Second):
				t.Fatal("expected image generation context cancellation")
				return nil, nil
			}
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/images/generate", bytes.NewBufferString(`{"prompt":"diagram","count":1}`))
	rec := httptest.NewRecorder()

	startedAt := time.Now()
	handler.HandleGenerate(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rec.Code)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("expected quick timeout response, got %s", elapsed)
	}
}

func TestImageSerializationHelpers(t *testing.T) {
	t.Run("serialize from GCS", func(t *testing.T) {
		url, mimeType, source, ok := serializeGeneratedImage(genai.Image{
			GCSURI:   "gs://bucket/one.png",
			MIMEType: "image/png",
		})
		if !ok || source != "gcs" || url != "gs://bucket/one.png" || mimeType != "image/png" {
			t.Fatalf("unexpected gcs image serialization: ok=%v source=%q url=%q mime=%q", ok, source, url, mimeType)
		}
	})

	t.Run("serialize inline with explicit mime", func(t *testing.T) {
		url, mimeType, source, ok := serializeGeneratedImage(genai.Image{
			ImageBytes: []byte("abc"),
			MIMEType:   "image/png",
		})
		if !ok || source != "inline" || !strings.HasPrefix(url, "data:image/png;base64,") || mimeType != "image/png" {
			t.Fatalf("unexpected inline image serialization: ok=%v source=%q url=%q mime=%q", ok, source, url, mimeType)
		}
	})

	t.Run("serialize inline with default mime", func(t *testing.T) {
		url, mimeType, source, ok := serializeGeneratedImage(genai.Image{
			ImageBytes: []byte("abc"),
		})
		if !ok || source != "inline" || !strings.HasPrefix(url, "data:image/png;base64,") || mimeType != "image/png" {
			t.Fatalf("unexpected default mime serialization: ok=%v source=%q url=%q mime=%q", ok, source, url, mimeType)
		}
	})

	t.Run("serialize empty image", func(t *testing.T) {
		url, mimeType, source, ok := serializeGeneratedImage(genai.Image{})
		if ok || url != "" || mimeType != "" || source != "" {
			t.Fatalf("expected empty payload rejection, got ok=%v url=%q mime=%q source=%q", ok, url, mimeType, source)
		}
	})
}

func TestImageRatioHelpers(t *testing.T) {
	t.Run("supported ratios", func(t *testing.T) {
		valid := []string{"1:1", "2:3", "3:2", "3:4", "4:3", "9:16", "16:9", "21:9"}
		for _, ratio := range valid {
			if !isSupportedAspectRatio(ratio) {
				t.Fatalf("expected %q to be valid", ratio)
			}
		}
		if isSupportedAspectRatio("7:5") {
			t.Fatalf("expected invalid ratio")
		}
	})

	t.Run("dimensions from ratios", func(t *testing.T) {
		width, height := dimensionsForAspectRatio("1:1")
		if width != 1024 || height != 1024 {
			t.Fatalf("expected square fallback dimensions, got %dx%d", width, height)
		}
		width, height = dimensionsForAspectRatio("16:9")
		if width <= height {
			t.Fatalf("expected wider dimensions, got %dx%d", width, height)
		}
		width, height = dimensionsForAspectRatio("9:16")
		if height <= width {
			t.Fatalf("expected taller dimensions, got %dx%d", width, height)
		}
		width, height = dimensionsForAspectRatio("bad")
		if width != 1024 || height != 1024 {
			t.Fatalf("expected default dimensions, got %dx%d", width, height)
		}
		width, height = dimensionsForAspectRatio("0:16")
		if width != 1024 || height != 1024 {
			t.Fatalf("expected default dimensions for zero ratio, got %dx%d", width, height)
		}
		width, height = dimensionsForAspectRatio("16:0")
		if width != 1024 || height != 1024 {
			t.Fatalf("expected default dimensions for zero height ratio, got %dx%d", width, height)
		}
		width, height = dimensionsForAspectRatio("bad:ratio")
		if width != 1024 || height != 1024 {
			t.Fatalf("expected default dimensions for non numeric ratio, got %dx%d", width, height)
		}
		width, height = dimensionsForAspectRatio("1:bad")
		if width != 1024 || height != 1024 {
			t.Fatalf("expected default dimensions for non numeric height ratio, got %dx%d", width, height)
		}
		width, height = dimensionsForAspectRatio("1")
		if width != 1024 || height != 1024 {
			t.Fatalf("expected default dimensions for malformed ratio, got %dx%d", width, height)
		}
	})
}
