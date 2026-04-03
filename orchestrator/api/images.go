package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"google.golang.org/genai"
)

type imageGenerator interface {
	GenerateImages(ctx context.Context, modelID, prompt string, count int, aspectRatio string) ([]genai.Image, error)
}

type ImageHandler struct {
	vertex imageGenerator
}

func NewImageHandler(vertex imageGenerator) *ImageHandler {
	return &ImageHandler{vertex: vertex}
}

type GenerateImageRequest struct {
	Prompt       string `json:"prompt"`
	SuggestionID string `json:"suggestion_id"`
	AspectRatio  string `json:"aspect_ratio"`
	Count        int    `json:"count"`
}

type GeneratedImage struct {
	ImageID      string `json:"image_id"`
	SuggestionID string `json:"suggestion_id,omitempty"`
	ImageURL     string `json:"image_url"`
	Prompt       string `json:"prompt"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	MIMEType     string `json:"mime_type,omitempty"`
	Source       string `json:"source"`
}

type GenerateImageResponse struct {
	OK               bool             `json:"ok"`
	ImageID          string           `json:"image_id,omitempty"`
	SuggestionID     string           `json:"suggestion_id,omitempty"`
	ImageURL         string           `json:"image_url,omitempty"`
	Prompt           string           `json:"prompt"`
	Width            int              `json:"width,omitempty"`
	Height           int              `json:"height,omitempty"`
	MIMEType         string           `json:"mime_type,omitempty"`
	GenerationTimeMs int64            `json:"generation_time_ms"`
	Count            int              `json:"count"`
	Images           []GeneratedImage `json:"images"`
}

func (h *ImageHandler) HandleGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	if h.vertex == nil {
		WriteError(w, http.StatusServiceUnavailable, ErrDependencyFailed, "image generation backend is unavailable", nil)
		return
	}

	var req GenerateImageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "prompt is required", map[string]any{
			"field": "prompt",
		})
		return
	}

	if req.Count <= 0 {
		req.Count = 1
	}
	if req.Count > 4 {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "count must be between 1 and 4", map[string]any{
			"field": "count",
			"value": req.Count,
		})
		return
	}

	if req.AspectRatio == "" {
		req.AspectRatio = "1:1"
	}
	if !isSupportedAspectRatio(req.AspectRatio) {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "unsupported aspect ratio", map[string]any{
			"field": "aspect_ratio",
			"value": req.AspectRatio,
		})
		return
	}

	startTime := time.Now()
	images, err := h.vertex.GenerateImages(r.Context(), "", req.Prompt, req.Count, req.AspectRatio)
	if err != nil {
		WriteError(w, http.StatusBadGateway, ErrDependencyFailed, "image generation failed", map[string]any{
			"error": err.Error(),
		})
		return
	}

	if len(images) == 0 {
		WriteError(w, http.StatusBadGateway, ErrDependencyFailed, "image generation returned no images", nil)
		return
	}

	width, height := dimensionsForAspectRatio(req.AspectRatio)
	serialized := make([]GeneratedImage, 0, len(images))
	for idx, image := range images {
		imageURL, mimeType, source, ok := serializeGeneratedImage(image)
		if !ok {
			continue
		}

		serialized = append(serialized, GeneratedImage{
			ImageID:      fmt.Sprintf("img_%d_%d", startTime.Unix(), idx+1),
			SuggestionID: req.SuggestionID,
			ImageURL:     imageURL,
			Prompt:       req.Prompt,
			Width:        width,
			Height:       height,
			MIMEType:     mimeType,
			Source:       source,
		})
	}

	if len(serialized) == 0 {
		WriteError(w, http.StatusBadGateway, ErrDependencyFailed, "image generation returned images without usable payloads", nil)
		return
	}

	resp := GenerateImageResponse{
		OK:               true,
		Prompt:           req.Prompt,
		GenerationTimeMs: time.Since(startTime).Milliseconds(),
		Count:            len(serialized),
		Images:           serialized,
		ImageID:          serialized[0].ImageID,
		SuggestionID:     serialized[0].SuggestionID,
		ImageURL:         serialized[0].ImageURL,
		Width:            serialized[0].Width,
		Height:           serialized[0].Height,
		MIMEType:         serialized[0].MIMEType,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func serializeGeneratedImage(image genai.Image) (imageURL string, mimeType string, source string, ok bool) {
	if image.GCSURI != "" {
		return image.GCSURI, image.MIMEType, "gcs", true
	}

	if len(image.ImageBytes) == 0 {
		return "", "", "", false
	}

	mimeType = image.MIMEType
	if mimeType == "" {
		mimeType = "image/png"
	}

	encoded := base64.StdEncoding.EncodeToString(image.ImageBytes)
	return "data:" + mimeType + ";base64," + encoded, mimeType, "inline", true
}

func isSupportedAspectRatio(aspectRatio string) bool {
	switch aspectRatio {
	case "1:1", "2:3", "3:2", "3:4", "4:3", "9:16", "16:9", "21:9":
		return true
	default:
		return false
	}
}

func dimensionsForAspectRatio(aspectRatio string) (int, int) {
	parts := strings.Split(aspectRatio, ":")
	if len(parts) != 2 {
		return 1024, 1024
	}

	widthRatio, err := strconv.Atoi(parts[0])
	if err != nil || widthRatio <= 0 {
		return 1024, 1024
	}

	heightRatio, err := strconv.Atoi(parts[1])
	if err != nil || heightRatio <= 0 {
		return 1024, 1024
	}

	const base = 1024
	if widthRatio >= heightRatio {
		return int(float64(base) * float64(widthRatio) / float64(heightRatio)), base
	}
	return base, int(float64(base) * float64(heightRatio) / float64(widthRatio))
}
