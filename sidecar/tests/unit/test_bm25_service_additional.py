"""Additional branch tests for services/bm25_service.py."""

import importlib.util
from pathlib import Path
from unittest.mock import patch

from services import bm25_service
from services.bm25_service import BM25Index, _FallbackBM25Okapi


def test_fallback_bm25_index_used_when_library_missing(monkeypatch):
    monkeypatch.setattr(bm25_service, "BM25Okapi", None)
    idx = BM25Index()
    idx.add_documents(["machine learning paper", "clinical trial paper", "unrelated text"])

    assert isinstance(idx.index, _FallbackBM25Okapi)

    results = idx.search("paper", top_k=2)
    assert len(results) >= 2
    # The documents containing the term should rank first with positive scores.
    assert {entry[0] for entry in results[:2]} <= {"0", "1"}
    assert all(score > 0 for _, score, _ in results)


def test_fallback_scores_argsort_in_ascending_score_order():
    scores = bm25_service._FallbackScores([0.2, 2.0, 1.0, 2.0])
    assert scores.argsort() == [0, 2, 1, 3]


def test_module_import_sets_bm25okapi_none_when_rank_bm25_missing():
    source_path = Path(bm25_service.__file__)
    spec = importlib.util.spec_from_file_location("test_bm25_missing_rank", source_path)
    module = importlib.util.module_from_spec(spec)
    original_import = __import__

    def _importer(name, globals=None, locals=None, fromlist=(), level=0):
        if name == "rank_bm25":
            raise ModuleNotFoundError("rank_bm25 missing")
        return original_import(name, globals, locals, fromlist, level)

    assert spec is not None and spec.loader is not None
    with patch("builtins.__import__", side_effect=_importer):
        spec.loader.exec_module(module)

    assert module.BM25Okapi is None
