from __future__ import annotations

import builtins
import types
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

import telemetry
import proto as proto_mod


def test_configure_telemetry_without_sdk_only_patches_structlog(monkeypatch):
    monkeypatch.setattr(telemetry, "HAS_OTEL_SDK", False)
    with patch.object(telemetry, "_patch_structlog") as mock_patch:
        telemetry.configure_telemetry("1.2.3")
    mock_patch.assert_called_once_with("")


def test_configure_telemetry_with_exporter_failure_still_sets_provider(monkeypatch):
    monkeypatch.setattr(telemetry, "HAS_OTEL_SDK", True)
    monkeypatch.setattr(telemetry, "HAS_CLOUD_TRACE", True)
    monkeypatch.setenv("GOOGLE_CLOUD_PROJECT", "project-1")

    resource = object()
    provider = MagicMock()
    with patch.object(telemetry.Resource, "create", return_value=resource):
        with patch.object(telemetry, "TracerProvider", return_value=provider):
            with patch.object(telemetry, "CloudTraceSpanExporter", side_effect=RuntimeError("boom"), create=True):
                with patch.object(telemetry.trace, "set_tracer_provider") as mock_set_provider:
                    with patch.object(telemetry, "set_global_textmap") as mock_set_textmap:
                        with patch.object(telemetry, "_patch_structlog") as mock_patch:
                            telemetry.configure_telemetry("9.9.9")

    mock_set_provider.assert_called_once_with(provider)
    mock_set_textmap.assert_called_once()
    mock_patch.assert_called_once_with("project-1")


def test_gcp_trace_processor_injects_trace_fields(monkeypatch):
    monkeypatch.setenv("GOOGLE_CLOUD_PROJECT", "proj")
    ctx = types.SimpleNamespace(is_valid=True, trace_id=1, span_id=2, trace_flags=1)
    span = types.SimpleNamespace(get_span_context=lambda: ctx)

    with patch.object(telemetry.trace, "get_current_span", return_value=span):
        event = telemetry._gcp_trace_processor(None, "info", {})

    assert event["logging.googleapis.com/trace"] == "projects/proj/traces/00000000000000000000000000000001"
    assert event["logging.googleapis.com/spanId"] == "0000000000000002"
    assert event["logging.googleapis.com/traceSampled"] is True


def test_gcp_trace_processor_skips_invalid_context(monkeypatch):
    monkeypatch.setenv("GOOGLE_CLOUD_PROJECT", "proj")
    ctx = types.SimpleNamespace(is_valid=False, trace_id=1, span_id=2, trace_flags=0)
    span = types.SimpleNamespace(get_span_context=lambda: ctx)

    with patch.object(telemetry.trace, "get_current_span", return_value=span):
        event = telemetry._gcp_trace_processor(None, "info", {"message": "x"})

    assert event == {"message": "x"}


def test_make_grpc_server_interceptor_returns_none_when_import_missing():
    original_import = builtins.__import__

    def importer(name, globals=None, locals=None, fromlist=(), level=0):
        if name == "opentelemetry.instrumentation.grpc":
            raise ImportError("missing")
        return original_import(name, globals, locals, fromlist, level)

    with patch("builtins.__import__", side_effect=importer):
        assert telemetry.make_grpc_server_interceptor() is None


def test_read_generated_version_returns_none_on_missing_pattern(tmp_path: Path):
    path = tmp_path / "generated.py"
    path.write_text("no matching header", encoding="utf-8")
    assert proto_mod._read_generated_version(path, proto_mod._PROTOBUF_HEADER_PATTERN) is None


def test_import_optional_records_error():
    proto_mod.PROTO_IMPORT_ERRORS.clear()
    with patch("proto.importlib.import_module", side_effect=RuntimeError("bad import")):
        assert proto_mod._import_optional(".missing_proto") is None
    assert "missing_proto" in proto_mod.PROTO_IMPORT_ERRORS


def test_require_generated_module_returns_existing_module(monkeypatch):
    module = types.ModuleType("dummy")
    monkeypatch.setitem(proto_mod.__dict__, "dummy_mod", module)
    assert proto_mod.require_generated_module("dummy_mod") is module


def test_require_generated_module_raises_with_diagnostics(monkeypatch):
    monkeypatch.setitem(proto_mod.__dict__, "missing_mod", None)
    monkeypatch.setitem(proto_mod.PROTO_IMPORT_ERRORS, "missing_mod", "nope")
    monkeypatch.setattr(
        proto_mod,
        "get_proto_runtime_diagnostics",
        lambda: {"protobufRuntimeVersion": "1", "grpcRuntimeVersion": "2"},
    )
    with pytest.raises(RuntimeError, match="missing_mod: nope"):
        proto_mod.require_generated_module("missing_mod")


def test_require_proto_runtime_compatibility_returns_when_clean(monkeypatch):
    monkeypatch.setattr(proto_mod, "PROTO_IMPORT_ERRORS", {})
    proto_mod.require_proto_runtime_compatibility()


def test_require_proto_runtime_compatibility_raises_when_imports_failed(monkeypatch):
    monkeypatch.setattr(proto_mod, "PROTO_IMPORT_ERRORS", {"llm_pb2": "bad"})
    monkeypatch.setattr(
        proto_mod,
        "get_proto_runtime_diagnostics",
        lambda: {
            "modules": {"llm": {"error": "bad"}},
            "protobufRuntimeVersion": "1",
            "grpcRuntimeVersion": "2",
        },
    )
    with pytest.raises(RuntimeError, match="protobuf/gRPC runtime mismatch"):
        proto_mod.require_proto_runtime_compatibility()


def test_grpc_runtime_pins_match_checked_in_stub_headers():
    requirements_path = Path(__file__).resolve().parents[1] / "requirements.txt"
    requirements = requirements_path.read_text(encoding="utf-8").splitlines()

    pins: dict[str, str] = {}
    for line in requirements:
        stripped = line.split("#", 1)[0].strip()
        if "==" not in stripped:
            continue
        package, version = (part.strip() for part in stripped.split("==", 1))
        pins[package] = version

    proto_dir = Path(proto_mod.__file__).resolve().parent
    llm_grpc_version = proto_mod._read_generated_version(proto_dir / "llm_pb2_grpc.py", proto_mod._GRPC_HEADER_PATTERN)
    wisdev_grpc_version = proto_mod._read_generated_version(proto_dir / "wisdev_pb2_grpc.py", proto_mod._GRPC_HEADER_PATTERN)

    assert llm_grpc_version
    assert llm_grpc_version == wisdev_grpc_version
    assert pins["grpcio"] == llm_grpc_version
    assert pins["grpcio-tools"] == wisdev_grpc_version


def test_protobuf_runtime_pin_satisfies_checked_in_stub_headers():
    requirements_path = Path(__file__).resolve().parents[1] / "requirements.txt"
    requirements = requirements_path.read_text(encoding="utf-8").splitlines()

    protobuf_pin = None
    for line in requirements:
        stripped = line.split("#", 1)[0].strip()
        if stripped.startswith("protobuf=="):
            protobuf_pin = stripped.split("==", 1)[1].strip()
            break

    assert protobuf_pin

    proto_dir = Path(proto_mod.__file__).resolve().parent
    llm_protobuf_version = proto_mod._read_generated_version(proto_dir / "llm_pb2.py", proto_mod._PROTOBUF_HEADER_PATTERN)
    wisdev_protobuf_version = proto_mod._read_generated_version(proto_dir / "wisdev_pb2.py", proto_mod._PROTOBUF_HEADER_PATTERN)

    assert llm_protobuf_version
    assert llm_protobuf_version == wisdev_protobuf_version

    def parse_version(value: str) -> tuple[int, ...]:
        return tuple(int(part) for part in value.split("."))

    assert parse_version(protobuf_pin) >= parse_version(llm_protobuf_version)
