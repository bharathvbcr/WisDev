"""Additional manuscript router formatting and endpoint coverage."""

from __future__ import annotations

from unittest.mock import AsyncMock, patch

from fastapi import FastAPI
from fastapi.testclient import TestClient

from routers import manuscript_router as mr


def _app() -> FastAPI:
    app = FastAPI()
    app.include_router(mr.router)
    return app


def test_format_claim_packets_renders_quantitative_claims() -> None:
    packets = [
        {
            "claimText": "Void growth accelerates under high triaxiality.",
            "quantitativeClaims": [{"value": "30", "unit": "%"}],
            "corroboratingSourceCount": 3,
        }
    ]
    rendered = mr._format_claim_packets(packets)
    assert "30 %" in rendered
    assert "corroborated by 3 sources" in rendered


def test_format_section_roster_and_review_sources() -> None:
    roster = mr._format_section_roster(
        [{"sectionId": "intro", "title": "Intro", "claimPacketIds": ["p1", "p2"]}]
    )
    assert "intro" in roster
    n = mr._MAX_CLAIM_PACKETS + 5
    sources = mr._format_review_sources(
        [{"claimText": f"Claim {idx}", "sourceTitle": f"Paper {idx}"} for idx in range(n)]
    )
    assert "Claim 0" in sources
    assert "omitted for length" in sources


def test_strip_prior_section_citations_replaces_local_markers() -> None:
    stripped = mr._strip_prior_section_citations("Finding [1] and grouped [2, 3] with packet [evp_abc].")
    assert stripped == "Finding (cited) and grouped (cited) with packet (cited)."


def test_format_prior_sections_strips_citation_markers() -> None:
    rendered = mr._format_prior_sections(
        [{"title": "Intro", "text": "Magnesium is lightweight [18]."}]
    )
    assert "(cited)" in rendered
    assert "[18]" not in rendered


def test_generate_abstract_with_prior_sections_uses_summary_prompt() -> None:
    payload = {
        "section_id": "abstract",
        "writer_role": "professor",
        "section_goal": "Summarize the review",
        "claim_packets": [{"packetId": "p1", "claimText": "Void growth matters."}],
        "source_titles": ["Paper"],
        "query": "void growth",
        "thesis": "HCP crystals",
        "prior_sections": [{"title": "Intro", "text": "Magnesium is lightweight [18]."}],
        "max_tokens": 500,
    }
    with patch(
        "routers.manuscript_router.manuscript_llm",
        return_value=type(
            "LLM",
            (),
            {"generate_text": AsyncMock(return_value="Abstract summarizing prior sections.")},
        )(),
    ) as mock_llm:
        client = TestClient(_app())
        resp = client.post("/wisdev/manuscript/section/generate", json=payload)

    assert resp.status_code == 200
    prompt = mock_llm.return_value.generate_text.await_args.args[0]
    assert "DRAFTED SECTIONS" in prompt
    assert "Magnesium is lightweight (cited)." in prompt
    assert "[18]" not in prompt
    assert mr._SECTION_LOCAL_CITATION_DIRECTIVE in prompt


def test_review_clamps_invalid_content_score() -> None:
    payload = {
        "query": "void growth",
        "thesis": "HCP",
        "sections": [{"title": "Intro", "text": "Draft [1]."}],
    }
    bad_review = mr.ManuscriptReviewResponse(content_score=99.0)
    with patch(
        "routers.manuscript_router.manuscript_llm",
        return_value=type(
            "LLM",
            (),
            {"generate_json": AsyncMock(return_value=bad_review)},
        )(),
    ):
        client = TestClient(_app())
        resp = client.post("/wisdev/manuscript/review", json=payload)

    assert resp.status_code == 200
    assert resp.json()["content_score"] == 1.0


def test_coordinate_dedupe_failure_returns_empty_response() -> None:
    payload = {
        "query": "void growth",
        "thesis": "HCP",
        "genre": "literature-review",
        "sections": [{"section_id": "intro", "title": "Intro", "text": "Draft [1]."}],
        "redundancies": ["Repeated void-growth paragraph."],
    }
    with patch(
        "routers.manuscript_router.manuscript_llm",
        return_value=type(
            "LLM",
            (),
            {"generate_json": AsyncMock(side_effect=RuntimeError("llm down"))},
        )(),
    ):
        client = TestClient(_app())
        resp = client.post("/wisdev/manuscript/coordinate-dedupe", json=payload)

    assert resp.status_code == 200
    assert resp.json()["sections"] == []
