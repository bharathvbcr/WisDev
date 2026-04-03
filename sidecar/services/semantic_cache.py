"""
Semantic Cache Service
Caches AI-generated questions using embedding similarity.

Features:
- Embedding-based similarity matching (not just exact string match)
- TTL-based expiration (24 hours default)
- In-memory cache with LRU eviction
"""

import os
import hashlib
import time
from typing import Optional, Any, Generic, TypeVar

import structlog
import httpx
from pydantic import BaseModel

logger = structlog.get_logger(__name__)

T = TypeVar("T")


class CacheEntry(BaseModel, Generic[T]):
    """A cached entry with metadata."""

    key: str
    value: Any
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

    DEFAULT_SIMILARITY_THRESHOLD = 0.85
    DEFAULT_TTL_SECONDS = 86400
    MAX_MEMORY_CACHE_SIZE = 1000
    EMBEDDING_FAMILY = "local"
    EMBEDDING_VERSION = "v1"

    def __init__(
        self,
        namespace: str = "wisdev",
        embedding_model: str = "local-embedding",
        similarity_threshold: Optional[float] = None,
    ):
        self.namespace = namespace
        self.embedding_model = embedding_model
        self.similarity_threshold = (
            similarity_threshold or self.DEFAULT_SIMILARITY_THRESHOLD
        )

        self._memory_cache: dict[str, CacheEntry] = {}
        self._embedding_cache: dict[str, list[float]] = {}

        self._hits_exact: int = 0
        self._hits_semantic: int = 0
        self._total_requests: int = 0

        logger.info(
            "semantic_cache_initialized",
            namespace=namespace,
        )

    def _make_key(self, key: str) -> str:
        return f"{self.namespace}:{key}"

    def _hash_query(self, query: str) -> str:
        normalized = query.lower().strip()
        return hashlib.sha256(normalized.encode()).hexdigest()[:16]

    async def _get_embedding(self, text: str) -> Optional[list[float]]:
        cache_key = self._hash_query(text)
        if cache_key in self._embedding_cache:
            return self._embedding_cache[cache_key]

        try:
            embed_url = os.environ.get("EMBEDDING_API_URL")
            if embed_url:
                async with httpx.AsyncClient(timeout=20.0) as client:
                    response = await client.post(
                        embed_url,
                        json={"text": text},
                        headers={"Content-Type": "application/json"},
                    )
                if response.status_code == 200:
                    data = response.json()
                    embedding = data.get("embedding")
                    if isinstance(embedding, list):
                        self._embedding_cache[cache_key] = embedding
                        return embedding
        except Exception as e:
            logger.warning("embedding_generation_failed", error=str(e))

        return None

    def _cosine_similarity(self, vec1: list[float], vec2: list[float]) -> float:
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
        self._total_requests += 1
        exact_key = self._make_key(self._hash_query(query))

        if exact_key in self._memory_cache:
            entry = self._memory_cache[exact_key]
            if time.time() - entry.created_at < entry.ttl_seconds:
                logger.info("cache_hit_exact", key=exact_key[:20])
                self._hits_exact += 1
                return entry.value, False
            else:
                del self._memory_cache[exact_key]

        if use_semantic:
            query_embedding = await self._get_embedding(query)
            if query_embedding:
                best_match: Optional[tuple[str, float]] = None
                now = time.time()
                for key, entry in list(self._memory_cache.items()):
                    if now - entry.created_at >= entry.ttl_seconds:
                        del self._memory_cache[key]
                        continue
                    if entry.embedding:
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
        ttl = ttl_seconds or self.DEFAULT_TTL_SECONDS
        key = self._make_key(self._hash_query(query))

        embedding = None
        if store_embedding:
            embedding = await self._get_embedding(query)

        entry = CacheEntry(
            key=key,
            value=value,
            embedding=embedding,
            embedding_family=self.EMBEDDING_FAMILY,
            embedding_version=self.EMBEDDING_VERSION,
            created_at=time.time(),
            ttl_seconds=ttl,
        )

        if len(self._memory_cache) >= self.MAX_MEMORY_CACHE_SIZE:
            oldest_key = next(iter(self._memory_cache))
            del self._memory_cache[oldest_key]

        self._memory_cache[key] = entry
        logger.info("cache_set_memory", key=key[:20], ttl=ttl)
        return True

    async def delete(self, query: str) -> bool:
        key = self._make_key(self._hash_query(query))
        self._memory_cache.pop(key, None)
        return True

    async def clear_namespace(self) -> int:
        count = len(self._memory_cache)
        self._memory_cache.clear()
        logger.info("cache_cleared", namespace=self.namespace, count=count)
        return count

    def stats(self) -> dict:
        total = self._total_requests
        hits = self._hits_exact + self._hits_semantic
        return {
            "memory_entries": len(self._memory_cache),
            "embedding_cache_entries": len(self._embedding_cache),
            "namespace": self.namespace,
            "similarity_threshold": self.similarity_threshold,
            "hits_exact": self._hits_exact,
            "hits_semantic": self._hits_semantic,
            "total_requests": total,
            "hit_rate": hits / total if total > 0 else 0.0,
        }


semantic_cache = SemanticCache(namespace="wisdev:questions", similarity_threshold=0.85)
domain_cache = SemanticCache(namespace="wisdev:domains", similarity_threshold=0.80)
analysis_cache = SemanticCache(namespace="wisdev:analysis", similarity_threshold=0.85)
