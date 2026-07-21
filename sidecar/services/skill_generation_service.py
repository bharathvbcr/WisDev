"""
skill_generation_service.py — SkillDefinition model and related types.

Defines the canonical schema for compiled research skills that can be
registered, stored, and executed by the WisDev agent runtime.
"""

from typing import List, Optional, Dict
from pydantic import BaseModel


class ToolParameter(BaseModel):
    """Represents a single input/output parameter of a skill."""
    name: str
    type: str = "string"
    description: str = ""
    required: bool = False


class SkillDefinition(BaseModel):
    """Canonical representation of a compiled research skill."""
    skill_name: str
    description: str
    parameters: List[ToolParameter] = []
    execution_logic_pseudo: str = ""
    academic_citation: str = ""


def compile_skill(name: str, objective: str, findings: List[str], paper_metadata: Optional[Dict] = None) -> SkillDefinition:
    """
    Heuristic-based skill compiler.
    Transforms raw research findings and a target objective into a reusable WisDev skill.
    """
    # In a production environment, this would involve an LLM call to synthesize
    # the execution_logic_pseudo from the findings.
    
    steps = [f"Objective: {objective}"]
    for i, finding in enumerate(findings):
        steps.append(f"Step {i+1}: Validate and apply finding: {finding}")
    
    params = [
        ToolParameter(name="context", type="string", description="Research context for execution", required=True),
        ToolParameter(name="depth", type="integer", description="Search depth", required=False)
    ]
    
    citation = ""
    if paper_metadata:
        citation = f"{paper_metadata.get('authors', 'Unknown')}, {paper_metadata.get('title', 'Untitled')} ({paper_metadata.get('year', 'n.d.')})"

    return SkillDefinition(
        skill_name=name.lower().replace(" ", "_"),
        description=f"Automated skill for: {objective}",
        parameters=params,
        execution_logic_pseudo="\n".join(steps),
        academic_citation=citation
    )
