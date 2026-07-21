"""Additional edge-case tests for services/chunking_service.py."""

from unittest.mock import patch

from services.chunking_service import (
    _split_oversized_span,
    _trim_span,
    detect_sections,
    find_page,
    find_section,
    sentence_boundary_split,
    chunk_with_offsets,
)


def test_detect_sections_orders_and_boundaries():
    text = (
        "Abstract\nSummary paragraph.\n\n"
        "Introduction\nOverview text.\n\n"
        "Methods\nProcedure details.\n\n"
        "Results\nFindings section.\n\n"
        "Conclusion\nFinal points."
    )
    sections = detect_sections(text)

    assert [item["name"] for item in sections] == [
        "abstract",
        "introduction",
        "methods",
        "results",
        "conclusion",
    ]
    assert sections[0]["char_start"] == 0
    assert sections[-1]["char_end"] == len(text)


def test_find_section_empty_results_when_not_in_section():
    sections = [{"name": "results", "char_start": 10, "char_end": 25}]
    assert find_section(9, sections) == ""
    assert find_section(10, sections) == "results"
    assert find_section(24, sections) == "results"
    assert find_section(25, sections) == ""


def test_trim_span_trims_and_returns_none_for_only_whitespace():
    assert _trim_span("   tokenized text   ", 0, len("   tokenized text   ")) == {
        "text": "tokenized text",
        "start": 3,
        "end": 17,
    }
    assert _trim_span("      ", 0, 6) is None


def test_split_oversized_span_prefers_word_boundaries():
    text = "alpha beta gamma delta epsilon zeta eta theta"
    parts = _split_oversized_span(text, 0, len(text), max_chars=14)
    assert len(parts) >= 2
    assert parts[0]["text"] == "alpha beta"
    # Ensure no piece has trailing/leading spaces.
    for part in parts:
        assert part["text"] == part["text"].strip()


def test_sentence_boundary_split_with_max_segment_splits_long_sentence():
    text = (
        "Short. "
        "This second sentence is intentionally long enough to exceed the configured maximum."
        " It keeps extending to ensure oversized split behavior."
    )
    segments = sentence_boundary_split(text, max_segment_chars=30)

    assert len(segments) >= 3
    assert segments[0]["text"] == "Short."
    assert all(len(segment["text"]) <= 30 for segment in segments)


def test_find_page_and_chunks_include_overrides():
    text = "Abstract\n\n" + ("lorem " * 500)
    sections = detect_sections("Abstract\n\nBody begins here.\n\nMethods\nDetails.")
    chunks = chunk_with_offsets(text, page_breaks=[80], detected_sections=sections, max_chunk_tokens=50, overlap_tokens=0)

    assert chunks
    assert chunks[0]["page"] == 1
    assert any(chunk["page"] == 2 for chunk in chunks[1:])
    if sections:
        assert any(chunk["section"] in {"", "abstract"} for chunk in chunks)


def test_find_page_counts_multiple_breaks():
    assert find_page(0, [10, 20, 30]) == 1
    assert find_page(10, [10, 20, 30]) == 2
    assert find_page(25, [10, 20, 30]) == 3
    assert find_page(35, [10, 20, 30]) == 4


def test_sentence_boundary_split_without_limit_keeps_sentence_spans():
    text = "Alpha sentence. Beta sentence. Gamma sentence."
    segments = sentence_boundary_split(text)

    assert len(segments) == 3
    assert segments[0]["text"] == "Alpha sentence."
    assert segments[-1]["text"] == "Gamma sentence."


def test_chunk_with_offsets_returns_empty_for_blank_text():
    assert chunk_with_offsets("   \n\t   ", page_breaks=[], detected_sections=[]) == []


def test_chunk_with_offsets_preserves_overlap_between_chunks():
    text = "Sentence one. Sentence two. Sentence three. Sentence four."
    chunks = chunk_with_offsets(
        text,
        page_breaks=[],
        detected_sections=[],
        max_chunk_tokens=4,
        overlap_tokens=2,
    )

    assert len(chunks) >= 2
    assert "Sentence two." in chunks[1]["content"] or "Sentence three." in chunks[1]["content"]


def test_split_oversized_span_skips_whitespace_only_parts():
    text = "     abc"
    parts = _split_oversized_span(text, 0, 5, max_chars=2)
    assert parts == []


def test_split_oversized_span_recovers_when_candidate_end_does_not_advance():
    text = "alpha beta"
    original_min = min
    calls = {"count": 0}

    def fake_min(*args):
        calls["count"] += 1
        if calls["count"] == 1:
            return 0
        return original_min(*args)

    with patch("builtins.min", side_effect=fake_min):
        parts = _split_oversized_span(text, 0, len(text), max_chars=5)

    assert parts
    assert parts[0]["text"] == "alpha"
