from __future__ import annotations

import asyncio
import json
import sys
import types
from unittest.mock import AsyncMock, MagicMock, patch

import grpc
import pytest

import grpc_server
from grpc_server import LLMServiceServicer
from proto import llm_pb2


class AbortCalled(RuntimeError):
    def __init__(self, status_code, details):
        super().__init__(details)
        self.status_code = status_code
        self.details = details


class DummyContext:
    def __init__(self, metadata=None):
        self._metadata = metadata or {}

    async def abort(self, status_code, details):
        raise AbortCalled(status_code, details)

    def invocation_metadata(self):
        return self._metadata.items()


def test_split_stream_text_empty_and_fixed_chunks():
    assert grpc_server.split_stream_text("") == []
    chunks = grpc_server.split_stream_text("x" * 300)
    assert len(chunks) == 3


def test_log_helpers_without_fields():
    with patch.object(grpc_server.logger, "info") as mock_info:
        grpc_server._log("event")
    mock_info.assert_called_once_with("event")

    with patch.object(grpc_server.logger, "error") as mock_error:
        grpc_server._log_error("event")
    mock_error.assert_called_once_with("event")


def test_get_trace_id_handles_missing_metadata():
    assert grpc_server._get_trace_id(types.SimpleNamespace()) == ""
    assert grpc_server._get_trace_id(types.SimpleNamespace(metadata={"trace_id": "t1"})) == "t1"


def test_validate_internal_key_sync_oidc_success():
    oauth2_mod = types.ModuleType("google.oauth2")
    id_token_mod = types.ModuleType("google.oauth2.id_token")
    id_token_mod.verify_oauth2_token = lambda token, request, audience: {"ok": True}
    oauth2_mod.id_token = id_token_mod
    transport_mod = types.ModuleType("google.auth.transport")
    requests_mod = types.ModuleType("google.auth.transport.requests")
    requests_mod.Request = lambda: object()
    transport_mod.requests = requests_mod

    with patch.dict(
        sys.modules,
        {
            "google.oauth2": oauth2_mod,
            "google.oauth2.id_token": id_token_mod,
            "google.auth.transport": transport_mod,
            "google.auth.transport.requests": requests_mod,
        },
        clear=False,
    ):
        grpc_server._validate_internal_key_sync({"authorization": "Bearer token"}, "", "aud")


def test_validate_internal_key_sync_falls_back_to_static_key_after_oidc_failure():
    oauth2_mod = types.ModuleType("google.oauth2")
    id_token_mod = types.ModuleType("google.oauth2.id_token")
    id_token_mod.verify_oauth2_token = lambda token, request, audience: (_ for _ in ()).throw(RuntimeError("bad"))
    oauth2_mod.id_token = id_token_mod
    transport_mod = types.ModuleType("google.auth.transport")
    requests_mod = types.ModuleType("google.auth.transport.requests")
    requests_mod.Request = lambda: object()
    transport_mod.requests = requests_mod

    with patch.dict(
        sys.modules,
        {
            "google.oauth2": oauth2_mod,
            "google.oauth2.id_token": id_token_mod,
            "google.auth.transport": transport_mod,
            "google.auth.transport.requests": requests_mod,
        },
        clear=False,
    ):
        grpc_server._validate_internal_key_sync(
            {"authorization": "Bearer token", "internal_service_key": "secret"},
            "secret",
            "aud",
        )


def test_validate_internal_key_sync_raises_permission_error():
    with pytest.raises(PermissionError):
        grpc_server._validate_internal_key_sync({}, "secret", "")


@pytest.mark.asyncio
async def test_validate_internal_key_returns_without_configuration(monkeypatch):
    monkeypatch.delenv("INTERNAL_SERVICE_KEY", raising=False)
    monkeypatch.delenv("OIDC_AUDIENCE", raising=False)
    await grpc_server._validate_internal_key(None, DummyContext())


@pytest.mark.asyncio
async def test_generate_aborts_when_model_returns_empty_text():
    servicer = LLMServiceServicer()
    request = llm_pb2.GenerateRequest(prompt="hello")
    context = DummyContext()

    with patch("grpc_server.GeminiService.generate_text", AsyncMock(return_value="   ")):
        with pytest.raises(AbortCalled) as exc:
            await servicer.Generate(request, context)

    payload = json.loads(exc.value.details)
    assert payload["error"]["code"] == "GENERATE_FAILED"


@pytest.mark.asyncio
async def test_generate_reraises_cancelled_error():
    servicer = LLMServiceServicer()
    request = llm_pb2.GenerateRequest(prompt="hello")
    context = DummyContext()

    with patch("grpc_server.GeminiService.generate_text", AsyncMock(side_effect=asyncio.CancelledError())):
        with pytest.raises(asyncio.CancelledError):
            await servicer.Generate(request, context)


@pytest.mark.asyncio
async def test_generate_stream_permission_denied():
    servicer = LLMServiceServicer()
    request = llm_pb2.GenerateRequest(prompt="hello")
    context = DummyContext()

    with patch("grpc_server._validate_internal_key", AsyncMock(side_effect=PermissionError("nope"))):
        with pytest.raises(AbortCalled) as exc:
            [chunk async for chunk in servicer.GenerateStream(request, context)]

    assert exc.value.status_code == grpc.StatusCode.PERMISSION_DENIED


@pytest.mark.asyncio
async def test_generate_stream_reraises_cancelled_error():
    servicer = LLMServiceServicer()
    request = llm_pb2.GenerateRequest(prompt="hello")
    context = DummyContext()

    async def cancelled_stream(*args, **kwargs):
        raise asyncio.CancelledError()
        yield ""

    with patch("grpc_server.GeminiService.generate_stream", side_effect=cancelled_stream):
        with pytest.raises(asyncio.CancelledError):
            [chunk async for chunk in servicer.GenerateStream(request, context)]


@pytest.mark.asyncio
async def test_structured_output_invalid_prompt_aborts():
    servicer = LLMServiceServicer()
    request = llm_pb2.StructuredRequest(prompt="   ", json_schema='{"type":"object"}')
    context = DummyContext()

    with pytest.raises(AbortCalled) as exc:
        await servicer.StructuredOutput(request, context)

    payload = json.loads(exc.value.details)
    assert payload["error"]["code"] == "INVALID_PROMPT"


@pytest.mark.asyncio
async def test_structured_output_reraises_cancelled_error():
    servicer = LLMServiceServicer()
    request = llm_pb2.StructuredRequest(prompt="hello", json_schema='{"type":"object"}')
    context = DummyContext()

    with patch("grpc_server.GeminiService.generate_structured", AsyncMock(side_effect=asyncio.CancelledError())):
        with pytest.raises(asyncio.CancelledError):
            await servicer.StructuredOutput(request, context)


@pytest.mark.asyncio
async def test_embed_success_and_failure():
    servicer = LLMServiceServicer()
    context = DummyContext()

    with patch.object(servicer.default_gemini, "embed", AsyncMock(return_value=[0.1, 0.2])):
        response = await servicer.Embed(llm_pb2.EmbedRequest(text="hello"), context)
    assert pytest.approx(list(response.embedding)) == [0.1, 0.2]

    with patch.object(servicer.default_gemini, "embed", AsyncMock(side_effect=RuntimeError("boom"))):
        with pytest.raises(AbortCalled) as exc:
            await servicer.Embed(llm_pb2.EmbedRequest(text="hello"), context)
    payload = json.loads(exc.value.details)
    assert payload["error"]["code"] == "EMBED_FAILED"

    with patch.object(servicer.default_gemini, "embed", AsyncMock(side_effect=asyncio.TimeoutError())):
        with pytest.raises(AbortCalled) as exc:
            await servicer.Embed(llm_pb2.EmbedRequest(text="hello"), context)
    payload = json.loads(exc.value.details)
    assert exc.value.status_code == grpc.StatusCode.DEADLINE_EXCEEDED
    assert payload["error"]["code"] == "EMBED_TIMEOUT"


@pytest.mark.asyncio
async def test_embed_batch_success_cancelled_and_failure():
    servicer = LLMServiceServicer()
    context = DummyContext()

    with patch.object(servicer.default_gemini, "embed_batch", AsyncMock(return_value=[[0.1], [0.2]])):
        response = await servicer.EmbedBatch(llm_pb2.EmbedBatchRequest(texts=["a", "b"]), context)
    assert len(response.embeddings) == 2

    with patch.object(servicer.default_gemini, "embed_batch", AsyncMock(side_effect=asyncio.CancelledError())):
        with pytest.raises(asyncio.CancelledError):
            await servicer.EmbedBatch(llm_pb2.EmbedBatchRequest(texts=["a"]), context)

    with patch.object(servicer.default_gemini, "embed_batch", AsyncMock(side_effect=RuntimeError("boom"))):
        with pytest.raises(AbortCalled) as exc:
            await servicer.EmbedBatch(llm_pb2.EmbedBatchRequest(texts=["a"]), context)
    payload = json.loads(exc.value.details)
    assert payload["error"]["code"] == "EMBED_BATCH_FAILED"

    with patch.object(servicer.default_gemini, "embed_batch", AsyncMock(side_effect=ValueError("bad embed batch request"))):
        with pytest.raises(AbortCalled) as exc:
            await servicer.EmbedBatch(llm_pb2.EmbedBatchRequest(texts=["a"]), context)
    payload = json.loads(exc.value.details)
    assert exc.value.status_code == grpc.StatusCode.INVALID_ARGUMENT
    assert payload["error"]["code"] == "INVALID_EMBED_BATCH_REQUEST"


@pytest.mark.asyncio
async def test_health_reports_not_ready_detail():
    servicer = LLMServiceServicer()
    with patch("grpc_server.get_gemini_runtime_diagnostics", return_value={"ready": False, "detail": "down"}):
        response = await servicer.Health(None, None)
    assert response.ok is False
    assert response.error == "down"


@pytest.mark.asyncio
async def test_serve_async_uses_explicit_addr_and_handles_signal_registration_failure(monkeypatch):
    server = MagicMock()
    server.start = AsyncMock()
    server.wait_for_termination = AsyncMock(return_value=None)
    server.add_insecure_port = MagicMock(return_value=1)
    server.stop = AsyncMock()

    fake_loop = MagicMock()
    fake_loop.add_signal_handler.side_effect = NotImplementedError()

    monkeypatch.setenv("PYTHON_SIDECAR_GRPC_ADDR", "127.0.0.1:6000")
    monkeypatch.setenv("GRPC_SHUTDOWN_GRACE_SECONDS", "3")

    with patch("grpc_server.grpc.aio.server", return_value=server):
        with patch("grpc_server.asyncio.get_running_loop", return_value=fake_loop):
            with patch("grpc_server.llm_pb2_grpc.add_LLMServiceServicer_to_server") as mock_add:
                await grpc_server.serve_async(interceptors=["otel"])

    server.add_insecure_port.assert_called_once_with("127.0.0.1:6000")
    mock_add.assert_called_once()


def test_serve_uses_new_event_loop():
    fake_loop = MagicMock()
    fake_loop.run_until_complete = MagicMock(side_effect=lambda coro: asyncio.run(coro))
    with patch("grpc_server.asyncio.new_event_loop", return_value=fake_loop):
        with patch("grpc_server.asyncio.set_event_loop") as mock_set_loop:
            with patch("grpc_server.serve_async", AsyncMock(return_value=None)):
                grpc_server.serve(interceptors=["otel"])
    mock_set_loop.assert_called_once_with(fake_loop)
    fake_loop.run_until_complete.assert_called_once()
