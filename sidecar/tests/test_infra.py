import os
import pytest
from fastapi.testclient import TestClient
from unittest.mock import patch, MagicMock, AsyncMock

from main import app, limiter

client = TestClient(app)


def test_health_check_structure():
    """Verify the health check returns the expected structure."""
    response = client.get("/health")
    assert response.status_code == 200
    data = response.json()
    assert "status" in data
    assert "dependencies" in data
    assert "grpc" in data["dependencies"]


def test_health_check_grpc_failed():
    """Verify health check reports unhealthy (503) when the gRPC sidecar is not ready."""
    with patch(
        "main._grpc_sidecar_ready",
        new_callable=AsyncMock,
        return_value=(False, "grpc unavailable"),
    ):
        response = client.get("/health")
        assert response.status_code == 503
        data = response.json()
        assert data["dependencies"]["grpc"]["error"] == "grpc unavailable"
        assert data["status"] == "unhealthy"


def test_rate_limiter_config():
    """Verify rate limiter uses memory storage."""
    assert limiter._storage_uri is None or limiter._storage_uri == "memory://"
