from unittest.mock import AsyncMock, patch

import pytest

from services.embedding_service import AzureEmbeddingBadRequest, EmbeddingService


@pytest.mark.asyncio
async def test_embed_batch_recovers_by_splitting_bad_batch():
    service = EmbeddingService()
    service.provider = "azure_openai"
    service.endpoint = "https://example.openai.azure.com"
    service.api_key = "test-key"
    service.deployment = "embed-prod"
    service.output_dimension = 3

    calls: list[list[str]] = []

    async def fake_request(texts: list[str]) -> list[list[float]]:
        calls.append(list(texts))
        if len(texts) > 1 and "bad" in texts:
            raise AzureEmbeddingBadRequest("model_error")
        return [[float(index + 1)] * 3 for index, _ in enumerate(texts)]

    with patch.object(service, "_request_embeddings", AsyncMock(side_effect=fake_request)):
        result = await service.embed_batch_async(["good", "bad", "fine"])

    assert len(result) == 3
    assert result[0] == [1.0, 1.0, 1.0]
    assert result[1] == [1.0, 1.0, 1.0]
    assert result[2] == [1.0, 1.0, 1.0]
    assert calls[0] == ["good", "bad", "fine"]
    assert ["bad"] in calls
    await service.close()


@pytest.mark.asyncio
async def test_embed_batch_returns_zero_vector_after_persistent_single_item_failure():
    service = EmbeddingService()
    service.provider = "azure_openai"
    service.endpoint = "https://example.openai.azure.com"
    service.api_key = "test-key"
    service.deployment = "embed-prod"
    service.output_dimension = 4

    with patch.object(
        service,
        "_request_embeddings",
        AsyncMock(side_effect=AzureEmbeddingBadRequest("model_error")),
    ):
        result = await service.embed_batch_async(["still bad"])

    assert result == [[0.0, 0.0, 0.0, 0.0]]
    await service.close()
