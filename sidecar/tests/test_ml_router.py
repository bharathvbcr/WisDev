from unittest.mock import patch, AsyncMock
import base64
import pytest
from fastapi.testclient import TestClient
from fastapi import FastAPI
from services.ai_generation_service import (
    AiGenerationStructuredOutputRequiresNativeRuntimeError,
)

def make_app():
    from routers.ml_router import router
    app = FastAPI()
    app.include_router(router)
    return app

def test_pdf_endpoint_returns_text():
    mock_result = {
        "full_text": "Abstract: We propose...",
        "fullText": "Abstract: We propose...",
        "pages": 5,
        "pageCount": 5,
    }
    with patch("routers.ml_router.extract_pdf_content", return_value=mock_result):
        app = make_app()
        client = TestClient(app)
        b64 = base64.b64encode(b"%PDF-1.4 fake").decode()
        resp = client.post("/ml/pdf", json={"file_base64": b64, "file_name": "paper.pdf"})
    assert resp.status_code == 200
    assert "full_text" in resp.json()
    assert resp.json()["fullText"] == resp.json()["full_text"]
    assert resp.json()["pageCount"] == resp.json()["pages"]

def test_pdf_endpoint_missing_body_returns_422():
    app = make_app()
    client = TestClient(app)
    resp = client.post("/ml/pdf", json={})
    assert resp.status_code == 422

def test_pdf_endpoint_rejects_invalid_base64():
    app = make_app()
    client = TestClient(app)
    resp = client.post("/ml/pdf", json={"file_base64": "not-base64", "file_name": "paper.pdf"})
    assert resp.status_code == 400

def test_pdf_endpoint_surfaces_extractor_failures():
    with patch("routers.ml_router.extract_pdf_content", side_effect=RuntimeError("extractor down")):
        app = make_app()
        client = TestClient(app)
        b64 = base64.b64encode(b"%PDF-1.4 fake").decode()
        resp = client.post("/ml/pdf", json={"file_base64": b64, "file_name": "paper.pdf"})
    assert resp.status_code == 502
    assert "PDF extraction failed" in resp.json()["detail"]

def test_embed_endpoint_returns_vector():
    import asyncio
    mock_vector = [0.1] * 768
    async def fake_embed(text, **kwargs):
        return mock_vector
    with patch("routers.ml_router.embedding_service") as mock_svc:
        mock_svc.embed_single_async = fake_embed
        app = make_app()
        client = TestClient(app)
        resp = client.post("/ml/embed", json={"text": "transformer attention"})
    assert resp.status_code == 200
    assert len(resp.json()["embedding"]) == 768

def test_embed_endpoint_missing_text_returns_422():
    app = make_app()
    client = TestClient(app)
    resp = client.post("/ml/embed", json={})
    assert resp.status_code == 422

def test_embed_batch_endpoint_returns_vectors():
    async def fake_embed_batch(texts, **kwargs):
        return [[0.2, 0.3], [0.4, 0.5]][:len(texts)]
    with patch("routers.ml_router.embedding_service") as mock_svc:
        mock_svc.embed_batch_async = fake_embed_batch
        app = make_app()
        client = TestClient(app)
        resp = client.post("/ml/embed/batch", json={"texts": ["a", "b"]})
    assert resp.status_code == 200
    assert len(resp.json()["embeddings"]) == 2

def test_docling_parse_endpoint_returns_normalized_payload():
    with patch("routers.ml_router._docling_extract", return_value={
        "full_text": "# Title\n\nBody text",
        "structure_map": [{"label": "Title", "page": 0, "bbox": None}],
        "docling_meta": {"version": "2.3.0+"},
    }):
        app = make_app()
        client = TestClient(app)
        b64 = base64.b64encode(b"%PDF-1.4 fake").decode()
        resp = client.post("/ml/docling/parse", json={"file_base64": b64, "file_name": "paper.pdf"})
    assert resp.status_code == 200
    payload = resp.json()
    assert payload["fullText"] == payload["full_text"]
    assert payload["extractionInfo"]["usedDocling"] is True
    assert payload["doclingMeta"]["version"] == "2.3.0+"

def test_docling_parse_endpoint_returns_503_when_unavailable():
    with patch("routers.ml_router._docling_extract", return_value=None):
        app = make_app()
        client = TestClient(app)
        b64 = base64.b64encode(b"%PDF-1.4 fake").decode()
        resp = client.post("/ml/docling/parse", json={"file_base64": b64, "file_name": "paper.pdf"})
    assert resp.status_code == 503


def test_generate_research_ideas_endpoint_returns_dedicated_payload():
    class FakeIdeaResponse:
        def model_dump(self, by_alias: bool = False):
            assert by_alias is True
            return {
                "ideas": [{"title": "Idea 1"}],
                "thoughtSignature": "sig-1",
            }

    with patch(
        "routers.ml_router.idea_generation_service.generate_ideas",
        new=AsyncMock(return_value=FakeIdeaResponse()),
    ):
        app = make_app()
        client = TestClient(app)
        resp = client.post(
            "/ml/research/generate-ideas",
            json={
                "query": "causal inference",
                "papers": [{"title": "Paper 1"}],
                "thoughtSignature": "sig-1",
            },
        )

    assert resp.status_code == 200
    payload = resp.json()
    assert payload["thoughtSignature"] == "sig-1"
    assert payload["ideas"] == [{"title": "Idea 1"}]


def test_generate_research_ideas_endpoint_surfaces_failures():
    with patch(
        "routers.ml_router.idea_generation_service.generate_ideas",
        new=AsyncMock(side_effect=RuntimeError("model unavailable")),
    ):
        app = make_app()
        client = TestClient(app)
        resp = client.post(
            "/ml/research/generate-ideas",
            json={
                "query": "causal inference",
                "papers": [],
                "thoughtSignature": "sig-2",
            },
        )

    assert resp.status_code == 503
    assert "Idea generation failed" in resp.json()["detail"]


def test_generate_research_ideas_endpoint_surfaces_native_structured_runtime_error():
    with patch(
        "routers.ml_router.idea_generation_service.generate_ideas",
        new=AsyncMock(
            side_effect=AiGenerationStructuredOutputRequiresNativeRuntimeError(
                "generate_json requires native structured generation"
            )
        ),
    ):
        app = make_app()
        client = TestClient(app)
        resp = client.post(
            "/ml/research/generate-ideas",
            json={
                "query": "causal inference",
                "papers": [],
                "thoughtSignature": "sig-3",
            },
        )

    assert resp.status_code == 412
    payload = resp.json()["detail"]
    assert payload["code"] == "STRUCTURED_OUTPUT_REQUIRES_NATIVE_RUNTIME"
    assert "native structured generation" in payload["message"]
