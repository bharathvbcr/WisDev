"""Shared Images API models."""

from __future__ import annotations

from typing import Optional

from pydantic import BaseModel, Field


class DraftSection(BaseModel):
    section_id: str
    title: str
    content: str


class ImageSuggestion(BaseModel):
    id: str
    section_id: str
    type: str
    suggested_prompt: str
    placement: str
    paragraph_index: int
    reasoning: str


class SuggestImagesRequest(BaseModel):
    draft_id: str
    sections: list[DraftSection]
    document_type: str


class SuggestImagesResponse(BaseModel):
    suggestions: list[ImageSuggestion]
    total_suggestions: int


class GenerateImageRequest(BaseModel):
    suggestion_id: str
    prompt: str
    image_type: str
    style: str = Field(default='academic')
    aspect_ratio: str = Field(default='16:9')


class GeneratedImage(BaseModel):
    image_id: str
    suggestion_id: str
    image_url: str
    prompt: str
    width: int
    height: int
    generation_time_ms: int


class ImageStatus(BaseModel):
    image_id: str
    status: str
    progress_percent: Optional[float] = None
    image_url: Optional[str] = None
    error: Optional[str] = None

