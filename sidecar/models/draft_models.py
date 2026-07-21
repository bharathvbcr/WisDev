"""Shared Draft API models."""

from __future__ import annotations

from typing import Optional

from pydantic import BaseModel, Field


class PaperReference(BaseModel):
    paper_id: str
    doi: Optional[str] = None
    title: str
    abstract: Optional[str] = None
    authors: list[str] = Field(default_factory=list)
    year: Optional[int] = None


class OutlineSection(BaseModel):
    id: str
    title: str
    target_words: int
    subsections: list['OutlineSection'] = Field(default_factory=list)
    suggested_papers: list[str] = Field(default_factory=list)
    goal: Optional[str] = None


class GenerateOutlineRequest(BaseModel):
    papers: list[PaperReference]
    document_type: str
    target_word_count: int = Field(default=10000, ge=1000, le=25000)
    custom_sections: Optional[list[str]] = None
    citation_style: str = Field(default='apa')


class GenerateOutlineResponse(BaseModel):
    draft_id: str
    outline: OutlineSection
    estimated_time_minutes: int
    total_target_words: int


class StreamSectionRequest(BaseModel):
    draft_id: str
    section_id: str
    section_title: Optional[str] = None
    section_goal: Optional[str] = None
    papers: list[PaperReference]
    outline: OutlineSection
    previous_sections: list[str] = Field(default_factory=list)
    citation_style: str = Field(default='apa')
    context_chunks: list[dict] = Field(default_factory=list)


class RegenerateSectionRequest(BaseModel):
    draft_id: str
    section_id: str
    feedback: Optional[str] = None


class ExpandSectionRequest(BaseModel):
    draft_id: str
    section_id: str
    additional_words: int = Field(default=500, ge=100, le=2000)
    focus_area: Optional[str] = None


class GuardrailsRequest(BaseModel):
    draft_id: str
    sections: list[dict]
    papers: list[PaperReference]


class CitationVerification(BaseModel):
    passed: bool
    invalid_citations: list[str]


class PlagiarismResult(BaseModel):
    passed: bool
    flagged_sections: list[dict]


class UnsupportedClaim(BaseModel):
    claim: str
    section_id: str
    citation_number: Optional[int] = None
    reason: str


class FabricatedItem(BaseModel):
    type: str
    text: str
    section_id: str
    reason: str


class FactualGrounding(BaseModel):
    passed: bool
    unsupported_claims: list[UnsupportedClaim]


class HallucinationDetection(BaseModel):
    passed: bool
    fabricated_items: list[FabricatedItem]


class GuardrailsResponse(BaseModel):
    draft_id: str
    overall_passed: bool
    citation_verification: CitationVerification
    plagiarism_check: PlagiarismResult
    factual_grounding: FactualGrounding
    hallucination_detection: HallucinationDetection


class DraftStatus(BaseModel):
    draft_id: str
    status: str
    current_section: int
    total_sections: int
    word_count: int
    target_word_count: int
    progress_percent: float


OutlineSection.model_rebuild()

