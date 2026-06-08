from __future__ import annotations

import json
from typing import Any, AsyncIterator

from fastapi import APIRouter, Request
from fastapi.responses import JSONResponse, StreamingResponse
from pydantic import BaseModel, ConfigDict, Field

from proto import llm_pb2
from services.llm_runtime import LLMRuntime, LLMRuntimeError, error_envelope

router = APIRouter(prefix="/llm", tags=["llm"])
_RUNTIME = LLMRuntime()


class GenerateRequestModel(BaseModel):
    model_config = ConfigDict(populate_by_name=True)

    prompt: str
    system_prompt: str = Field("", alias="systemPrompt")
    model: str = ""
    temperature: float = 0.0
    max_tokens: int = Field(0, alias="maxTokens")
    stop_sequences: list[str] = Field(default_factory=list, alias="stopSequences")
    thinking_budget: int | None = Field(None, alias="thinkingBudget")
    service_tier: str = Field("", alias="serviceTier")
    retry_profile: str = Field("", alias="retryProfile")
    request_class: str = Field("", alias="requestClass")
    latency_budget_ms: int = Field(0, alias="latencyBudgetMs")
    metadata: dict[str, str] = Field(default_factory=dict)


class StructuredRequestModel(BaseModel):
    model_config = ConfigDict(populate_by_name=True)

    prompt: str
    system_prompt: str = Field("", alias="systemPrompt")
    json_schema: str = Field("", alias="jsonSchema")
    model: str = ""
    temperature: float = 0.0
    max_tokens: int = Field(0, alias="maxTokens")
    thinking_budget: int | None = Field(None, alias="thinkingBudget")
    service_tier: str = Field("", alias="serviceTier")
    retry_profile: str = Field("", alias="retryProfile")
    request_class: str = Field("", alias="requestClass")
    latency_budget_ms: int = Field(0, alias="latencyBudgetMs")
    metadata: dict[str, str] = Field(default_factory=dict)


class EmbedRequestModel(BaseModel):
    model_config = ConfigDict(populate_by_name=True)

    text: str
    model: str = ""
    task_type: str = Field("", alias="taskType")
    latency_budget_ms: int = Field(0, alias="latencyBudgetMs")
    metadata: dict[str, str] = Field(default_factory=dict)


class EmbedBatchRequestModel(BaseModel):
    model_config = ConfigDict(populate_by_name=True)

    texts: list[str] = Field(default_factory=list)
    model: str = ""
    task_type: str = Field("", alias="taskType")
    latency_budget_ms: int = Field(0, alias="latencyBudgetMs")
    metadata: dict[str, str] = Field(default_factory=dict)


def _effective_metadata(request: Request, metadata: dict[str, str]) -> dict[str, str]:
    merged = dict(metadata or {})
    trace_id = merged.get("trace_id") or getattr(request.state, "trace_id", "")
    if trace_id:
        merged["trace_id"] = trace_id
    return merged


def _request_metadata(request: Request, metadata: dict[str, str]) -> dict[str, str]:
    merged = _effective_metadata(request, metadata)
    authorization = request.headers.get("Authorization", "").strip()
    if authorization:
        merged["authorization"] = authorization
    internal_key = request.headers.get("X-Internal-Service-Key", "").strip()
    if internal_key:
        merged["internal_service_key"] = internal_key
    return merged


def _generate_proto(req: GenerateRequestModel, request: Request) -> llm_pb2.GenerateRequest:
    payload: dict[str, Any] = {
        "prompt": req.prompt,
        "system_prompt": req.system_prompt,
        "model": req.model,
        "temperature": req.temperature,
        "max_tokens": req.max_tokens,
        "stop_sequences": req.stop_sequences,
        "service_tier": req.service_tier,
        "retry_profile": req.retry_profile,
        "request_class": req.request_class,
        "latency_budget_ms": req.latency_budget_ms,
        "metadata": _effective_metadata(request, req.metadata),
    }
    if req.thinking_budget is not None:
        payload["thinking_budget"] = req.thinking_budget
    return llm_pb2.GenerateRequest(
        **payload,
    )


def _structured_proto(req: StructuredRequestModel, request: Request) -> llm_pb2.StructuredRequest:
    payload: dict[str, Any] = {
        "prompt": req.prompt,
        "system_prompt": req.system_prompt,
        "json_schema": req.json_schema,
        "model": req.model,
        "temperature": req.temperature,
        "max_tokens": req.max_tokens,
        "service_tier": req.service_tier,
        "retry_profile": req.retry_profile,
        "request_class": req.request_class,
        "latency_budget_ms": req.latency_budget_ms,
        "metadata": _effective_metadata(request, req.metadata),
    }
    if req.thinking_budget is not None:
        payload["thinking_budget"] = req.thinking_budget
    return llm_pb2.StructuredRequest(
        **payload,
    )


def _embed_proto(req: EmbedRequestModel, request: Request) -> llm_pb2.EmbedRequest:
    return llm_pb2.EmbedRequest(
        text=req.text,
        model=req.model,
        task_type=req.task_type,
        latency_budget_ms=req.latency_budget_ms,
        metadata=_effective_metadata(request, req.metadata),
    )


def _embed_batch_proto(req: EmbedBatchRequestModel, request: Request) -> llm_pb2.EmbedBatchRequest:
    return llm_pb2.EmbedBatchRequest(
        texts=req.texts,
        model=req.model,
        task_type=req.task_type,
        latency_budget_ms=req.latency_budget_ms,
        metadata=_effective_metadata(request, req.metadata),
    )


def _generate_response_payload(response: llm_pb2.GenerateResponse) -> dict[str, Any]:
    return {
        "text": response.text,
        "modelUsed": response.model_used,
        "inputTokens": response.input_tokens,
        "outputTokens": response.output_tokens,
        "finishReason": response.finish_reason,
        "latencyMs": response.latency_ms,
    }


def _structured_response_payload(response: llm_pb2.StructuredResponse) -> dict[str, Any]:
    return {
        "jsonResult": response.json_result,
        "modelUsed": response.model_used,
        "inputTokens": response.input_tokens,
        "outputTokens": response.output_tokens,
        "schemaValid": response.schema_valid,
        "error": response.error,
        "latencyMs": response.latency_ms,
    }


def _embed_response_payload(response: llm_pb2.EmbedResponse) -> dict[str, Any]:
    return {
        "embedding": list(response.embedding),
        "tokenCount": response.token_count,
        "modelUsed": response.model_used,
        "latencyMs": response.latency_ms,
    }


def _embed_batch_response_payload(response: llm_pb2.EmbedBatchResponse) -> dict[str, Any]:
    return {
        "embeddings": [
            {
                "values": list(vector.values),
                "tokenCount": vector.token_count,
            }
            for vector in response.embeddings
        ],
        "modelUsed": response.model_used,
        "latencyMs": response.latency_ms,
    }


def _chunk_event(chunk: llm_pb2.GenerateChunk) -> bytes:
    payload = {
        "chunk": {
            "delta": chunk.delta,
            "done": chunk.done,
            "finishReason": chunk.finish_reason,
        }
    }
    return (json.dumps(payload) + "\n").encode("utf-8")


def _error_event(payload: dict[str, Any]) -> bytes:
    return (json.dumps({"error": payload.get("error", payload)}) + "\n").encode("utf-8")


async def _stream_events(
    stream: AsyncIterator[llm_pb2.GenerateChunk],
    first_chunk: llm_pb2.GenerateChunk,
) -> AsyncIterator[bytes]:
    yield _chunk_event(first_chunk)
    try:
        async for chunk in stream:
            yield _chunk_event(chunk)
    except LLMRuntimeError as exc:
        yield _error_event(error_envelope(exc))


@router.post("/generate")
async def generate(request: Request, payload: GenerateRequestModel):
    metadata = _request_metadata(request, payload.metadata)
    try:
        response = await _RUNTIME.generate(_generate_proto(payload, request), metadata=metadata)
    except LLMRuntimeError as exc:
        return JSONResponse(status_code=exc.http_status, content=error_envelope(exc))
    return _generate_response_payload(response)


@router.post("/generate/stream")
async def generate_stream(request: Request, payload: GenerateRequestModel):
    metadata = _request_metadata(request, payload.metadata)
    stream = _RUNTIME.generate_stream(_generate_proto(payload, request), metadata=metadata)
    try:
        first_chunk = await stream.__anext__()
    except StopAsyncIteration:
        return StreamingResponse(iter(()), media_type="application/x-ndjson")
    except LLMRuntimeError as exc:
        return JSONResponse(status_code=exc.http_status, content=error_envelope(exc))

    return StreamingResponse(
        _stream_events(stream, first_chunk),
        media_type="application/x-ndjson",
    )


@router.post("/structured-output")
async def structured_output(request: Request, payload: StructuredRequestModel):
    metadata = _request_metadata(request, payload.metadata)
    try:
        response = await _RUNTIME.structured_output(_structured_proto(payload, request), metadata=metadata)
    except LLMRuntimeError as exc:
        return JSONResponse(status_code=exc.http_status, content=error_envelope(exc))
    return _structured_response_payload(response)


@router.post("/embed")
async def embed(request: Request, payload: EmbedRequestModel):
    metadata = _request_metadata(request, payload.metadata)
    try:
        response = await _RUNTIME.embed(_embed_proto(payload, request), metadata=metadata)
    except LLMRuntimeError as exc:
        return JSONResponse(status_code=exc.http_status, content=error_envelope(exc))
    return _embed_response_payload(response)


@router.post("/embed/batch")
async def embed_batch(request: Request, payload: EmbedBatchRequestModel):
    metadata = _request_metadata(request, payload.metadata)
    try:
        response = await _RUNTIME.embed_batch(_embed_batch_proto(payload, request), metadata=metadata)
    except LLMRuntimeError as exc:
        return JSONResponse(status_code=exc.http_status, content=error_envelope(exc))
    return _embed_batch_response_payload(response)


@router.get("/health")
async def health():
    response = await _RUNTIME.health()
    return {
        "ok": response.ok,
        "version": response.version,
        "modelsAvailable": list(response.models_available),
        "error": response.error,
    }
