"""
Dynamic Skill Registry — in-memory skill lookup with optional file persistence.
"""

import json
import os
import logging
from typing import Dict, List, Optional, Any

from fastapi import APIRouter
from pydantic import BaseModel

from services.skill_generation_service import SkillDefinition

logger = logging.getLogger(__name__)


class SkillSchemaIn(BaseModel):
    name: str
    description: str
    inputs: List[Any] = []
    outputs: List[Any] = []
    steps: List[str] = []
    code_template: str = ""
    source_paper: Dict[str, Any] = {}


router = APIRouter(prefix="/skills")


class _SkillRegistry:
    def __init__(self):
        self._skills: Dict[str, SkillDefinition] = {}
        self._persist_path = os.environ.get("WISDEV_SKILLS_PATH", "wisdev_skills.json")
        self._load_from_file()

    def _load_from_file(self) -> None:
        if os.path.exists(self._persist_path):
            try:
                with open(self._persist_path, "r") as f:
                    data = json.load(f)
                for item in data:
                    skill = SkillDefinition(
                        skill_name=item["name"],
                        description=item["description"],
                        parameters=[],
                        execution_logic_pseudo=item.get("steps", ""),
                        academic_citation=json.dumps(item.get("source_paper", {})),
                    )
                    self._skills[skill.skill_name] = skill
                logger.info(
                    "Loaded %d skills from %s", len(self._skills), self._persist_path
                )
            except Exception as e:
                logger.warning("Failed to load skills file: %s", e)

    def _persist_to_file(self) -> None:
        try:
            data = []
            for name, skill in self._skills.items():
                data.append(
                    {
                        "name": name,
                        "description": skill.description,
                        "steps": skill.execution_logic_pseudo,
                        "source_paper": json.loads(skill.academic_citation)
                        if skill.academic_citation
                        else {},
                    }
                )
            with open(self._persist_path, "w") as f:
                json.dump(data, f, indent=2)
        except Exception as e:
            logger.warning("Failed to persist skills: %s", e)

    def register_skill(self, skill: SkillDefinition) -> None:
        self._skills[skill.skill_name] = skill
        self._persist_to_file()

    def get_skill(self, name: str) -> Optional[SkillDefinition]:
        return self._skills.get(name)

    def list_skills(self) -> List[SkillDefinition]:
        return list(self._skills.values())


dynamic_skill_registry = _SkillRegistry()


@router.post("/register")
async def register_skill_endpoint(skill_in: SkillSchemaIn):
    skill = SkillDefinition(
        skill_name=skill_in.name,
        description=skill_in.description,
        parameters=[],
        execution_logic_pseudo="\n".join(skill_in.steps),
        academic_citation=json.dumps(skill_in.source_paper),
    )
    dynamic_skill_registry.register_skill(skill)
    return {"status": "success", "skill_name": skill_in.name}
