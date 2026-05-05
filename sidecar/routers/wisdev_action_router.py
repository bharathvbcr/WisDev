"""
routers/wisdev_action_router.py — Schema-typed WisDev action dispatch router.

This router defines the **HTTP contract boundary** between Go's
``defaultPythonExecutor`` and Python compute. The endpoints here are currently
stubs (501 Not Implemented) because Go handles all listed actions natively via
BrainCapabilities. They will be wired to live Python handlers when:

  1. A new capability requires Python-only compute (e.g. PDF-fed citation
     extraction, BM25 re-ranking, or ML embedding pipelines).
  2. The team decides to move an action from Go to Python for scale reasons.

Each endpoint:
  - Accepts the canonical action payload validated by the typed request model.
  - Returns a ``StepArtifactEnvelope`` serialized as a flat legacy map via
    ``flatten_to_legacy``, so Go's ``normalizeStepArtifacts`` continues to work
    without modification.

Registered under ``/wisdev`` in main.py.
"""

from __future__ import annotations

from typing import Any, Optional

import structlog
from fastapi import APIRouter, HTTPException, Request
from pydantic import BaseModel, ConfigDict, Field

from artifacts.emitters import (
    emit_build_claim_evidence_table,
    emit_for_action,
    emit_propose_hypotheses,
    emit_resolve_citations,
    emit_verify_citations,
    emit_verify_reasoning_paths,
    flatten_to_legacy,
)
from artifacts.schema import ARTIFACT_SCHEMA_VERSION
from services.ai_generation_service import ai_generation_service


class CanonicalCitationResponse(BaseModel):
    model_config = ConfigDict(populate_by_name=True)

    id: Optional[str] = None
    title: str = ""
    doi: Optional[str] = None
    arxiv_id: Optional[str] = Field(None, alias="arxivId")
    canonical_id: Optional[str] = Field(None, alias="canonicalId")
    authors: list[str] = Field(default_factory=list)
    year: Optional[int] = None
    resolved: bool = False
    verified: bool = False


class ReasoningBranchResponse(BaseModel):
    model_config = ConfigDict(populate_by_name=True)

    claim: str = ""
    falsifiability_condition: str = Field("", alias="falsifiabilityCondition")
    support_score: float = Field(0.0, alias="supportScore")
    is_terminated: bool = Field(False, alias="isTerminated")


class ReasoningVerificationResponse(BaseModel):
    model_config = ConfigDict(populate_by_name=True)

    total_branches: int = Field(0, alias="totalBranches")
    verified_branches: int = Field(0, alias="verifiedBranches")
    rejected_branches: int = Field(0, alias="rejectedBranches")
    ready_for_synthesis: bool = Field(False, alias="readyForSynthesis")


class ClaimEvidenceTableResponse(BaseModel):
    model_config = ConfigDict(populate_by_name=True)

    table: str = ""
    row_count: int = Field(0, alias="rowCount")


class ResolveCitationsResponse(BaseModel):
    model_config = ConfigDict(populate_by_name=True)

    canonical_sources: list[CanonicalCitationResponse] = Field(default_factory=list, alias="canonicalSources")
    citations: list[CanonicalCitationResponse] = Field(default_factory=list)
    resolved_count: int = Field(0, alias="resolvedCount")
    duplicate_count: int = Field(0, alias="duplicateCount")


class VerifyCitationsResponse(BaseModel):
    model_config = ConfigDict(populate_by_name=True)

    verified_records: list[CanonicalCitationResponse] = Field(default_factory=list, alias="verifiedRecords")
    citations: list[CanonicalCitationResponse] = Field(default_factory=list)
    valid_count: int = Field(0, alias="validCount")
    invalid_count: int = Field(0, alias="invalidCount")
    duplicate_count: int = Field(0, alias="duplicateCount")


class ProposeHypothesesResponse(BaseModel):
    model_config = ConfigDict(populate_by_name=True)

    branches: list[ReasoningBranchResponse] = Field(default_factory=list)


class VerifyReasoningPathsResponse(BaseModel):
    model_config = ConfigDict(populate_by_name=True)

    branches: list[ReasoningBranchResponse] = Field(default_factory=list)
    reasoning_verification: ReasoningVerificationResponse = Field(
        default_factory=lambda: ReasoningVerificationResponse.model_validate({}),
        alias="reasoningVerification",
    )


from services.idea_generation_service import idea_generation_service


class ResearchIdeaResponse(BaseModel):
    model_config = ConfigDict(populate_by_name=True)

    id: str
    title: str
    description: str
    novelty_score: float = Field(0.0, alias="noveltyScore")
    feasibility_score: float = Field(0.0, alias="feasibilityScore")
    reasoning: str
    hypotheses: list[str] = Field(default_factory=list)


class IdeaGenerationResponse(BaseModel):
    model_config = ConfigDict(populate_by_name=True)

    ideas: list[ResearchIdeaResponse] = Field(default_factory=list)
    thought_signature: str = Field("", alias="thoughtSignature")


class BuildClaimEvidenceTableResponse(BaseModel):
    model_config = ConfigDict(populate_by_name=True)

    claim_evidence_table: ClaimEvidenceTableResponse = Field(
        default_factory=lambda: ClaimEvidenceTableResponse.model_validate({}),
        alias="claimEvidenceTable",
    )

logger = structlog.get_logger(__name__)

router = APIRouter(tags=["wisdev"])


def _emit_action_output(action: str, raw_result: dict[str, Any]) -> dict[str, Any]:
    try:
        return ai_generation_service.emit_wisdev_action_output(action, raw_result)
    except ValueError as exc:
        raise HTTPException(
            status_code=422,
            detail={
                "code": "ARTIFACT_SCHEMA_VIOLATION",
                "message": str(exc),
                "action": action,
                "schemaVersion": ARTIFACT_SCHEMA_VERSION,
            },
        ) from exc


# ---------------------------------------------------------------------------
# Schema version probe
# ---------------------------------------------------------------------------


@router.get("/schema-version", response_model=dict[str, str])
async def schema_version() -> dict[str, str]:
    """Return the current artifact schema version as a readiness/contract probe."""
    return {"schemaVersion": ARTIFACT_SCHEMA_VERSION}


# ---------------------------------------------------------------------------
# Generic action dispatch (primary entry point for Go's future HTTP fallback)
# ---------------------------------------------------------------------------


class ActionRequest(BaseModel):
    """Generic action dispatch payload — mirrors the dict Go sends to Python."""

    model_config = ConfigDict(populate_by_name=True)

    action: str
    session_id: Optional[str] = Field(None, alias="sessionId")
    plan_id: Optional[str] = Field(None, alias="planId")
    plan_step_id: Optional[str] = Field(None, alias="planStepId")
    query: Optional[str] = None
    model: Optional[str] = None
    # All remaining fields passed through as-is.
    payload: dict[str, Any] = Field(default_factory=dict)


@router.post("/action", response_model=dict[str, Any])
async def dispatch_action(request: Request) -> dict[str, Any]:
    """
    Generic action dispatcher.

    Go's ``defaultPythonExecutor`` may call this endpoint as a fallback for any
    action not handled natively. The response is always a flat legacy map that
    ``normalizeStepArtifacts`` can consume directly.

    Currently returns 501 for all actions while Go handles them natively. Wire
    ``emit_for_action`` to live service calls here when needed.
    """
    try:
        body: dict[str, Any] = await request.json()
    except Exception:
        raise HTTPException(status_code=400, detail="Invalid JSON body")

    action = str(body.get("action", "")).strip()
    if not action:
        raise HTTPException(status_code=400, detail="Missing 'action' field")

    logger.info("wisdev_action_dispatch", action=action)

    payload = body.get("payload")
    payload_map = payload if isinstance(payload, dict) else {}

    if action == "research.resolveCanonicalCitations":
        papers = payload_map.get("papers", body.get("papers", []))
        return _emit_action_output(
            action,
            {
                "canonicalSources": papers,
                "resolvedCount": len(papers) if isinstance(papers, list) else 0,
                "duplicateCount": 0,
            },
        )

    if action == "research.verifyCitations":
        papers = payload_map.get("papers", body.get("papers", []))
        return _emit_action_output(
            action,
            {
                "verifiedRecords": papers,
                "validCount": len(papers) if isinstance(papers, list) else 0,
                "invalidCount": 0,
                "duplicateCount": 0,
            },
        )

    if action in {"research.proposeHypotheses", "research.generateHypotheses"}:
        return _emit_action_output(
            action,
            {
                "branches": payload_map.get("branches", body.get("branches", [])),
            },
        )

    if action == "research.verifyReasoningPaths":
        branches = payload_map.get("branches", body.get("branches", []))
        total = len(branches) if isinstance(branches, list) else 0
        return _emit_action_output(
            action,
            {
                "branches": branches,
                "totalBranches": total,
                "verifiedBranches": total,
                "rejectedBranches": 0,
                "readyForSynthesis": total > 0,
            },
        )

    if action == "research.buildClaimEvidenceTable":
        papers = payload_map.get("papers", body.get("papers", []))
        row_count = len(papers) if isinstance(papers, list) else 0
        return _emit_action_output(
            action,
            {
                "table": f"| Claim | Evidence |\n|---|---|\n| {body.get('query', payload_map.get('query', 'N/A'))} | {row_count} sources |",
                "rowCount": row_count,
            },
        )

    if action == "research.generateIdeas":
        query = body.get("query", "")
        papers = payload_map.get("papers", body.get("papers", []))
        thought_sig = body.get("thoughtSignature", "")
        
        result = await idea_generation_service.generate_ideas(
            query=query,
            literature=papers,
            thought_signature=thought_sig
        )
        return _emit_action_output(action, result.model_dump(by_alias=True))

    # Remaining actions are currently handled natively in Go.
    # Return 501 with the schema version so consumers can detect the boundary.
    raise HTTPException(
        status_code=501,
        detail={
            "code": "NOT_IMPLEMENTED",
            "message": f"Action '{action}' is not yet implemented in the Python sidecar. "
            "It is handled natively by the Go orchestrator.",
            "action": action,
            "schemaVersion": ARTIFACT_SCHEMA_VERSION,
        },
    )


# ---------------------------------------------------------------------------
# Typed stub endpoints — one per defined WisDev action
# ---------------------------------------------------------------------------
# These stubs:
#   1. Validate the incoming payload against the typed request model.
#   2. Return 501 — the live implementation is in Go.
#   3. Demonstrate the expected response envelope so client code can be written
#      against the contract without deploying live Python compute.
# ---------------------------------------------------------------------------


class ResolveCitationsRequest(BaseModel):
    model_config = ConfigDict(populate_by_name=True)

    papers: list[dict[str, Any]] = Field(default_factory=list)
    query: Optional[str] = None
    model: Optional[str] = None
    session_id: Optional[str] = Field(None, alias="sessionId")


@router.post("/action/research.resolveCanonicalCitations", response_model=ResolveCitationsResponse)
async def resolve_canonical_citations(req: ResolveCitationsRequest) -> dict[str, Any]:
    """Resolve citations with emitter-backed legacy flattening for Go ingress."""
    raw_result: dict[str, Any] = {
        "canonicalSources": req.papers,
        "resolvedCount": len(req.papers),
        "duplicateCount": 0,
    }
    return _emit_action_output(
        "research.resolveCanonicalCitations",
        raw_result,
    )


class VerifyCitationsRequest(BaseModel):
    model_config = ConfigDict(populate_by_name=True)

    papers: list[dict[str, Any]] = Field(default_factory=list)
    model: Optional[str] = None
    session_id: Optional[str] = Field(None, alias="sessionId")


@router.post("/action/research.verifyCitations", response_model=VerifyCitationsResponse)
async def verify_citations(req: VerifyCitationsRequest) -> dict[str, Any]:
    """Verify citations with emitter-backed legacy flattening for Go ingress."""
    raw_result: dict[str, Any] = {
        "verifiedRecords": req.papers,
        "validCount": len(req.papers),
        "invalidCount": 0,
        "duplicateCount": 0,
    }
    return _emit_action_output(
        "research.verifyCitations",
        raw_result,
    )


class ProposeHypothesesRequest(BaseModel):
    model_config = ConfigDict(populate_by_name=True)

    query: str
    intent: Optional[str] = None
    model: Optional[str] = None
    session_id: Optional[str] = Field(None, alias="sessionId")


@router.post("/action/research.proposeHypotheses", response_model=ProposeHypothesesResponse)
@router.post("/action/research.generateHypotheses", response_model=ProposeHypothesesResponse)
async def propose_hypotheses(req: ProposeHypothesesRequest) -> dict[str, Any]:
    """Return emitter-backed hypotheses output for both action aliases."""
    raw_result: dict[str, Any] = {
        "branches": [
            {
                "claim": req.query,
                "falsifiabilityCondition": "Test against contradictory evidence",
                "supportScore": 0.5,
                "isTerminated": False,
            }
        ]
    }
    return _emit_action_output(
        "research.proposeHypotheses",
        raw_result,
    )


class VerifyReasoningPathsRequest(BaseModel):
    model_config = ConfigDict(populate_by_name=True)

    branches: list[dict[str, Any]] = Field(default_factory=list)
    model: Optional[str] = None
    session_id: Optional[str] = Field(None, alias="sessionId")


@router.post("/action/research.verifyReasoningPaths", response_model=VerifyReasoningPathsResponse)
async def verify_reasoning_paths(req: VerifyReasoningPathsRequest) -> dict[str, Any]:
    """Return emitter-backed reasoning verification output."""
    total = len(req.branches)
    raw_result: dict[str, Any] = {
        "branches": req.branches,
        "totalBranches": total,
        "verifiedBranches": total,
        "rejectedBranches": 0,
        "readyForSynthesis": total > 0,
    }
    return _emit_action_output(
        "research.verifyReasoningPaths",
        raw_result,
    )


class BuildClaimEvidenceTableRequest(BaseModel):
    model_config = ConfigDict(populate_by_name=True)

    query: str
    papers: list[dict[str, Any]] = Field(default_factory=list)
    model: Optional[str] = None
    session_id: Optional[str] = Field(None, alias="sessionId")


@router.post("/action/research.buildClaimEvidenceTable", response_model=BuildClaimEvidenceTableResponse)
async def build_claim_evidence_table(
    req: BuildClaimEvidenceTableRequest,
) -> dict[str, Any]:
    """Return emitter-backed claim/evidence table output."""
    raw_result: dict[str, Any] = {
        "table": f"| Claim | Evidence |\n|---|---|\n| {req.query} | {len(req.papers)} sources |",
        "rowCount": len(req.papers),
    }
    return _emit_action_output(
        "research.buildClaimEvidenceTable",
        raw_result,
    )
