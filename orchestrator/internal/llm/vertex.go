package llm

import (
	"context"
	"fmt"
	"os"
	"strings"

	"google.golang.org/genai"
)

// VertexClient wraps the Google GenAI Go SDK.
type VertexClient struct {
	client *genai.Client
}

// NewVertexClient creates a new LLM Provider client using the unified GenAI SDK.
func NewVertexClient(ctx context.Context, projectID, location string) (*VertexClient, error) {
	if projectID == "" {
		projectID = os.Getenv("VERTEX_PROJECT")
	}
	if location == "" {
		location = "us-central1"
	}

	cfg := &genai.ClientConfig{
		Project:  projectID,
		Location: location,
		Backend:  genai.BackendVertexAI,
	}

	client, err := genai.NewClient(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create genai client: %w", err)
	}

	return &VertexClient{
		client: client,
	}, nil
}

// GenerateText generates a text completion using Gemini.
func (v *VertexClient) GenerateText(ctx context.Context, modelID, prompt, systemPrompt string, temperature float32, maxTokens int32) (string, error) {
	if modelID == "" {
		modelID = "gemini-2.0-flash"
	}

	config := &genai.GenerateContentConfig{
		Temperature:      &temperature,
		MaxOutputTokens:  maxTokens,
	}
	
	if systemPrompt != "" {
		config.SystemInstruction = &genai.Content{
			Parts: []*genai.Part{{Text: systemPrompt}},
		}
	}

	contents := []*genai.Content{
		{
			Parts: []*genai.Part{{Text: prompt}},
		},
	}

	resp, err := v.client.Models.GenerateContent(ctx, modelID, contents, config)
	if err != nil {
		return "", fmt.Errorf("generate content failed: %w", err)
	}

	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("no candidates returned")
	}

	var textBuilder strings.Builder
	for _, part := range resp.Candidates[0].Content.Parts {
		textBuilder.WriteString(part.Text)
	}

	return textBuilder.String(), nil
}

// EmbedText generates an embedding for a single text.
func (v *VertexClient) EmbedText(ctx context.Context, modelID, text, taskType string) ([]float32, error) {
	if modelID == "" {
		modelID = "text-embedding-005"
	}

	config := &genai.EmbedContentConfig{
		TaskType: taskType,
	}

	contents := []*genai.Content{
		{
			Parts: []*genai.Part{{Text: text}},
		},
	}

	res, err := v.client.Models.EmbedContent(ctx, modelID, contents, config)
	if err != nil {
		return nil, fmt.Errorf("embed content failed: %w", err)
	}

	if len(res.Embeddings) == 0 {
		return nil, fmt.Errorf("no embedding returned")
	}

	return res.Embeddings[0].Values, nil
}

// EmbedBatch generates embeddings for a batch of texts.
func (v *VertexClient) EmbedBatch(ctx context.Context, modelID string, texts []string, taskType string) ([][]float32, error) {
	if modelID == "" {
		modelID = "text-embedding-005"
	}

	config := &genai.EmbedContentConfig{
		TaskType: taskType,
	}

	var contents []*genai.Content
	for _, t := range texts {
		contents = append(contents, &genai.Content{
			Parts: []*genai.Part{{Text: t}},
		})
	}

	res, err := v.client.Models.EmbedContent(ctx, modelID, contents, config)
	if err != nil {
		return nil, fmt.Errorf("batch embed content failed: %w", err)
	}

	var results [][]float32
	for _, e := range res.Embeddings {
		results = append(results, e.Values)
	}

	return results, nil
}

// GenerateImages generates images using Imagen.
func (v *VertexClient) GenerateImages(ctx context.Context, modelID, prompt string, count int, aspectRatio string) ([]genai.Image, error) {
	if modelID == "" {
		modelID = "imagen-3.0-generate-001"
	}

	config := &genai.GenerateImagesConfig{
		NumberOfImages: int32(count),
		AspectRatio:    aspectRatio,
	}

	resp, err := v.client.Models.GenerateImages(ctx, modelID, prompt, config)
	if err != nil {
		return nil, fmt.Errorf("generate images failed: %w", err)
	}

	var results []genai.Image
	for _, img := range resp.GeneratedImages {
		if img.Image != nil {
			results = append(results, *img.Image)
		}
	}

	return results, nil
}
