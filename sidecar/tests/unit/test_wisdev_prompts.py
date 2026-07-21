"""Tests for prompts/wisdev_prompts.py."""

import pytest
from prompts.wisdev_prompts import (
    format_query_analysis_prompt,
    format_domain_detection_prompt,
    format_subtopic_prompt,
    format_follow_up_prompt,
    format_study_type_prompt,
    format_regenerate_prompt,
    format_section_refiner_prompt,
    format_section_writer_prompt,
    SECTION_REFINER_PROMPT,
)
from models.wisdev_models import FALLBACK_SUBTOPICS, DOMAIN_STUDY_TYPES


# ---------------------------------------------------------------------------
# format_query_analysis_prompt
# ---------------------------------------------------------------------------

def test_query_analysis_contains_query():
    prompt = format_query_analysis_prompt("cancer immunotherapy")
    assert "cancer immunotherapy" in prompt


def test_query_analysis_returns_string():
    prompt = format_query_analysis_prompt("neural networks")
    assert isinstance(prompt, str)
    assert len(prompt) > 0


def test_query_analysis_mentions_complexity():
    prompt = format_query_analysis_prompt("machine learning")
    assert "Complexity" in prompt or "complexity" in prompt


# ---------------------------------------------------------------------------
# format_domain_detection_prompt
# ---------------------------------------------------------------------------

def test_domain_detection_contains_query():
    prompt = format_domain_detection_prompt("CRISPR gene editing")
    assert "CRISPR gene editing" in prompt


def test_domain_detection_without_entities():
    prompt = format_domain_detection_prompt("quantum computing")
    assert "quantum computing" in prompt
    # No entity context injected
    assert "Previously identified entities" not in prompt


def test_domain_detection_with_entities():
    prompt = format_domain_detection_prompt(
        "cancer treatment", entities=["chemotherapy", "immunotherapy"]
    )
    assert "chemotherapy" in prompt
    assert "immunotherapy" in prompt
    assert "Previously identified entities" in prompt


def test_domain_detection_with_empty_entities():
    # Empty list should behave like None
    prompt = format_domain_detection_prompt("machine learning", entities=[])
    assert "Previously identified entities" not in prompt


# ---------------------------------------------------------------------------
# format_subtopic_prompt
# ---------------------------------------------------------------------------

def test_subtopic_prompt_contains_query():
    prompt = format_subtopic_prompt(
        query="deep learning",
        domain="cs",
        intent="broad_topic",
        entities=["neural network"],
    )
    assert "deep learning" in prompt


def test_subtopic_prompt_contains_count_range():
    prompt = format_subtopic_prompt(
        query="test",
        domain="medicine",
        intent="methodology",
        entities=[],
        min_count=4,
        max_count=10,
    )
    assert "4" in prompt
    assert "10" in prompt


def test_subtopic_prompt_no_history():
    prompt = format_subtopic_prompt(
        query="test query",
        domain="cs",
        intent="broad_topic",
        entities=["AI"],
    )
    assert "past research interests" not in prompt


def test_subtopic_prompt_with_history():
    prompt = format_subtopic_prompt(
        query="deep learning",
        domain="cs",
        intent="broad_topic",
        entities=[],
        user_history=["transformers", "attention mechanisms"],
    )
    assert "past research interests" in prompt
    assert "transformers" in prompt


def test_subtopic_prompt_with_seed_terms():
    prompt = format_subtopic_prompt(
        query="AI safety",
        domain="cs",
        intent="comparison",
        entities=[],
        seed_terms=["alignment", "robustness"],
    )
    assert "alignment" in prompt
    assert "robustness" in prompt


def test_subtopic_prompt_no_entities():
    prompt = format_subtopic_prompt(
        query="climate change",
        domain="climate",
        intent="broad_topic",
        entities=[],
    )
    assert "None identified" in prompt


def test_subtopic_prompt_with_entities():
    prompt = format_subtopic_prompt(
        query="mRNA vaccines",
        domain="medicine",
        intent="methodology",
        entities=["mRNA", "COVID-19"],
    )
    assert "mRNA" in prompt
    assert "COVID-19" in prompt


# ---------------------------------------------------------------------------
# format_follow_up_prompt
# ---------------------------------------------------------------------------

def test_follow_up_contains_query():
    prompt = format_follow_up_prompt(
        query="cancer therapy",
        domain="medicine",
        scope="targeted",
        timeframe="last_5_years",
        ambiguity_score=0.7,
        entities=["lung cancer"],
    )
    assert "cancer therapy" in prompt


def test_follow_up_contains_ambiguity_score():
    prompt = format_follow_up_prompt(
        query="test",
        domain="cs",
        scope="broad",
        timeframe="all_time",
        ambiguity_score=0.3,
        entities=[],
    )
    assert "0.3" in prompt


def test_follow_up_entities_joined():
    prompt = format_follow_up_prompt(
        query="immunology",
        domain="medicine",
        scope="narrow",
        timeframe="recent",
        ambiguity_score=0.5,
        entities=["T-cells", "cytokines"],
    )
    assert "T-cells" in prompt
    assert "cytokines" in prompt


def test_follow_up_no_entities():
    prompt = format_follow_up_prompt(
        query="test",
        domain="cs",
        scope="broad",
        timeframe="all",
        ambiguity_score=0.5,
        entities=[],
    )
    assert "None" in prompt


# ---------------------------------------------------------------------------
# format_study_type_prompt
# ---------------------------------------------------------------------------

def test_study_type_contains_query():
    prompt = format_study_type_prompt(
        query="meta-analysis cancer",
        domain="medicine",
        subtopics=["RCTs", "cohort studies"],
        intent="methodology",
    )
    assert "meta-analysis cancer" in prompt


def test_study_type_contains_domain():
    prompt = format_study_type_prompt(
        query="NLP tasks",
        domain="cs",
        subtopics=["benchmarks"],
        intent="comparison",
    )
    assert "cs" in prompt


def test_study_type_subtopics_joined():
    prompt = format_study_type_prompt(
        query="AI research",
        domain="cs",
        subtopics=["supervised learning", "reinforcement learning"],
        intent="broad_topic",
    )
    assert "supervised learning" in prompt
    assert "reinforcement learning" in prompt


def test_study_type_empty_subtopics():
    prompt = format_study_type_prompt(
        query="biology",
        domain="biology",
        subtopics=[],
        intent="broad_topic",
    )
    assert "None selected" in prompt


# ---------------------------------------------------------------------------
# format_regenerate_prompt
# ---------------------------------------------------------------------------

def test_regenerate_contains_query():
    prompt = format_regenerate_prompt(
        query="neuroscience memory",
        domain="neuro",
        intent="broad_topic",
        previous_options=["synaptic plasticity", "long-term potentiation"],
    )
    assert "neuroscience memory" in prompt


def test_regenerate_includes_previous_options():
    prompt = format_regenerate_prompt(
        query="test",
        domain="cs",
        intent="comparison",
        previous_options=["option_a", "option_b"],
    )
    assert "option_a" in prompt
    assert "option_b" in prompt


def test_regenerate_no_feedback_default():
    prompt = format_regenerate_prompt(
        query="test",
        domain="cs",
        intent="broad_topic",
        previous_options=["x"],
        feedback=None,
    )
    assert "No specific feedback provided" in prompt


def test_regenerate_with_feedback():
    prompt = format_regenerate_prompt(
        query="test",
        domain="biology",
        intent="broad_topic",
        previous_options=["genetics"],
        feedback="More focus on genomics",
    )
    assert "More focus on genomics" in prompt


def test_regenerate_with_seed_terms():
    prompt = format_regenerate_prompt(
        query="protein folding",
        domain="biology",
        intent="methodology",
        previous_options=["MD simulation"],
        seed_terms=["AlphaFold", "cryo-EM"],
    )
    assert "AlphaFold" in prompt
    assert "cryo-EM" in prompt


def test_regenerate_custom_count():
    prompt = format_regenerate_prompt(
        query="test",
        domain="cs",
        intent="broad_topic",
        previous_options=[],
        count=5,
    )
    assert "5" in prompt


# ---------------------------------------------------------------------------
# Static data validation
# ---------------------------------------------------------------------------

def test_fallback_subtopics_count():
    assert len(FALLBACK_SUBTOPICS) == 6


def test_fallback_subtopics_have_values():
    for item in FALLBACK_SUBTOPICS:
        assert item.value
        assert item.label


def test_domain_study_types_has_medicine():
    assert "medicine" in DOMAIN_STUDY_TYPES
    assert len(DOMAIN_STUDY_TYPES["medicine"]) > 0


def test_domain_study_types_has_cs():
    assert "cs" in DOMAIN_STUDY_TYPES


def test_domain_study_types_has_default():
    assert "default" in DOMAIN_STUDY_TYPES


# ---------------------------------------------------------------------------
# format_section_writer_prompt / format_section_refiner_prompt
# ---------------------------------------------------------------------------


def test_section_writer_prompt_uses_default_role_and_no_packets():
    system_prompt, user_prompt = format_section_writer_prompt(
        writer_role="unknown",
        section_goal="Summarize the evidence",
        claim_packets=[],
        source_titles=[],
    )

    assert "disciplined academic prose" in system_prompt
    assert "No claim packets were provided." in user_prompt
    assert "Source titles: None provided" in user_prompt


def test_section_writer_prompt_formats_packets_and_limits_spans():
    system_prompt, user_prompt = format_section_writer_prompt(
        writer_role="results_writer",
        section_goal="Describe the outcomes",
        claim_packets=[
            {
                "packetId": "packet_1",
                "claimText": "A claim",
                "verifierStatus": "accepted",
                "verifierNotes": ["note one", "note two"],
                "evidenceSpans": [
                    {"sourceCanonicalId": "src1", "snippet": "snippet 1"},
                    {"sourceCanonicalId": "src2", "snippet": "snippet 2"},
                    {"sourceCanonicalId": "src3", "snippet": "snippet 3"},
                    {"sourceCanonicalId": "src4", "snippet": "snippet 4"},
                ],
            }
        ],
        source_titles=["Paper A", "Paper B"],
    )

    assert "Use objective quantitative language" in system_prompt
    assert "Packet ID: packet_1" in user_prompt
    assert "- src1: snippet 1" in user_prompt
    assert "- src3: snippet 3" in user_prompt
    assert "snippet 4" not in user_prompt
    assert "Notes: note one, note two" in user_prompt


def test_section_refiner_prompt_uses_defaults_when_content_missing():
    system_prompt, user_prompt = format_section_refiner_prompt(
        original_content="",
        unresolved_issues=[],
        claim_packets=[],
    )

    assert system_prompt == SECTION_REFINER_PROMPT
    assert "No existing content provided." in user_prompt
    assert "- No explicit issues provided." in user_prompt
    assert "No grounding packets were provided." in user_prompt
