package wisdev

import (
	"context"
	"strings"
)

type loopProgressContextKey struct{}

// LoopProgressEmitter publishes structured stage events for CLI/TUI consumers.
type LoopProgressEmitter struct {
	Emit func(PlanExecutionEvent)
	Req  LoopRequest
}

func WithLoopProgress(ctx context.Context, emitter *LoopProgressEmitter) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if emitter == nil || emitter.Emit == nil {
		return ctx
	}
	return context.WithValue(ctx, loopProgressContextKey{}, emitter)
}

func LoopProgressFromContext(ctx context.Context) *LoopProgressEmitter {
	if ctx == nil {
		return nil
	}
	emitter, _ := ctx.Value(loopProgressContextKey{}).(*LoopProgressEmitter)
	if emitter == nil || emitter.Emit == nil {
		return nil
	}
	return emitter
}

func EmitLoopStage(ctx context.Context, stage, message string, payload map[string]any) {
	emitter := LoopProgressFromContext(ctx)
	if emitter == nil {
		return
	}
	emitLoopProgress(emitter.Emit, emitter.Req, stage, message, payload)
}

func EmitLoopDegraded(ctx context.Context, stage, message string, payload map[string]any) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["degraded"] = true
	if strings.TrimSpace(AsOptionalString(payload["fallback"])) == "" {
		payload["fallback"] = "heuristic"
	}
	EmitLoopStage(ctx, stage, message, payload)
}

func EmitLoopFailed(ctx context.Context, stage, message string, payload map[string]any) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["failed"] = true
	EmitLoopStage(ctx, stage, message, payload)
}
