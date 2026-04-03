"""
BM25 Lexical Search Service

Provides BM25 (Okapi) lexical search for academic papers.
BM25 complements vector search by catching exact terms that
semantic similarity may miss (paper IDs, gene names, formulas).
"""

from rank_bm25 import BM25Okapi
from typing import List, Tuple, Optional
import logging
import re

logger = logging.getLogger(__name__)


def tokenize(text: str) -> List[str]:
    """
    Tokenize text for BM25 indexing.

    Simple whitespace tokenization with lowercasing and
    basic punctuation handling. Preserves alphanumeric tokens.
    """
    # Lowercase and split on whitespace
    text = text.lower()
    # Keep alphanumeric chars, hyphens, and underscores
    tokens = re.findall(r'[\w\-]+', text)
    # Filter very short tokens (except potential IDs)
    return [t for t in tokens if len(t) > 1 or t.isdigit()]


class BM25Index:
    """BM25 lexical search index for academic papers."""

    def __init__(self):
        self.documents: List[str] = []
        self.doc_ids: List[str] = []
        self.index: Optional[BM25Okapi] = None

    def add_documents(self, docs: List[str], doc_ids: Optional[List[str]] = None) -> int:
        """
        Index documents for BM25 search.

        Args:
            docs: List of document texts to index
            doc_ids: Optional list of document IDs (defaults to index position)

        Returns:
            Number of documents indexed
        """
        if not docs:
            logger.warning("No documents provided for indexing")
            return 0

        self.documents = docs
        self.doc_ids = doc_ids or [str(i) for i in range(len(docs))]

        # Tokenize all documents
        tokenized = [tokenize(doc) for doc in docs]

        # Build BM25 index
        self.index = BM25Okapi(tokenized)

        logger.info(f"BM25 indexed {len(docs)} documents")
        return len(docs)

    def search(self, query: str, top_k: int = 10) -> List[Tuple[str, float, str]]:
        """
        Search indexed documents using BM25.

        Args:
            query: Search query
            top_k: Maximum number of results to return

        Returns:
            List of (doc_id, score, text) tuples sorted by score descending
        """
        if not self.index:
            logger.warning("BM25 index is empty, returning no results")
            return []

        # Tokenize query
        tokenized_query = tokenize(query)

        if not tokenized_query:
            logger.warning("Query tokenized to empty list")
            return []

        # Get BM25 scores for all documents
        scores = self.index.get_scores(tokenized_query)

        # Get top-k indices (sorted by score descending)
        top_indices = scores.argsort()[-top_k:][::-1]

        # Return results with positive scores only
        results = []
        for i in top_indices:
            if scores[i] > 0:
                results.append((
                    self.doc_ids[i],
                    float(scores[i]),
                    self.documents[i]
                ))

        logger.debug(f"BM25 search for '{query[:50]}...' returned {len(results)} results")
        return results

    def clear(self) -> None:
        """Clear the BM25 index."""
        self.documents = []
        self.doc_ids = []
        self.index = None
        logger.info("BM25 index cleared")

    @property
    def size(self) -> int:
        """Return the number of indexed documents."""
        return len(self.documents)

    @property
    def is_empty(self) -> bool:
        """Check if the index is empty."""
        return self.index is None


# Singleton instance for the service
_bm25_index = BM25Index()


def get_bm25_index() -> BM25Index:
    """Get the singleton BM25 index instance."""
    return _bm25_index
