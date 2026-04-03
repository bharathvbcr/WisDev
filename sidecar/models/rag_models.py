"""Shared RAG API models for Container Service routes."""

from __future__ import annotations

from typing import Any, Dict, List, Optional

from pydantic import BaseModel, Field


class VerifyClaimsRequest(BaseModel):
    session_id: str
    candidate_output: dict
    mode: str = 'rerank'


class VerifyClaimsResponse(BaseModel):
    score: float
    confidence_report: dict
    reward_components: dict


class AgenticHybridEndpointRequest(BaseModel):
    query: str = Field(..., min_length=1, max_length=2000)
    domain: str = 'general'
    max_iterations: int = Field(default=3, ge=1, le=6)
    limit: int = Field(default=50, ge=1, le=200)
    session_id: Optional[str] = None
    retrieval_mode: Optional[str] = None
    fusion_mode: Optional[str] = None
    latency_budget_ms: Optional[int] = None


class EvidenceGateEndpointSource(BaseModel):
    paperId: Optional[str] = None
    id: Optional[str] = None
    doi: Optional[str] = None
    title: str = ''
    abstract: Optional[str] = None
    summary: Optional[str] = None


class EvidenceGateEndpointRequest(BaseModel):
    synthesis_text: str = Field(..., min_length=10, max_length=50_000)
    sources: List[EvidenceGateEndpointSource] = Field(default_factory=list, min_length=1, max_length=200)


class SearchFilters(BaseModel):
    domains: list[str] = Field(default_factory=list)
    year_start: Optional[int] = None
    year_end: Optional[int] = None
    study_types: list[str] = Field(default_factory=list)
    exclude_terms: list[str] = Field(default_factory=list)
    min_citations: Optional[int] = None


class SearchRequest(BaseModel):
    queries: list[str] = Field(..., min_length=1, max_length=15)
    filters: SearchFilters = Field(default_factory=SearchFilters)
    max_results: int = Field(default=100, ge=10, le=500)
    use_cag_cache: bool = True
    intent_hash: Optional[str] = None


class PaperResult(BaseModel):
    paper_id: str
    doi: Optional[str] = None
    title: str
    abstract: Optional[str] = None
    authors: list[str] = Field(default_factory=list)
    year: Optional[int] = None
    venue: Optional[str] = None
    citation_count: Optional[int] = None
    relevance_score: float
    source: str
    chunks: list[str] = Field(default_factory=list)


class SearchResponse(BaseModel):
    papers: list[PaperResult]
    total_found: int
    cag_cache_hit: bool
    search_time_ms: int
    queries_executed: int
    sources_queried: list[str]


class RerankRequest(BaseModel):
    query: str
    papers: list[PaperResult]
    top_k: int = Field(default=30, ge=5, le=100)


class RerankResponse(BaseModel):
    papers: list[PaperResult]
    rerank_time_ms: int


class RerankItem(BaseModel):
    paper_id: str
    relevance_score: float = Field(..., ge=0, le=1)
    reasoning: Optional[str] = None


class ScoreBreakdown(BaseModel):
    semantic_score: float
    keyword_score: float
    citation_score: float
    recency_score: float
    rrf_score: float
    rerank_score: Optional[float] = None
    final_score: float


class ExplainabilityResponse(BaseModel):
    paper_id: str
    query_terms_matched: list[str]
    abstract_highlights: list[str]
    score_breakdown: ScoreBreakdown
    reasoning: str


class SmallToBigChunkRequest(BaseModel):
    text: str
    small_chunk_size: int = 256
    big_chunk_size: int = 1024
    overlap: int = 32


class AdaptiveChunkRequest(BaseModel):
    text: Optional[str] = None
    paper_id: Optional[str] = None
    papers: Optional[List[Dict[str, Any]]] = None
    initial_size: int = 2048
    min_size: int = 512
    overlap: int = 100
    preserve_sections: bool = True


class PdfExtractJsonRequest(BaseModel):
    file_base64: str
    file_name: str = 'document.pdf'


class VectorIndexRequest(BaseModel):
    documents: List[Dict[str, Any]]


class VectorSearchRequest(BaseModel):
    vector: List[float]
    top_k: int = 10
    filter: Optional[Dict[str, Any]] = None

