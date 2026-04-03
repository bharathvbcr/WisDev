import pytest
import os
import json
import asyncio
from unittest.mock import MagicMock, patch, AsyncMock
from services.semantic_cache import SemanticCache, CacheEntry


@pytest.mark.asyncio
async def test_semantic_cache_init():
    cache = SemanticCache(namespace="test")
    assert cache.namespace == "test"


@pytest.mark.asyncio
async def test_semantic_cache_get_set_memory():
    cache = SemanticCache(namespace="test")
    query = "test query"
    value = {"ans": 42}

    with patch.object(cache, "_get_embedding", AsyncMock(return_value=[0.1] * 768)):
        await cache.set(query, value)
        res, is_semantic = await cache.get(query)
        assert res == value
        assert is_semantic is False

        with patch.object(
            cache, "_get_embedding", AsyncMock(return_value=[0.11] * 768)
        ):
            res2, is_semantic2 = await cache.get("similar query")
            assert res2 == value
            assert is_semantic2 is True


@pytest.mark.asyncio
async def test_semantic_cache_delete_clear():
    cache = SemanticCache(namespace="test")
    await cache.set("q1", "v1", store_embedding=False)
    assert len(cache._memory_cache) == 1

    await cache.delete("q1")
    assert len(cache._memory_cache) == 0

    await cache.set("q2", "v2", store_embedding=False)
    await cache.clear_namespace()
    assert len(cache._memory_cache) == 0


@pytest.mark.asyncio
async def test_semantic_cache_lru_eviction():
    cache = SemanticCache(namespace="test")
    cache.MAX_MEMORY_CACHE_SIZE = 2

    with patch.object(cache, "_get_embedding", AsyncMock(return_value=None)):
        await cache.set("q1", "v1")
        await cache.set("q2", "v2")
        await cache.set("q3", "v3")

        assert len(cache._memory_cache) == 2
        assert cache._make_key(cache._hash_query("q1")) not in cache._memory_cache


@pytest.mark.asyncio
async def test_get_embedding_no_api_url():
    cache = SemanticCache(namespace="test")
    res = await cache._get_embedding("text")
    assert res is None


@pytest.mark.asyncio
async def test_cosine_similarity():
    cache = SemanticCache(namespace="test")
    vec1 = [1.0, 0.0, 0.0]
    vec2 = [1.0, 0.0, 0.0]
    assert cache._cosine_similarity(vec1, vec2) == 1.0

    vec3 = [0.0, 1.0, 0.0]
    assert cache._cosine_similarity(vec1, vec3) == 0.0


def test_semantic_cache_stats():
    cache = SemanticCache(namespace="test")
    stats = cache.stats()
    assert stats["namespace"] == "test"
    assert stats["total_requests"] == 0
