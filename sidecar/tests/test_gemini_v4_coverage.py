import pytest
import json
import os
import httpx
from unittest.mock import AsyncMock, patch, MagicMock
from services.ai_generation_service import (
    AiGenerationService, ModelSelectionStrategy, AiGenerationRetryableError,
    AiGenerationRateLimitError, AiGenerationServiceError, AiGenerationParsingError,
    AiGenerationStructuredOutputRequiresNativeRuntimeError,
)
from pydantic import BaseModel

class MockModel(BaseModel):
    answer: str = "default"

@pytest.fixture
def service():
    return AiGenerationService()

@pytest.mark.asyncio
async def test_gemini_select_model(service):
    assert service._select_model(0.1, 0.1) == "light"
    assert service._select_model(0.5, 0.5) == "balanced"
    assert service._select_model(0.9, 0.9) == "heavy"
    assert service._select_model(0.1, 0.1, strict_domain=True) == "heavy"
    assert service._select_model(0.4, 0.35, remaining_budget_ratio=0.1) == "light"
    assert service._select_model(0.2, 0.2, historical_reward=0.1) == "balanced"
    assert service._select_model(0.5, strategy=ModelSelectionStrategy.ALWAYS_LIGHT) == "light"
    assert service._select_model(0.5, strategy=ModelSelectionStrategy.ALWAYS_BALANCED) == "balanced"
    assert service._select_model(0.5, strategy=ModelSelectionStrategy.ALWAYS_HEAVY) == "heavy"

def test_model_fallback_chain(service):
    assert service._model_fallback_chain("heavy") == ["heavy", "balanced", "light"]
    assert service._model_fallback_chain("balanced") == ["balanced", "light"]
    assert service._model_fallback_chain("light") == ["light"]

@pytest.mark.asyncio
async def test_generate_via_proxy_success(service):
    mock_resp = MagicMock()
    mock_resp.status_code = 200
    mock_resp.json.return_value = {"text": "Proxy OK"}
    mock_client = AsyncMock()
    mock_client.post.return_value = mock_resp
    mock_client.__aenter__.return_value = mock_client
    with patch("httpx.AsyncClient", return_value=mock_client):
        res = await service.generate("Hi")
        assert res == "Proxy OK"

@pytest.mark.asyncio
async def test_generate_json_native_path(service):
    service.native_structured_enabled = True
    from pydantic import BaseModel
    class MockModel(BaseModel):
        answer: str
    with patch.object(service, "_generate_native_structured", new_callable=AsyncMock) as mock_native:
        mock_native.return_value = MockModel(answer="42")
        res = await service.generate_json("Q", MockModel)
        assert res.answer == "42"

@pytest.mark.asyncio
async def test_generate_json_proxy_fallback(service):
    service.native_structured_enabled = False
    with patch.object(service, "_generate_via_vertex_proxy", new_callable=AsyncMock) as mock_proxy:
        mock_proxy.return_value = '{"answer":"proxy-42"}'
        result = await service.generate_json("Q", MockModel)
        assert result.answer == "proxy-42"
        assert mock_proxy.await_count >= 1

@pytest.mark.asyncio
async def test_generate_json_demotes_across_native_models(service):
    service.native_structured_enabled = True
    with patch.object(service, "_generate_native_structured", new_callable=AsyncMock) as mock_gen:
        mock_gen.side_effect = [
            AiGenerationRateLimitError("retry"),
            MockModel(answer="md-42"),
        ]
        with patch.object(service, "_generate_via_vertex_proxy", new_callable=AsyncMock) as mock_proxy:
            mock_proxy.return_value = '{"answer":"md-42"}'
            res = await service.generate_json(
                "Q",
                MockModel,
                strategy=ModelSelectionStrategy.ALWAYS_HEAVY,
            )
            assert res.answer == "md-42"
            assert mock_proxy.await_count >= 1

@pytest.mark.asyncio
async def test_generate_native_structured_details(service):
    mock_client = MagicMock()
    mock_resp = MagicMock()
    mock_resp.text = '{"answer": "42"}'
    mock_client.aio.models.generate_content = AsyncMock(return_value=mock_resp)
    with patch('google.genai.Client', return_value=mock_client):
        with patch('google.genai.types.GenerateContentConfig', side_effect=lambda **kwargs: kwargs):
            with patch.dict(os.environ, {"GOOGLE_CLOUD_PROJECT": "p1", "AI_MODEL_HEAVY_ID": "heavy-v1"}):
                res = await service._generate_native_structured("Q", MockModel, 0.7, 100, model_tier="heavy")
                assert res.answer == "42"

@pytest.mark.asyncio
async def test_generate_with_thinking_details(service):
    mock_client = MagicMock()
    mock_resp = MagicMock()
    mock_resp.text = '{"answer": "thought"}'
    mock_client.aio.models.generate_content = AsyncMock(return_value=mock_resp)
    with patch('google.genai.Client', return_value=mock_client):
        with patch('google.genai.types.GenerateContentConfig', side_effect=lambda **kwargs: kwargs):
            with patch('google.genai.types.ThinkingConfig', side_effect=lambda **kwargs: kwargs):
                with patch.dict(os.environ, {"GOOGLE_CLOUD_PROJECT": "p1", "AI_MODEL_THINKING_ID": "thought-v1"}):
                    res = await service.generate_with_thinking("Q", MockModel)
                    assert res.answer == "thought"

def test_estimate_complexity(service):
    query = "systematic review meta-analysis randomized controlled longitudinal cohort qualitative quantitative statistically significant confidence interval"
    assert service.estimate_complexity(query) > 0.5

@pytest.mark.asyncio
async def test_generate_json_rejects_truncated_proxy_payload(service):
    service.native_structured_enabled = True
    with patch.object(service, "_generate_native_structured", new_callable=AsyncMock) as mock_native:
        mock_native.side_effect = [AiGenerationServiceError("first")]
        with patch.object(service, "_generate_via_vertex_proxy", new_callable=AsyncMock) as mock_proxy:
            mock_proxy.return_value = '{"answer":"trunc"'
            with pytest.raises(AiGenerationParsingError):
                await service.generate_json(
                    "p",
                    MockModel,
                    strategy=ModelSelectionStrategy.ALWAYS_BALANCED,
                )
