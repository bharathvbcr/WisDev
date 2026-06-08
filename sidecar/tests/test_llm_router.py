from __future__ import annotations

import json
from unittest.mock import AsyncMock, patch

import grpc
from fastapi import FastAPI
from fastapi.testclient import TestClient

from proto import llm_pb2
from routers import llm_router
from services.llm_runtime import LLMRuntimeError


def make_app():
    app = FastAPI()
    app.include_router(llm_router.router)
    return app


def test_generate_endpoint_returns_http_payload():
    with patch.object(
        llm_router._RUNTIME,
        "generate",
        new=AsyncMock(return_value=llm_pb2.GenerateResponse(
            text="generated text",
            model_used="gemini-http",
            input_tokens=5,
            output_tokens=7,
            finish_reason="stop",
            latency_ms=19,
        )),
    ):
        client = TestClient(make_app())
        response = client.post("/llm/generate", json={"prompt": "hello"})

    assert response.status_code == 200
    assert response.json() == {
        "text": "generated text",
        "modelUsed": "gemini-http",
        "inputTokens": 5,
        "outputTokens": 7,
        "finishReason": "stop",
        "latencyMs": 19,
    }


def test_generate_endpoint_translates_bridge_abort():
    with patch.object(
        llm_router._RUNTIME,
        "generate",
        new=AsyncMock(
            side_effect=LLMRuntimeError(
                grpc_status=grpc.StatusCode.PERMISSION_DENIED,
                http_status=403,
                code="UNAUTHORIZED",
                message="missing credentials",
            )
        ),
    ):
        client = TestClient(make_app())
        response = client.post("/llm/generate", json={"prompt": "hello"})

    assert response.status_code == 403
    assert response.json()["error"]["code"] == "UNAUTHORIZED"


def test_generate_stream_endpoint_emits_ndjson_chunks():
    async def fake_stream(*args, **kwargs):
        yield llm_pb2.GenerateChunk(delta="alpha", done=False)
        yield llm_pb2.GenerateChunk(delta="", done=True, finish_reason="stop")

    with patch.object(llm_router._RUNTIME, "generate_stream", side_effect=fake_stream):
        client = TestClient(make_app())
        response = client.post("/llm/generate/stream", json={"prompt": "hello"})

    assert response.status_code == 200
    events = [json.loads(line) for line in response.text.strip().splitlines()]
    assert events[0]["chunk"]["delta"] == "alpha"
    assert events[1]["chunk"]["done"] is True
    assert events[1]["chunk"]["finishReason"] == "stop"
