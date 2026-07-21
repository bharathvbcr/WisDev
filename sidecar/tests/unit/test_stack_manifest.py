from __future__ import annotations

import copy

import pytest

import stack_manifest


@pytest.fixture(autouse=True)
def restore_manifest():
    snapshot = copy.deepcopy(stack_manifest.ENDPOINTS_MANIFEST)
    yield
    stack_manifest.ENDPOINTS_MANIFEST.clear()
    stack_manifest.ENDPOINTS_MANIFEST.update(copy.deepcopy(snapshot))


def test_current_overlay_name_uses_env_override(monkeypatch):
    monkeypatch.setenv("ENDPOINTS_MANIFEST_ENV", "docker")
    assert stack_manifest.current_overlay_name() == "docker"


def test_current_overlay_name_falls_back_to_default_for_unknown_overlay(monkeypatch):
    monkeypatch.setenv("ENDPOINTS_MANIFEST_ENV", "does-not-exist")
    assert stack_manifest.current_overlay_name() == stack_manifest.ENDPOINTS_DEFAULT_OVERLAY


def test_current_overlay_name_defaults_when_env_missing(monkeypatch):
    monkeypatch.delenv("ENDPOINTS_MANIFEST_ENV", raising=False)
    assert stack_manifest.current_overlay_name() == stack_manifest.ENDPOINTS_DEFAULT_OVERLAY


def test_current_overlay_returns_copy():
    overlay = stack_manifest.current_overlay()
    overlay["env"]["CUSTOM_VALUE"] = "modified"
    assert "CUSTOM_VALUE" not in stack_manifest.current_overlay().get("env", {})


def test_resolve_env_prefers_explicit_env(monkeypatch):
    monkeypatch.setenv("TARGET_KEY", "  explicit ")
    assert stack_manifest.resolve_env("TARGET_KEY") == "explicit"


def test_resolve_env_falls_back_to_overlay_env(monkeypatch):
    monkeypatch.delenv("TARGET_KEY", raising=False)
    stack_manifest.ENDPOINTS_MANIFEST["overlays"]["local"]["env"]["TARGET_KEY"] = "overlay-value"
    assert stack_manifest.resolve_env("TARGET_KEY") == "overlay-value"


def test_resolve_env_returns_empty_when_missing_everywhere(monkeypatch):
    monkeypatch.delenv("TARGET_KEY", raising=False)
    stack_manifest.ENDPOINTS_MANIFEST["overlays"]["local"]["env"].pop("TARGET_KEY", None)
    assert stack_manifest.resolve_env("TARGET_KEY") == ""


def test_resolve_service_base_url_uses_open_source_go_overlay(monkeypatch):
    monkeypatch.setenv("PYTHON_SIDECAR_HTTP_URL", "http://unused.sidecar.local")
    assert (
        stack_manifest.resolve_service_base_url("go_orchestrator")
        == "http://127.0.0.1:8081"
    )


def test_resolve_service_base_url_falls_back_to_overlay_service_base_urls(monkeypatch):
    monkeypatch.delenv("VITE_API_BASE_URL", raising=False)
    assert stack_manifest.resolve_service_base_url("python_sidecar") == "http://127.0.0.1:8090"


def test_resolve_service_base_url_falls_back_to_default_url():
    stack_manifest.ENDPOINTS_MANIFEST["services"]["unit_test_service"] = {
        "defaultBaseUrl": "http://unit.test.default"
    }
    assert stack_manifest.resolve_service_base_url("unit_test_service") == "http://unit.test.default"


def test_resolve_service_base_url_trims_trailing_slash(monkeypatch):
    monkeypatch.delenv("VITE_API_BASE_URL", raising=False)
    stack_manifest.ENDPOINTS_MANIFEST["services"]["unit_test_service"] = {
        "defaultBaseUrl": "http://unit.test.default/"
    }
    assert stack_manifest.resolve_service_base_url("unit_test_service") == "http://unit.test.default"


def test_resolve_listen_port_prefers_http_env(monkeypatch):
    monkeypatch.setenv("PORT", "9876")
    assert stack_manifest.resolve_listen_port("frontend", "http") == 9876


def test_resolve_listen_port_uses_service_listen_ports(monkeypatch):
    monkeypatch.setenv("PORT", "not-a-number")
    assert stack_manifest.resolve_listen_port("go_orchestrator", "http") == 8081


def test_resolve_listen_port_non_http_ignores_port_env(monkeypatch):
    monkeypatch.setenv("PORT", "9999")
    stack_manifest.ENDPOINTS_MANIFEST["services"]["unit_test_service"] = {
        "listenPorts": {"grpc": 7001}
    }
    assert stack_manifest.resolve_listen_port("unit_test_service", "grpc") == 7001


def test_validate_service_passes_for_known_service_from_overlay_env():
    stack_manifest.validate_service("go_orchestrator")


def test_validate_service_requires_matching_manifest_version():
    stack_manifest.ENDPOINTS_MANIFEST["version"] = 1
    with pytest.raises(RuntimeError, match="generated stack manifest version mismatch"):
        stack_manifest.validate_service("go_orchestrator")


def test_validate_service_rejects_unknown_service():
    with pytest.raises(RuntimeError, match="unknown service id"):
        stack_manifest.validate_service("missing_service")


def test_validate_service_reports_missing_required_env():
    stack_manifest.ENDPOINTS_MANIFEST["services"]["missing_env_service"] = {
        "requiredEnv": ["SOME_MISSING_ENV_VAR"]
    }
    with pytest.raises(RuntimeError, match="missing required manifest env"):
        stack_manifest.validate_service("missing_env_service")
