package telemetry

import (
	"context"
	"testing"
)

func TestInitTracing_NoEndpointReturnsNoopTracer(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	shutdown, err := InitTracing("test-service-noop")
	if err != nil {
		t.Fatalf("expected no error for empty endpoint, got %v", err)
	}
	if shutdown == nil {
		t.Fatal("expected shutdown function to be returned")
	}

	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("expected shutdown to succeed for noop tracer: %v", err)
	}
}

func TestInitTracing_EndpointPathCreatesTracerProvider(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317")

	shutdown, err := InitTracing("test-service-otel")
	if err != nil {
		t.Fatalf("expected tracing init with local endpoint to succeed, got %v", err)
	}
	if shutdown == nil {
		t.Fatal("expected shutdown function to be returned")
	}

	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("expected shutdown to succeed, got %v", err)
	}
}
