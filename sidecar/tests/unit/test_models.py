"""Tests for all Pydantic model files in models/."""

import pytest
from pydantic import ValidationError


# ---------------------------------------------------------------------------
# draft_models
# ---------------------------------------------------------------------------

from models.draft_models import (
    PaperReference,
    OutlineSection,
    GenerateOutlineRequest,
    GenerateOutlineResponse,
    StreamSectionRequest,
    RegenerateSectionRequest,
    ExpandSectionRequest,
    GuardrailsRequest,
    CitationVerification,
    PlagiarismResult,
    UnsupportedClaim,
    FabricatedItem,
    FactualGrounding,
    HallucinationDetection,
    GuardrailsResponse,
    DraftStatus,
)


class TestPaperReference:
    def test_required_fields(self):
        p = PaperReference(paper_id="p1", title="Deep Learning")
        assert p.paper_id == "p1"
        assert p.title == "Deep Learning"

    def test_optional_defaults(self):
        p = PaperReference(paper_id="p1", title="T")
        assert p.doi is None
        assert p.abstract is None
        assert p.authors == []
        assert p.year is None

    def test_full_fields(self):
        p = PaperReference(
            paper_id="p1",
            doi="10.1234/xyz",
            title="Study",
            abstract="Abstract text",
            authors=["Smith", "Jones"],
            year=2023,
        )
        assert p.doi == "10.1234/xyz"
        assert p.authors == ["Smith", "Jones"]
        assert p.year == 2023


class TestOutlineSection:
    def test_basic(self):
        s = OutlineSection(id="s1", title="Introduction", target_words=500)
        assert s.id == "s1"
        assert s.target_words == 500
        assert s.subsections == []
        assert s.suggested_papers == []
        assert s.goal is None

    def test_nested_subsections(self):
        child = OutlineSection(id="s1.1", title="Background", target_words=200)
        parent = OutlineSection(
            id="s1", title="Introduction", target_words=500, subsections=[child]
        )
        assert len(parent.subsections) == 1
        assert parent.subsections[0].id == "s1.1"

    def test_with_suggested_papers(self):
        s = OutlineSection(
            id="s1", title="Methods", target_words=300, suggested_papers=["p1", "p2"]
        )
        assert s.suggested_papers == ["p1", "p2"]


class TestGenerateOutlineRequest:
    def _paper(self):
        return PaperReference(paper_id="p1", title="T")

    def test_defaults(self):
        req = GenerateOutlineRequest(papers=[self._paper()], document_type="review")
        assert req.target_word_count == 10000
        assert req.citation_style == "apa"
        assert req.custom_sections is None

    def test_word_count_min(self):
        req = GenerateOutlineRequest(
            papers=[self._paper()], document_type="essay", target_word_count=1000
        )
        assert req.target_word_count == 1000

    def test_word_count_max(self):
        req = GenerateOutlineRequest(
            papers=[self._paper()], document_type="essay", target_word_count=25000
        )
        assert req.target_word_count == 25000

    def test_word_count_below_min_raises(self):
        with pytest.raises(ValidationError):
            GenerateOutlineRequest(
                papers=[self._paper()], document_type="essay", target_word_count=999
            )

    def test_word_count_above_max_raises(self):
        with pytest.raises(ValidationError):
            GenerateOutlineRequest(
                papers=[self._paper()], document_type="essay", target_word_count=25001
            )


class TestGenerateOutlineResponse:
    def test_fields(self):
        outline = OutlineSection(id="s1", title="Intro", target_words=500)
        resp = GenerateOutlineResponse(
            draft_id="d1",
            outline=outline,
            estimated_time_minutes=12,
            total_target_words=5000,
        )
        assert resp.draft_id == "d1"
        assert resp.outline.title == "Intro"


class TestStreamSectionRequest:
    def test_defaults(self):
        paper = PaperReference(paper_id="p1", title="T")
        outline = OutlineSection(id="s1", title="Intro", target_words=500)
        req = StreamSectionRequest(
            draft_id="d1",
            section_id="s1",
            papers=[paper],
            outline=outline,
        )
        assert req.previous_sections == []
        assert req.citation_style == "apa"
        assert req.context_chunks == []


class TestRegenerateSectionRequest:
    def test_feedback_optional(self):
        req = RegenerateSectionRequest(draft_id="d1", section_id="s1")
        assert req.feedback is None


class TestExpandSectionRequest:
    def test_defaults(self):
        req = ExpandSectionRequest(draft_id="d1", section_id="s1")
        assert req.additional_words == 500

    def test_min_additional_words(self):
        req = ExpandSectionRequest(draft_id="d1", section_id="s1", additional_words=100)
        assert req.additional_words == 100

    def test_below_min_raises(self):
        with pytest.raises(ValidationError):
            ExpandSectionRequest(draft_id="d1", section_id="s1", additional_words=99)

    def test_above_max_raises(self):
        with pytest.raises(ValidationError):
            ExpandSectionRequest(draft_id="d1", section_id="s1", additional_words=2001)


class TestGuardrailsModels:
    def test_guardrails_request(self):
        paper = PaperReference(paper_id="p1", title="T")
        req = GuardrailsRequest(
            draft_id="d1",
            sections=[{"id": "s1", "content": "text"}],
            papers=[paper],
        )
        assert req.draft_id == "d1"

    def test_citation_verification(self):
        cv = CitationVerification(passed=True, invalid_citations=[])
        assert cv.passed is True

    def test_plagiarism_result(self):
        pr = PlagiarismResult(passed=False, flagged_sections=[{"s": "x"}])
        assert pr.passed is False

    def test_guardrails_response(self):
        resp = GuardrailsResponse(
            draft_id="d1",
            overall_passed=True,
            citation_verification=CitationVerification(passed=True, invalid_citations=[]),
            plagiarism_check=PlagiarismResult(passed=True, flagged_sections=[]),
            factual_grounding=FactualGrounding(passed=True, unsupported_claims=[]),
            hallucination_detection=HallucinationDetection(passed=True, fabricated_items=[]),
        )
        assert resp.overall_passed is True


class TestDraftStatus:
    def test_draft_status_fields(self):
        ds = DraftStatus(
            draft_id="d1",
            status="in_progress",
            current_section=2,
            total_sections=5,
            word_count=1500,
            target_word_count=5000,
            progress_percent=30.0,
        )
        assert ds.draft_id == "d1"
        assert ds.progress_percent == 30.0


class TestVerifyClaimsModels:
    def test_request_defaults(self):
        req = VerifyClaimsRequest(session_id="sess-1", candidate_output={"text": "x"})
        assert req.mode == "rerank"

    def test_response_fields(self):
        resp = VerifyClaimsResponse(
            score=0.9,
            confidence_report={"status": "ok"},
            reward_components={"faithfulness": 0.8},
        )
        assert resp.score == 0.9


# ---------------------------------------------------------------------------
# rag_models
# ---------------------------------------------------------------------------

from models.rag_models import (
    VerifyClaimsRequest,
    VerifyClaimsResponse,
    AgenticHybridEndpointRequest,
    EvidenceGateEndpointSource,
    EvidenceGateEndpointRequest,
    SearchFilters,
    SearchRequest,
    PaperResult,
    SearchResponse,
    RerankRequest,
    RerankResponse,
    RerankItem,
    SmallToBigChunkRequest,
    AdaptiveChunkRequest,
    PdfExtractJsonRequest,
)


class TestSearchRequest:
    def test_defaults(self):
        req = SearchRequest(queries=["cancer"])
        assert req.max_results == 100
        assert req.use_cag_cache is True

    def test_query_max_results_bounds(self):
        req = SearchRequest(queries=["q"], max_results=10)
        assert req.max_results == 10

        req = SearchRequest(queries=["q"], max_results=500)
        assert req.max_results == 500

    def test_max_results_below_min_raises(self):
        with pytest.raises(ValidationError):
            SearchRequest(queries=["q"], max_results=9)

    def test_max_results_above_max_raises(self):
        with pytest.raises(ValidationError):
            SearchRequest(queries=["q"], max_results=501)

    def test_queries_must_be_non_empty(self):
        with pytest.raises(ValidationError):
            SearchRequest(queries=[])

    def test_queries_max_15(self):
        queries = [f"query {i}" for i in range(15)]
        req = SearchRequest(queries=queries)
        assert len(req.queries) == 15

    def test_queries_above_15_raises(self):
        with pytest.raises(ValidationError):
            SearchRequest(queries=[f"q{i}" for i in range(16)])


class TestSearchFilters:
    def test_defaults(self):
        filters = SearchFilters()
        assert filters.domains == []
        assert filters.study_types == []
        assert filters.exclude_terms == []
        assert filters.min_citations is None


class TestPaperResultAndResponses:
    def test_paper_result_defaults(self):
        paper = PaperResult(
            paper_id="p1",
            title="Study",
            relevance_score=0.8,
            source="pubmed",
        )
        assert paper.authors == []
        assert paper.chunks == []

    def test_search_response_fields(self):
        paper = PaperResult(
            paper_id="p1",
            title="Study",
            relevance_score=0.8,
            source="pubmed",
        )
        resp = SearchResponse(
            papers=[paper],
            total_found=1,
            cag_cache_hit=False,
            search_time_ms=123,
            queries_executed=2,
            sources_queried=["pubmed"],
        )
        assert resp.total_found == 1
        assert resp.sources_queried == ["pubmed"]


class TestRerankRequest:
    def test_defaults(self):
        req = RerankRequest(query="test", papers=[])
        assert req.top_k == 30

    def test_top_k_bounds(self):
        req = RerankRequest(query="q", papers=[], top_k=5)
        assert req.top_k == 5
        req = RerankRequest(query="q", papers=[], top_k=100)
        assert req.top_k == 100

    def test_top_k_below_min_raises(self):
        with pytest.raises(ValidationError):
            RerankRequest(query="q", papers=[], top_k=4)

    def test_top_k_above_max_raises(self):
        with pytest.raises(ValidationError):
            RerankRequest(query="q", papers=[], top_k=101)


class TestRerankItem:
    def test_relevance_score_bounds(self):
        item = RerankItem(paper_id="p1", relevance_score=0.0)
        assert item.relevance_score == 0.0
        item = RerankItem(paper_id="p1", relevance_score=1.0)
        assert item.relevance_score == 1.0

    def test_relevance_score_below_zero_raises(self):
        with pytest.raises(ValidationError):
            RerankItem(paper_id="p1", relevance_score=-0.01)

    def test_relevance_score_above_one_raises(self):
        with pytest.raises(ValidationError):
            RerankItem(paper_id="p1", relevance_score=1.01)


class TestRerankResponse:
    def test_fields(self):
        paper = PaperResult(
            paper_id="p1",
            title="Study",
            relevance_score=0.8,
            source="pubmed",
        )
        resp = RerankResponse(papers=[paper], rerank_time_ms=75)
        assert resp.rerank_time_ms == 75


from models.rag_models import ScoreBreakdown, ExplainabilityResponse, VectorIndexRequest, VectorSearchRequest


class TestExplainabilityModels:
    def test_score_breakdown_optional_rerank(self):
        score = ScoreBreakdown(
            semantic_score=0.8,
            keyword_score=0.7,
            citation_score=0.6,
            recency_score=0.5,
            rrf_score=0.9,
            final_score=0.85,
        )
        assert score.rerank_score is None

    def test_explainability_response_fields(self):
        score = ScoreBreakdown(
            semantic_score=0.8,
            keyword_score=0.7,
            citation_score=0.6,
            recency_score=0.5,
            rrf_score=0.9,
            final_score=0.85,
        )
        resp = ExplainabilityResponse(
            paper_id="p1",
            query_terms_matched=["cancer"],
            abstract_highlights=["important text"],
            score_breakdown=score,
            reasoning="Good match",
        )
        assert resp.score_breakdown.final_score == 0.85


class TestVectorModels:
    def test_vector_index_request_fields(self):
        req = VectorIndexRequest(documents=[{"id": "d1"}])
        assert req.documents == [{"id": "d1"}]

    def test_vector_search_request_defaults(self):
        req = VectorSearchRequest(vector=[0.1, 0.2])
        assert req.top_k == 10
        assert req.filter is None


class TestEvidenceGateEndpointRequest:
    def _source(self):
        return EvidenceGateEndpointSource(title="Paper Title")

    def test_synthesis_text_min_length(self):
        req = EvidenceGateEndpointRequest(
            synthesis_text="a" * 10,
            sources=[self._source()],
        )
        assert len(req.synthesis_text) == 10

    def test_synthesis_text_too_short_raises(self):
        with pytest.raises(ValidationError):
            EvidenceGateEndpointRequest(
                synthesis_text="short",
                sources=[self._source()],
            )

    def test_synthesis_text_too_long_raises(self):
        with pytest.raises(ValidationError):
            EvidenceGateEndpointRequest(
                synthesis_text="x" * 50_001,
                sources=[self._source()],
            )

    def test_sources_empty_raises(self):
        with pytest.raises(ValidationError):
            EvidenceGateEndpointRequest(
                synthesis_text="a" * 10,
                sources=[],
            )

    def test_source_optional_fields(self):
        s = EvidenceGateEndpointSource(title="T", paperId="abc", doi="10.x")
        assert s.paperId == "abc"


class TestAgenticHybridEndpointRequest:
    def test_defaults(self):
        req = AgenticHybridEndpointRequest(query="cancer research")
        assert req.domain == "general"
        assert req.max_iterations == 3
        assert req.limit == 50

    def test_query_length_bounds(self):
        req = AgenticHybridEndpointRequest(query="a")
        assert req.query == "a"

    def test_query_too_long_raises(self):
        with pytest.raises(ValidationError):
            AgenticHybridEndpointRequest(query="x" * 2001)


class TestSmallToBigChunkRequest:
    def test_defaults(self):
        req = SmallToBigChunkRequest(text="some text content here")
        assert req.small_chunk_size == 256
        assert req.big_chunk_size == 1024
        assert req.overlap == 32


class TestAdaptiveChunkRequest:
    def test_defaults(self):
        req = AdaptiveChunkRequest()
        assert req.initial_size == 2048
        assert req.min_size == 512
        assert req.preserve_sections is True


class TestPdfExtractJsonRequest:
    def test_default_filename(self):
        req = PdfExtractJsonRequest(file_base64="abc123")
        assert req.file_name == "document.pdf"

    def test_custom_filename(self):
        req = PdfExtractJsonRequest(file_base64="abc123", file_name="paper.pdf")
        assert req.file_name == "paper.pdf"


# ---------------------------------------------------------------------------
# wisdev_models
# ---------------------------------------------------------------------------

from models.wisdev_models import (
    AnalyzeQueryRequest,
    AnalyzeQueryResponse,
    DomainDetectRequest,
    DomainDetectResponse,
    QuestionOption,
    WisDevQuestion,
    GetQuestionsRequest,
    GetQuestionsResponse,
    FollowUpRequest,
    FollowUpResponse,
    RegenerateRequest,
    RegenerateResponse,
    TopicTreeNode,
    GenerateTopicTreeRequest,
    GenerateTopicTreeResponse,
    GenerateQueriesRequest,
    GenerateQueriesResponse,
    FALLBACK_SUBTOPICS,
    DOMAIN_STUDY_TYPES,
)


class TestAnalyzeQueryRequest:
    def test_valid_query(self):
        req = AnalyzeQueryRequest(query="cancer treatment")
        assert req.query == "cancer treatment"
        assert req.user_id is None

    def test_query_too_short_raises(self):
        with pytest.raises(ValidationError):
            AnalyzeQueryRequest(query="ab")

    def test_query_too_long_raises(self):
        with pytest.raises(ValidationError):
            AnalyzeQueryRequest(query="x" * 1001)


class TestAnalyzeQueryResponse:
    def test_ambiguity_score_bounds(self):
        resp = AnalyzeQueryResponse(
            intent="broad_topic",
            complexity="moderate",
            ambiguity_score=0.5,
        )
        assert resp.ambiguity_score == 0.5
        assert resp.cache_hit is False

    def test_ambiguity_score_below_zero_raises(self):
        with pytest.raises(ValidationError):
            AnalyzeQueryResponse(
                intent="x", complexity="simple", ambiguity_score=-0.1
            )

    def test_ambiguity_score_above_one_raises(self):
        with pytest.raises(ValidationError):
            AnalyzeQueryResponse(
                intent="x", complexity="simple", ambiguity_score=1.1
            )


class TestDomainDetectResponse:
    def test_request_valid(self):
        req = DomainDetectRequest(query="cancer treatment")
        assert req.entities is None

    def test_confidence_bounds(self):
        resp = DomainDetectResponse(primary_domain="medicine", confidence=0.9)
        assert resp.confidence == 0.9
        assert resp.cache_hit is False

    def test_confidence_below_zero_raises(self):
        with pytest.raises(ValidationError):
            DomainDetectResponse(primary_domain="cs", confidence=-0.1)


class TestWisDevQuestion:
    def test_defaults(self):
        q = WisDevQuestion(id="q1", type="multi_choice", question="What is your focus?")
        assert q.is_multi_select is False
        assert q.is_required is True
        assert q.options is None
        assert q.help_text is None


class TestQuestionOption:
    def test_optional_fields(self):
        option = QuestionOption(value="v1", label="Label")
        assert option.description is None
        assert option.icon is None
        assert option.relevance_score is None

    def test_relevance_score_bounds(self):
        option = QuestionOption(value="v1", label="Label", relevance_score=1.0)
        assert option.relevance_score == 1.0

        with pytest.raises(ValidationError):
            QuestionOption(value="v1", label="Label", relevance_score=1.1)


class TestTopicTreeNode:
    def test_basic_node(self):
        node = TopicTreeNode(id="n1", label="Cancer")
        assert node.is_selected is True
        assert node.depth == 0
        assert node.children == []

    def test_nested_children(self):
        child = TopicTreeNode(id="n1.1", label="Treatment", depth=1)
        parent = TopicTreeNode(id="n1", label="Cancer", children=[child])
        assert len(parent.children) == 1
        assert parent.children[0].label == "Treatment"


class TestFallbackSubtopics:
    def test_has_six_entries(self):
        assert len(FALLBACK_SUBTOPICS) == 6

    def test_all_have_value_and_label(self):
        for opt in FALLBACK_SUBTOPICS:
            assert opt.value
            assert opt.label


class TestDomainStudyTypes:
    def test_medicine_key_exists(self):
        assert "medicine" in DOMAIN_STUDY_TYPES

    def test_cs_key_exists(self):
        assert "cs" in DOMAIN_STUDY_TYPES

    def test_default_key_exists(self):
        assert "default" in DOMAIN_STUDY_TYPES

    def test_medicine_has_options(self):
        assert len(DOMAIN_STUDY_TYPES["medicine"]) > 0


class TestQuestionFlowModels:
    def test_get_questions_request_defaults(self):
        req = GetQuestionsRequest(query="cancer treatment", question_id="q1")
        assert req.previous_answers == {}
        assert req.query_analysis is None
        assert req.user_history is None
        assert req.seed_terms is None

    def test_get_questions_response_defaults(self):
        question = WisDevQuestion(id="q1", type="single", question="Pick one")
        resp = GetQuestionsResponse(question=question)
        assert resp.is_ai_generated is True
        assert resp.source == "ai"
        assert resp.cache_hit is False

    def test_followup_request_and_response(self):
        req = FollowUpRequest(query="cancer treatment", answers={"q1": ["a"]})
        assert req.query_analysis is None

        resp = FollowUpResponse(needs_followup=True)
        assert resp.reason is None
        assert resp.followup_question is None

    def test_regenerate_request_and_response(self):
        req = RegenerateRequest(
            query="cancer treatment",
            question_id="q1",
            previous_options=["a", "b"],
        )
        assert req.feedback is None
        assert req.seed_terms is None

        resp = RegenerateResponse(
            options=[QuestionOption(value="a", label="A")],
            explanation="updated",
        )
        assert len(resp.options) == 1

    def test_topic_tree_and_query_generation_models(self):
        node = TopicTreeNode(id="n1", label="Cancer")
        tree_resp = GenerateTopicTreeResponse(
            tree=node,
            summary="summary",
            estimated_papers=42,
        )
        assert tree_resp.estimated_papers == 42

        tree_req = GenerateTopicTreeRequest(
            query="cancer treatment",
            answers={"q1": ["a"]},
            domain="medicine",
        )
        assert tree_req.domain == "medicine"

        query_req = GenerateQueriesRequest(
            tree=node,
            domain="medicine",
            scope="broad",
            timeframe="recent",
        )
        assert query_req.exclusions == []

        query_resp = GenerateQueriesResponse(
            queries=["cancer treatment"],
            query_count=1,
            estimated_results=100,
        )
        assert query_resp.query_count == 1


# ---------------------------------------------------------------------------
# export_models
# ---------------------------------------------------------------------------

from models.export_models import (
    BibliographyEntry,
    DraftContent,
    ExportOptions,
    ExportRequest,
    ExportResponse,
)


class TestExportOptions:
    def test_defaults(self):
        opts = ExportOptions()
        assert opts.citation_style == "apa"
        assert opts.paper_size == "letter"
        assert opts.font_family == "times"
        assert opts.font_size == 12
        assert opts.line_spacing == 1.5
        assert opts.include_toc is True
        assert opts.include_page_numbers is True

    def test_custom_values(self):
        opts = ExportOptions(citation_style="mla", font_size=11, line_spacing=2.0)
        assert opts.citation_style == "mla"
        assert opts.font_size == 11


class TestBibliographyEntry:
    def test_required_fields(self):
        entry = BibliographyEntry(paper_id="p1", title="Study", authors=["Smith"])
        assert entry.paper_id == "p1"
        assert entry.authors == ["Smith"]

    def test_optional_fields(self):
        entry = BibliographyEntry(
            paper_id="p1", title="T", authors=[], doi="10.x", year=2022, venue="Nature"
        )
        assert entry.doi == "10.x"
        assert entry.year == 2022


class TestDraftContent:
    def test_basic(self):
        bib = BibliographyEntry(paper_id="p1", title="T", authors=[])
        dc = DraftContent(title="My Paper", sections=[{"id": "s1"}], bibliography=[bib])
        assert dc.title == "My Paper"
        assert dc.images == []


class TestExportRequest:
    def test_default_options(self):
        bib = BibliographyEntry(paper_id="p1", title="T", authors=[])
        content = DraftContent(title="T", sections=[], bibliography=[bib])
        req = ExportRequest(draft_id="d1", content=content)
        assert req.options.citation_style == "apa"
        assert req.gate == {}


class TestExportResponse:
    def test_fields(self):
        resp = ExportResponse(filename="paper.docx", content_type="application/docx", size_bytes=12345)
        assert resp.filename == "paper.docx"
        assert resp.size_bytes == 12345


# ---------------------------------------------------------------------------
# images_models
# ---------------------------------------------------------------------------

from models.images_models import (
    DraftSection,
    ImageSuggestion,
    SuggestImagesRequest,
    SuggestImagesResponse,
    GenerateImageRequest,
    GeneratedImage,
    ImageStatus,
)


class TestGenerateImageRequest:
    def test_defaults(self):
        req = GenerateImageRequest(
            suggestion_id="s1", prompt="A diagram of...", image_type="diagram"
        )
        assert req.style == "academic"
        assert req.aspect_ratio == "16:9"

    def test_custom_style(self):
        req = GenerateImageRequest(
            suggestion_id="s1", prompt="chart", image_type="chart",
            style="minimal", aspect_ratio="4:3"
        )
        assert req.style == "minimal"
        assert req.aspect_ratio == "4:3"


class TestImageStatus:
    def test_basic(self):
        status = ImageStatus(image_id="img1", status="pending")
        assert status.progress_percent is None
        assert status.image_url is None
        assert status.error is None

    def test_with_error(self):
        status = ImageStatus(image_id="img1", status="failed", error="Timeout")
        assert status.error == "Timeout"

    def test_complete_status(self):
        status = ImageStatus(
            image_id="img1",
            status="complete",
            progress_percent=100.0,
            image_url="https://example.com/img.png",
        )
        assert status.image_url is not None


class TestImageSuggestion:
    def test_fields(self):
        sug = ImageSuggestion(
            id="s1",
            section_id="sec1",
            type="diagram",
            suggested_prompt="Flow chart of...",
            placement="after_paragraph",
            paragraph_index=2,
            reasoning="Helps illustrate",
        )
        assert sug.paragraph_index == 2


class TestSuggestImagesResponse:
    def test_empty(self):
        resp = SuggestImagesResponse(suggestions=[], total_suggestions=0)
        assert resp.total_suggestions == 0

    def test_with_suggestions(self):
        sug = ImageSuggestion(
            id="s1", section_id="sec1", type="chart",
            suggested_prompt="A bar chart", placement="inline",
            paragraph_index=1, reasoning="Data visualization",
        )
        resp = SuggestImagesResponse(suggestions=[sug], total_suggestions=1)
        assert len(resp.suggestions) == 1


class TestImageRequestAndGeneratedImage:
    def test_suggest_images_request_fields(self):
        section = DraftSection(section_id="s1", title="Intro", content="Background")
        req = SuggestImagesRequest(
            draft_id="d1",
            sections=[section],
            document_type="review",
        )
        assert req.sections[0].section_id == "s1"

    def test_generated_image_fields(self):
        img = GeneratedImage(
            image_id="img1",
            suggestion_id="s1",
            image_url="https://example.com/img.png",
            prompt="diagram",
            width=800,
            height=600,
            generation_time_ms=321,
        )
        assert img.generation_time_ms == 321


class TestDraftSection:
    def test_fields(self):
        sec = DraftSection(section_id="s1", title="Introduction", content="Background...")
        assert sec.section_id == "s1"
        assert "Background" in sec.content
