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
        "services.gemini_service.gemini_service.generate_text",
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


def test_section_generate_default_length_directive():
    with patch(
        "services.gemini_service.gemini_service.generate_text",
        AsyncMock(return_value="Generated section text [1]."),
    ) as mock_generate:
        client = TestClient(make_app())
        resp = client.post("/wisdev/manuscript/section/generate", json=_GENERATE_PAYLOAD)

    assert resp.status_code == 200
    prompt = mock_generate.await_args.args[0]
    assert "AT LEAST 800 words" in prompt
    assert "approximately" not in prompt


def test_section_generate_honors_target_words():
    payload = {**_GENERATE_PAYLOAD, "target_words": 1500}
    with patch(
        "services.gemini_service.gemini_service.generate_text",
        AsyncMock(return_value="Generated section text [1]."),
    ) as mock_generate:
        client = TestClient(make_app())
        resp = client.post("/wisdev/manuscript/section/generate", json=payload)

    assert resp.status_code == 200
    prompt = mock_generate.await_args.args[0]
    assert "approximately 1500 words" in prompt
    assert "AT LEAST 800 words" not in prompt


def test_section_generate_honors_min_citations():
    payload = {**_GENERATE_PAYLOAD, "min_citations": 12}
    with patch(
        "services.gemini_service.gemini_service.generate_text",
        AsyncMock(return_value="Generated section text [1]."),
    ) as mock_generate:
        client = TestClient(make_app())
        resp = client.post("/wisdev/manuscript/section/generate", json=payload)

    assert resp.status_code == 200
    prompt = mock_generate.await_args.args[0]
    assert "at least 12 DISTINCT sources" in prompt


def test_section_generate_discourages_em_dashes():
    with patch(
        "services.gemini_service.gemini_service.generate_text",
        AsyncMock(return_value="Generated section text [1]."),
    ) as mock_generate:
        client = TestClient(make_app())
        resp = client.post("/wisdev/manuscript/section/generate", json=_GENERATE_PAYLOAD)

    assert resp.status_code == 200
    prompt = mock_generate.await_args.args[0]
    assert "minimize em-dashes" in prompt


def test_section_refine_returns_content():
    with patch(
        "services.gemini_service.gemini_service.generate_text",
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
        "services.gemini_service.gemini_service.generate_text",
        AsyncMock(side_effect=RuntimeError("provider exploded")),
    ):
        client = TestClient(make_app())
        resp = client.post("/wisdev/manuscript/section/generate", json=_GENERATE_PAYLOAD)

    assert resp.status_code == 503


def test_review_manuscript_empty_sections_skips_llm():
    with patch(
        "services.gemini_service.gemini_service.generate_json",
        AsyncMock(side_effect=AssertionError("LLM must not be called for empty sections")),
    ):
        client = TestClient(make_app())
        resp = client.post("/wisdev/manuscript/review", json={"query": "q", "thesis": "t", "sections": []})

    assert resp.status_code == 200
    assert resp.json()["content_score"] == 0.0


def test_review_manuscript_returns_structured_review():
    from routers.manuscript_router import ManuscriptReviewResponse

    review = ManuscriptReviewResponse(
        content_score=0.7,
        attribution_issues=["uses 'we conducted'"],
        recommendations=["attribute to sources"],
    )
    with patch(
        "services.gemini_service.gemini_service.generate_json",
        AsyncMock(return_value=review),
    ):
        client = TestClient(make_app())
        resp = client.post(
            "/wisdev/manuscript/review",
            json={"query": "q", "thesis": "t", "sections": [{"title": "Methods", "text": "We conducted a study."}]},
        )

    assert resp.status_code == 200
    body = resp.json()
    assert body["content_score"] == 0.7
    assert "uses 'we conducted'" in body["attribution_issues"]


def test_review_manuscript_genre_and_citation_aware():
    from routers.manuscript_router import ManuscriptReviewResponse

    # Default genre (review) + provided sources -> review voice rule + sources block.
    with patch(
        "services.gemini_service.gemini_service.generate_json",
        AsyncMock(return_value=ManuscriptReviewResponse(content_score=0.8)),
    ) as mock_review:
        client = TestClient(make_app())
        resp = client.post(
            "/wisdev/manuscript/review",
            json={
                "query": "q",
                "thesis": "t",
                "sections": [{"title": "Results", "text": "Scaffold repair helps [1]."}],
                "claim_packets": [{"claimText": "Scaffold repair improves outcomes"}],
            },
        )
    assert resp.status_code == 200
    prompt = mock_review.await_args.args[0]
    assert "narrative literature review" in prompt.lower()
    assert "Scaffold repair improves outcomes" in prompt  # sources block (citation-aware)
    assert "GROUNDED" in prompt

    # Research-paper genre -> first-person voice is EXPECTED, not penalized.
    with patch(
        "services.gemini_service.gemini_service.generate_json",
        AsyncMock(return_value=ManuscriptReviewResponse(content_score=0.8)),
    ) as mock_paper:
        client = TestClient(make_app())
        resp = client.post(
            "/wisdev/manuscript/review",
            json={"query": "q", "thesis": "t", "genre": "research paper",
                  "sections": [{"title": "Intro", "text": "We propose a method."}]},
        )
    assert resp.status_code == 200
    paper_prompt = mock_paper.await_args.args[0]
    assert "research paper" in paper_prompt
    assert "EXPECTED" in paper_prompt


def test_review_manuscript_llm_failure_returns_empty():
    with patch(
        "services.gemini_service.gemini_service.generate_json",
        AsyncMock(side_effect=RuntimeError("boom")),
    ):
        client = TestClient(make_app())
        resp = client.post(
            "/wisdev/manuscript/review",
            json={"query": "q", "sections": [{"title": "X", "text": "content"}]},
        )

    assert resp.status_code == 200
    assert resp.json()["content_score"] == 0.0


def test_section_generate_empty_content_returns_503():
    with patch(
        "services.gemini_service.gemini_service.generate_text",
        AsyncMock(return_value="   "),
    ):
        client = TestClient(make_app())
        resp = client.post("/wisdev/manuscript/section/refine", json=_REFINE_PAYLOAD)

    assert resp.status_code == 503


# --- coordination (concept-level cross-section ownership) ---

_COORDINATE_PAYLOAD = {
    "query": "RAG for clinical decision support",
    "thesis": "RAG improves grounding in clinical LLMs",
    "sections": [
        {"section_id": "results", "title": "Synthesis of Findings", "claim_packet_ids": ["p1"]},
        {"section_id": "introduction", "title": "Introduction", "claim_packet_ids": ["p1"]},
    ],
    "claim_packets": _GENERATE_PAYLOAD["claim_packets"],
}


def test_coordinate_returns_assignments():
    from routers.manuscript_router import ConceptAssignment, SectionCoordinationResponse

    plan = SectionCoordinationResponse(
        assignments=[
            ConceptAssignment(
                concept_label="89-study survey", owning_section_id="results", packet_ids=["p1"]
            )
        ]
    )
    with patch(
        "services.gemini_service.gemini_service.generate_json",
        AsyncMock(return_value=plan),
    ) as mock_json:
        client = TestClient(make_app())
        resp = client.post("/wisdev/manuscript/coordinate", json=_COORDINATE_PAYLOAD)

    assert resp.status_code == 200
    body = resp.json()
    assert body["assignments"][0]["owning_section_id"] == "results"
    prompt = mock_json.await_args.args[0]
    assert "results: Synthesis of Findings" in prompt
    assert "Scaffold-augmented repair improves outcomes" in prompt


def test_coordinate_drops_unknown_section_assignment():
    from routers.manuscript_router import ConceptAssignment, SectionCoordinationResponse

    plan = SectionCoordinationResponse(
        assignments=[
            ConceptAssignment(concept_label="real", owning_section_id="results", packet_ids=["p1"]),
            ConceptAssignment(concept_label="ghost", owning_section_id="nonexistent", packet_ids=[]),
        ]
    )
    with patch(
        "services.gemini_service.gemini_service.generate_json",
        AsyncMock(return_value=plan),
    ):
        client = TestClient(make_app())
        resp = client.post("/wisdev/manuscript/coordinate", json=_COORDINATE_PAYLOAD)

    assert resp.status_code == 200
    labels = [a["concept_label"] for a in resp.json()["assignments"]]
    assert labels == ["real"]


def test_coordinate_empty_inputs_skips_llm():
    with patch(
        "services.gemini_service.gemini_service.generate_json",
        AsyncMock(side_effect=AssertionError("LLM must not be called with empty inputs")),
    ):
        client = TestClient(make_app())
        resp = client.post("/wisdev/manuscript/coordinate", json={"query": "q", "sections": [], "claim_packets": []})

    assert resp.status_code == 200
    assert resp.json()["assignments"] == []


def test_coordinate_llm_failure_degrades():
    with patch(
        "services.gemini_service.gemini_service.generate_json",
        AsyncMock(side_effect=RuntimeError("boom")),
    ):
        client = TestClient(make_app())
        resp = client.post("/wisdev/manuscript/coordinate", json=_COORDINATE_PAYLOAD)

    assert resp.status_code == 200
    assert resp.json()["assignments"] == []


# --- prose-vs-source entailment fact-check ---

_FACTCHECK_PAYLOAD = {
    "section_id": "results",
    "content": "Outcomes improved by 70% across all cohorts [p1].",
    "claim_packets": _GENERATE_PAYLOAD["claim_packets"],
}


def test_fact_check_flags_unentailed():
    from routers.manuscript_router import FlaggedSentence, SectionFactCheckResponse

    flagged = SectionFactCheckResponse(
        flagged_sentences=[
            FlaggedSentence(
                sentence_text="Outcomes improved by 70% across all cohorts [p1].",
                issue="unsupported statistic",
                entailed_packet_ids=[],
                unentailed_reason="no snippet states 70%",
            )
        ]
    )
    with patch(
        "services.gemini_service.gemini_service.generate_json",
        AsyncMock(return_value=flagged),
    ) as mock_json:
        client = TestClient(make_app())
        resp = client.post("/wisdev/manuscript/fact-check", json=_FACTCHECK_PAYLOAD)

    assert resp.status_code == 200
    body = resp.json()
    assert body["flagged_sentences"][0]["issue"] == "unsupported statistic"
    prompt = mock_json.await_args.args[0]
    assert "[p1]" in prompt
    assert "Scaffold cohort showed improved Lysholm scores." in prompt
    assert "Outcomes improved by 70%" in prompt


def test_fact_check_empty_content_skips_llm():
    with patch(
        "services.gemini_service.gemini_service.generate_json",
        AsyncMock(side_effect=AssertionError("LLM must not be called with empty content")),
    ):
        client = TestClient(make_app())
        resp = client.post(
            "/wisdev/manuscript/fact-check",
            json={"section_id": "results", "content": "   ", "claim_packets": _GENERATE_PAYLOAD["claim_packets"]},
        )

    assert resp.status_code == 200
    assert resp.json()["flagged_sentences"] == []


def test_fact_check_llm_failure_degrades():
    with patch(
        "services.gemini_service.gemini_service.generate_json",
        AsyncMock(side_effect=RuntimeError("boom")),
    ):
        client = TestClient(make_app())
        resp = client.post("/wisdev/manuscript/fact-check", json=_FACTCHECK_PAYLOAD)

    assert resp.status_code == 200
    assert resp.json()["flagged_sentences"] == []


def test_manuscript_llm_selects_local_when_configured(monkeypatch):
    """#2: the manuscript endpoints route through manuscript_llm(), which uses a
    configured local model (Ollama) instead of Gemini/Vertex."""
    from services.manuscript_llm import manuscript_llm
    from services.local_llm_service import local_llm_service
    from services.gemini_service import gemini_service

    for var in ("MANUSCRIPT_LLM_PROVIDER", "LOCAL_LLM_BASE_URL", "OLLAMA_BASE_URL"):
        monkeypatch.delenv(var, raising=False)
    assert manuscript_llm() is gemini_service  # auto + unconfigured -> cloud (unchanged)

    monkeypatch.setenv("MANUSCRIPT_LLM_PROVIDER", "local")
    assert manuscript_llm() is local_llm_service

    monkeypatch.delenv("MANUSCRIPT_LLM_PROVIDER", raising=False)
    monkeypatch.setenv("OLLAMA_BASE_URL", "http://127.0.0.1:11434")
    assert manuscript_llm() is local_llm_service  # auto detects a configured local backend

    monkeypatch.setenv("MANUSCRIPT_LLM_PROVIDER", "gemini")
    assert manuscript_llm() is gemini_service  # explicit cloud overrides


def test_coordinate_dedupe_noop_without_redundancies():
    # No reviewer redundancies -> no LLM call, empty response (per-section draft stands).
    with patch(
        "services.gemini_service.gemini_service.generate_json",
        AsyncMock(side_effect=AssertionError("LLM must not be called without redundancies")),
    ):
        client = TestClient(make_app())
        resp = client.post(
            "/wisdev/manuscript/coordinate-dedupe",
            json={"query": "q", "sections": [
                {"sectionId": "intro", "title": "Intro", "text": "A."},
                {"sectionId": "results", "title": "Results", "text": "B."},
            ], "redundancies": []},
        )
    assert resp.status_code == 200
    assert resp.json()["sections"] == []


def test_coordinate_dedupe_revises_all_sections():
    from routers.manuscript_router import CoordinatedDedupeResponse, CoordinatedDedupeSectionOut

    revised = CoordinatedDedupeResponse(sections=[
        CoordinatedDedupeSectionOut(section_id="intro", text="Intro kept the definition."),
        CoordinatedDedupeSectionOut(section_id="results", text="Results, no longer repeating it."),
    ])
    with patch(
        "services.gemini_service.gemini_service.generate_json",
        AsyncMock(return_value=revised),
    ) as mock_dedupe:
        client = TestClient(make_app())
        resp = client.post(
            "/wisdev/manuscript/coordinate-dedupe",
            json={
                "query": "q",
                "sections": [
                    {"sectionId": "intro", "title": "Intro", "text": "Defines X. Then more."},
                    {"sectionId": "results", "title": "Results", "text": "Defines X again. Findings."},
                ],
                "redundancies": ["Definition of X repeated in Intro and Results"],
            },
        )
    assert resp.status_code == 200
    out = {s["section_id"]: s["text"] for s in resp.json()["sections"]}
    assert out["intro"] == "Intro kept the definition."
    assert "no longer repeating" in out["results"]
    prompt = mock_dedupe.await_args.args[0]
    assert "Definition of X repeated" in prompt
