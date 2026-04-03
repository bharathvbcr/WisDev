"""Shared WisDev API models and static option payloads."""

from __future__ import annotations

from typing import Optional

from pydantic import BaseModel, Field


class AnalyzeQueryRequest(BaseModel):
    query: str = Field(..., min_length=3, max_length=1000)
    user_id: Optional[str] = None


class AnalyzeQueryResponse(BaseModel):
    intent: str
    entities: list[str] = Field(default_factory=list)
    research_questions: list[str] = Field(default_factory=list)
    complexity: str
    ambiguity_score: float = Field(ge=0.0, le=1.0)
    suggested_domains: list[str] = Field(default_factory=list)
    methodology_hints: list[str] = Field(default_factory=list)
    reasoning: Optional[str] = None
    cache_hit: bool = False


class DomainDetectRequest(BaseModel):
    query: str = Field(..., min_length=3, max_length=1000)
    entities: Optional[list[str]] = None


class DomainDetectResponse(BaseModel):
    primary_domain: str
    secondary_domains: list[str] = Field(default_factory=list)
    confidence: float = Field(ge=0.0, le=1.0)
    entities: list[str] = Field(default_factory=list)
    reasoning: Optional[str] = None
    cache_hit: bool = False


class QuestionOption(BaseModel):
    value: str
    label: str
    description: Optional[str] = None
    icon: Optional[str] = None
    relevance_score: Optional[float] = Field(default=None, ge=0.0, le=1.0)


class WisDevQuestion(BaseModel):
    id: str
    type: str
    question: str
    options: Optional[list[QuestionOption]] = None
    is_multi_select: bool = False
    is_required: bool = True
    help_text: Optional[str] = None


class GetQuestionsRequest(BaseModel):
    query: str = Field(..., min_length=3, max_length=1000)
    question_id: str
    previous_answers: dict[str, list[str]] = Field(default_factory=dict)
    query_analysis: Optional[AnalyzeQueryResponse] = None
    user_history: Optional[list[str]] = None
    seed_terms: Optional[list[str]] = None


class GetQuestionsResponse(BaseModel):
    question: WisDevQuestion
    explanation: Optional[str] = None
    is_ai_generated: bool = True
    source: str = 'ai'
    cache_hit: bool = False


class FollowUpRequest(BaseModel):
    query: str = Field(..., min_length=3, max_length=1000)
    answers: dict[str, list[str]]
    query_analysis: Optional[AnalyzeQueryResponse] = None


class FollowUpResponse(BaseModel):
    needs_followup: bool
    reason: Optional[str] = None
    followup_question: Optional[WisDevQuestion] = None


class RegenerateRequest(BaseModel):
    query: str = Field(..., min_length=3, max_length=1000)
    question_id: str
    previous_options: list[str]
    feedback: Optional[str] = None
    query_analysis: Optional[AnalyzeQueryResponse] = None
    seed_terms: Optional[list[str]] = None


class RegenerateResponse(BaseModel):
    options: list[QuestionOption]
    explanation: str


class TopicTreeNode(BaseModel):
    id: str
    label: str
    children: list['TopicTreeNode'] = Field(default_factory=list)
    is_selected: bool = True
    depth: int = 0


TopicTreeNode.model_rebuild()


class GenerateTopicTreeRequest(BaseModel):
    query: str
    answers: dict[str, list[str]]
    domain: str


class GenerateTopicTreeResponse(BaseModel):
    tree: TopicTreeNode
    summary: str
    estimated_papers: int


class GenerateQueriesRequest(BaseModel):
    tree: TopicTreeNode
    domain: str
    scope: str
    timeframe: str
    exclusions: list[str] = Field(default_factory=list)


class GenerateQueriesResponse(BaseModel):
    queries: list[str]
    query_count: int
    estimated_results: int


FALLBACK_SUBTOPICS = [
    QuestionOption(value='overview', label='Overview & Background', description='Foundational concepts and history'),
    QuestionOption(value='methods', label='Methods & Techniques', description='Methodological approaches'),
    QuestionOption(value='applications', label='Applications', description='Practical uses and implementations'),
    QuestionOption(value='challenges', label='Challenges & Limitations', description='Current problems and gaps'),
    QuestionOption(value='future', label='Future Directions', description='Emerging trends and opportunities'),
    QuestionOption(value='recent_advances', label='Recent Advances', description='Latest developments (2023-2024)'),
]


DOMAIN_STUDY_TYPES = {
    'medicine': [
        QuestionOption(value='rct', label='Randomized Controlled Trials', description='Gold standard clinical evidence'),
        QuestionOption(value='meta_analysis', label='Meta-analyses', description='Statistical synthesis of studies'),
        QuestionOption(value='systematic_review', label='Systematic Reviews', description='Comprehensive literature summaries'),
        QuestionOption(value='cohort', label='Cohort Studies', description='Observational longitudinal studies'),
        QuestionOption(value='clinical_trial', label='Clinical Trials', description='Drug/treatment trials'),
    ],
    'cs': [
        QuestionOption(value='empirical', label='Empirical Studies', description='Experimental evaluations'),
        QuestionOption(value='benchmark', label='Benchmark Papers', description='Standardized comparisons'),
        QuestionOption(value='survey', label='Survey Papers', description='Field overviews'),
        QuestionOption(value='system', label='System Papers', description='Implementation and architecture'),
    ],
    'default': [
        QuestionOption(value='primary', label='Primary Research', description='Original studies'),
        QuestionOption(value='review', label='Review Articles', description='Literature summaries'),
        QuestionOption(value='meta_analysis', label='Meta-analyses', description='Statistical synthesis'),
    ],
}

