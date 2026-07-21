"""
Idea Generation Service — Inspired by AI-Scientist and autoresearch.

Responsible for:
1. Novelty checking against project history and retrieved literature.
2. Generating "frontier" research ideas and hypotheses.
3. Ranking ideas based on feasibility, novelty, and interest.
"""

from __future__ import annotations

import json
from typing import Any, List, Optional
from pydantic import BaseModel, Field
import structlog

from services.ai_generation_service import ai_generation_service, ModelSelectionStrategy

logger = structlog.get_logger(__name__)

class ResearchIdea(BaseModel):
    id: str
    title: str
    description: str
    novelty_score: float = Field(..., ge=0, le=1)
    feasibility_score: float = Field(..., ge=0, le=1)
    reasoning: str
    hypotheses: List[str] = Field(default_factory=list)

class IdeaGenerationResponse(BaseModel):
    ideas: List[ResearchIdea]
    thought_signature: str = Field(..., alias="thoughtSignature")

class IdeaGenerationService:
    def __init__(self):
        self.generation_prompt = """
        You are a visionary AI Scientist. Your goal is to generate NOVEL research ideas based on the provided literature and project context.
        
        Project Context:
        {project_context}
        
        Retrieved Literature:
        {literature_summary}
        
        Existing Ideas/Hypotheses:
        {existing_context}
        
        Generate 3-5 research ideas that:
        1. Are grounded in the retrieved literature.
        2. Are demonstrably NOVEL (not just minor extensions).
        3. Address "frontier" gaps in the current knowledge.
        
        For each idea, provide a novelty score, feasibility score, and specific testable hypotheses.
        """

    async def generate_ideas(
        self,
        query: str,
        literature: List[dict[str, Any]],
        project_context: str = "",
        existing_context: str = "",
        thought_signature: str = "",
    ) -> IdeaGenerationResponse:
        """
        Generates research ideas with a focus on novelty and grounded reasoning.
        """
        lit_summary = self._summarize_literature(literature)
        
        prompt = self.generation_prompt.format(
            project_context=project_context or "N/A",
            literature_summary=lit_summary,
            existing_context=existing_context or "N/A"
        )

        # We use 'heavy' model for idea generation as it requires deep synthesis and creativity
        result = await ai_generation_service.generate_json(
            prompt=prompt,
            response_model=IdeaGenerationResponse,
            complexity_score=0.9,
            strategy=ModelSelectionStrategy.ALWAYS_HEAVY
        )
        
        logger.info("ideas_generated", count=len(result.ideas), query=query)
        return result

    def _summarize_literature(self, literature: List[dict[str, Any]]) -> str:
        summary_parts = []
        for i, paper in enumerate(literature[:15]): # Limit to top 15 for context
            title = paper.get("title", "Untitled")
            abstract = paper.get("abstract", "No abstract available")
            summary_parts.append(f"[{i+1}] {title}: {abstract[:300]}...")
        return "\n".join(summary_parts)

idea_generation_service = IdeaGenerationService()
