package search

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	oteltrace "go.opentelemetry.io/otel/trace"
)

func TestBuildProviderSearchLogArgsIncludesConsistentStageFields(t *testing.T) {
	args := buildProviderSearchLogArgs(
		"provider_dispatch_start",
		"openalex",
		"  neural   retrieval  ",
		SearchOpts{Limit: 7, Domain: "cs", TraceID: "trace-provider", Sources: []string{"openalex"}},
		"result",
		"started",
	)
	fields := providerLogArgsToMap(args)

	assert.Equal(t, "go_orchestrator", fields["service"])
	assert.Equal(t, "go", fields["runtime"])
	assert.Equal(t, "search.provider", fields["component"])
	assert.Equal(t, "provider_search", fields["operation"])
	assert.Equal(t, "provider_dispatch_start", fields["stage"])
	assert.Equal(t, "openalex", fields["provider"])
	assert.Equal(t, "trace-provider", fields["trace_id"])
	assert.Equal(t, "neural retrieval", fields["query_preview"])
	assert.Equal(t, len("neural retrieval"), fields["query_length"])
	assert.NotEmpty(t, fields["query_hash"])
	assert.Equal(t, 7, fields["limit"])
	assert.Equal(t, "cs", fields["domain"])
	assert.Equal(t, 1, fields["source_count"])
	assert.Equal(t, "started", fields["result"])
}

func TestBuildProviderSearchLogArgsDefaultsStageAndEmptyQueryHash(t *testing.T) {
	args := buildProviderSearchLogArgs("", "core", "", SearchOpts{})
	fields := providerLogArgsToMap(args)

	assert.Equal(t, "unspecified", fields["stage"])
	assert.NotContains(t, fields, "trace_id")
	assert.Equal(t, "", fields["query_preview"])
	assert.Equal(t, 0, fields["query_length"])
	assert.Equal(t, "", fields["query_hash"])
}

func TestBuildProviderSearchLogArgsForContextFallsBackToSpanTraceID(t *testing.T) {
	traceID, err := oteltrace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
	assert.NoError(t, err)
	spanID, err := oteltrace.SpanIDFromHex("0123456789abcdef")
	assert.NoError(t, err)
	ctx := oteltrace.ContextWithSpanContext(context.Background(), oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID: traceID,
		SpanID:  spanID,
	}))

	args := buildProviderSearchLogArgsForContext(ctx, "provider_dispatch_failed", "pubmed", "sleep", SearchOpts{})
	fields := providerLogArgsToMap(args)

	assert.Equal(t, traceID.String(), fields["trace_id"])
}

func TestBuildProviderSearchLogArgsForContextPrefersExplicitRequestTraceID(t *testing.T) {
	traceID, err := oteltrace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
	assert.NoError(t, err)
	spanID, err := oteltrace.SpanIDFromHex("0123456789abcdef")
	assert.NoError(t, err)
	ctx := oteltrace.ContextWithSpanContext(context.Background(), oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID: traceID,
		SpanID:  spanID,
	}))

	args := buildProviderSearchLogArgsForContext(ctx, "provider_dispatch_failed", "pubmed", "sleep", SearchOpts{
		TraceID: "req-live-autonomous",
	})
	fields := providerLogArgsToMap(args)

	assert.Equal(t, "req-live-autonomous", fields["trace_id"])
}

func TestHandleToolSearchPassesTraceIDToProviderFanout(t *testing.T) {
	reg := NewProviderRegistry()
	var seenTraceID string
	reg.Register(&MockProvider{
		name: "mock",
		searchFn: func(ctx context.Context, query string, opts SearchOpts) ([]Paper, error) {
			seenTraceID = opts.TraceID
			return []Paper{{ID: "p1", Title: "Paper"}}, nil
		},
	})

	result, err := HandleToolSearch(context.Background(), reg, ToolSearchPapersName, map[string]any{
		"query":   "sleep interventions",
		"limit":   1,
		"traceId": "trace-tool-search",
	})

	assert.NoError(t, err)
	assert.Len(t, result.Papers, 1)
	assert.Equal(t, "trace-tool-search", seenTraceID)
}

func providerLogArgsToMap(args []any) map[string]any {
	fields := make(map[string]any, len(args)/2)
	for i := 0; i+1 < len(args); i += 2 {
		key, ok := args[i].(string)
		if !ok {
			continue
		}
		fields[key] = args[i+1]
	}
	return fields
}
