"""
WisDev AI Prompts
Prompts for the agentic question generation system.

These prompts are designed to:
1. Extract deep insights from research queries
2. Generate contextually relevant questions and options
3. Decide when follow-up clarification is needed
4. Provide explanations for AI suggestions
"""

from typing import Optional

# =============================================================================
# Query Analysis Prompt
# =============================================================================

QUERY_ANALYSIS_PROMPT = """You are an expert research librarian and academic search specialist. Analyze the following research query to understand the user's research intent.

Research Query: "{query}"

Analyze this query and extract:

1. **Intent**: What type of search is this?
   - "broad_topic": General exploration of a research area
   - "specific_paper": Looking for specific papers/authors
   - "methodology": Seeking methods, techniques, or approaches
   - "comparison": Comparing different approaches, treatments, or technologies

2. **Entities**: Key concepts, terms, technologies, diseases, authors, etc. mentioned

3. **Research Questions**: Implicit research questions the user might be trying to answer

4. **Complexity**: How complex is this query?
   - "simple": Single focused topic, clear scope
   - "moderate": Multiple related concepts, some nuance
   - "complex": Multi-faceted, interdisciplinary, or highly specialized

5. **Ambiguity Score**: How ambiguous is the query? (0.0 = crystal clear, 1.0 = very ambiguous)

6. **Suggested Domains**: Which academic domains does this query span?
   Options: medicine, cs, social, climate, neuro, physics, biology, humanities

7. **Methodology Hints**: Any hints about preferred research methods or study types"""


def format_query_analysis_prompt(query: str) -> str:
    """Format the query analysis prompt."""
    return QUERY_ANALYSIS_PROMPT.format(query=query)


# =============================================================================
# Domain Detection Prompt
# =============================================================================

DOMAIN_DETECTION_PROMPT = """You are an expert at classifying academic research queries into research domains.

Research Query: "{query}"
{entity_context}

Classify this query into one or more research domains:
- medicine: Medical research, clinical studies, healthcare, diseases, treatments
- cs: Computer science, AI/ML, software, algorithms, data science
- social: Psychology, sociology, economics, political science, education
- climate: Climate science, environmental studies, ecology, sustainability
- neuro: Neuroscience, brain research, cognitive science
- physics: Physics, engineering, materials science
- biology: Molecular biology, genetics, biochemistry, life sciences
- humanities: Philosophy, history, literature, arts, cultural studies

Also extract key entities that help identify the domain."""


def format_domain_detection_prompt(
    query: str,
    entities: Optional[list[str]] = None,
) -> str:
    """Format the domain detection prompt."""
    entity_context = ""
    if entities:
        entity_context = f"\nPreviously identified entities: {', '.join(entities)}"
    
    return DOMAIN_DETECTION_PROMPT.format(
        query=query,
        entity_context=entity_context,
    )


# =============================================================================
# Subtopic Generation Prompt
# =============================================================================

SUBTOPIC_GENERATION_PROMPT = """You are an expert research strategist helping a user plan a comprehensive literature search.

Research Query: "{query}"
Primary Domain: {domain}
User's Research Intent: {intent}
Key Entities: {entities}
{user_history_context}

Generate {min_count}-{max_count} specific research subtopics that would help comprehensively cover this query. 

Requirements for subtopics:
1. Be SPECIFIC to this exact query (not generic subtopics)
2. Cover different aspects/angles of the research area
3. Be distinct from each other (minimal overlap)
4. Each should yield meaningful academic papers when searched
5. Order by relevance (most important first)

For each subtopic, provide:
- A short snake_case identifier
- A human-readable label (2-4 words)
- A brief description explaining why this subtopic is relevant to the query
- A relevance score (0.0-1.0)

Also provide a brief explanation of why you chose these subtopics."""


def format_subtopic_prompt(
    query: str,
    domain: str,
    intent: str,
    entities: list[str],
    user_history: Optional[list[str]] = None,
    seed_terms: Optional[list[str]] = None,
    min_count: int = 6,
    max_count: int = 12,
) -> str:
    """Format the subtopic generation prompt."""
    user_history_context = ""
    if user_history:
        user_history_context = (
            f"\nUser's past research interests: {', '.join(user_history[:10])}"
            "\nConsider including related subtopics if relevant."
        )
    
    seed_context = ""
    if seed_terms:
        seed_context = (
            f"\nThe user specifically wants to explore: {', '.join(seed_terms)}. "
            "Include these AND add complementary subtopics to round out coverage."
        )
    
    return SUBTOPIC_GENERATION_PROMPT.format(
        query=query,
        domain=domain,
        intent=intent,
        entities=", ".join(entities) if entities else "None identified",
        user_history_context=user_history_context + seed_context,
        min_count=min_count,
        max_count=max_count,
    )


# =============================================================================
# Follow-up Decision Prompt
# =============================================================================

FOLLOW_UP_DECISION_PROMPT = """You are a research librarian helping a user refine their search. Based on the query and their answers so far, decide if a clarifying follow-up question would help narrow down their search.

Research Query: "{query}"
Detected Domain: {domain}
Selected Scope: {scope}
Selected Timeframe: {timeframe}
Ambiguity Score: {ambiguity_score}
Key Entities: {entities}

A follow-up question is NEEDED when:
- The query spans multiple distinct sub-areas that need narrowing
- The query is ambiguous about the specific aspect the user cares about
- There's a clear binary or multiple-choice clarification that would help
- The scope is "comprehensive" or "exhaustive" but the query is broad

A follow-up question is NOT needed when:
- The query is already specific and focused
- The ambiguity score is low (< 0.4)
- A follow-up would just annoy the user without adding value

If a follow-up IS needed, generate a clear, helpful clarifying question with 2-4 options. If needs_followup is false, set followup_question to null."""


def format_follow_up_prompt(
    query: str,
    domain: str,
    scope: str,
    timeframe: str,
    ambiguity_score: float,
    entities: list[str],
) -> str:
    """Format the follow-up decision prompt."""
    return FOLLOW_UP_DECISION_PROMPT.format(
        query=query,
        domain=domain,
        scope=scope,
        timeframe=timeframe,
        ambiguity_score=ambiguity_score,
        entities=", ".join(entities) if entities else "None",
    )


# =============================================================================
# Study Type Suggestion Prompt
# =============================================================================

STUDY_TYPE_SUGGESTION_PROMPT = """You are an expert methodologist helping a researcher identify relevant study types for their literature search.

Research Query: "{query}"
Domain: {domain}
Selected Subtopics: {subtopics}
Research Intent: {intent}

Based on the query and domain, suggest the most relevant study/paper types to include in the search.

Consider domain-specific study types:
- Medicine: RCTs, meta-analyses, systematic reviews, cohort studies, case reports, clinical trials
- CS: Empirical studies, benchmarks, surveys, theoretical papers, system papers, dataset papers
- Social Sciences: Quantitative, qualitative, mixed methods, longitudinal, experimental
- Physics/Engineering: Experimental, theoretical, simulation, review articles
- Biology: Experimental, computational, clinical/translational, review articles

Generate 4-8 study type suggestions, ordered by relevance to this specific query. Also provide a brief explanation of why these study types were selected."""


def format_study_type_prompt(
    query: str,
    domain: str,
    subtopics: list[str],
    intent: str,
) -> str:
    """Format the study type suggestion prompt."""
    return STUDY_TYPE_SUGGESTION_PROMPT.format(
        query=query,
        domain=domain,
        subtopics=", ".join(subtopics) if subtopics else "None selected",
        intent=intent,
    )


# =============================================================================
# Regeneration Prompt
# =============================================================================

REGENERATE_SUBTOPICS_PROMPT = """The user wasn't satisfied with the previous subtopic suggestions and requested new ones.

Research Query: "{query}"
Domain: {domain}
Intent: {intent}

Previous suggestions (user didn't like these):
{previous_options}

User feedback: {feedback}

Generate {count} NEW subtopics that are DIFFERENT from the previous ones. 
Consider the user's feedback to provide more relevant suggestions. Provide an explanation of how these differ from previous suggestions."""


def format_regenerate_prompt(
    query: str,
    domain: str,
    intent: str,
    previous_options: list[str],
    feedback: Optional[str] = None,
    seed_terms: Optional[list[str]] = None,
    count: int = 8,
) -> str:
    """Format the regeneration prompt."""
    seed_context = ""
    if seed_terms:
        seed_context = f"\nThe user specifically requested these focus areas: {', '.join(seed_terms)}."

    return REGENERATE_SUBTOPICS_PROMPT.format(
        query=query,
        domain=domain,
        intent=intent,
        previous_options="\n".join(f"- {opt}" for opt in previous_options),
        feedback=(feedback or "No specific feedback provided") + seed_context,
        count=count,
    )
