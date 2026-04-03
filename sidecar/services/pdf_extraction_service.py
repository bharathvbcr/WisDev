"""
PDF Extraction Service

Extracts text, metadata, and lightweight structural hints from uploaded PDFs.
The service is intentionally self-contained so the Go orchestrator can call the
sidecar without depending on any legacy HTTP router surface.
"""

from __future__ import annotations

import importlib
import io
import os
import re
from typing import Any

CHUNK_SIZE_CHARS = 3000
CHUNK_OVERLAP_CHARS = 200
MAX_CHUNKS = 500


def extract_year(text: str) -> int | None:
    patterns = [
        r"(?<!\d)(20[0-2]\d)(?!\d)",
        r"(?<!\d)(19[8-9]\d)(?!\d)",
        r"Published[:\s]+(\d{4})",
        r"Copyright[:\s]+(\d{4})",
        r"\((\d{4})\)",
    ]
    for pattern in patterns:
        match = re.search(pattern, text, re.IGNORECASE)
        if match:
            year = int(match.group(1))
            if 1950 <= year <= 2030:
                return year
    return None


def extract_doi(text: str) -> str | None:
    match = re.search(r"\b(10\.\d{4,}(?:\.\d+)*\/\S+)", text, re.IGNORECASE)
    return match.group(1).rstrip(".") if match else None


def chunk_text_chars(text: str) -> list[dict[str, Any]]:
    chunks: list[dict[str, Any]] = []
    if not text:
        return chunks

    start = 0
    index = 0
    while start < len(text):
        end = min(start + CHUNK_SIZE_CHARS, len(text))
        chunk_text = text[start:end]
        if chunk_text:
            chunks.append(
                {
                    "index": index,
                    "text": chunk_text,
                    "charCount": len(chunk_text),
                }
            )
        if end >= len(text):
            break
        start = max(0, end - CHUNK_OVERLAP_CHARS)
        index += 1
        if index >= MAX_CHUNKS:
            break
    return chunks


def _fast_regex_extract(text_content: str, file_name: str) -> dict[str, Any]:
    year = extract_year(text_content) or extract_year(file_name)
    doi = extract_doi(text_content)
    title = re.sub(r"\.pdf$", "", file_name, flags=re.IGNORECASE)
    title = title.replace("_", " ").strip()
    return {
        "title": title,
        "doi": doi,
        "year": year,
    }


def _llm_fallback_extract(first_page_text: str) -> dict[str, Any]:
    try:
        import textwrap

        lx = importlib.import_module("langextract")
    except Exception:
        return {}

    prompt = textwrap.dedent(
        """\
        Extract the title, authors, abstract, publication year, and DOI from this academic paper's first page.
        Use exact text for extractions where possible. Do not paraphrase or overlap entities.
        For authors, extract individual names.
        """
    )

    examples = [
        lx.data.ExampleData(
            text=(
                "Attention Is All You Need\nAshish Vaswani, Noam Shazeer\nAbstract\n"
                "The dominant sequence transduction models are based on complex recurrent "
                "or convolutional neural networks..."
            ),
            extractions=[
                lx.data.Extraction(
                    extraction_class="title",
                    extraction_text="Attention Is All You Need",
                    attributes={},
                ),
                lx.data.Extraction(
                    extraction_class="author",
                    extraction_text="Ashish Vaswani",
                    attributes={},
                ),
                lx.data.Extraction(
                    extraction_class="author",
                    extraction_text="Noam Shazeer",
                    attributes={},
                ),
                lx.data.Extraction(
                    extraction_class="abstract",
                    extraction_text=(
                        "The dominant sequence transduction models are based on complex "
                        "recurrent or convolutional neural networks..."
                    ),
                    attributes={},
                ),
            ],
        )
    ]

    try:
        safe_text = first_page_text[:4000]
        if not safe_text.strip():
            return {}

        model_id = os.getenv("AI_MODEL_STANDARD_ID", "gemini-2.5-flash")
        result = lx.extract(
            text_or_documents=safe_text,
            prompt_description=prompt,
            examples=examples,
            model_id=model_id,
        )

        extracted: dict[str, Any] = {
            "title": None,
            "authors": [],
            "abstract": None,
            "year": None,
            "doi": None,
        }

        if result and hasattr(result, "extractions"):
            for ext in result.extractions:
                value = ext.extraction_text.strip()
                if ext.extraction_class == "title" and not extracted["title"]:
                    extracted["title"] = value
                elif ext.extraction_class == "author":
                    extracted["authors"].append(value)
                elif ext.extraction_class == "abstract" and not extracted["abstract"]:
                    extracted["abstract"] = value
                elif ext.extraction_class == "year" and not extracted["year"]:
                    try:
                        extracted["year"] = int(value)
                    except ValueError:
                        extracted["year"] = extract_year(value)
                elif ext.extraction_class == "doi" and not extracted["doi"]:
                    extracted["doi"] = value

        return extracted
    except Exception:
        return {}


def _normalize_title(file_name: str) -> str:
    title = re.sub(r"\.pdf$", "", file_name, flags=re.IGNORECASE)
    title = title.replace("_", " ").strip()
    title = re.sub(r"\s+", " ", title)
    return title


def _extract_pdf_text(file_bytes: bytes) -> tuple[str, str, int, list[dict[str, Any]], bool]:
    text_content = ""
    first_page_text = ""
    pages_count = 0
    blocks: list[dict[str, Any]] = []
    used_pymupdf = False

    try:
        try:
            import fitz

            doc = fitz.open(stream=file_bytes, filetype="pdf")
            pages_count = len(doc)
            for page_index, page in enumerate(doc):
                try:
                    page_dict = page.get_text("dict")
                except TypeError:
                    page_text = page.get_text() or ""
                    if page_index == 0:
                        first_page_text = page_text
                    text_content += page_text + "\n"
                    continue
                page_text = ""
                if isinstance(page_dict, str):
                    page_text = page_dict
                    if page_index == 0:
                        first_page_text = page_text
                    text_content += page_text + "\n"
                    continue
                for block in page_dict.get("blocks", []):
                    if block.get("type") != 0:
                        continue
                    block_text = ""
                    for line in block.get("lines", []):
                        for span in line.get("spans", []):
                            block_text += span.get("text", "") + " "
                    block_text = block_text.strip()
                    if block_text:
                        blocks.append(
                            {
                                "page": page_index,
                                "text": block_text,
                                "bbox": block.get("bbox"),
                            }
                        )
                        page_text += block_text + "\n"
                if page_index == 0:
                    first_page_text = page_text
                text_content += page_text + "\n"
            used_pymupdf = True
        except ImportError:
            import pypdf

            reader = pypdf.PdfReader(io.BytesIO(file_bytes))
            pages_count = len(reader.pages)
            for page_index, page in enumerate(reader.pages):
                page_text = page.extract_text() or ""
                if page_index == 0:
                    first_page_text = page_text
                text_content += page_text + "\n"
    except Exception:
        return "", "", 0, [], False

    text_content = re.sub(r"(\n\s*)+\n", "\n\n", text_content).strip()
    first_page_text = first_page_text.strip()
    return text_content, first_page_text, pages_count, blocks, used_pymupdf


def extract_pdf_content(file_bytes: bytes, file_name: str) -> dict[str, Any]:
    """
    Extract PDF content and return both normalized and legacy response keys.

    The response keeps compatibility with existing callers by exposing:
    - `full_text` for the old HTTP route contract
    - `fullText` for newer consumers
    - `pageCount` and `pages` for page counts
    """
    text_content, first_page_text, pages_count, blocks, used_pymupdf = _extract_pdf_text(file_bytes)
    fast_meta = _fast_regex_extract(text_content or file_name, file_name)

    structure_map: list[dict[str, Any]] = []
    section_patterns = [
        (r"(?i)^abstract", "abstract"),
        (r"(?i)^introduction", "introduction"),
        (r"(?i)^methodology|^methods", "methodology"),
        (r"(?i)^results", "results"),
        (r"(?i)^discussion", "discussion"),
        (r"(?i)^conclusion", "conclusion"),
        (r"(?i)^references", "references"),
    ]
    for block in blocks:
        text = block["text"].strip()
        for pattern, label in section_patterns:
            if re.match(pattern, text):
                structure_map.append(
                    {
                        "label": label,
                        "page": block["page"],
                        "bbox": block["bbox"],
                    }
                )
                break

    needs_llm = not fast_meta.get("doi") or not fast_meta.get("year") or len(fast_meta.get("title", "")) < 5
    title = fast_meta.get("title") or _normalize_title(file_name)
    doi = fast_meta.get("doi")
    year = fast_meta.get("year")
    authors: list[str] = []
    abstract = None
    used_llm_fallback = False

    if needs_llm and first_page_text:
        llm_meta = _llm_fallback_extract(first_page_text)
        if llm_meta:
            used_llm_fallback = True
            title = llm_meta.get("title") or title
            doi = llm_meta.get("doi") or doi
            year = llm_meta.get("year") or year
            authors = llm_meta.get("authors") or authors
            abstract = llm_meta.get("abstract") or abstract

    chunks = chunk_text_chars(text_content)
    paper = {
        "title": title,
        "doi": doi,
        "publishDate": {"year": year} if year else None,
        "authors": authors or None,
        "abstract": abstract,
        "sourceApis": ["pdf_upload"],
    }

    return {
        "paper": paper,
        "fullText": text_content,
        "full_text": text_content,
        "structureMap": structure_map,
        "blocks": blocks[:100],
        "chunks": chunks,
        "pageCount": pages_count,
        "pages": pages_count,
        "extractionInfo": {
            "fileName": file_name,
            "usedPyMuPDF": used_pymupdf,
            "usedLlmFallback": used_llm_fallback,
        },
    }
