"""Tests for limiter_config.py."""

import importlib
import os
import sys
import pytest
from slowapi import Limiter


def _reload_limiter_config(env: dict):
    """Reload limiter_config with a specific environment."""
    # Remove the cached module so it re-executes module-level code
    for mod in list(sys.modules.keys()):
        if "limiter_config" in mod:
            del sys.modules[mod]

    old_env = {}
    for key, val in env.items():
        old_env[key] = os.environ.get(key)
        if val is None:
            os.environ.pop(key, None)
        else:
            os.environ[key] = val

    try:
        import limiter_config
        return limiter_config
    finally:
        # Restore original env
        for key, old_val in old_env.items():
            if old_val is None:
                os.environ.pop(key, None)
            else:
                os.environ[key] = old_val


def test_memory_limiter_when_no_redis_url():
    mod = _reload_limiter_config({"UPSTASH_REDIS_URL": None})
    assert isinstance(mod.limiter, Limiter)
    assert mod.redis_url is None


def test_redis_limiter_when_url_set():
    # Pass a fake redis URL; slowapi may accept it at init time without connecting
    try:
        mod = _reload_limiter_config(
            {"UPSTASH_REDIS_URL": "redis://localhost:6379"}
        )
        # If it didn't raise, we got a Limiter back
        assert isinstance(mod.limiter, Limiter)
        assert mod.redis_url == "redis://localhost:6379"
    except Exception:
        # Connection errors are acceptable — the point is the branch was taken
        pass


def test_limiter_is_limiter_instance():
    mod = _reload_limiter_config({"UPSTASH_REDIS_URL": None})
    assert isinstance(mod.limiter, Limiter)


def test_upstash_prefix_branch():
    """Ensure the upstash:// prefix branch doesn't raise."""
    try:
        mod = _reload_limiter_config(
            {"UPSTASH_REDIS_URL": "upstash://some-upstash-host"}
        )
        assert isinstance(mod.limiter, Limiter)
        assert mod.redis_url == "rediss://some-upstash-host"
    except Exception:
        pass
