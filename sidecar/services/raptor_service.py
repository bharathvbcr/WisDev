"""In-memory RAPTOR tree builder used by the Azure compute compatibility API."""
from __future__ import annotations

import logging
import uuid
from datetime import datetime, timezone

import numpy as np
from scipy.cluster.hierarchy import fcluster, ward
from scipy.spatial.distance import pdist
from sklearn.metrics import silhouette_score

logger = logging.getLogger(__name__)


class RaptorService:
    """Adaptive RAPTOR tree builder and query engine."""

    def __init__(self) -> None:
        self.trees: dict[str, dict] = {}

    def build_adaptive_tree(
        self,
        papers: list[dict],
        min_clusters: int = 3,
        max_levels: int = 4,
    ) -> dict:
        now = datetime.now(timezone.utc).isoformat()

        leaves = []
        for paper in papers:
            for chunk in paper["chunks"]:
                leaves.append(
                    {
                        "id": chunk.get("chunk_id") or chunk.get("chunkId") or str(uuid.uuid4()),
                        "paper_id": paper["paper_id"],
                        "content": chunk["content"],
                        "embedding": chunk.get("embedding", []),
                        "char_start": chunk.get("char_start", chunk.get("charStart", 0)),
                        "char_end": chunk.get("char_end", chunk.get("charEnd", 0)),
                        "page": chunk.get("page", 0),
                        "section": chunk.get("section", ""),
                        "level": 0,
                    }
                )

        tree_id = f"tree_{uuid.uuid4().hex[:8]}"
        if not leaves:
            self.trees[tree_id] = {
                "leaves": [],
                "clusters": [],
                "topics": [],
                "root": None,
                "all_nodes": [],
            }
            return {
                "tree_id": tree_id,
                "levels": 1,
                "total_nodes": 0,
                "created_at": now,
                "updated_at": now,
            }

        embeddings = np.array([leaf["embedding"] for leaf in leaves if leaf["embedding"]])
        if len(embeddings) < 3 or len(leaves) < 3:
            self.trees[tree_id] = {
                "leaves": leaves,
                "clusters": [],
                "topics": [],
                "root": None,
                "all_nodes": list(leaves),
            }
            return {
                "tree_id": tree_id,
                "levels": 1,
                "total_nodes": len(leaves),
                "created_at": now,
                "updated_at": now,
            }

        all_nodes = list(leaves)
        clusters = self._adaptive_cluster(embeddings, leaves, min_k=min_clusters)
        cluster_nodes = []
        for index, cluster in enumerate(clusters):
            centroid = np.mean([np.array(leaf["embedding"]) for leaf in cluster], axis=0)
            cluster_nodes.append(
                {
                    "id": f"cluster_{index}",
                    "content": self._extractive_summary(cluster),
                    "embedding": centroid.tolist(),
                    "children": [leaf["id"] for leaf in cluster],
                    "paper_ids": sorted({leaf["paper_id"] for leaf in cluster}),
                    "level": 1,
                }
            )

        all_nodes.extend(cluster_nodes)

        topic_nodes = []
        if len(cluster_nodes) >= 3 and max_levels >= 3:
            cluster_embeddings = np.array([node["embedding"] for node in cluster_nodes])
            topics = self._adaptive_cluster(cluster_embeddings, cluster_nodes, min_k=2)
            for index, topic in enumerate(topics):
                centroid = np.mean([np.array(node["embedding"]) for node in topic], axis=0)
                topic_nodes.append(
                    {
                        "id": f"topic_{index}",
                        "content": self._extractive_summary(topic),
                        "embedding": centroid.tolist(),
                        "children": [node["id"] for node in topic],
                        "paper_ids": sorted({pid for node in topic for pid in node.get("paper_ids", [])}),
                        "level": 2,
                    }
                )
            all_nodes.extend(topic_nodes)

        top_level = topic_nodes if topic_nodes else cluster_nodes
        root = None
        if top_level and max_levels >= 4:
            root_embedding = np.mean([np.array(node["embedding"]) for node in top_level], axis=0)
            root = {
                "id": "root",
                "content": self._extractive_summary(top_level),
                "embedding": root_embedding.tolist(),
                "children": [node["id"] for node in top_level],
                "paper_ids": sorted({pid for node in top_level for pid in node.get("paper_ids", [])}),
                "level": 3,
            }
            all_nodes.append(root)

        self.trees[tree_id] = {
            "leaves": leaves,
            "clusters": cluster_nodes,
            "topics": topic_nodes,
            "root": root,
            "all_nodes": all_nodes,
        }

        levels = 1
        if cluster_nodes:
            levels = 2
        if topic_nodes:
            levels = 3
        if root:
            levels = 4

        return {
            "tree_id": tree_id,
            "levels": levels,
            "total_nodes": len(all_nodes),
            "created_at": now,
            "updated_at": now,
        }

    def query_tree(
        self,
        tree_id: str,
        query_embedding: list[float] | None,
        top_k: int = 10,
        levels: list[int] | None = None,
    ) -> list[dict]:
        tree = self.trees.get(tree_id)
        if not tree or not query_embedding:
            return []

        query_emb = np.array(query_embedding)
        if np.linalg.norm(query_emb) == 0:
            return []

        scored = []
        for node in tree.get("all_nodes", []):
            node_level = int(node.get("level", 0))
            if levels and node_level not in levels:
                continue

            node_embedding = np.array(node.get("embedding") or [])
            if node_embedding.size == 0 or np.linalg.norm(node_embedding) == 0:
                continue

            score = float(
                np.dot(query_emb, node_embedding)
                / (np.linalg.norm(query_emb) * np.linalg.norm(node_embedding) + 1e-10)
            )
            scored.append(
                {
                    "chunk_id": node["id"],
                    "content": node["content"],
                    "paper_id": node.get("paper_id", ""),
                    "score": score,
                    "level": node_level,
                    "char_start": node.get("char_start", 0),
                    "char_end": node.get("char_end", 0),
                    "page": node.get("page", 0),
                    "section": node.get("section", ""),
                }
            )

        scored.sort(key=lambda item: item["score"], reverse=True)
        return scored[:top_k]

    def incremental_update(self, tree_id: str, new_papers: list[dict]) -> dict:
        now = datetime.now(timezone.utc).isoformat()
        tree = self.trees.get(tree_id)
        if not tree:
            return self.build_adaptive_tree(new_papers)

        new_leaves = []
        for paper in new_papers:
            for chunk in paper["chunks"]:
                new_leaves.append(
                    {
                        "id": chunk.get("chunk_id") or chunk.get("chunkId") or str(uuid.uuid4()),
                        "paper_id": paper["paper_id"],
                        "content": chunk["content"],
                        "embedding": chunk.get("embedding", []),
                        "char_start": chunk.get("char_start", chunk.get("charStart", 0)),
                        "char_end": chunk.get("char_end", chunk.get("charEnd", 0)),
                        "page": chunk.get("page", 0),
                        "section": chunk.get("section", ""),
                        "level": 0,
                    }
                )

        tree["leaves"].extend(new_leaves)
        clusters = tree.get("clusters", [])
        for leaf in new_leaves:
            if not leaf["embedding"] or not clusters:
                continue

            leaf_embedding = np.array(leaf["embedding"])
            best_cluster = None
            best_score = -1.0
            for cluster in clusters:
                cluster_embedding = np.array(cluster.get("embedding") or [])
                if cluster_embedding.size == 0:
                    continue
                score = float(
                    np.dot(leaf_embedding, cluster_embedding)
                    / (np.linalg.norm(leaf_embedding) * np.linalg.norm(cluster_embedding) + 1e-10)
                )
                if score > best_score:
                    best_score = score
                    best_cluster = cluster

            if best_cluster is not None:
                best_cluster.setdefault("children", []).append(leaf["id"])
                if leaf["paper_id"] not in best_cluster.setdefault("paper_ids", []):
                    best_cluster["paper_ids"].append(leaf["paper_id"])

        all_nodes = list(tree["leaves"])
        all_nodes.extend(tree.get("clusters", []))
        all_nodes.extend(tree.get("topics", []))
        if tree.get("root"):
            all_nodes.append(tree["root"])
        tree["all_nodes"] = all_nodes

        levels = 1
        if tree.get("clusters"):
            levels = 2
        if tree.get("topics"):
            levels = 3
        if tree.get("root"):
            levels = 4

        return {
            "tree_id": tree_id,
            "levels": levels,
            "total_nodes": len(all_nodes),
            "created_at": now,
            "updated_at": now,
        }

    def has_tree(self, tree_id: str) -> bool:
        return tree_id in self.trees

    @staticmethod
    def _adaptive_cluster(
        embeddings: np.ndarray,
        items: list[dict],
        min_k: int = 3,
    ) -> list[list[dict]]:
        count = len(embeddings)
        if count < 3:
            return [items]

        max_k = min(max(min_k + 1, count // 3), count - 1)
        if min_k >= max_k:
            min_k = 2
            max_k = max(3, count // 2)

        try:
            distances = np.nan_to_num(pdist(embeddings, metric="cosine"), nan=1.0)
            linkage_matrix = ward(distances)
        except Exception as exc:  # pragma: no cover
            logger.warning("Falling back to a single RAPTOR cluster: %s", exc)
            return [items]

        best_k = min_k
        best_score = -1.0
        for k in range(min_k, max_k + 1):
            try:
                labels = fcluster(linkage_matrix, t=k, criterion="maxclust")
                if len(set(labels)) < 2:
                    continue
                score = silhouette_score(embeddings, labels, metric="cosine")
                if score > best_score:
                    best_score = score
                    best_k = k
            except Exception:
                continue

        labels = fcluster(linkage_matrix, t=best_k, criterion="maxclust")
        clusters: dict[int, list[dict]] = {}
        for item, label in zip(items, labels):
            clusters.setdefault(int(label), []).append(item)
        return list(clusters.values())

    @staticmethod
    def _extractive_summary(nodes: list[dict], max_length: int = 500) -> str:
        sentences = []
        for node in nodes:
            content = str(node.get("content") or "")
            first_sentence = content.split(".")[0].strip()
            if len(first_sentence) > 20:
                sentences.append(first_sentence + ".")
        combined = " ".join(sentences)
        return combined[:max_length] if len(combined) > max_length else combined
