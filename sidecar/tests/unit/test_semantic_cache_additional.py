"""Additional tests for services/semantic_cache.py edge cases."""

from __future__ import annotations

import asyncio
import json

import pytest
from unittest.mock import AsyncMock, patch

from services.semantic_cache import CacheEntry, SemanticCache


def make_cache(**kwargs) -> SemanticCache:
    defaults = dict(redis_url="redis://localhost:6379", namespace="test-ns")
    defaults.update(kwargs)
    return SemanticCache(**defaults)


def _fake_redis_with(*, scans, gets):
    fake = AsyncMock()
    scan_iter = iter(scans)
    get_iter = iter(gets)

    async def scan(cursor, match, count):
        return next(scan_iter)

    async def get(key):
        return next(get_iter)

    async def setex(key, ttl, value):
        return True

    async def delete(*keys):
        return len(keys)

    fake.scan = scan
    fake.get = get
    fake.setex = setex
    fake.delete = delete
    fake.ping = AsyncMock(return_value=True)
    return fake


def test_warm_from_redis_loads_cached_embeddings():
    cache = make_cache()
    key = cache._make_key("abc")
    payload = {
        "value": {"answer": 1},
        "embedding": [0.1, 0.2, 0.3],
        "embedding_family": cache.EMBEDDING_FAMILY,
        "embedding_version": cache.EMBEDDING_VERSION,
        "created_at": 1.0,
    }

    redis = _fake_redis_with(scans=[(0, [key])], gets=[json.dumps(payload)])
    cache._memory_cache.clear()

    async def run():
        with patch.object(cache, "_get_redis", AsyncMock(return_value=redis)):
            await cache._warm_semantic_index_from_redis()
        assert key in cache._memory_cache
        assert cache._semantic_index_warmed is True

    asyncio.run(run())


def test_warm_from_redis_skips_existing_empty_and_invalid_entries_and_evicts_oldest():
    cache = make_cache()
    cache.MAX_MEMORY_CACHE_SIZE = 1
    existing_key = cache._make_key("existing")
    new_key = cache._make_key("new")
    empty_key = cache._make_key("empty")
    invalid_key = cache._make_key("invalid")
    cache._memory_cache[existing_key] = CacheEntry(
        key=existing_key,
        value={"answer": 0},
        embedding=[0.1],
        embedding_family=cache.EMBEDDING_FAMILY,
        embedding_version=cache.EMBEDDING_VERSION,
        created_at=1.0,
        ttl_seconds=cache.DEFAULT_TTL_SECONDS,
    )

    redis = _fake_redis_with(
        scans=[(0, [existing_key, empty_key, invalid_key, new_key])],
        gets=[
            json.dumps({"value": {"answer": 2}, "embedding": []}),
            "not-json",
            json.dumps(
                {
                    "value": {"answer": 3},
                    "embedding": [0.2, 0.3],
                    "embedding_family": cache.EMBEDDING_FAMILY,
                    "embedding_version": cache.EMBEDDING_VERSION,
                    "created_at": 2.0,
                }
            ),
        ],
    )

    async def run():
        with patch.object(cache, "_get_redis", AsyncMock(return_value=redis)):
            await cache._warm_semantic_index_from_redis()
        assert existing_key not in cache._memory_cache
        assert new_key in cache._memory_cache

    asyncio.run(run())


def test_warm_from_redis_handles_scan_failure():
    cache = make_cache()
    redis = AsyncMock()
    redis.scan = AsyncMock(side_effect=RuntimeError("scan failed"))

    async def run():
        with patch.object(cache, "_get_redis", AsyncMock(return_value=redis)):
            await cache._warm_semantic_index_from_redis()
        assert cache._semantic_index_warmed is True

    asyncio.run(run())


def test_get_exact_hit_with_embedding_version_mismatch_returns_none():
    cache = make_cache()
    payload = {"value": {"answer": 2}, "embedding_version": "legacy"}
    redis = _fake_redis_with(scans=[(0, [])], gets=[json.dumps(payload)])

    async def run():
        cache._memory_cache.clear()
        with patch.object(cache, "_get_redis", AsyncMock(return_value=redis)):
            result = await cache.get("version mismatch query", use_semantic=False)
        assert result is None

    asyncio.run(run())


def test_set_writes_to_redis_and_memory():
    cache = make_cache()
    cache._memory_cache.clear()
    redis = _fake_redis_with(scans=[(0, [])], gets=[])

    async def run():
        with patch.object(cache, "_get_redis", AsyncMock(return_value=redis)):
            with patch.object(cache, "_get_embedding", AsyncMock(return_value=[0.5, 0.6])):
                ok = await cache.set("stored in redis", {"k": "v"})
        assert ok is True
        key = cache._make_key(cache._hash_query("stored in redis"))
        assert key in cache._memory_cache

    asyncio.run(run())


def test_delete_removes_cache_entry():
    cache = make_cache()
    cache._memory_cache[cache._make_key(cache._hash_query("delq"))] = {
        "value": "v"
    }
    redis = _fake_redis_with(scans=[(0, [])], gets=[])

    async def run():
        with patch.object(cache, "_get_redis", AsyncMock(return_value=redis)):
            await cache.delete("delq")
        assert cache._make_key(cache._hash_query("delq")) not in cache._memory_cache

    asyncio.run(run())


def test_clear_namespace_with_redis_delete_keys():
    cache = make_cache()
    k1 = cache._make_key("one")
    k2 = cache._make_key("two")
    cache._memory_cache[k1] = object()
    cache._memory_cache[k2] = object()

    fake = AsyncMock()

    async def scan(cursor, match, count):
        return (0, [k1, k2])

    async def delete(*keys):
        return len(keys)

    fake.scan = scan
    fake.delete = delete
    fake.ping = AsyncMock(return_value=True)

    async def run():
        with patch.object(cache, "_get_redis", AsyncMock(return_value=fake)):
            count = await cache.clear_namespace()
        assert count >= 2
        assert cache._memory_cache == {}

    asyncio.run(run())


def test_stats_hits_reflect_counters():
    cache = make_cache()
    cache._memory_cache[cache._make_key("q")] = cache._memory_cache.get(cache._make_key("q"), None) or object()
    cache._hits_exact = 2
    cache._hits_semantic = 3
    cache._total_requests = 10
    stats = cache.stats()
    assert stats["hits_exact"] == 2
    assert stats["hits_semantic"] == 3
    assert stats["hit_rate"] == 0.5


def test_get_exact_hit_uses_redis_and_tracks_hit_count():
    cache = make_cache()
    query = "redis-hit"
    key = cache._make_key(cache._hash_query(query))
    payload = {
        "value": {"answer": 7},
        "embedding": [0.1, 0.2],
        "embedding_version": cache.EMBEDDING_VERSION,
        "created_at": 1.0,
    }
    redis = _fake_redis_with(scans=[(0, [key])], gets=[json.dumps(payload)])

    async def run():
        with patch.object(cache, "_get_redis", AsyncMock(return_value=redis)):
            result = await cache.get(query, use_semantic=False)

        assert result == ({"answer": 7}, False)
        assert cache._hits_exact == 1
        assert key in cache._memory_cache

    import asyncio

    asyncio.run(run())


def test_get_exact_hit_repopulates_memory_and_evicts_oldest():
    cache = make_cache()
    cache.MAX_MEMORY_CACHE_SIZE = 1
    old_key = cache._make_key("old")
    cache._memory_cache[old_key] = CacheEntry(
        key=old_key,
        value={"old": True},
        embedding=[0.1],
        embedding_family=cache.EMBEDDING_FAMILY,
        embedding_version=cache.EMBEDDING_VERSION,
        created_at=1.0,
        ttl_seconds=cache.DEFAULT_TTL_SECONDS,
    )

    query = "redis-hit-new"
    key = cache._make_key(cache._hash_query(query))
    payload = {
        "value": {"answer": 7},
        "embedding": [0.1, 0.2],
        "embedding_version": cache.EMBEDDING_VERSION,
        "created_at": 1.0,
    }
    redis = _fake_redis_with(scans=[(0, [])], gets=[json.dumps(payload)])

    async def run():
        with patch.object(cache, "_get_redis", AsyncMock(return_value=redis)):
            result = await cache.get(query, use_semantic=False)

        assert result == ({"answer": 7}, False)
        assert old_key not in cache._memory_cache
        assert key in cache._memory_cache

    asyncio.run(run())


def test_get_handles_redis_get_error_and_memory_semantic_version_mismatch():
    cache = make_cache()
    query = "semantic-query"
    key = cache._make_key(cache._hash_query("cached-query"))
    cache._memory_cache[key] = CacheEntry(
        key=key,
        value={"cached": True},
        embedding=[1.0, 0.0],
        embedding_family=cache.EMBEDDING_FAMILY,
        embedding_version="legacy",
        created_at=1.0,
        ttl_seconds=cache.DEFAULT_TTL_SECONDS,
    )

    redis = AsyncMock()
    redis.get = AsyncMock(side_effect=RuntimeError("redis get failed"))

    async def run():
        with patch.object(cache, "_get_redis", AsyncMock(return_value=redis)):
            with patch.object(cache, "_get_embedding", AsyncMock(return_value=[1.0, 0.0])):
                result = await cache.get(query, use_semantic=True)
        assert result is None

    asyncio.run(run())


def test_get_exact_hit_rejects_legacy_embedding_version_in_memory():
    cache = make_cache()
    query = "legacy-query"
    key = cache._make_key(cache._hash_query(query))
    cache._memory_cache[key] = CacheEntry(
        key=key,
        value={"v": "legacy"},
        embedding=[0.1],
        embedding_family=cache.EMBEDDING_FAMILY,
        embedding_version="legacy",
        created_at=1.0,
        ttl_seconds=cache.DEFAULT_TTL_SECONDS,
    )
    async def run():
        result = await cache.get(query, use_semantic=False)
        assert result is None
    import asyncio

    asyncio.run(run())


def test_warm_from_redis_skips_invalid_entries():
    cache = make_cache()
    key = cache._make_key("bad-json")
    redis = _fake_redis_with(scans=[(0, [key])], gets=["not-json"])

    async def run():
        with patch.object(cache, "_get_redis", AsyncMock(return_value=redis)):
            await cache._warm_semantic_index_from_redis()
        assert key not in cache._memory_cache
        assert cache._semantic_index_warmed is True

    import asyncio

    asyncio.run(run())


def test_warm_from_redis_skips_empty_cached_entries():
    cache = make_cache()
    key = cache._make_key("empty")
    redis = _fake_redis_with(scans=[(0, [key])], gets=[""])

    async def run():
        with patch.object(cache, "_get_redis", AsyncMock(return_value=redis)):
            await cache._warm_semantic_index_from_redis()
        assert key not in cache._memory_cache
        assert cache._semantic_index_warmed is True

    asyncio.run(run())


def test_set_redis_failure_still_caches_in_memory():
    cache = make_cache()
    key = cache._make_key(cache._hash_query("query"))
    cache._memory_cache.clear()

    redis = AsyncMock()

    async def setex(_key, _ttl, _value):
        raise RuntimeError("redis write failed")

    redis.setex = setex

    async def scan(_cursor, _match, _count):
        return (0, [])

    redis.scan = scan
    redis.ping = AsyncMock(return_value=True)

    async def run():
        with patch.object(cache, "_get_redis", AsyncMock(return_value=redis)):
            with patch.object(cache, "_get_embedding", AsyncMock(return_value=[0.9, 0.1])):
                ok = await cache.set("query", {"value": "x"})

        assert ok is True
        assert key in cache._memory_cache

    import asyncio

    asyncio.run(run())


def test_delete_and_clear_namespace_handle_redis_errors():
    cache = make_cache()
    key = cache._make_key(cache._hash_query("query"))
    cache._memory_cache[key] = CacheEntry(
        key=key,
        value={"value": "x"},
        embedding=[0.9, 0.1],
        embedding_family=cache.EMBEDDING_FAMILY,
        embedding_version=cache.EMBEDDING_VERSION,
        created_at=1.0,
        ttl_seconds=cache.DEFAULT_TTL_SECONDS,
    )

    redis = AsyncMock()
    redis.delete = AsyncMock(side_effect=RuntimeError("redis delete failed"))
    redis.scan = AsyncMock(side_effect=RuntimeError("redis scan failed"))

    async def run():
        with patch.object(cache, "_get_redis", AsyncMock(return_value=redis)):
            assert await cache.delete("query") is True
            count = await cache.clear_namespace()
        assert count == 0

    asyncio.run(run())
