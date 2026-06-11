"""Compatibility routes for the Azure compute container app."""
from __future__ import annotations

import hashlib
from pathlib import Path
from urllib.parse import urlparse

import httpx
from fastapi import APIRouter, Body, File, HTTPException, UploadFile
from fastapi.concurrency import run_in_threadpool

from services.bm25_service import get_bm25_index
from services.chunking_service import chunk_with_offsets, detect_sections
from services.embedding_service import embedding_service
from services.pdf_extraction_service import extract_pdf_content
from services.raptor_service import RaptorService
from services.tree_cache_service import TreeCacheService

router = APIRouter(tags=["azure-compute"])

raptor_service = RaptorService()
tree_cache = TreeCacheService()


def _int_value(payload: dict, *keys: str, default: int) -> int:
    for key in keys:
        if key in payload and payload[key] is not None:
            return int(payload[key])
    return default


def _string_value(payload: dict, *keys: str) -> str:
    for key in keys:
        value = payload.get(key)
        if value is not None and str(value).strip():
            return str(value).strip()
    return ""


def _list_value(payload: dict, *keys: str) -> list:
    for key in keys:
        value = payload.get(key)
        if isinstance(value, list):
            return value
    return []


def _normalize_chunk(chunk: dict) -> dict:
    return {
        "chunk_id": _string_value(chunk, "chunk_id", "chunkId"),
        "content": str(chunk.get("content") or ""),
        "embedding": chunk.get("embedding") or [],
        "char_start": _int_value(chunk, "char_start", "charStart", default=0),
        "char_end": _int_value(chunk, "char_end", "charEnd", default=0),
        "page": _int_value(chunk, "page", default=0),
        "section": str(chunk.get("section") or ""),
    }


def _chunk_response(paper_id: str, index: int, chunk: dict, embedding: list[float]) -> dict:
    chunk_id = chunk.get("chunk_id") or f"{paper_id}_chunk_{index}"
    token_count = embedding_service.count_tokens(str(chunk.get("content") or ""))
    char_start = int(chunk.get("char_start", 0))
    char_end = int(chunk.get("char_end", 0))
    page = int(chunk.get("page", 0))
    section = str(chunk.get("section") or "")
    content = str(chunk.get("content") or "")
    return {
        "chunk_index": index,
        "chunk_id": chunk_id,
        "chunkId": chunk_id,
        "content": content,
        "token_count": token_count,
        "tokenCount": token_count,
        "char_start": char_start,
        "charStart": char_start,
        "char_end": char_end,
        "charEnd": char_end,
        "page": page,
        "section": section,
        "embedding": embedding,
    }


def _build_tree_response(tree_id: str, paper_hash: str, metadata: dict) -> dict:
    levels = int(metadata.get("levels", 1))
    total_nodes = int(metadata.get("total_nodes", 0))
    created_at = str(metadata.get("created_at") or "")
    updated_at = str(metadata.get("updated_at") or "")
    return {
        "tree_id": tree_id,
        "treeId": tree_id,
        "paper_hash": paper_hash,
        "paperHash": paper_hash,
        "levels": levels,
        "total_nodes": total_nodes,
        "totalNodes": total_nodes,
        "created_at": created_at,
        "createdAt": created_at,
        "updated_at": updated_at,
        "updatedAt": updated_at,
    }


def _query_result_payload(result: dict) -> dict:
    chunk_id = str(result.get("chunk_id") or "")
    paper_id = str(result.get("paper_id") or "")
    char_start = int(result.get("char_start", 0))
    char_end = int(result.get("char_end", 0))
    return {
        "chunk_id": chunk_id,
        "chunkId": chunk_id,
        "content": str(result.get("content") or ""),
        "paper_id": paper_id,
        "paperId": paper_id,
        "score": float(result.get("score", 0.0)),
        "level": int(result.get("level", 0)),
        "char_start": char_start,
        "charStart": char_start,
        "char_end": char_end,
        "charEnd": char_end,
        "page": int(result.get("page", 0)),
        "section": str(result.get("section") or ""),
    }


def _paper_hash(paper_ids: list[str]) -> str:
    return hashlib.sha256(":".join(sorted(paper_ids)).encode()).hexdigest()[:16]


def _tree_metadata_is_live(metadata: dict | None) -> bool:
    if not metadata:
        return False
    tree_id = _string_value(metadata, "tree_id", "treeId")
    return bool(tree_id and raptor_service.has_tree(tree_id))


@router.post("/extract-pdf")
async def extract_pdf(
    file: UploadFile | None = File(None),
    request: dict | None = Body(None),
):
    """Extract PDF text from uploaded bytes or a remote URL."""
    try:
        if file is not None:
            content = await file.read()
            file_name = file.filename or "paper.pdf"
        elif request and _string_value(request, "url"):
            pdf_url = _string_value(request, "url")
            async with httpx.AsyncClient(timeout=30.0, follow_redirects=True) as client:
                response = await client.get(pdf_url)
                response.raise_for_status()
                content = response.content
            file_name = (
                _string_value(request, "file_name", "fileName")
                or Path(urlparse(pdf_url).path).name
                or "paper.pdf"
            )
        else:
            raise HTTPException(status_code=400, detail="Provide file upload or URL")
    except httpx.HTTPError as exc:
        raise HTTPException(status_code=502, detail=f"PDF download failed: {exc}") from exc

    try:
        extracted = extract_pdf_content(content, file_name)
        text = str(extracted.get("full_text") or extracted.get("fullText") or "")
        page_count = int(extracted.get("pageCount") or extracted.get("pages") or 0)
        return {
            **extracted,
            "text": text,
            "content": text,
            "page_count": page_count,
            "pageCount": page_count,
            "char_count": len(text),
            "charCount": len(text),
            "page_breaks": extracted.get("page_breaks") or extracted.get("pageBreaks") or [],
            "pageBreaks": extracted.get("pageBreaks") or extracted.get("page_breaks") or [],
        }
    except HTTPException:
        raise
    except Exception as exc:
        raise HTTPException(status_code=500, detail=f"PDF extraction failed: {exc}") from exc


@router.post("/chunk-and-embed")
async def chunk_and_embed(payload: dict = Body(...)):
    """Chunk document text and embed each chunk with Azure-safe fallbacks."""
    paper_id = _string_value(payload, "paper_id", "paperId")
    text = str(payload.get("text") or "")
    if not paper_id or not text.strip():
        raise HTTPException(status_code=422, detail="paper_id and text are required")

    max_chunk_tokens = max(
        100,
        min(2000, _int_value(payload, "max_chunk_tokens", "maxChunkTokens", default=500)),
    )
    overlap_tokens = max(
        0,
        min(500, _int_value(payload, "overlap_tokens", "overlapTokens", default=100)),
    )

    sections = detect_sections(text)
    raw_chunks = await run_in_threadpool(
        chunk_with_offsets,
        text,
        [],
        sections,
        max_chunk_tokens,
        overlap_tokens,
    )

    try:
        embeddings = await embedding_service.embed_batch_async([chunk["content"] for chunk in raw_chunks])
    except Exception as exc:
        raise HTTPException(status_code=503, detail=f"Embedding failed: {exc}") from exc

    chunks = [
        _chunk_response(
            paper_id,
            index,
            {**chunk, "chunk_id": f"{paper_id}_chunk_{index}"},
            embedding,
        )
        for index, (chunk, embedding) in enumerate(zip(raw_chunks, embeddings))
    ]

    return {
        "paper_id": paper_id,
        "paperId": paper_id,
        "chunks": chunks,
        "total_chunks": len(chunks),
        "totalChunks": len(chunks),
    }


@router.post("/raptor/build-tree")
async def build_raptor_tree(payload: dict = Body(...)):
    papers_payload = _list_value(payload, "papers")
    if not papers_payload:
        raise HTTPException(status_code=422, detail="papers is required")

    papers = []
    paper_ids: list[str] = []
    for paper in papers_payload:
        if not isinstance(paper, dict):
            continue
        paper_id = _string_value(paper, "paper_id", "paperId")
        if not paper_id:
            continue
        paper_ids.append(paper_id)
        papers.append(
            {
                "paper_id": paper_id,
                "chunks": [
                    _normalize_chunk(chunk)
                    for chunk in _list_value(paper, "chunks")
                    if isinstance(chunk, dict)
                ],
            }
        )

    if not papers:
        raise HTTPException(status_code=422, detail="No valid papers supplied")

    raw_config = payload.get("config")
    config: dict = raw_config if isinstance(raw_config, dict) else {}
    min_clusters = max(2, _int_value(config, "min_clusters", "minClusters", default=3))
    max_levels = max(2, min(6, _int_value(config, "max_levels", "maxLevels", default=4)))
    paper_hash = _paper_hash(paper_ids)

    cached = tree_cache.get(paper_hash)
    if _tree_metadata_is_live(cached):
        return cached

    metadata = await run_in_threadpool(
        raptor_service.build_adaptive_tree,
        papers,
        min_clusters,
        max_levels,
    )
    tree_id = _string_value(metadata, "tree_id", "treeId")
    response = _build_tree_response(tree_id, paper_hash, metadata)
    tree_cache.put(paper_hash, response)
    return response


@router.post("/raptor/query")
async def query_raptor_tree(payload: dict = Body(...)):
    tree_id = _string_value(payload, "tree_id", "treeId")
    query = _string_value(payload, "query")
    if not tree_id or not query:
        raise HTTPException(status_code=422, detail="tree_id and query are required")

    query_embedding = payload.get("query_embedding") or payload.get("queryEmbedding")
    if not isinstance(query_embedding, list):
        try:
            query_embedding = await embedding_service.embed_single_async(query)
        except Exception as exc:
            raise HTTPException(status_code=503, detail=f"Query embedding failed: {exc}") from exc

    top_k = max(1, min(50, _int_value(payload, "top_k", "topK", default=10)))
    levels = _list_value(payload, "levels")
    results = await run_in_threadpool(
        raptor_service.query_tree,
        tree_id,
        query_embedding,
        top_k,
        levels or None,
    )

    chunks = [_query_result_payload(result) for result in results]
    context = "\n\n---\n\n".join(
        f"[{chunk['section'] or 'chunk'}] {chunk['content']}" for chunk in chunks[:5]
    )
    return {"chunks": chunks, "context": context}


@router.post("/raptor/update-tree")
async def update_raptor_tree(payload: dict = Body(...)):
    tree_id = _string_value(payload, "tree_id", "treeId")
    new_papers_payload = _list_value(payload, "new_papers", "newPapers")
    if not tree_id or not new_papers_payload:
        raise HTTPException(status_code=422, detail="tree_id and new_papers are required")

    new_papers = []
    paper_ids: list[str] = []
    for paper in new_papers_payload:
        if not isinstance(paper, dict):
            continue
        paper_id = _string_value(paper, "paper_id", "paperId")
        if not paper_id:
            continue
        paper_ids.append(paper_id)
        new_papers.append(
            {
                "paper_id": paper_id,
                "chunks": [
                    _normalize_chunk(chunk)
                    for chunk in _list_value(paper, "chunks")
                    if isinstance(chunk, dict)
                ],
            }
        )

    metadata = await run_in_threadpool(raptor_service.incremental_update, tree_id, new_papers)
    resolved_tree_id = _string_value(metadata, "tree_id", "treeId") or tree_id
    return _build_tree_response(resolved_tree_id, _paper_hash(paper_ids), metadata)


@router.post("/raptor/cache-status")
async def cache_status(payload: dict = Body(...)):
    paper_ids = [str(value) for value in _list_value(payload, "paper_ids", "paperIds") if str(value).strip()]
    if not paper_ids:
        raise HTTPException(status_code=422, detail="paper_ids is required")

    paper_hash = _paper_hash(paper_ids)
    cached = tree_cache.get(paper_hash)
    if _tree_metadata_is_live(cached):
        return {"cached": True, **cached}

    return {
        "cached": False,
        "paper_hash": paper_hash,
        "paperHash": paper_hash,
    }


@router.post("/bm25/index")
async def bm25_index(payload: dict = Body(...)):
    documents = [str(doc) for doc in _list_value(payload, "documents")]
    doc_ids = [str(doc_id) for doc_id in _list_value(payload, "doc_ids", "docIds")] or None
    indexed_count = get_bm25_index().add_documents(documents, doc_ids)
    return {
        "status": "indexed",
        "indexed_count": indexed_count,
        "indexedCount": indexed_count,
        "document_count": indexed_count,
        "documentCount": indexed_count,
    }


@router.post("/bm25/search")
async def bm25_search(payload: dict = Body(...)):
    query = _string_value(payload, "query")
    top_k = max(1, min(100, _int_value(payload, "top_k", "topK", default=10)))
    results = get_bm25_index().search(query, top_k)
    return {
        "results": [
            {
                "doc_id": doc_id,
                "docId": doc_id,
                "score": score,
                "text": text,
            }
            for doc_id, score, text in results
        ]
    }


@router.delete("/bm25")
async def bm25_clear():
    get_bm25_index().clear()
    return {"status": "cleared"}
