"""
WisDev Prompts Package
AI prompts for the agentic question flow.
"""

from .wisdev_prompts import (
    QUERY_ANALYSIS_PROMPT,
    DOMAIN_DETECTION_PROMPT,
    SUBTOPIC_GENERATION_PROMPT,
    FOLLOW_UP_DECISION_PROMPT,
    STUDY_TYPE_SUGGESTION_PROMPT,
    format_query_analysis_prompt,
    format_domain_detection_prompt,
    format_subtopic_prompt,
    format_follow_up_prompt,
    format_study_type_prompt,
)

__all__ = [
    "QUERY_ANALYSIS_PROMPT",
    "DOMAIN_DETECTION_PROMPT",
    "SUBTOPIC_GENERATION_PROMPT",
    "FOLLOW_UP_DECISION_PROMPT",
    "STUDY_TYPE_SUGGESTION_PROMPT",
    "format_query_analysis_prompt",
    "format_domain_detection_prompt",
    "format_subtopic_prompt",
    "format_follow_up_prompt",
    "format_study_type_prompt",
]
