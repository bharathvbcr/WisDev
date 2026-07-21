"""Lightweight in-memory cache for Azure compute RAPTOR metadata."""
from __future__ import annotations

from collections import OrderedDict

MAX_LOCAL_CACHE_SIZE = 50


class TreeCacheService:
    """Small LRU cache keyed by paper hash."""

    def __init__(self, max_local_size: int = MAX_LOCAL_CACHE_SIZE):
        self.max_local_size = max_local_size
        self._local_cache: OrderedDict[str, dict] = OrderedDict()

    def get(self, paper_hash: str) -> dict | None:
        if paper_hash not in self._local_cache:
            return None
        self._local_cache.move_to_end(paper_hash)
        return self._local_cache[paper_hash]

    def put(self, paper_hash: str, tree_data: dict) -> None:
        if paper_hash in self._local_cache:
            self._local_cache.move_to_end(paper_hash)
        self._local_cache[paper_hash] = tree_data
        while len(self._local_cache) > self.max_local_size:
            self._local_cache.popitem(last=False)

    def invalidate(self, paper_hash: str) -> bool:
        return self._local_cache.pop(paper_hash, None) is not None

    def clear(self) -> None:
        self._local_cache.clear()
