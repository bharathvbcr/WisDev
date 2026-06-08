import pytest
from fastapi import Request
from fastapi.testclient import TestClient
from main import app, limiter

client = TestClient(app)

def test_rate_limiter_active_trigger():
    """Verify that the rate limiter actually returns 429 when triggered."""
    # We apply a very strict limit to a test endpoint
    # Note: Request type hint is REQUIRED for slowapi to work
    @app.get("/api/test-limit-new")
    @limiter.limit("1/minute")
    def limited_endpoint(request: Request):
        return {"status": "ok"}

    # First request: OK
    response = client.get("/api/test-limit-new")
    assert response.status_code == 200
    
    # Second request within a minute: 429
    response = client.get("/api/test-limit-new")
    assert response.status_code == 429
    assert "Rate limit exceeded" in response.text
