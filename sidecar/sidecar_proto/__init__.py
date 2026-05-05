"""Compatibility exports for sidecar protobuf modules.

Canonical generated modules now live under ``proto`` and are re-exported here
for any lingering package-level imports.
"""

from __future__ import annotations

import importlib
from types import ModuleType

try:
    llm_pb2: ModuleType | None = importlib.import_module('proto.llm_pb2')
    llm_pb2_grpc: ModuleType | None = importlib.import_module('proto.llm_pb2_grpc')
except Exception:  # pragma: no cover - generated modules may be absent locally
    llm_pb2 = None
    llm_pb2_grpc = None

try:
    wisdev_pb2: ModuleType | None = importlib.import_module('proto.wisdev_pb2')
    wisdev_pb2_grpc: ModuleType | None = importlib.import_module('proto.wisdev_pb2_grpc')
except Exception:  # pragma: no cover - generated modules may be absent locally
    wisdev_pb2 = None
    wisdev_pb2_grpc = None

from proto import (  # noqa: E402
    PROTO_IMPORT_ERRORS,
    get_proto_runtime_diagnostics,
    require_proto_runtime_compatibility,
)

__all__ = [
    'llm_pb2',
    'llm_pb2_grpc',
    'wisdev_pb2',
    'wisdev_pb2_grpc',
    'PROTO_IMPORT_ERRORS',
    'get_proto_runtime_diagnostics',
    'require_proto_runtime_compatibility',
]
