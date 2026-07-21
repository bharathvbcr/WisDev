"""ML worker endpoints — stateless HTTP wrappers called by the Go orchestrator."""
from __future__ import annotations

import base64
import binascii

from typing import Any, List, Optional

from fastapi import APIRouter, HTTPException
from pydantic import BaseModel, Field, ConfigDict
from services.ai_generation_service import (
    AiGenerationStructuredOutputRequiresNativeRuntimeError,
)
from services.embedding_service import embedding_service
from services.idea_generation_service import idea_generation_service
from services.pdf_extraction_service import extract_pdf_content, _docling_extract
from services.bm25_service import get_bm25_index

router = APIRouter(prefix="/ml", tags=["ml-workers"])


class PdfRequest(BaseModel):
    model_config = ConfigDict(populate_by_name=True)
    file_base64: str = Field(..., alias="fileBase64")
    file_name: str = Field("paper.pdf", alias="fileName")


class EmbedRequest(BaseModel):
    text: str


class EmbedBatchRequest(BaseModel):
    texts: List[str] = Field(..., min_length=1)


class DoclingParseRequest(BaseModel):
    model_config = ConfigDict(populate_by_name=True)
    file_base64: str = Field(..., alias="fileBase64")
    file_name: str = Field("document.pdf", alias="fileName")


class Bm25IndexRequest(BaseModel):
    model_config = ConfigDict(populate_by_name=True)
    documents: List[str]
    doc_ids: Optional[List[str]] = Field(None, alias="docIds")


class Bm25SearchRequest(BaseModel):
    model_config = ConfigDict(populate_by_name=True)
    query: str
    top_k: int = Field(10, alias="topK")


class GenerateIdeasRequest(BaseModel):
    model_config = ConfigDict(populate_by_name=True)
    query: str
    papers: List[dict[str, Any]] = Field(default_factory=list)
    thought_signature: str = Field("", alias="thoughtSignature")


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
        vector = await embedding_service.embed_single_async(req.text)
        return {"embedding": vector}
    except Exception as exc:
        raise HTTPException(status_code=503, detail=f"Embedding failed: {exc}") from exc


@router.post("/embed/batch")
async def embed_text_batch(req: EmbedBatchRequest):
    try:
        vectors = await embedding_service.embed_batch_async(req.texts)
        return {"embeddings": vectors}
    except Exception as exc:
        raise HTTPException(status_code=503, detail=f"Batch embedding failed: {exc}") from exc


@router.post("/docling/parse")
async def docling_parse(req: DoclingParseRequest):
    try:
        file_bytes = base64.b64decode(req.file_base64, validate=True)
    except (binascii.Error, ValueError) as exc:
        raise HTTPException(status_code=400, detail="file_base64 must be valid base64") from exc

    parsed = _docling_extract(file_bytes, req.file_name)
    if not parsed:
        raise HTTPException(status_code=503, detail="Docling parsing unavailable")

    full_text = parsed.get("full_text", "")
    structure_map = parsed.get("structure_map", []) or []
    sections = [
        {
            "label": item.get("label", ""),
            "page": item.get("page", 0),
            "bbox": item.get("bbox"),
        }
        for item in structure_map
    ]
    return {
        "paper": {
            "fileName": req.file_name,
            "title": req.file_name,
        },
        "fullText": full_text,
        "full_text": full_text,
        "sections": sections,
        "figures": [],
        "tables": [],
        "references": [],
        "structureMap": structure_map,
        "structure_map": structure_map,
        "doclingMeta": parsed.get("docling_meta", {}),
        "docling_meta": parsed.get("docling_meta", {}),
        "extractionInfo": {
            "usedDocling": True,
        },
    }


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


@router.post("/research/generate-ideas")
async def generate_research_ideas(req: GenerateIdeasRequest):
    try:
        result = await idea_generation_service.generate_ideas(
            query=req.query,
            literature=req.papers,
            thought_signature=req.thought_signature,
        )
        return result.model_dump(by_alias=True)
    except AiGenerationStructuredOutputRequiresNativeRuntimeError as exc:
        raise HTTPException(
            status_code=412,
            detail={
                "code": "STRUCTURED_OUTPUT_REQUIRES_NATIVE_RUNTIME",
                "message": f"Idea generation requires native structured generation: {exc}",
            },
        ) from exc
    except Exception as exc:
        raise HTTPException(status_code=503, detail=f"Idea generation failed: {exc}") from exc
