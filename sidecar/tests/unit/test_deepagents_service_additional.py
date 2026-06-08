"""Additional tests for services/deepagents_service.py."""

from __future__ import annotations

import asyncio
import builtins
import os
import time
import types

import pytest
from unittest.mock import AsyncMock, patch

from services.deepagents_service import (
    DeepAgentsTimeoutError,
    DeepAgentsUnavailableError,
    _build_wisdev_action_tool,
    _execute_wisdev_action_sync,
    _extract_agent_output,
    _resolve_max_execution_ms,
    _run_sync_with_timeout,
    _run_deep_agent_sync,
    run_deep_agent,
)


def test_build_wisdev_action_tool_invalid_json_payload_uses_defaults():
    tool = _build_wisdev_action_tool(
        session_id="s1",
        user_id="u1",
        query="default query",
        papers=[{"title": "A"}],
        allowlisted_tools=["research.retrievePapers"],
        require_human_confirmation=False,
        confirmed_actions=None,
    )

    result = tool("research.searchPapers", "{invalid-json")
    assert "error" not in result
    assert result["backend"] == "wisdev_search_local"


def test_extract_agent_output_prefers_legacy_message_text_list():
    output = _extract_agent_output(
        {
            "output": {
                "response": "n/a"
            },
            "messages": [
                {"role": "assistant", "content": "ignore"},
                {
                    "role": "assistant",
                    "content": [
                        {"type": "text", "text": "part1"},
                        {"type": "text", "text": "part2"},
                    ],
                },
            ],
        }
    )
    assert output == "part1\npart2"


@pytest.mark.asyncio
async def test_run_deep_agent_times_out_when_worker_times_out():
    with patch(
        "services.deepagents_service._run_sync_with_timeout",
        AsyncMock(side_effect=asyncio.TimeoutError("timed out")),
    ):
        with pytest.raises(DeepAgentsTimeoutError):
            await run_deep_agent(query="x", max_execution_ms=200)


def test_run_deep_agent_import_error_when_library_missing():
    fake_module = types.ModuleType("deepagents")

    with patch.dict("sys.modules", {"deepagents": fake_module}):
        with pytest.raises(DeepAgentsUnavailableError):
            _run_deep_agent_sync(
                query="x",
                system_prompt=None,
                model=None,
                session_id=None,
                user_id=None,
                papers=None,
                enable_wisdev_tools=True,
                allowlisted_tools=None,
                require_human_confirmation=False,
                confirmed_actions=None,
            )


def test_run_deep_agent_sync_runs_with_model_tooling():
    model_mod = types.ModuleType("deepagents")
    lang_mod = types.ModuleType("langchain")
    chat_mod = types.ModuleType("chat_models")
    lang_mod.chat_models = chat_mod

    created = {"called": False}

    def create_deep_agent(**kwargs):
        created["called"] = True

        class Agent:
            def invoke(self, payload):
                return "done"

        return Agent()

    chat_mod.init_chat_model = lambda *args, **kwargs: "model"
    model_mod.create_deep_agent = create_deep_agent
    with patch.dict(
        "sys.modules",
        {"deepagents": model_mod, "langchain": lang_mod, "langchain.chat_models": chat_mod},
    ):
        result = _run_deep_agent_sync(
            query="hello",
            system_prompt=None,
            model="example:model",
            session_id=None,
            user_id=None,
            papers=None,
            enable_wisdev_tools=True,
            allowlisted_tools=None,
            require_human_confirmation=False,
            confirmed_actions=None,
        )

    assert result["toolCount"] == 1
    assert created["called"] is True


@pytest.mark.asyncio
async def test_run_sync_with_timeout_cancels_background_task_on_timeout():
    def slow():
        time.sleep(0.05)
        return "late"

    with pytest.raises(asyncio.TimeoutError):
        await _run_sync_with_timeout(1, slow)


@pytest.mark.asyncio
async def test_run_sync_with_timeout_awaits_cancelled_task_on_wait_for_error():
    started = asyncio.Event()

    async def fake_to_thread(func, *args):
        started.set()
        await asyncio.sleep(1)
        return func(*args)

    async def fake_wait_for(task, timeout):
        await started.wait()
        raise RuntimeError("boom")

    with patch("services.deepagents_service.asyncio.to_thread", side_effect=fake_to_thread):
        with patch("services.deepagents_service.asyncio.wait_for", side_effect=fake_wait_for):
            with pytest.raises(RuntimeError, match="boom"):
                await _run_sync_with_timeout(10, lambda: "done")


def test_run_deep_agent_sync_requires_langchain_when_model_is_set():
    model_mod = types.ModuleType("deepagents")
    model_mod.create_deep_agent = lambda **kwargs: None
    original_import = builtins.__import__

    def _importer(name, globals=None, locals=None, fromlist=(), level=0):
        if name == "langchain.chat_models":
            raise ImportError("langchain missing")
        return original_import(name, globals, locals, fromlist, level)

    with patch.dict("sys.modules", {"deepagents": model_mod}, clear=False):
        with patch("builtins.__import__", side_effect=_importer):
            with pytest.raises(DeepAgentsUnavailableError, match="langchain is required"):
                _run_deep_agent_sync(
                    query="x",
                    system_prompt=None,
                    model="example:model",
                    session_id=None,
                    user_id=None,
                    papers=None,
                    enable_wisdev_tools=False,
                    allowlisted_tools=None,
                    require_human_confirmation=False,
                    confirmed_actions=None,
                )


def test_build_wisdev_action_tool_rejects_unsupported_action():
    tool = _build_wisdev_action_tool(
        session_id="s1",
        user_id="u1",
        query="default query",
        papers=[],
        allowlisted_tools=None,
        require_human_confirmation=False,
        confirmed_actions=None,
    )

    result = tool("research.unknownAction", "{}")
    assert result["error"]["code"] == "UNSUPPORTED_ACTION"


def test_build_wisdev_action_tool_non_dict_payload_uses_defaults():
    tool = _build_wisdev_action_tool(
        session_id="s1",
        user_id="u1",
        query="default query",
        papers=[{"title": "Alpha"}],
        allowlisted_tools=["research.retrievePapers"],
        require_human_confirmation=False,
        confirmed_actions=None,
    )

    result = tool("research.retrievePapers", '["not-a-dict"]')
    assert result["count"] == 1
    assert result["query"] == "default query"


def test_execute_wisdev_action_sync_retrieve_papers_skips_blank_haystacks():
    result = _execute_wisdev_action_sync(
        "research.retrievePapers",
        {
            "query": "alpha",
            "papers": [
                {"title": "", "abstract": ""},
                {"title": "Alpha findings", "abstract": "useful abstract"},
            ],
        },
    )

    assert result["count"] == 1
    assert result["results"][0]["title"] == "Alpha findings"


def test_execute_wisdev_action_sync_emits_resolve_hypothesis_and_claim_outputs():
    resolved = _execute_wisdev_action_sync(
        "research.resolveCanonicalCitations",
        {"papers": [{"title": "Paper A"}]},
    )
    hypothesized = _execute_wisdev_action_sync(
        "research.proposeHypotheses",
        {"query": "test claim"},
    )
    claim_table = _execute_wisdev_action_sync(
        "research.buildClaimEvidenceTable",
        {"query": "test claim", "papers": [{"title": "Paper A"}]},
    )

    assert resolved["resolvedCount"] == 1
    assert hypothesized["branches"][0]["claim"] == "test claim"
    assert "test claim" in claim_table["claimEvidenceTable"]["table"]


def test_execute_wisdev_action_sync_synthesize_answer_handles_missing_summaries():
    result = _execute_wisdev_action_sync(
        "research.synthesizeAnswer",
        {"query": "topic", "papers": [{"title": "Paper Only"}]},
    )

    assert "Paper Only" in result["answer"]
    assert result["confidence"] == 0.6


def test_resolve_max_execution_ms_handles_invalid_explicit_and_env(monkeypatch):
    monkeypatch.setenv("DEEPAGENTS_MAX_EXECUTION_MS", "not-a-number")

    assert _resolve_max_execution_ms("bad") == 45000
    assert _resolve_max_execution_ms(None) == 45000


def test_extract_agent_output_prefers_output_string():
    assert _extract_agent_output({"output": "direct output"}) == "direct output"


def test_extract_agent_output_skips_non_dict_and_non_assistant_messages():
    output = _extract_agent_output(
        {
            "messages": [
                {"role": "assistant", "content": "assistant text"},
                {"role": "user", "content": "user text"},
                "ignore-me",
            ]
        }
    )
    assert output == "assistant text"


def test_extract_agent_output_falls_back_to_str_for_unknown_shapes():
    assert _extract_agent_output(42) == "42"
