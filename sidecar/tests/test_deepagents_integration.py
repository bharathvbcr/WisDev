"""Integration tests for Deep Agents HTTP routes through main FastAPI app."""

from unittest.mock import AsyncMock, patch

from fastapi.testclient import TestClient

from main import app


def test_deepagents_execute_route_with_service_boundary_mock() -> None:
    with patch(
        "routers.agent_router.run_deep_agent",
        new=AsyncMock(
            return_value={
                "output": "integration-ok",
                "backend": "deepagents",
                "model": "openai:gpt-4o-mini",
                "toolsEnabled": True,
                "toolCount": 1,
                "allowlistedTools": ["research.verifyCitations"],
                "requireHumanConfirmation": True,
            }
        ),
    ) as run_mock:
        client = TestClient(app)
        response = client.post(
            "/wisdev/deep-agents/execute",
            json={
                "query": "Run integration boundary test",
                "sessionId": "sess-int",
                "userId": "user-int",
                "papers": [{"title": "Paper A"}],
                "enableWisdevTools": True,
                "allowlistedTools": ["research.verifyCitations"],
                "requireHumanConfirmation": True,
                "confirmedActions": ["research.verifyCitations"],
            },
        )

    assert response.status_code == 200
    payload = response.json()
    assert payload["output"] == "integration-ok"
    assert payload["backend"] == "deepagents"
    assert payload["toolsEnabled"] is True
    assert payload["toolCount"] == 1
    assert payload["allowlistedTools"] == ["research.verifyCitations"]
    assert payload["requireHumanConfirmation"] is True
    assert run_mock.await_count == 1
