from unittest.mock import AsyncMock, patch

import pytest
from fastapi import FastAPI
from fastapi.testclient import TestClient

from routers.azure_compute_router import router, raptor_service, tree_cache
from services.bm25_service import get_bm25_index


@pytest.fixture(autouse=True)
def reset_router_state():
    raptor_service.trees.clear()
    tree_cache.clear()
    get_bm25_index().clear()
    yield
    raptor_service.trees.clear()
    tree_cache.clear()
    get_bm25_index().clear()


@pytest.fixture
def client():
    app = FastAPI()
    app.include_router(router)
    return TestClient(app)


def test_extract_pdf_returns_compatibility_fields(client):
    with patch(
        "routers.azure_compute_router.extract_pdf_content",
        return_value={"full_text": "Recovered text", "pageCount": 2, "paper": {"title": "Paper"}},
    ):
        response = client.post(
            "/extract-pdf",
            files={"file": ("paper.pdf", b"%PDF-1.4", "application/pdf")},
        )

    assert response.status_code == 200
    payload = response.json()
    assert payload["text"] == "Recovered text"
    assert payload["content"] == "Recovered text"
    assert payload["page_count"] == 2
    assert payload["pageCount"] == 2


def test_chunk_and_embed_returns_snake_and_camel_fields(client):
    with patch(
        "routers.azure_compute_router.chunk_with_offsets",
        return_value=[
            {
                "content": "Alpha chunk",
                "char_start": 0,
                "char_end": 11,
                "page": 1,
                "section": "intro",
            }
        ],
    ):
        with patch(
            "routers.azure_compute_router.embedding_service.embed_batch_async",
            new=AsyncMock(return_value=[[0.1, 0.2, 0.3]]),
        ):
            response = client.post(
                "/chunk-and-embed",
                json={"paper_id": "paper-1", "text": "Alpha chunk"},
            )

    assert response.status_code == 200
    payload = response.json()
    assert payload["paper_id"] == "paper-1"
    assert payload["paperId"] == "paper-1"
    assert payload["total_chunks"] == 1
    assert payload["totalChunks"] == 1
    chunk = payload["chunks"][0]
    assert chunk["chunk_id"] == "paper-1_chunk_0"
    assert chunk["chunkId"] == "paper-1_chunk_0"
    assert chunk["char_start"] == 0
    assert chunk["charStart"] == 0
    assert "token_count" in chunk


def test_raptor_routes_build_query_and_cache(client):
    build = client.post(
        "/raptor/build-tree",
        json={
            "papers": [
                {
                    "paper_id": "paper-1",
                    "chunks": [
                        {
                            "chunk_id": "chunk-1",
                            "content": "Alpha evidence.",
                            "embedding": [1.0, 0.0],
                            "char_start": 0,
                            "char_end": 15,
                            "page": 1,
                            "section": "intro",
                        }
                    ],
                }
            ]
        },
    )
    assert build.status_code == 200
    tree_id = build.json()["tree_id"]

    query = client.post(
        "/raptor/query",
        json={
            "tree_id": tree_id,
            "query": "Alpha",
            "query_embedding": [1.0, 0.0],
        },
    )
    assert query.status_code == 200
    assert query.json()["chunks"][0]["chunk_id"] == "chunk-1"
    assert query.json()["chunks"][0]["chunkId"] == "chunk-1"

    cache = client.post("/raptor/cache-status", json={"paper_ids": ["paper-1"]})
    assert cache.status_code == 200
    assert cache.json()["cached"] is True


def test_bm25_root_routes_return_docid_aliases(client):
    index = client.post(
        "/bm25/index",
        json={
            "documents": [
                "cancer immunotherapy clinical trials treatment outcomes",
                "deep neural network machine learning image recognition",
                "climate change environmental impact global warming arctic",
            ],
            "doc_ids": ["d1", "d2", "d3"],
        },
    )
    assert index.status_code == 200

    search = client.post(
        "/bm25/search",
        json={"query": "cancer immunotherapy treatment", "top_k": 5},
    )
    assert search.status_code == 200
    assert search.json()["results"][0]["doc_id"] == "d1"
    assert search.json()["results"][0]["docId"] == "d1"
