package llm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/core"
	genkitmiddleware "github.com/firebase/genkit/go/plugins/middleware"
)

type vertexGenkitModelCallOptions struct {
	Operation     string
	ModelID       string
	RequestClass  string
	RetryProfile  string
	Backend       string
	MaxRetries    int
	InitialDelay  time.Duration
	MaxDelay      time.Duration
	BackoffFactor float64
	NoJitter      bool
}

type vertexGenkitTraceMiddleware struct {
	operation    string
	modelID      string
	requestClass string
	retryProfile string
	backend      string
	startedAt    time.Time
}

func (m *vertexGenkitTraceMiddleware) Name() string {
	return "scholarlm/vertex-trace"
}

func (m *vertexGenkitTraceMiddleware) New(ctx context.Context) (*ai.Hooks, error) {
	return &ai.Hooks{
		WrapModel: func(ctx context.Context, params *ai.ModelParams, next ai.ModelNext) (*ai.ModelResponse, error) {
			started := time.Now()
			slog.Info("vertex genkit middleware model call start",
				"component", "llm.vertex",
				"operation", m.operation,
				"stage", "genkit_middleware_start",
				"model", m.modelID,
				"request_class", m.requestClass,
				"retry_profile", m.retryProfile,
				"elapsed_ms", time.Since(m.startedAt).Milliseconds(),
				"backend", m.backend,
			)
			resp, err := next(ctx, params)
			fields := []any{
				"component", "llm.vertex",
				"operation", m.operation,
				"model", m.modelID,
				"request_class", m.requestClass,
				"retry_profile", m.retryProfile,
				"latency_ms", time.Since(started).Milliseconds(),
				"total_elapsed_ms", time.Since(m.startedAt).Milliseconds(),
				"backend", m.backend,
			}
			if err != nil {
				slog.Warn("vertex genkit middleware model call failed", append(fields,
					"stage", "genkit_middleware_failed",
					"error_class", classifyVertexError(err),
					"error_summary", safeVertexGenkitErrorSummary(err),
				)...)
				return nil, err
			}
			slog.Info("vertex genkit middleware model call success", append(fields,
				"stage", "genkit_middleware_success",
			)...)
			return resp, nil
		},
	}, nil
}

func runVertexGenkitModelMiddleware[T any](ctx context.Context, opts vertexGenkitModelCallOptions, call func(context.Context) (T, error)) (T, error) {
	var zero T
	if ctx == nil {
		ctx = context.Background()
	}
	if call == nil {
		return zero, fmt.Errorf("vertex genkit middleware call is required")
	}

	startedAt := time.Now()
	initialDelay := opts.InitialDelay
	if initialDelay <= 0 {
		initialDelay = 200 * time.Millisecond
	}
	maxDelay := opts.MaxDelay
	if maxDelay <= 0 {
		maxDelay = initialDelay
	}
	backoffFactor := opts.BackoffFactor
	if backoffFactor <= 0 {
		backoffFactor = 2
	}
	maxRetries := opts.MaxRetries
	if maxRetries > 0 {
		if ok, remainingMs := hasVertexRetryBudget(ctx, initialDelay); !ok {
			slog.Warn("vertex genkit middleware retry skipped",
				"component", "llm.vertex",
				"operation", opts.Operation,
				"stage", "retry_budget_exhausted",
				"model", opts.ModelID,
				"request_class", opts.RequestClass,
				"retry_profile", opts.RetryProfile,
				"retry_delay_ms", initialDelay.Milliseconds(),
				"ctx_remaining_ms", remainingMs,
				"backend", opts.Backend,
			)
			maxRetries = 0
		}
	}

	wrapped := func(callCtx context.Context, params *ai.ModelParams) (*ai.ModelResponse, error) {
		result, err := call(callCtx)
		if err != nil {
			return nil, vertexErrorAsGenkitError(err)
		}
		return &ai.ModelResponse{
			Raw:     result,
			Message: &ai.Message{Role: ai.RoleModel},
		}, nil
	}

	middlewares := []ai.Middleware{
		&vertexGenkitTraceMiddleware{
			operation:    strings.TrimSpace(opts.Operation),
			modelID:      strings.TrimSpace(opts.ModelID),
			requestClass: strings.TrimSpace(opts.RequestClass),
			retryProfile: strings.TrimSpace(opts.RetryProfile),
			backend:      strings.TrimSpace(opts.Backend),
			startedAt:    startedAt,
		},
	}
	if maxRetries > 0 {
		middlewares = append(middlewares, &genkitmiddleware.Retry{
			MaxRetries:     maxRetries,
			Statuses:       vertexGenkitRetryStatuses(),
			InitialDelayMs: int(initialDelay / time.Millisecond),
			MaxDelayMs:     int(maxDelay / time.Millisecond),
			BackoffFactor:  backoffFactor,
			NoJitter:       opts.NoJitter,
		})
	}

	for i := len(middlewares) - 1; i >= 0; i-- {
		hooks, err := middlewares[i].New(ctx)
		if err != nil {
			return zero, err
		}
		if hooks == nil || hooks.WrapModel == nil {
			continue
		}
		next := wrapped
		wrap := hooks.WrapModel
		wrapped = func(callCtx context.Context, params *ai.ModelParams) (*ai.ModelResponse, error) {
			return wrap(callCtx, params, next)
		}
	}

	resp, err := wrapped(ctx, &ai.ModelParams{Request: &ai.ModelRequest{}})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return zero, ctxErr
		}
		return zero, err
	}
	if resp == nil {
		return zero, fmt.Errorf("vertex genkit middleware returned nil response")
	}
	result, ok := resp.Raw.(T)
	if !ok {
		return zero, fmt.Errorf("vertex genkit middleware returned unexpected response type %T", resp.Raw)
	}
	return result, nil
}

func vertexGenkitRetryStatuses() []core.StatusName {
	return []core.StatusName{
		core.UNAVAILABLE,
		core.DEADLINE_EXCEEDED,
		core.ABORTED,
		core.INTERNAL,
	}
}

func vertexErrorAsGenkitError(err error) error {
	if err == nil {
		return nil
	}
	var genkitErr *core.GenkitError
	if errors.As(err, &genkitErr) {
		return err
	}
	status := vertexErrorStatus(err)
	return core.NewError(status, "%v", err)
}

func vertexErrorStatus(err error) core.StatusName {
	if err == nil {
		return core.OK
	}
	switch {
	case errors.Is(err, context.Canceled):
		return core.CANCELLED
	case errors.Is(err, context.DeadlineExceeded):
		return core.DEADLINE_EXCEEDED
	}
	switch classifyVertexError(err) {
	case "unsupported_parameter", "invalid_request":
		return core.INVALID_ARGUMENT
	case "timeout":
		return core.DEADLINE_EXCEEDED
	case "rate_limit":
		return core.RESOURCE_EXHAUSTED
	case "unavailable":
		return core.UNAVAILABLE
	case "canceled":
		return core.CANCELLED
	default:
		return core.INTERNAL
	}
}

func safeVertexGenkitErrorSummary(err error) string {
	if err == nil {
		return ""
	}
	switch classifyVertexError(err) {
	case "unsupported_parameter":
		return "unsupported Vertex parameter"
	case "invalid_request":
		return "invalid Vertex request"
	case "timeout":
		return "Vertex request timed out"
	case "rate_limit":
		return "Vertex provider rate limited"
	case "unavailable":
		return "Vertex provider unavailable"
	case "canceled":
		return "Vertex request canceled"
	default:
		return "Vertex model call failed"
	}
}
