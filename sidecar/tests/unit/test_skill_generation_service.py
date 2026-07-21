"""Tests for services/skill_generation_service.py."""

import pytest
from services.skill_generation_service import (
    ToolParameter,
    SkillDefinition,
    compile_skill,
)


# ---------------------------------------------------------------------------
# ToolParameter
# ---------------------------------------------------------------------------

class TestToolParameter:
    def test_required_fields(self):
        p = ToolParameter(name="query")
        assert p.name == "query"
        assert p.type == "string"
        assert p.description == ""
        assert p.required is False

    def test_custom_values(self):
        p = ToolParameter(
            name="depth", type="integer", description="Search depth", required=True
        )
        assert p.type == "integer"
        assert p.required is True


# ---------------------------------------------------------------------------
# SkillDefinition
# ---------------------------------------------------------------------------

class TestSkillDefinition:
    def test_defaults(self):
        sd = SkillDefinition(skill_name="my_skill", description="Does something")
        assert sd.parameters == []
        assert sd.execution_logic_pseudo == ""
        assert sd.academic_citation == ""

    def test_with_parameters(self):
        params = [ToolParameter(name="context", required=True)]
        sd = SkillDefinition(
            skill_name="search_skill",
            description="Searches things",
            parameters=params,
        )
        assert len(sd.parameters) == 1


# ---------------------------------------------------------------------------
# compile_skill
# ---------------------------------------------------------------------------

class TestCompileSkill:
    def test_basic_compilation(self):
        skill = compile_skill(
            name="My Skill",
            objective="Find relevant papers",
            findings=["Paper A is relevant", "Paper B confirms"],
        )
        assert skill.skill_name == "my_skill"
        assert "Find relevant papers" in skill.description

    def test_name_normalized_lowercase(self):
        skill = compile_skill(
            name="CANCER SEARCH", objective="obj", findings=[]
        )
        assert skill.skill_name == "cancer_search"

    def test_name_spaces_replaced_with_underscores(self):
        skill = compile_skill(
            name="my search skill", objective="obj", findings=[]
        )
        assert " " not in skill.skill_name
        assert skill.skill_name == "my_search_skill"

    def test_empty_findings_produces_only_objective_step(self):
        skill = compile_skill(name="skill", objective="Run analysis", findings=[])
        lines = skill.execution_logic_pseudo.split("\n")
        assert lines[0].startswith("Objective:")
        assert "Run analysis" in lines[0]
        assert len(lines) == 1

    def test_multiple_findings_become_steps(self):
        findings = ["finding one", "finding two", "finding three"]
        skill = compile_skill(name="s", objective="obj", findings=findings)
        pseudo = skill.execution_logic_pseudo
        assert "Step 1:" in pseudo
        assert "Step 2:" in pseudo
        assert "Step 3:" in pseudo
        assert "finding one" in pseudo

    def test_two_parameters_always_created(self):
        skill = compile_skill(name="s", objective="obj", findings=[])
        assert len(skill.parameters) == 2
        names = {p.name for p in skill.parameters}
        assert "context" in names
        assert "depth" in names

    def test_context_parameter_is_required(self):
        skill = compile_skill(name="s", objective="obj", findings=[])
        context_param = next(p for p in skill.parameters if p.name == "context")
        assert context_param.required is True

    def test_depth_parameter_is_not_required(self):
        skill = compile_skill(name="s", objective="obj", findings=[])
        depth_param = next(p for p in skill.parameters if p.name == "depth")
        assert depth_param.required is False

    def test_no_paper_metadata_empty_citation(self):
        skill = compile_skill(name="s", objective="obj", findings=[], paper_metadata=None)
        assert skill.academic_citation == ""

    def test_paper_metadata_full(self):
        skill = compile_skill(
            name="s",
            objective="obj",
            findings=[],
            paper_metadata={
                "authors": "Smith et al.",
                "title": "A Study on X",
                "year": "2023",
            },
        )
        assert "Smith et al." in skill.academic_citation
        assert "A Study on X" in skill.academic_citation
        assert "2023" in skill.academic_citation

    def test_paper_metadata_missing_fields_use_defaults(self):
        # Non-empty dict (truthy) but missing all optional keys → uses .get() defaults
        skill = compile_skill(
            name="s",
            objective="obj",
            findings=[],
            paper_metadata={"irrelevant_key": "ignored"},
        )
        assert "Unknown" in skill.academic_citation
        assert "Untitled" in skill.academic_citation
        assert "n.d." in skill.academic_citation

    def test_paper_metadata_partial_fields(self):
        skill = compile_skill(
            name="s",
            objective="obj",
            findings=[],
            paper_metadata={"authors": "Jones", "year": "2021"},
        )
        assert "Jones" in skill.academic_citation
        assert "2021" in skill.academic_citation
        assert "Untitled" in skill.academic_citation
