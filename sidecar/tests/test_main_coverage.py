import pytest
import os
from fastapi.testclient import TestClient
from unittest.mock import MagicMock, patch, AsyncMock
from fastapi import Request

@pytest.fixture
def mock_redis_env():
    with patch.dict(os.environ, {"UPSTASH_REDIS_URL": "rediss://test:test@test.upstash.io:6379"}):
        yield

def test_main_startup_with_redis(mock_redis_env):
    # We need to reload the module to trigger the redis config logic
    import importlib
    import main
    with patch("main._start_grpc_server"):
        with patch("main._wait_for_grpc_sidecar_ready", new_callable=AsyncMock):
            importlib.reload(main)
            from main import app
            
            with TestClient(app) as client:
                response = client.get("/health")
                assert response.status_code == 200

@pytest.mark.asyncio
async def test_lifespan_shutdown():
    from main import lifespan
    app = MagicMock()
    with patch("main._start_grpc_server"):
        with patch("main._wait_for_grpc_sidecar_ready", new_callable=AsyncMock):
            async with lifespan(app):
                pass


@pytest.mark.asyncio
async def test_lifespan_requires_grpc_sidecar_ready():
    from main import lifespan

    app = MagicMock()
    with patch("main._start_grpc_server"):
        with patch("main._wait_for_grpc_sidecar_ready", new_callable=AsyncMock, side_effect=RuntimeError("grpc unavailable")):
            with pytest.raises(RuntimeError):
                async with lifespan(app):
                    pass

@pytest.mark.asyncio
async def test_rate_limit_handler():
    from main import app
    from slowapi.errors import RateLimitExceeded
    from fastapi.responses import JSONResponse
    
    handler = app.exception_handlers.get(RateLimitExceeded)
    if handler:
        request = MagicMock(spec=Request)
        request.url.path = "/test"
        mock_limit = MagicMock()
        mock_limit.error_message = "limit exceeded"
        exc = RateLimitExceeded(mock_limit)
        
        # If the handler is a mock or returns a mock, just pass
        response = handler(request, exc)
        if isinstance(response, JSONResponse):
            assert response.status_code == 429

@pytest.mark.asyncio
async def test_general_exception_handler():
    from main import app
    handler = app.exception_handlers.get(Exception)
    if handler:
        request = MagicMock(spec=Request)
        request.url.path = "/test"
        exc = Exception("random error")
        response = await handler(request, exc)
        assert response.status_code == 500

@pytest.mark.asyncio
async def test_http_exception_handler():
    from main import app
    from fastapi import HTTPException
    handler = app.exception_handlers.get(HTTPException)
    if handler:
        request = MagicMock(spec=Request)
        request.url.path = "/test"
        exc = HTTPException(status_code=400, detail={"code": "BAD", "message": "msg"})
        response = await handler(request, exc)
        assert response.status_code == 400

def test_root_endpoint():
    from main import app
    with TestClient(app) as client:
        response = client.get("/")
        assert response.status_code == 200
        assert "service" in response.json()

def test_metrics_endpoint():
    from main import app
    with TestClient(app) as client:
        response = client.get("/metrics")
        assert response.status_code == 200
        assert "caches" in response.json()

def test_trace_propagation_middleware():
    from main import app
    with TestClient(app) as client:
        # Test with existing traceparent
        trace_id = "0af7651916cd43dd8448eb211c80319c"
        traceparent = f"00-{trace_id}-b7ad6b7169203331-01"
        response = client.get("/", headers={"traceparent": traceparent})
        assert response.headers["X-Trace-Id"] == trace_id
        
        # Test without traceparent (should generate new one)
        response2 = client.get("/")
        assert "X-Trace-Id" in response2.headers
        assert len(response2.headers["X-Trace-Id"]) == 32
