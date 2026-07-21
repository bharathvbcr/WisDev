from __future__ import annotations

import numpy as np
from unittest.mock import patch

import pytest

from services.raptor_service import RaptorService
import services.raptor_service as raptor_module


def _paper(paper_id: str, chunk_defs: list[tuple[str, list[float], str]]):
    return {
        "paper_id": paper_id,
        "chunks": [
            {
                "chunk_id": chunk_id,
                "content": content,
                "embedding": list(embedding),
                "char_start": 0,
                "char_end": len(content),
                "page": 1,
                "section": "intro",
            }
            for chunk_id, embedding, content in chunk_defs
        ],
    }


def _leaf(chunk_id: str, content: str, paper_id: str, embedding: list[float] | None = None):
    return {
        "id": chunk_id,
        "paper_id": paper_id,
        "content": content,
        "embedding": list(embedding) if embedding is not None else [],
        "char_start": 0,
        "char_end": len(content),
        "page": 1,
        "section": "intro",
        "level": 0,
    }


def test_build_adaptive_tree_without_leaves_returns_empty_tree_node():
    service = RaptorService()
    result = service.build_adaptive_tree([{"paper_id": "paper-1", "chunks": []}])

    assert result["levels"] == 1
    assert result["total_nodes"] == 0
    metadata = service.trees[result["tree_id"]]
    assert metadata["clusters"] == []
    assert metadata["topics"] == []
    assert metadata["root"] is None


def test_build_adaptive_tree_less_than_three_vectors_uses_leaf_level_only():
    service = RaptorService()
    result = service.build_adaptive_tree(
        [
            _paper(
                "paper-1",
                [
                    ("chunk-1", [0.1, 0.0], "evidence one"),
                    ("chunk-2", [0.1, 0.0], "evidence two"),
                ],
            )
        ],
        min_clusters=3,
        max_levels=4,
    )

    assert result["levels"] == 1
    assert result["total_nodes"] == 2
    metadata = service.trees[result["tree_id"]]
    assert metadata["root"] is None
    assert metadata["clusters"] == []
    assert metadata["topics"] == []


def test_build_adaptive_tree_constructs_topics_and_root_for_multi_level_flow():
    service = RaptorService()

    papers = [
        _paper(
            f"paper-{index}",
            [
                (
                    f"chunk-{index}",
                    [float(index), 0.0],
                    f"evidence chunk {index}",
                )
            ],
        )
        for index in range(6)
    ]

    state = {"stage": 0}

    def _fake_cluster(_embeddings, items, min_k=3):
        if state["stage"] == 0:
            state["stage"] += 1
            return [items[:2], items[2:4], items[4:]]
        return [items[:2], items[2:]]

    with patch.object(RaptorService, "_adaptive_cluster", staticmethod(_fake_cluster)):
        result = service.build_adaptive_tree(papers, min_clusters=3, max_levels=4)

    assert result["levels"] == 4
    metadata = service.trees[result["tree_id"]]
    assert metadata["root"] is not None
    assert metadata["root"]["id"] == "root"
    assert len(metadata["clusters"]) == 3
    assert len(metadata["topics"]) == 2
    assert result["total_nodes"] > 6


def test_query_tree_returns_empty_for_unknown_tree_or_zero_norm_query():
    service = RaptorService()
    assert service.query_tree("missing-tree", [0.1, 0.2]) == []

    tree_id = "tree-empty"
    service.trees[tree_id] = {
        "all_nodes": [
            {"id": "n1", "content": "one", "embedding": [0.0, 0.0], "paper_id": "paper", "level": 0}
        ]
    }
    assert service.query_tree(tree_id, [0.0, 0.0]) == []


def test_query_tree_filters_by_level_and_score_and_sorts():
    service = RaptorService()
    tree_id = "tree-filter"
    service.trees[tree_id] = {
        "all_nodes": [
            {
                "id": "chunk-level-0",
                "content": "first",
                "paper_id": "p",
                "embedding": [1.0, 0.0],
                "level": 0,
                "char_start": 0,
                "char_end": 5,
                "page": 1,
                "section": "summary",
            },
            {
                "id": "chunk-level-1",
                "content": "second",
                "paper_id": "p",
                "embedding": [0.8, 0.6],
                "level": 1,
                "char_start": 6,
                "char_end": 12,
                "page": 1,
                "section": "analysis",
            },
            {
                "id": "chunk-no-vec",
                "content": "third",
                "paper_id": "p",
                "embedding": [],
                "level": 1,
                "char_start": 13,
                "char_end": 18,
                "page": 1,
                "section": "analysis",
            },
        ]
    }

    results = service.query_tree(tree_id, [1.0, 0.0], top_k=10, levels=[1])
    assert len(results) == 1
    assert results[0]["chunk_id"] == "chunk-level-1"

    results_all = service.query_tree(tree_id, [1.0, 0.0], top_k=10)
    assert [row["chunk_id"] for row in results_all] == ["chunk-level-0", "chunk-level-1"]


def test_incremental_update_builds_tree_when_tree_missing():
    service = RaptorService()
    new_papers = [
        _paper("paper-1", [("chunk-1", [0.1, 0.2], "fresh")]),
    ]

    fake_metadata = {
        "tree_id": "tree-generated",
        "levels": 1,
        "total_nodes": 1,
        "created_at": "2025-01-01T00:00:00+00:00",
        "updated_at": "2025-01-01T00:00:00+00:00",
    }

    with patch.object(
        service,
        "build_adaptive_tree",
        return_value=fake_metadata,
    ) as patched_build:
        metadata = service.incremental_update("missing-tree", new_papers)

    patched_build.assert_called_once()
    assert metadata == fake_metadata


def test_incremental_update_updates_existing_cluster_with_best_match():
    service = RaptorService()
    tree_id = "tree-1"
    service.trees[tree_id] = {
        "leaves": [],
        "clusters": [
            {
                "id": "cluster-0",
                "children": ["chunk-old"],
                "paper_ids": ["paper-old"],
                "embedding": [1.0, 0.0],
                "level": 1,
            }
        ],
        "topics": [],
        "root": None,
        "all_nodes": [],
    }

    result = service.incremental_update(
        tree_id,
        [
            _paper("paper-new", [("chunk-new", [1.0, 0.0], "new chunk")]),
        ],
    )

    assert result["tree_id"] == tree_id
    assert result["levels"] == 2
    assert result["total_nodes"] == 2
    cluster = service.trees[tree_id]["clusters"][0]
    assert "chunk-new" in cluster["children"]
    assert "paper-new" in cluster["paper_ids"]


def test_has_tree_checks_cache():
    service = RaptorService()
    assert service.has_tree("nope") is False
    service.trees["known"] = {}
    assert service.has_tree("known") is True


def test_adaptive_cluster_returns_items_when_count_below_three():
    service = RaptorService()
    items = [{"id": "a"}, {"id": "b"}]
    clusters = service._adaptive_cluster(
        np.array([[0.1, 0.2], [0.2, 0.3]]),
        items,
        min_k=3,
    )
    assert clusters == [items]


def test_adaptive_cluster_falls_back_to_single_cluster_on_failure():
    service = RaptorService()
    items = [{"id": "a"}, {"id": "b"}, {"id": "c"}]
    with patch.object(raptor_module, "pdist", side_effect=RuntimeError("boom")):
        clusters = service._adaptive_cluster(
            np.array([[0.1, 0.0], [0.0, 0.1], [0.2, 0.2]]),
            items,
            min_k=3,
        )
    assert clusters == [items]


def test_extractive_summary_includes_long_sentences_and_truncates_output():
    service = RaptorService()
    short_sentence = "short."
    long_sentence = "x" * 700

    summary = service._extractive_summary(
        [
            {"content": short_sentence},
            {"content": long_sentence},
        ],
        max_length=120,
    )

    assert short_sentence[:-1] not in summary
    assert summary.startswith("x" * 120)

