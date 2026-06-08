from __future__ import annotations

from stack_contract import (
    ENDPOINTS_DEFAULT_OVERLAY,
    ENDPOINTS_MANIFEST,
    ENDPOINTS_MANIFEST_VERSION,
)


def test_manifest_version_matches_constant():
    assert ENDPOINTS_MANIFEST["version"] == ENDPOINTS_MANIFEST_VERSION


def test_default_overlay_exists_in_manifest():
    assert ENDPOINTS_DEFAULT_OVERLAY in ENDPOINTS_MANIFEST["overlays"]


def test_python_sidecar_service_contract_present():
    service = ENDPOINTS_MANIFEST["services"]["python_sidecar"]
    assert service["transport"] == "http-json+grpc-protobuf"
    assert service["listenPorts"]["http"] == 8090
    assert service["listenPorts"]["grpc"] == 50052
    assert "/llm/generate" in ENDPOINTS_MANIFEST["httpRoutes"]["python_sidecar"]
    assert ENDPOINTS_MANIFEST["overlays"]["cloudrun"]["env"]["PYTHON_SIDECAR_LLM_TRANSPORT"] == "http-json"


def test_proto_contracts_include_python_generated_files():
    proto_contracts = ENDPOINTS_MANIFEST["protoContracts"]
    assert "sidecar/proto/llm_pb2.py" in proto_contracts["llm"]["generatedPython"]
    assert "wisdev" not in proto_contracts


def test_open_source_manifest_has_no_rust_gateway():
    assert "rust_gateway" not in ENDPOINTS_MANIFEST["services"]
    assert "rust_gateway" not in ENDPOINTS_MANIFEST["canonicalRequestFlow"]
