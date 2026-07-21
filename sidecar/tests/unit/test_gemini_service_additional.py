"""Additional tests for services/gemini_service.py branch coverage."""

from __future__ import annotations

import asyncio
import json
import sys
import types
from types import SimpleNamespace
from unittest.mock import AsyncMock, MagicMock, patch

import pytest
from pydantic import BaseModel

from services.gemini_service import (
    GEMINI_STANDARD_MODEL,
    GEMINI_EMBED_STANDARD_MODEL,
    GeminiService,
    _clear_sticky_embedding_model_override,
    _cosine_similarity,
    _gemini_runtime_source,
    _gemini_runtime_unavailable_detail,
    _is_google_auth_transport_error,
    _resolve_thinking_budget,
    _resolve_thinking_level,
    get_gemini_runtime_diagnostics,
    _normalize_service_tier,
    _service_tier_for_wisdev,
    _token_set,
    _vertex_location,
)

TEST_GEMINI_25_PRO_MODEL = "gemini-2.5-pro"
TEST_GEMINI_25_FLASH_LITE_MODEL = "gemini-2.5-flash-lite"
TEST_GEMINI_25_FLASH_MODEL = "gemini-2.5-flash"
TEST_GEMINI_3_FLASH_MODEL = "gemini-3-flash"
TEST_GEMINI_3_PRO_MODEL = "gemini-3-pro"


@pytest.fixture(autouse=True)
def reset_sticky_embedding_override():
    _clear_sticky_embedding_model_override()
    yield
    _clear_sticky_embedding_model_override()


class _ResponseModel(BaseModel):
    value: str


class _SDKStructuredEnvelope(BaseModel):
    sdk_http_response: dict[str, object] | None = None
    parsed: dict[str, object] | None = None
    text: str = ""


def _install_fake_google_modules(monkeypatch) -> None:
    genai_types = types.ModuleType("google.genai.types")

    class DummyGenerateContentConfig:
        def __init__(self, **kwargs):
            self.kwargs = kwargs

    class DummyThinkingConfig:
        def __init__(self, **kwargs):
            self.kwargs = kwargs

    class DummyHttpOptions:
        def __init__(self, **kwargs):
            self.kwargs = kwargs

    setattr(genai_types, "GenerateContentConfig", DummyGenerateContentConfig)
    setattr(genai_types, "ThinkingConfig", DummyThinkingConfig)
    setattr(genai_types, "HttpOptions", DummyHttpOptions)

    genai_module = types.ModuleType("google.genai")
    setattr(genai_module, "Client", MagicMock())
    setattr(genai_module, "types", genai_types)

    google_module = types.ModuleType("google")
    setattr(google_module, "genai", genai_module)

    monkeypatch.setitem(sys.modules, "google", google_module)
    monkeypatch.setitem(sys.modules, "google.genai", genai_module)
    monkeypatch.setitem(sys.modules, "google.genai.types", genai_types)


def _install_strict_google_modules(monkeypatch) -> None:
    genai_types = types.ModuleType("google.genai.types")

    class StrictGenerateContentConfig:
        def __init__(self, **kwargs):
            if "service_tier" in kwargs:
                raise TypeError("unexpected keyword argument 'service_tier'")
            self.kwargs = kwargs

    class DummyThinkingConfig:
        def __init__(self, **kwargs):
            self.kwargs = kwargs

    class DummyHttpOptions:
        def __init__(self, **kwargs):
            self.kwargs = kwargs

    setattr(genai_types, "GenerateContentConfig", StrictGenerateContentConfig)
    setattr(genai_types, "ThinkingConfig", DummyThinkingConfig)
    setattr(genai_types, "HttpOptions", DummyHttpOptions)

    genai_module = types.ModuleType("google.genai")
    setattr(genai_module, "Client", MagicMock())
    setattr(genai_module, "types", genai_types)

    google_module = types.ModuleType("google")
    setattr(google_module, "genai", genai_module)

    monkeypatch.setitem(sys.modules, "google", google_module)
    monkeypatch.setitem(sys.modules, "google.genai", genai_module)
    monkeypatch.setitem(sys.modules, "google.genai.types", genai_types)


def test_token_set_filters_short_tokens():
    assert _token_set("AI and machine-learning 12 are great") == {
        "and",
        "are",
        "great",
        "machine",
        "learning",
    }


@pytest.mark.parametrize(
    ("raw", "expected"),
    [
        (None, None),
        ("", None),
        ("flex", "flex"),
        ("auto", None),
        ("PRIORITY", "priority"),
    ],
)
def test_normalize_service_tier(raw, expected):
    assert _normalize_service_tier(raw) == expected


@pytest.mark.parametrize(
    ("mode", "interactive", "expected"),
    [
        ("yolo", False, "flex"),
        ("autonomous", False, "flex"),
        ("guided", False, "standard"),
        ("constraint", False, "standard"),
        ("other", False, None),
        ("any", True, "priority"),
    ],
)
def test_service_tier_for_wisdev(mode, interactive, expected):
    assert _service_tier_for_wisdev(mode, interactive=interactive) == expected


def test_resolve_thinking_budget_clamps_and_defaults_from_latency_budget():
    assert _resolve_thinking_budget(
        TEST_GEMINI_25_PRO_MODEL,
        0,
        latency_budget_ms=20_000,
        structured=True,
    ) == 128
    assert _resolve_thinking_budget(
        TEST_GEMINI_25_FLASH_LITE_MODEL,
        None,
        latency_budget_ms=10_000,
        structured=True,
    ) == 0
    assert _resolve_thinking_budget(
        TEST_GEMINI_25_FLASH_MODEL,
        99_999,
        latency_budget_ms=40_000,
        structured=True,
    ) == 24_576
    assert _resolve_thinking_budget(
        TEST_GEMINI_25_FLASH_MODEL,
        -1,
        latency_budget_ms=40_000,
        structured=True,
        max_output_tokens=64,
    ) == 0
    assert _resolve_thinking_budget(
        TEST_GEMINI_25_FLASH_MODEL,
        -1,
        latency_budget_ms=40_000,
        structured=True,
        max_output_tokens=512,
    ) == -1
    assert _resolve_thinking_budget(
        TEST_GEMINI_25_FLASH_MODEL,
        1024,
        latency_budget_ms=40_000,
        structured=True,
        max_output_tokens=64,
    ) == 1024


def test_resolve_thinking_budget_keeps_lite_models_non_thinking_by_default():
    # Flash-lite ships with thinking disabled by default (Gemini docs); a high
    # latency budget must not silently enable dynamic thinking on lite models.
    assert _resolve_thinking_budget(
        TEST_GEMINI_25_FLASH_LITE_MODEL,
        None,
        latency_budget_ms=45_000,
        structured=True,
    ) == 0
    assert _resolve_thinking_budget(
        TEST_GEMINI_25_FLASH_LITE_MODEL,
        -1,
        latency_budget_ms=45_000,
        structured=True,
        max_output_tokens=2048,
    ) == 0
    # An explicit positive budget still enables thinking, clamped to limits.
    assert _resolve_thinking_budget(
        TEST_GEMINI_25_FLASH_LITE_MODEL,
        2_048,
        latency_budget_ms=45_000,
        structured=True,
    ) == 2_048
    # Non-lite flash keeps the latency-driven dynamic default.
    assert _resolve_thinking_budget(
        TEST_GEMINI_25_FLASH_MODEL,
        None,
        latency_budget_ms=45_000,
        structured=True,
        max_output_tokens=2048,
    ) == -1


def test_resolve_thinking_level_keeps_lite_models_minimal_by_default():
    lite_gemini3_model = "gemini-3.1-flash-lite"
    assert (
        _resolve_thinking_level(
            lite_gemini3_model,
            None,
            latency_budget_ms=45_000,
            structured=True,
            max_output_tokens=2048,
        )
        == "MINIMAL"
    )
    assert (
        _resolve_thinking_level(
            lite_gemini3_model,
            -1,
            latency_budget_ms=45_000,
            structured=False,
            max_output_tokens=2048,
        )
        == "MINIMAL"
    )
    # An explicit positive budget still maps to the matching level.
    assert (
        _resolve_thinking_level(
            lite_gemini3_model,
            1024,
            latency_budget_ms=45_000,
            structured=True,
            max_output_tokens=2048,
        )
        == "LOW"
    )


def test_resolve_thinking_level_minimizes_tiny_structured_dynamic_budget():
    assert (
        _resolve_thinking_level(
            TEST_GEMINI_3_FLASH_MODEL,
            -1,
            latency_budget_ms=40_000,
            structured=True,
            max_output_tokens=64,
        )
        == "MINIMAL"
    )
    assert (
        _resolve_thinking_level(
            TEST_GEMINI_3_FLASH_MODEL,
            None,
            latency_budget_ms=40_000,
            structured=True,
            max_output_tokens=64,
        )
        == "MINIMAL"
    )
    assert (
        _resolve_thinking_level(
            TEST_GEMINI_3_FLASH_MODEL,
            -1,
            latency_budget_ms=40_000,
            structured=True,
            max_output_tokens=512,
        )
        == "HIGH"
    )
    assert (
        _resolve_thinking_level(
            TEST_GEMINI_3_FLASH_MODEL,
            1024,
            latency_budget_ms=40_000,
            structured=True,
            max_output_tokens=64,
        )
        == "LOW"
    )
    assert (
        _resolve_thinking_level(
            TEST_GEMINI_3_FLASH_MODEL,
            -1,
            latency_budget_ms=40_000,
            structured=False,
            max_output_tokens=64,
        )
        == "HIGH"
    )


def test_cosine_similarity_mismatched_lengths():
    assert _cosine_similarity([1.0, 0.0], [1.0, 0.0, 1.0]) == 0.0


@pytest.mark.asyncio
async def test_generate_native_structured_retries_and_succeeds(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = MagicMock()

    runner = AsyncMock(
        side_effect=[
            RuntimeError("network"),
            SimpleNamespace(text='{"value":"cached"}'),
        ]
    )

    with patch("services.gemini_service._run_sync_with_timeout", runner):
        with patch("services.gemini_service.asyncio.sleep", AsyncMock()):
            result = await svc._generate_native_structured("prompt", _ResponseModel, 0.1, 16, 0.2, None)

    assert isinstance(result, _ResponseModel)
    assert result.value == "cached"
    assert runner.await_count == 2


@pytest.mark.asyncio
async def test_generate_native_structured_retries_until_exhausted(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = MagicMock()

    with patch(
        "services.gemini_service._run_sync_with_timeout",
        AsyncMock(side_effect=[RuntimeError("x"), RuntimeError("y"), RuntimeError("z")]),
    ):
        with patch("services.gemini_service.asyncio.sleep", AsyncMock()):
            with pytest.raises(RuntimeError):
                await svc._generate_native_structured("prompt", _ResponseModel, 0.1, 16, 0.2, None)


@pytest.mark.asyncio
async def test_generate_text_native_retries_on_empty_value(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = MagicMock()

    runner = AsyncMock(
        side_effect=[
            SimpleNamespace(text=""),
            SimpleNamespace(text="generated"),
        ]
    )

    with patch("services.gemini_service._run_sync_with_timeout", runner):
        with patch("services.gemini_service.asyncio.sleep", AsyncMock()):
            text = await svc._generate_text_native("prompt", 0.5, 32, 0.1, None)

    assert text == "generated"


@pytest.mark.asyncio
async def test_generate_text_with_usage_reads_native_usage_metadata(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = MagicMock()

    runner = AsyncMock(
        return_value=SimpleNamespace(
            text="generated",
            usage_metadata=SimpleNamespace(
                prompt_token_count=29,
                candidates_token_count=8,
            ),
        )
    )

    with patch("services.gemini_service._run_sync_with_timeout", runner):
        result = await svc.generate_text_with_usage("prompt", 0.5, 32, 0.1, None)

    assert result.text == "generated"
    assert result.input_tokens == 29
    assert result.output_tokens == 8


@pytest.mark.asyncio
async def test_generate_structured_retries_on_empty_json_text(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = MagicMock()

    runner = AsyncMock(
        side_effect=[
            SimpleNamespace(text=""),
            SimpleNamespace(text='{"value":"ok"}'),
        ]
    )

    with patch("services.gemini_service._run_sync_with_timeout", runner):
        with patch("services.gemini_service.asyncio.sleep", AsyncMock()):
            result = await svc.generate_structured("prompt", {"type": "object"})

    assert result == '{"value":"ok"}'


@pytest.mark.asyncio
async def test_generate_structured_with_usage_reads_native_usage_metadata(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = MagicMock()

    runner = AsyncMock(
        return_value=SimpleNamespace(
            text='{"value":"ok"}',
            usage_metadata=SimpleNamespace(
                prompt_token_count=31,
                candidates_token_count=9,
            ),
        )
    )

    with patch("services.gemini_service._run_sync_with_timeout", runner):
        result = await svc.generate_structured_with_usage("prompt", {"type": "object"})

    assert result.text == '{"value":"ok"}'
    assert result.input_tokens == 31
    assert result.output_tokens == 9


@pytest.mark.asyncio
async def test_generate_structured_retries_on_invalid_json_text(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = MagicMock()

    runner = AsyncMock(
        side_effect=[
            SimpleNamespace(text='{"value":'),
            SimpleNamespace(text='{"value":"ok"}'),
        ]
    )

    with patch("services.gemini_service._run_sync_with_timeout", runner):
        with patch("services.gemini_service.asyncio.sleep", AsyncMock()):
            result = await svc.generate_structured("prompt", {"type": "object"})

    assert result == '{"value":"ok"}'


@pytest.mark.asyncio
async def test_generate_structured_rejects_fenced_json_text(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = MagicMock()

    runner = AsyncMock(
        side_effect=[
            SimpleNamespace(text='```json\n{"value":"bad"}\n```'),
            SimpleNamespace(text='{"value":"ok"}'),
        ]
    )

    with patch("services.gemini_service._run_sync_with_timeout", runner):
        with patch("services.gemini_service.asyncio.sleep", AsyncMock()):
            result = await svc.generate_structured("prompt", {"type": "object"})

    assert result == '{"value":"ok"}'
    assert runner.await_count == 2


@pytest.mark.asyncio
async def test_generate_structured_retries_truncated_json_text_without_repair(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = MagicMock()

    runner = AsyncMock(
        side_effect=[
            SimpleNamespace(text='{"value":"bad"'),
            SimpleNamespace(text='{"value":"ok"}'),
        ]
    )

    with patch("services.gemini_service._run_sync_with_timeout", runner):
        with patch("services.gemini_service.asyncio.sleep", AsyncMock()):
            result = await svc.generate_structured("prompt", {"type": "object"})

    assert result == '{"value":"ok"}'
    assert runner.await_count == 2


@pytest.mark.asyncio
async def test_generate_structured_uses_sdk_parsed_payload_when_text_empty(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = MagicMock()

    runner = AsyncMock(return_value=SimpleNamespace(text="", parsed={"value": "ok"}))

    with patch("services.gemini_service._run_sync_with_timeout", runner):
        result = await svc.generate_structured("prompt", {"type": "object"})

    assert result == '{"value":"ok"}'
    assert runner.await_count == 1


@pytest.mark.asyncio
async def test_generate_structured_prefers_sdk_parsed_payload_over_envelope(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = MagicMock()

    runner = AsyncMock(
        return_value=_SDKStructuredEnvelope(
            sdk_http_response={"headers": {"content-type": "application/json"}},
            parsed={"value": "parsed"},
        )
    )

    with patch("services.gemini_service._run_sync_with_timeout", runner):
        result = await svc.generate_structured("prompt", {"type": "object"})

    assert result == '{"value":"parsed"}'
    assert "sdk_http_response" not in result
    assert runner.await_count == 1


@pytest.mark.asyncio
async def test_generate_structured_retries_invalid_sdk_envelope_without_emitting_it(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = MagicMock()

    runner = AsyncMock(
        side_effect=[
            _SDKStructuredEnvelope(
                sdk_http_response={"headers": {"content-type": "application/json"}},
                parsed=None,
                text="Here is the",
            ),
            SimpleNamespace(text='{"value":"ok"}'),
        ]
    )

    with patch("services.gemini_service._run_sync_with_timeout", runner):
        with patch("services.gemini_service.asyncio.sleep", AsyncMock()):
            result = await svc.generate_structured("prompt", {"type": "object"})

    assert result == '{"value":"ok"}'
    assert "sdk_http_response" not in result
    assert runner.await_count == 2


@pytest.mark.asyncio
async def test_generate_structured_uses_candidate_part_text_when_text_empty(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = MagicMock()

    candidate = SimpleNamespace(
        content=SimpleNamespace(parts=[SimpleNamespace(text='{"value":"candidate"}')])
    )
    runner = AsyncMock(return_value=SimpleNamespace(text="", candidates=[candidate]))

    with patch("services.gemini_service._run_sync_with_timeout", runner):
        result = await svc.generate_structured("prompt", {"type": "object"})

    assert result == '{"value":"candidate"}'
    assert runner.await_count == 1


@pytest.mark.asyncio
async def test_generate_structured_ignores_incompatible_thinking_config(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    class DummyGenerateContentConfig:
        def __init__(self, **kwargs):
            self.kwargs = kwargs

    class StrictThinkingConfig:
        def __init__(self, **kwargs):
            raise TypeError("unexpected keyword argument 'thinking_level'")

    sys.modules["google.genai.types"].GenerateContentConfig = DummyGenerateContentConfig
    sys.modules["google.genai.types"].ThinkingConfig = StrictThinkingConfig

    captured: dict[str, object] = {}

    def fake_generate_content(*, model, contents, config):
        captured["model"] = model
        captured["contents"] = contents
        captured["config"] = config
        return SimpleNamespace(text='{"value":"ok"}')

    svc = GeminiService.__new__(GeminiService)
    svc.model = TEST_GEMINI_3_PRO_MODEL
    svc._client = SimpleNamespace(
        models=SimpleNamespace(generate_content=MagicMock(side_effect=fake_generate_content))
    )

    with patch(
        "services.gemini_service._run_sync_with_timeout",
        AsyncMock(side_effect=lambda _timeout, func, *args, **kwargs: func(*args, **kwargs)),
    ):
        result = await svc.generate_structured(
            "prompt",
            {"type": "object"},
            thinking_budget=128,
        )

    assert result == '{"value":"ok"}'
    assert captured["model"] == TEST_GEMINI_3_PRO_MODEL
    assert captured["contents"] == "prompt"
    assert "thinking_config" not in getattr(captured["config"], "kwargs", {})


@pytest.mark.asyncio
async def test_generate_native_structured_retries_truncated_native_json_text(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = MagicMock()

    runner = AsyncMock(
        side_effect=[
            SimpleNamespace(text='{"value":"bad"'),
            SimpleNamespace(text='{"value":"ok"}'),
        ]
    )

    with patch("services.gemini_service._run_sync_with_timeout", runner):
        with patch("services.gemini_service.asyncio.sleep", AsyncMock()):
            result = await svc._generate_native_structured(
                "prompt",
                _ResponseModel,
                0.1,
                16,
                0.2,
                None,
            )

    assert isinstance(result, _ResponseModel)
    assert result.value == "ok"
    assert runner.await_count == 2


@pytest.mark.asyncio
async def test_generate_diverse_hypotheses_uses_embeddings_when_available(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = MagicMock()

    model_payload = SimpleNamespace(hypotheses=["alpha variant", "alpha alternate", "beta route"])

    with patch.object(svc, "generate_json", AsyncMock(return_value=model_payload)) as generate_json_mock:
        with patch.object(
            svc,
            "embed_batch",
            AsyncMock(return_value=[[1.0, 0.0], [0.99, 0.02], [0.0, 1.0]]),
        ):
            hypotheses = await svc.generate_diverse_hypotheses(
                "query",
                n=2,
                min_cosine_distance=0.2,
            )

    assert len(hypotheses) == 2
    assert "alpha variant" in hypotheses
    assert "beta route" in hypotheses
    prompt = generate_json_mock.await_args.args[0]
    assert "Use the provided structured response schema exactly." in prompt
    assert "Populate the hypotheses field with 6 strings." in prompt
    assert "Return a JSON object" not in prompt


@pytest.mark.asyncio
async def test_generate_diverse_hypotheses_falls_back_to_token_filter(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = MagicMock()

    model_payload = SimpleNamespace(
        hypotheses=[
            "deep learning with graphs",
            "deep-learning with graphs",
            "causal causal inference",
        ]
    )

    with patch.object(svc, "generate_json", AsyncMock(return_value=model_payload)):
        with patch.object(svc, "embed_batch", AsyncMock(side_effect=RuntimeError("embedding unavailable"))):
            hypotheses = await svc.generate_diverse_hypotheses(
                "query",
                n=2,
                min_cosine_distance=0.45,
            )

    assert len(hypotheses) == 2
    assert hypotheses[0] == "deep learning with graphs"
    assert hypotheses[1] == "causal causal inference"


@pytest.mark.asyncio
async def test_generate_diverse_hypotheses_returns_empty_when_llm_fails(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = MagicMock()

    with patch.object(svc, "generate_json", AsyncMock(side_effect=RuntimeError("boom"))):
        hypotheses = await svc.generate_diverse_hypotheses("query")

    assert hypotheses == []


def test_gemini_runtime_source_prefers_api_key(monkeypatch):
    monkeypatch.setenv("GOOGLE_API_KEY", "test-key")
    monkeypatch.delenv("GOOGLE_CLOUD_PROJECT", raising=False)
    monkeypatch.setenv("VERTEX_PROXY_URL", "")

    assert _gemini_runtime_source() == "env"


def test_gemini_runtime_source_uses_vertex_project_when_no_key(monkeypatch):
    monkeypatch.delenv("GOOGLE_API_KEY", raising=False)
    monkeypatch.setenv("GOOGLE_CLOUD_PROJECT", "test-project")
    monkeypatch.setenv("VERTEX_PROXY_URL", "")

    assert _gemini_runtime_source() == "vertex_project"


def test_gemini_runtime_source_uses_vertex_proxy_when_available(monkeypatch):
    monkeypatch.delenv("GOOGLE_API_KEY", raising=False)
    monkeypatch.delenv("GOOGLE_CLOUD_PROJECT", raising=False)
    monkeypatch.setenv("VERTEX_PROXY_URL", "https://proxy.example")

    with patch("services.gemini_service.get_project_id_resolution", return_value={"projectId": "", "projectConfigured": False, "projectSource": "none"}):
        assert _gemini_runtime_source() == "vertex_proxy"


def test_gemini_runtime_source_none_when_no_runtime(monkeypatch):
    monkeypatch.delenv("GOOGLE_API_KEY", raising=False)
    monkeypatch.delenv("GOOGLE_CLOUD_PROJECT", raising=False)
    monkeypatch.setenv("VERTEX_PROXY_URL", "")

    with patch("services.gemini_service.get_project_id_resolution", return_value={"projectId": "", "projectConfigured": False, "projectSource": "none"}):
        assert _gemini_runtime_source() == "none"


@pytest.mark.parametrize(
    ("source", "mode", "project_configured", "proxy_configured", "expected_fragment"),
    [
        (
            "env",
            "auto",
            False,
            False,
            "Gemini API key is configured from env",
        ),
        (
            "vertex_project",
            "auto",
            True,
            True,
            "proxy fallback was not usable",
        ),
        (
            "vertex_project",
            "auto",
            True,
            False,
            "native Gemini client is unavailable.",
        ),
        (
            "vertex_proxy",
            "auto",
            False,
            True,
            "GEMINI_RUNTIME_MODE=vertex_proxy requires VERTEX_PROXY_URL.",
        ),
        (
            "none",
            "native",
            False,
            False,
            "GEMINI_RUNTIME_MODE=native requires a working GOOGLE_API_KEY",
        ),
        (
            "none",
            "auto",
            False,
            False,
            "Set GOOGLE_API_KEY, GOOGLE_CLOUD_PROJECT, or VERTEX_PROXY_URL.",
        ),
    ],
)
def test_gemini_runtime_unavailable_detail_messages(
    source,
    mode,
    project_configured,
    proxy_configured,
    expected_fragment,
):
    assert (
        expected_fragment
        in _gemini_runtime_unavailable_detail(
            source,
            mode,
            project_configured,
            proxy_configured,
        )
    )


def test_gemini_runtime_diagnostics_missing_configuration(monkeypatch):
    monkeypatch.delenv("GOOGLE_API_KEY", raising=False)
    monkeypatch.delenv("GOOGLE_CLOUD_PROJECT", raising=False)
    monkeypatch.setenv("VERTEX_PROXY_URL", "")
    monkeypatch.setenv("GEMINI_RUNTIME_MODE", "auto")

    with patch("services.gemini_service.get_project_id_resolution", return_value={"projectId": "", "projectConfigured": False, "projectSource": "none"}):
        diagnostics = get_gemini_runtime_diagnostics()

    assert diagnostics["status"] == "missing"
    assert diagnostics["source"] == "none"
    assert diagnostics["ready"] is False
    assert "Set GOOGLE_API_KEY, GOOGLE_CLOUD_PROJECT, or VERTEX_PROXY_URL." in diagnostics["detail"]


def test_gemini_runtime_unavailable_detail_configured_but_unavailable():
    detail = _gemini_runtime_unavailable_detail("none", "auto", True, False)
    assert detail == "Gemini runtime is configured but unavailable."


def test_cosine_similarity_zero_norm_returns_zero():
    assert _cosine_similarity([0.0, 0.0], [1.0, 0.0]) == 0.0


def test_build_client_returns_native_client_for_api_key(monkeypatch):
    _install_fake_google_modules(monkeypatch)
    from services import gemini_service as gmod

    fake_client = MagicMock(name="client")
    sys.modules["google.genai"].Client.return_value = fake_client
    monkeypatch.setenv("GOOGLE_API_KEY", "key")
    monkeypatch.delenv("GOOGLE_CLOUD_PROJECT", raising=False)
    monkeypatch.delenv("VERTEX_PROXY_URL", raising=False)
    monkeypatch.setenv("GEMINI_RUNTIME_MODE", "auto")

    with patch("services.gemini_service.get_project_id_resolution", return_value={"projectId": "", "projectConfigured": False, "projectSource": "none"}):
        svc = gmod.GeminiService(model="test-model")
    assert svc._client is fake_client
    _, kwargs = sys.modules["google.genai"].Client.call_args
    assert kwargs["api_key"] == "key"
    assert kwargs["http_options"].kwargs == {"api_version": "v1"}


def test_build_client_returns_native_client_for_vertex_project(monkeypatch):
    _install_fake_google_modules(monkeypatch)
    from services import gemini_service as gmod

    fake_client = MagicMock(name="client")
    sys.modules["google.genai"].Client.return_value = fake_client
    monkeypatch.delenv("GOOGLE_API_KEY", raising=False)
    monkeypatch.setenv("GOOGLE_CLOUD_PROJECT", "project-1")
    monkeypatch.delenv("VERTEX_PROXY_URL", raising=False)
    monkeypatch.setenv("GEMINI_RUNTIME_MODE", "auto")

    svc = gmod.GeminiService(model="test-model")
    assert svc._client is fake_client
    _, kwargs = sys.modules["google.genai"].Client.call_args
    assert kwargs["vertexai"] is True
    assert kwargs["project"] == "project-1"
    assert kwargs["http_options"].kwargs == {"api_version": "v1"}


def test_build_client_uses_configured_vertex_location(monkeypatch):
    _install_fake_google_modules(monkeypatch)
    from services import gemini_service as gmod

    fake_client = MagicMock(name="client")
    sys.modules["google.genai"].Client.return_value = fake_client
    monkeypatch.delenv("GOOGLE_API_KEY", raising=False)
    monkeypatch.setenv("GOOGLE_CLOUD_PROJECT", "project-1")
    monkeypatch.setenv("GOOGLE_CLOUD_REGION", "europe-west4")
    monkeypatch.delenv("GOOGLE_CLOUD_LOCATION", raising=False)
    monkeypatch.delenv("VERTEX_PROXY_URL", raising=False)
    monkeypatch.setenv("GEMINI_RUNTIME_MODE", "auto")

    svc = gmod.GeminiService(model="test-model")
    assert svc._client is fake_client
    _, kwargs = sys.modules["google.genai"].Client.call_args
    assert kwargs["location"] == "europe-west4"
    assert kwargs["http_options"].kwargs == {"api_version": "v1"}


def test_vertex_location_uses_model_config_when_env_unset(monkeypatch):
    monkeypatch.delenv("GOOGLE_CLOUD_LOCATION", raising=False)
    monkeypatch.delenv("GOOGLE_CLOUD_REGION", raising=False)

    with patch("services.gemini_service.get_model_location", return_value="global"):
        assert _vertex_location() == "global"


def test_vertex_location_uses_embedding_config_separately(monkeypatch):
    monkeypatch.setenv("GOOGLE_CLOUD_LOCATION", "us")
    monkeypatch.delenv("GOOGLE_CLOUD_EMBEDDING_LOCATION", raising=False)
    monkeypatch.delenv("GOOGLE_CLOUD_EMBEDDING_REGION", raising=False)

    assert _vertex_location("embedding") == "global"


def test_build_client_returns_none_when_client_init_fails_without_proxy(monkeypatch):
    _install_fake_google_modules(monkeypatch)
    from services import gemini_service as gmod

    sys.modules["google.genai"].Client.side_effect = RuntimeError("boom")
    monkeypatch.setenv("GOOGLE_API_KEY", "key")
    monkeypatch.delenv("VERTEX_PROXY_URL", raising=False)
    monkeypatch.setenv("GEMINI_RUNTIME_MODE", "auto")

    with patch("services.gemini_service.get_project_id_resolution", return_value={"projectId": "", "projectConfigured": False, "projectSource": "none"}):
        svc = gmod.GeminiService(model="test-model")
    assert svc._client is None


def test_build_client_returns_none_when_client_init_fails_with_proxy(monkeypatch):
    _install_fake_google_modules(monkeypatch)
    from services import gemini_service as gmod

    sys.modules["google.genai"].Client.side_effect = RuntimeError("boom")
    monkeypatch.setenv("GOOGLE_API_KEY", "key")
    monkeypatch.setenv("VERTEX_PROXY_URL", "https://proxy.example")
    monkeypatch.setenv("GEMINI_RUNTIME_MODE", "auto")

    with patch("services.gemini_service.get_project_id_resolution", return_value={"projectId": "", "projectConfigured": False, "projectSource": "none"}):
        svc = gmod.GeminiService(model="test-model")
    assert svc._client is None


def test_gemini_runtime_diagnostics_reuses_cached_service(monkeypatch):
    from services import gemini_service as gmod

    class _FakeService:
        def __init__(self):
            self.ready_checks = 0

        def is_ready(self):
            self.ready_checks += 1
            return True

    created: list[_FakeService] = []

    def _factory():
        service = _FakeService()
        created.append(service)
        return service

    monkeypatch.setenv("GOOGLE_API_KEY", "key")
    monkeypatch.setenv("GEMINI_RUNTIME_MODE", "auto")
    monkeypatch.delenv("GOOGLE_CLOUD_PROJECT", raising=False)
    monkeypatch.delenv("VERTEX_PROXY_URL", raising=False)
    monkeypatch.setattr(gmod, "_runtime_service_cache", None)
    monkeypatch.setattr(gmod, "_runtime_service_cache_key", None)
    monkeypatch.setattr(gmod, "GeminiService", _factory)

    first = gmod.get_gemini_runtime_diagnostics()
    second = gmod.get_gemini_runtime_diagnostics()

    assert first["ready"] is True
    assert second["ready"] is True
    assert len(created) == 1
    assert created[0].ready_checks == 2


@pytest.mark.asyncio
async def test_generate_json_uses_native_path_when_client_present():
    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = MagicMock()

    expected = _ResponseModel(value="native")
    with patch.object(svc, "_generate_native_structured", AsyncMock(return_value=expected)):
        result = await svc.generate_json("prompt", _ResponseModel)

    assert result == expected


@pytest.mark.asyncio
async def test_generate_with_thinking_omits_unsupported_native_service_tier(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = MagicMock()

    runner = AsyncMock(return_value=SimpleNamespace(text='{"value":"thought"}'))
    with patch("services.gemini_service._run_sync_with_timeout", runner):
        result = await svc.generate_with_thinking("prompt", _ResponseModel, service_tier="flex")

    assert result.value == "thought"
    assert "service_tier" not in runner.await_args.kwargs["config"].kwargs


@pytest.mark.asyncio
async def test_generate_with_thinking_passes_thinking_level_for_gemini3(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    svc = GeminiService.__new__(GeminiService)
    svc.model = TEST_GEMINI_3_PRO_MODEL
    svc._client = MagicMock()

    runner = AsyncMock(return_value=SimpleNamespace(text='{"value":"thought"}'))
    with patch("services.gemini_service._run_sync_with_timeout", runner):
        result = await svc.generate_with_thinking(
            "prompt",
            _ResponseModel,
            thinking_budget=2048,
        )

    assert result.value == "thought"
    thinking_config = runner.await_args.kwargs["config"].kwargs["thinking_config"]
    assert thinking_config.kwargs["thinking_level"] == "MEDIUM"
    assert "thinking_budget" not in thinking_config.kwargs


@pytest.mark.asyncio
async def test_generate_stream_without_client_splits_text_chunks():
    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = None

    with patch.object(svc, "generate_text", AsyncMock(return_value="a\n\nb")):
        chunks = [chunk async for chunk in svc.generate_stream("prompt")]

    assert chunks == ["a\n\n", "b\n\n"]


@pytest.mark.asyncio
async def test_generate_stream_yields_native_chunks(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    async def _gen():
        yield SimpleNamespace(text="first")
        yield SimpleNamespace(text="")
        yield SimpleNamespace(text="second")

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = SimpleNamespace(
        aio=SimpleNamespace(
            models=SimpleNamespace(generate_content_stream=AsyncMock(return_value=_gen()))
        )
    )

    chunks = [chunk async for chunk in svc.generate_stream("prompt")]
    assert chunks == ["first", "second"]


@pytest.mark.asyncio
async def test_generate_stream_raises_when_native_stream_fails(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = SimpleNamespace(
        aio=SimpleNamespace(
            models=SimpleNamespace(generate_content_stream=AsyncMock(side_effect=RuntimeError("stream boom")))
        )
    )

    with pytest.raises(RuntimeError, match="stream boom"):
        async for _ in svc.generate_stream("prompt"):
            pass


@pytest.mark.asyncio
async def test_generate_structured_accepts_metadata_but_omits_unsupported_native_service_tier(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = MagicMock()

    runner = AsyncMock(return_value=SimpleNamespace(text='{"ok":true}'))
    with patch("services.gemini_service._run_sync_with_timeout", runner):
        result = await svc.generate_structured(
            "prompt",
            {"type": "object"},
            service_tier="flex",
            retry_profile="standard",
            request_class="structured_high_value",
        )

    assert result == '{"ok":true}'
    assert "service_tier" not in runner.await_args.kwargs["config"].kwargs


@pytest.mark.asyncio
async def test_generate_structured_strips_service_tier_when_sdk_constructor_rejects_it(monkeypatch):
    _install_strict_google_modules(monkeypatch)

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = MagicMock()

    runner = AsyncMock(return_value=SimpleNamespace(text='{"ok":true}'))
    with patch("services.gemini_service._run_sync_with_timeout", runner):
        with patch("services.gemini_service._native_generate_config_supports", return_value=True):
            result = await svc.generate_structured(
                "prompt",
                {"type": "object"},
                service_tier="flex",
            )

    assert result == '{"ok":true}'
    assert "service_tier" not in runner.await_args.kwargs["config"].kwargs


@pytest.mark.asyncio
async def test_generate_structured_passes_thinking_budget_for_gemini_25(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    svc = GeminiService.__new__(GeminiService)
    svc.model = TEST_GEMINI_25_FLASH_MODEL
    svc._client = MagicMock()

    runner = AsyncMock(return_value=SimpleNamespace(text='{"ok":true}'))
    with patch("services.gemini_service._run_sync_with_timeout", runner):
        result = await svc.generate_structured(
            "prompt",
            {"type": "object"},
            thinking_budget=-1,
        )

    assert result == '{"ok":true}'
    assert runner.await_args.kwargs["config"].kwargs["thinking_config"].kwargs["thinking_budget"] == -1


@pytest.mark.asyncio
async def test_generate_structured_disables_auto_thinking_for_tiny_output_budget(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    svc = GeminiService.__new__(GeminiService)
    svc.model = TEST_GEMINI_25_FLASH_MODEL
    svc._client = MagicMock()

    runner = AsyncMock(return_value=SimpleNamespace(text='{"ok":true}'))
    with patch("services.gemini_service._run_sync_with_timeout", runner):
        result = await svc.generate_structured(
            "prompt",
            {"type": "object"},
            max_tokens=64,
            thinking_budget=-1,
        )

    assert result == '{"ok":true}'
    thinking_config = runner.await_args.kwargs["config"].kwargs["thinking_config"]
    assert thinking_config.kwargs["thinking_budget"] == 0


@pytest.mark.asyncio
async def test_generate_structured_minimizes_gemini3_dynamic_thinking_for_tiny_output_budget(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    svc = GeminiService.__new__(GeminiService)
    svc.model = TEST_GEMINI_3_FLASH_MODEL
    svc._client = MagicMock()

    runner = AsyncMock(return_value=SimpleNamespace(text='{"ok":true}'))
    with patch("services.gemini_service._run_sync_with_timeout", runner):
        result = await svc.generate_structured(
            "prompt",
            {"type": "object"},
            max_tokens=64,
            thinking_budget=-1,
        )

    assert result == '{"ok":true}'
    thinking_config = runner.await_args.kwargs["config"].kwargs["thinking_config"]
    assert thinking_config.kwargs["thinking_level"] == "MINIMAL"
    assert "thinking_budget" not in thinking_config.kwargs


@pytest.mark.asyncio
async def test_generate_structured_raises_on_last_failure(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = MagicMock()

    with patch("services.gemini_service._run_sync_with_timeout", AsyncMock(side_effect=RuntimeError("boom"))):
        with patch("services.gemini_service.asyncio.sleep", AsyncMock()):
            with pytest.raises(RuntimeError, match="boom"):
                await svc.generate_structured("prompt", {"type": "object"})


@pytest.mark.asyncio
async def test_generate_structured_fast_fails_google_auth_transport(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    class TransportError(RuntimeError):
        pass

    TransportError.__module__ = "google.auth.exceptions"
    exc = TransportError(
        "HTTPSConnectionPool(host='oauth2.googleapis.com', port=443): "
        "Max retries exceeded with url: /token "
        "(Caused by SSLEOFError(8, 'UNEXPECTED_EOF_WHILE_READING'))"
    )

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = MagicMock()

    assert _is_google_auth_transport_error(exc)
    with patch("services.gemini_service._run_sync_with_timeout", AsyncMock(side_effect=exc)) as runner:
        with patch("services.gemini_service.asyncio.sleep", AsyncMock()) as sleeper:
            with pytest.raises(TransportError):
                await svc.generate_structured("prompt", {"type": "object"})

    assert runner.await_count == 1
    sleeper.assert_not_awaited()


@pytest.mark.asyncio
async def test_generate_structured_returns_empty_when_retries_disabled(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = MagicMock()

    monkeypatch.setattr("services.gemini_service._MAX_RETRIES", 0)
    assert await svc.generate_structured("prompt", {"type": "object"}) == ""


@pytest.mark.asyncio
async def test_generate_native_structured_omits_unsupported_native_service_tier(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = MagicMock()

    runner = AsyncMock(return_value=SimpleNamespace(text='{"value":"ok"}'))
    with patch("services.gemini_service._run_sync_with_timeout", runner):
        result = await svc._generate_native_structured("prompt", _ResponseModel, 0.2, 32, 0.5, "flex")

    assert result.value == "ok"
    assert "service_tier" not in runner.await_args.kwargs["config"].kwargs


@pytest.mark.asyncio
async def test_generate_native_structured_raises_timeout_on_last_attempt(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = MagicMock()

    monkeypatch.setattr("services.gemini_service._MAX_RETRIES", 1)
    with patch("services.gemini_service._run_sync_with_timeout", AsyncMock(side_effect=asyncio.TimeoutError())):
        with pytest.raises(asyncio.TimeoutError):
            await svc._generate_native_structured("prompt", _ResponseModel, 0.2, 32, 0.5, None)


@pytest.mark.asyncio
async def test_generate_native_structured_raises_runtime_when_retries_disabled(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = MagicMock()

    monkeypatch.setattr("services.gemini_service._MAX_RETRIES", 0)
    with pytest.raises(RuntimeError, match="exhausted all retries"):
        await svc._generate_native_structured("prompt", _ResponseModel, 0.2, 32, 0.5, None)


@pytest.mark.asyncio
async def test_generate_text_native_omits_unsupported_native_service_tier(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = MagicMock()

    runner = AsyncMock(return_value=SimpleNamespace(text="hello"))
    with patch("services.gemini_service._run_sync_with_timeout", runner):
        text = await svc._generate_text_native("prompt", 0.2, 32, 0.5, "flex")

    assert text == "hello"
    assert "service_tier" not in runner.await_args.kwargs["config"].kwargs


@pytest.mark.asyncio
async def test_generate_text_native_fast_fails_google_auth_transport(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    class TransportError(RuntimeError):
        pass

    TransportError.__module__ = "google.auth.exceptions"
    exc = TransportError(
        "HTTPSConnectionPool(host='oauth2.googleapis.com', port=443): "
        "Max retries exceeded with url: /token "
        "(Caused by SSLEOFError(8, 'UNEXPECTED_EOF_WHILE_READING'))"
    )

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = MagicMock()

    assert _is_google_auth_transport_error(exc)
    with patch("services.gemini_service._run_sync_with_timeout", AsyncMock(side_effect=exc)) as runner:
        with patch("services.gemini_service.asyncio.sleep", AsyncMock()) as sleeper:
            with pytest.raises(TransportError):
                await svc._generate_text_native("prompt", 0.2, 32, 0.5, None)

    assert runner.await_count == 1
    sleeper.assert_not_awaited()


@pytest.mark.asyncio
async def test_generate_text_native_returns_empty_when_retries_disabled(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = MagicMock()

    monkeypatch.setattr("services.gemini_service._MAX_RETRIES", 0)
    assert await svc._generate_text_native("prompt", 0.2, 32, 0.5, None) == ""


@pytest.mark.asyncio
async def test_generate_via_vertex_proxy_passes_service_tier(monkeypatch):
    from services import gemini_service as gmod

    monkeypatch.setenv("VERTEX_PROXY_URL", "https://proxy.test")
    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = None

    response = MagicMock()
    response.raise_for_status = MagicMock()
    response.json.return_value = {"text": '{"value":"proxy"}'}

    with patch("httpx.AsyncClient") as mock_cls:
        client = AsyncMock()
        client.__aenter__ = AsyncMock(return_value=client)
        client.__aexit__ = AsyncMock(return_value=False)
        client.post = AsyncMock(return_value=response)
        mock_cls.return_value = client

        result = await svc._generate_via_vertex_proxy("prompt", _ResponseModel, 0.3, 32, "flex")

    assert result.value == "proxy"
    assert client.post.await_args.args[0] == "https://proxy.test/generate"
    payload = client.post.await_args.kwargs["json"]
    assert payload["serviceTier"] == "flex"
    assert payload["maxTokens"] == 32
    assert payload["responseFormat"] == "json_object"
    assert payload["jsonSchema"]["type"] == "object"
    assert payload["retryProfile"] == "strict_json"
    assert payload["routingIntent"]["latencyBudgetMs"] > 0
    assert "service_tier" not in payload
    assert "max_tokens" not in payload
    assert "response_schema" not in payload


@pytest.mark.asyncio
async def test_generate_via_vertex_proxy_omits_provider_only_thinking_level(monkeypatch):
    monkeypatch.setenv("VERTEX_PROXY_URL", "https://proxy.test")
    svc = GeminiService.__new__(GeminiService)
    svc.model = TEST_GEMINI_3_FLASH_MODEL
    svc._client = None

    response = MagicMock()
    response.raise_for_status = MagicMock()
    response.json.return_value = {"text": '{"value":"proxy"}'}

    with patch("httpx.AsyncClient") as mock_cls:
        client = AsyncMock()
        client.__aenter__ = AsyncMock(return_value=client)
        client.__aexit__ = AsyncMock(return_value=False)
        client.post = AsyncMock(return_value=response)
        mock_cls.return_value = client

        result = await svc._generate_via_vertex_proxy(
            "prompt",
            _ResponseModel,
            0.3,
            32,
            None,
            thinking_budget=2048,
        )

    assert result.value == "proxy"
    payload = client.post.await_args.kwargs["json"]
    assert payload["tier"] == "standard"
    assert "thinkingLevel" not in payload
    assert "thinkingBudget" not in payload


@pytest.mark.asyncio
async def test_generate_via_vertex_proxy_raises_when_retries_disabled(monkeypatch):
    monkeypatch.setenv("VERTEX_PROXY_URL", "https://proxy.test")
    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = None

    monkeypatch.setattr("services.gemini_service._MAX_RETRIES", 0)
    with pytest.raises(RuntimeError, match="exhausted all retries"):
        await svc._generate_via_vertex_proxy("prompt", _ResponseModel, 0.3, 32, None)


@pytest.mark.asyncio
async def test_embed_returns_empty_when_provider_response_has_no_embeddings(monkeypatch):
    _install_fake_google_modules(monkeypatch)
    sys.modules["google.genai.types"].EmbedContentConfig = lambda **kwargs: SimpleNamespace(**kwargs)

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = SimpleNamespace(
        models=SimpleNamespace(embed_content=MagicMock(return_value=SimpleNamespace(embeddings=[])))
    )

    assert await svc.embed("hello") == []


@pytest.mark.asyncio
async def test_embed_returns_embedding_values(monkeypatch):
    _install_fake_google_modules(monkeypatch)
    sys.modules["google.genai.types"].EmbedContentConfig = lambda **kwargs: SimpleNamespace(**kwargs)

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = SimpleNamespace(
        models=SimpleNamespace(
            embed_content=MagicMock(
                return_value=SimpleNamespace(embeddings=[SimpleNamespace(values=[0.1, 0.2])])
            )
        )
    )

    assert await svc.embed("hello") == [0.1, 0.2]


@pytest.mark.asyncio
async def test_embed_omits_unsupported_task_type_and_keeps_output_dimension(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    class StrictEmbedContentConfig:
        def __init__(self, **kwargs):
            if "task_type" in kwargs:
                raise TypeError("unexpected keyword argument 'task_type'")
            self.kwargs = kwargs

    sys.modules["google.genai.types"].EmbedContentConfig = StrictEmbedContentConfig

    captured: dict[str, object] = {}

    def fake_embed_content(*, model, contents, config):
        captured["model"] = model
        captured["contents"] = contents
        captured["config"] = config
        return SimpleNamespace(embeddings=[SimpleNamespace(values=[0.9, 1.0])])

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = SimpleNamespace(
        models=SimpleNamespace(
            embed_content=MagicMock(side_effect=fake_embed_content)
        )
    )

    # Pin the model to gemini-embedding-2 so this test exercises the
    # embedding-2 code path (proactive task_type omission + task-prefixed
    # content) deterministically, independent of whatever the "standard"
    # embedding tier resolves to in scholar_models.json.
    assert await svc.embed("hello", model="gemini-embedding-2") == [0.9, 1.0]
    assert captured["model"] == "gemini-embedding-2"
    # embed() now delegates to embed_batch(), so contents arrive as a
    # one-element batch.
    assert captured["contents"] == ["task:search query | text: hello"]
    assert getattr(captured["config"], "kwargs", {}) == {"output_dimensionality": 768}


@pytest.mark.asyncio
async def test_embed_batch_returns_values(monkeypatch):
    _install_fake_google_modules(monkeypatch)
    sys.modules["google.genai.types"].EmbedContentConfig = lambda **kwargs: SimpleNamespace(**kwargs)

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = SimpleNamespace(
        models=SimpleNamespace(
            embed_content=MagicMock(
                return_value=SimpleNamespace(
                    embeddings=[SimpleNamespace(values=[0.1]), SimpleNamespace(values=[0.2])]
                )
            )
        )
    )

    assert await svc.embed_batch(["a", "b"]) == [[0.1], [0.2]]


@pytest.mark.asyncio
async def test_embed_batch_uses_api_key_client_for_gemini_embedding_2(monkeypatch):
    _install_fake_google_modules(monkeypatch)
    sys.modules["google.genai.types"].EmbedContentConfig = lambda **kwargs: SimpleNamespace(**kwargs)

    calls: list[dict[str, object]] = []

    def fake_client(**kwargs):
        calls.append(kwargs)
        return SimpleNamespace(
            models=SimpleNamespace(
                embed_content=MagicMock(
                    return_value=SimpleNamespace(embeddings=[SimpleNamespace(values=[0.7])])
                ),
                generate_content=MagicMock(return_value=SimpleNamespace(text="ok")),
            )
        )

    sys.modules["google.genai"].Client.side_effect = fake_client
    monkeypatch.setenv("GOOGLE_API_KEY", "key")
    monkeypatch.setenv("GOOGLE_CLOUD_PROJECT", "project-1")
    monkeypatch.delenv("GOOGLE_CLOUD_LOCATION", raising=False)
    monkeypatch.delenv("GOOGLE_CLOUD_REGION", raising=False)
    monkeypatch.delenv("GOOGLE_CLOUD_EMBEDDING_LOCATION", raising=False)
    monkeypatch.delenv("GOOGLE_CLOUD_EMBEDDING_REGION", raising=False)
    monkeypatch.delenv("VERTEX_PROXY_URL", raising=False)
    monkeypatch.setenv("GEMINI_RUNTIME_MODE", "auto")

    svc = GeminiService(model="test-model")
    assert calls[0]["location"] == "global"
    # Pin the model to gemini-embedding-2 so embed_batch takes the
    # api-key-client branch deterministically, independent of the
    # configured "standard" embedding tier in scholar_models.json.
    assert await svc.embed_batch(["a"], model="gemini-embedding-2") == [[0.7]]
    assert calls[1]["api_key"] == "key"
    assert "location" not in calls[1]


@pytest.mark.asyncio
async def test_embed_resolves_standard_alias_to_concrete_model(monkeypatch):
    _install_fake_google_modules(monkeypatch)
    sys.modules["google.genai.types"].EmbedContentConfig = lambda **kwargs: SimpleNamespace(**kwargs)

    captured: dict[str, object] = {}

    def fake_embed_content(*, model, contents, config):
        captured["model"] = model
        captured["contents"] = contents
        captured["config"] = config
        return SimpleNamespace(embeddings=[SimpleNamespace(values=[0.3, 0.4])])

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = SimpleNamespace(
        models=SimpleNamespace(
            embed_content=MagicMock(side_effect=fake_embed_content)
        )
    )

    assert await svc.embed("hello", model="standard") == [0.3, 0.4]
    assert captured["model"] == GEMINI_EMBED_STANDARD_MODEL


@pytest.mark.asyncio
async def test_generate_diverse_hypotheses_returns_empty_when_no_candidates(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = MagicMock()

    with patch.object(svc, "generate_json", AsyncMock(return_value=SimpleNamespace(hypotheses=[]))):
        assert await svc.generate_diverse_hypotheses("query") == []


@pytest.mark.asyncio
async def test_generate_diverse_hypotheses_breaks_once_target_count_reached(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = MagicMock()

    model_payload = SimpleNamespace(hypotheses=["alpha", "beta", "gamma"])
    with patch.object(svc, "generate_json", AsyncMock(return_value=model_payload)):
        with patch.object(svc, "embed_batch", AsyncMock(return_value=[[1.0], [0.0], [0.5]])):
            hypotheses = await svc.generate_diverse_hypotheses("query", n=1, min_cosine_distance=0.2)

    assert hypotheses == ["alpha"]


@pytest.mark.asyncio
async def test_generate_diverse_hypotheses_token_fallback_breaks_once_target_count_reached(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = MagicMock()

    model_payload = SimpleNamespace(hypotheses=["deep learning", "causal inference", "graph mining"])
    with patch.object(svc, "generate_json", AsyncMock(return_value=model_payload)):
        with patch.object(svc, "embed_batch", AsyncMock(side_effect=RuntimeError("no embeddings"))):
            hypotheses = await svc.generate_diverse_hypotheses("query", n=1)

    assert hypotheses == ["deep learning"]

