"""Canonical protobuf imports for the Python sidecar.

Active code should import `llm_pb2` / `llm_pb2_grpc` from this package instead
of referencing version-labelled module names directly. The generated
version-labelled files remain the wire-compatible source modules underneath.
"""

from __future__ import annotations

import importlib
import re
from pathlib import Path
from types import ModuleType

import grpc
from google.protobuf import __version__ as protobuf_runtime_version

_PROTO_DIR = Path(__file__).resolve().parent
_PROTOBUF_HEADER_PATTERN = re.compile(r"^# Protobuf Python Version:\s+(.+)$", re.MULTILINE)
_GRPC_HEADER_PATTERN = re.compile(r"^GRPC_GENERATED_VERSION\s*=\s*['\"](.+?)['\"]$", re.MULTILINE)

PROTO_IMPORT_ERRORS: dict[str, str] = {}


def _read_generated_version(path: Path, pattern: re.Pattern[str]) -> str | None:
    try:
        match = pattern.search(path.read_text(encoding="utf-8", errors="ignore"))
        return match.group(1) if match else None
    except Exception:
        return None


def _import_optional(name: str) -> ModuleType | None:
    try:
        return importlib.import_module(name, __name__)
    except Exception as exc:  # pragma: no cover - generated modules may be absent locally
        PROTO_IMPORT_ERRORS[name.rsplit(".", 1)[-1]] = str(exc)
        return None


llm_pb2: ModuleType | None = _import_optional(".llm_pb2")
llm_pb2_grpc: ModuleType | None = _import_optional(".llm_pb2_grpc")
wisdev_pb2: ModuleType | None = _import_optional(".wisdev_pb2")
wisdev_pb2_grpc: ModuleType | None = _import_optional(".wisdev_pb2_grpc")


def require_generated_module(name: str) -> ModuleType:
    module = globals().get(name)
    if isinstance(module, ModuleType):
        return module

    diagnostics = get_proto_runtime_diagnostics()
    detail = PROTO_IMPORT_ERRORS.get(name, "module did not import")
    raise RuntimeError(
        "python sidecar generated protobuf module unavailable: "
        f"{name}: {detail} "
        f"(protobuf={diagnostics['protobufRuntimeVersion']}, grpc={diagnostics['grpcRuntimeVersion']})"
    )


def get_proto_runtime_diagnostics() -> dict:
    return {
        "protobufRuntimeVersion": protobuf_runtime_version,
        "grpcRuntimeVersion": grpc.__version__,
        "modules": {
            "llm": {
                "protobufGeneratedVersion": _read_generated_version(_PROTO_DIR / "llm_pb2.py", _PROTOBUF_HEADER_PATTERN),
                "grpcGeneratedVersion": _read_generated_version(_PROTO_DIR / "llm_pb2_grpc.py", _GRPC_HEADER_PATTERN),
                "imported": llm_pb2 is not None and llm_pb2_grpc is not None,
                "error": PROTO_IMPORT_ERRORS.get("llm_pb2") or PROTO_IMPORT_ERRORS.get("llm_pb2_grpc") or "",
            },
            "wisdev": {
                "protobufGeneratedVersion": _read_generated_version(_PROTO_DIR / "wisdev_pb2.py", _PROTOBUF_HEADER_PATTERN),
                "grpcGeneratedVersion": _read_generated_version(_PROTO_DIR / "wisdev_pb2_grpc.py", _GRPC_HEADER_PATTERN),
                "imported": wisdev_pb2 is not None and wisdev_pb2_grpc is not None,
                "error": PROTO_IMPORT_ERRORS.get("wisdev_pb2") or PROTO_IMPORT_ERRORS.get("wisdev_pb2_grpc") or "",
            },
        },
    }


def require_proto_runtime_compatibility() -> None:
    if not PROTO_IMPORT_ERRORS:
        return

    diagnostics = get_proto_runtime_diagnostics()
    raise RuntimeError(
        "python sidecar protobuf/gRPC runtime mismatch: "
        f"{diagnostics['modules']} "
        f"(protobuf={diagnostics['protobufRuntimeVersion']}, grpc={diagnostics['grpcRuntimeVersion']})"
    )

__all__ = [
    "llm_pb2",
    "llm_pb2_grpc",
    "wisdev_pb2",
    "wisdev_pb2_grpc",
    "PROTO_IMPORT_ERRORS",
    "get_proto_runtime_diagnostics",
    "require_generated_module",
    "require_proto_runtime_compatibility",
]
