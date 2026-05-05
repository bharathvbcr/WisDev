from __future__ import annotations

import asyncio
import inspect
import json
import logging
import os
import time
from typing import Any, AsyncIterator, Mapping, cast

import grpc
from proto import require_generated_module, require_proto_runtime_compatibility

require_proto_runtime_compatibility()
llm_pb2 = require_generated_module("llm_pb2")

from services.gemini_service import (
    GEMINI_HEAVY_MODEL,
    GEMINI_LIGHT_MODEL,
    GeminiService,
    StructuredOutputRequiresNativeRuntimeError,
    _is_cold_start_window as _gemini_is_cold_start_window,
    _resolve_embedding_model,
    _service_uptime_ms as _gemini_service_uptime_ms,
    get_gemini_runtime_diagnostics,
)

logger = logging.getLogger(__name__)

SERVER_VERSION = "1.1.0-sidecar"
DEFAULT_LATENCY_BUDGET_MS = 45_000
MIN_LATENCY_BUDGET_MS = 4_000
MAX_LATENCY_BUDGET_MS = 90_000


class LLMRuntimeError(Exception):
    def __init__(
        self,
        *,
        grpc_status: grpc.StatusCode,
        http_status: int,
        code: str,
        message: str,
        trace_id: str = "",
        details: dict[str, Any] | None = None,
    ):
        super().__init__(message)
        self.grpc_status = grpc_status
        self.http_status = http_status
        self.code = code
        self.message = message
        self.trace_id = trace_id
        self.details = details or None


def error_envelope(exc: LLMRuntimeError) -> dict[str, Any]:
    payload: dict[str, Any] = {
        "ok": False,
        "traceId": exc.trace_id,
        "error": {
            "code": exc.code,
            "message": exc.message,
            "status": exc.http_status,
            "traceId": exc.trace_id,
        },
    }
    if exc.details:
        payload["error"]["details"] = exc.details
    return payload


def trace_id_from_request(
    request: Any, metadata: Mapping[str, str] | None = None
) -> str:
    request_metadata = getattr(request, "metadata", None)
    if request_metadata:
        trace_id = request_metadata.get("trace_id", "")
        if trace_id:
            return trace_id
    return (metadata or {}).get("trace_id", "")


def _log(event: str, **fields: Any) -> None:
    if fields:
        logger.info("%s | %s", event, json.dumps(fields, sort_keys=True, default=str))
        return
    logger.info(event)


def _log_error(event: str, **fields: Any) -> None:
    if fields:
        logger.error("%s | %s", event, json.dumps(fields, sort_keys=True, default=str))
        return
    logger.error(event)


async def _invoke_compatible_method(
    method: Any, *, method_name: str, log_trace_id: str, **kwargs: Any
) -> Any:
    try:
        signature = inspect.signature(method)
    except (TypeError, ValueError):
        result = method(**kwargs)
        if inspect.isawaitable(result):
            return await result
        return result

    if any(
        parameter.kind == inspect.Parameter.VAR_KEYWORD
        for parameter in signature.parameters.values()
    ):
        result = method(**kwargs)
        if inspect.isawaitable(result):
            return await result
        return result

    allowed_fields = {
        name
        for name, parameter in signature.parameters.items()
        if parameter.kind
        in (
            inspect.Parameter.POSITIONAL_OR_KEYWORD,
            inspect.Parameter.KEYWORD_ONLY,
        )
    }
    filtered_kwargs = {
        key: value for key, value in kwargs.items() if key in allowed_fields
    }
    dropped_fields = sorted(set(kwargs) - set(filtered_kwargs))
    if dropped_fields:
        _log(
            "llm_runtime_kwargs_filtered",
            trace_id=log_trace_id,
            method=method_name,
            dropped_fields=dropped_fields,
        )

    result = method(**filtered_kwargs)
    if inspect.isawaitable(result):
        return await result
    return result


def _ensure_non_empty_prompt(prompt: str, trace_id: str) -> str:
    prompt = prompt.strip()
    if not prompt:
        raise ValueError(f"empty prompt for trace_id={trace_id or 'unknown'}")
    return prompt


def _merged_prompt(request: Any, trace_id: str) -> str:
    prompt = _ensure_non_empty_prompt(request.prompt, trace_id)
    if getattr(request, "system_prompt", ""):
        prompt = f"{request.system_prompt}\n\n{prompt}"
    return prompt


def _request_metadata_map(request: Any) -> Mapping[str, str]:
    metadata = getattr(request, "metadata", None)
    if isinstance(metadata, Mapping):
        return cast(Mapping[str, str], metadata)
    return {}


def _request_metadata_value(request: Any, key: str) -> str:
    value = _request_metadata_map(request).get(key, "")
    return str(value).strip()


def _request_metadata_int(request: Any, key: str) -> int | None:
    raw = _request_metadata_value(request, key)
    if not raw:
        return None
    try:
        return int(raw)
    except ValueError:
        return None


def _request_optional_int_field(
    request: Any, field_name: str, metadata_key: str | None = None
) -> int | None:
    has_field = getattr(request, "HasField", None)
    if callable(has_field):
        try:
            if has_field(field_name):
                return int(getattr(request, field_name))
            if metadata_key:
                return _request_metadata_int(request, metadata_key)
            return None
        except Exception:
            if metadata_key:
                return _request_metadata_int(request, metadata_key)
            return None
    value = getattr(request, field_name, None)
    if value is None:
        if metadata_key:
            return _request_metadata_int(request, metadata_key)
        return None
    try:
        return int(value)
    except (TypeError, ValueError):
        if metadata_key:
            return _request_metadata_int(request, metadata_key)
        return None


def _request_string_field(request: Any, field_name: str, metadata_key: str) -> str:
    raw = getattr(request, field_name, "")
    value = str(raw or "").strip()
    if value:
        return value
    return _request_metadata_value(request, metadata_key)


def _request_metadata_timeout_s(request: Any) -> float:
    raw = _request_metadata_value(request, "timeout_s")
    if not raw:
        return 60.0
    try:
        return max(float(raw), 1.0)
    except ValueError:
        return 60.0


def _request_metadata_latency_budget_ms(request: Any) -> int | None:
    typed_value = getattr(request, "latency_budget_ms", 0)
    try:
        typed_int = int(typed_value)
    except (TypeError, ValueError):
        typed_int = 0
    if typed_int > 0:
        return typed_int
    raw = _request_metadata_value(request, "latency_budget_ms")
    if not raw:
        return None
    try:
        return max(int(raw), MIN_LATENCY_BUDGET_MS)
    except ValueError:
        return None


def _normalize_latency_budget_ms(latency_budget_ms: int | None) -> int:
    if latency_budget_ms is None:
        return DEFAULT_LATENCY_BUDGET_MS
    return min(
        max(int(latency_budget_ms), MIN_LATENCY_BUDGET_MS), MAX_LATENCY_BUDGET_MS
    )


def _classify_runtime_error(exc: Exception) -> tuple[grpc.StatusCode, int, str]:
    if isinstance(exc, StructuredOutputRequiresNativeRuntimeError):
        return (
            grpc.StatusCode.FAILED_PRECONDITION,
            412,
            "STRUCTURED_OUTPUT_REQUIRES_NATIVE_RUNTIME",
        )
    if isinstance(exc, ValueError):
        return grpc.StatusCode.INVALID_ARGUMENT, 400, "INVALID_PROMPT"
    message = str(exc).lower()
    if (
        isinstance(exc, asyncio.TimeoutError)
        or "deadline exceeded" in message
        or "timed out" in message
    ):
        return grpc.StatusCode.DEADLINE_EXCEEDED, 504, "TIMEOUT"
    return grpc.StatusCode.INTERNAL, 500, "INTERNAL_ERROR"


def _validate_internal_key_sync(
    metadata: Mapping[str, str], internal_key: str, oidc_audience: str
) -> None:
    if oidc_audience:
        auth_header = metadata.get("authorization", "")
        if auth_header.startswith("Bearer "):
            id_token_str = auth_header[7:]
            try:
                from google.auth.transport import requests as google_requests
                from google.oauth2 import id_token

                id_token.verify_oauth2_token(
                    id_token_str, google_requests.Request(), oidc_audience
                )
                return
            except (
                Exception
            ) as exc:  # pragma: no cover - auth network errors are environment-specific
                _log("oidc_token_verification_failed", error=str(exc))

    provided_key = metadata.get("internal_service_key", "")
    if internal_key and provided_key == internal_key:
        return

    raise PermissionError("Unauthorized: missing or invalid service credentials")


async def validate_invocation_metadata(metadata: Mapping[str, str] | None) -> None:
    internal_key = os.environ.get("INTERNAL_SERVICE_KEY", "")
    oidc_audience = os.environ.get("OIDC_AUDIENCE", "")
    if not internal_key and not oidc_audience:
        return
    await asyncio.to_thread(
        _validate_internal_key_sync,
        dict(metadata or {}),
        internal_key,
        oidc_audience,
    )


class LLMRuntime:
    def __init__(self, gemini_factory: type[GeminiService] = GeminiService):
        self._gemini_factory = gemini_factory
        self._default_gemini = gemini_factory()

    async def generate(
        self,
        request: Any,
        *,
        metadata: Mapping[str, str] | None = None,
        validate_credentials: bool = True,
    ) -> Any:
        trace_id = trace_id_from_request(request, metadata)
        if validate_credentials:
            try:
                await validate_invocation_metadata(metadata)
            except PermissionError as exc:
                raise LLMRuntimeError(
                    grpc_status=grpc.StatusCode.PERMISSION_DENIED,
                    http_status=403,
                    code="UNAUTHORIZED",
                    message=str(exc),
                    trace_id=trace_id,
                ) from exc

        model = request.model or GEMINI_LIGHT_MODEL
        svc = self._gemini_factory(model=model)
        start_time = time.time()
        try:
            latency_budget_ms = _normalize_latency_budget_ms(
                _request_metadata_latency_budget_ms(request)
            )
            service_tier = (
                _request_string_field(request, "service_tier", "service_tier") or None
            )
            thinking_budget = _request_optional_int_field(
                request, "thinking_budget", "thinking_budget"
            )
            retry_profile = (
                _request_string_field(request, "retry_profile", "retry_profile") or None
            )
            request_class = (
                _request_string_field(request, "request_class", "request_class") or None
            )
            _log(
                "llm_generate_start",
                trace_id=trace_id,
                model=model,
                latency_budget_ms=latency_budget_ms,
                service_tier=service_tier,
                thinking_budget=thinking_budget,
                retry_profile=retry_profile,
                request_class=request_class,
                startup_age_ms=_gemini_service_uptime_ms(),
                cold_start_suspected=_gemini_is_cold_start_window(),
            )
            prompt = _merged_prompt(request, trace_id)
            text = await _invoke_compatible_method(
                svc.generate_text,
                method_name="generate_text",
                log_trace_id=trace_id,
                prompt=prompt,
                temperature=request.temperature or 0.7,
                max_tokens=request.max_tokens or 2048,
                service_tier=service_tier,
                thinking_budget=thinking_budget,
                latency_budget_ms=latency_budget_ms,
                retry_profile=retry_profile,
                request_class=request_class,
                trace_id=trace_id,
            )
            if not text.strip():
                raise RuntimeError("empty text response from Gemini")
            latency_ms = int((time.time() - start_time) * 1000)
            _log(
                "llm_generate_success",
                trace_id=trace_id,
                latency_ms=latency_ms,
                latency_budget_ms=latency_budget_ms,
                model=model,
                service_tier=service_tier,
                thinking_budget=thinking_budget,
                retry_profile=retry_profile,
                request_class=request_class,
            )
            return llm_pb2.GenerateResponse(
                text=text,
                model_used=model,
                input_tokens=len(prompt) // 4,
                output_tokens=len(text) // 4,
                finish_reason="stop",
                latency_ms=latency_ms,
            )
        except asyncio.CancelledError:
            _log("llm_generate_cancelled", trace_id=trace_id)
            raise
        except Exception as exc:
            grpc_status, http_status, code = _classify_runtime_error(exc)
            _log_error(
                "llm_generate_failed",
                trace_id=trace_id,
                error=str(exc),
                latency_budget_ms=latency_budget_ms,
                model=model,
                service_tier=service_tier,
                thinking_budget=thinking_budget,
                retry_profile=retry_profile,
                request_class=request_class,
                code=code,
            )
            raise LLMRuntimeError(
                grpc_status=grpc_status,
                http_status=http_status,
                code="INVALID_PROMPT"
                if code == "INVALID_PROMPT"
                else "GENERATE_TIMEOUT"
                if code == "TIMEOUT"
                else "GENERATE_FAILED",
                message=str(exc),
                trace_id=trace_id,
                details={
                    "model": model,
                    "latencyBudgetMs": latency_budget_ms,
                    "serviceTier": service_tier,
                    "thinkingBudget": thinking_budget,
                    "retryProfile": retry_profile,
                    "requestClass": request_class,
                    "code": code,
                },
            ) from exc

    async def generate_stream(
        self,
        request: Any,
        *,
        metadata: Mapping[str, str] | None = None,
        validate_credentials: bool = True,
    ) -> AsyncIterator[Any]:
        trace_id = trace_id_from_request(request, metadata)
        if validate_credentials:
            try:
                await validate_invocation_metadata(metadata)
            except PermissionError as exc:
                raise LLMRuntimeError(
                    grpc_status=grpc.StatusCode.PERMISSION_DENIED,
                    http_status=403,
                    code="UNAUTHORIZED",
                    message=str(exc),
                    trace_id=trace_id,
                ) from exc

        model = request.model or GEMINI_LIGHT_MODEL
        svc = self._gemini_factory(model=model)
        try:
            latency_budget_ms = _normalize_latency_budget_ms(
                _request_metadata_latency_budget_ms(request)
            )
            service_tier = (
                _request_string_field(request, "service_tier", "service_tier") or None
            )
            thinking_budget = _request_optional_int_field(
                request, "thinking_budget", "thinking_budget"
            )
            _log(
                "llm_stream_start",
                trace_id=trace_id,
                model=model,
                latency_budget_ms=latency_budget_ms,
                service_tier=service_tier,
                thinking_budget=thinking_budget,
            )
            prompt = _merged_prompt(request, trace_id)
            stream = await _invoke_compatible_method(
                svc.generate_stream,
                method_name="generate_stream",
                log_trace_id=trace_id,
                prompt=prompt,
                temperature=request.temperature or 0.7,
                max_tokens=request.max_tokens or 2048,
                service_tier=service_tier,
                thinking_budget=thinking_budget,
                latency_budget_ms=latency_budget_ms,
                trace_id=trace_id,
            )
            async for chunk_text in stream:
                yield llm_pb2.GenerateChunk(
                    delta=chunk_text, done=False, finish_reason=""
                )
            yield llm_pb2.GenerateChunk(delta="", done=True, finish_reason="stop")
            _log(
                "llm_stream_success",
                trace_id=trace_id,
                model=model,
                latency_budget_ms=latency_budget_ms,
                service_tier=service_tier,
                thinking_budget=thinking_budget,
            )
        except asyncio.CancelledError:
            _log("llm_stream_cancelled", trace_id=trace_id, model=model)
            raise
        except Exception as exc:
            _log_error(
                "llm_stream_failed",
                trace_id=trace_id,
                model=model,
                latency_budget_ms=locals().get("latency_budget_ms"),
                service_tier=locals().get("service_tier"),
                thinking_budget=locals().get("thinking_budget"),
                error=str(exc),
            )
            raise LLMRuntimeError(
                grpc_status=grpc.StatusCode.INVALID_ARGUMENT
                if isinstance(exc, ValueError)
                else grpc.StatusCode.INTERNAL,
                http_status=400 if isinstance(exc, ValueError) else 500,
                code="INVALID_PROMPT"
                if isinstance(exc, ValueError)
                else "STREAM_FAILED",
                message=str(exc),
                trace_id=trace_id,
            ) from exc

    async def structured_output(
        self,
        request: Any,
        *,
        metadata: Mapping[str, str] | None = None,
        validate_credentials: bool = True,
    ) -> Any:
        trace_id = trace_id_from_request(request, metadata)
        if validate_credentials:
            try:
                await validate_invocation_metadata(metadata)
            except PermissionError as exc:
                raise LLMRuntimeError(
                    grpc_status=grpc.StatusCode.PERMISSION_DENIED,
                    http_status=403,
                    code="UNAUTHORIZED",
                    message=str(exc),
                    trace_id=trace_id,
                ) from exc

        model = request.model or GEMINI_LIGHT_MODEL
        svc = self._gemini_factory(model=model)
        start_time = time.time()
        schema: dict[str, Any] = {}
        if request.json_schema:
            try:
                schema = json.loads(request.json_schema)
            except json.JSONDecodeError as exc:
                raise LLMRuntimeError(
                    grpc_status=grpc.StatusCode.INVALID_ARGUMENT,
                    http_status=400,
                    code="INVALID_JSON_SCHEMA",
                    message="json_schema must be valid JSON",
                    trace_id=trace_id,
                    details={"error": str(exc)},
                ) from exc
        if not schema:
            raise LLMRuntimeError(
                grpc_status=grpc.StatusCode.INVALID_ARGUMENT,
                http_status=400,
                code="MISSING_JSON_SCHEMA",
                message="json_schema is required for structured output",
                trace_id=trace_id,
            )

        try:
            service_tier = (
                _request_string_field(request, "service_tier", "service_tier") or None
            )
            thinking_budget = _request_optional_int_field(
                request, "thinking_budget", "thinking_budget"
            )
            retry_profile = (
                _request_string_field(request, "retry_profile", "retry_profile") or None
            )
            request_class = (
                _request_string_field(request, "request_class", "request_class") or None
            )
            latency_budget_ms = _normalize_latency_budget_ms(
                _request_metadata_latency_budget_ms(request)
            )
            # timeout_s from metadata is no longer forwarded — latency_budget_ms drives
            # per-attempt timeouts inside gemini_service.py via _retry_timeout_s().
            # We log it only for legacy debugging if a caller still sends it.
            legacy_timeout_s = _request_metadata_timeout_s(request)
            _log(
                "llm_structured_start",
                trace_id=trace_id,
                model=model,
                latency_budget_ms=latency_budget_ms,
                service_tier=service_tier,
                thinking_budget=thinking_budget,
                retry_profile=retry_profile,
                request_class=request_class,
                startup_age_ms=_gemini_service_uptime_ms(),
                cold_start_suspected=_gemini_is_cold_start_window(),
                legacy_timeout_s=legacy_timeout_s if legacy_timeout_s != 60.0 else None,
            )
            prompt = _merged_prompt(request, trace_id)
            json_result = await _invoke_compatible_method(
                svc.generate_structured,
                method_name="generate_structured",
                log_trace_id=trace_id,
                prompt=prompt,
                json_schema=schema,
                temperature=request.temperature or 0.3,
                max_tokens=request.max_tokens or 2048,
                latency_budget_ms=latency_budget_ms,
                service_tier=service_tier,
                thinking_budget=thinking_budget,
                retry_profile=retry_profile,
                request_class=request_class,
                trace_id=trace_id,
            )
            if not isinstance(json_result, str):
                json_result = json.dumps(json_result)
            try:
                json.loads(json_result)
            except json.JSONDecodeError as exc:
                raise RuntimeError(
                    "structured output returned invalid JSON"
                ) from exc
            latency_ms = int((time.time() - start_time) * 1000)
            _log(
                "llm_structured_success",
                trace_id=trace_id,
                latency_ms=latency_ms,
                model=model,
                latency_budget_ms=latency_budget_ms,
                service_tier=service_tier,
                thinking_budget=thinking_budget,
                retry_profile=retry_profile,
                request_class=request_class,
                result_bytes=len(json_result),
            )
            return llm_pb2.StructuredResponse(
                json_result=json_result,
                model_used=model,
                input_tokens=len(prompt) // 4,
                output_tokens=len(json_result) // 4,
                schema_valid=True,
                latency_ms=latency_ms,
            )
        except asyncio.CancelledError:
            _log(
                "llm_structured_cancelled",
                trace_id=trace_id,
                model=model,
                latency_budget_ms=latency_budget_ms,
                service_tier=service_tier,
                thinking_budget=thinking_budget,
                retry_profile=retry_profile,
                request_class=request_class,
            )
            raise
        except LLMRuntimeError:
            raise
        except Exception as exc:
            grpc_status, http_status, code = _classify_runtime_error(exc)
            _log_error(
                "llm_structured_failed",
                trace_id=trace_id,
                error=str(exc),
                model=model,
                latency_budget_ms=latency_budget_ms,
                service_tier=service_tier,
                thinking_budget=thinking_budget,
                retry_profile=retry_profile,
                request_class=request_class,
                code=code,
                startup_age_ms=_gemini_service_uptime_ms(),
                cold_start_suspected=_gemini_is_cold_start_window(),
            )
            response_code = (
                "INVALID_PROMPT"
                if code == "INVALID_PROMPT"
                else "STRUCTURED_TIMEOUT"
                if code == "TIMEOUT"
                else "STRUCTURED_OUTPUT_REQUIRES_NATIVE_RUNTIME"
                if code == "STRUCTURED_OUTPUT_REQUIRES_NATIVE_RUNTIME"
                else "STRUCTURED_FAILED"
            )
            raise LLMRuntimeError(
                grpc_status=grpc_status,
                http_status=http_status,
                code=response_code,
                message=str(exc),
                trace_id=trace_id,
                details={
                    "model": model,
                    "latencyBudgetMs": latency_budget_ms,
                    "thinkingBudget": thinking_budget,
                    "serviceTier": service_tier,
                    "retryProfile": retry_profile,
                    "requestClass": request_class,
                    "code": code,
                },
            ) from exc

    async def embed(
        self,
        request: Any,
        *,
        metadata: Mapping[str, str] | None = None,
        validate_credentials: bool = True,
    ) -> Any:
        trace_id = trace_id_from_request(request, metadata)
        if validate_credentials:
            try:
                await validate_invocation_metadata(metadata)
            except PermissionError as exc:
                raise LLMRuntimeError(
                    grpc_status=grpc.StatusCode.PERMISSION_DENIED,
                    http_status=403,
                    code="UNAUTHORIZED",
                    message=str(exc),
                    trace_id=trace_id,
                ) from exc

        start_time = time.time()
        try:
            latency_budget_ms = _request_metadata_latency_budget_ms(request)
            _log(
                "llm_embed_start",
                trace_id=trace_id,
                latency_budget_ms=latency_budget_ms,
            )
            model = _resolve_embedding_model(request.model or "text-embedding-005")
            vector = await _invoke_compatible_method(
                self._default_gemini.embed,
                method_name="embed",
                log_trace_id=trace_id,
                text=request.text,
                model=model,
                task_type=request.task_type or "RETRIEVAL_QUERY",
                latency_budget_ms=latency_budget_ms,
                trace_id=trace_id,
            )
            latency_ms = int((time.time() - start_time) * 1000)
            _log("llm_embed_success", trace_id=trace_id, latency_ms=latency_ms)
            return llm_pb2.EmbedResponse(
                embedding=vector,
                token_count=len(request.text) // 4,
                model_used=model,
                latency_ms=latency_ms,
            )
        except asyncio.CancelledError:
            _log("llm_embed_cancelled", trace_id=trace_id)
            raise
        except Exception as exc:
            grpc_status, http_status, code = _classify_runtime_error(exc)
            _log_error(
                "llm_embed_failed",
                trace_id=trace_id,
                error=str(exc),
                model=model,
                latency_budget_ms=latency_budget_ms,
                code=code,
            )
            raise LLMRuntimeError(
                grpc_status=grpc_status,
                http_status=http_status,
                code="INVALID_EMBED_REQUEST"
                if code == "INVALID_PROMPT"
                else "EMBED_TIMEOUT"
                if code == "TIMEOUT"
                else "EMBED_FAILED",
                message=str(exc),
                trace_id=trace_id,
                details={
                    "model": model,
                    "latencyBudgetMs": latency_budget_ms,
                    "code": code,
                },
            ) from exc

    async def embed_batch(
        self,
        request: Any,
        *,
        metadata: Mapping[str, str] | None = None,
        validate_credentials: bool = True,
    ) -> Any:
        trace_id = trace_id_from_request(request, metadata)
        if validate_credentials:
            try:
                await validate_invocation_metadata(metadata)
            except PermissionError as exc:
                raise LLMRuntimeError(
                    grpc_status=grpc.StatusCode.PERMISSION_DENIED,
                    http_status=403,
                    code="UNAUTHORIZED",
                    message=str(exc),
                    trace_id=trace_id,
                ) from exc

        start_time = time.time()
        try:
            latency_budget_ms = _request_metadata_latency_budget_ms(request)
            _log(
                "llm_embed_batch_start",
                trace_id=trace_id,
                count=len(request.texts),
                latency_budget_ms=latency_budget_ms,
            )
            model = _resolve_embedding_model(request.model or "text-embedding-005")
            vectors = await _invoke_compatible_method(
                self._default_gemini.embed_batch,
                method_name="embed_batch",
                log_trace_id=trace_id,
                texts=list(request.texts),
                model=model,
                task_type=request.task_type or "RETRIEVAL_DOCUMENT",
                latency_budget_ms=latency_budget_ms,
                trace_id=trace_id,
            )
            latency_ms = int((time.time() - start_time) * 1000)
            _log("llm_embed_batch_success", trace_id=trace_id, latency_ms=latency_ms)
            proto_vectors = [
                llm_pb2.EmbedVector(values=v, token_count=0) for v in vectors
            ]
            return llm_pb2.EmbedBatchResponse(
                embeddings=proto_vectors,
                model_used=model,
                latency_ms=latency_ms,
            )
        except asyncio.CancelledError:
            _log("llm_embed_batch_cancelled", trace_id=trace_id)
            raise
        except Exception as exc:
            grpc_status, http_status, code = _classify_runtime_error(exc)
            _log_error(
                "llm_embed_batch_failed",
                trace_id=trace_id,
                error=str(exc),
                model=model,
                count=len(request.texts),
                latency_budget_ms=latency_budget_ms,
                code=code,
            )
            raise LLMRuntimeError(
                grpc_status=grpc_status,
                http_status=http_status,
                code="INVALID_EMBED_BATCH_REQUEST"
                if code == "INVALID_PROMPT"
                else "EMBED_BATCH_TIMEOUT"
                if code == "TIMEOUT"
                else "EMBED_BATCH_FAILED",
                message=str(exc),
                trace_id=trace_id,
                details={
                    "model": model,
                    "count": len(request.texts),
                    "latencyBudgetMs": latency_budget_ms,
                    "code": code,
                },
            ) from exc

    async def health(self) -> Any:
        diagnostics = get_gemini_runtime_diagnostics()
        is_ready = bool(diagnostics.get("ready"))
        return llm_pb2.HealthResponse(
            ok=is_ready,
            version=SERVER_VERSION,
            models_available=[GEMINI_LIGHT_MODEL, GEMINI_HEAVY_MODEL],
            error=""
            if is_ready
            else str(diagnostics.get("detail") or "Gemini runtime unavailable"),
        )
