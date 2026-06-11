from fastapi.testclient import TestClient

import main
from main import app
from artifacts.schema import ARTIFACT_SCHEMA_VERSION
from stack_contract import ENDPOINTS_MANIFEST


def _manifest_python_paths() -> set[str]:
    # Validate against this repo's own stack contract rather than the parent
    # app's config/endpoints.manifest.json, which does not exist in a
    # standalone OSS checkout.
    return set(ENDPOINTS_MANIFEST['httpRoutes']['python_sidecar'])


def test_manifest_python_routes_are_mounted() -> None:
    mounted_paths = {
        path
        for route in app.routes
        for path in [getattr(route, 'path', None)]
        if isinstance(path, str)
    }
    missing = set()
    for manifest_path in _manifest_python_paths():
        if manifest_path.endswith('/*'):
            prefix = manifest_path[:-1]
            if any(path.startswith(prefix) for path in mounted_paths):
                continue
        elif manifest_path in mounted_paths:
            continue
        missing.add(manifest_path)
    assert not missing

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
