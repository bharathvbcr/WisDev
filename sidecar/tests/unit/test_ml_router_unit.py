"""
Unit tests for routers/ml_router.py — covers the BM25 endpoints natively
and mocks the PDF / embedding endpoints.
"""

import pytest
from unittest.mock import AsyncMock, MagicMock, patch
from fastapi import FastAPI
from fastapi.testclient import TestClient

from routers.ml_router import router


@pytest.fixture(scope="module")
def client():
    app = FastAPI()
    app.include_router(router)
    return TestClient(app)


# ---------------------------------------------------------------------------
# /ml/bm25/index
# ---------------------------------------------------------------------------

def test_bm25_index_returns_count(client):
    response = client.post(
        "/ml/bm25/index",
        json={"documents": ["cancer research methods", "deep learning neural networks", "climate change models"]},
    )
    assert response.status_code == 200
    assert response.json()["indexed_count"] == 3


def test_bm25_index_with_doc_ids(client):
    response = client.post(
        "/ml/bm25/index",
        json={
            "documents": ["paper about biology genetics dna", "machine learning classification study"],
            "doc_ids": ["bio-1", "ml-1"],
        },
    )
    assert response.status_code == 200
    assert response.json()["indexed_count"] == 2


def test_bm25_index_empty_docs(client):
    response = client.post("/ml/bm25/index", json={"documents": []})
    assert response.status_code == 200
    assert response.json()["indexed_count"] == 0


# ---------------------------------------------------------------------------
# /ml/bm25/search
# ---------------------------------------------------------------------------

def test_bm25_search_returns_results(client):
    # First index some documents
    client.post(
        "/ml/bm25/index",
        json={
            "documents": [
                "cancer immunotherapy clinical trials treatment outcomes",
                "deep neural network machine learning image recognition",
                "climate change environmental impact global warming arctic",
            ],
            "doc_ids": ["cancer-doc", "ml-doc", "climate-doc"],
        },
    )
    response = client.post(
        "/ml/bm25/search",
        json={"query": "cancer immunotherapy treatment", "top_k": 5},
    )
    assert response.status_code == 200
    data = response.json()
    assert "results" in data
    # Should find at least the cancer doc
    ids = [r["id"] for r in data["results"]]
    assert "cancer-doc" in ids


def test_bm25_search_result_has_expected_keys(client):
    client.post(
        "/ml/bm25/index",
        json={"documents": ["quantum computing quantum bits entanglement superposition"], "doc_ids": ["q-doc"]},
    )
    response = client.post(
        "/ml/bm25/search",
        json={"query": "quantum computing entanglement"},
    )
    assert response.status_code == 200
    results = response.json()["results"]
    if results:
        r = results[0]
        assert "id" in r
        assert "score" in r
        assert "text" in r


def test_bm25_search_empty_when_no_matches(client):
    response = client.post(
        "/ml/bm25/search",
        json={"query": "zzzzthisqueryclearlyhasnoterms"},
    )
    assert response.status_code == 200
    assert "results" in response.json()


# ---------------------------------------------------------------------------
# DELETE /ml/bm25
# ---------------------------------------------------------------------------

def test_bm25_clear(client):
    client.post(
        "/ml/bm25/index",
        json={"documents": ["something to index for clear test"]},
    )
    response = client.delete("/ml/bm25")
    assert response.status_code == 200
    assert response.json()["status"] == "cleared"


# ---------------------------------------------------------------------------
# /ml/pdf  (mocked)
# ---------------------------------------------------------------------------

def test_pdf_invalid_base64_returns_400(client):
    response = client.post(
        "/ml/pdf",
        json={"file_base64": "!!!not-valid-base64!!!", "file_name": "test.pdf"},
    )
    assert response.status_code == 400


def test_pdf_extraction_success(client):
    import base64
    dummy_bytes = b"%PDF-1.4 fake pdf content"
    b64 = base64.b64encode(dummy_bytes).decode()

    with patch(
        "routers.ml_router.extract_pdf_content",
        return_value={"title": "Test Paper", "text": "abstract..."},
    ):
        response = client.post(
            "/ml/pdf",
            json={"file_base64": b64, "file_name": "test.pdf"},
        )
    assert response.status_code == 200
    assert response.json()["title"] == "Test Paper"


def test_pdf_extraction_service_error_returns_502(client):
    import base64
    dummy_bytes = b"%PDF-1.4 fake"
    b64 = base64.b64encode(dummy_bytes).decode()

    with patch(
        "routers.ml_router.extract_pdf_content",
        side_effect=RuntimeError("fitz not available"),
    ):
        response = client.post(
            "/ml/pdf",
            json={"file_base64": b64, "file_name": "test.pdf"},
        )
    assert response.status_code == 502


# ---------------------------------------------------------------------------
# /ml/embed  (mocked)
# ---------------------------------------------------------------------------

def test_embed_success(client):
    with patch(
        "routers.ml_router.embedding_service",
        new_callable=MagicMock,
    ) as mock_svc:
        mock_svc.embed_single_async = AsyncMock(return_value=[0.1] * 768)
        response = client.post("/ml/embed", json={"text": "hello world"})
    assert response.status_code == 200
    assert "embedding" in response.json()
    assert len(response.json()["embedding"]) == 768


def test_embed_service_error_returns_503(client):
    with patch(
        "routers.ml_router.embedding_service",
        new_callable=MagicMock,
    ) as mock_svc:
        mock_svc.embed_single_async = AsyncMock(side_effect=RuntimeError("embedding failed"))
        response = client.post("/ml/embed", json={"text": "hello"})
    assert response.status_code == 503


def test_embed_batch_success(client):
    with patch(
        "routers.ml_router.embedding_service",
        new_callable=MagicMock,
    ) as mock_svc:
        mock_svc.embed_batch_async = AsyncMock(return_value=[[0.1, 0.2], [0.3, 0.4]])
        response = client.post("/ml/embed/batch", json={"texts": ["hello", "world"]})
    assert response.status_code == 200
    assert len(response.json()["embeddings"]) == 2


def test_embed_batch_service_error_returns_503(client):
    with patch(
        "routers.ml_router.embedding_service",
        new_callable=MagicMock,
    ) as mock_svc:
        mock_svc.embed_batch_async = AsyncMock(side_effect=RuntimeError("batch embedding failed"))
        response = client.post("/ml/embed/batch", json={"texts": ["hello", "world"]})
    assert response.status_code == 503


def test_docling_parse_success(client):
    import base64
    dummy_bytes = b"%PDF-1.4 fake"
    b64 = base64.b64encode(dummy_bytes).decode()

    with patch(
        "routers.ml_router._docling_extract",
        return_value={
            "full_text": "# Test Paper",
            "structure_map": [{"label": "Test Paper", "page": 0, "bbox": None}],
            "docling_meta": {"version": "2.3.0+"},
        },
    ):
        response = client.post(
            "/ml/docling/parse",
            json={"file_base64": b64, "file_name": "test.pdf"},
        )
    assert response.status_code == 200
    assert response.json()["fullText"] == "# Test Paper"
    assert response.json()["extractionInfo"]["usedDocling"] is True


def test_docling_parse_returns_503_when_docling_missing(client):
    import base64
    dummy_bytes = b"%PDF-1.4 fake"
    b64 = base64.b64encode(dummy_bytes).decode()

    with patch(
        "routers.ml_router._docling_extract",
        return_value=None,
    ):
        response = client.post(
            "/ml/docling/parse",
            json={"file_base64": b64, "file_name": "test.pdf"},
        )
    assert response.status_code == 503


def test_docling_parse_invalid_base64_returns_400(client):
    response = client.post(
        "/ml/docling/parse",
        json={"file_base64": "!!!not-a-valid-base64!!!", "file_name": "test.pdf"},
    )
    assert response.status_code == 400


def test_docling_parse_service_error_bubbles_up(client):
    import base64
    dummy_bytes = b"%PDF-1.4 fake"
    b64 = base64.b64encode(dummy_bytes).decode()

    with patch(
        "routers.ml_router._docling_extract",
        side_effect=RuntimeError("parser crashed"),
    ):
        with pytest.raises(RuntimeError, match="parser crashed"):
            client.post(
                "/ml/docling/parse",
                json={"file_base64": b64, "file_name": "test.pdf"},
            )
