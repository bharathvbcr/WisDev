import pytest
from unittest.mock import AsyncMock, patch
from fastapi.testclient import TestClient
from fastapi import FastAPI
from services.dynamic_skill_registry import router, dynamic_skill_registry, _persist_to_postgres, SkillSchemaIn

app = FastAPI()
app.include_router(router)
client = TestClient(app)

VALID_SKILL = {
    "name": "sparse_attention_v1",
    "description": "Sparse attention mechanism",
    "inputs": [],
    "outputs": [],
    "steps": ["apply sparse mask", "compute attention scores"],
    "code_template": "",
    "source_paper": {"arxiv_id": "2401.00001"}
}

def test_register_skill_in_memory_succeeds():
    """Skill registers successfully without DATABASE_URL."""
    resp = client.post("/skills/register", json=VALID_SKILL)
    assert resp.status_code == 200
    assert resp.json()["skill_name"] == "sparse_attention_v1"

def test_register_skill_writes_to_postgres_when_db_url_set():
    """When DATABASE_URL is set, Postgres write is attempted."""
    mock_conn = AsyncMock()
    mock_conn.execute = AsyncMock()
    mock_conn.close = AsyncMock()

    async def fake_connect(url):
        return mock_conn

    with patch.dict("os.environ", {"DATABASE_URL": "postgresql://localhost/test"}):
        with patch("services.dynamic_skill_registry.asyncpg.connect", side_effect=fake_connect):
            resp = client.post("/skills/register", json=VALID_SKILL)

    assert resp.status_code == 200
    mock_conn.execute.assert_called_once()
    call_sql = mock_conn.execute.call_args[0][0]
    assert "research_skills" in call_sql

def test_register_skill_postgres_failure_degrades_gracefully():
    """Postgres connection failure does not fail the endpoint."""
    async def fail_connect(url):
        raise Exception("connection refused")

    with patch.dict("os.environ", {"DATABASE_URL": "postgresql://localhost/test"}):
        with patch("services.dynamic_skill_registry.asyncpg.connect", side_effect=fail_connect):
            resp = client.post("/skills/register", json=VALID_SKILL)

    assert resp.status_code == 200


def test_register_skill_populates_runtime_registry_fields():
    resp = client.post("/skills/register", json=VALID_SKILL)

    assert resp.status_code == 200
    skill = dynamic_skill_registry.get_skill("sparse_attention_v1")
    assert skill is not None
    assert skill.skill_name == "sparse_attention_v1"
    assert "apply sparse mask" in skill.execution_logic_pseudo
    assert "2401.00001" in skill.academic_citation


def test_runtime_registry_list_contains_registered_skill():
    client.post("/skills/register", json=VALID_SKILL)

    names = [skill.skill_name for skill in dynamic_skill_registry.list_skills()]
    assert "sparse_attention_v1" in names


@pytest.mark.asyncio
async def test_persist_to_postgres_returns_early_without_database_url():
    with patch.dict("os.environ", {}, clear=True):
        with patch("services.dynamic_skill_registry.asyncpg.connect", new=AsyncMock()) as connect_mock:
            await _persist_to_postgres(SkillSchemaIn(**VALID_SKILL))

    connect_mock.assert_not_called()


@pytest.mark.asyncio
async def test_persist_to_postgres_closes_connection_on_execute_failure():
    mock_conn = AsyncMock()
    mock_conn.execute = AsyncMock(side_effect=RuntimeError("write failed"))
    mock_conn.close = AsyncMock()

    async def fake_connect(url):
        return mock_conn

    with patch.dict("os.environ", {"DATABASE_URL": "postgresql://localhost/test"}):
        with patch("services.dynamic_skill_registry.asyncpg.connect", side_effect=fake_connect):
            await _persist_to_postgres(SkillSchemaIn(**VALID_SKILL))

    mock_conn.close.assert_awaited_once()
