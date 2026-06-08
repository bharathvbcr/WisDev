import json
from pathlib import Path

from fastapi.testclient import TestClient

import main
from main import app
from artifacts.schema import ARTIFACT_SCHEMA_VERSION


def _manifest_python_paths() -> set[str]:
    manifest_path = Path(__file__).resolve().parents[3] / 'config' / 'endpoints.manifest.json'
    manifest = json.loads(manifest_path.read_text(encoding='utf-8'))
    return set(manifest['httpRoutes']['python_sidecar'])


def test_manifest_python_routes_are_mounted() -> None:
    mounted_paths = {
        path
        for route in app.routes
        for path in [getattr(route, 'path', None)]
        if isinstance(path, str)
    }
    assert _manifest_python_paths().issubset(mounted_paths)

def test_deepagents_capabilities_contract_smoke() -> None:
    client = TestClient(app)
    canonical = client.get('/wisdev/deep-agents/capabilities')

    assert canonical.status_code == 200

    payload = canonical.json()
    assert payload.get('backend') == 'deepagents'
    assert payload.get('artifactSchema') == ARTIFACT_SCHEMA_VERSION
    assert isinstance(payload.get('wisdevActions'), list)
    assert isinstance(payload.get('sensitiveWisdevActions'), list)
    assert isinstance(payload.get('defaultMaxExecutionMs'), int)
    assert all(isinstance(action, str) for action in payload.get('wisdevActions', []))
    assert all(isinstance(action, str) for action in payload.get('sensitiveWisdevActions', []))


def test_health_reports_degraded_when_grpc_models_are_unavailable(monkeypatch) -> None:
    async def fake_grpc_health(*_args, **_kwargs):
        return "degraded", "Gemini credentials not configured"

    monkeypatch.setattr(main, "_grpc_sidecar_health", fake_grpc_health)
    client = TestClient(app)
    response = client.get('/health')

    assert response.status_code == 200
    payload = response.json()
    assert payload["status"] == "degraded"
    assert payload["warmup"]["grpcReady"] is True
    assert any(
        dependency["name"] == "grpc_sidecar"
        and "Gemini credentials not configured" in dependency["status"]
        for dependency in payload["dependencies"]
    )


def test_readiness_reports_degraded_when_grpc_models_are_unavailable(monkeypatch) -> None:
    async def fake_grpc_health(*_args, **_kwargs):
        return "degraded", "Gemini credentials not configured"

    monkeypatch.setattr(main, "_grpc_sidecar_health", fake_grpc_health)
    client = TestClient(app)
    response = client.get('/readiness')

    assert response.status_code == 200
    payload = response.json()
    assert payload["status"] == "degraded"
    assert payload["warmup"]["grpcReady"] is True
    assert any(
        dependency["name"] == "grpc_sidecar"
        and "Gemini credentials not configured" in dependency["status"]
        for dependency in payload["dependencies"]
    )
