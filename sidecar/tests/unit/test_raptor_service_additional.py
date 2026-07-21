"""Additional branch coverage tests for services/raptor_service.py."""

from __future__ import annotations

import numpy as np
from unittest.mock import patch

from services.raptor_service import RaptorService
import services.raptor_service as raptor_module


def _make_leaf(chunk_id: str, paper_id: str, content: str, embedding: list[float] | None = None):
    return {
        "chunk_id": chunk_id,
        "paper_id": paper_id,
        "content": content,
        "embedding": [] if embedding is None else embedding,
        "char_start": 3,
        "char_end": len(content),
        "page": 1,
        "section": "intro",
        "level": 0,
    }


def test_build_adaptive_tree_without_topics_when_max_levels_is_2():
    service = RaptorService()

    papers = [
        {
            "paper_id": f"paper-{index}",
            "chunks": [
                _make_leaf(f"chunk-{index}", f"paper-{index}", f"content {index}", [float(index), 0.1 * index]),
            ],
        }
        for index in range(4)
    ]

    def _cluster(_embeddings, _items, min_k=3):
        return [_items[:2], _items[2:]]

    with patch.object(RaptorService, "_adaptive_cluster", staticmethod(_cluster)):
        result = service.build_adaptive_tree(papers, min_clusters=3, max_levels=2)

    assert result["levels"] == 2
    assert result["total_nodes"] == 4 + 2
    assert not result["tree_id"] == ""

    stored = service.trees[result["tree_id"]]
    assert stored["topics"] == []
    assert stored["root"] is None


def test_normalize_chunk_fallback_to_generated_chunk_id():
    service = RaptorService()
    papers = [
        {
            "paper_id": "paper-1",
            "chunks": [
                {
                    "content": "first chunk",
                    "embedding": [0.1, 0.2],
                    "charStart": 1,
                    "charEnd": 11,
                    "page": 2,
                    "section": "abstract",
                    "chunkId": "",
                }
            ],
        }
    ]

    result = service.build_adaptive_tree(papers)
    metadata = service.trees[result["tree_id"]]
    leaf = metadata["leaves"][0]
    assert leaf["id"]
    assert leaf["char_start"] == 1
    assert leaf["char_end"] == 11
    assert leaf["page"] == 2


def test_adaptive_cluster_adjusts_k_when_min_exceeds_max():
    service = RaptorService()
    embeddings = np.array(
        [
            [1.0, 0.0],
            [0.0, 1.0],
            [1.0, 1.0],
        ]
    )
    items = [{"id": "a"}, {"id": "b"}, {"id": "c"}]

    with patch.object(raptor_module, "pdist", return_value=np.array([0.1, 0.2, 0.3])):
        with patch.object(raptor_module, "ward", return_value="linkage-matrix"):
            with patch.object(raptor_module, "silhouette_score", return_value=0.42):
                with patch.object(raptor_module, "fcluster", return_value=np.array([1, 2, 1])):
                    clusters = service._adaptive_cluster(embeddings, items, min_k=3)

    assert len(clusters) == 2
    assert sorted(len(cluster) for cluster in clusters) == [1, 2]


def test_incremental_update_adds_unembedded_chunks_without_matching_cluster():
    service = RaptorService()
    service.trees["tree-1"] = {
        "leaves": [
            _make_leaf("existing", "paper-old", "old chunk", [0.9, 0.9]),
        ],
        "clusters": [],
        "topics": [],
        "root": None,
        "all_nodes": [],
    }

    result = service.incremental_update(
        "tree-1",
        [
            {
                "paper_id": "paper-new",
                "chunks": [
                    _make_leaf("new-chunk", "paper-new", "new chunk", []),
                ],
            }
        ],
    )

    assert result["tree_id"] == "tree-1"
    assert result["total_nodes"] == 2  # existing leaf + new leaf
    tree = service.trees["tree-1"]
    assert len(tree["all_nodes"]) == 2
    assert tree["leaves"][1]["id"] == "new-chunk"


def test_incremental_update_skips_clusters_without_embeddings_and_sets_root_level():
    service = RaptorService()
    service.trees["tree-1"] = {
        "leaves": [],
        "clusters": [
            {
                "id": "cluster-empty",
                "children": [],
                "paper_ids": [],
                "embedding": [],
                "level": 1,
            }
        ],
        "topics": [{"id": "topic-1", "embedding": [0.5, 0.5], "level": 2}],
        "root": {"id": "root", "embedding": [0.5, 0.5], "level": 3},
        "all_nodes": [],
    }

    result = service.incremental_update(
        "tree-1",
        [{"paper_id": "paper-new", "chunks": [_make_leaf("new-chunk", "paper-new", "new chunk", [1.0, 0.0])]}],
    )

    assert result["levels"] == 4
    cluster = service.trees["tree-1"]["clusters"][0]
    assert cluster["children"] == []


def test_adaptive_cluster_skips_single_cluster_labels_and_exception_scores():
    service = RaptorService()
    embeddings = np.array(
        [
            [1.0, 0.0],
            [0.0, 1.0],
            [1.0, 1.0],
            [0.5, 0.5],
        ]
    )
    items = [{"id": "a"}, {"id": "b"}, {"id": "c"}, {"id": "d"}]

    with patch.object(raptor_module, "pdist", return_value=np.array([0.1, 0.2, 0.3, 0.4, 0.5, 0.6])):
        with patch.object(raptor_module, "ward", return_value="linkage-matrix"):
            with patch.object(
                raptor_module,
                "fcluster",
                side_effect=[np.array([1, 1, 1, 1]), np.array([1, 2, 1, 2]), np.array([1, 2, 1, 2])],
            ):
                with patch.object(
                    raptor_module,
                    "silhouette_score",
                    side_effect=[RuntimeError("bad silhouette"), 0.42],
                ):
                    clusters = service._adaptive_cluster(embeddings, items, min_k=2)

    assert len(clusters) == 2
