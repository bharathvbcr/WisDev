"""Structured route-stage logging complementing request_lifecycle_middleware."""
from __future__ import annotations

import functools
import time
from typing import Any, Callable, TypeVar

import structlog

logger = structlog.get_logger(__name__)

F = TypeVar("F", bound=Callable[..., Any])


def log_route_stage(
    component: str,
    operation: str,
    *,
    query_field: str | None = None,
) -> Callable[[F], F]:
    """Decorator for FastAPI handlers: logs request_received and response stages."""

    def decorator(fn: F) -> F:
        @functools.wraps(fn)
        async def wrapper(*args: Any, **kwargs: Any) -> Any:
            started = time.monotonic()
            query_preview = ""
            if query_field:
                raw = kwargs.get(query_field)
                if raw is None and args:
                    raw = getattr(args[0], query_field, None)
                if isinstance(raw, str):
                    query_preview = raw[:120]
                elif raw is not None and hasattr(raw, "query"):
                    q = getattr(raw, "query", "")
                    if isinstance(q, str):
                        query_preview = q[:120]

            logger.info(
                "sidecar_route_received",
                service="wisdev-python-sidecar",
                runtime="python",
                component=component,
                operation=operation,
                stage="request_received",
                query_preview=query_preview,
                result="accepted",
            )
            try:
                result = await fn(*args, **kwargs)
                latency_ms = int((time.monotonic() - started) * 1000)
                logger.info(
                    "sidecar_route_completed",
                    service="wisdev-python-sidecar",
                    runtime="python",
                    component=component,
                    operation=operation,
                    stage="response",
                    query_preview=query_preview,
                    latency_ms=latency_ms,
                    result="success",
                )
                return result
            except Exception as exc:
                latency_ms = int((time.monotonic() - started) * 1000)
                logger.warning(
                    "sidecar_route_failed",
                    service="wisdev-python-sidecar",
                    runtime="python",
                    component=component,
                    operation=operation,
                    stage="response",
                    query_preview=query_preview,
                    latency_ms=latency_ms,
                    result="error",
                    error=str(exc),
                )
                raise

        return wrapper  # type: ignore[return-value]

    return decorator
