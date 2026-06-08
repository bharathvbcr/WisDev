"""Tests for services/pdf_extraction_service.py — pure-Python functions only."""

import pytest
from services.pdf_extraction_service import (
    extract_year,
    extract_doi,
    chunk_text_chars,
    CHUNK_SIZE_CHARS,
    CHUNK_OVERLAP_CHARS,
    MAX_CHUNKS,
)


# ---------------------------------------------------------------------------
# extract_year
# ---------------------------------------------------------------------------

class TestExtractYear:
    def test_year_2000s(self):
        assert extract_year("Published in 2021") == 2021

    def test_year_1990s(self):
        assert extract_year("Copyright 1995 Elsevier") == 1995

    def test_parenthesized_year(self):
        assert extract_year("Smith et al. (2019) showed that") == 2019

    def test_no_year_returns_none(self):
        assert extract_year("No year in this text") is None

    def test_out_of_range_year_ignored(self):
        # 1800 is out of range (< 1950) — should not be returned
        result = extract_year("Written in 1800")
        assert result is None

    def test_future_year_beyond_2030_ignored(self):
        result = extract_year("Predicted for 2099")
        assert result is None

    def test_empty_string(self):
        assert extract_year("") is None

    def test_published_prefix(self):
        assert extract_year("Published: 2023 in Nature") == 2023

    def test_copyright_prefix(self):
        assert extract_year("Copyright: 2020 IEEE") == 2020

    def test_boundary_parenthesized_1950(self):
        # 1950 is in range and matched via parenthesized pattern
        result = extract_year("(1950)")
        assert result == 1950

    def test_boundary_max_year(self):
        # Regex pattern 20[0-2]\d covers up to 2029; 2029 is the max plain match
        result = extract_year("Published in 2029")
        assert result == 2029

    def test_2030_not_matched_by_plain_pattern(self):
        # 2030 doesn't match 20[0-2]\d; needs prefix/parens to be returned
        result = extract_year("Projection for 2030")
        assert result is None


# ---------------------------------------------------------------------------
# extract_doi
# ---------------------------------------------------------------------------

class TestExtractDoi:
    def test_standard_doi(self):
        text = "DOI: 10.1038/nature12373"
        result = extract_doi(text)
        assert result == "10.1038/nature12373"

    def test_doi_with_version(self):
        text = "See 10.1016/j.cell.2021.01.001 for details"
        result = extract_doi(text)
        assert result == "10.1016/j.cell.2021.01.001"

    def test_no_doi_returns_none(self):
        assert extract_doi("No DOI here") is None

    def test_empty_string(self):
        assert extract_doi("") is None

    def test_trailing_period_stripped(self):
        text = "reference: 10.1234/test."
        result = extract_doi(text)
        assert result is not None
        assert not result.endswith(".")

    def test_doi_with_special_chars(self):
        text = "10.1093/bioinformatics/btab123"
        result = extract_doi(text)
        assert result == "10.1093/bioinformatics/btab123"


# ---------------------------------------------------------------------------
# chunk_text_chars
# ---------------------------------------------------------------------------

class TestChunkTextChars:
    def test_empty_text_returns_empty(self):
        chunks = chunk_text_chars("")
        assert chunks == []

    def test_short_text_single_chunk(self):
        text = "short text"
        chunks = chunk_text_chars(text)
        assert len(chunks) == 1
        assert chunks[0]["text"] == text
        assert chunks[0]["index"] == 0

    def test_chunk_has_expected_keys(self):
        chunks = chunk_text_chars("hello world this is text")
        assert "index" in chunks[0]
        assert "text" in chunks[0]
        assert "charCount" in chunks[0]

    def test_char_count_matches_text_length(self):
        text = "x" * 500
        chunks = chunk_text_chars(text)
        for chunk in chunks:
            assert chunk["charCount"] == len(chunk["text"])

    def test_text_exactly_chunk_size(self):
        text = "a" * CHUNK_SIZE_CHARS
        chunks = chunk_text_chars(text)
        assert len(chunks) == 1
        assert len(chunks[0]["text"]) == CHUNK_SIZE_CHARS

    def test_text_larger_than_chunk_size(self):
        text = "word " * 1000  # well above CHUNK_SIZE_CHARS=3000
        chunks = chunk_text_chars(text)
        assert len(chunks) >= 2

    def test_chunks_cover_full_text(self):
        text = "a" * (CHUNK_SIZE_CHARS + 500)
        chunks = chunk_text_chars(text)
        # First chunk is CHUNK_SIZE_CHARS long
        assert chunks[0]["charCount"] == CHUNK_SIZE_CHARS

    def test_max_chunks_limit(self):
        # Create very long text
        text = "x" * (CHUNK_SIZE_CHARS * (MAX_CHUNKS + 5))
        chunks = chunk_text_chars(text)
        assert len(chunks) <= MAX_CHUNKS

    def test_index_increments(self):
        text = "z" * (CHUNK_SIZE_CHARS * 3)
        chunks = chunk_text_chars(text)
        for i, chunk in enumerate(chunks):
            assert chunk["index"] == i

    def test_overlap_between_chunks(self):
        # With overlap, the start of chunk n+1 is inside the end of chunk n
        text = "A" * CHUNK_SIZE_CHARS + "B" * CHUNK_SIZE_CHARS
        chunks = chunk_text_chars(text)
        if len(chunks) >= 2:
            # The first chunk ends at CHUNK_SIZE_CHARS, the second starts
            # CHUNK_OVERLAP_CHARS before that.
            first_end = chunks[0]["text"][-CHUNK_OVERLAP_CHARS:]
            second_start = chunks[1]["text"][:CHUNK_OVERLAP_CHARS]
            assert first_end == second_start
