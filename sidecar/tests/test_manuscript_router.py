from unittest.mock import AsyncMock, patch

from fastapi import FastAPI
from fastapi.testclient import TestClient


def make_app():
    from routers.manuscript_router import router

    app = FastAPI()
    app.include_router(router)
    return app


_GENERATE_PAYLOAD = {
    "section_id": "introduction",
    "writer_role": "methods writer",
    "section_goal": "Summarize ACL reconstruction evidence",
    "claim_packets": [
        {
            "packetId": "p1",
            "claimText": "Scaffold-augmented repair improves outcomes",
            "verifierStatus": "verified",
            "confidence": 0.91,
            "evidenceSpans": [
                {
                    "sourceCanonicalId": "doi:10.1/abc",
                    "snippet": "Scaffold cohort showed improved Lysholm scores.",
                    "support": "supports",
                }
            ],
        }
    ],
    "source_titles": ["Meniscal scaffold trial"],
    "query": "meniscus scaffolds and ACL reconstruction",
    "max_tokens": 800,
}

_REFINE_PAYLOAD = {
    "section_id": "introduction",
    "original_content": "Draft section text [1].",
    "unresolved_issues": ["Citation [1] lacks support for the second claim"],
    "claim_packets": _GENERATE_PAYLOAD["claim_packets"],
    "max_tokens": 800,
}


def test_section_generate_returns_content():
    with patch(
        "routers.manuscript_router.gemini_service.generate_text",
        AsyncMock(return_value="Generated section text [1]."),
    ) as mock_generate:
        client = TestClient(make_app())
        resp = client.post("/wisdev/manuscript/section/generate", json=_GENERATE_PAYLOAD)

    assert resp.status_code == 200
    assert resp.json() == {"content": "Generated section text [1]."}
    prompt = mock_generate.await_args.args[0]
    assert "Scaffold-augmented repair improves outcomes" in prompt
    assert "Scaffold cohort showed improved Lysholm scores." in prompt
    assert "meniscus scaffolds and ACL reconstruction" in prompt


def test_section_refine_returns_content():
    with patch(
        "routers.manuscript_router.gemini_service.generate_text",
        AsyncMock(return_value="Refined section text [1]."),
    ) as mock_generate:
        client = TestClient(make_app())
        resp = client.post("/wisdev/manuscript/section/refine", json=_REFINE_PAYLOAD)

    assert resp.status_code == 200
    assert resp.json() == {"content": "Refined section text [1]."}
    prompt = mock_generate.await_args.args[0]
    assert "Draft section text [1]." in prompt
    assert "Citation [1] lacks support for the second claim" in prompt


def test_section_generate_llm_failure_returns_503():
    with patch(
        "routers.manuscript_router.gemini_service.generate_text",
        AsyncMock(side_effect=RuntimeError("provider exploded")),
    ):
        client = TestClient(make_app())
        resp = client.post("/wisdev/manuscript/section/generate", json=_GENERATE_PAYLOAD)

    assert resp.status_code == 503


def test_section_generate_empty_content_returns_503():
    with patch(
        "routers.manuscript_router.gemini_service.generate_text",
        AsyncMock(return_value="   "),
    ):
        client = TestClient(make_app())
        resp = client.post("/wisdev/manuscript/section/refine", json=_REFINE_PAYLOAD)

    assert resp.status_code == 503
