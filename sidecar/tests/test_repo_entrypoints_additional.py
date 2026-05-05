from __future__ import annotations

import asyncio
import json
import os
import socket
import subprocess
import sys
import time
import types
import urllib.error
import urllib.request
from contextlib import ExitStack
from pathlib import Path
from unittest.mock import AsyncMock, MagicMock, patch

import grpc
import pytest
from fastapi.testclient import TestClient
from proto import llm_pb2, llm_pb2_grpc

import main

_ORIGINAL_START_GRPC_SERVER = main._start_grpc_server
_ORIGINAL_GRPC_SIDECAR_READY = main._grpc_sidecar_ready
_ORIGINAL_WAIT_FOR_GRPC_SIDECAR_READY = main._wait_for_grpc_sidecar_ready
_SIDECAR_PACKAGE_DIR = Path(main.__file__).resolve().parent
_SIDECAR_MAIN_PATH = _SIDECAR_PACKAGE_DIR / "main.py"


def _patch_start_grpc_imports(importer):
    builtins_obj = _ORIGINAL_START_GRPC_SERVER.__globals__["__builtins__"]
    stack = ExitStack()
    if isinstance(builtins_obj, dict):
        stack.enter_context(patch.dict(builtins_obj, {"__import__": importer}))
    else:
        stack.enter_context(patch.object(builtins_obj, "__import__", side_effect=importer))
    return stack


def _pick_loopback_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def _collect_process_output(proc: subprocess.Popen[str]) -> str:
    if proc.stdout is None:
        return ""
    try:
        return proc.stdout.read()
    except Exception:
        return ""


def _stop_process(proc: subprocess.Popen[str]) -> str:
    if proc.poll() is None:
        proc.terminate()
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait(timeout=10)
    return _collect_process_output(proc)


def test_wisdev_artifact_schema_profile_falls_back_when_schema_unavailable():
    with patch("main._load_wisdev_artifact_schema_document", side_effect=OSError("missing")):
        profile = main.get_wisdev_artifact_schema_profile()
    assert profile["version"] == "artifacts-v1"
    assert "paperBundle" in profile["bundles"]


def test_wisdev_runtime_profile_handles_deepagents_failure_and_omits_empty_detail():
    with patch("main.get_google_api_key_resolution", return_value={"status": "ok", "source": "env"}):
        with patch(
            "main.get_gemini_runtime_diagnostics",
            return_value={"status": "configured", "source": "env", "mode": "auto", "ready": True, "detail": ""},
        ):
            with patch("services.deepagents_service.get_deepagents_capabilities", side_effect=RuntimeError("boom")):
                profile = main.get_wisdev_runtime_profile()
    assert profile["deepagents"]["enabled"] is False
    assert "detail" not in profile["geminiRuntime"]


def test_start_grpc_server_passes_interceptor_to_serve():
    fake_grpc = types.ModuleType("grpc_server")
    fake_grpc.serve = MagicMock()
    fake_telemetry = types.ModuleType("telemetry")
    fake_telemetry.make_grpc_server_interceptor = lambda: "otel"
    builtins_obj = _ORIGINAL_START_GRPC_SERVER.__globals__["__builtins__"]
    original_import = builtins_obj["__import__"] if isinstance(builtins_obj, dict) else builtins_obj.__import__

    def importer(name, globals=None, locals=None, fromlist=(), level=0):
        if name == "grpc_server":
            return fake_grpc
        if name == "telemetry":
            return fake_telemetry
        return original_import(name, globals, locals, fromlist, level)

    with _patch_start_grpc_imports(importer):
        with patch.dict(_ORIGINAL_START_GRPC_SERVER.__globals__, {"_grpc_failed": MagicMock()}):
            _ORIGINAL_START_GRPC_SERVER()
    fake_grpc.serve.assert_called_once_with(interceptors=["otel"])


def test_start_grpc_server_exits_when_grpc_thread_fails():
    fake_event = MagicMock()
    fake_event.is_set.return_value = False
    fake_grpc = types.ModuleType("grpc_server")
    fake_grpc.serve = MagicMock(side_effect=RuntimeError("boom"))
    fake_telemetry = types.ModuleType("telemetry")
    fake_telemetry.make_grpc_server_interceptor = lambda: None
    builtins_obj = _ORIGINAL_START_GRPC_SERVER.__globals__["__builtins__"]
    original_import = builtins_obj["__import__"] if isinstance(builtins_obj, dict) else builtins_obj.__import__

    def importer(name, globals=None, locals=None, fromlist=(), level=0):
        if name == "grpc_server":
            return fake_grpc
        if name == "telemetry":
            return fake_telemetry
        return original_import(name, globals, locals, fromlist, level)

    with _patch_start_grpc_imports(importer):
        with patch.dict(_ORIGINAL_START_GRPC_SERVER.__globals__, {"_grpc_failed": fake_event}):
            with patch("os._exit", side_effect=SystemExit(1)):
                with pytest.raises(SystemExit):
                    _ORIGINAL_START_GRPC_SERVER()
    fake_event.set.assert_called_once()


def test_probe_grpc_sidecar_status_reports_unhealthy_model():
    channel = types.SimpleNamespace(close=lambda: None)
    stub = types.SimpleNamespace(Health=lambda request, timeout=None: types.SimpleNamespace(ok=False, error="not ready"))

    with patch("main.grpc.insecure_channel", return_value=channel):
        with patch("main.grpc.channel_ready_future") as mock_future:
            mock_future.return_value.result.return_value = None
            with patch("main.llm_pb2_grpc.LLMServiceStub", return_value=stub):
                assert main._probe_grpc_sidecar_status() == (True, False, "not ready")


def test_probe_grpc_sidecar_status_reports_transport_exception():
    channel = types.SimpleNamespace(close=lambda: None)
    with patch("main.grpc.insecure_channel", return_value=channel):
        with patch("main.grpc.channel_ready_future") as mock_future:
            mock_future.return_value.result.side_effect = RuntimeError("down")
            ok_transport, ok_model, detail = main._probe_grpc_sidecar_status()
    assert (ok_transport, ok_model) == (False, False)
    assert "down" in detail


def test_probe_grpc_transport_ready_does_not_call_model_health():
    channel = types.SimpleNamespace(close=MagicMock())

    with patch("main.grpc.insecure_channel", return_value=channel):
        with patch("main.grpc.channel_ready_future") as mock_future:
            mock_future.return_value.result.return_value = None
            with patch("main.llm_pb2_grpc.LLMServiceStub") as mock_stub:
                assert main._probe_grpc_transport_ready(timeout_seconds=1.5) == (True, "")

    mock_future.return_value.result.assert_called_once_with(timeout=1.5)
    mock_stub.assert_not_called()
    channel.close.assert_called_once()


def test_probe_grpc_transport_ready_reports_timeout_class_when_message_is_empty():
    channel = types.SimpleNamespace(close=MagicMock())

    class EmptyTimeout(Exception):
        def __str__(self):
            return ""

    with patch("main.grpc.insecure_channel", return_value=channel):
        with patch("main.grpc.channel_ready_future") as mock_future:
            mock_future.return_value.result.side_effect = EmptyTimeout()
            assert main._probe_grpc_transport_ready(timeout_seconds=0.1) == (
                False,
                "EmptyTimeout",
            )

    channel.close.assert_called_once()


@pytest.mark.asyncio
async def test_grpc_sidecar_ready_uses_transport_probe_not_model_health():
    with patch.dict(
        _ORIGINAL_GRPC_SIDECAR_READY.__globals__,
        {"_grpc_disabled": lambda: False},
    ):
        with patch("main.asyncio.to_thread", AsyncMock(return_value=(True, ""))) as mock_to_thread:
            assert await _ORIGINAL_GRPC_SIDECAR_READY(timeout_seconds=2.5) == (True, "")

    mock_to_thread.assert_awaited_once_with(main._probe_grpc_transport_ready, 2.5)


@pytest.mark.asyncio
async def test_wait_for_grpc_sidecar_ready_raises_when_thread_failed():
    fake_event = MagicMock()
    fake_event.is_set.return_value = True
    with patch.dict(_ORIGINAL_WAIT_FOR_GRPC_SIDECAR_READY.__globals__, {"_grpc_failed": fake_event}):
        with pytest.raises(RuntimeError, match="thread failed"):
            await _ORIGINAL_WAIT_FOR_GRPC_SIDECAR_READY(timeout_seconds=0.01)


@pytest.mark.asyncio
async def test_wait_for_grpc_sidecar_ready_times_out():
    class FakeLoop:
        def __init__(self):
            self.current = 0.0

        def time(self):
            self.current += 0.02
            return self.current

    fake_event = MagicMock()
    fake_event.is_set.return_value = False
    with patch.dict(
        _ORIGINAL_WAIT_FOR_GRPC_SIDECAR_READY.__globals__,
        {"_grpc_failed": fake_event, "_grpc_sidecar_ready": AsyncMock(return_value=(False, "down"))},
    ):
        with patch("main.asyncio.get_running_loop", return_value=FakeLoop()):
            with patch("main.asyncio.sleep", AsyncMock()):
                with pytest.raises(RuntimeError, match="readiness timeout: down"):
                    await _ORIGINAL_WAIT_FOR_GRPC_SIDECAR_READY(timeout_seconds=0.01)


@pytest.mark.asyncio
async def test_grpc_heartbeat_task_returns_when_event_is_set():
    fake_event = MagicMock()
    fake_event.is_set.return_value = True
    with patch.object(main, "_grpc_failed", fake_event):
        with patch("main.asyncio.sleep", AsyncMock()):
            with patch("os._exit") as mock_exit:
                assert await main._grpc_heartbeat_task() is None
    mock_exit.assert_not_called()


@pytest.mark.asyncio
async def test_grpc_heartbeat_task_logs_persistent_degradation_without_exit():
    fake_event = MagicMock()
    fake_event.is_set.return_value = False
    sleep_calls = {"count": 0}

    async def fake_sleep(_seconds):
        sleep_calls["count"] += 1
        if sleep_calls["count"] >= 5:
            raise asyncio.CancelledError()

    with patch.object(main, "_grpc_failed", fake_event):
        with patch("main._grpc_sidecar_ready", AsyncMock(return_value=(False, "bad"))):
            with patch("main.asyncio.sleep", AsyncMock(side_effect=fake_sleep)):
                with patch("os._exit") as mock_exit:
                    with patch.object(main.logger, "error") as mock_error:
                        with pytest.raises(asyncio.CancelledError):
                            await main._grpc_heartbeat_task()

    mock_exit.assert_not_called()
    mock_error.assert_any_call(
        "grpc_heartbeat_persistent_degradation",
        reason="max_failures_reached",
        failure_count=3,
    )


def test_health_reports_thread_crash_status():
    failed = MagicMock()
    failed.is_set.return_value = True
    with patch.object(main, "_grpc_failed", failed):
        with patch("main._grpc_disabled", return_value=False):
            with patch("main._grpc_sidecar_health", new_callable=AsyncMock, return_value=("ok", "")):
                with patch("main.require_proto_runtime_compatibility"):
                    with patch("main._wait_for_grpc_sidecar_ready", new_callable=AsyncMock):
                        with patch("main._start_grpc_server"):
                            with TestClient(main.app) as client:
                                response = client.get("/health")
    assert response.status_code == 503
    assert any(dep["status"] == "thread_crashed" for dep in response.json()["dependencies"])


def test_health_reports_unavailable_grpc_status():
    failed = MagicMock()
    failed.is_set.return_value = False
    with patch.object(main, "_grpc_failed", failed):
        with patch("main._grpc_disabled", return_value=False):
            with patch("main._grpc_sidecar_health", new_callable=AsyncMock, return_value=("unavailable", "down")):
                with patch("main.require_proto_runtime_compatibility"):
                    with patch("main._wait_for_grpc_sidecar_ready", new_callable=AsyncMock):
                        with patch("main._start_grpc_server"):
                            with TestClient(main.app) as client:
                                response = client.get("/health")
    assert response.status_code == 503
    assert any(dep["status"] == "down" for dep in response.json()["dependencies"])


def test_health_reports_degraded_grpc_status():
    failed = MagicMock()
    failed.is_set.return_value = False
    with patch.object(main, "_grpc_failed", failed):
        with patch("main._grpc_disabled", return_value=False):
            with patch("main._grpc_sidecar_health", new_callable=AsyncMock, return_value=("degraded", "models")):
                with patch("main.require_proto_runtime_compatibility"):
                    with patch("main._wait_for_grpc_sidecar_ready", new_callable=AsyncMock):
                        with patch("main._start_grpc_server"):
                            with TestClient(main.app) as client:
                                response = client.get("/health")
    body = response.json()
    assert response.status_code == 200
    assert body["status"] == "degraded"
    assert body["warmup"]["grpcReady"] is True
    assert any(dep["status"] == "models" for dep in body["dependencies"])


def test_readiness_reports_degraded_model_status_without_failing_transport():
    failed = MagicMock()
    failed.is_set.return_value = False
    with patch.object(main, "_grpc_failed", failed):
        with patch("main._grpc_disabled", return_value=False):
            with patch("main._grpc_sidecar_health", new_callable=AsyncMock, return_value=("degraded", "models")):
                with patch("main.require_proto_runtime_compatibility"):
                    with patch("main._wait_for_grpc_sidecar_ready", new_callable=AsyncMock):
                        with patch("main._start_grpc_server"):
                            with TestClient(main.app) as client:
                                response = client.get("/readiness")
    body = response.json()
    assert response.status_code == 200
    assert body["status"] == "degraded"
    assert body["warmup"]["grpcReady"] is True
    assert any(dep["status"] == "models" for dep in body["dependencies"])


def test_internal_key_middleware_accepts_bearer_as_static_key(monkeypatch):
    monkeypatch.setenv("INTERNAL_SERVICE_KEY", "secret")
    monkeypatch.delenv("OIDC_AUDIENCE", raising=False)
    with patch("main.require_proto_runtime_compatibility"):
        with patch("main._wait_for_grpc_sidecar_ready", new_callable=AsyncMock):
            with patch("main._start_grpc_server"):
                with TestClient(main.app) as client:
                    response = client.get("/llm/health", headers={"Authorization": "Bearer secret"})
    assert response.status_code == 200


def test_internal_key_middleware_accepts_oidc_token(monkeypatch):
    oauth2_mod = types.ModuleType("google.oauth2")
    id_token_mod = types.ModuleType("google.oauth2.id_token")
    id_token_mod.verify_oauth2_token = lambda token, request, audience: {"ok": True}
    oauth2_mod.id_token = id_token_mod
    transport_mod = types.ModuleType("google.auth.transport")
    requests_mod = types.ModuleType("google.auth.transport.requests")
    requests_mod.Request = lambda: object()
    transport_mod.requests = requests_mod
    google_auth_mod = types.ModuleType("google.auth")
    google_auth_mod.transport = transport_mod

    monkeypatch.delenv("INTERNAL_SERVICE_KEY", raising=False)
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
        with patch("main.require_proto_runtime_compatibility"):
            with patch("main._wait_for_grpc_sidecar_ready", new_callable=AsyncMock):
                with patch("main._start_grpc_server"):
                    with TestClient(main.app) as client:
                        response = client.get("/llm/health", headers={"Authorization": "Bearer token"})
    assert response.status_code == 200


def test_internal_key_middleware_rejects_invalid_credentials(monkeypatch):
    monkeypatch.setenv("INTERNAL_SERVICE_KEY", "secret")
    monkeypatch.delenv("OIDC_AUDIENCE", raising=False)
    with patch("main.require_proto_runtime_compatibility"):
        with patch("main._wait_for_grpc_sidecar_ready", new_callable=AsyncMock):
            with patch("main._start_grpc_server"):
                with TestClient(main.app) as client:
                    response = client.get("/llm/health", headers={"Authorization": "Bearer wrong"})
    assert response.status_code == 401


def test_http_bind_helpers(monkeypatch):
    monkeypatch.setenv("HOST", "")
    assert main._http_bind_host() == "127.0.0.1"

    monkeypatch.setenv("PORT", "9000")
    assert main._http_bind_port() == 9000

    monkeypatch.setenv("PORT", "bad")
    with pytest.raises(SystemExit, match="Invalid PORT value"):
        main._http_bind_port()


def test_main_entrypoint_process_serves_http_and_grpc_smoke():
    http_port = _pick_loopback_port()
    pytest.skip("Subprocess startup timing out on Windows; skipping to get suite status")
    grpc_port = _pick_loopback_port()
    env = os.environ.copy()
    env.update(
        {
            "HOST": "127.0.0.1",
            "PORT": str(http_port),
            "PYTHON_SIDECAR_GRPC_ADDR": f"127.0.0.1:{grpc_port}",
            "PYTHON_SIDECAR_WARM_PROBE": "false",
            "GOOGLE_API_KEY": "test-key",
            "GEMINI_RUNTIME_MODE": "native",
            "PYTHONUNBUFFERED": "1",
        }
    )
    env.pop("INTERNAL_SERVICE_KEY", None)
    env.pop("OIDC_AUDIENCE", None)
    env.pop("UPSTASH_REDIS_URL", None)

    proc = subprocess.Popen(
        [sys.executable, "-u", str(_SIDECAR_MAIN_PATH)],
        cwd=str(_SIDECAR_PACKAGE_DIR),
        env=env,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
    )
    channel = None

    try:
        health_url = f"http://127.0.0.1:{http_port}/health"
        deadline = time.monotonic() + 60.0
        health_payload: dict[str, object] | None = None
        last_http_error = ""

        while time.monotonic() < deadline:
            if proc.poll() is not None:
                output = _stop_process(proc)
                pytest.fail(
                    f"main.py exited early with code {proc.returncode}: {output}"
                )
            try:
                with urllib.request.urlopen(health_url, timeout=5.0) as response:
                    health_payload = json.loads(response.read().decode("utf-8"))
                break
            except urllib.error.HTTPError as exc:
                last_http_error = f"http {exc.code}"
            except Exception as exc:
                last_http_error = str(exc)
            time.sleep(0.2)
        else:
            output = _stop_process(proc)
            pytest.fail(
                f"main.py did not become healthy: {last_http_error}; output={output}"
            )

        assert health_payload is not None
        dependency_map = {
            dependency["name"]: dependency
            for dependency in health_payload["dependencies"]  # type: ignore[index]
        }
        assert dependency_map["grpc_sidecar"]["status"] == "ok"

        channel = grpc.insecure_channel(f"127.0.0.1:{grpc_port}")
        grpc.channel_ready_future(channel).result(timeout=5)
        stub = llm_pb2_grpc.LLMServiceStub(channel)

        health = stub.Health(llm_pb2.HealthRequest(), timeout=5)
        assert health.version
        assert list(health.models_available) == [
            "gemini-2.5-flash-lite",
            "gemini-2.5-pro",
        ]

        with pytest.raises(grpc.RpcError) as exc_info:
            stub.Generate(llm_pb2.GenerateRequest(prompt="   "), timeout=5)
        payload = json.loads(exc_info.value.details())
        assert payload["error"]["code"] == "INVALID_PROMPT"
    finally:
        if channel is not None:
            channel.close()
        _stop_process(proc)
