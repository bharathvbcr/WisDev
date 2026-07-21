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
    
    # Set and Get
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
    
    # Mock embedding to be the same for both
    mock_vec = [0.1] * 768
    with patch.object(cache, "_get_embedding", return_value=mock_vec):
        await cache.set(query1, value)
        
        # Get query2 (should match query1 semantically)
        cached_val, is_semantic = await cache.get(query2)
        
        assert cached_val == value
        assert is_semantic is True
        assert cache.stats()["hits_semantic"] == 1

@pytest.mark.asyncio
async def test_cache_lru_eviction(cache):
    # Set max size small for test
    cache.MAX_MEMORY_CACHE_SIZE = 2
    
    await cache.set("q1", "v1", store_embedding=False)
    await cache.set("q2", "v2", store_embedding=False)
    await cache.set("q3", "v3", store_embedding=False) # Should evict q1
    
    assert await cache.get("q1") is None
    assert (await cache.get("q2"))[0] == "v2"
    assert (await cache.get("q3"))[0] == "v3"

@pytest.mark.asyncio
async def test_cache_redis_fallback():
    # Test with redis failing
    cache = SemanticCache(redis_url="redis://localhost:1234")
    
    with patch("redis.asyncio.from_url") as mock_redis:
        mock_redis.side_effect = Exception("Connection failed")
        
        # Should still work via memory
        await cache.set("q", "v", store_embedding=False)
        val, _ = await cache.get("q")
        assert val == "v"
        assert cache.stats()["has_redis"] is False

def test_cache_stats(cache):
    stats = cache.stats()
    assert stats["namespace"] == "test_namespace"
    assert stats["hit_rate"] == 0.0
