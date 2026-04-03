from unittest.mock import patch
import base64
import pytest
from fastapi.testclient import TestClient
from fastapi import FastAPI

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
    with patch("routers.ml_router.gemini_service") as mock_svc:
        mock_svc.embed = fake_embed
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
