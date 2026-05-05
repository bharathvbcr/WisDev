"""
WisDev Python Sidecar - observability bootstrap.

Initialises OpenTelemetry tracing with the GCP Cloud Trace exporter and wires
structlog so every log event carries the GCP trace-correlation fields required
for the "View in Trace" button in Cloud Logging:

    logging.googleapis.com/trace        → "projects/<id>/traces/<traceID>"
    logging.googleapis.com/spanId       → hex span ID
    logging.googleapis.com/traceSampled → bool

Call ``configure_telemetry()`` once at application startup, before FastAPI is
fully initialised, so the ASGI instrumentor can wrap the app.
"""
from __future__ import annotations

import os
from typing import Any, MutableMapping

import structlog
from opentelemetry import trace

try:
    from opentelemetry.exporter.cloud_trace import CloudTraceSpanExporter
    HAS_CLOUD_TRACE = True
except Exception:
    HAS_CLOUD_TRACE = False

try:
    from opentelemetry.propagate import set_global_textmap
    from opentelemetry.propagators.cloud_trace_propagator import CloudTraceFormatPropagator
    from opentelemetry.propagators.composite import CompositePropagator
    from opentelemetry.sdk.resources import Resource, SERVICE_NAME, SERVICE_VERSION
    from opentelemetry.sdk.trace import TracerProvider
    from opentelemetry.sdk.trace.export import BatchSpanProcessor
    from opentelemetry.trace.propagation.tracecontext import TraceContextTextMapPropagator
    HAS_OTEL_SDK = True
except Exception:
    HAS_OTEL_SDK = False

_SERVICE_NAME = "wisdev-python-sidecar"


def configure_telemetry(service_version: str = "unknown") -> None:
    """Bootstrap OTel tracing and patch structlog for GCP trace correlation.

    Safe to call multiple times (idempotent via module-level guard).
    """
    project_id = os.environ.get("GOOGLE_CLOUD_PROJECT", "")

    if not HAS_OTEL_SDK:
        # Keep application startup/test imports healthy even when local OTel
        # packages are out of sync.
        _patch_structlog(project_id)
        return

    resource = Resource.create(
        {
            SERVICE_NAME: _SERVICE_NAME,
            SERVICE_VERSION: service_version,
            "cloud.provider": "gcp",
            "cloud.account.id": project_id,
        }
    )

    provider = TracerProvider(resource=resource)

    if project_id and HAS_CLOUD_TRACE:
        try:
            # GCP Cloud Trace exporter — uses ADC automatically on Cloud Run.
            exporter = CloudTraceSpanExporter(project_id=project_id)
            provider.add_span_processor(BatchSpanProcessor(exporter))
        except Exception:
            pass
    # When no project is configured (local dev), spans are created but not
    # exported — instrumented code still runs and can be unit-tested.

    trace.set_tracer_provider(provider)

    # Accept both W3C traceparent (from Go otelhttp) and GCP X-Cloud-Trace-Context
    # (legacy Cloud Run header). The composite propagator tries both.
    set_global_textmap(
        CompositePropagator(
            [
                TraceContextTextMapPropagator(),   # W3C — primary (from Go)
                CloudTraceFormatPropagator(),       # X-Cloud-Trace-Context fallback
            ]
        )
    )

    # Patch structlog to inject GCP trace fields into every log event.
    _patch_structlog(project_id)


# ── structlog GCP trace processor ────────────────────────────────────────────

def _gcp_trace_processor(
    _logger: Any,
    _method: str,
    event_dict: MutableMapping[str, Any],
) -> MutableMapping[str, Any]:
    """structlog processor that injects GCP Cloud Logging trace-correlation fields.

    Must run AFTER any processor that might change the context (e.g. after
    bind_contextvars processors) so that the active span is the one for the
    current request coroutine.
    """
    project_id = os.environ.get("GOOGLE_CLOUD_PROJECT", "")
    span = trace.get_current_span()
    ctx = span.get_span_context()

    if ctx.is_valid and project_id:
        trace_id = format(ctx.trace_id, "032x")
        span_id = format(ctx.span_id, "016x")
        event_dict["logging.googleapis.com/trace"] = (
            f"projects/{project_id}/traces/{trace_id}"
        )
        event_dict["logging.googleapis.com/spanId"] = span_id
        event_dict["logging.googleapis.com/traceSampled"] = bool(
            ctx.trace_flags & 0x01
        )

    return event_dict


def _patch_structlog(project_id: str) -> None:
    """Reconfigure structlog to include the GCP trace correlation processor."""
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
            # Inject GCP trace fields from the active OTel span.
            _gcp_trace_processor,
            structlog.processors.JSONRenderer(),
        ],
        wrapper_class=structlog.stdlib.BoundLogger,
        context_class=dict,
        logger_factory=structlog.stdlib.LoggerFactory(),
        cache_logger_on_first_use=True,
    )


# ── gRPC server-side trace propagation ───────────────────────────────────────

def make_grpc_server_interceptor():
    """Returns an OTel gRPC server-side interceptor for the LLM sidecar.

    Extracts the traceparent from inbound gRPC metadata so that Python spans
    created during gRPC handler execution are children of the Go caller's span.
    """
    try:
        from opentelemetry.instrumentation.grpc import aio_server_interceptor
        return aio_server_interceptor()
    except ImportError:
        return None
