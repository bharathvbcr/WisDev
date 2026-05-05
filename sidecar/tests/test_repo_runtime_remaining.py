from __future__ import annotations

import asyncio
import importlib.util
import sys
import types
from pathlib import Path
from unittest.mock import AsyncMock, MagicMock, patch

import pytest
from fastapi.testclient import TestClient

import main
import telemetry

_ORIGINAL_GRPC_SIDECAR_READY = main._grpc_sidecar_ready
_ORIGINAL_GRPC_SIDECAR_HEALTH = main._grpc_sidecar_health
_ORIGINAL_WAIT_FOR_GRPC_SIDECAR_READY = main._wait_for_grpc_sidecar_ready
_ORIGINAL_GRPC_HEARTBEAT_TASK = main._grpc_heartbeat_task
_TELEMETRY_PATH = Path(telemetry.__file__)


def _load_telemetry_with_import_failures(module_name: str, blocked: set[str]):
    spec = importlib.util.spec_from_file_location(module_name, _TELEMETRY_PATH)
    assert spec is not None
    assert spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    original_import = __import__

    def importer(name, globals=None, locals=None, fromlist=(), level=0):
        if name in blocked:
            raise ImportError(name)
        return original_import(name, globals, locals, fromlist, level)

    with patch("builtins.__import__", side_effect=importer):
        spec.loader.exec_module(module)
    return module


def test_telemetry_import_flags_fall_back_when_dependencies_fail():
    cloud_missing = _load_telemetry_with_import_failures(
        "telemetry_cloud_missing",
        {"opentelemetry.exporter.cloud_trace"},
    )
    assert cloud_missing.HAS_CLOUD_TRACE is False

    sdk_missing = _load_telemetry_with_import_failures(
        "telemetry_sdk_missing",
        {"opentelemetry.propagate"},
    )
    assert sdk_missing.HAS_OTEL_SDK is False


def test_telemetry_import_sets_cloud_trace_flag_when_exporter_exists():
    exporter_pkg = types.ModuleType("opentelemetry.exporter")
    cloud_trace_mod = types.ModuleType("opentelemetry.exporter.cloud_trace")
    cloud_trace_mod.CloudTraceSpanExporter = object
    spec = importlib.util.spec_from_file_location("telemetry_cloud_present", _TELEMETRY_PATH)
    assert spec is not None
    assert spec.loader is not None
    module = importlib.util.module_from_spec(spec)

    with patch.dict(
        sys.modules,
        {
            "opentelemetry.exporter": exporter_pkg,
            "opentelemetry.exporter.cloud_trace": cloud_trace_mod,
        },
        clear=False,
    ):
        spec.loader.exec_module(module)

    assert module.HAS_CLOUD_TRACE is True


def test_positive_float_env_accepts_override(monkeypatch):
    monkeypatch.setenv("PYTHON_SIDECAR_GRPC_STARTUP_TIMEOUT_SECONDS", "45.5")

    assert main._positive_float_env(
        "PYTHON_SIDECAR_GRPC_STARTUP_TIMEOUT_SECONDS",
        90.0,
    ) == 45.5


def test_positive_float_env_rejects_invalid_override(monkeypatch):
    monkeypatch.setenv("PYTHON_SIDECAR_GRPC_STARTUP_TIMEOUT_SECONDS", "0")

    with patch.object(main.logger, "warning") as mock_warning:
        assert main._positive_float_env(
            "PYTHON_SIDECAR_GRPC_STARTUP_TIMEOUT_SECONDS",
            90.0,
        ) == 90.0

    mock_warning.assert_called_once()


def test_configure_telemetry_ignores_span_processor_errors(monkeypatch):
    provider = MagicMock()
    monkeypatch.setenv("GOOGLE_CLOUD_PROJECT", "proj")

    with patch.object(telemetry, "HAS_OTEL_SDK", True):
        with patch.object(telemetry, "HAS_CLOUD_TRACE", True):
            with patch.object(telemetry, "Resource") as mock_resource:
                with patch.object(telemetry, "TracerProvider", return_value=provider):
                    with patch.object(telemetry, "CloudTraceSpanExporter", return_value="exporter", create=True):
                        with patch.object(telemetry, "BatchSpanProcessor", return_value="processor"):
                            with patch.object(telemetry, "_patch_structlog") as mock_patch_structlog:
                                with patch.object(telemetry.trace, "set_tracer_provider") as mock_set_provider:
                                    with patch.object(telemetry, "set_global_textmap"):
                                        provider.add_span_processor.side_effect = RuntimeError("boom")
                                        mock_resource.create.return_value = "resource"
                                        telemetry.configure_telemetry("1.2.3")

    mock_set_provider.assert_called_once_with(provider)
    mock_patch_structlog.assert_called_once_with("proj")


def test_probe_grpc_sidecar_status_reports_healthy_model():
    channel = types.SimpleNamespace(close=lambda: None)
    stub = types.SimpleNamespace(Health=lambda request, timeout=None: types.SimpleNamespace(ok=True, error=""))

    with patch("main.grpc.insecure_channel", return_value=channel):
        with patch("main.grpc.channel_ready_future") as mock_future:
            mock_future.return_value.result.return_value = None
            with patch("main.llm_pb2_grpc.LLMServiceStub", return_value=stub):
                assert main._probe_grpc_sidecar_status() == (True, True, "")


@pytest.mark.asyncio
async def test_grpc_sidecar_ready_covers_disabled_and_thread_probe_paths():
    with patch("main._grpc_disabled", return_value=True):
        assert await _ORIGINAL_GRPC_SIDECAR_READY() == (True, "")

    with patch("main._grpc_disabled", return_value=False):
        with patch("main.asyncio.to_thread", AsyncMock(return_value=(False, "down"))) as mock_to_thread:
            assert await _ORIGINAL_GRPC_SIDECAR_READY(timeout_seconds=2.0) == (False, "down")
    mock_to_thread.assert_awaited_once()


@pytest.mark.asyncio
async def test_grpc_sidecar_health_covers_all_status_branches():
    with patch("main._grpc_disabled", return_value=True):
        assert await _ORIGINAL_GRPC_SIDECAR_HEALTH() == ("disabled", "")

    with patch("main._grpc_disabled", return_value=False):
        with patch("main.asyncio.to_thread", AsyncMock(return_value=(False, False, "down"))):
            assert await _ORIGINAL_GRPC_SIDECAR_HEALTH() == ("unavailable", "down")

    with patch("main._grpc_disabled", return_value=False):
        with patch("main.asyncio.to_thread", AsyncMock(return_value=(True, True, ""))):
            assert await _ORIGINAL_GRPC_SIDECAR_HEALTH() == ("ok", "")

    with patch("main._grpc_disabled", return_value=False):
        with patch("main.asyncio.to_thread", AsyncMock(return_value=(True, False, "models"))):
            assert await _ORIGINAL_GRPC_SIDECAR_HEALTH() == ("degraded", "models")


@pytest.mark.asyncio
async def test_wait_for_grpc_sidecar_ready_retries_then_logs_ready():
    class FakeLoop:
        def __init__(self):
            self.current = 0.0

        def time(self):
            self.current += 0.1
            return self.current

    fake_event = MagicMock()
    fake_event.is_set.return_value = False
    fake_ready = AsyncMock(side_effect=[(False, "booting"), (True, "")])

    with patch.dict(
        _ORIGINAL_WAIT_FOR_GRPC_SIDECAR_READY.__globals__,
        {"_grpc_failed": fake_event, "_grpc_sidecar_ready": fake_ready},
    ):
        with patch("main.asyncio.get_running_loop", return_value=FakeLoop()):
            with patch("main.asyncio.sleep", AsyncMock()) as mock_sleep:
                with patch.object(main.logger, "info") as mock_info:
                    await _ORIGINAL_WAIT_FOR_GRPC_SIDECAR_READY(timeout_seconds=1.0)

    mock_sleep.assert_awaited_once()
    mock_info.assert_called_once()


@pytest.mark.asyncio
async def test_grpc_heartbeat_task_returns_immediately_when_disabled():
    with patch("main._grpc_disabled", return_value=True):
        assert await _ORIGINAL_GRPC_HEARTBEAT_TASK() is None


@pytest.mark.asyncio
async def test_grpc_heartbeat_task_resets_failures_after_recovery():
    fake_event = MagicMock()
    fake_event.is_set.return_value = False
    fake_ready = AsyncMock(side_effect=[(False, "bad"), (True, "")])
    sleep_calls = {"count": 0}

    async def fake_sleep(_seconds):
        sleep_calls["count"] += 1
        if sleep_calls["count"] >= 3:
            raise asyncio.CancelledError()

    with patch.dict(
        _ORIGINAL_GRPC_HEARTBEAT_TASK.__globals__,
        {"_grpc_failed": fake_event, "_grpc_sidecar_ready": fake_ready},
    ):
        with patch("main._grpc_disabled", return_value=False):
            with patch("main.asyncio.sleep", AsyncMock(side_effect=fake_sleep)):
                with patch("os._exit") as mock_exit:
                    with patch.object(main.logger, "warning") as mock_warning:
                        with pytest.raises(asyncio.CancelledError):
                            await _ORIGINAL_GRPC_HEARTBEAT_TASK()

    mock_warning.assert_called_once()
    mock_exit.assert_not_called()


def test_internal_key_middleware_logs_oidc_failure_then_falls_back_to_static_key(monkeypatch):
    oauth2_mod = types.ModuleType("google.oauth2")
    id_token_mod = types.ModuleType("google.oauth2.id_token")
    id_token_mod.verify_oauth2_token = lambda token, request, audience: (_ for _ in ()).throw(RuntimeError("bad"))
    oauth2_mod.id_token = id_token_mod
    transport_mod = types.ModuleType("google.auth.transport")
    requests_mod = types.ModuleType("google.auth.transport.requests")
    requests_mod.Request = lambda: object()
    transport_mod.requests = requests_mod
    google_auth_mod = types.ModuleType("google.auth")
    google_auth_mod.transport = transport_mod

    monkeypatch.setenv("INTERNAL_SERVICE_KEY", "secret")
    monkeypatch.setenv("OIDC_AUDIENCE", "aud")

    with patch.dict(
        sys.modules,
        {
            "google.oauth2": oauth2_mod,
            "google.oauth2.id_token": id_token_mod,
            "google.auth": google_auth_mod,
            "google.auth.transport": transport_mod,
            "google.auth.transport.requests": requests_mod,
        },
        clear=False,
    ):
        with patch.object(main.logger, "warning") as mock_warning:
            with patch("main.require_proto_runtime_compatibility"):
                with patch("main._wait_for_grpc_sidecar_ready", new_callable=AsyncMock):
                    with patch("main._start_grpc_server"):
                        with TestClient(main.app) as client:
                            response = client.get("/llm/health", headers={"Authorization": "Bearer secret"})

    assert response.status_code == 200
    mock_warning.assert_called_once()


def test_readiness_returns_ok_when_grpc_is_healthy():
    failed = MagicMock()
    failed.is_set.return_value = False

    with patch.object(main, "_grpc_failed", failed):
        with patch("main._grpc_disabled", return_value=False):
            with patch("main._grpc_sidecar_health", new_callable=AsyncMock, return_value=("ok", "")):
                with patch("main.require_proto_runtime_compatibility"):
                    with patch("main._wait_for_grpc_sidecar_ready", new_callable=AsyncMock):
                        with patch("main._start_grpc_server"):
                            with TestClient(main.app) as client:
                                response = client.get("/readiness")

    assert response.status_code == 200
    payload = response.json()
    assert payload["status"] == "ok"
    assert any(dep["status"] == "ok" for dep in payload["dependencies"] if dep["name"] == "grpc_sidecar")
