import asyncio
import json
import logging
import os
from types import SimpleNamespace
from typing import Dict, List, Optional, Any

from fastapi import APIRouter
from pydantic import BaseModel

from services.skill_generation_service import SkillDefinition, ToolParameter

try:
    import asyncpg
except ImportError:  # pragma: no cover - optional dependency in local/dev test envs
    asyncpg = SimpleNamespace(connect=None)

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


# In-memory registry (for runtime skill lookup)
class _SkillRegistry:
    def __init__(self):
        self._skills: Dict[str, SkillDefinition] = {}

    def register_skill(self, skill: SkillDefinition) -> None:
        self._skills[skill.skill_name] = skill

    def get_skill(self, name: str) -> Optional[SkillDefinition]:
        return self._skills.get(name)

    def list_skills(self) -> List[SkillDefinition]:
        return list(self._skills.values())


dynamic_skill_registry = _SkillRegistry()


@router.post("/register")
async def register_skill_endpoint(skill_in: SkillSchemaIn):
    # Map SkillSchemaIn → SkillDefinition using actual fields
    skill = SkillDefinition(
        skill_name=skill_in.name,
        description=skill_in.description,
        parameters=[],  # ToolParameter list — empty for compiled skills
        execution_logic_pseudo="\n".join(skill_in.steps),
        academic_citation=json.dumps(skill_in.source_paper),
    )
    dynamic_skill_registry.register_skill(skill)

    await _persist_to_postgres(skill_in)

    return {"status": "success", "skill_name": skill_in.name}


async def _persist_to_postgres(skill_in: SkillSchemaIn) -> None:
    """Write skill to Postgres research_skills table if DATABASE_URL is set."""
    db_url = os.getenv("DATABASE_URL")
    if not db_url or asyncpg.connect is None:
        return
    conn = None
    try:
        conn = await asyncpg.connect(db_url)
        await conn.execute(
            """INSERT INTO research_skills (name, description, steps, source_paper, created_at)
               VALUES ($1, $2, $3, $4, NOW())
               ON CONFLICT (name) DO UPDATE SET
                   description = EXCLUDED.description,
                   steps = EXCLUDED.steps,
                   source_paper = EXCLUDED.source_paper""",
            skill_in.name,
            skill_in.description,
            json.dumps(skill_in.steps),
            json.dumps(skill_in.source_paper),
        )
        logger.info(f"Persisted skill to postgres: {skill_in.name}")
    except Exception as e:
        logger.warning(f"Postgres write failed for skill {skill_in.name}: {e}")
    finally:
        if conn is not None:
            await conn.close()
