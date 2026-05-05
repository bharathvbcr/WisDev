import json
from pathlib import Path

import pytest
from fastapi.testclient import TestClient
from main import app
from artifacts.schema import ARTIFACT_SCHEMA_VERSION

client = TestClient(app)
SNAPSHOT_PATH = Path(__file__).resolve().parents[1] / "fixtures" / "wisdev_openapi_snapshot.json"


def _wisdev_openapi_subset(spec: dict) -> dict:
    paths = spec["paths"]
    schemas = spec.get("components", {}).get("schemas", {})
    component_names = [
        "BuildClaimEvidenceTableResponse",
        "CanonicalCitationResponse",
        "ClaimEvidenceTableResponse",
        "ProposeHypothesesResponse",
        "ReasoningBranchResponse",
        "ReasoningVerificationResponse",
        "ResolveCitationsResponse",
        "VerifyCitationsResponse",
        "VerifyReasoningPathsResponse",
    ]
    return {
        "paths": {
            path: paths[path]
            for path in sorted(paths)
            if path.startswith("/wisdev/action/research.")
        },
        "components": {
            name: schemas[name]
            for name in component_names
            if name in schemas
        },
    }

def test_schema_version_endpoint():
    response = client.get("/wisdev/schema-version")
    assert response.status_code == 200
    assert response.json() == {"schemaVersion": ARTIFACT_SCHEMA_VERSION}

def test_dispatch_action_resolve_citations_live():
    payload = {
        "action": "research.resolveCanonicalCitations",
        "sessionId": "sess_123",
        "payload": {
            "papers": [{"title": "Paper 1"}],
        },
    }
    response = client.post("/wisdev/action", json=payload)
    assert response.status_code == 200
    data = response.json()
    assert "canonicalSources" in data
    assert "citations" in data
    assert data["resolvedCount"] == 1


def test_dispatch_action_verify_citations_live():
    payload = {
        "action": "research.verifyCitations",
        "sessionId": "sess_123",
        "payload": {
            "papers": [{"title": "Paper 1"}],
        },
    }
    response = client.post("/wisdev/action", json=payload)
    assert response.status_code == 200
    data = response.json()
    assert "verifiedRecords" in data
    assert "citations" in data
    assert data["validCount"] == 1


def test_dispatch_action_generate_hypotheses_live():
    payload = {
        "action": "research.generateHypotheses",
        "sessionId": "sess_123",
        "payload": {
            "branches": [{"claim": "c1"}],
        },
    }
    response = client.post("/wisdev/action", json=payload)
    assert response.status_code == 200
    data = response.json()
    assert "branches" in data
    assert isinstance(data["branches"], list)


def test_dispatch_action_verify_reasoning_paths_live():
    payload = {
        "action": "research.verifyReasoningPaths",
        "sessionId": "sess_123",
        "payload": {
            "branches": [{"claim": "c1"}],
        },
    }
    response = client.post("/wisdev/action", json=payload)
    assert response.status_code == 200
    data = response.json()
    assert "reasoningVerification" in data
    assert data["reasoningVerification"]["totalBranches"] == 1


def test_dispatch_action_build_claim_evidence_table_live():
    payload = {
        "action": "research.buildClaimEvidenceTable",
        "sessionId": "sess_123",
        "query": "test query",
        "payload": {
            "papers": [{"title": "Paper 1"}],
        },
    }
    response = client.post("/wisdev/action", json=payload)
    assert response.status_code == 200
    data = response.json()
    assert "claimEvidenceTable" in data
    assert data["claimEvidenceTable"]["rowCount"] == 1


def test_dispatch_action_stub_not_implemented_for_unsupported_action():
    payload = {
        "action": "research.testAction",
        "sessionId": "sess_123",
        "payload": {"key": "value"}
    }
    response = client.post("/wisdev/action", json=payload)
    assert response.status_code == 501
    data = response.json()["detail"]
    assert data["code"] == "NOT_IMPLEMENTED"
    assert data["action"] == "research.testAction"
    assert data["schemaVersion"] == ARTIFACT_SCHEMA_VERSION

def test_resolve_citations_stub():
    payload = {
        "papers": [{"title": "Paper 1"}],
        "query": "test query",
        "sessionId": "sess_123"
    }
    response = client.post("/wisdev/action/research.resolveCanonicalCitations", json=payload)
    assert response.status_code == 200
    data = response.json()
    assert "canonicalSources" in data
    assert "citations" in data
    assert data["resolvedCount"] == 1

def test_verify_citations_stub():
    payload = {
        "papers": [{"title": "Paper 1"}],
        "sessionId": "sess_123"
    }
    response = client.post("/wisdev/action/research.verifyCitations", json=payload)
    assert response.status_code == 200
    data = response.json()
    assert "verifiedRecords" in data
    assert "citations" in data
    assert data["validCount"] == 1

def test_verify_citations_rejects_schema_invalid_artifact():
    payload = {
        "papers": [{"doi": "10.1000/no-title"}],
        "sessionId": "sess_123"
    }
    response = client.post("/wisdev/action/research.verifyCitations", json=payload)
    assert response.status_code == 422
    detail = response.json()["detail"]
    assert detail["code"] == "ARTIFACT_SCHEMA_VIOLATION"
    assert detail["action"] == "research.verifyCitations"
    assert detail["schemaVersion"] == ARTIFACT_SCHEMA_VERSION

def test_propose_hypotheses_live():
    payload = {
        "query": "test query",
        "sessionId": "sess_123"
    }
    response = client.post("/wisdev/action/research.proposeHypotheses", json=payload)
    assert response.status_code == 200
    data = response.json()
    assert "branches" in data
    assert isinstance(data["branches"], list)


def test_generate_hypotheses_live_alias():
    payload = {
        "query": "test query",
        "sessionId": "sess_123"
    }
    response = client.post("/wisdev/action/research.generateHypotheses", json=payload)
    assert response.status_code == 200
    data = response.json()
    assert "branches" in data
    assert isinstance(data["branches"], list)

def test_verify_reasoning_paths_live():
    payload = {
        "branches": [{"claim": "C1"}],
        "sessionId": "sess_123"
    }
    response = client.post("/wisdev/action/research.verifyReasoningPaths", json=payload)
    assert response.status_code == 200
    data = response.json()
    assert "branches" in data
    assert "reasoningVerification" in data
    assert data["reasoningVerification"]["totalBranches"] == 1

def test_build_claim_evidence_table_live():
    payload = {
        "query": "test query",
        "papers": [{"title": "P1"}],
        "sessionId": "sess_123"
    }
    response = client.post("/wisdev/action/research.buildClaimEvidenceTable", json=payload)
    assert response.status_code == 200
    data = response.json()
    assert "claimEvidenceTable" in data
    assert data["claimEvidenceTable"]["rowCount"] == 1

def test_invalid_payload_validation_error():
    # Missing required 'query' for buildClaimEvidenceTable
    payload = {
        "papers": []
    }
    response = client.post("/wisdev/action/research.buildClaimEvidenceTable", json=payload)
    assert response.status_code == 422 # Pydantic validation error


def test_openapi_exposes_explicit_wisdev_response_models():
    openapi = client.get("/openapi.json")
    assert openapi.status_code == 200
    spec = openapi.json()
    paths = spec["paths"]

    resolve_schema = paths["/wisdev/action/research.resolveCanonicalCitations"]["post"]["responses"]["200"]["content"]["application/json"]["schema"]
    verify_schema = paths["/wisdev/action/research.verifyCitations"]["post"]["responses"]["200"]["content"]["application/json"]["schema"]
    hypotheses_schema = paths["/wisdev/action/research.proposeHypotheses"]["post"]["responses"]["200"]["content"]["application/json"]["schema"]
    reasoning_schema = paths["/wisdev/action/research.verifyReasoningPaths"]["post"]["responses"]["200"]["content"]["application/json"]["schema"]
    claim_table_schema = paths["/wisdev/action/research.buildClaimEvidenceTable"]["post"]["responses"]["200"]["content"]["application/json"]["schema"]

    assert resolve_schema["$ref"].endswith("/ResolveCitationsResponse")
    assert verify_schema["$ref"].endswith("/VerifyCitationsResponse")
    assert hypotheses_schema["$ref"].endswith("/ProposeHypothesesResponse")
    assert reasoning_schema["$ref"].endswith("/VerifyReasoningPathsResponse")
    assert claim_table_schema["$ref"].endswith("/BuildClaimEvidenceTableResponse")


def test_openapi_snapshot_matches_wisdev_contract():
    openapi = client.get("/openapi.json")
    assert openapi.status_code == 200
    expected = json.loads(SNAPSHOT_PATH.read_text(encoding="utf-8"))
    assert _wisdev_openapi_subset(openapi.json()) == expected
