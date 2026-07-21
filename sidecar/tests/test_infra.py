import os
import pytest
from fastapi.testclient import TestClient
from unittest.mock import patch, MagicMock, AsyncMock

from main import app, limiter

client = TestClient(app)

def _dependency_map(payload):
    dependencies = payload["dependencies"]
    if isinstance(dependencies, dict):
        return dependencies

    mapped = {}
    for dependency in dependencies:
        name = dependency.get("name")
        status = dependency.get("status")
        if name == "redis":
            mapped["redis"] = "connected" if status == "ok" else status
        elif name == "grpc_sidecar":
            if status == "ok":
                mapped["grpc"] = "connected"
            elif status == "disabled":
                mapped["grpc"] = "disabled"
            else:
                mapped["grpc"] = {"error": status}
    return mapped

def test_health_check_structure():
    """Verify the health check returns the expected structure."""
    response = client.get("/health")
    assert response.status_code == 200
    data = response.json()
    assert "status" in data
    assert "dependencies" in data
    assert "warmup" in data
    assert "firstCallReady" in data["warmup"]
    assert "redis" in _dependency_map(data)

@pytest.mark.asyncio
async def test_health_check_redis_connected():
    """Verify health check reports connected when Redis is working."""
    # Mock semantic_cache._get_redis to return a working client
    with patch("services.semantic_cache.semantic_cache._get_redis", new_callable=AsyncMock) as mock_get_redis:
        mock_client = AsyncMock()
        mock_client.ping.return_value = True
        mock_get_redis.return_value = mock_client
        
        response = client.get("/health")
        assert response.status_code == 200
        data = response.json()
        assert _dependency_map(data)["redis"] == "connected"
        assert data["status"] == "ok"

@pytest.mark.asyncio
async def test_health_check_redis_failed():
    """Verify health check reports degraded when Redis fails."""
    with patch("services.semantic_cache.semantic_cache._get_redis", new_callable=AsyncMock) as mock_get_redis:
        mock_client = AsyncMock()
        mock_client.ping.side_effect = Exception("Connection refused")
        mock_get_redis.return_value = mock_client
        
        response = client.get("/health")
        assert response.status_code == 200
        data = response.json()
        assert "error:" in _dependency_map(data)["redis"]
        assert data["status"] == "degraded"


@pytest.mark.asyncio
async def test_health_check_grpc_failed():
    """Verify health check reports unhealthy (503) when the gRPC sidecar is not ready."""
    with patch("main._grpc_sidecar_health", new_callable=AsyncMock, return_value=("unavailable", "grpc unavailable")):
        response = client.get("/health")
        assert response.status_code == 503
        data = response.json()
        assert _dependency_map(data)["grpc"]["error"] == "grpc unavailable"
        assert data["status"] == "degraded"


def test_health_check_grpc_disabled_mode():
    with patch("main._grpc_disabled", return_value=True):
        response = client.get("/health")
        assert response.status_code == 200
        data = response.json()
        assert _dependency_map(data)["grpc"] == "disabled"
        assert data["status"] == "ok"


def test_readiness_grpc_disabled_mode():
    with patch("main._grpc_disabled", return_value=True):
        response = client.get("/readiness")
        assert response.status_code == 200
        data = response.json()
        assert _dependency_map(data)["grpc"] == "disabled"
        assert data["status"] == "ok"
        assert "warmup" in data

def test_rate_limiter_config():
    """Verify rate limiter falls back to memory if no env var."""
    # Since we can't easily change env vars for the already-imported main module
    # without reloading it, we check the current state (which should be memory in test env)
    if not os.environ.get("UPSTASH_REDIS_URL"):
        # slowapi/limits uses None or "memory://" depending on version, 
        # but typically None means "no explicit storage URI provided" -> defaults to memory
        assert limiter._storage_uri is None or limiter._storage_uri == "memory://"
