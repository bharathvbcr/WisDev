import os
from types import SimpleNamespace

import pytest

from services import secret_manager


@pytest.fixture(autouse=True)
def reset_secret_manager_state(monkeypatch):
    secret_manager.clear_secret_cache()
    monkeypatch.setattr(secret_manager, "_sm_client", None)
    monkeypatch.setattr(secret_manager, "_sm_client_init_failed", False)
    monkeypatch.setattr(secret_manager, "_sm_client_retry_at", 0.0)
    yield
    secret_manager.clear_secret_cache()


def test_get_google_api_key_prefers_secret_manager_when_project_is_available(monkeypatch):
    monkeypatch.setenv("GOOGLE_API_KEY", "env-key")
    monkeypatch.setenv("GOOGLE_CLOUD_PROJECT", "test-project")
    monkeypatch.delenv("PYTEST_CURRENT_TEST", raising=False)
    monkeypatch.setattr(secret_manager, "_fetch_secret_from_manager", lambda _name: "secret-key")

    assert secret_manager.get_google_api_key() == "secret-key"


def test_get_secret_uses_explicit_secret_name_override(monkeypatch):
    monkeypatch.delenv("GOOGLE_API_KEY", raising=False)
    monkeypatch.delenv("GEMINI_API_KEY", raising=False)
    monkeypatch.delenv("PYTEST_CURRENT_TEST", raising=False)
    monkeypatch.setenv("GOOGLE_CLOUD_PROJECT", "test-project")
    monkeypatch.setenv("GOOGLE_API_KEY_SECRET_NAME", "gemini-api-key-prod")

    calls: list[str] = []

    def fake_fetch(secret_name: str) -> str:
        calls.append(secret_name)
        return "gsm-key" if secret_name == "gemini-api-key-prod" else ""

    monkeypatch.setattr(secret_manager, "_fetch_secret_from_manager", fake_fetch)

    assert secret_manager.get_google_api_key() == "gsm-key"
    assert calls == ["gemini-api-key-prod"]


def test_get_secret_falls_back_to_default_secret_names(monkeypatch):
    monkeypatch.delenv("GOOGLE_API_KEY", raising=False)
    monkeypatch.delenv("GEMINI_API_KEY", raising=False)
    monkeypatch.delenv("GOOGLE_API_KEY_SECRET_NAME", raising=False)
    monkeypatch.delenv("GEMINI_API_KEY_SECRET_NAME", raising=False)
    monkeypatch.delenv("PYTEST_CURRENT_TEST", raising=False)
    monkeypatch.setenv("GOOGLE_CLOUD_PROJECT", "test-project")

    calls: list[str] = []

    def fake_fetch(secret_name: str) -> str:
        calls.append(secret_name)
        return "gsm-key" if secret_name == "GOOGLE_API_KEY" else ""

    monkeypatch.setattr(secret_manager, "_fetch_secret_from_manager", fake_fetch)

    assert secret_manager.get_google_api_key() == "gsm-key"
    assert calls == ["GOOGLE_API_KEY"]


def test_fetch_secret_from_manager_caches_values(monkeypatch):
    monkeypatch.delenv("PYTEST_CURRENT_TEST", raising=False)
    monkeypatch.setenv("GOOGLE_CLOUD_PROJECT", "test-project")
    secret_manager.clear_secret_cache()

    class FakeSecretManagerClient:
        def __init__(self):
            self.calls = 0

        def access_secret_version(self, request):
            self.calls += 1
            assert request["name"] == "projects/test-project/secrets/GOOGLE_API_KEY/versions/latest"
            return SimpleNamespace(payload=SimpleNamespace(data=b"cached-key"))

    client = FakeSecretManagerClient()
    monkeypatch.setattr(secret_manager, "_sm_client", client)
    monkeypatch.setattr(secret_manager, "_sm_client_init_failed", False)

    first = secret_manager._fetch_secret_from_manager("GOOGLE_API_KEY")
    second = secret_manager._fetch_secret_from_manager("GOOGLE_API_KEY")

    assert first == "cached-key"
    assert second == "cached-key"
    assert client.calls == 1


def test_fetch_secret_from_manager_retries_after_short_negative_cache(monkeypatch):
    monkeypatch.delenv("PYTEST_CURRENT_TEST", raising=False)
    monkeypatch.setenv("GOOGLE_CLOUD_PROJECT", "test-project")
    secret_manager.clear_secret_cache()

    now = {"value": 1000.0}
    monkeypatch.setattr(secret_manager.time, "time", lambda: now["value"])

    class FlakySecretManagerClient:
        def __init__(self):
            self.calls = 0

        def access_secret_version(self, request):
            self.calls += 1
            assert request["name"] == "projects/test-project/secrets/GOOGLE_API_KEY/versions/latest"
            if self.calls == 1:
                raise RuntimeError("temporary outage")
            return SimpleNamespace(payload=SimpleNamespace(data=b"recovered-key"))

    client = FlakySecretManagerClient()
    monkeypatch.setattr(secret_manager, "_sm_client", client)
    monkeypatch.setattr(secret_manager, "_sm_client_init_failed", False)

    first = secret_manager._fetch_secret_from_manager("GOOGLE_API_KEY")
    second = secret_manager._fetch_secret_from_manager("GOOGLE_API_KEY")
    now["value"] += secret_manager._SECRET_MISS_TTL_SECONDS + 1
    third = secret_manager._fetch_secret_from_manager("GOOGLE_API_KEY")

    assert first == ""
    assert second == ""
    assert third == "recovered-key"
    assert client.calls == 2


def test_get_google_api_key_resolution_reports_secret_manager(monkeypatch):
    monkeypatch.delenv("GOOGLE_API_KEY", raising=False)
    monkeypatch.delenv("GEMINI_API_KEY", raising=False)
    monkeypatch.delenv("PYTEST_CURRENT_TEST", raising=False)
    monkeypatch.setenv("GOOGLE_CLOUD_PROJECT", "test-project")
    monkeypatch.setattr(secret_manager, "_fetch_secret_from_manager", lambda _name: "gsm-key")

    resolution = secret_manager.get_google_api_key_resolution()

    assert resolution["status"] == "ok"
    assert resolution["source"] == "secret_manager"
    assert resolution["projectSource"] == "env:GOOGLE_CLOUD_PROJECT"


def test_prime_google_api_key_warms_secret_manager_without_mutating_env(monkeypatch):
    monkeypatch.delenv("GOOGLE_API_KEY", raising=False)
    monkeypatch.delenv("GEMINI_API_KEY", raising=False)
    monkeypatch.setattr(secret_manager, "get_google_api_key_resolution", lambda: {
        "status": "ok",
        "source": "secret_manager",
        "secretManagerEnabled": True,
        "projectConfigured": True,
    })
    monkeypatch.setattr(secret_manager, "get_google_api_key", lambda: "gsm-key")

    resolution = secret_manager.prime_google_api_key()

    assert resolution["status"] == "ok"
    assert "GOOGLE_API_KEY" not in os.environ
