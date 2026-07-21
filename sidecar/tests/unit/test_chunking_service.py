from services.chunking_service import chunk_with_offsets


def test_chunking_splits_oversized_sentence_into_bounded_chunks():
    text = " ".join(f"token{i}" for i in range(500)) + "."

    chunks = chunk_with_offsets(
        text,
        page_breaks=[],
        detected_sections=[],
        max_chunk_tokens=50,
        overlap_tokens=0,
    )

    assert len(chunks) > 1
    assert all(len(chunk["content"]) <= 200 for chunk in chunks)
    assert chunks[0]["char_start"] == 0
    assert chunks[-1]["char_end"] <= len(text)
