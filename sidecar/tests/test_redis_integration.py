import pytest


@pytest.mark.asyncio
async def test_redis_upstash_detection():
    """Skipped: Redis removed in open-source version."""
    pytest.skip("Redis removed in open-source version")


@pytest.mark.asyncio
async def test_semantic_cache_redis_flow():
    """Skipped: Redis removed in open-source version."""
    pytest.skip("Redis removed in open-source version")
