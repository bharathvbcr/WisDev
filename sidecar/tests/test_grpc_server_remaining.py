from __future__ import annotations

import asyncio
import json
import types
from unittest.mock import AsyncMock, MagicMock, patch

import grpc
import pytest

import grpc_server
from grpc_server import LLMServiceServicer
from proto import llm_pb2, llm_pb2_grpc
from services.llm_runtime import LLMRuntime


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


async def _start_test_llm_server(runtime: LLMRuntime) -> tuple[grpc.aio.Server, grpc.aio.Channel, llm_pb2_grpc.LLMServiceStub]:
    server = grpc.aio.server()
    llm_pb2_grpc.add_LLMServiceServicer_to_server(LLMServiceServicer(runtime=runtime), server)
    port = server.add_insecure_port("127.0.0.1:0")
    await server.start()
    channel = grpc.aio.insecure_channel(f"127.0.0.1:{port}")
    await channel.channel_ready()
    return server, channel, llm_pb2_grpc.LLMServiceStub(channel)


def test_split_stream_text_uses_paragraph_boundaries():
    assert grpc_server.split_stream_text("alpha\n\nbeta\n\ngamma") == ["alpha\n\n", "beta\n\n", "gamma"]


@pytest.mark.asyncio
async def test_validate_internal_key_delegates_metadata_to_thread(monkeypatch):
    monkeypatch.setenv("INTERNAL_SERVICE_KEY", "secret")
    monkeypatch.setenv("OIDC_AUDIENCE", "aud")
    context = DummyContext(metadata={"authorization": "Bearer t", "internal_service_key": "secret"})

    with patch("grpc_server.asyncio.to_thread", AsyncMock(return_value=None)) as mock_to_thread:
        await grpc_server._validate_internal_key(None, context)

    args = mock_to_thread.await_args.args
    assert args[0] is grpc_server._validate_internal_key_sync
    assert args[1] == {"authorization": "Bearer t", "internal_service_key": "secret"}
    assert args[2:] == ("secret", "aud")


@pytest.mark.asyncio
async def test_generate_covers_permission_denied_and_system_prompt_success():
    servicer = LLMServiceServicer()
    context = DummyContext()

    with patch("grpc_server._validate_internal_key", AsyncMock(side_effect=PermissionError("nope"))):
        with pytest.raises(AbortCalled) as exc:
            await servicer.Generate(llm_pb2.GenerateRequest(prompt="hello"), context)
    assert exc.value.status_code == grpc.StatusCode.PERMISSION_DENIED

    request = llm_pb2.GenerateRequest(prompt="hello", system_prompt="system")
    with patch("grpc_server.GeminiService.generate_text", AsyncMock(return_value="reply")) as mock_generate:
        with patch("grpc_server.time.time", return_value=100.25):
            response = await servicer.Generate(request, context)

    assert mock_generate.await_args.kwargs["prompt"] == "system\n\nhello"
    assert response.text == "reply"
    assert response.model_used == grpc_server.GEMINI_LIGHT_MODEL


@pytest.mark.asyncio
async def test_generate_stream_covers_system_prompt_and_invalid_prompt():
    servicer = LLMServiceServicer()
    context = DummyContext()
    captured = {}

    async def fake_stream(**kwargs):
        captured.update(kwargs)
        yield kwargs["prompt"]

    request = llm_pb2.GenerateRequest(prompt="hello", system_prompt="system")
    with patch("grpc_server.GeminiService.generate_stream", side_effect=fake_stream):
        chunks = [chunk async for chunk in servicer.GenerateStream(request, context)]

    assert captured["prompt"] == "system\n\nhello"
    assert [chunk.delta for chunk in chunks] == ["system\n\nhello", ""]
    assert chunks[-1].done is True

    with patch("grpc_server.GeminiService.generate_stream", side_effect=ValueError("bad prompt")):
        with pytest.raises(AbortCalled) as exc:
            [chunk async for chunk in servicer.GenerateStream(llm_pb2.GenerateRequest(prompt="hello"), context)]

    payload = json.loads(exc.value.details)
    assert payload["error"]["code"] == "INVALID_PROMPT"


@pytest.mark.asyncio
async def test_structured_output_covers_permission_schema_error_and_system_prompt():
    servicer = LLMServiceServicer()
    context = DummyContext()

    with patch("grpc_server._validate_internal_key", AsyncMock(side_effect=PermissionError("nope"))):
        with pytest.raises(AbortCalled) as exc:
            await servicer.StructuredOutput(llm_pb2.StructuredRequest(prompt="hello"), context)
    assert exc.value.status_code == grpc.StatusCode.PERMISSION_DENIED

    with pytest.raises(AbortCalled) as exc:
        await servicer.StructuredOutput(llm_pb2.StructuredRequest(prompt="hello", json_schema="{bad"), context)
    payload = json.loads(exc.value.details)
    assert payload["error"]["code"] == "INVALID_JSON_SCHEMA"

    request = llm_pb2.StructuredRequest(prompt="hello", system_prompt="system", json_schema='{"type":"object"}')
    with patch("grpc_server.GeminiService.generate_structured", AsyncMock(return_value={"ok": True})) as mock_structured:
        with patch("grpc_server.time.time", return_value=10.2):
            response = await servicer.StructuredOutput(request, context)

    assert mock_structured.await_args.kwargs["prompt"] == "system\n\nhello"
    assert json.loads(response.json_result) == {"ok": True}


@pytest.mark.asyncio
async def test_structured_output_invalid_json_surfaces_provider_failure():
    servicer = LLMServiceServicer()
    context = DummyContext()
    request = llm_pb2.StructuredRequest(
        prompt="hello",
        json_schema='{"type":"object"}',
    )

    with patch(
        "grpc_server.GeminiService.generate_structured",
        AsyncMock(return_value='{"value":'),
    ):
        with pytest.raises(AbortCalled) as exc:
            await servicer.StructuredOutput(request, context)

    payload = json.loads(exc.value.details)
    assert payload["error"]["code"] == "STRUCTURED_FAILED"


@pytest.mark.asyncio
async def test_embed_covers_permission_denied_and_cancelled():
    servicer = LLMServiceServicer()
    context = DummyContext()

    with patch("grpc_server._validate_internal_key", AsyncMock(side_effect=PermissionError("nope"))):
        with pytest.raises(AbortCalled) as exc:
            await servicer.Embed(llm_pb2.EmbedRequest(text="hello"), context)
    assert exc.value.status_code == grpc.StatusCode.PERMISSION_DENIED

    with patch.object(servicer.default_gemini, "embed", AsyncMock(side_effect=asyncio.CancelledError())):
        with pytest.raises(asyncio.CancelledError):
            await servicer.Embed(llm_pb2.EmbedRequest(text="hello"), context)


@pytest.mark.asyncio
async def test_embed_batch_covers_permission_denied():
    servicer = LLMServiceServicer()
    context = DummyContext()

    with patch("grpc_server._validate_internal_key", AsyncMock(side_effect=PermissionError("nope"))):
        with pytest.raises(AbortCalled) as exc:
            await servicer.EmbedBatch(llm_pb2.EmbedBatchRequest(texts=["a"]), context)
    assert exc.value.status_code == grpc.StatusCode.PERMISSION_DENIED


@pytest.mark.asyncio
async def test_structured_output_grpc_boundary_exercises_runtime_compatibility_filter():
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

    servicer = LLMServiceServicer(runtime=LLMRuntime(gemini_factory=LegacyStructuredGemini))
    context = DummyContext(metadata={"x-test": "1"})
    request = llm_pb2.StructuredRequest(
        prompt="hello",
        json_schema='{"type":"object"}',
        temperature=0.1,
        max_tokens=32,
        service_tier="standard",
        retry_profile="standard",
        request_class="structured_high_value",
        latency_budget_ms=30000,
        metadata={"trace_id": "trace-structured-grpc"},
    )
    request.thinking_budget = 64

    with patch("grpc_server._validate_internal_key", AsyncMock(return_value=None)):
        with patch("services.llm_runtime._log") as log_mock:
            response = await servicer.StructuredOutput(request, context)

    assert response.json_result == '{"value":"ok"}'
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


@pytest.mark.asyncio
async def test_embed_grpc_boundary_exercises_runtime_compatibility_filter():
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
                    "text": text,
                    "model": model,
                    "task_type": task_type,
                }
            )
            return [0.1, 0.2]

    servicer = LLMServiceServicer(runtime=LLMRuntime(gemini_factory=LegacyEmbedGemini))
    context = DummyContext(metadata={"x-test": "1"})
    request = llm_pb2.EmbedRequest(
        text="hello",
        task_type="RETRIEVAL_QUERY",
        latency_budget_ms=12000,
        metadata={"trace_id": "trace-embed-grpc"},
    )

    with patch("grpc_server._validate_internal_key", AsyncMock(return_value=None)):
        with patch("services.llm_runtime._log") as log_mock:
            response = await servicer.Embed(request, context)

    assert response.embedding == pytest.approx([0.1, 0.2])
    assert len(LegacyEmbedGemini.calls) == 1
    assert LegacyEmbedGemini.calls[0] == {
        "text": "hello",
        "model": "text-embedding-005",
        "task_type": "RETRIEVAL_QUERY",
    }

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


@pytest.mark.asyncio
async def test_structured_output_grpc_transport_exercises_runtime_compatibility_filter(monkeypatch):
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
            return '{"transport":"ok"}'

    monkeypatch.delenv("INTERNAL_SERVICE_KEY", raising=False)
    monkeypatch.delenv("OIDC_AUDIENCE", raising=False)

    runtime = LLMRuntime(gemini_factory=LegacyStructuredGemini)
    server, channel, stub = await _start_test_llm_server(runtime)
    request = llm_pb2.StructuredRequest(
        prompt="hello",
        json_schema='{"type":"object"}',
        temperature=0.2,
        max_tokens=64,
        service_tier="standard",
        retry_profile="standard",
        request_class="structured_high_value",
        latency_budget_ms=28000,
        metadata={"trace_id": "trace-structured-grpc-transport"},
    )
    request.thinking_budget = 32

    try:
        with patch("services.llm_runtime._log") as log_mock:
            response = await stub.StructuredOutput(request, metadata=(("x-test", "1"),), timeout=5)
    finally:
        await channel.close()
        await server.stop(grace=0)

    assert response.json_result == '{"transport":"ok"}'
    assert len(LegacyStructuredGemini.calls) == 1
    structured_call = LegacyStructuredGemini.calls[0]
    assert structured_call["model"] == "gemini-2.5-flash-lite"
    assert structured_call["prompt"] == "hello"
    assert structured_call["json_schema"] == {"type": "object"}
    assert structured_call["temperature"] == pytest.approx(0.2)
    assert structured_call["max_tokens"] == 64

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


@pytest.mark.asyncio
async def test_embed_grpc_transport_exercises_runtime_compatibility_filter(monkeypatch):
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
                    "text": text,
                    "model": model,
                    "task_type": task_type,
                }
            )
            return [0.3, 0.4]

    monkeypatch.delenv("INTERNAL_SERVICE_KEY", raising=False)
    monkeypatch.delenv("OIDC_AUDIENCE", raising=False)

    runtime = LLMRuntime(gemini_factory=LegacyEmbedGemini)
    server, channel, stub = await _start_test_llm_server(runtime)
    request = llm_pb2.EmbedRequest(
        text="hello",
        task_type="RETRIEVAL_QUERY",
        latency_budget_ms=15000,
        metadata={"trace_id": "trace-embed-grpc-transport"},
    )

    try:
        with patch("services.llm_runtime._log") as log_mock:
            response = await stub.Embed(request, metadata=(("x-test", "1"),), timeout=5)
    finally:
        await channel.close()
        await server.stop(grace=0)

    assert response.embedding == pytest.approx([0.3, 0.4])
    assert len(LegacyEmbedGemini.calls) == 1
    assert LegacyEmbedGemini.calls[0] == {
        "text": "hello",
        "model": "text-embedding-005",
        "task_type": "RETRIEVAL_QUERY",
    }

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


@pytest.mark.asyncio
async def test_generate_stream_grpc_transport_exercises_runtime_compatibility_filter(monkeypatch):
    class LegacyStreamGemini:
        calls: list[dict[str, object]] = []

        def __init__(self, model: str = ""):
            self.model = model

        async def generate_stream(
            self,
            prompt: str,
            temperature: float = 0.7,
            max_tokens: int = 2048,
        ):
            self.__class__.calls.append(
                {
                    "model": self.model,
                    "prompt": prompt,
                    "temperature": temperature,
                    "max_tokens": max_tokens,
                }
            )
            yield "chunk-1"
            yield "chunk-2"

    monkeypatch.delenv("INTERNAL_SERVICE_KEY", raising=False)
    monkeypatch.delenv("OIDC_AUDIENCE", raising=False)

    runtime = LLMRuntime(gemini_factory=LegacyStreamGemini)
    server, channel, stub = await _start_test_llm_server(runtime)
    request = llm_pb2.GenerateRequest(
        prompt="hello",
        temperature=0.4,
        max_tokens=48,
        service_tier="standard",
        latency_budget_ms=16000,
        metadata={"trace_id": "trace-stream-grpc-transport"},
    )
    request.thinking_budget = 24

    try:
        with patch("services.llm_runtime._log") as log_mock:
            response_stream = stub.GenerateStream(
                request, metadata=(("x-test", "1"),), timeout=5
            )
            chunks = [chunk async for chunk in response_stream]
    finally:
        await channel.close()
        await server.stop(grace=0)

    assert [chunk.delta for chunk in chunks] == ["chunk-1", "chunk-2", ""]
    assert [chunk.done for chunk in chunks] == [False, False, True]
    assert len(LegacyStreamGemini.calls) == 1
    assert LegacyStreamGemini.calls[0] == {
        "model": "gemini-2.5-flash-lite",
        "prompt": "hello",
        "temperature": pytest.approx(0.4),
        "max_tokens": 48,
    }

    filtered_call = next(
        call
        for call in log_mock.call_args_list
        if call.args and call.args[0] == "llm_runtime_kwargs_filtered"
    )
    assert filtered_call.kwargs["method"] == "generate_stream"
    assert set(filtered_call.kwargs["dropped_fields"]) == {
        "latency_budget_ms",
        "service_tier",
        "thinking_budget",
        "trace_id",
    }


@pytest.mark.asyncio
async def test_generate_grpc_transport_exercises_runtime_compatibility_filter(monkeypatch):
    class LegacyGenerateGemini:
        calls: list[dict[str, object]] = []

        def __init__(self, model: str = ""):
            self.model = model

        async def generate_text(
            self,
            prompt: str,
            temperature: float = 0.7,
            max_tokens: int = 2048,
        ) -> str:
            self.__class__.calls.append(
                {
                    "model": self.model,
                    "prompt": prompt,
                    "temperature": temperature,
                    "max_tokens": max_tokens,
                }
            )
            return "transport-text"

    monkeypatch.delenv("INTERNAL_SERVICE_KEY", raising=False)
    monkeypatch.delenv("OIDC_AUDIENCE", raising=False)

    runtime = LLMRuntime(gemini_factory=LegacyGenerateGemini)
    server, channel, stub = await _start_test_llm_server(runtime)
    request = llm_pb2.GenerateRequest(
        prompt="hello",
        temperature=0.5,
        max_tokens=40,
        service_tier="standard",
        retry_profile="standard",
        request_class="light",
        latency_budget_ms=17000,
        metadata={"trace_id": "trace-generate-grpc-transport"},
    )
    request.thinking_budget = 20

    try:
        with patch("services.llm_runtime._log") as log_mock:
            response = await stub.Generate(
                request, metadata=(("x-test", "1"),), timeout=5
            )
    finally:
        await channel.close()
        await server.stop(grace=0)

    assert response.text == "transport-text"
    assert len(LegacyGenerateGemini.calls) == 1
    assert LegacyGenerateGemini.calls[0] == {
        "model": "gemini-2.5-flash-lite",
        "prompt": "hello",
        "temperature": pytest.approx(0.5),
        "max_tokens": 40,
    }

    filtered_call = next(
        call
        for call in log_mock.call_args_list
        if call.args and call.args[0] == "llm_runtime_kwargs_filtered"
    )
    assert filtered_call.kwargs["method"] == "generate_text"
    assert set(filtered_call.kwargs["dropped_fields"]) == {
        "latency_budget_ms",
        "request_class",
        "retry_profile",
        "service_tier",
        "thinking_budget",
        "trace_id",
    }


@pytest.mark.asyncio
async def test_serve_async_invokes_graceful_shutdown_handler(monkeypatch):
    server = MagicMock()
    server.start = AsyncMock()
    server.add_insecure_port = MagicMock(return_value=1)
    server.stop = AsyncMock()
    callbacks = []
    tasks = []

    def add_signal_handler(_sig, callback):
        callbacks.append(callback)

    def ensure_future(coro):
        task = asyncio.create_task(coro)
        tasks.append(task)
        return task

    async def wait_for_termination():
        callbacks[0]()
        await asyncio.gather(*tasks)

    server.wait_for_termination = AsyncMock(side_effect=wait_for_termination)
    fake_loop = types.SimpleNamespace(add_signal_handler=add_signal_handler)

    monkeypatch.setenv("GRPC_SHUTDOWN_GRACE_SECONDS", "7")

    with patch("grpc_server.grpc.aio.server", return_value=server):
        with patch("grpc_server.asyncio.get_running_loop", return_value=fake_loop):
            with patch("grpc_server.asyncio.ensure_future", side_effect=ensure_future):
                with patch("grpc_server.llm_pb2_grpc.add_LLMServiceServicer_to_server"):
                    await grpc_server.serve_async(interceptors=["otel"])

    server.stop.assert_awaited_once_with(grace=7)


@pytest.mark.asyncio
async def test_permission_denied_and_invalid_schema_paths_return_after_abort():
    servicer = LLMServiceServicer()
    context = DummyContext()

    with patch("grpc_server._validate_internal_key", AsyncMock(side_effect=PermissionError("nope"))):
        with patch("grpc_server._abort_with_typed_error", AsyncMock(return_value=None)) as mock_abort:
            assert await servicer.Generate(llm_pb2.GenerateRequest(prompt="hello"), context) is None
    mock_abort.assert_awaited_once()

    with patch("grpc_server._validate_internal_key", AsyncMock(side_effect=PermissionError("nope"))):
        with patch("grpc_server._abort_with_typed_error", AsyncMock(return_value=None)) as mock_abort:
            chunks = [chunk async for chunk in servicer.GenerateStream(llm_pb2.GenerateRequest(prompt="hello"), context)]
    assert chunks == []
    mock_abort.assert_awaited_once()

    with patch("grpc_server._validate_internal_key", AsyncMock(side_effect=PermissionError("nope"))):
        with patch("grpc_server._abort_with_typed_error", AsyncMock(return_value=None)) as mock_abort:
            assert await servicer.StructuredOutput(llm_pb2.StructuredRequest(prompt="hello"), context) is None
    mock_abort.assert_awaited_once()

    with patch("grpc_server._abort_with_typed_error", AsyncMock(return_value=None)) as mock_abort:
        assert await servicer.StructuredOutput(llm_pb2.StructuredRequest(prompt="hello", json_schema="{bad"), context) is None
    mock_abort.assert_awaited_once()

    with patch("grpc_server._validate_internal_key", AsyncMock(side_effect=PermissionError("nope"))):
        with patch("grpc_server._abort_with_typed_error", AsyncMock(return_value=None)) as mock_abort:
            assert await servicer.Embed(llm_pb2.EmbedRequest(text="hello"), context) is None
    mock_abort.assert_awaited_once()

    with patch("grpc_server._validate_internal_key", AsyncMock(side_effect=PermissionError("nope"))):
        with patch("grpc_server._abort_with_typed_error", AsyncMock(return_value=None)) as mock_abort:
            assert await servicer.EmbedBatch(llm_pb2.EmbedBatchRequest(texts=["hello"]), context) is None
    mock_abort.assert_awaited_once()
