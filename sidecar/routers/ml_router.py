"""ML worker endpoints — stateless HTTP wrappers called by the Go orchestrator."""
from __future__ import annotations

import base64
import binascii

from typing import List, Optional

from fastapi import APIRouter, HTTPException
from pydantic import BaseModel
from services.pdf_extraction_service import extract_pdf_content
from services.gemini_service import gemini_service
from services.bm25_service import get_bm25_index

router = APIRouter(prefix="/ml", tags=["ml-workers"])


class PdfRequest(BaseModel):
    file_base64: str
    file_name: str = "paper.pdf"


class EmbedRequest(BaseModel):
    text: str


class Bm25IndexRequest(BaseModel):
    documents: List[str]
    doc_ids: Optional[List[str]] = None


class Bm25SearchRequest(BaseModel):
    query: str
    top_k: int = 10


@router.post("/pdf")
async def extract_pdf(req: PdfRequest):
    try:
        file_bytes = base64.b64decode(req.file_base64, validate=True)
    except (binascii.Error, ValueError) as exc:
        raise HTTPException(status_code=400, detail="file_base64 must be valid base64") from exc

    try:
        return extract_pdf_content(file_bytes, req.file_name)
    except Exception as exc:
        raise HTTPException(status_code=502, detail=f"PDF extraction failed: {exc}") from exc


@router.post("/embed")
async def embed_text(req: EmbedRequest):
    try:
        vector = await gemini_service.embed(req.text)
        return {"embedding": vector}
    except Exception as exc:
        raise HTTPException(status_code=503, detail=f"Embedding failed: {exc}") from exc


@router.post("/bm25/index")
async def bm25_index(req: Bm25IndexRequest):
    idx = get_bm25_index()
    count = idx.add_documents(req.documents, req.doc_ids)
    return {"indexed_count": count}


@router.post("/bm25/search")
async def bm25_search(req: Bm25SearchRequest):
    idx = get_bm25_index()
    results = idx.search(req.query, req.top_k)
    return {
        "results": [
            {"id": r[0], "score": r[1], "text": r[2]}
            for r in results
        ]
    }


@router.delete("/bm25")
async def bm25_clear():
    get_bm25_index().clear()
    return {"status": "cleared"}
