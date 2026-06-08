package llm

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	llmpb "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

type vertexInterface interface {
	GenerateText(ctx context.Context, modelID, prompt, systemPrompt string, temperature float32, maxTokens int32) (string, error)
	GenerateStructured(ctx context.Context, modelID, prompt, systemPrompt string, jsonSchemaStr string, temperature float32, maxTokens int32) (string, error)
	EmbedText(ctx context.Context, modelID, text, taskType string) ([]float32, error)
	EmbedBatch(ctx context.Context, modelID string, texts []string, taskType string) ([][]float32, error)
}

type vertexStructuredPolicyInterface interface {
	GenerateStructuredWithPolicy(ctx context.Context, modelID, prompt, systemPrompt string, jsonSchemaStr string, temperature float32, maxTokens int32, serviceTier string, thinkingBudget *int32, requestClass string, retryProfile string) (string, error)
}

// Server implements the LLMService gRPC interface.
type Server struct {
	llmpb.UnimplementedLLMServiceServer
	vertex vertexInterface
}

var errStructuredSchemaRequired = errors.New("structured output requires json_schema")

// NewServer creates a new LLM gRPC server.
func NewServer(vertex vertexInterface) *Server {
	return &Server{
		vertex: vertex,
	}
}

// Generate produces a text completion.
func (s *Server) Generate(ctx context.Context, req *llmpb.GenerateRequest) (*llmpb.GenerateResponse, error) {
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

	return &llmpb.GenerateResponse{
		Text:         text,
		ModelUsed:    req.Model,
		FinishReason: "stop",
		LatencyMs:    time.Since(start).Milliseconds(),
	}, nil
}

// Embed generates a single embedding.
func (s *Server) Embed(ctx context.Context, req *llmpb.EmbedRequest) (*llmpb.EmbedResponse, error) {
	start := time.Now()

	values, err := s.vertex.EmbedText(ctx, req.Model, req.Text, req.TaskType)
	if err != nil {
		return nil, err
	}

	return &llmpb.EmbedResponse{
		Embedding: values,
		ModelUsed: req.Model,
		LatencyMs: time.Since(start).Milliseconds(),
	}, nil
}

// EmbedBatch generates multiple embeddings.
func (s *Server) EmbedBatch(ctx context.Context, req *llmpb.EmbedBatchRequest) (*llmpb.EmbedBatchResponse, error) {
	start := time.Now()

	batchValues, err := s.vertex.EmbedBatch(ctx, req.Model, req.Texts, req.TaskType)
	if err != nil {
		return nil, err
	}

	var embeddings []*llmpb.EmbedVector
	for _, v := range batchValues {
		embeddings = append(embeddings, &llmpb.EmbedVector{
			Values: v,
		})
	}

	return &llmpb.EmbedBatchResponse{
		Embeddings: embeddings,
		ModelUsed:  req.Model,
		LatencyMs:  time.Since(start).Milliseconds(),
	}, nil
}

// GenerateStream simulates server-side streaming by generating the full
// response via Vertex AI and sending it in fixed-size rune chunks.
func (s *Server) GenerateStream(req *llmpb.GenerateRequest, stream llmpb.LLMService_GenerateStreamServer) error {
	text, err := s.vertex.GenerateText(
		stream.Context(),
		req.Model,
		req.Prompt,
		req.SystemPrompt,
		req.Temperature,
		req.MaxTokens,
	)
	if err != nil {
		return err
	}

	if len(text) == 0 {
		return stream.Send(&llmpb.GenerateChunk{Delta: "", Done: true, FinishReason: "stop"})
	}

	const chunkSize = 128
	runes := []rune(text)
	for i := 0; i < len(runes); i += chunkSize {
		end := i + chunkSize
		if end > len(runes) {
			end = len(runes)
		}
		done := end == len(runes)
		finishReason := ""
		if done {
			finishReason = "stop"
		}
		if err := stream.Send(&llmpb.GenerateChunk{
			Delta:        string(runes[i:end]),
			Done:         done,
			FinishReason: finishReason,
		}); err != nil {
			return err
		}
	}
	return nil
}

// StructuredOutput generates a JSON response using Gemini native structured
// output instead of prompt-injected schema text.
func (s *Server) StructuredOutput(ctx context.Context, req *llmpb.StructuredRequest) (*llmpb.StructuredResponse, error) {
	start := time.Now()
	if req == nil || strings.TrimSpace(req.GetJsonSchema()) == "" {
		return nil, errStructuredSchemaRequired
	}
	var text string
	var err error
	if policyVertex, ok := s.vertex.(vertexStructuredPolicyInterface); ok {
		text, err = policyVertex.GenerateStructuredWithPolicy(
			ctx,
			req.Model,
			req.Prompt,
			req.SystemPrompt,
			req.JsonSchema,
			req.Temperature,
			req.MaxTokens,
			req.ServiceTier,
			req.ThinkingBudget,
			req.RequestClass,
			req.RetryProfile,
		)
	} else {
		text, err = s.vertex.GenerateStructured(
			ctx,
			req.Model,
			req.Prompt,
			req.SystemPrompt,
			req.JsonSchema,
			req.Temperature,
			req.MaxTokens,
		)
	}
	if err != nil {
		return nil, err
	}

	schemaValid := json.Valid([]byte(text))
	errMsg := ""
	if !schemaValid {
		errMsg = "vertex structured output returned invalid JSON"
	}

	return &llmpb.StructuredResponse{
		JsonResult:  text,
		ModelUsed:   req.Model,
		SchemaValid: schemaValid,
		Error:       errMsg,
		LatencyMs:   time.Since(start).Milliseconds(),
	}, nil
}

// Health returns the sidecar's readiness state.
func (s *Server) Health(ctx context.Context, req *llmpb.HealthRequest) (*llmpb.HealthResponse, error) {
	return &llmpb.HealthResponse{
		Ok:      true,
		Version: "1.1.0-go",
	}, nil
}
