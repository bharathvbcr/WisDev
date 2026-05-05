"""Additional branch tests for services/embedding_service.py."""

from __future__ import annotations

from types import SimpleNamespace
from unittest.mock import AsyncMock, MagicMock, patch

import httpx
import pytest

from services import embedding_service as embedding_module
from services.embedding_service import AzureEmbeddingBadRequest, EmbeddingService


@pytest.mark.asyncio
async def test_request_embeddings_retries_on_429_and_returns_result(monkeypatch):
    service = EmbeddingService()
    service.endpoint = "https://example.openai.azure.com"
    service.api_key = "test-key"
    service.deployment = "embed-prod"
    service.output_dimension = 3

    resp429 = MagicMock(status_code=429, json=lambda: {"error": {"message": "throttle"}})
    resp200 = MagicMock(
        status_code=200,
        json=lambda: {
            "data": [
                {"index": 0, "embedding": [1, 1, 1]},
            ]
        },
    )
    resp200.raise_for_status = MagicMock()
    resp429.raise_for_status = MagicMock()

    service.client = MagicMock()
    service.client.post = AsyncMock(side_effect=[resp429, resp200])

    with patch("services.embedding_service.asyncio.sleep", AsyncMock()):
        vectors = await service._request_embeddings(["hello"])

    assert vectors == [[1, 1, 1]]


@pytest.mark.asyncio
async def test_request_embeddings_maps_bad_request_to_custom_error(monkeypatch):
    service = EmbeddingService()
    service.endpoint = "https://example.openai.azure.com"
    service.api_key = "test-key"
    service.deployment = "embed-prod"
    service.output_dimension = 3

    resp = MagicMock(
        status_code=400,
        json=lambda: {"error": {"message": "invalid input"}},
        text="err",
    )
    service.client = MagicMock()
    service.client.post = AsyncMock(return_value=resp)

    with pytest.raises(AzureEmbeddingBadRequest):
        await service._request_embeddings(["bad"])


@pytest.mark.asyncio
async def test_request_embeddings_raises_on_invalid_shape(monkeypatch):
    service = EmbeddingService()
    service.endpoint = "https://example.openai.azure.com"
    service.api_key = "test-key"
    service.deployment = "embed-prod"
    service.output_dimension = 3

    resp = MagicMock(status_code=200, json=lambda: {"not": "data"}, text="invalid")
    resp.raise_for_status = MagicMock()
    service.client = MagicMock()
    service.client.post = AsyncMock(return_value=resp)

    with pytest.raises(RuntimeError):
        await service._request_embeddings(["hello"])


@pytest.mark.asyncio
async def test_embed_batch_via_azure_returns_zero_vectors_for_empty_inputs():
    service = EmbeddingService()
    service.provider = "azure_openai"
    service.endpoint = "https://example.openai.azure.com"
    service.api_key = "test-key"
    service.deployment = "embed-prod"
    result = await service._embed_batch_via_azure(["", "   "])
    assert result == [[0.0] * service.output_dimension, [0.0] * service.output_dimension]


@pytest.mark.asyncio
async def test_embed_batch_via_azure_raises_when_service_not_ready():
    service = EmbeddingService()
    service.provider = "azure_openai"
    service.endpoint = ""
    service.api_key = ""
    service.deployment = ""

    with pytest.raises(RuntimeError, match="Azure OpenAI embedding is not configured"):
        await service._embed_batch_via_azure(["hello"])


@pytest.mark.asyncio
async def test_embed_batch_via_gemini_skips_empty_chunks():
    service = EmbeddingService()
    service.provider = "gemini"

    with patch("services.embedding_service.gemini_service") as gemini_service:
        gemini_service.embed_batch = AsyncMock(return_value=[[0.4, 0.5]])
        result = await service._embed_batch_via_gemini(["", "text"])

    assert result == [[0.0] * service.output_dimension, [0.4, 0.5]]


def test_count_tokens_no_encoding_fallback():
    service = EmbeddingService()
    service._encoding = None
    assert service.count_tokens("abcd") == 1
    assert service.count_tokens("a " * 10) == 4


def test_truncate_without_encoding_trims_by_char_count():
    service = EmbeddingService()
    service._encoding = None
    assert service._truncate_to_tokens("abcdef", max_tokens=1) == "abcd"
    assert service._truncate_to_tokens("abcdefgh", max_tokens=2) == "abcdefgh"


def test_prepare_text_uses_max_input_tokens():
    service = EmbeddingService()
    service._encoding = None
    text = "a " * 1000
    truncated = service._prepare_text(text)
    assert len(truncated) <= service.max_input_tokens * 4


def test_iter_recovery_candidates_ignores_non_shrinking_values():
    service = EmbeddingService()
    service.max_input_tokens = 8
    service._encoding = None

    candidates = list(service._iter_recovery_candidates("token " * 100))
    assert candidates  # non-empty
    assert len(candidates) == len(set(candidates))


def test_load_encoding_returns_none_when_tiktoken_missing(monkeypatch):
    monkeypatch.setattr(embedding_module, "tiktoken", None)
    assert embedding_module._load_encoding() is None


def test_load_encoding_falls_back_to_cl100k_base(monkeypatch):
    fake_tiktoken = SimpleNamespace(
        encoding_for_model=MagicMock(side_effect=RuntimeError("bad model")),
        get_encoding=MagicMock(return_value="fallback-encoding"),
    )
    monkeypatch.setattr(embedding_module, "tiktoken", fake_tiktoken)
    assert embedding_module._load_encoding() == "fallback-encoding"


def test_is_ready_uses_gemini_service_when_not_using_azure():
    service = EmbeddingService()
    service.provider = "gemini"

    with patch("services.embedding_service.gemini_service.is_ready", return_value=False):
        assert service.is_ready() is False


def test_count_tokens_returns_zero_for_blank_input():
    service = EmbeddingService()
    assert service.count_tokens(" \n\t ") == 0


@pytest.mark.asyncio
async def test_embed_single_async_returns_zero_vector_when_batch_is_empty():
    service = EmbeddingService()

    with patch.object(service, "embed_batch_async", AsyncMock(return_value=[])):
        vector = await service.embed_single_async("query")

    assert vector == [0.0] * service.output_dimension


@pytest.mark.asyncio
async def test_embed_batch_async_returns_empty_when_no_texts():
    service = EmbeddingService()
    assert await service.embed_batch_async([]) == []


@pytest.mark.asyncio
async def test_embed_batch_async_routes_to_gemini_provider():
    service = EmbeddingService()
    service.provider = "gemini"

    with patch.object(service, "_embed_batch_via_gemini", AsyncMock(return_value=[[1.0]])) as mocked:
        result = await service.embed_batch_async(["text"])

    assert result == [[1.0]]
    mocked.assert_awaited_once()


@pytest.mark.asyncio
async def test_embed_batch_via_gemini_returns_zero_vectors_when_all_inputs_blank():
    service = EmbeddingService()
    result = await service._embed_batch_via_gemini(["", "   "])
    assert result == [[0.0] * service.output_dimension, [0.0] * service.output_dimension]


@pytest.mark.asyncio
async def test_recover_single_embedding_returns_first_successful_candidate():
    service = EmbeddingService()
    item = embedding_module._PendingEmbedding(index=2, text="very long text")

    with patch.object(service, "_iter_recovery_candidates", return_value=iter(["bad", "good"])):
        with patch.object(
            service,
            "_request_embeddings",
            AsyncMock(side_effect=[AzureEmbeddingBadRequest("bad"), [[0.2, 0.4]]]),
        ):
            vector = await service._recover_single_embedding(item, AzureEmbeddingBadRequest("orig"))

    assert vector == [0.2, 0.4]


@pytest.mark.asyncio
async def test_request_embeddings_retries_on_http_error_then_succeeds():
    service = EmbeddingService()
    service.endpoint = "https://example.openai.azure.com"
    service.api_key = "test-key"
    service.deployment = "embed-prod"

    response = MagicMock(
        status_code=200,
        json=lambda: {"data": [{"index": 0, "embedding": [0.3, 0.6]}]},
    )
    response.raise_for_status = MagicMock()

    service.client = MagicMock()
    service.client.post = AsyncMock(
        side_effect=[httpx.HTTPError("net"), httpx.HTTPError("net"), response]
    )

    with patch("services.embedding_service.asyncio.sleep", AsyncMock()):
        vectors = await service._request_embeddings(["hello"])

    assert vectors == [[0.3, 0.6]]


@pytest.mark.asyncio
async def test_request_embeddings_raises_after_http_error_retries_exhausted():
    service = EmbeddingService()
    service.endpoint = "https://example.openai.azure.com"
    service.api_key = "test-key"
    service.deployment = "embed-prod"

    service.client = MagicMock()
    service.client.post = AsyncMock(side_effect=httpx.HTTPError("network down"))

    with patch("services.embedding_service.asyncio.sleep", AsyncMock()):
        with pytest.raises(RuntimeError, match="network down"):
            await service._request_embeddings(["hello"])


@pytest.mark.asyncio
async def test_request_embeddings_raises_after_retryable_status_exhausted():
    service = EmbeddingService()
    service.endpoint = "https://example.openai.azure.com"
    service.api_key = "test-key"
    service.deployment = "embed-prod"

    response = MagicMock(
        status_code=500,
        json=lambda: {"error": {"message": "server error"}},
        text="server error",
    )
    response.raise_for_status = MagicMock()

    service.client = MagicMock()
    service.client.post = AsyncMock(return_value=response)

    with patch("services.embedding_service.asyncio.sleep", AsyncMock()):
        with pytest.raises(RuntimeError, match="server error"):
            await service._request_embeddings(["hello"])


@pytest.mark.asyncio
async def test_request_embeddings_raises_on_embedding_shape_mismatch():
    service = EmbeddingService()
    service.endpoint = "https://example.openai.azure.com"
    service.api_key = "test-key"
    service.deployment = "embed-prod"

    response = MagicMock(
        status_code=200,
        json=lambda: {"data": [{"index": 0, "embedding": [0.1]}]},
    )
    response.raise_for_status = MagicMock()
    service.client = MagicMock()
    service.client.post = AsyncMock(return_value=response)

    with pytest.raises(RuntimeError, match="shape mismatch"):
        await service._request_embeddings(["hello", "world"])


def test_truncate_to_tokens_returns_empty_for_non_positive_budget():
    service = EmbeddingService()
    assert service._truncate_to_tokens("abcdef", 0) == ""


def test_extract_error_message_falls_back_to_response_text():
    response = MagicMock()
    response.json.side_effect = ValueError("bad json")
    response.text = "plain text failure"
    assert EmbeddingService._extract_error_message(response) == "plain text failure"


def test_extract_error_message_handles_non_dict_error_payload():
    response = MagicMock()
    response.json.return_value = {"error": "simple error"}
    assert EmbeddingService._extract_error_message(response) == "simple error"
