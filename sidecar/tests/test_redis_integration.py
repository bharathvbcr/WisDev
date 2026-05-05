import pytest
import json
from unittest.mock import AsyncMock, patch, MagicMock
from services.semantic_cache import SemanticCache

@pytest.mark.asyncio
async def test_redis_upstash_detection():
    """Verify that UpstashRedis is used when 'upstash' is in the URL."""
    redis_url = "https://fine-ant-123.upstash.io"
    
    with patch("upstash_redis.asyncio.Redis.from_env") as mock_upstash:
        mock_redis = AsyncMock()
        mock_upstash.return_value = mock_redis
        
        cache = SemanticCache(redis_url=redis_url)
        client = await cache._get_redis()
        
        assert client == mock_redis
        assert mock_upstash.called

@pytest.mark.asyncio
async def test_semantic_cache_redis_flow():
    """Test the full flow: set in Redis -> get from Redis."""
    cache = SemanticCache(redis_url="redis://localhost")
    mock_redis = AsyncMock()
    cache._redis_client = mock_redis
    
    query = "What is quantum computing?"
    value = {"answer": "A complex topic"}
    
    # 1. Mock setting
    await cache.set(query, value, store_embedding=False)
    assert mock_redis.setex.called
    
    # 2. Mock getting
    mock_redis.get.return_value = json.dumps({"value": value, "embedding": None})
    result, is_semantic = await cache.get(query, use_semantic=False)
    
    assert result == value
    assert is_semantic is False
    assert mock_redis.get.called
