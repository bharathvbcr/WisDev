from __future__ import annotations

from unittest.mock import AsyncMock, patch

import pytest

from services.ai_generation_service import ModelSelectionStrategy
from services.idea_generation_service import IdeaGenerationResponse, IdeaGenerationService
import services.idea_generation_service as idea_generation_service


def _fixture_idea_response():
        return IdeaGenerationResponse.model_validate(
            {
                "ideas": [
                {
                    "id": "idea-1",
                    "title": "Novel idea",
                    "description": "A compact summary of a novel direction.",
                    "novelty_score": 0.9,
                    "feasibility_score": 0.75,
                    "reasoning": "Reasoned from literature and project context.",
                    "hypotheses": ["Hypothesis A", "Hypothesis B"],
                }
            ],
                # Use the published response alias required by the model schema.
                "thoughtSignature": "sig-123",
        }
    )


@pytest.mark.asyncio
async def test_generate_ideas_uses_heavy_generation_and_returns_parsed_response(monkeypatch):
    service = IdeaGenerationService()

    async def fake_generate_json(
        prompt: str,
        response_model,
        complexity_score: float,
        strategy=ModelSelectionStrategy.ALWAYS_LIGHT,
    ):
        assert response_model is IdeaGenerationResponse
        assert complexity_score == 0.9
        assert strategy is ModelSelectionStrategy.ALWAYS_HEAVY
        assert "Project Context" in prompt
        return _fixture_idea_response()

    with patch.object(idea_generation_service, "ai_generation_service") as mocked_agent:
        mocked_agent.generate_json = AsyncMock(side_effect=fake_generate_json)
        result = await service.generate_ideas(
            query="future-facing model",
            literature=[
                {"title": "Paper 1", "abstract": "First paper abstract."},
                {"title": "Paper 2", "abstract": "Second paper abstract."},
            ],
            project_context="Project context details",
            existing_context="",
            thought_signature="client",
        )

    assert result.thought_signature == "sig-123"
    assert len(result.ideas) == 1
    assert result.ideas[0].title == "Novel idea"


def test_summarize_literature_uses_top_fifteen_items_and_truncates_abstract():
    service = IdeaGenerationService()
    literature = [
        {"title": f"Paper {idx}", "abstract": f"Abstract text {idx} {'x' * 400}"}
        for idx in range(16)
    ]

    summary = service._summarize_literature(literature)

    assert summary.count("\n") == 14
    assert "Paper 15" not in summary
    assert "Paper 0" in summary
    assert "Paper 14" in summary


def test_summarize_literature_uses_default_title_and_abstract_values():
    service = IdeaGenerationService()
    summary = service._summarize_literature([{}])
    assert summary == "[1] Untitled: No abstract available..."


def test_summarize_literature_empty_input():
    service = IdeaGenerationService()
    assert service._summarize_literature([]) == ""
