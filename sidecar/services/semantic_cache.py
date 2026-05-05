"""
Semantic Cache Service
Caches AI-generated questions using embedding similarity.

Features:
- Embedding-based similarity matching (not just exact string match)
- Redis/Upstash for fast distributed cache
- TTL-based expiration (24 hours default)
- Fallback to in-memory cache if Redis unavailable
"""

import os
import json
import hashlib
from typing import Optional, Any, Generic, TypeVar
from datetime import timedelta

import structlog
from pydantic import BaseModel
from services.gemini_service import gemini_service

logger = structlog.get_logger(__name__)

T = TypeVar("T")


class CacheEntry(BaseModel, Generic[T]):
    """A cached entry with metadata."""

    key: str
    value: Any  # Will be serialized JSON
    embedding: Optional[list[float]] = None
    embedding_family: Optional[str] = None
    embedding_version: Optional[str] = None
    created_at: float
    ttl_seconds: int


class SemanticCache:
    """
    Semantic similarity cache for query results.

    Uses text embeddings to find similar queries and return cached results.
    Falls back to exact match if embeddings unavailable.
    """

    # Default similarity threshold for cache hits (0.85 = 85% similar)
    DEFAULT_SIMILARITY_THRESHOLD = 0.85
    DEFAULT_TTL_SECONDS = 86400  # 24 hours
    MAX_MEMORY_CACHE_SIZE = 1000  # LRU eviction above this size
    EMBEDDING_FAMILY = "gemini"
    EMBEDDING_VERSION = "text-embedding-005-v1"

    def __init__(
        self,
        redis_url: Optional[str] = None,
        namespace: str = "wisdev",
        embedding_model: str = "text-embedding-005",
        similarity_threshold: Optional[float] = None,
    ):
        self.redis_url = redis_url or os.environ.get("UPSTASH_REDIS_URL")
        self.namespace = namespace
        self.embedding_model = embedding_model
        self.similarity_threshold = (
            similarity_threshold or self.DEFAULT_SIMILARITY_THRESHOLD
        )
        self._redis_client: Optional[Any] = None
        self._memory_cache: dict[str, CacheEntry] = {}
        self._embedding_cache: dict[str, list[float]] = {}

        # Hit counters for stats()
        self._hits_exact: int = 0
        self._hits_semantic: int = 0
        self._total_requests: int = 0

        # Guard: only warm the in-memory semantic index from Redis once per
        # process lifetime.  After the first warm pass, new entries are written
        # to both Redis AND _memory_cache (see set()), so the flag stays True.
        self._semantic_index_warmed: bool = False

        logger.info(
            "semantic_cache_initialized",
            namespace=namespace,
            has_redis=bool(self.redis_url),
        )

    async def _get_redis(self) -> Optional[Any]:
        """Get or create Redis client."""
        if self._redis_client is not None:
            return self._redis_client

        if not self.redis_url:
            return None

        try:
            # Try Upstash Redis first
            if "upstash" in self.redis_url.lower():
                from upstash_redis.asyncio import Redis as UpstashRedis

                self._redis_client = UpstashRedis.from_env()
            else:
                import redis.asyncio as aioredis

                self._redis_client = aioredis.from_url(self.redis_url)

            # Test connection
            await self._redis_client.ping()
            logger.info("redis_connected", url=self.redis_url[:30] + "...")
            return self._redis_client

        except Exception as e:
            logger.warning("redis_connection_failed", error=str(e))
            return None

    def _make_key(self, key: str) -> str:
        """Create a namespaced cache key."""
        return f"{self.namespace}:{key}"

    def _hash_query(self, query: str) -> str:
        """Create a hash key from a query string."""
        normalized = query.lower().strip()
        return hashlib.sha256(normalized.encode()).hexdigest()[:16]

    async def _get_embedding(self, text: str) -> Optional[list[float]]:
        """
        Get text embedding using the local Gemini sidecar service.
        """
        # Check cache first
        cache_key = self._hash_query(text)
        if cache_key in self._embedding_cache:
            return self._embedding_cache[cache_key]

        try:
            embedding = await gemini_service.embed(
                text=text,
                model=self.embedding_model,
                task_type="RETRIEVAL_QUERY",
            )
            if not isinstance(embedding, list):
                return None
            if not embedding:
                return None

            self._embedding_cache[cache_key] = embedding
            return embedding

        except Exception as e:
            logger.warning("embedding_generation_failed", error=str(e))
            return None

    async def _warm_semantic_index_from_redis(self) -> None:
        """
        Scan Redis for all namespace entries and load those that carry an
        embedding into _memory_cache.  Called once per process lifetime the
        first time a semantic lookup would otherwise run against an empty
        in-memory index.
        """
        redis = await self._get_redis()
        if not redis:
            self._semantic_index_warmed = True
            return
        try:
            import time as _time

            cursor = 0
            pattern = f"{self.namespace}:*"
            loaded = 0
            while True:
                cursor, keys = await redis.scan(cursor, match=pattern, count=100)
                for key in keys:
                    if key in self._memory_cache:
                        continue
                    try:
                        cached = await redis.get(key)
                        if not cached:
                            continue
                        data = json.loads(cached)
                        embedding = data.get("embedding")
                        if not isinstance(embedding, list) or not embedding:
                            continue
                        if len(self._memory_cache) >= self.MAX_MEMORY_CACHE_SIZE:
                            oldest = next(iter(self._memory_cache))
                            del self._memory_cache[oldest]
                        self._memory_cache[key] = CacheEntry(
                            key=key,
                            value=data["value"],
                            embedding=embedding,
                            embedding_family=data.get("embedding_family"),
                            embedding_version=data.get("embedding_version"),
                            created_at=data.get("created_at", _time.time()),
                            ttl_seconds=self.DEFAULT_TTL_SECONDS,
                        )
                        loaded += 1
                    except Exception:
                        continue
                if cursor == 0:
                    break
            logger.info(
                "semantic_index_warmed_from_redis",
                loaded=loaded,
                namespace=self.namespace,
            )
        except Exception as e:
            logger.warning("semantic_index_warm_failed", error=str(e))
        finally:
            self._semantic_index_warmed = True

    def _cosine_similarity(self, vec1: list[float], vec2: list[float]) -> float:
        """Calculate cosine similarity between two vectors."""
        if len(vec1) != len(vec2):
            return 0.0

        dot_product = sum(a * b for a, b in zip(vec1, vec2))
        magnitude1 = sum(a * a for a in vec1) ** 0.5
        magnitude2 = sum(b * b for b in vec2) ** 0.5

        if magnitude1 == 0 or magnitude2 == 0:
            return 0.0

        return dot_product / (magnitude1 * magnitude2)

    async def get(
        self,
        query: str,
        use_semantic: bool = True,
    ) -> Optional[tuple[Any, bool]]:
        """
        Get cached value for a query.

        Args:
            query: The query string
            use_semantic: Whether to use semantic similarity matching

        Returns:
            Tuple of (cached_value, is_semantic_match) or None if not found
        """
        self._total_requests += 1
        exact_key = self._make_key(self._hash_query(query))

        # Try Redis first
        import time as _time

        redis = await self._get_redis()
        if redis:
            try:
                cached = await redis.get(exact_key)
                if cached:
                    data = json.loads(cached)
                    if data.get("embedding_version") not in (
                        None,
                        self.EMBEDDING_VERSION,
                    ):
                        return None
                    # Repopulate in-memory entry so future semantic scans can use it
                    if exact_key not in self._memory_cache:
                        embedding = data.get("embedding")
                        if isinstance(embedding, list) and embedding:
                            if len(self._memory_cache) >= self.MAX_MEMORY_CACHE_SIZE:
                                oldest = next(iter(self._memory_cache))
                                del self._memory_cache[oldest]
                            self._memory_cache[exact_key] = CacheEntry(
                                key=exact_key,
                                value=data["value"],
                                embedding=embedding,
                                embedding_family=data.get("embedding_family"),
                                embedding_version=data.get("embedding_version"),
                                created_at=data.get("created_at", _time.time()),
                                ttl_seconds=self.DEFAULT_TTL_SECONDS,
                            )
                    logger.info("cache_hit_exact", key=exact_key[:20])
                    self._hits_exact += 1
                    return data["value"], False
            except Exception as e:
                logger.warning("redis_get_error", error=str(e))

        # Try memory cache for exact match
        if exact_key in self._memory_cache:
            entry = self._memory_cache[exact_key]
            if entry.embedding_version not in (None, self.EMBEDDING_VERSION):
                return None
            logger.info("cache_hit_memory_exact", key=exact_key[:20])
            self._hits_exact += 1
            return entry.value, False

        # Try semantic matching if enabled
        if use_semantic:
            # On first semantic lookup after a process restart the in-memory
            # index is empty even though Redis may hold many entries with
            # embeddings.  Warm the index once so semantic search works.
            if not self._semantic_index_warmed:
                await self._warm_semantic_index_from_redis()

            query_embedding = await self._get_embedding(query)
            if query_embedding:
                best_match: Optional[tuple[str, float]] = None

                # Search memory cache for similar entries
                for key, entry in self._memory_cache.items():
                    if entry.embedding:
                        if entry.embedding_version not in (
                            None,
                            self.EMBEDDING_VERSION,
                        ):
                            continue
                        similarity = self._cosine_similarity(
                            query_embedding, entry.embedding
                        )
                        if similarity >= self.similarity_threshold:
                            if best_match is None or similarity > best_match[1]:
                                best_match = (key, similarity)

                if best_match:
                    entry = self._memory_cache[best_match[0]]
                    logger.info(
                        "cache_hit_semantic",
                        key=best_match[0][:20],
                        similarity=f"{best_match[1]:.3f}",
                    )
                    self._hits_semantic += 1
                    return entry.value, True

        logger.debug("cache_miss", query=query[:50])
        return None

    async def set(
        self,
        query: str,
        value: Any,
        ttl_seconds: Optional[int] = None,
        store_embedding: bool = True,
    ) -> bool:
        """
        Cache a value for a query.

        Args:
            query: The query string
            value: The value to cache (must be JSON-serializable)
            ttl_seconds: Time-to-live in seconds
            store_embedding: Whether to store embedding for semantic matching

        Returns:
            True if cached successfully
        """
        import time

        ttl = ttl_seconds or self.DEFAULT_TTL_SECONDS
        key = self._make_key(self._hash_query(query))

        # Get embedding if requested
        embedding = None
        if store_embedding:
            embedding = await self._get_embedding(query)

        entry: CacheEntry[Any] = CacheEntry(
            key=key,
            value=value,
            embedding=embedding,
            embedding_family=self.EMBEDDING_FAMILY,
            embedding_version=self.EMBEDDING_VERSION,
            created_at=time.time(),
            ttl_seconds=ttl,
        )

        # LRU eviction: remove oldest entry if over size limit
        if len(self._memory_cache) >= self.MAX_MEMORY_CACHE_SIZE:
            oldest_key = next(iter(self._memory_cache))
            del self._memory_cache[oldest_key]

        # Store in memory cache
        self._memory_cache[key] = entry

        # Store in Redis if available
        redis = await self._get_redis()
        if redis:
            try:
                cache_data = {
                    "value": value,
                    "embedding": embedding,
                    "embedding_family": self.EMBEDDING_FAMILY,
                    "embedding_version": self.EMBEDDING_VERSION,
                    "created_at": entry.created_at,
                }
                await redis.setex(
                    key,
                    ttl,
                    json.dumps(cache_data),
                )
                logger.info("cache_set_redis", key=key[:20], ttl=ttl)
                return True
            except Exception as e:
                logger.warning("redis_set_error", error=str(e))

        logger.info("cache_set_memory", key=key[:20], ttl=ttl)
        return True

    async def delete(self, query: str) -> bool:
        """Delete a cached entry."""
        key = self._make_key(self._hash_query(query))

        # Delete from memory
        if key in self._memory_cache:
            del self._memory_cache[key]

        # Delete from Redis
        redis = await self._get_redis()
        if redis:
            try:
                await redis.delete(key)
            except Exception as e:
                logger.warning("redis_delete_error", error=str(e))

        return True

    async def clear_namespace(self) -> int:
        """Clear all entries in this namespace."""
        count = len(self._memory_cache)
        self._memory_cache.clear()

        redis = await self._get_redis()
        if redis:
            try:
                # Scan and delete all keys with namespace prefix
                cursor = 0
                pattern = f"{self.namespace}:*"
                while True:
                    cursor, keys = await redis.scan(cursor, match=pattern, count=100)
                    if keys:
                        await redis.delete(*keys)
                        count += len(keys)
                    if cursor == 0:
                        break
            except Exception as e:
                logger.warning("redis_clear_error", error=str(e))

        logger.info("cache_cleared", namespace=self.namespace, count=count)
        return count

    def stats(self) -> dict:
        """Get cache statistics."""
        total = self._total_requests
        hits = self._hits_exact + self._hits_semantic
        return {
            "memory_entries": len(self._memory_cache),
            "embedding_cache_entries": len(self._embedding_cache),
            "namespace": self.namespace,
            "similarity_threshold": self.similarity_threshold,
            "has_redis": self._redis_client is not None,
            "hits_exact": self._hits_exact,
            "hits_semantic": self._hits_semantic,
            "total_requests": total,
            "hit_rate": hits / total if total > 0 else 0.0,
        }


# Singleton instances for different cache purposes
# Domain detection is straightforward — lower threshold improves hit rate
semantic_cache = SemanticCache(namespace="wisdev:questions", similarity_threshold=0.85)
domain_cache = SemanticCache(namespace="wisdev:domains", similarity_threshold=0.80)
analysis_cache = SemanticCache(namespace="wisdev:analysis", similarity_threshold=0.85)
