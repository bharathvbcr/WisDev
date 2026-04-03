package llm

import (
	"context"
	"time"

	llmv1 "github.com/wisdev-agent/wisdev-agent-os/orchestrator/proto/llm/v1"
)

// Server implements the LLMService gRPC interface.
type Server struct {
	llmv1.UnimplementedLLMServiceServer
	vertex *VertexClient
}

// NewServer creates a new LLM gRPC server.
func NewServer(vertex *VertexClient) *Server {
	return &Server{
		vertex: vertex,
	}
}

// Generate produces a text completion.
func (s *Server) Generate(ctx context.Context, req *llmv1.GenerateRequest) (*llmv1.GenerateResponse, error) {
	start := time.Now()
	
	text, err := s.vertex.GenerateText(
		ctx,
		req.Model,
		req.Prompt,
		req.SystemPrompt,
		req.Temperature,
		req.MaxTokens,
	)
	if err != nil {
		return nil, err
	}

	return &llmv1.GenerateResponse{
		Text:         text,
		ModelUsed:    req.Model,
		FinishReason: "stop",
		LatencyMs:    time.Since(start).Milliseconds(),
	}, nil
}

// Embed generates a single embedding.
func (s *Server) Embed(ctx context.Context, req *llmv1.EmbedRequest) (*llmv1.EmbedResponse, error) {
	start := time.Now()
	
	values, err := s.vertex.EmbedText(ctx, req.Model, req.Text, req.TaskType)
	if err != nil {
		return nil, err
	}

	return &llmv1.EmbedResponse{
		Embedding: values,
		ModelUsed: req.Model,
		LatencyMs: time.Since(start).Milliseconds(),
	}, nil
}

// EmbedBatch generates multiple embeddings.
func (s *Server) EmbedBatch(ctx context.Context, req *llmv1.EmbedBatchRequest) (*llmv1.EmbedBatchResponse, error) {
	start := time.Now()
	
	batchValues, err := s.vertex.EmbedBatch(ctx, req.Model, req.Texts, req.TaskType)
	if err != nil {
		return nil, err
	}

	var embeddings []*llmv1.EmbedVector
	for _, v := range batchValues {
		embeddings = append(embeddings, &llmv1.EmbedVector{
			Values: v,
		})
	}

	return &llmv1.EmbedBatchResponse{
		Embeddings: embeddings,
		ModelUsed:  req.Model,
		LatencyMs:  time.Since(start).Milliseconds(),
	}, nil
}

// Health returns the sidecar's readiness state.
func (s *Server) Health(ctx context.Context, req *llmv1.HealthRequest) (*llmv1.HealthResponse, error) {
	return &llmv1.HealthResponse{
		Ok:      true,
		Version: "1.1.0-go",
	}, nil
}
