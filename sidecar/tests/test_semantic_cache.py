import pytest
import asyncio
from unittest.mock import AsyncMock, patch, MagicMock
from services.semantic_cache import SemanticCache, CacheEntry


@pytest.fixture
def cache():
    return SemanticCache(namespace="test_namespace")


@pytest.mark.asyncio
async def test_cache_exact_hit_memory(cache):
    query = "What is AI?"
    value = {"result": "intelligence"}

    await cache.set(query, value, store_embedding=False)
    cached_val, is_semantic = await cache.get(query, use_semantic=False)

    assert cached_val == value
    assert is_semantic is False
    assert cache.stats()["hits_exact"] == 1


@pytest.mark.asyncio
async def test_cache_semantic_hit_memory(cache):
    query1 = "AI in cancer"
    query2 = "Artificial intelligence in oncology"
    value = {"result": "medical AI"}

    mock_vec = [0.1] * 768
    with patch.object(cache, "_get_embedding", return_value=mock_vec):
        await cache.set(query1, value)
        cached_val, is_semantic = await cache.get(query2)
        assert cached_val == value
        assert is_semantic is True
        assert cache.stats()["hits_semantic"] == 1


@pytest.mark.asyncio
async def test_cache_lru_eviction(cache):
    cache.MAX_MEMORY_CACHE_SIZE = 2

    await cache.set("q1", "v1", store_embedding=False)
    await cache.set("q2", "v2", store_embedding=False)
    await cache.set("q3", "v3", store_embedding=False)

    assert await cache.get("q1") is None
    assert (await cache.get("q2"))[0] == "v2"
    assert (await cache.get("q3"))[0] == "v3"


@pytest.mark.asyncio
async def test_cache_redis_fallback():
    pytest.skip("Redis removed in open-source version")


def test_cache_stats(cache):
    stats = cache.stats()
    assert stats["namespace"] == "test_namespace"
    assert stats["hit_rate"] == 0.0
