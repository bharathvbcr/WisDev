"""Additional edge-case tests for services/ai_generation_service.py."""

import builtins
import json
import sys
import types
from pydantic import BaseModel
from unittest.mock import AsyncMock, patch
import pytest

from services.ai_generation_service import (
    AiGenerationRateLimitError,
    AiGenerationService,
    AiGenerationServiceError,
    AiGenerationStructuredOutputRequiresNativeRuntimeError,
    ModelSelectionStrategy,
)


class SimpleModel(BaseModel):
    value: str
    count: int = 0


class TestGenerateFallbackChains:
    @pytest.mark.asyncio
    async def test_generate_demotes_from_heavy_to_balanced(self, monkeypatch):
        svc = AiGenerationService(default_strategy=ModelSelectionStrategy.ALWAYS_HEAVY)

        mock_generate = AsyncMock(side_effect=[AiGenerationRateLimitError("rate limit"), "ok"])
        with patch.object(
            svc,
            "_generate_via_vertex_proxy",
            mock_generate,
        ):
            text = await svc.generate("prompt")

        assert text == "ok"
        calls = mock_generate.await_args_list
        assert calls[0].kwargs["model"] == "heavy"
        assert calls[1].kwargs["model"] == "balanced"

    @pytest.mark.asyncio
    async def test_generate_raises_last_error_when_all_models_fail(self):
        svc = AiGenerationService(default_strategy=ModelSelectionStrategy.ALWAYS_HEAVY)

        with patch.object(
            svc,
            "_generate_via_vertex_proxy",
            AsyncMock(
                side_effect=[
                    AiGenerationServiceError("x"),
                    AiGenerationServiceError("y"),
                    AiGenerationServiceError("z"),
                ]
            ),
        ):
            with pytest.raises(AiGenerationServiceError, match="z"):
                await svc.generate("prompt")


class TestGenerateJsonFallbackChains:
    @pytest.mark.asyncio
    async def test_generate_json_demotes_native_model_after_retry(self, monkeypatch):
        monkeypatch.setenv("AI_NATIVE_STRUCTURED_ENABLED", "true")
        svc = AiGenerationService(default_strategy=ModelSelectionStrategy.ALWAYS_HEAVY)

        with patch.object(
            svc,
            "_generate_native_structured",
            AsyncMock(
                side_effect=[
                    AiGenerationRateLimitError("rate limit"),
                ]
            ),
        ):
            with patch.object(
                svc,
                "_generate_via_vertex_proxy",
                AsyncMock(return_value='{"value":"ok","count":9}'),
            ) as mock_proxy:
                result = await svc.generate_json("prompt", SimpleModel)
                assert mock_proxy.await_count >= 1

        assert isinstance(result, SimpleModel)
        assert result.value == "ok"
        assert result.count == 9


class TestResolveModelIdAdditional:
    def test_import_error_raises_runtime_error(self):
        original_import = builtins.__import__

        def _importer(name, globals=None, locals=None, fromlist=(), level=0):
            if name == "services.gemini_service":
                raise ImportError("missing gemini")
            return original_import(name, globals, locals, fromlist, level)

        with patch.dict(sys.modules, {"services.gemini_service": None}, clear=False):
            with patch("builtins.__import__", side_effect=_importer):
                with pytest.raises(RuntimeError, match="Missing model configuration"):
                    AiGenerationService._resolve_model_id_for_class("light")


class TestSchemaHelpers:
    def test_prepare_schema_additional_properties_are_annotated(self):
        schema = {
            "type": "object",
            "properties": {
                "name": {"type": "string"},
            },
            "additionalProperties": {
                "type": "object",
                "properties": {"nested": {"type": "string"}},
            },
        }

        prepared = AiGenerationService._prepare_schema_for_provider(schema)

        assert prepared["propertyOrdering"] == ["name"]
        assert "additionalProperties" in prepared
        assert prepared["additionalProperties"]["type"] == "object"
        assert "propertyOrdering" in prepared["additionalProperties"]

    def test_estimate_schema_depth_stops_at_circular_reference(self):
        schema = {"type": "object", "properties": {}}
        schema["properties"]["self"] = schema

        depth = AiGenerationService._estimate_schema_depth(schema)
        assert depth == 1


class TestVertexProxyAdditional:
    @pytest.mark.asyncio
    async def test_generate_via_vertex_proxy_uses_plain_text_when_error_json_parse_fails(self):
        svc = AiGenerationService()
        response = types.SimpleNamespace(
            status_code=400,
            text="plain failure",
            json=lambda: (_ for _ in ()).throw(ValueError("bad json")),
        )

        client = AsyncMock()
        client.__aenter__.return_value = client
        client.__aexit__.return_value = False
        client.post.return_value = response

        with patch("httpx.AsyncClient", return_value=client):
            with pytest.raises(AiGenerationServiceError, match="plain failure"):
                await svc._generate_via_vertex_proxy("prompt", 0.3, 128)


class TestGenerateAdditionalBranches:
    @pytest.mark.asyncio
    async def test_generate_raises_last_error_when_final_response_is_empty(self):
        svc = AiGenerationService(default_strategy=ModelSelectionStrategy.ALWAYS_BALANCED)

        with patch.object(
            svc,
            "_generate_via_vertex_proxy",
            AsyncMock(side_effect=[AiGenerationServiceError("failed"), ""]),
        ):
            with pytest.raises(AiGenerationServiceError, match="failed"):
                await svc.generate("prompt")

    @pytest.mark.asyncio
    async def test_generate_json_schema_failure_and_last_error_re_raise(self, monkeypatch):
        monkeypatch.setenv("AI_NATIVE_STRUCTURED_ENABLED", "true")
        svc = AiGenerationService(default_strategy=ModelSelectionStrategy.ALWAYS_BALANCED)

        with patch.object(
            svc,
            "_generate_native_structured",
            AsyncMock(
                side_effect=[
                    AiGenerationServiceError("native failed"),
                    AiGenerationServiceError("native failed"),
                ]
            ),
        ):
            with patch.object(
                svc,
                "_generate_via_vertex_proxy",
                AsyncMock(side_effect=AiGenerationServiceError("proxy failed")),
            ):
                with pytest.raises(AiGenerationServiceError, match="proxy failed"):
                    await svc.generate_json("prompt", SimpleModel)

    @pytest.mark.asyncio
    async def test_generate_json_raises_when_native_structured_is_disabled(self, monkeypatch):
        monkeypatch.setenv("AI_NATIVE_STRUCTURED_ENABLED", "false")
        svc = AiGenerationService(default_strategy=ModelSelectionStrategy.ALWAYS_BALANCED)

        with patch.object(
            svc,
            "_generate_via_vertex_proxy",
            AsyncMock(return_value='{"value":"proxy","count":2}'),
        ) as mock_proxy:
            result = await svc.generate_json("prompt", SimpleModel)

        assert result.value == "proxy"
        assert result.count == 2
        assert mock_proxy.await_count >= 1


class TestNativeStructuredAdditional:
    def _install_fake_google_modules(self):
        genai_types = types.ModuleType("google.genai.types")

        class DummyGenerateContentConfig:
            def __init__(self, **kwargs):
                self.kwargs = kwargs

        genai_types.GenerateContentConfig = DummyGenerateContentConfig

        genai_module = types.ModuleType("google.genai")
        google_module = types.ModuleType("google")
        google_module.genai = genai_module
        return google_module, genai_module, genai_types

    @pytest.mark.asyncio
    async def test_generate_native_structured_raises_when_sdk_missing(self):
        svc = AiGenerationService()
        original_import = builtins.__import__

        def _importer(name, globals=None, locals=None, fromlist=(), level=0):
            if name in {"google.genai", "google.genai.types"}:
                raise ImportError("missing sdk")
            return original_import(name, globals, locals, fromlist, level)

        with patch("builtins.__import__", side_effect=_importer):
            with pytest.raises(
                RuntimeError,
                match="google-genai SDK not available",
            ):
                await svc._generate_native_structured("prompt", SimpleModel, 0.1, 32)

    @pytest.mark.asyncio
    async def test_generate_native_structured_requires_credentials(self, monkeypatch):
        svc = AiGenerationService()
        google_module, genai_module, genai_types = self._install_fake_google_modules()
        genai_module.Client = lambda **kwargs: None

        monkeypatch.delenv("GOOGLE_CLOUD_PROJECT", raising=False)
        monkeypatch.delenv("GCLOUD_PROJECT", raising=False)
        monkeypatch.delenv("GOOGLE_API_KEY", raising=False)

        with patch.dict(
            sys.modules,
            {
                "google": google_module,
                "google.genai": genai_module,
                "google.genai.types": genai_types,
            },
            clear=False,
        ):
            with pytest.raises(
                RuntimeError,
                match="GOOGLE_CLOUD_PROJECT or GOOGLE_API_KEY",
            ):
                await svc._generate_native_structured("prompt", SimpleModel, 0.1, 32)

    @pytest.mark.asyncio
    @pytest.mark.parametrize("model_tier", ["balanced", "weird"])
    async def test_generate_native_structured_uses_api_key_client_and_standard_tier(self, monkeypatch, model_tier):
        svc = AiGenerationService()
        google_module, genai_module, genai_types = self._install_fake_google_modules()
        mock_client = types.SimpleNamespace(
            aio=types.SimpleNamespace(
                models=types.SimpleNamespace(
                    generate_content=AsyncMock(return_value=types.SimpleNamespace(text='{"value":"ok","count":1}'))
                )
            )
        )
        genai_module.Client = lambda **kwargs: mock_client

        monkeypatch.delenv("GOOGLE_CLOUD_PROJECT", raising=False)
        monkeypatch.delenv("GCLOUD_PROJECT", raising=False)
        monkeypatch.setenv("GOOGLE_API_KEY", "test-key")

        with patch.object(svc, "_resolve_model_id_for_class", return_value="resolved-model") as resolver:
            with patch.dict(
                sys.modules,
                {
                    "google": google_module,
                    "google.genai": genai_module,
                    "google.genai.types": genai_types,
                },
                clear=False,
            ):
                    result = await svc._generate_native_structured(
                        "prompt", SimpleModel, 0.1, 32, model_tier=model_tier
                    )

        assert result.value == "ok"
        resolver.assert_called_with("standard")


class TestThinkingAdditional:
    @pytest.mark.asyncio
    async def test_generate_with_thinking_falls_back_when_thinking_config_is_unsupported(self, monkeypatch):
        svc = AiGenerationService()

        genai_types = types.ModuleType("google.genai.types")

        class StrictGenerateContentConfig:
            def __init__(self, **kwargs):
                if "thinking_config" in kwargs:
                    raise TypeError("unexpected keyword argument 'thinking_config'")
                self.kwargs = kwargs

        class DummyThinkingConfig:
            def __init__(self, **kwargs):
                self.kwargs = kwargs

        genai_types.GenerateContentConfig = StrictGenerateContentConfig
        genai_types.ThinkingConfig = DummyThinkingConfig

        mock_client = types.SimpleNamespace(
            aio=types.SimpleNamespace(
                models=types.SimpleNamespace(
                    generate_content=AsyncMock(
                        return_value=types.SimpleNamespace(text='{"value":"native","count":3}')
                    )
                )
            )
        )

        genai_module = types.ModuleType("google.genai")
        genai_module.Client = lambda **kwargs: mock_client
        google_module = types.ModuleType("google")
        google_module.genai = genai_module

        monkeypatch.delenv("GOOGLE_CLOUD_PROJECT", raising=False)
        monkeypatch.delenv("GCLOUD_PROJECT", raising=False)
        monkeypatch.setenv("AI_MODEL_THINKING_ID", "thinking-model")
        monkeypatch.setenv("GOOGLE_API_KEY", "test-key")

        fallback = AsyncMock(return_value=SimpleModel(value="fallback", count=1))
        with patch.object(svc, "generate_json", fallback):
            with patch.dict(
                sys.modules,
                {
                    "google": google_module,
                    "google.genai": genai_module,
                    "google.genai.types": genai_types,
                },
                clear=False,
            ):
                result = await svc.generate_with_thinking("prompt", SimpleModel)

        assert result == SimpleModel(value="fallback", count=1)
        fallback.assert_awaited_once()

    @pytest.mark.asyncio
    async def test_generate_with_thinking_falls_back_when_required_native_config_is_unsupported(
        self, monkeypatch
    ):
        svc = AiGenerationService()

        genai_types = types.ModuleType("google.genai.types")

        class StrictGenerateContentConfig:
            def __init__(self, **kwargs):
                raise TypeError("unexpected keyword argument 'response_json_schema'")

        class DummyThinkingConfig:
            def __init__(self, **kwargs):
                self.kwargs = kwargs

        genai_types.GenerateContentConfig = StrictGenerateContentConfig
        genai_types.ThinkingConfig = DummyThinkingConfig

        mock_client = types.SimpleNamespace(
            aio=types.SimpleNamespace(
                models=types.SimpleNamespace(generate_content=AsyncMock())
            )
        )

        genai_module = types.ModuleType("google.genai")
        genai_module.Client = lambda **kwargs: mock_client
        google_module = types.ModuleType("google")
        google_module.genai = genai_module

        monkeypatch.delenv("GOOGLE_CLOUD_PROJECT", raising=False)
        monkeypatch.delenv("GCLOUD_PROJECT", raising=False)
        monkeypatch.setenv("AI_MODEL_THINKING_ID", "thinking-model")
        monkeypatch.setenv("GOOGLE_API_KEY", "test-key")

        expected = SimpleModel(value="fallback", count=7)
        fallback = AsyncMock(return_value=expected)
        with patch.object(svc, "generate_json", fallback):
            with patch.dict(
                sys.modules,
                {
                    "google": google_module,
                    "google.genai": genai_module,
                    "google.genai.types": genai_types,
                },
                clear=False,
            ):
                result = await svc.generate_with_thinking("prompt", SimpleModel)

        assert result == expected
        fallback.assert_awaited_once()
        mock_client.aio.models.generate_content.assert_not_awaited()

    @pytest.mark.asyncio
    async def test_generate_with_thinking_api_key_path_falls_back_on_exception(self, monkeypatch):
        svc = AiGenerationService()

        genai_types = types.ModuleType("google.genai.types")

        class DummyGenerateContentConfig:
            def __init__(self, **kwargs):
                self.kwargs = kwargs

        class DummyThinkingConfig:
            def __init__(self, **kwargs):
                self.kwargs = kwargs

        genai_types.GenerateContentConfig = DummyGenerateContentConfig
        genai_types.ThinkingConfig = DummyThinkingConfig

        mock_client = types.SimpleNamespace(
            aio=types.SimpleNamespace(
                models=types.SimpleNamespace(
                    generate_content=AsyncMock(side_effect=RuntimeError("thinking failed"))
                )
            )
        )

        genai_module = types.ModuleType("google.genai")
        genai_module.Client = lambda **kwargs: mock_client
        google_module = types.ModuleType("google")
        google_module.genai = genai_module

        monkeypatch.delenv("GOOGLE_CLOUD_PROJECT", raising=False)
        monkeypatch.delenv("GCLOUD_PROJECT", raising=False)
        monkeypatch.setenv("AI_MODEL_THINKING_ID", "thinking-model")
        monkeypatch.setenv("GOOGLE_API_KEY", "test-key")

        expected = SimpleModel(value="fallback", count=2)
        with patch.object(svc, "generate_json", AsyncMock(return_value=expected)):
            with patch.dict(
                sys.modules,
                {
                    "google": google_module,
                    "google.genai": genai_module,
                    "google.genai.types": genai_types,
                },
                clear=False,
            ):
                result = await svc.generate_with_thinking("prompt", SimpleModel)

        assert result == expected


class TestEnsureWisdevShapeAdditional:
    def test_ensure_wisdev_shape_defaults_reasoning_verification_and_claim_table(self):
        reasoning = AiGenerationService._ensure_wisdev_shape(
            "research.verifyReasoningPaths",
            {"branches": [], "reasoningVerification": "invalid"},
        )
        claim_table = AiGenerationService._ensure_wisdev_shape(
            "research.buildClaimEvidenceTable",
            {"claimEvidenceTable": "invalid"},
        )

        assert reasoning["reasoningVerification"]["totalBranches"] == 0
        assert reasoning["reasoningVerification"]["readyForSynthesis"] is False
        assert claim_table["claimEvidenceTable"]["table"] == ""

    def test_ensure_wisdev_shape_preserves_hypothesis_branches(self):
        shaped = AiGenerationService._ensure_wisdev_shape(
            "research.proposeHypotheses",
            {"branches": ["alpha"]},
        )

        assert shaped["branches"] == ["alpha"]
