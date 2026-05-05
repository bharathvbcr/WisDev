"""
Tests for services/ai_generation_service.py.

Covers: model selection, JSON parsing utilities, proxy generation, retry logic,
native structured path, generate_with_thinking, fallback helpers, and
estimate_complexity.
"""

import json
import os
import pytest
import pytest_asyncio
from unittest.mock import AsyncMock, MagicMock, patch
from pydantic import BaseModel, Field

from services.ai_generation_service import (
    AiGenerationService,
    AiGenerationServiceError,
    AiGenerationRetryableError,
    AiGenerationRateLimitError,
    AiGenerationParsingError,
    ModelSelectionStrategy,
)


# ---------------------------------------------------------------------------
# Helpers / fixtures
# ---------------------------------------------------------------------------

class SimpleModel(BaseModel):
    value: str
    count: int = 0


class NestedModel(BaseModel):
    name: str
    items: list[SimpleModel] = []


class ResolveCitationsModel(BaseModel):
    canonical_sources: list[dict] = Field(default_factory=list, alias="canonicalSources")
    resolved_count: int = Field(0, alias="resolvedCount")
    duplicate_count: int = Field(0, alias="duplicateCount")


@pytest.fixture
def svc():
    """AiGenerationService with ADAPTIVE strategy and short timeout."""
    return AiGenerationService(
        default_strategy=ModelSelectionStrategy.ADAPTIVE,
        timeout_seconds=5.0,
    )


@pytest.fixture
def light_svc():
    return AiGenerationService(default_strategy=ModelSelectionStrategy.ALWAYS_LIGHT)


@pytest.fixture
def heavy_svc():
    return AiGenerationService(default_strategy=ModelSelectionStrategy.ALWAYS_HEAVY)


@pytest.fixture
def balanced_svc():
    return AiGenerationService(default_strategy=ModelSelectionStrategy.ALWAYS_BALANCED)


# ---------------------------------------------------------------------------
# _select_model
# ---------------------------------------------------------------------------

class TestSelectModel:
    def test_always_light_strategy(self, light_svc):
        assert light_svc._select_model(0.9, 0.9) == "light"

    def test_always_balanced_strategy(self, balanced_svc):
        assert balanced_svc._select_model(0.0, 0.0) == "balanced"

    def test_always_heavy_strategy(self, heavy_svc):
        assert heavy_svc._select_model(0.0, 0.0) == "heavy"

    def test_adaptive_low_complexity_returns_light(self, svc):
        result = svc._select_model(complexity_score=0.1, uncertainty_score=0.1)
        assert result == "light"

    def test_adaptive_mid_complexity_returns_balanced(self, svc):
        result = svc._select_model(complexity_score=0.5, uncertainty_score=0.4)
        assert result == "balanced"

    def test_adaptive_high_complexity_returns_heavy(self, svc):
        result = svc._select_model(complexity_score=0.9, uncertainty_score=0.9)
        assert result == "heavy"

    def test_strict_domain_with_budget_returns_heavy(self, svc):
        result = svc._select_model(0.1, 0.1, strict_domain=True, remaining_budget_ratio=0.5)
        assert result == "heavy"

    def test_strict_domain_critically_constrained_budget(self, svc):
        # budget_ratio < 0.15 skips the strict_domain heavy override
        result = svc._select_model(0.1, 0.1, strict_domain=True, remaining_budget_ratio=0.10)
        assert result == "light"

    def test_low_reward_nudges_complexity_up(self, svc):
        # With reward < 0.45 and borderline complexity, should push to heavier tier
        result_normal = svc._select_model(0.25, 0.25, historical_reward=0.8)
        result_low_reward = svc._select_model(0.25, 0.25, historical_reward=0.3)
        # low reward should be >= normal (same or heavier tier)
        tier_rank = {"light": 0, "balanced": 1, "heavy": 2}
        assert tier_rank[result_low_reward] >= tier_rank[result_normal]

    def test_low_budget_nudges_complexity_down(self, svc):
        # With budget < 0.25 and borderline complexity, should push to lighter tier
        result_tight = svc._select_model(0.5, 0.5, remaining_budget_ratio=0.1)
        result_full = svc._select_model(0.5, 0.5, remaining_budget_ratio=1.0)
        tier_rank = {"light": 0, "balanced": 1, "heavy": 2}
        assert tier_rank[result_tight] <= tier_rank[result_full]

    def test_strategy_override_on_instance(self, svc):
        # Pass strategy kwarg to override instance default
        result = svc._select_model(0.9, 0.9, strategy=ModelSelectionStrategy.ALWAYS_LIGHT)
        assert result == "light"

    def test_scores_clamped_to_range(self, svc):
        # Should not crash on out-of-range scores
        result = svc._select_model(complexity_score=-5.0, uncertainty_score=99.0)
        assert result in ("light", "balanced", "heavy")


# ---------------------------------------------------------------------------
# _model_fallback_chain
# ---------------------------------------------------------------------------

class TestModelFallbackChain:
    def test_heavy_chain(self):
        assert AiGenerationService._model_fallback_chain("heavy") == [
            "heavy", "balanced", "light"
        ]

    def test_balanced_chain(self):
        assert AiGenerationService._model_fallback_chain("balanced") == [
            "balanced", "light"
        ]

    def test_standard_alias(self):
        assert AiGenerationService._model_fallback_chain("standard") == [
            "balanced", "light"
        ]

    def test_light_chain(self):
        assert AiGenerationService._model_fallback_chain("light") == ["light"]

    def test_unknown_model_returns_light(self):
        assert AiGenerationService._model_fallback_chain("unknown") == ["light"]

    def test_empty_string(self):
        assert AiGenerationService._model_fallback_chain("") == ["light"]


# ---------------------------------------------------------------------------
# _resolve_model_id_for_class
# ---------------------------------------------------------------------------

class TestResolveModelIdForClass:
    def test_light_resolves_from_env(self, monkeypatch):
        monkeypatch.setenv("AI_MODEL_LIGHT_ID", "gemini-flash")
        assert AiGenerationService._resolve_model_id_for_class("light") == "gemini-flash"

    def test_heavy_resolves_from_env(self, monkeypatch):
        monkeypatch.setenv("AI_MODEL_HEAVY_ID", "gemini-pro")
        assert AiGenerationService._resolve_model_id_for_class("heavy") == "gemini-pro"

    def test_balanced_resolves_from_balanced_env(self, monkeypatch):
        monkeypatch.setenv("AI_MODEL_BALANCED_ID", "gemini-balanced")
        assert AiGenerationService._resolve_model_id_for_class("balanced") == "gemini-balanced"

    def test_standard_alias_resolves_balanced_env(self, monkeypatch):
        monkeypatch.setenv("AI_MODEL_BALANCED_ID", "gemini-standard")
        assert AiGenerationService._resolve_model_id_for_class("standard") == "gemini-standard"

    def test_unknown_class_falls_back_to_balanced_env(self, monkeypatch):
        monkeypatch.setenv("AI_MODEL_BALANCED_ID", "default-model")
        assert AiGenerationService._resolve_model_id_for_class("mystery") == "default-model"

    def test_falls_back_to_default_id(self, monkeypatch):
        monkeypatch.delenv("AI_MODEL_LIGHT_ID", raising=False)
        monkeypatch.setenv("AI_MODEL_DEFAULT_ID", "shared-model")
        assert AiGenerationService._resolve_model_id_for_class("light") == "shared-model"

    def test_raises_when_no_env(self, monkeypatch):
        monkeypatch.delenv("AI_MODEL_LIGHT_ID", raising=False)
        monkeypatch.delenv("AI_MODEL_DEFAULT_ID", raising=False)
        with pytest.raises(RuntimeError, match="Missing model configuration"):
            AiGenerationService._resolve_model_id_for_class("light")


# ---------------------------------------------------------------------------
# _estimate_schema_depth
# ---------------------------------------------------------------------------

class TestEstimateSchemaDepth:
    def test_flat_schema(self):
        schema = {"type": "object", "properties": {"a": {"type": "string"}}}
        depth = AiGenerationService._estimate_schema_depth(schema)
        assert depth >= 1

    def test_empty_schema(self):
        depth = AiGenerationService._estimate_schema_depth({})
        assert depth == 0

    def test_nested_schema(self):
        schema = {
            "type": "object",
            "properties": {
                "level1": {
                    "type": "object",
                    "properties": {
                        "level2": {"type": "string"}
                    }
                }
            }
        }
        depth = AiGenerationService._estimate_schema_depth(schema)
        assert depth >= 2

    def test_array_items(self):
        schema = {
            "type": "array",
            "items": {"type": "object", "properties": {"x": {"type": "int"}}}
        }
        depth = AiGenerationService._estimate_schema_depth(schema)
        assert depth >= 1

    def test_max_depth_capped(self):
        # Build a deeply nested schema
        schema: dict = {}
        current = schema
        for _ in range(25):
            inner: dict = {}
            current["properties"] = {"child": inner}
            current = inner
        depth = AiGenerationService._estimate_schema_depth(schema)
        assert depth <= 20

    def test_non_dict_schema_returns_current(self):
        depth = AiGenerationService._estimate_schema_depth("not a dict")  # type: ignore
        assert depth == 0

    def test_defs_traversed(self):
        schema = {
            "$defs": {
                "MyType": {"type": "object", "properties": {"x": {"type": "str"}}}
            }
        }
        depth = AiGenerationService._estimate_schema_depth(schema)
        assert depth >= 1

    def test_any_of(self):
        schema = {
            "anyOf": [
                {"type": "object", "properties": {"a": {"type": "str"}}},
                {"type": "null"},
            ]
        }
        depth = AiGenerationService._estimate_schema_depth(schema)
        assert depth >= 1


# ---------------------------------------------------------------------------
# _prepare_schema_for_provider
# ---------------------------------------------------------------------------

class TestPrepareSchemaForProvider:
    def test_adds_property_ordering(self):
        schema = {
            "type": "object",
            "properties": {"a": {"type": "string"}, "b": {"type": "integer"}},
        }
        result = AiGenerationService._prepare_schema_for_provider(schema)
        assert "propertyOrdering" in result
        assert result["propertyOrdering"] == ["a", "b"]

    def test_preserves_existing_property_ordering(self):
        schema = {
            "type": "object",
            "properties": {"a": {"type": "string"}},
            "propertyOrdering": ["a"],
        }
        result = AiGenerationService._prepare_schema_for_provider(schema)
        assert result["propertyOrdering"] == ["a"]

    def test_nested_properties_annotated(self):
        schema = {
            "type": "object",
            "properties": {
                "outer": {
                    "type": "object",
                    "properties": {"inner": {"type": "string"}},
                }
            },
        }
        result = AiGenerationService._prepare_schema_for_provider(schema)
        outer = result["properties"]["outer"]
        assert "propertyOrdering" in outer

    def test_does_not_mutate_original(self):
        schema = {
            "type": "object",
            "properties": {"a": {"type": "string"}},
        }
        original_schema = schema.copy()
        AiGenerationService._prepare_schema_for_provider(schema)
        assert schema == original_schema

    def test_non_object_schema_unchanged(self):
        schema = {"type": "string"}
        result = AiGenerationService._prepare_schema_for_provider(schema)
        assert "propertyOrdering" not in result

    def test_array_items_annotated(self):
        schema = {
            "type": "array",
            "items": {
                "type": "object",
                "properties": {"x": {"type": "string"}},
            },
        }
        result = AiGenerationService._prepare_schema_for_provider(schema)
        assert "propertyOrdering" in result["items"]


# ---------------------------------------------------------------------------
# _generate_via_vertex_proxy
# ---------------------------------------------------------------------------

class TestGenerateViaVertexProxy:
    @pytest.mark.asyncio
    async def test_success(self, svc):
        mock_resp = MagicMock()
        mock_resp.status_code = 200
        mock_resp.json.return_value = {"text": "hello world"}

        with patch("httpx.AsyncClient") as mock_client_cls:
            mock_client = AsyncMock()
            mock_client.__aenter__ = AsyncMock(return_value=mock_client)
            mock_client.__aexit__ = AsyncMock(return_value=False)
            mock_client.post = AsyncMock(return_value=mock_resp)
            mock_client_cls.return_value = mock_client

            result = await svc._generate_via_vertex_proxy("prompt", 0.7, 256)
            assert result == "hello world"

    @pytest.mark.asyncio
    async def test_rate_limit_raises(self, svc):
        mock_resp = MagicMock()
        mock_resp.status_code = 429

        with patch("httpx.AsyncClient") as mock_client_cls:
            mock_client = AsyncMock()
            mock_client.__aenter__ = AsyncMock(return_value=mock_client)
            mock_client.__aexit__ = AsyncMock(return_value=False)
            mock_client.post = AsyncMock(return_value=mock_resp)
            mock_client_cls.return_value = mock_client

            with pytest.raises(AiGenerationRateLimitError):
                await svc._generate_via_vertex_proxy("prompt", 0.7, 256)

    @pytest.mark.asyncio
    async def test_server_error_raises_retryable(self, svc):
        mock_resp = MagicMock()
        mock_resp.status_code = 503

        with patch("httpx.AsyncClient") as mock_client_cls:
            mock_client = AsyncMock()
            mock_client.__aenter__ = AsyncMock(return_value=mock_client)
            mock_client.__aexit__ = AsyncMock(return_value=False)
            mock_client.post = AsyncMock(return_value=mock_resp)
            mock_client_cls.return_value = mock_client

            with pytest.raises(AiGenerationRetryableError):
                await svc._generate_via_vertex_proxy("prompt", 0.7, 256)

    @pytest.mark.asyncio
    async def test_client_error_raises_service_error(self, svc):
        mock_resp = MagicMock()
        mock_resp.status_code = 400
        mock_resp.text = "Bad request"
        mock_resp.json.return_value = {"error": "invalid prompt"}

        with patch("httpx.AsyncClient") as mock_client_cls:
            mock_client = AsyncMock()
            mock_client.__aenter__ = AsyncMock(return_value=mock_client)
            mock_client.__aexit__ = AsyncMock(return_value=False)
            mock_client.post = AsyncMock(return_value=mock_resp)
            mock_client_cls.return_value = mock_client

            with pytest.raises(AiGenerationServiceError):
                await svc._generate_via_vertex_proxy("prompt", 0.7, 256)

    @pytest.mark.asyncio
    async def test_timeout_raises_retryable(self, svc):
        import httpx

        with patch("httpx.AsyncClient") as mock_client_cls:
            mock_client = AsyncMock()
            mock_client.__aenter__ = AsyncMock(return_value=mock_client)
            mock_client.__aexit__ = AsyncMock(return_value=False)
            mock_client.post = AsyncMock(
                side_effect=httpx.TimeoutException("timed out")
            )
            mock_client_cls.return_value = mock_client

            with pytest.raises(AiGenerationRetryableError, match="timed out"):
                await svc._generate_via_vertex_proxy("prompt", 0.7, 256)

    @pytest.mark.asyncio
    async def test_http_error_raises_retryable(self, svc):
        import httpx

        with patch("httpx.AsyncClient") as mock_client_cls:
            mock_client = AsyncMock()
            mock_client.__aenter__ = AsyncMock(return_value=mock_client)
            mock_client.__aexit__ = AsyncMock(return_value=False)
            mock_client.post = AsyncMock(
                side_effect=httpx.HTTPError("connection refused")
            )
            mock_client_cls.return_value = mock_client

            with pytest.raises(AiGenerationRetryableError):
                await svc._generate_via_vertex_proxy("prompt", 0.7, 256)

    @pytest.mark.asyncio
    async def test_empty_text_raises_service_error(self, svc):
        mock_resp = MagicMock()
        mock_resp.status_code = 200
        mock_resp.json.return_value = {"text": ""}

        with patch("httpx.AsyncClient") as mock_client_cls:
            mock_client = AsyncMock()
            mock_client.__aenter__ = AsyncMock(return_value=mock_client)
            mock_client.__aexit__ = AsyncMock(return_value=False)
            mock_client.post = AsyncMock(return_value=mock_resp)
            mock_client_cls.return_value = mock_client

            with pytest.raises(AiGenerationServiceError, match="Empty response"):
                await svc._generate_via_vertex_proxy("prompt", 0.7, 256)

    @pytest.mark.asyncio
    async def test_invalid_json_response_raises_service_error(self, svc):
        mock_resp = MagicMock()
        mock_resp.status_code = 200
        mock_resp.json.side_effect = Exception("decode error")

        with patch("httpx.AsyncClient") as mock_client_cls:
            mock_client = AsyncMock()
            mock_client.__aenter__ = AsyncMock(return_value=mock_client)
            mock_client.__aexit__ = AsyncMock(return_value=False)
            mock_client.post = AsyncMock(return_value=mock_resp)
            mock_client_cls.return_value = mock_client

            with pytest.raises(AiGenerationServiceError, match="invalid JSON"):
                await svc._generate_via_vertex_proxy("prompt", 0.7, 256)


# ---------------------------------------------------------------------------
# generate_json — proxy path
# ---------------------------------------------------------------------------

class TestGenerateJson:
    def _make_proxy_mock(self, text: str):
        """Return a patched _generate_via_vertex_proxy that returns `text`."""
        return patch.object(
            AiGenerationService,
            "_generate_via_vertex_proxy",
            new=AsyncMock(return_value=text),
        )

    @pytest.mark.asyncio
    async def test_clean_json_parsed(self, svc, monkeypatch):
        monkeypatch.setenv("AI_MODEL_BALANCED_ID", "test-model")
        payload = json.dumps({"value": "hello", "count": 42})
        with self._make_proxy_mock(payload):
            result = await svc.generate_json("prompt", SimpleModel)
        assert result.value == "hello"
        assert result.count == 42

    @pytest.mark.asyncio
    async def test_markdown_wrapped_json_rejected(self, svc, monkeypatch):
        monkeypatch.setenv("AI_MODEL_BALANCED_ID", "test-model")
        payload = "```json\n{\"value\": \"ok\", \"count\": 1}\n```"
        with self._make_proxy_mock(payload):
            with pytest.raises(AiGenerationParsingError):
                await svc.generate_json("prompt", SimpleModel)

    @pytest.mark.asyncio
    async def test_json_with_preamble_rejected(self, svc, monkeypatch):
        monkeypatch.setenv("AI_MODEL_BALANCED_ID", "test-model")
        payload = 'Here is your response: {"value": "extracted", "count": 0}'
        with self._make_proxy_mock(payload):
            with pytest.raises(AiGenerationParsingError):
                await svc.generate_json("prompt", SimpleModel)

    @pytest.mark.asyncio
    async def test_invalid_json_raises_parsing_error(self, svc, monkeypatch):
        monkeypatch.setenv("AI_MODEL_BALANCED_ID", "test-model")
        with self._make_proxy_mock("not json at all"):
            with pytest.raises(AiGenerationParsingError):
                await svc.generate_json("prompt", SimpleModel)

    @pytest.mark.asyncio
    async def test_wrong_schema_raises_parsing_error(self, svc, monkeypatch):
        monkeypatch.setenv("AI_MODEL_BALANCED_ID", "test-model")
        # Valid JSON but wrong schema (missing required 'value' field)
        payload = json.dumps({"wrong_field": "x"})
        with self._make_proxy_mock(payload):
            with pytest.raises(AiGenerationParsingError):
                await svc.generate_json("prompt", SimpleModel)

    @pytest.mark.asyncio
    async def test_native_path_used_when_enabled(self, monkeypatch):
        monkeypatch.setenv("AI_NATIVE_STRUCTURED_ENABLED", "true")
        monkeypatch.setenv("AI_MODEL_BALANCED_ID", "test-model")
        svc = AiGenerationService()

        mock_result = SimpleModel(value="native", count=99)
        with patch.object(
            AiGenerationService,
            "_generate_native_structured",
            new=AsyncMock(return_value=mock_result),
        ):
            result = await svc.generate_json("prompt", SimpleModel)
        assert result.value == "native"

    @pytest.mark.asyncio
    async def test_native_path_falls_back_on_error(self, monkeypatch):
        monkeypatch.setenv("AI_NATIVE_STRUCTURED_ENABLED", "true")
        monkeypatch.setenv("AI_MODEL_BALANCED_ID", "test-model")
        svc = AiGenerationService()

        proxy_payload = json.dumps({"value": "fallback", "count": 0})

        with patch.object(
            AiGenerationService,
            "_generate_native_structured",
            new=AsyncMock(side_effect=RuntimeError("SDK unavailable")),
        ):
            with patch.object(
                AiGenerationService,
                "_generate_via_vertex_proxy",
                new=AsyncMock(return_value=proxy_payload),
            ):
                result = await svc.generate_json("prompt", SimpleModel)
        assert result.value == "fallback"

    @pytest.mark.asyncio
    async def test_exact_json_proxy_path(self, svc, monkeypatch):
        monkeypatch.setenv("AI_MODEL_BALANCED_ID", "test-model")
        payload = '{"value": "recovered", "count": 5}'
        with self._make_proxy_mock(payload):
            result = await svc.generate_json("prompt", SimpleModel)
        assert result.value == "recovered"


# ---------------------------------------------------------------------------
# generate (text)
# ---------------------------------------------------------------------------

class TestGenerate:
    @pytest.mark.asyncio
    async def test_success(self, svc, monkeypatch):
        monkeypatch.setenv("AI_MODEL_BALANCED_ID", "test-model")
        with patch.object(
            AiGenerationService,
            "_generate_via_vertex_proxy",
            new=AsyncMock(return_value="generated text"),
        ):
            result = await svc.generate("hello")
        assert result == "generated text"

    @pytest.mark.asyncio
    async def test_propagates_service_error(self, svc, monkeypatch):
        monkeypatch.setenv("AI_MODEL_BALANCED_ID", "test-model")
        with patch.object(
            AiGenerationService,
            "_generate_via_vertex_proxy",
            new=AsyncMock(side_effect=AiGenerationServiceError("permanent")),
        ):
            with pytest.raises(AiGenerationServiceError):
                await svc.generate("hello")


# ---------------------------------------------------------------------------
# generate_with_fallback
# ---------------------------------------------------------------------------

class TestGenerateWithFallback:
    @pytest.mark.asyncio
    async def test_success_no_fallback(self, svc, monkeypatch):
        monkeypatch.setenv("AI_MODEL_BALANCED_ID", "test-model")
        with patch.object(
            AiGenerationService,
            "generate",
            new=AsyncMock(return_value="great response"),
        ):
            text, used_fallback = await svc.generate_with_fallback("prompt", "fallback")
        assert text == "great response"
        assert used_fallback is False

    @pytest.mark.asyncio
    async def test_failure_returns_fallback(self, svc):
        with patch.object(
            AiGenerationService,
            "generate",
            new=AsyncMock(side_effect=Exception("network down")),
        ):
            text, used_fallback = await svc.generate_with_fallback("prompt", "my_fallback")
        assert text == "my_fallback"
        assert used_fallback is True


# ---------------------------------------------------------------------------
# generate_json_with_fallback
# ---------------------------------------------------------------------------

class TestGenerateJsonWithFallback:
    @pytest.mark.asyncio
    async def test_success_no_fallback(self, svc, monkeypatch):
        monkeypatch.setenv("AI_MODEL_BALANCED_ID", "test-model")
        expected = SimpleModel(value="ok", count=5)
        with patch.object(
            AiGenerationService,
            "generate_json",
            new=AsyncMock(return_value=expected),
        ):
            result, used_fallback = await svc.generate_json_with_fallback(
                "prompt", SimpleModel, SimpleModel(value="fallback")
            )
        assert result.value == "ok"
        assert used_fallback is False

    @pytest.mark.asyncio
    async def test_failure_returns_fallback(self, svc):
        fallback = SimpleModel(value="default")
        with patch.object(
            AiGenerationService,
            "generate_json",
            new=AsyncMock(side_effect=AiGenerationParsingError("bad json")),
        ):
            result, used_fallback = await svc.generate_json_with_fallback(
                "prompt", SimpleModel, fallback
            )
        assert result.value == "default"
        assert used_fallback is True


# ---------------------------------------------------------------------------
# generate_with_thinking
# ---------------------------------------------------------------------------

class TestGenerateWithThinking:
    @pytest.mark.asyncio
    async def test_falls_back_when_import_fails(self, svc, monkeypatch):
        monkeypatch.setenv("AI_MODEL_BALANCED_ID", "test-model")
        expected = SimpleModel(value="from_json", count=1)

        # Simulate google.genai not available at import time inside the method
        import builtins
        original_import = builtins.__import__

        def mock_import(name, *args, **kwargs):
            if name == "google.genai":
                raise ImportError("no module")
            return original_import(name, *args, **kwargs)

        with patch("builtins.__import__", side_effect=mock_import):
            with patch.object(
                AiGenerationService,
                "generate_json",
                new=AsyncMock(return_value=expected),
            ):
                result = await svc.generate_with_thinking("prompt", SimpleModel)
        assert result.value == "from_json"

    @pytest.mark.asyncio
    async def test_falls_back_when_no_credentials(self, svc, monkeypatch):
        monkeypatch.delenv("GOOGLE_CLOUD_PROJECT", raising=False)
        monkeypatch.delenv("GCLOUD_PROJECT", raising=False)
        monkeypatch.delenv("GOOGLE_API_KEY", raising=False)
        monkeypatch.setenv("AI_MODEL_BALANCED_ID", "test-model")

        expected = SimpleModel(value="fallback_no_creds")

        with patch.object(
            AiGenerationService,
            "generate_json",
            new=AsyncMock(return_value=expected),
        ):
            result = await svc.generate_with_thinking("prompt", SimpleModel)
        assert result.value == "fallback_no_creds"

    @pytest.mark.asyncio
    async def test_falls_back_when_no_model_id(self, svc, monkeypatch):
        monkeypatch.setenv("GOOGLE_CLOUD_PROJECT", "my-project")
        monkeypatch.delenv("AI_MODEL_THINKING_ID", raising=False)
        monkeypatch.delenv("AI_MODEL_HEAVY_ID", raising=False)
        monkeypatch.delenv("AI_MODEL_DEFAULT_ID", raising=False)

        expected = SimpleModel(value="fallback_no_model")

        with patch.object(
            AiGenerationService,
            "generate_json",
            new=AsyncMock(return_value=expected),
        ):
            result = await svc.generate_with_thinking("prompt", SimpleModel)
        assert result.value == "fallback_no_model"


# ---------------------------------------------------------------------------
# estimate_complexity
# ---------------------------------------------------------------------------

class TestEstimateComplexity:
    def test_short_simple_query(self, svc):
        score = svc.estimate_complexity("cancer")
        assert 0.0 <= score <= 1.0
        assert score < 0.5

    def test_long_query_has_higher_score(self, svc):
        long_query = " ".join(["word"] * 25)
        short_query = "simple query"
        assert svc.estimate_complexity(long_query) > svc.estimate_complexity(short_query)

    def test_complex_indicators_increase_score(self, svc):
        base = svc.estimate_complexity("query about biology")
        with_indicator = svc.estimate_complexity("mechanism of biology pathway")
        assert with_indicator >= base

    def test_ambiguity_indicators_increase_score(self, svc):
        base = svc.estimate_complexity("cancer treatment")
        with_ambiguity = svc.estimate_complexity("cancer treatment or surgery multiple types")
        assert with_ambiguity >= base

    def test_score_capped_at_one(self, svc):
        very_complex = (
            "comprehensive systematic review meta-analysis comparison versus "
            "mechanism pathway interaction relationship multifactorial "
            "interdisciplinary cross-domain various multiple different types "
            "all aspects everything about either and/or"
        )
        score = svc.estimate_complexity(very_complex)
        assert score <= 1.0

    def test_score_is_float(self, svc):
        assert isinstance(svc.estimate_complexity("test"), float)

    def test_medium_length_query(self, svc):
        query = " ".join(["word"] * 12)
        score = svc.estimate_complexity(query)
        assert score >= 0.15  # medium length adds 0.15


# ---------------------------------------------------------------------------
# Exception class hierarchy
# ---------------------------------------------------------------------------

class TestExceptions:
    def test_retryable_is_service_error(self):
        assert issubclass(AiGenerationRetryableError, AiGenerationServiceError)

    def test_rate_limit_is_retryable(self):
        assert issubclass(AiGenerationRateLimitError, AiGenerationRetryableError)

    def test_parsing_is_service_error(self):
        assert issubclass(AiGenerationParsingError, AiGenerationServiceError)

    def test_can_instantiate_all_exceptions(self):
        for exc_cls in [
            AiGenerationServiceError,
            AiGenerationRetryableError,
            AiGenerationRateLimitError,
            AiGenerationParsingError,
        ]:
            e = exc_cls("test message")
            assert "test message" in str(e)


# ---------------------------------------------------------------------------
# WisDev emitter-backed output helpers
# ---------------------------------------------------------------------------


class TestWisDevEmitterBackedOutputs:
    def test_emit_output_shapes_for_resolve_citations(self, svc):
        result = svc.emit_wisdev_action_output(
            "research.resolveCanonicalCitations",
            {
                "canonicalSources": [{"id": "p1", "title": "Paper 1", "doi": "10.1/p1"}],
                "resolvedCount": 1,
            },
        )
        assert isinstance(result["canonicalSources"], list)
        assert isinstance(result["citations"], list)
        assert isinstance(result["resolvedCount"], int)
        assert isinstance(result["duplicateCount"], int)

    def test_emit_output_shapes_for_verify_citations(self, svc):
        result = svc.emit_wisdev_action_output(
            "research.verifyCitations",
            {"verifiedRecords": [{"id": "p1", "title": "Paper 1"}], "validCount": 1},
        )
        assert isinstance(result["verifiedRecords"], list)
        assert isinstance(result["citations"], list)
        assert isinstance(result["validCount"], int)
        assert isinstance(result["invalidCount"], int)
        assert isinstance(result["duplicateCount"], int)

    def test_emit_output_shapes_for_reasoning_paths(self, svc):
        result = svc.emit_wisdev_action_output(
            "research.verifyReasoningPaths",
            {
                "branches": [{"claim": "h1", "supportScore": 0.8}],
                "reasoningVerification": {
                    "totalBranches": 1,
                    "verifiedBranches": 1,
                    "rejectedBranches": 0,
                    "readyForSynthesis": True,
                },
            },
        )
        assert isinstance(result["branches"], list)
        assert isinstance(result["reasoningVerification"], dict)
        assert set(result["reasoningVerification"].keys()) == {
            "totalBranches",
            "verifiedBranches",
            "rejectedBranches",
            "readyForSynthesis",
        }

    def test_emit_output_shapes_for_claim_evidence(self, svc):
        result = svc.emit_wisdev_action_output(
            "research.buildClaimEvidenceTable",
            {"table": "| C | E |", "rowCount": 2},
        )
        assert isinstance(result["claimEvidenceTable"], dict)
        assert set(result["claimEvidenceTable"].keys()) == {"table", "rowCount"}

    @pytest.mark.asyncio
    async def test_generate_wisdev_action_output_uses_emitters(self, svc):
        with patch.object(
            AiGenerationService,
            "generate_json",
            new=AsyncMock(
                return_value=ResolveCitationsModel.model_validate(
                    {
                        "canonicalSources": [{"id": "p1", "title": "P1", "doi": "10.1/p1"}],
                        "resolvedCount": 1,
                    }
                )
            ),
        ):
            result = await svc.generate_wisdev_action_output(
                action="research.resolveCanonicalCitations",
                prompt="prompt",
                response_model=ResolveCitationsModel,
            )

        assert isinstance(result["canonicalSources"], list)
        assert isinstance(result["citations"], list)
        assert result["canonicalSources"][0]["doi"] == "10.1/p1"
