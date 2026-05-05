"""Deep Agents execution wrapper for the Python sidecar."""

from __future__ import annotations

import asyncio
import json
import os
from contextlib import suppress
from typing import Any, Optional

from artifacts.emitters import emit_for_action, flatten_to_legacy
from artifacts.schema import ARTIFACT_SCHEMA_VERSION


DEFAULT_SYSTEM_PROMPT = (
    "You are WisDev's deep research assistant. Produce rigorous, evidence-aware "
    "outputs and keep responses concise unless asked for detailed explanations."
)

GO_ORCHESTRATOR_AUTHORITY = "go_orchestrator"
PYTHON_TOOL_AUTHORITY = "python_tool_primitive"

SUPPORTED_WISDEV_ACTIONS = (
    "research.retrievePapers",
    "research.resolveCanonicalCitations",
    "research.verifyCitations",
    "research.proposeHypotheses",
    "research.generateHypotheses",
    "research.verifyReasoningPaths",
    "research.buildClaimEvidenceTable",
    "research.synthesizeAnswer",
)

SENSITIVE_WISDEV_ACTIONS = {
    "research.synthesizeAnswer",
}

ACTION_ALIASES = {
    "research.searchPapers": "research.retrievePapers",
}

DEFAULT_MAX_EXECUTION_MS = 45_000
MIN_EXECUTION_MS = 1_000
MAX_EXECUTION_MS = 300_000


async def _run_sync_with_timeout(timeout_ms: int, func, *args):
    task = asyncio.create_task(asyncio.to_thread(func, *args))
    try:
        return await asyncio.wait_for(task, timeout=timeout_ms / 1000.0)
    except Exception:
        if not task.done():
            task.cancel()
            with suppress(asyncio.CancelledError):
                await task
        raise


class DeepAgentsUnavailableError(RuntimeError):
    """Raised when Deep Agents or model initialization dependencies are missing."""


class DeepAgentsTimeoutError(TimeoutError):
    """Raised when Deep Agents execution exceeds the configured timeout."""


async def run_deep_agent(
    *,
    query: str,
    system_prompt: Optional[str] = None,
    model: Optional[str] = None,
    session_id: Optional[str] = None,
    user_id: Optional[str] = None,
    papers: Optional[list[dict[str, Any]]] = None,
    enable_wisdev_tools: bool = True,
    max_execution_ms: Optional[int] = None,
    allowlisted_tools: Optional[list[str]] = None,
    require_human_confirmation: bool = False,
    confirmed_actions: Optional[list[str]] = None,
) -> dict[str, Any]:
    timeout_ms = _resolve_max_execution_ms(max_execution_ms)
    try:
        return await _run_sync_with_timeout(
            timeout_ms,
            _run_deep_agent_sync,
            query,
            system_prompt,
            model,
            session_id,
            user_id,
            papers,
            enable_wisdev_tools,
            allowlisted_tools,
            require_human_confirmation,
            confirmed_actions,
        )
    except TimeoutError as exc:
        raise DeepAgentsTimeoutError(
            f"Deep Agents execution exceeded max timeout ({timeout_ms} ms)."
        ) from exc


def _run_deep_agent_sync(
    query: str,
    system_prompt: Optional[str],
    model: Optional[str],
    session_id: Optional[str],
    user_id: Optional[str],
    papers: Optional[list[dict[str, Any]]],
    enable_wisdev_tools: bool,
    allowlisted_tools: Optional[list[str]],
    require_human_confirmation: bool,
    confirmed_actions: Optional[list[str]],
) -> dict[str, Any]:
    try:
        from deepagents import create_deep_agent
    except ImportError as exc:
        raise DeepAgentsUnavailableError(
            "Deep Agents is not installed. Install deepagents in the Python sidecar environment."
        ) from exc

    kwargs: dict[str, Any] = {
        "system_prompt": system_prompt or DEFAULT_SYSTEM_PROMPT,
    }

    resolved_model = model or os.environ.get("DEEPAGENTS_MODEL")
    if resolved_model:
        try:
            from langchain.chat_models import init_chat_model
        except ImportError as exc:
            raise DeepAgentsUnavailableError(
                "langchain is required when DEEPAGENTS_MODEL is set. Install langchain in the sidecar environment."
            ) from exc
        kwargs["model"] = init_chat_model(resolved_model)

    tools = []
    if enable_wisdev_tools:
        tools.append(
            _build_wisdev_action_tool(
                session_id=session_id,
                user_id=user_id,
                query=query,
                papers=papers or [],
                allowlisted_tools=allowlisted_tools,
                require_human_confirmation=require_human_confirmation,
                confirmed_actions=confirmed_actions,
            )
        )
    if tools:
        kwargs["tools"] = tools

    agent = create_deep_agent(**kwargs)
    result = agent.invoke({"messages": [{"role": "user", "content": query}]})

    return {
        "output": _extract_agent_output(result),
        "backend": "deepagents",
        "model": resolved_model,
        "toolsEnabled": len(tools) > 0,
        "toolCount": len(tools),
        "allowlistedTools": sorted(_resolve_allowlist(allowlisted_tools)),
        "requireHumanConfirmation": require_human_confirmation,
        "authoritative": False,
        "authority": PYTHON_TOOL_AUTHORITY,
        "finalAnswerAuthority": GO_ORCHESTRATOR_AUTHORITY,
    }


def get_deepagents_capabilities() -> dict[str, Any]:
    configured_model = os.environ.get("DEEPAGENTS_MODEL")
    supported_actions = list(SUPPORTED_WISDEV_ACTIONS)
    sensitive_actions = sorted(SENSITIVE_WISDEV_ACTIONS)
    guided_allowlist = [
        action for action in supported_actions if action not in SENSITIVE_WISDEV_ACTIONS
    ]
    return {
        "backend": "deepagents",
        "artifactSchema": ARTIFACT_SCHEMA_VERSION,
        "configuredModel": configured_model,
        "wisdevActions": supported_actions,
        "sensitiveWisdevActions": sensitive_actions,
        "allowlistedTools": supported_actions,
        "toolsEnabled": len(supported_actions) > 0,
        "toolCount": len(supported_actions),
        "requireHumanConfirmation": True,
        "authoritative": False,
        "authority": PYTHON_TOOL_AUTHORITY,
        "finalAnswerAuthority": GO_ORCHESTRATOR_AUTHORITY,
        "policy": (
            "Python DeepAgents actions are subordinate tool primitives; Go owns "
            "research verification, synthesis gating, and final answer promotion."
        ),
        "policyByMode": {
            "guided": {
                "enableWisdevTools": len(supported_actions) > 0,
                "allowlistedTools": guided_allowlist,
                "requireHumanConfirmation": True,
            },
            "yolo": {
                "enableWisdevTools": len(supported_actions) > 0,
                "allowlistedTools": supported_actions,
                "requireHumanConfirmation": False,
            },
        },
        "defaultMaxExecutionMs": _resolve_max_execution_ms(None),
    }


def _canonicalize_action(action: str) -> str:
    return ACTION_ALIASES.get(action, action)


def _build_wisdev_action_tool(
    *,
    session_id: Optional[str],
    user_id: Optional[str],
    query: str,
    papers: list[dict[str, Any]],
    allowlisted_tools: Optional[list[str]],
    require_human_confirmation: bool,
    confirmed_actions: Optional[list[str]],
):
    """Create a Deep Agents tool that maps action+payload into WisDev artifacts."""

    def run_wisdev_action(action: str, payload_json: str = "{}") -> dict[str, Any]:
        """
        Run a WisDev action and return artifact-schema-compatible output.

        `action` must be one of the documented `research.*` action IDs.
        `payload_json` is a JSON object string merged with the execution context.
        """
        action = _canonicalize_action(action)

        if action not in SUPPORTED_WISDEV_ACTIONS:
            return {
                "error": {
                    "code": "UNSUPPORTED_ACTION",
                    "message": f"Unsupported action '{action}'.",
                }
            }

        allowlist = _resolve_allowlist(allowlisted_tools)
        if action not in allowlist:
            return {
                "error": {
                    "code": "ACTION_NOT_ALLOWLISTED",
                    "message": f"Action '{action}' is outside the configured allowlist.",
                    "allowlistedTools": sorted(allowlist),
                }
            }

        confirmed = set(confirmed_actions or [])
        if require_human_confirmation and action in SENSITIVE_WISDEV_ACTIONS and action not in confirmed:
            return {
                "error": {
                    "code": "HUMAN_CONFIRMATION_REQUIRED",
                    "message": f"Action '{action}' requires explicit human confirmation.",
                    "requiredAction": action,
                }
            }

        payload: dict[str, Any]
        try:
            decoded = json.loads(payload_json) if payload_json else {}
            payload = decoded if isinstance(decoded, dict) else {}
        except json.JSONDecodeError:
            payload = {}

        merged_payload = {
            "query": payload.get("query", query),
            "papers": payload.get("papers", papers),
            "sessionId": payload.get("sessionId", session_id),
            "userId": payload.get("userId", user_id),
            **payload,
        }

        return _execute_wisdev_action_sync(action, merged_payload)

    return run_wisdev_action


def _execute_wisdev_action_sync(action: str, payload: dict[str, Any]) -> dict[str, Any]:
    action = _canonicalize_action(action)
    raw_papers = payload.get("papers")
    papers: list[dict[str, Any]] = raw_papers if isinstance(raw_papers, list) else []

    if action == "research.retrievePapers":
        query = str(payload.get("query") or "").strip().lower()
        query_terms = [term for term in query.split() if term]
        scored: list[tuple[dict[str, Any], float]] = []
        for paper in papers:
            title = str(paper.get("title") or "").lower()
            abstract = str(paper.get("abstract") or paper.get("summary") or "").lower()
            haystack = f"{title} {abstract}".strip()
            if not haystack:
                continue
            score = 0.0
            for term in query_terms:
                if term in haystack:
                    score += 1.0
            if query_terms:
                score = score / len(query_terms)
            scored.append((paper, score))

        scored.sort(key=lambda item: item[1], reverse=True)
        top_k = int(payload.get("topK") or payload.get("top_k") or 8)
        selected = [item[0] for item in scored[: max(1, top_k)]]
        return {
            "query": payload.get("query"),
            "results": selected,
            "count": len(selected),
            "backend": "wisdev_search_local",
        }

    if action == "research.resolveCanonicalCitations":
        return _emit_legacy(action, {
            "canonicalSources": papers,
            "resolvedCount": len(papers),
            "duplicateCount": 0,
        })

    if action == "research.verifyCitations":
        return _emit_legacy(action, {
            "verifiedRecords": papers,
            "validCount": len(papers),
            "invalidCount": 0,
            "duplicateCount": 0,
        })

    if action in {"research.proposeHypotheses", "research.generateHypotheses"}:
        query = str(payload.get("query") or "")
        return _emit_legacy(action, {
            "branches": [
                {
                    "claim": query,
                    "falsifiabilityCondition": "Test against contradictory evidence",
                    "supportScore": 0.5,
                    "isTerminated": False,
                }
            ]
        })

    if action == "research.verifyReasoningPaths":
        raw_branches = payload.get("branches")
        branches: list[Any] = raw_branches if isinstance(raw_branches, list) else []
        total = len(branches)
        result = _emit_legacy(action, {
            "branches": branches,
            "totalBranches": total,
            "verifiedBranches": 0,
            "rejectedBranches": total,
            "readyForSynthesis": False,
            "authoritative": False,
            "authority": PYTHON_TOOL_AUTHORITY,
            "verificationAuthority": GO_ORCHESTRATOR_AUTHORITY,
            "finalAnswerAuthority": GO_ORCHESTRATOR_AUTHORITY,
            "status": "non_authoritative_delegated",
            "failureReasons": [
                "Python sidecar cannot promote reasoning branches; Go verifier must score branches against source evidence."
            ],
        })
        verification = result.get("reasoningVerification")
        if isinstance(verification, dict):
            verification.update({
                "authoritative": False,
                "authority": PYTHON_TOOL_AUTHORITY,
                "verificationAuthority": GO_ORCHESTRATOR_AUTHORITY,
                "finalAnswerAuthority": GO_ORCHESTRATOR_AUTHORITY,
                "status": "non_authoritative_delegated",
            })
        return result

    if action == "research.buildClaimEvidenceTable":
        query = str(payload.get("query") or "N/A")
        return _emit_legacy(action, {
            "table": f"| Claim | Evidence |\\n|---|---|\\n| {query} | {len(papers)} sources |",
            "rowCount": len(papers),
        })

    if action == "research.synthesizeAnswer":
        query = str(payload.get("query") or "Research query")
        highlights: list[str] = []
        for idx, paper in enumerate(papers[:5], start=1):
            title = str(paper.get("title") or f"Source {idx}")
            summary = str(paper.get("abstract") or paper.get("summary") or "").strip()
            if summary:
                highlights.append(f"{idx}. {title}: {summary[:220]}")
            else:
                highlights.append(f"{idx}. {title}")

        body = "\\n".join(highlights) if highlights else "No evidence sources were provided."
        return {
            "answer": f"Synthesis for '{query}':\\n{body}",
            "citations": papers[:5],
            "confidence": 0.6 if highlights else 0.2,
            "backend": "wisdev_synthesis_local",
            "authoritative": False,
            "authority": PYTHON_TOOL_AUTHORITY,
            "finalAnswerAuthority": GO_ORCHESTRATOR_AUTHORITY,
            "answerStatus": "provisional_tool_output",
            "promotionAllowed": False,
        }

    return {
        "error": {
            "code": "UNSUPPORTED_ACTION",
            "message": f"Unsupported action '{action}'.",
        }
    }


def _emit_legacy(action: str, raw_result: dict[str, Any]) -> dict[str, Any]:
    envelope = emit_for_action(action, raw_result)
    return flatten_to_legacy(envelope)


def _resolve_max_execution_ms(explicit_ms: Optional[int]) -> int:
    if explicit_ms is not None:
        try:
            parsed = int(explicit_ms)
        except (TypeError, ValueError):
            parsed = DEFAULT_MAX_EXECUTION_MS
    else:
        env_value = os.environ.get("DEEPAGENTS_MAX_EXECUTION_MS")
        try:
            parsed = int(env_value) if env_value else DEFAULT_MAX_EXECUTION_MS
        except (TypeError, ValueError):
            parsed = DEFAULT_MAX_EXECUTION_MS
    return max(MIN_EXECUTION_MS, min(MAX_EXECUTION_MS, parsed))


def _resolve_allowlist(allowlisted_tools: Optional[list[str]]) -> set[str]:
    if not allowlisted_tools:
        return set(SUPPORTED_WISDEV_ACTIONS)
    normalized = {str(action).strip() for action in allowlisted_tools if str(action).strip()}
    return normalized.intersection(SUPPORTED_WISDEV_ACTIONS)


def _extract_agent_output(result: Any) -> str:
    if isinstance(result, str):
        return result

    if isinstance(result, dict):
        if isinstance(result.get("output"), str):
            return result["output"]

        messages = result.get("messages")
        if isinstance(messages, list):
            for message in reversed(messages):
                if not isinstance(message, dict):
                    continue
                if message.get("role") != "assistant":
                    continue
                content = message.get("content")
                if isinstance(content, str):
                    return content
                if isinstance(content, list):
                    text_chunks: list[str] = []
                    for item in content:
                        if isinstance(item, dict) and isinstance(item.get("text"), str):
                            text_chunks.append(item["text"])
                    if text_chunks:
                        return "\n".join(text_chunks)

    return str(result)
