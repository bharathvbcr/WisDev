from fastapi.testclient import TestClient

import compute_app


def test_compute_app_health_is_reachable():
    with TestClient(compute_app.app) as client:
        response = client.get("/health")

    assert response.status_code == 200
    payload = response.json()
    assert "status" in payload
    assert "services" in payload


def test_compute_app_root_reports_service_metadata():
    with TestClient(compute_app.app) as client:
        response = client.get("/")

    assert response.status_code == 200
    payload = response.json()
    assert payload["service"] == "wisdev-azure-compute"
    assert payload["version"] == "1.1.0"
    assert "embeddingProvider" in payload
