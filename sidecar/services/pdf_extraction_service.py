"""
PDF Extraction Service

Extracts text, metadata, and lightweight structural hints from uploaded PDFs.
The service is intentionally self-contained so the Go orchestrator can call the
sidecar without depending on HTTP router internals.
"""

from __future__ import annotations

import importlib
import io
import re
from typing import Any

from services.model_config import resolve_generation_model_id

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

        model_id = resolve_generation_model_id("standard")
        if not model_id:
            return {}
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


def _cell_to_string(value: Any) -> str:
    if value is None:
        return ""
    return str(value).strip()


def _element_page(element: Any) -> int:
    try:
        prov = getattr(element, "prov", None)
        if not prov:
            return 0
        first = prov[0] if isinstance(prov, (list, tuple)) else prov
        page = getattr(first, "page_no", None)
        if page is None:
            page = getattr(first, "page", None)
        if page is not None:
            return int(page)
    except Exception:
        pass
    return 0


def _table_from_dataframe(df: Any) -> tuple[list[str], list[list[str]]]:
    headers = [_cell_to_string(col) for col in df.columns.tolist()]
    rows = [
        [_cell_to_string(value) for value in row]
        for row in df.values.tolist()
    ]
    return headers, rows


def _table_from_grid(data: Any) -> tuple[list[str], list[list[str]]]:
    if not data:
        return [], []
    grid = [
        [_cell_to_string(cell) for cell in row]
        for row in data
        if isinstance(row, (list, tuple))
    ]
    if not grid:
        return [], []
    if len(grid) == 1:
        return grid[0], []
    return grid[0], grid[1:]


def _is_docling_table_element(element: Any) -> bool:
    label = getattr(element, "label", None)
    elem_type = getattr(element, "type", None) or getattr(element, "element_type", None)
    label_str = str(label).lower() if label is not None else ""
    type_str = str(elem_type).lower() if elem_type is not None else ""
    return "table" in label_str or type_str == "table" or type_str.endswith(".table")


def _extract_table_from_docling_element(element: Any) -> dict[str, Any] | None:
    if not _is_docling_table_element(element):
        return None

    try:
        headers: list[str] = []
        rows: list[list[str]] = []

        export_df = getattr(element, "export_to_dataframe", None)
        if callable(export_df):
            headers, rows = _table_from_dataframe(export_df())
        elif hasattr(element, "data") and getattr(element, "data") is not None:
            headers, rows = _table_from_grid(getattr(element, "data"))
        elif hasattr(element, "num_rows") and hasattr(element, "num_cols"):
            num_rows = int(getattr(element, "num_rows", 0) or 0)
            num_cols = int(getattr(element, "num_cols", 0) or 0)
            grid: list[list[str]] = []
            for row_idx in range(num_rows):
                row_cells: list[str] = []
                for col_idx in range(num_cols):
                    cell = None
                    if hasattr(element, "get_cell"):
                        cell = element.get_cell(row_idx, col_idx)
                    elif hasattr(element, "cell_at"):
                        cell = element.cell_at(row_idx, col_idx)
                    row_cells.append(_cell_to_string(cell))
                grid.append(row_cells)
            headers, rows = _table_from_grid(grid)

        if not headers and not rows:
            return None

        title = (
            getattr(element, "caption", None)
            or getattr(element, "title", None)
            or getattr(element, "text", None)
            or getattr(element, "label", None)
        )
        title_str = _cell_to_string(title) or "Table"

        return {
            "type": "table",
            "label": title_str,
            "title": title_str,
            "headers": headers,
            "rows": rows,
            "page": _element_page(element),
            "bbox": None,
        }
    except Exception:
        return None


def _is_markdown_table_row(line: str) -> bool:
    stripped = line.strip()
    return "|" in stripped and stripped.count("|") >= 2


def _is_markdown_separator_row(line: str) -> bool:
    stripped = line.strip().strip("|")
    if not stripped:
        return False
    cells = [cell.strip() for cell in stripped.split("|")]
    return all(re.fullmatch(r":?-{3,}:?", cell or "-") for cell in cells)


def _parse_markdown_table_row(line: str) -> list[str]:
    stripped = line.strip()
    if stripped.startswith("|"):
        stripped = stripped[1:]
    if stripped.endswith("|"):
        stripped = stripped[:-1]
    return [cell.strip() for cell in stripped.split("|")]


def _parse_markdown_table_block(block: list[str]) -> dict[str, Any] | None:
    if len(block) < 2 or not _is_markdown_separator_row(block[1]):
        return None

    headers = _parse_markdown_table_row(block[0])
    rows = [_parse_markdown_table_row(line) for line in block[2:]]
    rows = [row for row in rows if any(cell for cell in row)]
    if not headers and not rows:
        return None

    return {
        "type": "table",
        "label": headers[0] if headers else "Table",
        "title": headers[0] if headers else "Table",
        "headers": headers,
        "rows": rows,
        "page": 0,
        "bbox": None,
    }


def _extract_markdown_tables(full_text: str) -> list[dict[str, Any]]:
    if not full_text:
        return []

    tables: list[dict[str, Any]] = []
    lines = full_text.splitlines()
    index = 0
    while index < len(lines):
        if not _is_markdown_table_row(lines[index]):
            index += 1
            continue

        block = [lines[index]]
        next_index = index + 1
        while next_index < len(lines) and _is_markdown_table_row(lines[next_index]):
            block.append(lines[next_index])
            next_index += 1

        if len(block) >= 2 and _is_markdown_separator_row(block[1]):
            parsed = _parse_markdown_table_block(block)
            if parsed:
                tables.append(parsed)

        index = next_index if next_index > index + 1 else index + 1

    return tables


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


def _docling_extract(file_bytes: bytes, file_name: str) -> dict[str, Any] | None:
    """
    Attempts extraction using IBM's Docling for high-fidelity layout preservation.
    Returns None if docling is not installed or fails.
    """
    try:
        from docling.datamodel.base_models import InputFormat
        from docling.datamodel.pipeline_options import PdfPipelineOptions
        from docling.document_converter import DocumentConverter, PdfFormatOption
        from docling.datamodel.document import DocumentStream

        # Configure for fast but layout-aware extraction
        pipeline_options = PdfPipelineOptions()
        pipeline_options.do_ocr = False  # Faster, skip OCR unless needed
        pipeline_options.do_table_structure = True

        converter = DocumentConverter(
            format_options={
                InputFormat.PDF: PdfFormatOption(pipeline_options=pipeline_options)
            }
        )

        source = DocumentStream(name=file_name, stream=io.BytesIO(file_bytes))
        result = converter.convert(source)
        doc = result.document

        # Export to markdown for the "full text" to preserve structure/tables
        full_text = doc.export_to_markdown()

        # Build structure map from docling's hierarchy
        structure_map: list[dict[str, Any]] = []
        for element, _level in doc.iterate_items():
            label = getattr(element, "label", None)
            if label in ["heading", "title"]:
                text = getattr(element, "text", "")
                if text:
                    structure_map.append(
                        {
                            "label": text[:50],
                            "page": _element_page(element),
                            "bbox": None,
                        }
                    )
                continue

            table_entry = _extract_table_from_docling_element(element)
            if table_entry:
                structure_map.append(table_entry)

        if not any(item.get("type") == "table" for item in structure_map):
            structure_map.extend(_extract_markdown_tables(full_text))

        return {
            "full_text": full_text,
            "structure_map": structure_map,
            "docling_meta": {
                "version": "2.3.0+",
            }
        }
    except Exception as e:
        # Fallback silently or log at debug level
        return None


def extract_pdf_content(file_bytes: bytes, file_name: str) -> dict[str, Any]:
    """
    Extract PDF content and return the sidecar wire response.
    """
    # 1. Try Docling first (Modern High-Fidelity path)
    docling_res = _docling_extract(file_bytes, file_name)

    text_content, first_page_text, pages_count, blocks, used_pymupdf = _extract_pdf_text(file_bytes)

    # Merge docling results if available
    used_docling = False
    structure_map: list[dict[str, Any]] = []

    if docling_res:
        text_content = docling_res["full_text"]
        structure_map = docling_res["structure_map"]
        used_docling = True
    else:
        # Fallback to lightweight structure inference from extracted text blocks.
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
                            "bbox": block.get("bbox"),
                        }
                    )
                    break

    fast_meta = _fast_regex_extract(text_content or file_name, file_name)
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
            "usedDocling": used_docling,
        },
    }
