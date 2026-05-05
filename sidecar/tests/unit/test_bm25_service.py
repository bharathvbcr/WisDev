"""Tests for services/bm25_service.py."""

import pytest
from services.bm25_service import BM25Index, tokenize, get_bm25_index


# ---------------------------------------------------------------------------
# tokenize
# ---------------------------------------------------------------------------

def test_tokenize_basic():
    tokens = tokenize("Hello World foo bar")
    assert "hello" in tokens
    assert "world" in tokens
    assert "foo" in tokens
    assert "bar" in tokens


def test_tokenize_empty_string():
    assert tokenize("") == []


def test_tokenize_filters_single_chars():
    tokens = tokenize("a b c hello")
    assert "a" not in tokens
    assert "b" not in tokens
    assert "c" not in tokens
    assert "hello" in tokens


def test_tokenize_keeps_single_digit():
    # single digits (isdigit) should be kept
    tokens = tokenize("COVID-19 version 2 trial")
    assert "2" in tokens


def test_tokenize_special_characters_stripped():
    tokens = tokenize("cell!!! biology@@ research##")
    assert "cell" in tokens
    assert "biology" in tokens
    assert "research" in tokens


def test_tokenize_hyphenated_kept():
    tokens = tokenize("long-term meta-analysis")
    # hyphens are included in the regex [\w\-]+
    assert any("long" in t or "long-term" in t for t in tokens)


def test_tokenize_lowercases():
    tokens = tokenize("CANCER Biology")
    assert "cancer" in tokens
    assert "biology" in tokens


# ---------------------------------------------------------------------------
# BM25Index.add_documents
# ---------------------------------------------------------------------------

def test_add_documents_returns_count():
    idx = BM25Index()
    n = idx.add_documents(["paper about cancer", "study on diabetes"])
    assert n == 2


def test_add_documents_empty_list_returns_zero():
    idx = BM25Index()
    n = idx.add_documents([])
    assert n == 0
    assert idx.is_empty


def test_add_documents_single():
    idx = BM25Index()
    idx.add_documents(["single document"])
    assert idx.size == 1
    assert not idx.is_empty


def test_add_documents_uses_default_ids():
    idx = BM25Index()
    idx.add_documents(["doc one", "doc two"])
    assert idx.doc_ids == ["0", "1"]


def test_add_documents_custom_ids():
    idx = BM25Index()
    idx.add_documents(["doc one", "doc two"], doc_ids=["id_a", "id_b"])
    assert idx.doc_ids == ["id_a", "id_b"]


# ---------------------------------------------------------------------------
# BM25Index.search
# ---------------------------------------------------------------------------

def test_search_empty_index_returns_empty():
    idx = BM25Index()
    results = idx.search("anything")
    assert results == []


def test_search_finds_relevant_doc():
    idx = BM25Index()
    idx.add_documents(
        [
            "cancer treatment studies immunotherapy clinical outcomes",
            "machine learning algorithms deep neural networks classification",
            "random additional document about weather forecasting systems",
        ],
        doc_ids=["cancer_doc", "ml_doc", "weather_doc"],
    )
    results = idx.search("cancer treatment immunotherapy")
    ids = [r[0] for r in results]
    assert "cancer_doc" in ids


def test_search_returns_tuples_with_id_score_text():
    idx = BM25Index()
    idx.add_documents(
        [
            "neural network deep learning transformer architecture attention",
            "biology genetics dna protein expression cellular",
            "history philosophy ancient civilization culture society",
        ],
        doc_ids=["nn_doc", "bio_doc", "hist_doc"],
    )
    results = idx.search("neural network transformer attention")
    assert len(results) >= 1
    doc_id, score, text = results[0]
    assert doc_id == "nn_doc"
    assert score > 0
    assert "neural" in text


def test_search_top_k_limits_results():
    docs = [f"document about topic {i}" for i in range(20)]
    idx = BM25Index()
    idx.add_documents(docs)
    results = idx.search("document about topic", top_k=3)
    assert len(results) <= 3


def test_search_positive_scores_only():
    idx = BM25Index()
    idx.add_documents(
        ["cancer immunotherapy clinical trial", "unrelated cooking recipe baking"],
        doc_ids=["relevant", "irrelevant"],
    )
    results = idx.search("cancer immunotherapy")
    for _, score, _ in results:
        assert score > 0


def test_search_empty_query_returns_empty():
    idx = BM25Index()
    idx.add_documents(["some document text"])
    # empty string tokenizes to []
    results = idx.search("  ")
    assert results == []


def test_search_results_sorted_descending():
    idx = BM25Index()
    idx.add_documents([
        "cancer cancer cancer treatment therapy",
        "cancer research study",
        "general biology overview",
    ])
    results = idx.search("cancer treatment therapy")
    scores = [r[1] for r in results]
    assert scores == sorted(scores, reverse=True)


# ---------------------------------------------------------------------------
# BM25Index.clear
# ---------------------------------------------------------------------------

def test_clear_resets_index():
    idx = BM25Index()
    idx.add_documents(["some text"])
    assert not idx.is_empty
    idx.clear()
    assert idx.is_empty
    assert idx.size == 0
    assert idx.documents == []
    assert idx.doc_ids == []


# ---------------------------------------------------------------------------
# BM25Index properties
# ---------------------------------------------------------------------------

def test_size_property():
    idx = BM25Index()
    assert idx.size == 0
    idx.add_documents(["alpha beta gamma", "delta epsilon zeta", "theta iota kappa"])
    assert idx.size == 3


def test_is_empty_property():
    idx = BM25Index()
    assert idx.is_empty is True
    idx.add_documents(["text"])
    assert idx.is_empty is False


# ---------------------------------------------------------------------------
# get_bm25_index singleton
# ---------------------------------------------------------------------------

def test_get_bm25_index_returns_same_instance():
    a = get_bm25_index()
    b = get_bm25_index()
    assert a is b


def test_get_bm25_index_returns_bm25_index():
    idx = get_bm25_index()
    assert isinstance(idx, BM25Index)
