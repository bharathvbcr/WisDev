"""Additional branch tests for services/secret_manager.py."""

from __future__ import annotations

from pathlib import Path
from unittest.mock import patch
import builtins
import sys

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


def test_project_id_prefers_google_cloud_project(monkeypatch):
    monkeypatch.setenv("GOOGLE_CLOUD_PROJECT", "primary")
    monkeypatch.setenv("GCLOUD_PROJECT", "secondary")
    monkeypatch.setenv("GCP_PROJECT_ID", "tertiary")
    assert secret_manager._project_id() == "primary"


def test_project_id_falls_back_in_order(monkeypatch):
    monkeypatch.delenv("GOOGLE_CLOUD_PROJECT", raising=False)
    monkeypatch.setenv("GCLOUD_PROJECT", "secondary")
    monkeypatch.setenv("GCP_PROJECT_ID", "tertiary")
    assert secret_manager._project_id() == "secondary"

    monkeypatch.delenv("GCLOUD_PROJECT", raising=False)
    assert secret_manager._project_id() == "tertiary"


def test_project_id_uses_cloudsdk_core_project(monkeypatch):
    monkeypatch.delenv("GOOGLE_CLOUD_PROJECT", raising=False)
    monkeypatch.delenv("GCLOUD_PROJECT", raising=False)
    monkeypatch.delenv("GCP_PROJECT_ID", raising=False)
    monkeypatch.setenv("CLOUDSDK_CORE_PROJECT", "cloudsdk-project")

    assert secret_manager._project_id() == "cloudsdk-project"


def test_project_resolution_uses_gcloud_config_when_env_missing(monkeypatch):
    monkeypatch.delenv("GOOGLE_CLOUD_PROJECT", raising=False)
    monkeypatch.delenv("GCLOUD_PROJECT", raising=False)
    monkeypatch.delenv("GCP_PROJECT_ID", raising=False)
    monkeypatch.delenv("CLOUDSDK_CORE_PROJECT", raising=False)
    monkeypatch.setattr(secret_manager, "_project_id_from_gcloud_config", lambda: ("config-project", "gcloud_config"))
    monkeypatch.setattr(secret_manager, "_project_id_from_adc", lambda: ("", "none"))

    resolution = secret_manager.get_project_id_resolution()

    assert resolution["projectConfigured"] is True
    assert resolution["projectId"] == "config-project"
    assert resolution["projectSource"] == "gcloud_config"


def test_project_resolution_uses_adc_when_other_sources_missing(monkeypatch):
    monkeypatch.delenv("GOOGLE_CLOUD_PROJECT", raising=False)
    monkeypatch.delenv("GCLOUD_PROJECT", raising=False)
    monkeypatch.delenv("GCP_PROJECT_ID", raising=False)
    monkeypatch.delenv("CLOUDSDK_CORE_PROJECT", raising=False)
    monkeypatch.setattr(secret_manager, "_project_id_from_gcloud_config", lambda: ("", "none"))
    monkeypatch.setattr(secret_manager, "_project_id_from_adc", lambda: ("adc-project", "adc"))

    resolution = secret_manager.get_project_id_resolution()

    assert resolution["projectConfigured"] is True
    assert resolution["projectId"] == "adc-project"
    assert resolution["projectSource"] == "adc"


@pytest.mark.parametrize("value", ["1", "true", "YES", "On", "oN"])
def test_skip_secret_manager_with_truthy_flags(monkeypatch, value):
    monkeypatch.setenv("SKIP_GCP_SECRET_MANAGER", value)
    assert secret_manager._skip_secret_manager() is True


def test_skip_secret_manager_false_for_missing_or_empty(monkeypatch):
    monkeypatch.delenv("SKIP_GCP_SECRET_MANAGER", raising=False)
    monkeypatch.delenv("PYTEST_CURRENT_TEST", raising=False)
    assert secret_manager._skip_secret_manager() is False


def test_skip_secret_manager_true_for_pytest_current_test(monkeypatch):
    monkeypatch.delenv("SKIP_GCP_SECRET_MANAGER", raising=False)
    monkeypatch.setenv("PYTEST_CURRENT_TEST", "tests/example.py::test_case")
    assert secret_manager._skip_secret_manager() is True


def test_iter_secret_name_overrides_and_names_ordering(monkeypatch):
    monkeypatch.setenv("MY_SECRET_NAME", "alpha")
    monkeypatch.setenv("ALT_SECRET_NAME", "beta")
    monkeypatch.delenv("EMPTY_SECRET_NAME", raising=False)

    overrides = secret_manager._iter_secret_name_overrides("MY", ("ALT", "EMPTY"))
    assert overrides == ["alpha", "beta"]

    names = secret_manager._iter_secret_names("MY", ("ALT", "EMPTY"), ("alpha", "gamma"))
    assert names == ["alpha", "beta", "gamma", "MY", "ALT", "EMPTY"]


def test_cache_put_and_cache_get_expire(monkeypatch):
    monkeypatch.setattr(secret_manager.time, "time", lambda: 1000.0)
    secret_manager._cache_put("cache:test", "value", 10.0)
    assert secret_manager._cache_get("cache:test") == "value"

    monkeypatch.setattr(secret_manager.time, "time", lambda: 1011.0)
    assert secret_manager._cache_get("cache:test") is None


def test_secret_resource_path_prefers_existing_secret_path():
    assert (
        secret_manager._secret_resource_path("proj", "projects/team/secrets/EXISTING/versions/latest")
        == "projects/team/secrets/EXISTING/versions/latest"
    )
    assert (
        secret_manager._secret_resource_path("proj", "NAME")
        == "projects/proj/secrets/NAME/versions/latest"
    )


def test_get_sm_client_builds_and_caches_client():
    fake_client = object()

    class FakeClientFactory:
        def __call__(self):
            return fake_client

    fake_secret_manager_mod = type(sys)("google.cloud.secretmanager")
    fake_secret_manager_mod.SecretManagerServiceClient = FakeClientFactory()
    fake_cloud_mod = type(sys)("google.cloud")
    fake_google_mod = type(sys)("google")

    fake_cloud_mod.secretmanager = fake_secret_manager_mod
    fake_google_mod.cloud = fake_cloud_mod

    with patch.dict(
        "sys.modules",
        {
            "google": fake_google_mod,
            "google.cloud": fake_cloud_mod,
            "google.cloud.secretmanager": fake_secret_manager_mod,
        },
    ):
        first = secret_manager._get_sm_client()
        second = secret_manager._get_sm_client()

    assert first is fake_client
    assert second is first


def test_get_sm_client_temporarily_unshadows_local_proto(monkeypatch):
    fake_client = object()

    class FakeClientFactory:
        def __call__(self):
            return fake_client

    fake_secret_manager_mod = type(sys)("google.cloud.secretmanager")
    fake_secret_manager_mod.SecretManagerServiceClient = FakeClientFactory()
    fake_cloud_mod = type(sys)("google.cloud")
    fake_google_mod = type(sys)("google")

    fake_cloud_mod.secretmanager = fake_secret_manager_mod
    fake_google_mod.cloud = fake_cloud_mod

    local_proto = type(sys)("proto")
    local_proto.__file__ = str((Path(secret_manager.__file__).resolve().parents[1] / "proto" / "__init__.py"))
    monkeypatch.setitem(sys.modules, "proto", local_proto)

    with patch.dict(
        "sys.modules",
        {
            "google": fake_google_mod,
            "google.cloud": fake_cloud_mod,
            "google.cloud.secretmanager": fake_secret_manager_mod,
        },
        clear=False,
    ):
        client = secret_manager._get_sm_client()

    assert client is fake_client
    assert sys.modules["proto"] is local_proto


def test_get_sm_client_import_error_sets_retry_window(monkeypatch):
    original_import = builtins.__import__

    def fake_import(name, globals=None, locals=None, fromlist=(), level=0):
        if name in {"google", "google.cloud", "google.cloud.secretmanager"}:
            raise ImportError("missing google cloud secretmanager")
        return original_import(name, globals, locals, fromlist, level)

    with patch("builtins.__import__", side_effect=fake_import):
        client = secret_manager._get_sm_client()

    assert client is None
    assert secret_manager._sm_client_init_failed is True
    assert secret_manager._sm_client_retry_at > 0


def test_get_sm_client_returns_none_while_retry_window_active(monkeypatch):
    monkeypatch.setattr(secret_manager, "_sm_client", None)
    monkeypatch.setattr(secret_manager, "_sm_client_init_failed", True)
    monkeypatch.setattr(secret_manager, "_sm_client_retry_at", 9999.0)
    monkeypatch.setattr(secret_manager.time, "time", lambda: 1000.0)

    assert secret_manager._get_sm_client() is None


def test_get_sm_client_returns_cached_client_after_lock_recheck(monkeypatch):
    fake_client = object()

    class FakeLock:
        def __enter__(self):
            secret_manager._sm_client = fake_client
            return self

        def __exit__(self, exc_type, exc, tb):
            return False

    monkeypatch.setattr(secret_manager, "_sm_client", None)
    monkeypatch.setattr(secret_manager, "_client_lock", FakeLock())

    assert secret_manager._get_sm_client() is fake_client


def test_get_sm_client_returns_none_after_lock_retry_recheck(monkeypatch):
    class FakeLock:
        def __enter__(self):
            secret_manager._sm_client_init_failed = True
            secret_manager._sm_client_retry_at = 9999.0
            return self

        def __exit__(self, exc_type, exc, tb):
            return False

    monkeypatch.setattr(secret_manager, "_sm_client", None)
    monkeypatch.setattr(secret_manager, "_sm_client_init_failed", False)
    monkeypatch.setattr(secret_manager, "_sm_client_retry_at", 0.0)
    monkeypatch.setattr(secret_manager.time, "time", lambda: 1000.0)
    monkeypatch.setattr(secret_manager, "_client_lock", FakeLock())

    assert secret_manager._get_sm_client() is None


def test_fetch_secret_from_manager_respects_skip_flag(monkeypatch):
    monkeypatch.delenv("PYTEST_CURRENT_TEST", raising=False)
    monkeypatch.setenv("SKIP_GCP_SECRET_MANAGER", "1")
    monkeypatch.delenv("GOOGLE_CLOUD_PROJECT", raising=False)
    assert secret_manager._fetch_secret_from_manager("ANY_SECRET") == ""


def test_fetch_secret_from_manager_caches_negative_result_when_client_missing(monkeypatch):
    monkeypatch.delenv("PYTEST_CURRENT_TEST", raising=False)
    monkeypatch.setenv("GOOGLE_CLOUD_PROJECT", "test-project")
    monkeypatch.setattr(secret_manager, "_get_sm_client", lambda: None)
    secret_manager.clear_secret_cache()

    assert secret_manager._fetch_secret_from_manager("ANY_SECRET") == ""
    assert secret_manager._cache_get("test-project:ANY_SECRET") == ""


def test_get_google_api_key_resolution_skips_when_disabled(monkeypatch):
    monkeypatch.delenv("GOOGLE_API_KEY", raising=False)
    monkeypatch.delenv("GEMINI_API_KEY", raising=False)
    monkeypatch.delenv("PYTEST_CURRENT_TEST", raising=False)
    monkeypatch.setenv("SKIP_GCP_SECRET_MANAGER", "true")
    monkeypatch.setenv("GOOGLE_CLOUD_PROJECT", "secret-proj")

    resolution = secret_manager.get_google_api_key_resolution()

    assert resolution["status"] == "skipped"
    assert resolution["source"] == "none"
    assert resolution["secretManagerEnabled"] is False
    assert resolution["projectConfigured"] is True
    assert resolution["projectSource"] == "env:GOOGLE_CLOUD_PROJECT"


def test_get_google_api_key_resolution_reports_env_source(monkeypatch):
    monkeypatch.setenv("GOOGLE_API_KEY", "env-key")
    monkeypatch.setenv("GOOGLE_CLOUD_PROJECT", "secret-proj")
    monkeypatch.delenv("PYTEST_CURRENT_TEST", raising=False)
    monkeypatch.setattr(secret_manager, "_fetch_secret_from_manager", lambda _name: "")

    resolution = secret_manager.get_google_api_key_resolution()

    assert resolution["status"] == "ok"
    assert resolution["source"] == "env"
    assert resolution["secretManagerEnabled"] is True
    assert resolution["projectSource"] == "env:GOOGLE_CLOUD_PROJECT"


def test_get_google_api_key_uses_alias_env_when_secret_manager_is_unavailable(monkeypatch):
    monkeypatch.delenv("GOOGLE_API_KEY", raising=False)
    monkeypatch.setenv("GEMINI_API_KEY", "alias-env-key")
    monkeypatch.delenv("PYTEST_CURRENT_TEST", raising=False)
    monkeypatch.setattr(secret_manager, "_fetch_secret_from_manager", lambda _name: "")

    assert secret_manager.get_google_api_key() == "alias-env-key"
