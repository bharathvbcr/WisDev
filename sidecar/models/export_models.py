"""Shared Export API models."""

from __future__ import annotations

from typing import Optional

from pydantic import BaseModel, Field


class BibliographyEntry(BaseModel):
    paper_id: str
    doi: Optional[str] = None
    title: str
    authors: list[str]
    year: Optional[int] = None
    venue: Optional[str] = None
    url: Optional[str] = None


class DraftContent(BaseModel):
    title: str
    sections: list[dict]
    bibliography: list[BibliographyEntry]
    images: list[dict] = Field(default_factory=list)


class ExportOptions(BaseModel):
    citation_style: str = Field(default='apa')
    include_toc: bool = True
    include_abstract: bool = True
    include_ai_disclosure: bool = True
    paper_size: str = Field(default='letter')
    font_family: str = Field(default='times')
    font_size: int = Field(default=12)
    line_spacing: float = Field(default=1.5)
    include_page_numbers: bool = True
    include_headers: bool = True


class ExportRequest(BaseModel):
    draft_id: str
    content: DraftContent
    options: ExportOptions = Field(default_factory=ExportOptions)
    gate: dict = Field(default_factory=dict)


class ExportResponse(BaseModel):
    filename: str
    content_type: str
    size_bytes: int

