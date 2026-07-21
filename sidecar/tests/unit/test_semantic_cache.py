"""Tests for services/semantic_cache.py — all external I/O mocked."""

import hashlib
import time
import pytest
from unittest.mock import AsyncMock, MagicMock, patch

from services.semantic_cache import SemanticCache, CacheEntry


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def make_cache(**kwargs) -> SemanticCache:
    defaults = dict(redis_url=None, namespace="test")
    defaults.update(kwargs)
    return SemanticCache(**defaults)


# ---------------------------------------------------------------------------
# Initialization
# ---------------------------------------------------------------------------

class TestSemanticCacheInit:
    def test_no_redis_url(self):
        sc = make_cache(redis_url=None)
        assert sc.redis_url is None

    def test_redis_url_from_arg(self):
        sc = make_cache(redis_url="redis://localhost:6379")
        assert sc.redis_url == "redis://localhost:6379"

    def test_default_similarity_threshold(self):
        sc = make_cache()
        assert sc.similarity_threshold == SemanticCache.DEFAULT_SIMILARITY_THRESHOLD

    def test_custom_similarity_threshold(self):
        sc = make_cache(similarity_threshold=0.75)
        assert sc.similarity_threshold == 0.75

    def test_namespace_stored(self):
        sc = make_cache(namespace="myns")
        assert sc.namespace == "myns"

    def test_memory_cache_empty_on_init(self):
        sc = make_cache()
        assert sc._memory_cache == {}

    def test_hit_counters_zero(self):
        sc = make_cache()
        assert sc._hits_exact == 0
        assert sc._hits_semantic == 0
        assert sc._total_requests == 0


# ---------------------------------------------------------------------------
# _make_key / _hash_query
# ---------------------------------------------------------------------------

class TestHelpers:
    def test_make_key_includes_namespace(self):
        sc = make_cache(namespace="ns1")
        key = sc._make_key("mykey")
        assert key.startswith("ns1:")
        assert "mykey" in key

    def test_hash_query_deterministic(self):
        sc = make_cache()
        assert sc._hash_query("hello") == sc._hash_query("hello")

    def test_hash_query_case_insensitive(self):
        sc = make_cache()
        assert sc._hash_query("HELLO") == sc._hash_query("hello")

    def test_hash_query_strips_whitespace(self):
        sc = make_cache()
        assert sc._hash_query("  hello  ") == sc._hash_query("hello")

    def test_hash_query_length(self):
        sc = make_cache()
        h = sc._hash_query("test query")
        assert len(h) == 16  # first 16 chars of sha256 hex


# ---------------------------------------------------------------------------
# _cosine_similarity
# ---------------------------------------------------------------------------

class TestCosineSimilarity:
    def test_identical_vectors(self):
        sc = make_cache()
        v = [1.0, 2.0, 3.0]
        assert abs(sc._cosine_similarity(v, v) - 1.0) < 1e-6

    def test_orthogonal_vectors(self):
        sc = make_cache()
        assert sc._cosine_similarity([1.0, 0.0], [0.0, 1.0]) == pytest.approx(0.0)

    def test_opposite_vectors(self):
        sc = make_cache()
        result = sc._cosine_similarity([1.0, 0.0], [-1.0, 0.0])
        assert result == pytest.approx(-1.0)

    def test_zero_vector_returns_zero(self):
        sc = make_cache()
        assert sc._cosine_similarity([0.0, 0.0], [1.0, 2.0]) == 0.0

    def test_different_length_returns_zero(self):
        sc = make_cache()
        assert sc._cosine_similarity([1.0, 2.0], [1.0, 2.0, 3.0]) == 0.0

    def test_known_similarity(self):
        import math
        sc = make_cache()
        v1 = [1.0, 0.0]
        v2 = [1.0, 1.0]
        expected = 1.0 / math.sqrt(2)
        assert sc._cosine_similarity(v1, v2) == pytest.approx(expected, abs=1e-5)


# ---------------------------------------------------------------------------
# _get_redis (no Redis URL → returns None)
# ---------------------------------------------------------------------------

class TestGetRedis:
    @pytest.mark.asyncio
    async def test_no_redis_url_returns_none(self):
        sc = make_cache(redis_url=None)
        result = await sc._get_redis()
        assert result is None

    @pytest.mark.asyncio
    async def test_cached_client_returned(self):
        sc = make_cache(redis_url=None)
        mock_client = MagicMock()
        sc._redis_client = mock_client
        result = await sc._get_redis()
        assert result is mock_client

    @pytest.mark.asyncio
    async def test_connection_failure_returns_none(self, monkeypatch):
        sc = make_cache(redis_url="redis://localhost:9999")

        async def bad_ping():
            raise ConnectionError("refused")

        mock_client = MagicMock()
        mock_client.ping = bad_ping

        with patch("redis.asyncio.from_url", return_value=mock_client):
            result = await sc._get_redis()
        assert result is None


# ---------------------------------------------------------------------------
# _get_embedding
# ---------------------------------------------------------------------------

class TestGetEmbedding:
    @pytest.mark.asyncio
    async def test_successful_embedding(self):
        sc = make_cache()
        fake_embedding = [0.1] * 768
        with patch("services.semantic_cache.gemini_service.embed", AsyncMock(return_value=fake_embedding)):
            result = await sc._get_embedding("hello world")
        assert result == fake_embedding

    @pytest.mark.asyncio
    async def test_embedding_cached_in_memory(self):
        sc = make_cache()
        fake_embedding = [0.5] * 768
        with patch("services.semantic_cache.gemini_service.embed", AsyncMock(return_value=fake_embedding)) as mock_embed:
            await sc._get_embedding("test text")
            # Second call should use cache — embed called only once
            result = await sc._get_embedding("test text")
            assert mock_embed.await_count == 1
        assert result == fake_embedding

    @pytest.mark.asyncio
    async def test_non_200_returns_none(self):
        sc = make_cache()
        with patch("services.semantic_cache.gemini_service.embed", AsyncMock(return_value=[])):
            result = await sc._get_embedding("query")
        assert result is None

    @pytest.mark.asyncio
    async def test_exception_returns_none(self):
        sc = make_cache()
        with patch("services.semantic_cache.gemini_service.embed", AsyncMock(side_effect=Exception("embedding error"))):
            result = await sc._get_embedding("query")
        assert result is None

    @pytest.mark.asyncio
    async def test_wrong_embedding_version_returns_none(self):
        sc = make_cache()
        with patch("services.semantic_cache.gemini_service.embed", AsyncMock(return_value="not-a-list")):
            result = await sc._get_embedding("query")
        assert result is None


# ---------------------------------------------------------------------------
# get / set (memory path — no Redis)
# ---------------------------------------------------------------------------

class TestGetSetMemory:
    @pytest.mark.asyncio
    async def test_miss_returns_none(self):
        sc = make_cache()
        result = await sc.get("never cached query xyz")
        assert result is None

    @pytest.mark.asyncio
    async def test_set_then_get_exact(self):
        sc = make_cache()
        await sc.set("my query", {"answer": 42})
        result = await sc.get("my query", use_semantic=False)
        assert result is not None
        value, is_semantic = result
        assert value == {"answer": 42}
        assert is_semantic is False

    @pytest.mark.asyncio
    async def test_exact_hit_increments_counter(self):
        sc = make_cache()
        await sc.set("counter query", "response")
        await sc.get("counter query", use_semantic=False)
        assert sc._hits_exact == 1

    @pytest.mark.asyncio
    async def test_total_requests_incremented(self):
        sc = make_cache()
        await sc.get("any query")
        assert sc._total_requests == 1

    @pytest.mark.asyncio
    async def test_semantic_hit(self):
        sc = make_cache(similarity_threshold=0.5)
        # Store a value
        await sc.set("cancer research therapy", {"result": "found"})

        # Use a "similar" query — patch embeddings to be identical
        same_embedding = [1.0, 0.0, 0.0]
        with patch.object(sc, "_get_embedding", AsyncMock(return_value=same_embedding)):
            # Manually inject embedding into the cached entry
            hash_key = sc._make_key(sc._hash_query("cancer research therapy"))
            entry = sc._memory_cache[hash_key]
            entry.embedding = same_embedding

            result = await sc.get("cancer research treatment", use_semantic=True)

        assert result is not None
        value, is_semantic = result
        assert value == {"result": "found"}
        assert is_semantic is True

    @pytest.mark.asyncio
    async def test_lru_eviction_when_cache_full(self):
        sc = make_cache()
        # Fill cache beyond MAX_MEMORY_CACHE_SIZE — mock embedding so no HTTP calls
        with patch.object(sc, "_get_embedding", AsyncMock(return_value=None)):
            for i in range(SemanticCache.MAX_MEMORY_CACHE_SIZE + 5):
                await sc.set(f"query_{i}", f"value_{i}")
        assert len(sc._memory_cache) <= SemanticCache.MAX_MEMORY_CACHE_SIZE


# ---------------------------------------------------------------------------
# delete / clear
# ---------------------------------------------------------------------------

class TestDeleteClear:
    @pytest.mark.asyncio
    async def test_delete_removes_entry(self):
        sc = make_cache()
        await sc.set("key to delete", "value")
        await sc.delete("key to delete")
        result = await sc.get("key to delete", use_semantic=False)
        assert result is None

    @pytest.mark.asyncio
    async def test_clear_empties_memory_cache(self):
        sc = make_cache()
        with patch.object(sc, "_get_embedding", AsyncMock(return_value=None)):
            await sc.set("q1", "v1")
            await sc.set("q2", "v2")
        await sc.clear_namespace()
        assert sc._memory_cache == {}


# ---------------------------------------------------------------------------
# stats
# ---------------------------------------------------------------------------

class TestStats:
    def test_stats_structure(self):
        sc = make_cache()
        stats = sc.stats()
        assert "total_requests" in stats
        assert "hits_exact" in stats
        assert "hits_semantic" in stats
        assert "memory_entries" in stats

    @pytest.mark.asyncio
    async def test_stats_after_operations(self):
        sc = make_cache()
        with patch.object(sc, "_get_embedding", AsyncMock(return_value=None)):
            await sc.set("q", "v")
        await sc.get("q", use_semantic=False)  # exact hit
        stats = sc.stats()
        assert stats["hits_exact"] >= 1
        assert stats["memory_entries"] >= 1


# ---------------------------------------------------------------------------
# CacheEntry model
# ---------------------------------------------------------------------------

class TestCacheEntry:
    def test_cache_entry_fields(self):
        entry = CacheEntry(
            key="k1",
            value={"data": "test"},
            created_at=time.time(),
            ttl_seconds=3600,
        )
        assert entry.key == "k1"
        assert entry.embedding is None
        assert entry.ttl_seconds == 3600
