"""Additional tests for routers/azure_compute_router.py branch coverage."""

from __future__ import annotations

from unittest.mock import AsyncMock, MagicMock, patch

import httpx
import pytest
from fastapi import FastAPI, HTTPException
from fastapi.testclient import TestClient

from routers.azure_compute_router import (
    _build_tree_response,
    _int_value,
    _list_value,
    _normalize_chunk,
    _paper_hash,
    _query_result_payload,
    _string_value,
    _tree_metadata_is_live,
    router,
    extract_pdf,
    raptor_service,
    tree_cache,
)


@pytest.fixture(autouse=True)
def reset_state():
    raptor_service.trees.clear()
    tree_cache.clear()
    yield
    raptor_service.trees.clear()
    tree_cache.clear()


@pytest.fixture
def client():
    app = FastAPI()
    app.include_router(router)
    return TestClient(app)


def test_int_and_string_and_list_helpers():
    payload = {"a": "7", "b": "", "c": None, "d": 3, "list": ["x"]}
    assert _int_value(payload, "missing", "a", default=0) == 7
    assert _string_value(payload, "missing", "b", "c") == ""
    assert _string_value({"a": "   x "}, "a") == "x"
    assert _list_value({"x": "nope"}, "x") == []
    assert _list_value(payload, "list", "x") == ["x"]


def test_normalize_chunk_defaults_and_aliases():
    chunk = _normalize_chunk(
        {
            "chunkId": "c1",
            "char_start": 1,
            "page": 2,
            "section": "abstract",
            "embedding": [1, 2, 3],
            "content": "some text",
        }
    )
    assert chunk["chunk_id"] == "c1"
    assert chunk["char_end"] == 0
    assert chunk["section"] == "abstract"


def test_query_result_payload_casts_fields():
    payload = _query_result_payload(
        {
            "chunk_id": "x",
            "paper_id": "p1",
            "content": None,
            "score": 0.5,
            "level": "2",
            "char_start": "3",
            "page": "4",
        }
    )
    assert payload["chunkId"] == "x"
    assert payload["paperId"] == "p1"
    assert payload["score"] == 0.5
    assert payload["level"] == 2
    assert payload["charStart"] == 3


def test_build_tree_response_has_dual_keys():
    response = _build_tree_response("tree-1", "paper-hash", {"levels": 3, "total_nodes": 11})
    assert response["treeId"] == "tree-1"
    assert response["paperHash"] == "paper-hash"
    assert response["totalNodes"] == 11


def test_tree_metadata_is_live_checks_raptor_registry():
    assert _tree_metadata_is_live({}) is False
    raptor_service.trees["known"] = {}
    assert _tree_metadata_is_live({"tree_id": "known"}) is True


def test_paper_hash_is_deterministic_and_stable():
    value = _paper_hash(["z", "a", "b"])
    assert value == _paper_hash(["b", "a", "z"])


def test_extract_pdf_rejects_empty_request(client):
    response = client.post("/extract-pdf", json={})
    assert response.status_code == 400


def test_extract_pdf_extractor_http_exception_is_forwarded(client):
    with patch(
        "routers.azure_compute_router.extract_pdf_content",
        side_effect=HTTPException(status_code=500, detail="extractor failure"),
    ):
        response = client.post(
            "/extract-pdf",
            files={"file": ("paper.pdf", b"%PDF-1.4", "application/pdf")},
        )

    assert response.status_code == 500
    assert response.json()["detail"] == "extractor failure"


def test_extract_pdf_extractor_generic_exception_returns_500(client):
    with patch(
        "routers.azure_compute_router.extract_pdf_content",
        side_effect=RuntimeError("extractor crashed"),
    ):
        response = client.post(
            "/extract-pdf",
            files={"file": ("paper.pdf", b"%PDF-1.4", "application/pdf")},
        )

    assert response.status_code == 500
    assert "PDF extraction failed" in response.json()["detail"]


@pytest.mark.asyncio
async def test_extract_pdf_from_url_uses_httpx():
    with patch("routers.azure_compute_router.httpx.AsyncClient") as mock_client_cls:
        mock_client = AsyncMock()
        mock_response = MagicMock()
        mock_response.content = b"bytes"
        mock_response.raise_for_status = MagicMock()
        mock_client.get = AsyncMock(return_value=mock_response)
        mock_client.__aenter__ = AsyncMock(return_value=mock_client)
        mock_client.__aexit__ = AsyncMock(return_value=False)
        mock_client_cls.return_value = mock_client

        with patch(
            "routers.azure_compute_router.extract_pdf_content",
            return_value={"full_text": "doc", "pageCount": 1, "paper": {"title": "ok"}},
        ):
            response = await extract_pdf(
                file=None,
                request={"url": "https://example.com/sample.pdf"},
            )

    assert response["text"] == "doc"


@pytest.mark.asyncio
async def test_extract_pdf_from_url_with_http_error_returns_502():
    with patch("routers.azure_compute_router.httpx.AsyncClient") as mock_client_cls:
        mock_client = AsyncMock()
        mock_client.get = AsyncMock(side_effect=httpx.HTTPError("download failed"))
        mock_client.__aenter__ = AsyncMock(return_value=mock_client)
        mock_client.__aexit__ = AsyncMock(return_value=False)
        mock_client_cls.return_value = mock_client
        with pytest.raises(HTTPException) as exc:
            await extract_pdf(file=None, request={"url": "https://example.com/sample.pdf"})

    assert exc.value.status_code == 502


def test_chunk_and_embed_requires_text_and_paper_id(client):
    response = client.post("/chunk-and-embed", json={"paper_id": "p"})
    assert response.status_code == 422


def test_chunk_and_embed_surface_embedding_failure(client):
    with patch(
        "routers.azure_compute_router.chunk_with_offsets",
        return_value=[{"content": "bad", "char_start": 0, "char_end": 3}],
    ):
        with patch(
            "routers.azure_compute_router.embedding_service.embed_batch_async",
            AsyncMock(side_effect=RuntimeError("embedding unavailable")),
        ):
            response = client.post("/chunk-and-embed", json={"paper_id": "p", "text": "bad"})

    assert response.status_code == 503


def test_build_tree_rejects_invalid_papers_payload(client):
    response = client.post("/raptor/build-tree", json={"papers": [1, 2, 3]})
    assert response.status_code == 422


def test_build_tree_requires_papers_key(client):
    response = client.post("/raptor/build-tree", json={})
    assert response.status_code == 422
    assert response.json()["detail"] == "papers is required"


def test_raptor_query_requires_tree_id_and_query(client):
    response = client.post("/raptor/query", json={"query": "anything"})
    assert response.status_code == 422

    response = client.post("/raptor/query", json={"tree_id": "abc"})
    assert response.status_code == 422


def test_build_tree_uses_cached_tree_when_available(client):
    paper_hash = _paper_hash(["paper-a"])
    cached_tree = _build_tree_response(
        "tree-cached-1",
        paper_hash,
        {"levels": 2, "total_nodes": 3, "created_at": "2020", "updated_at": "2020"},
    )
    tree_cache.put(paper_hash, cached_tree)
    raptor_service.trees["tree-cached-1"] = {}

    response = client.post(
        "/raptor/build-tree",
        json={"papers": [{"paper_id": "paper-a", "chunks": []}]},
    )
    assert response.status_code == 200
    payload = response.json()
    assert payload["treeId"] == "tree-cached-1"
    assert payload["paperHash"] == paper_hash
    assert payload["tree_id"] == "tree-cached-1"


def test_raptor_query_generates_embedding_when_missing_embedding(client):
    with patch(
        "routers.azure_compute_router.embedding_service.embed_single_async",
        AsyncMock(return_value=[0.25, 0.75]),
    ) as mock_embed:
        with patch(
            "routers.azure_compute_router.run_in_threadpool",
            new=AsyncMock(side_effect=lambda fn, *args, **kwargs: fn(*args, **kwargs)),
        ):
            with patch(
                "routers.azure_compute_router.raptor_service.query_tree",
                return_value=[
                    {
                        "chunk_id": "chunk-1",
                        "paper_id": "paper-1",
                        "content": "query result",
                        "score": 0.9,
                        "level": "2",
                        "char_start": "5",
                        "char_end": "7",
                        "page": "3",
                    }
                ],
            ):
                response = client.post(
                    "/raptor/query",
                    json={"tree_id": "tree-1", "query": "anything"},
                )

    assert response.status_code == 200
    payload = response.json()
    assert payload["chunks"][0]["chunkId"] == "chunk-1"
    assert payload["chunks"][0]["charStart"] == 5
    assert mock_embed.await_count == 1


def test_build_tree_with_only_invalid_papers_is_rejected(client):
    response = client.post(
        "/raptor/build-tree",
        json={"papers": [{}, 1, 2, 3]},
    )
    assert response.status_code == 422
    assert response.json()["detail"] == "No valid papers supplied"


def test_raptor_query_fails_when_query_embedding_generation_fails(client):
    with patch("routers.azure_compute_router.embedding_service.embed_single_async", AsyncMock(side_effect=RuntimeError("bad"))):
        response = client.post(
            "/raptor/query",
            json={"tree_id": "missing", "query": "anything"},
        )
    assert response.status_code == 503


def test_update_tree_requires_tree_id_and_new_papers(client):
    response = client.post("/raptor/update-tree", json={"tree_id": "t-1"})
    assert response.status_code == 422

    response = client.post("/raptor/update-tree", json={"new_papers": [{"paper_id": "p", "chunks": []}]})
    assert response.status_code == 422


def test_update_tree_skips_invalid_entries_and_missing_paper_ids(client):
    with patch(
        "routers.azure_compute_router.raptor_service.incremental_update",
        return_value={"tree_id": "tree-updated", "levels": 1, "total_nodes": 1},
    ) as update_mock:
        with patch(
            "routers.azure_compute_router.run_in_threadpool",
            new=AsyncMock(side_effect=lambda fn, *args, **kwargs: fn(*args, **kwargs)),
        ):
            response = client.post(
                "/raptor/update-tree",
                json={
                    "tree_id": "tree-1",
                    "new_papers": [
                        {},
                        1,
                        {"paper_id": "paper-new", "chunks": []},
                    ],
                },
            )

    assert response.status_code == 200
    forwarded_papers = update_mock.call_args.args[1]
    assert forwarded_papers == [{"paper_id": "paper-new", "chunks": []}]


def test_update_tree_builds_payload_with_incremental_update(client):
    with patch(
        "routers.azure_compute_router.raptor_service.incremental_update",
        return_value={"tree_id": "tree-updated", "levels": 2, "total_nodes": 7},
    ):
        with patch(
            "routers.azure_compute_router.run_in_threadpool",
            new=AsyncMock(side_effect=lambda fn, *args, **kwargs: fn(*args, **kwargs)),
        ):
            response = client.post(
                "/raptor/update-tree",
                json={
                    "tree_id": "tree-1",
                    "new_papers": [
                        {
                            "paper_id": "paper-new",
                            "chunks": [
                                {
                                    "chunk_id": "chunk-new",
                                    "content": "abc",
                                    "embedding": [1.0],
                                    "char_start": 1,
                                    "char_end": 4,
                                }
                            ],
                        }
                    ],
                },
            )

    assert response.status_code == 200
    payload = response.json()
    assert payload["treeId"] == "tree-updated"
    assert payload["totalNodes"] == 7
    assert payload["levels"] == 2


def test_cache_status_reports_not_cached(client):
    response = client.post("/raptor/cache-status", json={"paper_ids": ["unknown"]})
    assert response.status_code == 200
    payload = response.json()
    assert payload["cached"] is False
    assert payload["paperHash"] == _paper_hash(["unknown"])


def test_cache_status_requires_non_empty_paper_ids(client):
    response = client.post("/raptor/cache-status", json={"paper_ids": ["", "   "]})
    assert response.status_code == 422
    assert response.json()["detail"] == "paper_ids is required"


def test_bm25_clear_clears_index(client):
    response = client.delete("/bm25")
    assert response.status_code == 200
    assert response.json()["status"] == "cleared"
