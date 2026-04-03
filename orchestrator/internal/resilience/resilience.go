package resilience

import (
	"context"
)

type contextKey string

const (
	ctxDegraded contextKey = "degraded"
)

func IsDegraded(ctx context.Context) bool {
	if val, ok := ctx.Value(ctxDegraded).(bool); ok {
		return val
	}
	return false
}

func SetDegraded(ctx context.Context, degraded bool) context.Context {
	return context.WithValue(ctx, ctxDegraded, degraded)
}
