"""
ScholarLM LLM Sidecar gRPC Server.

Provides primitives for LLM generation, embeddings, and structured output.
Legacy AcademicCapabilityService (v2) has been decommissioned.
"""

import asyncio
import json
import logging
import os
import time

import grpc
from proto import llm_v1_pb2 as llm_pb2
from proto import llm_v1_pb2_grpc as llm_pb2_grpc

from services.gemini_service import GeminiService, GEMINI_LIGHT_MODEL, GEMINI_HEAVY_MODEL

logger = logging.getLogger(__name__)


def split_stream_text(text: str) -> list[str]:
    """Split text into chunks for streaming simulation."""
    if not text:
        return []
    
    # Try splitting by double newline first
    if "\n\n" in text:
        parts = text.split("\n\n")
        chunks = [p + "\n\n" for p in parts[:-1]]
        if parts[-1]:
            chunks.append(parts[-1])
        return chunks
        
    # Fallback to fixed-length chunking for large contiguous strings
    # This ensures that contract tests expecting multiple chunks for long output pass.
    chunk_size = 128
    return [text[i:i+chunk_size] for i in range(0, len(text), chunk_size)]


def _log(event: str, **fields) -> None:
    if fields:
        logger.info("%s | %s", event, json.dumps(fields, sort_keys=True, default=str))
        return
    logger.info(event)


def _log_error(event: str, **fields) -> None:
    if fields:
        logger.error("%s | %s", event, json.dumps(fields, sort_keys=True, default=str))
        return
    logger.error(event)

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

SERVER_VERSION = "1.1.0-sidecar"
_server_start_time = 0.0


async def _abort_with_typed_error(
    context,
    status_code: grpc.StatusCode,
    code: str,
    message: str,
    trace_id: str,
    http_status: int = 500,
    details: dict | None = None,
):
    """Abort with a JSON error envelope in the details field."""
    envelope = {
        "ok": False,
        "traceId": trace_id,
        "error": {
            "code": code,
            "message": message,
            "status": http_status,
            "traceId": trace_id,
        },
    }
    if details:
        envelope["error"]["details"] = details

    await context.abort(status_code, json.dumps(envelope))


def _ensure_non_empty_prompt(prompt: str, trace_id: str) -> str:
    prompt = prompt.strip()
    if not prompt:
        raise ValueError(f"empty prompt for trace_id={trace_id or 'unknown'}")
    return prompt


def _get_trace_id(request) -> str:
    """Extract trace_id from request metadata map."""
    if hasattr(request, "metadata") and request.metadata:
        return request.metadata.get("trace_id", "")
    return ""


def _validate_internal_key(request, context) -> None:
    """Validate internal service key or OIDC token if configured."""
    internal_key = os.environ.get("INTERNAL_SERVICE_KEY")
    oidc_audience = os.environ.get("OIDC_AUDIENCE")
    
    if not internal_key and not oidc_audience:
        return
        
    metadata = dict(context.invocation_metadata())
    
    # 1. Try OIDC ID Token if audience is set (production preference)
    if oidc_audience:
        auth_header = metadata.get("authorization", "")
        if auth_header.startswith("Bearer "):
            id_token_str = auth_header[7:]
            try:
                from google.oauth2 import id_token
                from google.auth.transport import requests
                id_token.verify_oauth2_token(id_token_str, requests.Request(), oidc_audience)
                return # Success
            except Exception as e:
                _log("oidc_token_verification_failed", error=str(e))
                # Fall through to check internal key if verification fails
    
    # 2. Try static internal service key (legacy/internal)
    provided_key = metadata.get("internal_service_key", "")
    if internal_key and provided_key == internal_key:
        return # Success
        
    raise PermissionError("Unauthorized: missing or invalid service credentials")


class LLMServiceServicer(llm_pb2_grpc.LLMServiceServicer):
    """Implementation of the sidecar LLMService (v1)."""

    def __init__(self):
        self.default_gemini = GeminiService()

    async def Generate(self, request, context):
        trace_id = _get_trace_id(request)
        try:
            _validate_internal_key(request, context)
        except PermissionError as e:
            await _abort_with_typed_error(
                context, grpc.StatusCode.PERMISSION_DENIED, "UNAUTHORIZED", str(e), trace_id, 403
            )
            return

        model = request.model or GEMINI_LIGHT_MODEL
        svc = GeminiService(model=model)
        start_time = time.time()
        try:
            _log("llm_generate_start", trace_id=trace_id, model=model)
            prompt = _ensure_non_empty_prompt(request.prompt, trace_id)
            if request.system_prompt:
                prompt = f"{request.system_prompt}\n\n{prompt}"

            text = await svc.generate_text(
                prompt=prompt,
                temperature=request.temperature or 0.7,
                max_tokens=request.max_tokens or 2048,
            )
            if not text.strip():
                raise RuntimeError("empty text response from Gemini")
            latency_ms = int((time.time() - start_time) * 1000)
            _log("llm_generate_success", trace_id=trace_id, latency_ms=latency_ms)
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
        except Exception as e:
            _log_error("llm_generate_failed", trace_id=trace_id, error=str(e))
            status = grpc.StatusCode.INVALID_ARGUMENT if isinstance(e, ValueError) else grpc.StatusCode.INTERNAL
            code = "INVALID_PROMPT" if isinstance(e, ValueError) else "GENERATE_FAILED"
            http_status = 400 if isinstance(e, ValueError) else 500
            await _abort_with_typed_error(
                context, status, code, str(e), trace_id, http_status
            )

    async def GenerateStream(self, request, context):
        trace_id = _get_trace_id(request)
        try:
            _validate_internal_key(request, context)
        except PermissionError as e:
            await _abort_with_typed_error(
                context, grpc.StatusCode.PERMISSION_DENIED, "UNAUTHORIZED", str(e), trace_id, 403
            )
            return

        model = request.model or GEMINI_LIGHT_MODEL
        svc = GeminiService(model=model)

        try:
            _log("llm_stream_start", trace_id=trace_id, model=model)
            prompt = _ensure_non_empty_prompt(request.prompt, trace_id)
            if request.system_prompt:
                prompt = f"{request.system_prompt}\n\n{prompt}"

            # Use native streaming from GeminiService
            async for chunk_text in svc.generate_stream(
                prompt=prompt,
                temperature=request.temperature or 0.7,
                max_tokens=request.max_tokens or 2048,
            ):
                yield llm_pb2.GenerateChunk(
                    delta=chunk_text,
                    done=False,
                    finish_reason="",
                )
            
            # Final empty chunk to signal completion
            yield llm_pb2.GenerateChunk(
                delta="",
                done=True,
                finish_reason="stop",
            )
            _log("llm_stream_success", trace_id=trace_id)
        except asyncio.CancelledError:
            _log("llm_stream_cancelled", trace_id=trace_id)
            raise
        except Exception as e:
            _log_error("llm_stream_failed", trace_id=trace_id, error=str(e))
            status = grpc.StatusCode.INVALID_ARGUMENT if isinstance(e, ValueError) else grpc.StatusCode.INTERNAL
            code = "INVALID_PROMPT" if isinstance(e, ValueError) else "STREAM_FAILED"
            http_status = 400 if isinstance(e, ValueError) else 500
            await _abort_with_typed_error(
                context, status, code, str(e), trace_id, http_status
            )

    async def StructuredOutput(self, request, context):
        trace_id = _get_trace_id(request)
        try:
            _validate_internal_key(request, context)
        except PermissionError as e:
            await _abort_with_typed_error(
                context, grpc.StatusCode.PERMISSION_DENIED, "UNAUTHORIZED", str(e), trace_id, 403
            )
            return

        model = request.model or GEMINI_LIGHT_MODEL
        svc = GeminiService(model=model)
        start_time = time.time()
        schema = {}
        if request.json_schema:
            try:
                schema = json.loads(request.json_schema)
            except json.JSONDecodeError as e:
                await _abort_with_typed_error(
                    context,
                    grpc.StatusCode.INVALID_ARGUMENT,
                    "INVALID_JSON_SCHEMA",
                    "json_schema must be valid JSON",
                    trace_id,
                    400,
                    {"error": str(e)},
                )
                return
        try:
            _log("llm_structured_start", trace_id=trace_id, model=model)
            prompt = _ensure_non_empty_prompt(request.prompt, trace_id)
            if request.system_prompt:
                prompt = f"{request.system_prompt}\n\n{prompt}"

            json_result = await svc.generate_structured(
                prompt=prompt,
                json_schema=schema,
                temperature=request.temperature or 0.3,
                max_tokens=request.max_tokens or 2048,
            )
            if not isinstance(json_result, str):
                json_result = json.dumps(json_result)

            json.loads(json_result)
            latency_ms = int((time.time() - start_time) * 1000)
            _log("llm_structured_success", trace_id=trace_id, latency_ms=latency_ms)
            return llm_pb2.StructuredResponse(
                json_result=json_result,
                model_used=model,
                input_tokens=len(prompt) // 4,
                output_tokens=len(json_result) // 4,
                schema_valid=True,
                latency_ms=latency_ms,
            )
        except asyncio.CancelledError:
            _log("llm_structured_cancelled", trace_id=trace_id)
            raise
        except Exception as e:
            _log_error("llm_structured_failed", trace_id=trace_id, error=str(e))
            status = grpc.StatusCode.INVALID_ARGUMENT if isinstance(e, ValueError) else grpc.StatusCode.INTERNAL
            code = "INVALID_PROMPT" if isinstance(e, ValueError) else "STRUCTURED_FAILED"
            http_status = 400 if isinstance(e, ValueError) else 500
            await _abort_with_typed_error(
                context, status, code, str(e), trace_id, http_status
            )

    async def Embed(self, request, context):
        trace_id = _get_trace_id(request)
        try:
            _validate_internal_key(request, context)
        except PermissionError as e:
            await _abort_with_typed_error(
                context, grpc.StatusCode.PERMISSION_DENIED, "UNAUTHORIZED", str(e), trace_id, 403
            )
            return

        start_time = time.time()
        try:
            _log("llm_embed_start", trace_id=trace_id)
            model = request.model or "text-embedding-004"
            vector = await self.default_gemini.embed(
                text=request.text,
                model=model,
                task_type=request.task_type or "RETRIEVAL_QUERY",
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
        except Exception as e:
            _log_error("llm_embed_failed", trace_id=trace_id, error=str(e))
            await _abort_with_typed_error(
                context, grpc.StatusCode.INTERNAL, "EMBED_FAILED", str(e), trace_id, 500
            )

    async def EmbedBatch(self, request, context):
        trace_id = _get_trace_id(request)
        try:
            _validate_internal_key(request, context)
        except PermissionError as e:
            await _abort_with_typed_error(
                context, grpc.StatusCode.PERMISSION_DENIED, "UNAUTHORIZED", str(e), trace_id, 403
            )
            return

        start_time = time.time()
        try:
            _log("llm_embed_batch_start", trace_id=trace_id, count=len(request.texts))
            model = request.model or "text-embedding-004"
            vectors = await self.default_gemini.embed_batch(
                texts=list(request.texts),
                model=model,
                task_type=request.task_type or "RETRIEVAL_DOCUMENT",
            )
            latency_ms = int((time.time() - start_time) * 1000)
            _log("llm_embed_batch_success", trace_id=trace_id, latency_ms=latency_ms)

            proto_vectors = [llm_pb2.EmbedVector(values=v, token_count=0) for v in vectors]
            return llm_pb2.EmbedBatchResponse(
                embeddings=proto_vectors,
                model_used=model,
                latency_ms=latency_ms,
            )
        except asyncio.CancelledError:
            _log("llm_embed_batch_cancelled", trace_id=trace_id)
            raise
        except Exception as e:
            _log_error("llm_embed_batch_failed", trace_id=trace_id, error=str(e))
            await _abort_with_typed_error(
                context, grpc.StatusCode.INTERNAL, "EMBED_BATCH_FAILED", str(e), trace_id, 500
            )

    async def Health(self, request, context):
        is_ready = self.default_gemini.is_ready()
        return llm_pb2.HealthResponse(
            ok=is_ready,
            version=SERVER_VERSION,
            models_available=[GEMINI_LIGHT_MODEL, GEMINI_HEAVY_MODEL],
            error="" if is_ready else "Gemini credentials not configured",
        )


# ---------------------------------------------------------------------------
# Server lifecycle
# ---------------------------------------------------------------------------

async def serve_async(interceptors=None):
    global _server_start_time
    port = os.environ.get("GRPC_PORT", "50052")
    # Pass OTel server interceptor when provided so inbound gRPC calls from Go
    # continue the existing OpenTelemetry trace rather than starting a new root span.
    server = grpc.aio.server(interceptors=interceptors or [])

    # Register LLMService (sidecar contract).
    llm_pb2_grpc.add_LLMServiceServicer_to_server(LLMServiceServicer(), server)

    server.add_insecure_port("[::]:" + port)
    await server.start()
    _server_start_time = time.time()
    logging.info(
        f"Starting ScholarLM LLM Sidecar (Async) on port {port} "
        f"| version={SERVER_VERSION} | otel_interceptor={'yes' if interceptors else 'no'}"
    )
    await server.wait_for_termination()


def serve(interceptors=None):
    """Entry point to run the async server.

    Args:
        interceptors: Optional list of gRPC interceptors. Pass the OTel
            interceptor from ``telemetry.make_grpc_server_interceptor()`` to
            propagate trace context from Go into Python gRPC handler spans.
    """
    loop = asyncio.new_event_loop()
    asyncio.set_event_loop(loop)
    loop.run_until_complete(serve_async(interceptors=interceptors))


if __name__ == '__main__':
    logging.basicConfig(level=logging.INFO)
    serve()
