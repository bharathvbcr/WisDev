import pytest
import os
import json
import asyncio
from unittest.mock import MagicMock, patch, AsyncMock
from services.semantic_cache import SemanticCache, CacheEntry

@pytest.mark.asyncio
async def test_semantic_cache_init():
    cache = SemanticCache(redis_url=None, namespace="test")
    assert cache.namespace == "test"
    assert cache._redis_client is None

@pytest.mark.asyncio
async def test_get_redis_upstash():
    with patch.dict(os.environ, {"UPSTASH_REDIS_URL": "http://upstash"}):
        cache = SemanticCache(namespace="test")
        with patch("upstash_redis.asyncio.Redis.from_env") as mock_from_env:
            mock_client = AsyncMock()
            mock_from_env.return_value = mock_client
            client = await cache._get_redis()
            assert client == mock_client
            assert mock_client.ping.called

@pytest.mark.asyncio
async def test_get_redis_standard():
    cache = SemanticCache(redis_url="redis://localhost", namespace="test")
    with patch("redis.asyncio.from_url") as mock_from_url:
        mock_client = AsyncMock()
        mock_from_url.return_value = mock_client
        client = await cache._get_redis()
        assert client == mock_client

@pytest.mark.asyncio
async def test_semantic_cache_get_set_memory():
    cache = SemanticCache(redis_url=None, namespace="test")
    query = "test query"
    value = {"ans": 42}
    
    # Mock embedding
    with patch.object(cache, "_get_embedding", AsyncMock(return_value=[0.1]*768)):
        await cache.set(query, value)
        
        # Exact match
        res, is_semantic = await cache.get(query)
        assert res == value
        assert is_semantic is False
        
        # Semantic match
        with patch.object(cache, "_get_embedding", AsyncMock(return_value=[0.11]*768)):
            # similarity will be high
            res2, is_semantic2 = await cache.get("similar query")
            assert res2 == value
            assert is_semantic2 is True

@pytest.mark.asyncio
async def test_semantic_cache_redis_hit():
    cache = SemanticCache(redis_url="redis://localhost", namespace="test")
    mock_redis = AsyncMock()
    cache._redis_client = mock_redis
    
    mock_redis.get.return_value = json.dumps({"value": "cached", "embedding_version": cache.EMBEDDING_VERSION})
    
    res, is_semantic = await cache.get("query")
    assert res == "cached"
    assert is_semantic is False

@pytest.mark.asyncio
async def test_semantic_cache_delete_clear():
    cache = SemanticCache(redis_url=None, namespace="test")
    await cache.set("q1", "v1")
    assert "test:" in list(cache._memory_cache.keys())[0]
    
    await cache.delete("q1")
    assert len(cache._memory_cache) == 0
    
    await cache.set("q2", "v2")
    await cache.clear_namespace()
    assert len(cache._memory_cache) == 0

@pytest.mark.asyncio
async def test_semantic_cache_lru_eviction():
    cache = SemanticCache(namespace="test")
    cache.MAX_MEMORY_CACHE_SIZE = 2
    
    with patch.object(cache, "_get_embedding", AsyncMock(return_value=None)):
        await cache.set("q1", "v1")
        await cache.set("q2", "v2")
        await cache.set("q3", "v3") # Should evict q1
        
        assert len(cache._memory_cache) == 2
        assert cache._make_key(cache._hash_query("q1")) not in cache._memory_cache

@pytest.mark.asyncio
async def test_get_embedding_failure():
    cache = SemanticCache(namespace="test")
    with patch("services.semantic_cache.gemini_service.embed", AsyncMock(side_effect=Exception("embedding error"))):
        res = await cache._get_embedding("text")
        assert res is None

@pytest.mark.asyncio
async def test_get_embedding_non_200():
    cache = SemanticCache(namespace="test")
    with patch("services.semantic_cache.gemini_service.embed", AsyncMock(return_value=[])):
        res = await cache._get_embedding("text")
        assert res is None

def test_semantic_cache_stats():
    cache = SemanticCache(namespace="test")
    stats = cache.stats()
    assert stats["namespace"] == "test"
    assert stats["total_requests"] == 0
