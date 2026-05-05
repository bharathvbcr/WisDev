from __future__ import annotations

import json
from types import SimpleNamespace
from unittest.mock import AsyncMock, MagicMock, patch

import grpc
import pytest
from fastapi import FastAPI
from fastapi.testclient import TestClient

from proto import llm_pb2
from routers import llm_router
from services.llm_runtime import LLMRuntime, LLMRuntimeError


def _make_app() -> FastAPI:
    app = FastAPI()
    app.include_router(llm_router.router)
    return app


def test_request_metadata_merges_trace_authorization_and_internal_key():
    request = SimpleNamespace(
        state=SimpleNamespace(trace_id="req-trace"),
        headers={
            "Authorization": "Bearer token",
            "X-Internal-Service-Key": "internal-key",
        },
    )

    metadata = llm_router._request_metadata(request, {"trace_id": "payload-trace", "other": "value"})

    assert metadata["trace_id"] == "payload-trace"
    assert metadata["authorization"] == "Bearer token"
    assert metadata["internal_service_key"] == "internal-key"
    assert metadata["other"] == "value"


def test_embed_proto_threads_latency_budget_into_metadata():
    request = SimpleNamespace(state=SimpleNamespace(trace_id="req-trace"), headers={})
    payload = llm_router.EmbedRequestModel(
        text="hello",
        taskType="RETRIEVAL_QUERY",
        latencyBudgetMs=12000,
        metadata={"existing": "value"},
    )

    proto = llm_router._embed_proto(payload, request)

    assert proto.text == "hello"
    assert proto.task_type == "RETRIEVAL_QUERY"
    assert proto.latency_budget_ms == 12000
    assert proto.metadata["trace_id"] == "req-trace"
    assert proto.metadata["existing"] == "value"
    assert "latency_budget_ms" not in proto.metadata


def test_embed_batch_proto_threads_latency_budget_into_metadata():
    request = SimpleNamespace(state=SimpleNamespace(trace_id="req-trace"), headers={})
    payload = llm_router.EmbedBatchRequestModel(
        texts=["a", "b"],
        taskType="RETRIEVAL_DOCUMENT",
        latencyBudgetMs=9000,
        metadata={"existing": "value"},
    )

    proto = llm_router._embed_batch_proto(payload, request)

    assert list(proto.texts) == ["a", "b"]
    assert proto.task_type == "RETRIEVAL_DOCUMENT"
    assert proto.latency_budget_ms == 9000
    assert proto.metadata["trace_id"] == "req-trace"
    assert proto.metadata["existing"] == "value"
    assert "latency_budget_ms" not in proto.metadata


@pytest.mark.asyncio
async def test_stream_helpers_emit_error_events():
    first_chunk = llm_pb2.GenerateChunk(delta="alpha", done=False, finish_reason="")

    async def broken_stream():
        yield llm_pb2.GenerateChunk(delta="beta", done=False, finish_reason="")
        raise LLMRuntimeError(
            grpc_status=grpc.StatusCode.INTERNAL,
            http_status=500,
            code="BROKEN",
            message="boom",
        )

    events = []
    async for item in llm_router._stream_events(broken_stream(), first_chunk):
        events.append(json.loads(item.decode("utf-8")))

    assert events[0]["chunk"]["delta"] == "alpha"
    assert events[1]["chunk"]["delta"] == "beta"
    assert events[2]["error"]["code"] == "BROKEN"
    assert events[2]["error"]["message"] == "boom"


def test_generate_stream_endpoint_returns_empty_response_for_empty_stream():
    class EmptyStream:
        def __aiter__(self):
            return self

        async def __anext__(self):
            raise StopAsyncIteration

    with patch.object(llm_router._RUNTIME, "generate_stream", MagicMock(return_value=EmptyStream())):
        client = TestClient(_make_app())
        response = client.post("/llm/generate/stream", json={"prompt": "hello"})

    assert response.status_code == 200
    assert response.text == ""


def test_generate_stream_endpoint_translates_runtime_error():
    class ErrorStream:
        def __aiter__(self):
            return self

        async def __anext__(self):
            raise LLMRuntimeError(
                grpc_status=grpc.StatusCode.PERMISSION_DENIED,
                http_status=403,
                code="UNAUTHORIZED",
                message="missing credentials",
            )

    with patch.object(llm_router._RUNTIME, "generate_stream", MagicMock(return_value=ErrorStream())):
        client = TestClient(_make_app())
        response = client.post("/llm/generate/stream", json={"prompt": "hello"})

    assert response.status_code == 403
    assert response.json()["error"]["code"] == "UNAUTHORIZED"


@pytest.mark.parametrize(
    ("route", "method_name", "response", "expected_key", "payload"),
    [
        (
            "/llm/structured-output",
            "structured_output",
            llm_pb2.StructuredResponse(
                json_result='{"value":"ok"}',
                model_used="gemini-http",
                input_tokens=3,
                output_tokens=4,
                schema_valid=True,
                latency_ms=7,
            ),
            "jsonResult",
            {"prompt": "hello", "jsonSchema": "{\"type\":\"object\"}"},
        ),
        (
            "/llm/embed",
            "embed",
            llm_pb2.EmbedResponse(
                embedding=[0.1, 0.2],
                token_count=5,
                model_used="gemini-embed",
                latency_ms=11,
            ),
            "embedding",
            {"text": "hello"},
        ),
        (
            "/llm/embed/batch",
            "embed_batch",
            llm_pb2.EmbedBatchResponse(
                embeddings=[llm_pb2.EmbedVector(values=[0.1, 0.2], token_count=5)],
                model_used="gemini-embed",
                latency_ms=13,
            ),
            "embeddings",
            {"texts": ["a", "b"]},
        ),
    ],
)
def test_endpoint_payload_converters(route, method_name, response, expected_key, payload):
    with patch.object(llm_router._RUNTIME, method_name, AsyncMock(return_value=response)):
        client = TestClient(_make_app())
        response_obj = client.post(route, json=payload)

    assert response_obj.status_code == 200
    body = response_obj.json()
    assert expected_key in body


@pytest.mark.parametrize(
    ("route", "method_name", "payload"),
    [
        (
            "/llm/structured-output",
            "structured_output",
            {"prompt": "hello", "jsonSchema": "{\"type\":\"object\"}"},
        ),
        ("/llm/embed", "embed", {"text": "hello"}),
        ("/llm/embed/batch", "embed_batch", {"texts": ["a", "b"]}),
    ],
)
def test_endpoint_errors_translate_runtime_error(route, method_name, payload):
    with patch.object(
        llm_router._RUNTIME,
        method_name,
        AsyncMock(
            side_effect=LLMRuntimeError(
                grpc_status=grpc.StatusCode.INTERNAL,
                http_status=500,
                code="BROKEN",
                message="boom",
            )
        ),
    ):
        client = TestClient(_make_app())
        response = client.post(route, json=payload)

    assert response.status_code == 500
    assert response.json()["error"]["code"] == "BROKEN"


def test_structured_output_endpoint_exercises_runtime_compatibility_filter():
    class LegacyStructuredGemini:
        calls: list[dict[str, object]] = []

        def __init__(self, model: str = ""):
            self.model = model

        async def generate_structured(
            self,
            prompt: str,
            json_schema: dict[str, object],
            temperature: float = 0.3,
            max_tokens: int = 2048,
        ) -> str:
            self.__class__.calls.append(
                {
                    "model": self.model,
                    "prompt": prompt,
                    "json_schema": json_schema,
                    "temperature": temperature,
                    "max_tokens": max_tokens,
                }
            )
            return '{"value":"ok"}'

    runtime = LLMRuntime(gemini_factory=LegacyStructuredGemini)

    with patch.object(llm_router, "_RUNTIME", runtime):
        with patch("services.llm_runtime.validate_invocation_metadata", AsyncMock()):
            with patch("services.llm_runtime._log") as log_mock:
                client = TestClient(_make_app())
                response = client.post(
                    "/llm/structured-output",
                    json={
                        "prompt": "hello",
                        "jsonSchema": "{\"type\":\"object\"}",
                        "temperature": 0.1,
                        "maxTokens": 32,
                        "serviceTier": "standard",
                        "thinkingBudget": 64,
                        "retryProfile": "standard",
                        "requestClass": "structured_high_value",
                        "latencyBudgetMs": 30000,
                        "metadata": {"trace_id": "trace-structured-e2e"},
                    },
                )

    assert response.status_code == 200
    assert response.json()["jsonResult"] == '{"value":"ok"}'
    assert len(LegacyStructuredGemini.calls) == 1
    structured_call = LegacyStructuredGemini.calls[0]
    assert structured_call["model"] == "gemini-2.5-flash-lite"
    assert structured_call["prompt"] == "hello"
    assert structured_call["json_schema"] == {"type": "object"}
    assert structured_call["temperature"] == pytest.approx(0.1)
    assert structured_call["max_tokens"] == 32

    filtered_call = next(
        call
        for call in log_mock.call_args_list
        if call.args and call.args[0] == "llm_runtime_kwargs_filtered"
    )
    assert filtered_call.kwargs["method"] == "generate_structured"
    assert set(filtered_call.kwargs["dropped_fields"]) == {
        "latency_budget_ms",
        "request_class",
        "retry_profile",
        "service_tier",
        "thinking_budget",
        "trace_id",
    }


def test_embed_endpoint_exercises_runtime_compatibility_filter():
    class LegacyEmbedGemini:
        calls: list[dict[str, object]] = []

        def __init__(self, model: str = ""):
            self.model = model

        async def embed(
            self,
            text: str,
            model: str = "",
            task_type: str = "RETRIEVAL_QUERY",
        ) -> list[float]:
            self.__class__.calls.append(
                {
                    "model": model,
                    "text": text,
                    "task_type": task_type,
                }
            )
            return [0.1, 0.2]

    runtime = LLMRuntime(gemini_factory=LegacyEmbedGemini)

    with patch.object(llm_router, "_RUNTIME", runtime):
        with patch("services.llm_runtime.validate_invocation_metadata", AsyncMock()):
            with patch("services.llm_runtime._log") as log_mock:
                client = TestClient(_make_app())
                response = client.post(
                    "/llm/embed",
                    json={
                        "text": "hello",
                        "taskType": "RETRIEVAL_QUERY",
                        "latencyBudgetMs": 12000,
                        "metadata": {"trace_id": "trace-embed-e2e"},
                    },
                )

    assert response.status_code == 200
    assert response.json()["embedding"] == pytest.approx([0.1, 0.2])
    assert LegacyEmbedGemini.calls == [
        {
            "model": "text-embedding-005",
            "text": "hello",
            "task_type": "RETRIEVAL_QUERY",
        }
    ]

    filtered_call = next(
        call
        for call in log_mock.call_args_list
        if call.args and call.args[0] == "llm_runtime_kwargs_filtered"
    )
    assert filtered_call.kwargs["method"] == "embed"
    assert set(filtered_call.kwargs["dropped_fields"]) == {
        "latency_budget_ms",
        "trace_id",
    }
