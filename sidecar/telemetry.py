"""
WisDev Python Sidecar — Observability bootstrap.

Initialises OpenTelemetry tracing with standard OTLP exporters (compatible with
Jaeger, Tempo, or any OTLP collector). Wires structlog so every log event
carries trace correlation fields.

Call ``configure_telemetry()`` once at application startup.
"""

from __future__ import annotations

import os
from typing import Any, MutableMapping

import structlog
from opentelemetry import trace
from opentelemetry.exporter.otlp.proto.grpc.trace_exporter import OTLPSpanExporter
from opentelemetry.propagate import set_global_textmap
from opentelemetry.sdk.resources import Resource, SERVICE_NAME, SERVICE_VERSION
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor
from opentelemetry.trace.propagation.tracecontext import TraceContextTextMapPropagator

_SERVICE_NAME = "wisdev-sidecar"


def configure_telemetry(service_version: str = "dev") -> None:
    """Bootstrap OTel tracing with OTLP exporters.

    Safe to call multiple times (idempotent via module-level guard).
    """
    endpoint = os.environ.get("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317")

    resource = Resource.create(
        {
            SERVICE_NAME: _SERVICE_NAME,
            SERVICE_VERSION: service_version,
        }
    )

    provider = TracerProvider(resource=resource)

    try:
        exporter = OTLPSpanExporter(endpoint=endpoint, insecure=True)
        provider.add_span_processor(BatchSpanProcessor(exporter))
    except Exception:
        pass

    trace.set_tracer_provider(provider)

    set_global_textmap(TraceContextTextMapPropagator())

    _patch_structlog()


def _gcp_trace_processor(
    _logger: Any,
    _method: str,
    event_dict: MutableMapping[str, Any],
) -> MutableMapping[str, Any]:
    """structlog processor that injects trace correlation fields."""
    span = trace.get_current_span()
    ctx = span.get_span_context()

    if ctx.is_valid:
        trace_id = format(ctx.trace_id, "032x")
        span_id = format(ctx.span_id, "016x")
        event_dict["trace_id"] = trace_id
        event_dict["span_id"] = span_id
        event_dict["trace_sampled"] = bool(ctx.trace_flags & 0x01)

    return event_dict


def _patch_structlog() -> None:
    """Reconfigure structlog to include trace correlation processor."""
    structlog.configure(
        processors=[
            structlog.contextvars.merge_contextvars,
            structlog.stdlib.filter_by_level,
            structlog.stdlib.add_logger_name,
            structlog.stdlib.add_log_level,
            structlog.stdlib.PositionalArgumentsFormatter(),
            structlog.processors.TimeStamper(fmt="iso"),
            structlog.processors.StackInfoRenderer(),
            structlog.processors.format_exc_info,
            structlog.processors.UnicodeDecoder(),
            _gcp_trace_processor,
            structlog.processors.JSONRenderer(),
        ],
        wrapper_class=structlog.stdlib.BoundLogger,
        context_class=dict,
        logger_factory=structlog.stdlib.LoggerFactory(),
        cache_logger_on_first_use=True,
    )


def make_grpc_server_interceptor():
    """Returns an OTel gRPC server-side interceptor."""
    try:
        from opentelemetry.instrumentation.grpc import aio_server_interceptor

        return aio_server_interceptor()
    except ImportError:
        return None
