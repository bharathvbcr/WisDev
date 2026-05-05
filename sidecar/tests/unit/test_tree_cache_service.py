from __future__ import annotations

from services.tree_cache_service import TreeCacheService


def test_put_and_get_moves_key_to_end_and_refreshes_entry():
    cache = TreeCacheService(max_local_size=2)
    cache.put("paper-a", {"value": "A"})
    cache.put("paper-b", {"value": "B"})

    assert cache.get("paper-a") == {"value": "A"}
    cache.put("paper-c", {"value": "C"})

    assert cache.get("paper-b") is None
    assert cache.get("paper-a") == {"value": "A"}
    assert cache.get("paper-c") == {"value": "C"}


def test_get_missing_key_returns_none():
    cache = TreeCacheService(max_local_size=1)
    assert cache.get("missing") is None


def test_invalidate_returns_true_only_when_present():
    cache = TreeCacheService(max_local_size=2)
    cache.put("paper-a", {"value": 1})
    assert cache.invalidate("paper-a") is True
    assert cache.invalidate("paper-a") is False


def test_clear_empties_cache():
    cache = TreeCacheService(max_local_size=2)
    cache.put("paper-a", {})
    cache.put("paper-b", {})
    cache.clear()
    assert cache.get("paper-a") is None
    assert cache.get("paper-b") is None


def test_put_existing_key_moves_to_end_and_updates_value():
    cache = TreeCacheService(max_local_size=2)
    cache.put("paper-a", {"value": "A"})
    cache.put("paper-b", {"value": "B"})
    cache.put("paper-a", {"value": "A2"})
    cache.put("paper-c", {"value": "C"})

    assert cache.get("paper-b") is None
    assert cache.get("paper-a") == {"value": "A2"}
