from __future__ import annotations

import sys
import types
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

import pytest

from services import secret_manager


@pytest.fixture(autouse=True)
def reset_secret_manager_state(monkeypatch):
    secret_manager.clear_secret_cache()
    monkeypatch.setattr(secret_manager, "_sm_client", None)
    monkeypatch.setattr(secret_manager, "_sm_client_init_failed", False)
    monkeypatch.setattr(secret_manager, "_sm_client_retry_at", 0.0)
    monkeypatch.setattr(secret_manager, "_project_resolution", None)
    yield
    secret_manager.clear_secret_cache()


def test_gcloud_config_dir_fallbacks(monkeypatch):
    monkeypatch.setenv("CLOUDSDK_CONFIG", "C:/cfg")
    assert secret_manager._gcloud_config_dir() == "C:/cfg"

    monkeypatch.delenv("CLOUDSDK_CONFIG", raising=False)
    monkeypatch.setenv("APPDATA", "C:/appdata")
    assert secret_manager._gcloud_config_dir().replace("\\", "/").endswith("C:/appdata/gcloud")

    monkeypatch.delenv("APPDATA", raising=False)
    monkeypatch.setenv("XDG_CONFIG_HOME", "C:/xdg")
    assert secret_manager._gcloud_config_dir().replace("\\", "/").endswith("C:/xdg/gcloud")


def test_project_id_from_gcloud_config_reads_active_config(tmp_path: Path, monkeypatch):
    config_dir = tmp_path / "gcloud"
    (config_dir / "configurations").mkdir(parents=True)
    (config_dir / "active_config").write_text("custom", encoding="utf-8")
    (config_dir / "configurations" / "config_custom").write_text("[core]\nproject = config-project\n", encoding="utf-8")
    monkeypatch.setenv("CLOUDSDK_CONFIG", str(config_dir))
    monkeypatch.delenv("GOOGLE_CLOUD_PROJECT", raising=False)

    assert secret_manager._project_id_from_gcloud_config() == ("config-project", "gcloud_config")


def test_project_id_from_gcloud_config_handles_missing_and_invalid_files(tmp_path: Path, monkeypatch):
    config_dir = tmp_path / "gcloud"
    (config_dir / "configurations").mkdir(parents=True)
    monkeypatch.setenv("CLOUDSDK_CONFIG", str(config_dir))

    assert secret_manager._project_id_from_gcloud_config() == ("", "none")

    (config_dir / "active_config").write_text("bad", encoding="utf-8")
    (config_dir / "configurations" / "config_bad").write_text("not an ini file", encoding="utf-8")
    assert secret_manager._project_id_from_gcloud_config() == ("", "none")

    (config_dir / "active_config").write_text("empty", encoding="utf-8")
    (config_dir / "configurations" / "config_empty").write_text("[core]\nother = value\n", encoding="utf-8")
    assert secret_manager._project_id_from_gcloud_config() == ("", "none")


def test_project_id_from_adc_success_and_failure(monkeypatch):
    google_auth_mod = types.ModuleType("google.auth")
    google_auth_mod.default = lambda scopes=None: (object(), "adc-project")
    google_module = types.ModuleType("google")
    google_module.auth = google_auth_mod
    with patch.dict(
        "sys.modules",
        {"google": google_module, "google.auth": google_auth_mod},
        clear=False,
    ):
        assert secret_manager._project_id_from_adc() == ("adc-project", "adc")

    google_auth_fail = types.ModuleType("google.auth")
    google_auth_fail.default = lambda scopes=None: (_ for _ in ()).throw(RuntimeError("boom"))
    google_module_fail = types.ModuleType("google")
    google_module_fail.auth = google_auth_fail
    with patch.dict(
        "sys.modules",
        {"google": google_module_fail, "google.auth": google_auth_fail},
        clear=False,
    ):
        assert secret_manager._project_id_from_adc() == ("", "none")

    google_auth_blank = types.ModuleType("google.auth")
    google_auth_blank.default = lambda scopes=None: (object(), "")
    google_module_blank = types.ModuleType("google")
    google_module_blank.auth = google_auth_blank
    with patch.dict(
        "sys.modules",
        {"google": google_module_blank, "google.auth": google_auth_blank},
        clear=False,
    ):
        assert secret_manager._project_id_from_adc() == ("", "none")


def test_is_local_proto_module_handles_false_and_oserror(monkeypatch):
    assert secret_manager._is_local_proto_module(SimpleNamespace()) is False

    dummy = SimpleNamespace(__file__="C:/tmp/proto.py")
    with patch.object(secret_manager.Path, "resolve", side_effect=OSError("boom")):
        assert secret_manager._is_local_proto_module(dummy) is False


def test_without_local_proto_shadow_removes_new_proto_module(monkeypatch):
    local_proto = types.ModuleType("proto")
    local_proto.__file__ = str((Path(secret_manager.__file__).resolve().parents[1] / "proto" / "__init__.py"))
    monkeypatch.setitem(sys.modules, "proto", local_proto)
    monkeypatch.setitem(sys.modules, "proto.new_module", types.ModuleType("proto.new_module"))

    with secret_manager._without_local_proto_shadow():
        assert "proto" not in sys.modules or sys.modules["proto"] is not local_proto

    assert sys.modules["proto"] is local_proto


def test_get_secret_prefers_env_and_then_secret_manager(monkeypatch):
    monkeypatch.setenv("MY_SECRET", "env-value")
    assert secret_manager.get_secret("MY_SECRET") == "env-value"

    monkeypatch.delenv("MY_SECRET", raising=False)
    monkeypatch.setattr(secret_manager, "_fetch_secret_from_manager", lambda secret_name: "gsm-value" if secret_name == "MY_SECRET" else "")
    assert secret_manager.get_secret("MY_SECRET") == "gsm-value"
    monkeypatch.setattr(secret_manager, "_fetch_secret_from_manager", lambda secret_name: "")
    assert secret_manager.get_secret("MISSING_SECRET") == ""


def test_resolve_google_api_key_uses_secret_manager_and_env_fallback(monkeypatch):
    monkeypatch.setattr(secret_manager, "_skip_secret_manager", lambda: False)
    monkeypatch.setattr(secret_manager, "_project_id", lambda: "project-1")
    monkeypatch.setattr(secret_manager, "_fetch_secret_from_manager", lambda name: "secret-key")

    value, source = secret_manager._resolve_google_api_key(prefer_secret_manager=True)
    assert (value, source) == ("secret-key", "secret_manager")

    monkeypatch.setenv("GOOGLE_API_KEY", "env-key")
    value, source = secret_manager._resolve_google_api_key(prefer_secret_manager=False)
    assert (value, source) == ("env-key", "env")

    monkeypatch.delenv("GOOGLE_API_KEY", raising=False)
    monkeypatch.setattr(secret_manager, "_fetch_secret_from_manager", lambda name: "late-secret")
    value, source = secret_manager._resolve_google_api_key(prefer_secret_manager=False)
    assert (value, source) == ("late-secret", "secret_manager")


def test_google_api_key_resolution_reports_skipped_and_missing(monkeypatch):
    monkeypatch.setattr(secret_manager, "_skip_secret_manager", lambda: True)
    monkeypatch.setattr(secret_manager, "_project_id", lambda: "")
    monkeypatch.setattr(secret_manager, "_project_id_source", lambda: "none")
    resolution = secret_manager.get_google_api_key_resolution()
    assert resolution["status"] == "skipped"

    monkeypatch.setattr(secret_manager, "_skip_secret_manager", lambda: False)
    resolution = secret_manager.get_google_api_key_resolution()
    assert resolution["status"] == "missing"
