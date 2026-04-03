"""Generated protobuf modules for the sidecar gRPC service.

Canonical import names live here. Legacy ``wisdev_v2_*`` modules remain the
wire-compatibility source when generated artifacts are present.
"""

try:
    from . import wisdev_v2_pb2 as wisdev_pb2
    from . import wisdev_v2_pb2_grpc as wisdev_pb2_grpc
except Exception:  # pragma: no cover - generated modules may be absent locally
    wisdev_pb2 = None
    wisdev_pb2_grpc = None

__all__ = ['wisdev_pb2', 'wisdev_pb2_grpc']
