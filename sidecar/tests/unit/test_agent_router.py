"""Tests for routers/agent_router.py."""

import pytest
from unittest.mock import AsyncMock, patch
from fastapi.testclient import TestClient
from fastapi import FastAPI

from routers.agent_router import router
from services.deepagents_service import DeepAgentsTimeoutError, DeepAgentsUnavailableError


@pytest.fixture(scope="module")
def client():
    app = FastAPI()
    app.include_router(router)
    return TestClient(app)


def test_get_agent_card_status_200(client):
    response = client.get("/wisdev/agent/card")
    assert response.status_code == 200


def test_get_agent_card_agent_id(client):
    data = client.get("/wisdev/agent/card").json()
    assert data["agentId"] == "wisdev-python-worker"


def test_get_agent_card_name(client):
    data = client.get("/wisdev/agent/card").json()
    assert data["name"] == "WisDev Python Sidecar"


def test_get_agent_card_version(client):
    data = client.get("/wisdev/agent/card").json()
    assert data["version"] == "1.1.0"


def test_get_agent_card_protocol(client):
    data = client.get("/wisdev/agent/card").json()
    assert data["protocol"] == "refined"


def test_get_agent_card_capabilities(client):
    data = client.get("/wisdev/agent/card").json()
    assert data["capabilities"] == 3


def test_get_agent_card_has_all_keys(client):
    data = client.get("/wisdev/agent/card").json()
    expected_keys = {"agentId", "name", "version", "protocol", "capabilities"}
    assert expected_keys.issubset(data.keys())


def test_deepagents_execute_success(client):
    with patch(
        "routers.agent_router.run_deep_agent",
        new=AsyncMock(
            return_value={
                "output": "ok",
                "backend": "deepagents",
                "model": "openai:gpt-4o-mini",
                "toolsEnabled": True,
                "toolCount": 1,
                "allowlistedTools": ["research.verifyCitations"],
                "requireHumanConfirmation": True,
            }
        ),
    ):
        response = client.post(
            "/wisdev/deep-agents/execute",
            json={
                "query": "Summarize this research area.",
                "sessionId": "sess-1",
                "userId": "user-1",
                "papers": [{"title": "Paper A"}],
                "enableWisdevTools": True,
                "maxExecutionMs": 25000,
                "allowlistedTools": ["research.verifyCitations"],
                "requireHumanConfirmation": True,
                "confirmedActions": ["research.verifyCitations"],
            },
        )
    assert response.status_code == 200
    payload = response.json()
    assert payload["output"] == "ok"
    assert payload["backend"] == "deepagents"
    assert payload["model"] == "openai:gpt-4o-mini"
    assert payload["toolsEnabled"] is True
    assert payload["toolCount"] == 1
    assert payload["allowlistedTools"] == ["research.verifyCitations"]
    assert payload["requireHumanConfirmation"] is True
    assert payload["success"] is True


def test_deepagents_execute_unavailable_returns_503(client):
    with patch(
        "routers.agent_router.run_deep_agent",
        new=AsyncMock(side_effect=DeepAgentsUnavailableError("Deep Agents is not installed")),
    ):
        response = client.post(
            "/wisdev/deep-agents/execute",
            json={"query": "Summarize this research area."},
        )
    assert response.status_code == 503


def test_deepagents_execute_validation_error(client):
    response = client.post(
        "/wisdev/deep-agents/execute",
        json={"query": "hi"},
    )
    assert response.status_code == 422


def test_deepagents_execute_timeout_returns_504(client):
    with patch(
        "routers.agent_router.run_deep_agent",
        new=AsyncMock(side_effect=DeepAgentsTimeoutError("timed out")),
    ):
        response = client.post(
            "/wisdev/deep-agents/execute",
            json={"query": "Summarize this research area."},
        )
    assert response.status_code == 504


def test_deepagents_capabilities_endpoint(client):
    with patch(
        "routers.agent_router.get_deepagents_capabilities",
        return_value={
            "backend": "deepagents",
            "artifactSchema": "artifacts-v1",
            "configuredModel": None,
            "wisdevActions": ["research.verifyCitations"],
        },
    ):
        response = client.get("/wisdev/deep-agents/capabilities")
    assert response.status_code == 200
    payload = response.json()
    assert payload["backend"] == "deepagents"
    assert payload["artifactSchema"] == "artifacts-v1"
    assert payload["wisdevActions"] == ["research.verifyCitations"]


def test_deepagents_execute_forwards_context(client):
    with patch(
        "routers.agent_router.run_deep_agent",
        new=AsyncMock(return_value={"output": "ok", "backend": "deepagents", "model": None}),
    ) as run_mock:
        response = client.post(
            "/wisdev/deep-agents/execute",
            json={
                "query": "Run with context",
                "sessionId": "sess-ctx",
                "userId": "user-ctx",
                "papers": [{"title": "P1"}],
                "enableWisdevTools": False,
            },
        )
    assert response.status_code == 200
    assert run_mock.await_count == 1
    kwargs = run_mock.await_args.kwargs
    assert kwargs["session_id"] == "sess-ctx"
    assert kwargs["user_id"] == "user-ctx"
    assert kwargs["papers"] == [{"title": "P1"}]
    assert kwargs["enable_wisdev_tools"] is False


def test_deepagents_execute_unexpected_error_returns_500(client):
    with patch(
        "routers.agent_router.run_deep_agent",
        new=AsyncMock(side_effect=RuntimeError("unexpected failure")),
    ):
        response = client.post(
            "/wisdev/deep-agents/execute",
            json={"query": "Summarize this research area."},
        )
    assert response.status_code == 500
    assert response.json()["detail"].startswith("deepagents_execution_failed")
