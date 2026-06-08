from __future__ import annotations

import asyncio
import json
from types import SimpleNamespace
from unittest.mock import AsyncMock, MagicMock, patch

import grpc
import pytest

from services.gemini_service import StructuredOutputRequiresNativeRuntimeError
from services.llm_runtime import (
    LLMRuntime,
    LLMRuntimeError,
    _log,
    _log_error,
    error_envelope,
    trace_id_from_request,
    validate_invocation_metadata,
    _validate_internal_key_sync,
)


class _FakeGemini:
    def __init__(
        self,
        *,
        text: str = "reply",
        chunks: list[str] | None = None,
        structured: str = '{"value":"ok"}',
        embedding: list[float] | None = None,
        embeddings: list[list[float]] | None = None,
        raise_on_text: Exception | None = None,
        raise_on_stream: Exception | None = None,
        raise_on_structured: Exception | None = None,
        raise_on_embed: Exception | None = None,
        raise_on_embed_batch: Exception | None = None,
    ):
        self.text = text
        self.chunks = chunks or ["alpha", "beta"]
        self.structured = structured
        self.embedding = embedding or [0.1, 0.2]
        self.embeddings = embeddings or [[0.1], [0.2]]
        self.raise_on_text = raise_on_text
        self.raise_on_stream = raise_on_stream
        self.raise_on_structured = raise_on_structured
        self.raise_on_embed = raise_on_embed
        self.raise_on_embed_batch = raise_on_embed_batch

    async def generate_text(self, **_kwargs):
        if self.raise_on_text is not None:
            raise self.raise_on_text
        return self.text

    async def generate_stream(self, **_kwargs):
        if self.raise_on_stream is not None:
            raise self.raise_on_stream
        for chunk in self.chunks:
            yield chunk

    async def generate_structured(self, **_kwargs):
        if self.raise_on_structured is not None:
            raise self.raise_on_structured
        return self.structured

    async def embed(self, **_kwargs):
        if self.raise_on_embed is not None:
            raise self.raise_on_embed
        return self.embedding

    async def embed_batch(self, **_kwargs):
        if self.raise_on_embed_batch is not None:
            raise self.raise_on_embed_batch
        return self.embeddings

    def is_ready(self):
        return True


class _LimitedGemini(_FakeGemini):
    def __init__(self, **kwargs):
        super().__init__(**kwargs)
        self.last_generate_text_kwargs = None
        self.last_generate_stream_kwargs = None
        self.last_generate_structured_kwargs = None
        self.last_embed_kwargs = None
        self.last_embed_batch_kwargs = None

    async def generate_text(
        self,
        *,
        prompt,
        temperature,
        max_tokens,
        service_tier=None,
        thinking_budget=None,
        latency_budget_ms=None,
        trace_id=None,
    ):
        self.last_generate_text_kwargs = {
            "prompt": prompt,
            "temperature": temperature,
            "max_tokens": max_tokens,
            "service_tier": service_tier,
            "thinking_budget": thinking_budget,
            "latency_budget_ms": latency_budget_ms,
            "trace_id": trace_id,
        }
        return self.text

    async def generate_structured(
        self,
        *,
        prompt,
        json_schema,
        temperature,
        max_tokens,
        service_tier=None,
        thinking_budget=None,
        latency_budget_ms=None,
        trace_id=None,
    ):
        self.last_generate_structured_kwargs = {
            "prompt": prompt,
            "json_schema": json_schema,
            "temperature": temperature,
            "max_tokens": max_tokens,
            "service_tier": service_tier,
            "thinking_budget": thinking_budget,
            "latency_budget_ms": latency_budget_ms,
            "trace_id": trace_id,
        }
        return self.structured

    async def generate_stream(
        self,
        *,
        prompt,
        temperature,
        max_tokens,
        service_tier=None,
        thinking_budget=None,
        latency_budget_ms=None,
        trace_id=None,
    ):
        self.last_generate_stream_kwargs = {
            "prompt": prompt,
            "temperature": temperature,
            "max_tokens": max_tokens,
            "service_tier": service_tier,
            "thinking_budget": thinking_budget,
            "latency_budget_ms": latency_budget_ms,
            "trace_id": trace_id,
        }
        for chunk in self.chunks:
            yield chunk

    async def embed(
        self,
        *,
        text,
        model,
        task_type,
    ):
        self.last_embed_kwargs = {
            "text": text,
            "model": model,
            "task_type": task_type,
        }
        return self.embedding

    async def embed_batch(
        self,
        *,
        texts,
        model,
        task_type,
    ):
        self.last_embed_batch_kwargs = {
            "texts": texts,
            "model": model,
            "task_type": task_type,
        }
        return self.embeddings


def test_error_envelope_and_trace_id_from_request():
    request = SimpleNamespace(metadata={"trace_id": "req-trace"})
    assert trace_id_from_request(request, {"trace_id": "meta-trace"}) == "req-trace"
    assert trace_id_from_request(SimpleNamespace(metadata={}), {"trace_id": "meta-trace"}) == "meta-trace"

    envelope = error_envelope(
        LLMRuntimeError(
            grpc_status=grpc.StatusCode.INTERNAL,
            http_status=500,
            code="BROKEN",
            message="boom",
            trace_id="trace-1",
            details={"reason": "x"},
        )
    )
    assert envelope["error"]["details"] == {"reason": "x"}


def test_log_helpers_without_fields():
    with patch("services.llm_runtime.logger.info") as mock_info:
        _log("event")
    with patch("services.llm_runtime.logger.error") as mock_error:
        _log_error("event")

    mock_info.assert_called_once_with("event")
    mock_error.assert_called_once_with("event")


def test_validate_internal_key_sync_oidc_and_static_fallback(monkeypatch):
    requests_mod = SimpleNamespace(Request=lambda: object())
    id_token_mod = SimpleNamespace()
    id_token_mod.verify_oauth2_token = lambda token, request, audience: (_ for _ in ()).throw(RuntimeError("bad"))

    google_auth_mod = SimpleNamespace(transport=SimpleNamespace(requests=requests_mod))
    google_module = SimpleNamespace(auth=google_auth_mod, oauth2=SimpleNamespace(id_token=id_token_mod))

    with patch.dict(
        "sys.modules",
        {
            "google.auth": google_auth_mod,
            "google.auth.transport": google_auth_mod.transport,
            "google.auth.transport.requests": requests_mod,
            "google.oauth2": google_module.oauth2,
            "google.oauth2.id_token": id_token_mod,
        },
        clear=False,
    ):
        _validate_internal_key_sync(
            {"authorization": "Bearer token", "internal_service_key": "secret"},
            "secret",
            "aud",
        )

    with pytest.raises(PermissionError):
        _validate_internal_key_sync({}, "secret", "")


def test_validate_internal_key_sync_oidc_success(monkeypatch):
    requests_mod = SimpleNamespace(Request=lambda: object())
    id_token_mod = SimpleNamespace()
    id_token_mod.verify_oauth2_token = lambda token, request, audience: None
    google_auth_mod = SimpleNamespace(transport=SimpleNamespace(requests=requests_mod))
    google_module = SimpleNamespace(auth=google_auth_mod, oauth2=SimpleNamespace(id_token=id_token_mod))

    with patch.dict(
        "sys.modules",
        {
            "google.auth": google_auth_mod,
            "google.auth.transport": google_auth_mod.transport,
            "google.auth.transport.requests": requests_mod,
            "google.oauth2": google_module.oauth2,
            "google.oauth2.id_token": id_token_mod,
        },
        clear=False,
    ):
        _validate_internal_key_sync({"authorization": "Bearer token"}, "", "aud")


@pytest.mark.asyncio
async def test_validate_invocation_metadata_skips_when_disabled_and_uses_thread_when_enabled(monkeypatch):
    monkeypatch.delenv("INTERNAL_SERVICE_KEY", raising=False)
    monkeypatch.delenv("OIDC_AUDIENCE", raising=False)
    await validate_invocation_metadata(None)

    monkeypatch.setenv("INTERNAL_SERVICE_KEY", "secret")
    with patch("services.llm_runtime.asyncio.to_thread", AsyncMock(return_value=None)) as mock_to_thread:
        await validate_invocation_metadata({"trace_id": "abc"})

    mock_to_thread.assert_awaited_once()


@pytest.mark.asyncio
async def test_generate_covers_success_and_permission_denied(monkeypatch):
    runtime = LLMRuntime(gemini_factory=lambda model=None: _FakeGemini(text="generated"))
    request = SimpleNamespace(prompt="hello", model="", temperature=0.5, max_tokens=16, metadata={"trace_id": "trace-1"})

    with patch("services.llm_runtime.validate_invocation_metadata", AsyncMock(return_value=None)):
        response = await runtime.generate(request, metadata={"trace_id": "meta"}, validate_credentials=True)

    assert response.text == "generated"
    assert response.model_used

    with patch("services.llm_runtime.validate_invocation_metadata", AsyncMock(side_effect=PermissionError("nope"))):
        with pytest.raises(LLMRuntimeError) as exc:
            await runtime.generate(request, metadata={"trace_id": "meta"}, validate_credentials=True)

    assert exc.value.grpc_status == grpc.StatusCode.PERMISSION_DENIED
    assert exc.value.code == "UNAUTHORIZED"


@pytest.mark.asyncio
async def test_generate_logs_shared_trace_id_for_go_correlation(monkeypatch):
    runtime = LLMRuntime(gemini_factory=lambda model=None: _FakeGemini(text="generated"))
    request = SimpleNamespace(
        prompt="hello",
        model="",
        temperature=0.5,
        max_tokens=16,
        metadata={"trace_id": "trace-sidecar-correlation"},
    )

    with (
        patch("services.llm_runtime.validate_invocation_metadata", AsyncMock(return_value=None)),
        patch("services.llm_runtime.logger.info") as mock_info,
    ):
        await runtime.generate(request, metadata={"trace_id": "meta-trace"}, validate_credentials=True)

    events = {call.args[1]: json.loads(call.args[2]) for call in mock_info.call_args_list}
    assert events["llm_generate_start"]["trace_id"] == "trace-sidecar-correlation"
    assert events["llm_generate_success"]["trace_id"] == "trace-sidecar-correlation"


@pytest.mark.asyncio
async def test_generate_filters_kwargs_for_older_gemini_service(monkeypatch):
    limited = _LimitedGemini(text="compat-generated")
    runtime = LLMRuntime(gemini_factory=lambda model=None: limited)
    request = SimpleNamespace(
        prompt="hello",
        model="",
        temperature=0.5,
        max_tokens=16,
        metadata={
            "trace_id": "trace-generate-compat",
            "thinking_budget": "0",
            "service_tier": "standard",
            "retry_profile": "conservative",
            "request_class": "light",
            "latency_budget_ms": "12000",
        },
    )

    with patch("services.llm_runtime.validate_invocation_metadata", AsyncMock(return_value=None)):
        response = await runtime.generate(request, validate_credentials=True)

    assert response.text == "compat-generated"
    assert limited.last_generate_text_kwargs == {
        "prompt": "hello",
        "temperature": 0.5,
        "max_tokens": 16,
        "service_tier": "standard",
        "thinking_budget": 0,
        "latency_budget_ms": 12000,
        "trace_id": "trace-generate-compat",
    }


@pytest.mark.asyncio
async def test_generate_stream_and_structured_output_cover_error_paths(monkeypatch):
    success_stream_runtime = LLMRuntime(gemini_factory=lambda model=None: _FakeGemini(chunks=["alpha", "beta"]))
    stream_request = SimpleNamespace(prompt="hello", model="", temperature=0.5, max_tokens=16, metadata={})

    with patch("services.llm_runtime.validate_invocation_metadata", AsyncMock(return_value=None)):
        stream_chunks = [
            chunk
            async for chunk in success_stream_runtime.generate_stream(stream_request, validate_credentials=True)
        ]
    assert [chunk.delta for chunk in stream_chunks] == ["alpha", "beta", ""]

    stream_runtime = LLMRuntime(
        gemini_factory=lambda model=None: _FakeGemini(chunks=["alpha"], raise_on_stream=RuntimeError("boom"))
    )

    with patch("services.llm_runtime.validate_invocation_metadata", AsyncMock(return_value=None)):
        chunks = []
        with pytest.raises(LLMRuntimeError) as exc:
            async for chunk in stream_runtime.generate_stream(stream_request, validate_credentials=True):
                chunks.append(chunk)
    assert exc.value.code == "STREAM_FAILED"
    assert chunks == []

    with patch("services.llm_runtime.validate_invocation_metadata", AsyncMock(side_effect=PermissionError("nope"))):
        with pytest.raises(LLMRuntimeError) as denied_stream_exc:
            async for _ in success_stream_runtime.generate_stream(stream_request, validate_credentials=True):
                pass
    assert denied_stream_exc.value.code == "UNAUTHORIZED"

    structured_runtime = LLMRuntime(
        gemini_factory=lambda model=None: _FakeGemini(structured='{"value":"ok"}')
    )
    structured_request = SimpleNamespace(
        prompt="hello",
        model="",
        json_schema='{"type":"object"}',
        temperature=0.3,
        max_tokens=16,
        metadata={"trace_id": "trace-2"},
    )
    with patch("services.llm_runtime.validate_invocation_metadata", AsyncMock(return_value=None)):
        response = await structured_runtime.structured_output(structured_request, validate_credentials=True)
    assert response.schema_valid is True

    thinking_service = _FakeGemini(structured='{"value":"ok"}')
    thinking_service.generate_structured = AsyncMock(return_value='{"value":"ok"}')
    thinking_runtime = LLMRuntime(gemini_factory=lambda model=None: thinking_service)
    thinking_request = SimpleNamespace(
        prompt="hello",
        model="gemini-2.5-flash",
        json_schema='{"type":"object"}',
        temperature=0.3,
        max_tokens=16,
        metadata={
            "trace_id": "trace-thinking",
            "thinking_budget": "-1",
            "service_tier": "priority",
            "latency_budget_ms": "30000",
            "timeout_s": "45",
        },
    )
    with patch("services.llm_runtime.validate_invocation_metadata", AsyncMock(return_value=None)):
        await thinking_runtime.structured_output(thinking_request, validate_credentials=True)
    thinking_service.generate_structured.assert_awaited_once_with(
        prompt="hello",
        json_schema={"type": "object"},
        temperature=0.3,
        max_tokens=16,
        latency_budget_ms=30000,
        service_tier="priority",
        thinking_budget=-1,
        retry_profile=None,
        request_class=None,
        trace_id="trace-thinking",
    )

    invalid_schema_request = SimpleNamespace(
        prompt="hello",
        model="",
        json_schema="{bad",
        temperature=0.3,
        max_tokens=16,
        metadata={"trace_id": "trace-2"},
    )
    with patch("services.llm_runtime.validate_invocation_metadata", AsyncMock(return_value=None)):
        with pytest.raises(LLMRuntimeError) as bad_schema_exc:
            await structured_runtime.structured_output(invalid_schema_request, validate_credentials=True)
    assert bad_schema_exc.value.code == "INVALID_JSON_SCHEMA"

    missing_schema_request = SimpleNamespace(
        prompt="hello",
        model="",
        json_schema="",
        temperature=0.3,
        max_tokens=16,
        metadata={"trace_id": "trace-2"},
    )
    with patch("services.llm_runtime.validate_invocation_metadata", AsyncMock(return_value=None)):
        with pytest.raises(LLMRuntimeError) as missing_schema_exc:
            await structured_runtime.structured_output(missing_schema_request, validate_credentials=True)
    assert missing_schema_exc.value.code == "MISSING_JSON_SCHEMA"

    with patch(
        "services.llm_runtime.validate_invocation_metadata",
        AsyncMock(return_value=None),
    ):
        bad_runtime = LLMRuntime(
            gemini_factory=lambda model=None: _FakeGemini(
                raise_on_structured=LLMRuntimeError(
                    grpc_status=grpc.StatusCode.INTERNAL,
                    http_status=500,
                    code="STRUCTURED_FAILED",
                    message="bad",
                )
            )
        )
        with pytest.raises(LLMRuntimeError) as exc2:
            await bad_runtime.structured_output(structured_request, validate_credentials=True)
    assert exc2.value.code == "STRUCTURED_FAILED"

    with patch(
        "services.llm_runtime.validate_invocation_metadata",
        AsyncMock(return_value=None),
    ):
        invalid_json_runtime = LLMRuntime(
            gemini_factory=lambda model=None: _FakeGemini(structured='{"value":')
        )
        with pytest.raises(LLMRuntimeError) as invalid_json_exc:
            await invalid_json_runtime.structured_output(
                structured_request, validate_credentials=True
            )
    assert invalid_json_exc.value.code == "STRUCTURED_FAILED"
    assert invalid_json_exc.value.http_status == 500

    with patch(
        "services.llm_runtime.validate_invocation_metadata",
        AsyncMock(return_value=None),
    ):
        unsupported_runtime = LLMRuntime(
            gemini_factory=lambda model=None: _FakeGemini(
                raise_on_structured=StructuredOutputRequiresNativeRuntimeError(
                    "structured output requires native Gemini runtime"
                )
            )
        )
        with pytest.raises(LLMRuntimeError) as native_runtime_exc:
            await unsupported_runtime.structured_output(
                structured_request, validate_credentials=True
            )
    assert native_runtime_exc.value.code == "STRUCTURED_OUTPUT_REQUIRES_NATIVE_RUNTIME"
    assert native_runtime_exc.value.http_status == 412

    with patch("services.llm_runtime.validate_invocation_metadata", AsyncMock(side_effect=PermissionError("nope"))):
        with pytest.raises(LLMRuntimeError) as denied_structured_exc:
            await structured_runtime.structured_output(structured_request, validate_credentials=True)
    assert denied_structured_exc.value.code == "UNAUTHORIZED"


@pytest.mark.asyncio
async def test_generate_stream_threads_runtime_controls(monkeypatch):
    limited = _LimitedGemini(chunks=["alpha", "beta"])
    runtime = LLMRuntime(gemini_factory=lambda model=None: limited)
    request = SimpleNamespace(
        prompt="hello",
        model="",
        temperature=0.5,
        max_tokens=16,
        metadata={
            "trace_id": "trace-stream-compat",
            "thinking_budget": "0",
            "service_tier": "standard",
            "latency_budget_ms": "12000",
        },
    )

    with patch("services.llm_runtime.validate_invocation_metadata", AsyncMock(return_value=None)):
        chunks = [
            chunk
            async for chunk in runtime.generate_stream(request, validate_credentials=True)
        ]

    assert [chunk.delta for chunk in chunks] == ["alpha", "beta", ""]
    assert limited.last_generate_stream_kwargs == {
        "prompt": "hello",
        "temperature": 0.5,
        "max_tokens": 16,
        "service_tier": "standard",
        "thinking_budget": 0,
        "latency_budget_ms": 12000,
        "trace_id": "trace-stream-compat",
    }


@pytest.mark.asyncio
async def test_structured_output_filters_kwargs_for_older_gemini_service(monkeypatch):
    limited = _LimitedGemini(structured='{"value":"compat"}')
    runtime = LLMRuntime(gemini_factory=lambda model=None: limited)
    request = SimpleNamespace(
        prompt="hello",
        model="gemini-2.5-flash",
        json_schema='{"type":"object"}',
        temperature=0.3,
        max_tokens=16,
        metadata={
            "trace_id": "trace-structured-compat",
            "thinking_budget": "-1",
            "service_tier": "priority",
            "retry_profile": "standard",
            "request_class": "structured_high_value",
            "latency_budget_ms": "30000",
        },
    )

    with patch("services.llm_runtime.validate_invocation_metadata", AsyncMock(return_value=None)):
        response = await runtime.structured_output(request, validate_credentials=True)

    assert response.json_result == '{"value":"compat"}'
    assert limited.last_generate_structured_kwargs == {
        "prompt": "hello",
        "json_schema": {"type": "object"},
        "temperature": 0.3,
        "max_tokens": 16,
        "service_tier": "priority",
        "thinking_budget": -1,
        "latency_budget_ms": 30000,
        "trace_id": "trace-structured-compat",
    }


@pytest.mark.asyncio
async def test_embed_and_embed_batch_cover_success_and_failure(monkeypatch):
    runtime = LLMRuntime(gemini_factory=lambda model=None: _FakeGemini())
    embed_request = SimpleNamespace(text="hello world", model="", task_type="", metadata={"trace_id": "trace-3"})

    with patch("services.llm_runtime.validate_invocation_metadata", AsyncMock(return_value=None)):
        embed_response = await runtime.embed(embed_request, validate_credentials=True)
    assert embed_response.model_used
    assert embed_response.embedding == pytest.approx([0.1, 0.2])

    with patch("services.llm_runtime.validate_invocation_metadata", AsyncMock(return_value=None)):
        batch_response = await runtime.embed_batch(
            SimpleNamespace(texts=["a", "b"], model="", task_type="", metadata={}),
            validate_credentials=True,
        )
    assert len(batch_response.embeddings) == 2

    with patch("services.llm_runtime.validate_invocation_metadata", AsyncMock(side_effect=PermissionError("nope"))):
        with pytest.raises(LLMRuntimeError) as denied_embed_exc:
            await runtime.embed(embed_request, validate_credentials=True)
    assert denied_embed_exc.value.code == "UNAUTHORIZED"

    with patch("services.llm_runtime.validate_invocation_metadata", AsyncMock(side_effect=PermissionError("nope"))):
        with pytest.raises(LLMRuntimeError) as denied_batch_exc:
            await runtime.embed_batch(
                SimpleNamespace(texts=["a"], model="", task_type="", metadata={}),
                validate_credentials=True,
            )
    assert denied_batch_exc.value.code == "UNAUTHORIZED"

    failing_runtime = LLMRuntime(
        gemini_factory=lambda model=None: _FakeGemini(
            raise_on_embed=RuntimeError("embed boom"),
            raise_on_embed_batch=RuntimeError("batch boom"),
        )
    )
    with patch("services.llm_runtime.validate_invocation_metadata", AsyncMock(return_value=None)):
        with pytest.raises(LLMRuntimeError) as embed_exc:
            await failing_runtime.embed(embed_request, validate_credentials=True)
    assert embed_exc.value.code == "EMBED_FAILED"
    assert embed_exc.value.details["code"] == "INTERNAL_ERROR"

    with patch("services.llm_runtime.validate_invocation_metadata", AsyncMock(return_value=None)):
        with pytest.raises(LLMRuntimeError) as batch_exc:
            await failing_runtime.embed_batch(
                SimpleNamespace(texts=["a"], model="", task_type="", metadata={}),
                validate_credentials=True,
            )
    assert batch_exc.value.code == "EMBED_BATCH_FAILED"
    assert batch_exc.value.details["code"] == "INTERNAL_ERROR"


@pytest.mark.asyncio
async def test_embed_filters_kwargs_for_older_gemini_service(monkeypatch):
    limited = _LimitedGemini()
    runtime = LLMRuntime(gemini_factory=lambda model=None: limited)
    request = SimpleNamespace(
        text="hello",
        model="embed-model",
        task_type="RETRIEVAL_QUERY",
        metadata={"trace_id": "trace-embed-filter", "latency_budget_ms": "9000"},
    )

    with patch("services.llm_runtime.validate_invocation_metadata", AsyncMock(return_value=None)):
        response = await runtime.embed(request, validate_credentials=True)

    assert response.embedding == pytest.approx([0.1, 0.2])
    assert limited.last_embed_kwargs == {
        "text": "hello",
        "model": "embed-model",
        "task_type": "RETRIEVAL_QUERY",
    }


@pytest.mark.asyncio
async def test_embed_batch_filters_kwargs_for_older_gemini_service(monkeypatch):
    limited = _LimitedGemini()
    runtime = LLMRuntime(gemini_factory=lambda model=None: limited)
    request = SimpleNamespace(
        texts=["a", "b"],
        model="embed-batch-model",
        task_type="RETRIEVAL_DOCUMENT",
        metadata={"trace_id": "trace-embed-batch-filter", "latency_budget_ms": "11000"},
    )

    with patch("services.llm_runtime.validate_invocation_metadata", AsyncMock(return_value=None)):
        response = await runtime.embed_batch(request, validate_credentials=True)

    assert len(response.embeddings) == 2
    assert limited.last_embed_batch_kwargs == {
        "texts": ["a", "b"],
        "model": "embed-batch-model",
        "task_type": "RETRIEVAL_DOCUMENT",
    }


@pytest.mark.asyncio
async def test_embed_threads_runtime_controls_when_supported(monkeypatch):
    class _EmbeddingAwareGemini(_FakeGemini):
        def __init__(self):
            super().__init__()
            self.last_embed_kwargs = None
            self.last_embed_batch_kwargs = None

        async def embed(
            self,
            *,
            text,
            model,
            task_type,
            latency_budget_ms=None,
            trace_id=None,
        ):
            self.last_embed_kwargs = {
                "text": text,
                "model": model,
                "task_type": task_type,
                "latency_budget_ms": latency_budget_ms,
                "trace_id": trace_id,
            }
            return self.embedding

        async def embed_batch(
            self,
            *,
            texts,
            model,
            task_type,
            latency_budget_ms=None,
            trace_id=None,
        ):
            self.last_embed_batch_kwargs = {
                "texts": texts,
                "model": model,
                "task_type": task_type,
                "latency_budget_ms": latency_budget_ms,
                "trace_id": trace_id,
            }
            return self.embeddings

    aware = _EmbeddingAwareGemini()
    runtime = LLMRuntime(gemini_factory=lambda model=None: aware)
    embed_request = SimpleNamespace(
        text="hello",
        model="embed-model",
        task_type="RETRIEVAL_QUERY",
        metadata={"trace_id": "trace-embed-aware", "latency_budget_ms": "12000"},
    )
    batch_request = SimpleNamespace(
        texts=["a", "b"],
        model="embed-batch-model",
        task_type="RETRIEVAL_DOCUMENT",
        metadata={"trace_id": "trace-embed-batch-aware", "latency_budget_ms": "13000"},
    )

    with patch("services.llm_runtime.validate_invocation_metadata", AsyncMock(return_value=None)):
        await runtime.embed(embed_request, validate_credentials=True)
        await runtime.embed_batch(batch_request, validate_credentials=True)

    assert aware.last_embed_kwargs == {
        "text": "hello",
        "model": "embed-model",
        "task_type": "RETRIEVAL_QUERY",
        "latency_budget_ms": 12000,
        "trace_id": "trace-embed-aware",
    }
    assert aware.last_embed_batch_kwargs == {
        "texts": ["a", "b"],
        "model": "embed-batch-model",
        "task_type": "RETRIEVAL_DOCUMENT",
        "latency_budget_ms": 13000,
        "trace_id": "trace-embed-batch-aware",
    }


@pytest.mark.asyncio
async def test_generate_and_structured_timeout_errors_map_to_deadline_exceeded():
    generate_runtime = LLMRuntime(
        gemini_factory=lambda model=None: _FakeGemini(
            raise_on_text=asyncio.TimeoutError()
        )
    )
    request = SimpleNamespace(
        prompt="hello",
        model="",
        temperature=0.5,
        max_tokens=16,
        metadata={"trace_id": "trace-timeout", "latency_budget_ms": "12000"},
    )
    with patch("services.llm_runtime.validate_invocation_metadata", AsyncMock(return_value=None)):
        with pytest.raises(LLMRuntimeError) as generate_exc:
            await generate_runtime.generate(request, validate_credentials=True)
    assert generate_exc.value.grpc_status == grpc.StatusCode.DEADLINE_EXCEEDED
    assert generate_exc.value.http_status == 504
    assert generate_exc.value.code == "GENERATE_TIMEOUT"

    structured_runtime = LLMRuntime(
        gemini_factory=lambda model=None: _FakeGemini(
            raise_on_structured=asyncio.TimeoutError()
        )
    )
    structured_request = SimpleNamespace(
        prompt="hello",
        model="",
        json_schema='{"type":"object"}',
        temperature=0.3,
        max_tokens=16,
        metadata={"trace_id": "trace-structured-timeout", "latency_budget_ms": "18000"},
    )
    with patch("services.llm_runtime.validate_invocation_metadata", AsyncMock(return_value=None)):
        with pytest.raises(LLMRuntimeError) as structured_exc:
            await structured_runtime.structured_output(structured_request, validate_credentials=True)
    assert structured_exc.value.grpc_status == grpc.StatusCode.DEADLINE_EXCEEDED
    assert structured_exc.value.http_status == 504
    assert structured_exc.value.code == "STRUCTURED_TIMEOUT"


@pytest.mark.asyncio
async def test_embed_timeout_and_invalid_argument_are_typed():
    timeout_runtime = LLMRuntime(
        gemini_factory=lambda model=None: _FakeGemini(
            raise_on_embed=asyncio.TimeoutError()
        )
    )
    timeout_request = SimpleNamespace(
        text="hello",
        model="",
        task_type="RETRIEVAL_QUERY",
        metadata={"trace_id": "trace-embed-timeout", "latency_budget_ms": "9000"},
    )
    with patch("services.llm_runtime.validate_invocation_metadata", AsyncMock(return_value=None)):
        with pytest.raises(LLMRuntimeError) as timeout_exc:
            await timeout_runtime.embed(timeout_request, validate_credentials=True)
    assert timeout_exc.value.grpc_status == grpc.StatusCode.DEADLINE_EXCEEDED
    assert timeout_exc.value.http_status == 504
    assert timeout_exc.value.code == "EMBED_TIMEOUT"
    assert timeout_exc.value.details["code"] == "TIMEOUT"

    invalid_batch_runtime = LLMRuntime(
        gemini_factory=lambda model=None: _FakeGemini(
            raise_on_embed_batch=ValueError("bad embed batch request")
        )
    )
    invalid_batch_request = SimpleNamespace(
        texts=["a", "b"],
        model="",
        task_type="RETRIEVAL_DOCUMENT",
        metadata={"trace_id": "trace-embed-batch-invalid", "latency_budget_ms": "7000"},
    )
    with patch("services.llm_runtime.validate_invocation_metadata", AsyncMock(return_value=None)):
        with pytest.raises(LLMRuntimeError) as invalid_batch_exc:
            await invalid_batch_runtime.embed_batch(
                invalid_batch_request, validate_credentials=True
            )
    assert invalid_batch_exc.value.grpc_status == grpc.StatusCode.INVALID_ARGUMENT
    assert invalid_batch_exc.value.http_status == 400
    assert invalid_batch_exc.value.code == "INVALID_EMBED_BATCH_REQUEST"
    assert invalid_batch_exc.value.details["code"] == "INVALID_PROMPT"


@pytest.mark.asyncio
async def test_health_returns_diagnostics():
    runtime = LLMRuntime(gemini_factory=lambda model=None: _FakeGemini())
    with patch("services.llm_runtime.get_gemini_runtime_diagnostics", return_value={"ready": True, "detail": "", "status": "configured", "source": "env", "mode": "auto"}):
        response = await runtime.health()
    assert response.ok is True
    assert response.error == ""
