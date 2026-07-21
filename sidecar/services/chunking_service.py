"""Sentence-aware chunking with section and offset metadata."""
from __future__ import annotations

import logging
import re
from typing import TypedDict

logger = logging.getLogger(__name__)

SECTION_PATTERNS = [
    (r"(?:^|\n)(?:\d+\.?\s+)?(?:abstract)\b", "abstract"),
    (r"(?:^|\n)(?:\d+\.?\s+)?(?:introduction)\b", "introduction"),
    (
        r"(?:^|\n)(?:\d+\.?\s+)?(?:background|related\s+work|literature\s+review)\b",
        "background",
    ),
    (r"(?:^|\n)(?:\d+\.?\s+)?(?:methods?|methodology)\b", "methods"),
    (r"(?:^|\n)(?:\d+\.?\s+)?(?:results?)\b", "results"),
    (r"(?:^|\n)(?:\d+\.?\s+)?(?:discussion)\b", "discussion"),
    (r"(?:^|\n)(?:\d+\.?\s+)?(?:conclusion|conclusions)\b", "conclusion"),
    (r"(?:^|\n)(?:\d+\.?\s+)?(?:references|bibliography)\b", "references"),
    (r"(?:^|\n)(?:\d+\.?\s+)?(?:appendix|appendices)\b", "appendix"),
]

CHARS_PER_TOKEN = 4
_SENTENCE_BREAK_PATTERN = re.compile(r"(?<=[.!?])\s+(?=[A-Z\[])")

class SectionMatch(TypedDict):
    name: str
    index: int


class TextSpan(TypedDict):
    text: str
    start: int
    end: int


def detect_sections(text: str) -> list[dict]:
    """Detect common academic sections from document text."""
    matches: list[SectionMatch] = []
    for pattern, name in SECTION_PATTERNS:
        for pattern_match in re.finditer(pattern, text, re.IGNORECASE | re.MULTILINE):
            matches.append({"name": name, "index": pattern_match.start()})

    matches.sort(key=lambda item: int(item["index"]))
    sections = []
    for index, section_match in enumerate(matches):
        next_index = int(matches[index + 1]["index"]) if index < len(matches) - 1 else len(text)
        sections.append(
            {
                "name": str(section_match["name"]),
                "char_start": int(section_match["index"]),
                "char_end": next_index,
            }
        )
    return sections


def find_page(char_offset: int, page_breaks: list[int]) -> int:
    """Translate a character offset into a 1-based page number."""
    page = 1
    for break_offset in page_breaks:
        if char_offset >= break_offset:
            page += 1
        else:
            break
    return page


def find_section(char_offset: int, sections: list[dict]) -> str:
    """Find the detected section containing a character offset."""
    for section in sections:
        if section["char_start"] <= char_offset < section["char_end"]:
            return section["name"]
    return ""


def _trim_span(text: str, start: int, end: int) -> TextSpan | None:
    while start < end and text[start].isspace():
        start += 1
    while end > start and text[end - 1].isspace():
        end -= 1
    if start >= end:
        return None
    return {"text": text[start:end], "start": start, "end": end}


def _split_oversized_span(text: str, start: int, end: int, max_chars: int) -> list[TextSpan]:
    """Split a large span on whitespace, falling back to hard cuts when needed."""
    parts: list[TextSpan] = []
    cursor = start
    while cursor < end:
        candidate_end = min(cursor + max_chars, end)
        if candidate_end < end:
            window = text[cursor:candidate_end]
            split_at = max(window.rfind("\n"), window.rfind(" "), window.rfind("\t"))
            if split_at > max_chars // 2:
                candidate_end = cursor + split_at

        if candidate_end <= cursor:
            candidate_end = min(cursor + max_chars, end)

        part = _trim_span(text, cursor, candidate_end)
        if part:
            parts.append(part)
            cursor = part["end"]
        else:
            cursor = candidate_end
    return parts


def sentence_boundary_split(text: str, max_segment_chars: int | None = None) -> list[TextSpan]:
    """Split text on sentence boundaries, preserving character offsets."""
    spans: list[TextSpan] = []
    last_end = 0

    for match in _SENTENCE_BREAK_PATTERN.finditer(text):
        span = _trim_span(text, last_end, match.start())
        if span:
            spans.append(span)
        last_end = match.end()

    tail = _trim_span(text, last_end, len(text))
    if tail:
        spans.append(tail)

    if not max_segment_chars or max_segment_chars <= 0:
        return spans

    bounded: list[TextSpan] = []
    for span in spans:
        if len(span["text"]) <= max_segment_chars:
            bounded.append(span)
            continue
        bounded.extend(
            _split_oversized_span(text, span["start"], span["end"], max_segment_chars)
        )
    return bounded


def chunk_with_offsets(
    full_text: str,
    page_breaks: list[int],
    detected_sections: list[dict],
    max_chunk_tokens: int = 500,
    overlap_tokens: int = 100,
) -> list[dict]:
    """Chunk text into bounded spans with stable source offsets."""
    max_chunk_chars = max_chunk_tokens * CHARS_PER_TOKEN
    overlap_chars = overlap_tokens * CHARS_PER_TOKEN

    sentences = sentence_boundary_split(full_text, max_segment_chars=max_chunk_chars)
    if not sentences:
        return []

    chunks: list[dict] = []
    current_sentences: list[TextSpan] = []
    current_length = 0

    for sentence in sentences:
        sentence_length = len(sentence["text"])
        if current_sentences and current_length + sentence_length > max_chunk_chars:
            char_start = current_sentences[0]["start"]
            char_end = current_sentences[-1]["end"]
            chunks.append(
                {
                    "content": " ".join(item["text"] for item in current_sentences),
                    "char_start": char_start,
                    "char_end": char_end,
                    "page": find_page(char_start, page_breaks),
                    "section": find_section(char_start, detected_sections),
                }
            )

            overlap_length = 0
            overlap_start = len(current_sentences)
            while overlap_start > 0 and overlap_length < overlap_chars:
                overlap_start -= 1
                overlap_length += len(current_sentences[overlap_start]["text"])

            current_sentences = current_sentences[overlap_start:]
            current_length = sum(len(item["text"]) for item in current_sentences)

        current_sentences.append(sentence)
        current_length += sentence_length

    if current_sentences:
        char_start = current_sentences[0]["start"]
        char_end = current_sentences[-1]["end"]
        chunks.append(
            {
                "content": " ".join(item["text"] for item in current_sentences),
                "char_start": char_start,
                "char_end": char_end,
                "page": find_page(char_start, page_breaks),
                "section": find_section(char_start, detected_sections),
            }
        )

    logger.info("Chunked text into %s chunks", len(chunks))
    return chunks
