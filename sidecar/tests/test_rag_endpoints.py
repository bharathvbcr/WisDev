"""
Tests for the ml_router RAG-adjacent primitives (PDF extraction, embeddings).
Previously this file tested /api/rag/v2/* routes that lived in api_single.py.
Those endpoints migrated to the Go orchestrator; this file now covers the
Python-owned ML worker routes that feed into the Go RAG pipeline.
"""
import base64
from fastapi import FastAPI
from fastapi.testclient import TestClient
from unittest.mock import patch, AsyncMock

from routers.ml_router import router
from limiter_config import limiter

app = FastAPI()
app.include_router(router)
app.state.limiter = limiter
client = TestClient(app)


def _b64(data: bytes) -> str:
    return base64.b64encode(data).decode()


# ── PDF extraction ────────────────────────────────────────────────────────────

@patch("routers.ml_router.extract_pdf_content")
def test_pdf_extract_returns_structured_result(mock_extract):
    mock_extract.return_value = {
        "paper": {"title": "Mock PDF", "authors": []},
        "full_text": "Some text",
        "chunks": [],
    }
    response = client.post("/ml/pdf", json={"file_base64": _b64(b"%PDF-1.4 test"), "file_name": "paper.pdf"})
    assert response.status_code == 200
    assert response.json()["paper"]["title"] == "Mock PDF"
    mock_extract.assert_called_once()


@patch("routers.ml_router.extract_pdf_content")
def test_pdf_extract_passes_filename_to_service(mock_extract):
    mock_extract.return_value = {"paper": {}, "full_text": ""}
    client.post("/ml/pdf", json={"file_base64": _b64(b"%PDF-1.4"), "file_name": "my_paper.pdf"})
    args = mock_extract.call_args[0]
    assert args[1] == "my_paper.pdf"


def test_pdf_extract_rejects_invalid_base64():
    response = client.post("/ml/pdf", json={"file_base64": "not!!valid", "file_name": "x.pdf"})
    assert response.status_code == 400
    assert "base64" in response.json()["detail"].lower()


def test_pdf_extract_missing_required_field():
    response = client.post("/ml/pdf", json={"file_name": "x.pdf"})
    assert response.status_code == 422


@patch("routers.ml_router.extract_pdf_content")
def test_pdf_extract_surfaces_extractor_errors_as_502(mock_extract):
    mock_extract.side_effect = RuntimeError("corrupt PDF")
    response = client.post("/ml/pdf", json={"file_base64": _b64(b"bad"), "file_name": "bad.pdf"})
    assert response.status_code == 502
    assert "corrupt PDF" in response.json()["detail"]


@patch("routers.ml_router.extract_pdf_content")
def test_pdf_extract_default_filename(mock_extract):
    mock_extract.return_value = {"paper": {}, "full_text": ""}
    response = client.post("/ml/pdf", json={"file_base64": _b64(b"%PDF-1.4")})
    assert response.status_code == 200
    args = mock_extract.call_args[0]
    assert args[1] == "paper.pdf"


# ── Embeddings ────────────────────────────────────────────────────────────────

@patch("routers.ml_router.gemini_service")
def test_embed_returns_vector_of_expected_length(mock_gs):
    mock_gs.embed = AsyncMock(return_value=[0.1] * 768)
    response = client.post("/ml/embed", json={"text": "neural networks in medicine"})
    assert response.status_code == 200
    data = response.json()
    assert "embedding" in data
    assert len(data["embedding"]) == 768


def test_embed_missing_text_returns_422():
    response = client.post("/ml/embed", json={})
    assert response.status_code == 422


@patch("routers.ml_router.gemini_service")
def test_embed_surfaces_service_error_as_503(mock_gs):
    mock_gs.embed = AsyncMock(side_effect=Exception("embedding service down"))
    response = client.post("/ml/embed", json={"text": "test"})
    assert response.status_code == 503
    assert "embedding service down" in response.json()["detail"]
