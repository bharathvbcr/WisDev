"""
routers/manuscript_router.py — Manuscript section drafting endpoints.

These endpoints back the Go orchestrator's ``ManuscriptPipeline``
(``postSectionContent`` in ``internal/wisdev/manuscript_pipeline.go``), which
POSTs section briefs and refinement requests here and expects a
``{"content": "..."}`` body. Until this router existed every pipeline call
404'd and the manuscript silently degraded to its non-LLM fallback.

Registered with absolute paths (like ``agent_router``) in main.py.
"""

from __future__ import annotations

from typing import Any

import structlog
from fastapi import APIRouter, HTTPException
from pydantic import BaseModel, ConfigDict, Field

from services.gemini_service import gemini_service

router = APIRouter()
logger = structlog.get_logger(__name__)

# Section drafting is a background pipeline step, not an interactive request:
# give the sidecar a full-length first attempt plus retry headroom.
_SECTION_LATENCY_BUDGET_MS = 30_000
_MAX_CLAIM_PACKETS = 24
_MAX_SNIPPETS_PER_PACKET = 3
_MAX_SNIPPET_CHARS = 400
_MAX_SECTION_TOKENS = 4096


class SectionGenerateRequest(BaseModel):
    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    section_id: str = Field("", alias="sectionId")
    writer_role: str = Field("", alias="writerRole")
    section_goal: str = Field("", alias="sectionGoal")
    claim_packets: list[dict[str, Any]] = Field(default_factory=list, alias="claimPackets")
    source_titles: list[str] = Field(default_factory=list, alias="sourceTitles")
    query: str = ""
    max_tokens: int = Field(800, alias="maxTokens", ge=64, le=_MAX_SECTION_TOKENS)


class SectionRefineRequest(BaseModel):
    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    section_id: str = Field("", alias="sectionId")
    original_content: str = Field("", alias="originalContent")
    unresolved_issues: list[str] = Field(default_factory=list, alias="unresolvedIssues")
    claim_packets: list[dict[str, Any]] = Field(default_factory=list, alias="claimPackets")
    max_tokens: int = Field(800, alias="maxTokens", ge=64, le=_MAX_SECTION_TOKENS)


class SectionContentResponse(BaseModel):
    content: str


def _format_claim_packets(claim_packets: list[dict[str, Any]]) -> str:
    """Render claim packets (Go ``evidence.EvidencePacket`` JSON) for the prompt.

    Packet counts and snippet lengths are capped so a large evidence set cannot
    blow up the prompt (and with it, latency).
    """
    lines: list[str] = []
    for idx, packet in enumerate(claim_packets[:_MAX_CLAIM_PACKETS], start=1):
        if not isinstance(packet, dict):
            continue
        claim = str(packet.get("claimText") or packet.get("claim_text") or "").strip()
        if not claim:
            continue
        status = str(packet.get("verifierStatus") or packet.get("verifier_status") or "").strip()
        confidence = packet.get("confidence")
        header = f"{idx}. Claim: {claim}"
        if status:
            header += f" [verifier: {status}]"
        if isinstance(confidence, (int, float)) and confidence:
            header += f" (confidence {confidence:.2f})"
        lines.append(header)
        spans = packet.get("evidenceSpans") or packet.get("evidence_spans") or []
        if isinstance(spans, list):
            for span in spans[:_MAX_SNIPPETS_PER_PACKET]:
                if not isinstance(span, dict):
                    continue
                snippet = str(span.get("snippet") or "").strip()
                if not snippet:
                    continue
                source = str(
                    span.get("sourceCanonicalId") or span.get("source_canonical_id") or ""
                ).strip()
                prefix = f"   - [{source}] " if source else "   - "
                lines.append(prefix + snippet[:_MAX_SNIPPET_CHARS])
    dropped = max(0, len(claim_packets) - _MAX_CLAIM_PACKETS)
    if dropped:
        lines.append(f"(+{dropped} additional claim packets omitted for length)")
    return "\n".join(lines) if lines else "(no claim packets provided)"


async def _generate_section_text(prompt: str, max_tokens: int, operation: str) -> str:
    try:
        content = await gemini_service.generate_text(
            prompt,
            temperature=0.4,
            max_tokens=max_tokens,
            latency_budget_ms=_SECTION_LATENCY_BUDGET_MS,
            request_class="standard",
            retry_profile="standard",
        )
    except Exception as exc:
        logger.warning(
            "manuscript_section_generation_failed",
            operation=operation,
            error=str(exc),
            error_type=exc.__class__.__name__,
        )
        raise HTTPException(
            status_code=503, detail=f"{operation} failed: {exc}"
        ) from exc

    content = (content or "").strip()
    if not content:
        raise HTTPException(status_code=503, detail=f"{operation} returned empty content")
    return content


@router.post("/wisdev/manuscript/section/generate", response_model=SectionContentResponse)
async def generate_manuscript_section(payload: SectionGenerateRequest) -> SectionContentResponse:
    source_titles = "\n".join(f"- {title}" for title in payload.source_titles[:30])
    prompt = f"""You are the {payload.writer_role or 'scientific writer'} drafting the "{payload.section_id or 'section'}" section of a research manuscript.

Research query: {payload.query}

Section goal: {payload.section_goal}

Verified claim packets (the ONLY permissible evidence):
{_format_claim_packets(payload.claim_packets)}

Source titles:
{source_titles or '- (none provided)'}

Instructions:
1. Write the section in clear academic prose organized into paragraphs.
2. Ground every substantive statement in the claim packets above; do not invent evidence.
3. Reference claims by their numbered position, e.g. [1], at the end of supporting sentences.
4. Note unresolved contradictions explicitly rather than smoothing over them.
5. Return only the section text — no headings, preamble, or commentary."""
    content = await _generate_section_text(
        prompt, payload.max_tokens, "manuscript_section_generate"
    )
    logger.info(
        "manuscript_section_generated",
        section_id=payload.section_id,
        content_chars=len(content),
        claim_packets=len(payload.claim_packets),
    )
    return SectionContentResponse(content=content)


@router.post("/wisdev/manuscript/section/refine", response_model=SectionContentResponse)
async def refine_manuscript_section(payload: SectionRefineRequest) -> SectionContentResponse:
    issues = "\n".join(f"- {issue}" for issue in payload.unresolved_issues[:20])
    prompt = f"""You are revising the "{payload.section_id or 'section'}" section of a research manuscript to resolve reviewer issues.

Current section text:
{payload.original_content}

Unresolved issues to fix:
{issues or '- (none listed)'}

Verified claim packets (the ONLY permissible evidence):
{_format_claim_packets(payload.claim_packets)}

Instructions:
1. Rewrite the section so each listed issue is addressed.
2. Keep statements grounded in the claim packets; remove or qualify anything unsupported.
3. Preserve numbered claim references like [1] where the evidence still supports them.
4. Return only the revised section text — no headings, preamble, or commentary."""
    content = await _generate_section_text(
        prompt, payload.max_tokens, "manuscript_section_refine"
    )
    logger.info(
        "manuscript_section_refined",
        section_id=payload.section_id,
        content_chars=len(content),
        issues=len(payload.unresolved_issues),
    )
    return SectionContentResponse(content=content)
