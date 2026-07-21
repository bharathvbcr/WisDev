"""Tests for services/deepagents_service.py."""

import pytest

from services.deepagents_service import (
    _build_wisdev_action_tool,
    _execute_wisdev_action_sync,
    _resolve_allowlist,
    _resolve_max_execution_ms,
    get_deepagents_capabilities,
)


def test_execute_wisdev_action_sync_verify_citations_shape():
    result = _execute_wisdev_action_sync(
        "research.verifyCitations",
        {"papers": [{"id": "p1", "title": "Paper 1"}]},
    )
    assert "verifiedRecords" in result
    assert "validCount" in result
    assert result["validCount"] == 1


def test_execute_wisdev_action_sync_retrieve_papers_shape():
    result = _execute_wisdev_action_sync(
        "research.retrievePapers",
        {
            "query": "causal inference",
            "papers": [
                {"title": "Causal Inference with Graphs", "abstract": "Inference methods"},
                {"title": "Unrelated topic", "abstract": "random"},
            ],
            "topK": 1,
        },
    )
    assert result["backend"] == "wisdev_search_local"
    assert result["count"] == 1
    assert len(result["results"]) == 1


def test_execute_wisdev_action_sync_synthesis_shape():
    result = _execute_wisdev_action_sync(
        "research.synthesizeAnswer",
        {
            "query": "What is new?",
            "papers": [{"title": "Paper 1", "summary": "Important finding"}],
        },
    )
    assert "answer" in result
    assert "citations" in result
    assert result["backend"] == "wisdev_synthesis_local"
    assert result["authoritative"] is False
    assert result["finalAnswerAuthority"] == "go_orchestrator"
    assert result["promotionAllowed"] is False


def test_execute_wisdev_action_sync_reasoning_paths_shape():
    result = _execute_wisdev_action_sync(
        "research.verifyReasoningPaths",
        {
            "branches": [
                {
                    "claim": "C1",
                    "falsifiabilityCondition": "F1",
                    "supportScore": 0.8,
                    "isTerminated": False,
                }
            ]
        },
    )
    assert "branches" in result
    assert "reasoningVerification" in result
    assert result["reasoningVerification"]["verifiedBranches"] == 0
    assert result["reasoningVerification"]["rejectedBranches"] == 1
    assert result["reasoningVerification"]["readyForSynthesis"] is False
    assert result["reasoningVerification"]["authoritative"] is False
    assert result["reasoningVerification"]["verificationAuthority"] == "go_orchestrator"


def test_execute_wisdev_action_sync_unsupported_action():
    result = _execute_wisdev_action_sync("research.unknown", {})
    assert "error" in result
    assert result["error"]["code"] == "UNSUPPORTED_ACTION"


def test_resolve_allowlist_defaults_to_all_supported():
    allow = _resolve_allowlist(None)
    assert "research.verifyCitations" in allow
    assert "research.synthesizeAnswer" in allow


def test_resolve_allowlist_intersects_supported_only():
    allow = _resolve_allowlist(["research.verifyCitations", "research.fake"])
    assert allow == {"research.verifyCitations"}


def test_resolve_max_execution_ms_clamps(monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("DEEPAGENTS_MAX_EXECUTION_MS", "999999")
    assert _resolve_max_execution_ms(None) == 300000
    assert _resolve_max_execution_ms(100) == 1000


def test_wisdev_action_tool_requires_confirmation_for_sensitive_action():
    tool = _build_wisdev_action_tool(
        session_id="sess-1",
        user_id="user-1",
        query="q",
        papers=[],
        allowlisted_tools=["research.synthesizeAnswer"],
        require_human_confirmation=True,
        confirmed_actions=[],
    )
    result = tool("research.synthesizeAnswer", "{}")
    assert "error" in result
    assert result["error"]["code"] == "HUMAN_CONFIRMATION_REQUIRED"


def test_wisdev_action_tool_blocks_non_allowlisted_action():
    tool = _build_wisdev_action_tool(
        session_id="sess-1",
        user_id="user-1",
        query="q",
        papers=[],
        allowlisted_tools=["research.verifyCitations"],
        require_human_confirmation=False,
        confirmed_actions=[],
    )
    result = tool("research.retrievePapers", "{}")
    assert "error" in result
    assert result["error"]["code"] == "ACTION_NOT_ALLOWLISTED"


def test_get_deepagents_capabilities_has_contract_data(monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("DEEPAGENTS_MODEL", "openai:gpt-4o")
    caps = get_deepagents_capabilities()
    assert caps["backend"] == "deepagents"
    assert caps["artifactSchema"] == "artifacts-v1"
    assert caps["configuredModel"] == "openai:gpt-4o"
    assert "research.retrievePapers" in caps["wisdevActions"]
    assert "research.verifyCitations" in caps["wisdevActions"]
    assert "research.synthesizeAnswer" in caps["sensitiveWisdevActions"]
    assert caps["authoritative"] is False
    assert caps["finalAnswerAuthority"] == "go_orchestrator"
