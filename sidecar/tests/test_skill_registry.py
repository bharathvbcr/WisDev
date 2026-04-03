import pytest
from unittest.mock import AsyncMock, patch
from fastapi.testclient import TestClient
from fastapi import FastAPI
from services.dynamic_skill_registry import router

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
    "source_paper": {"arxiv_id": "2401.00001"},
}


def test_register_skill_in_memory_succeeds():
    """Skill registers successfully without DATABASE_URL."""
    resp = client.post("/skills/register", json=VALID_SKILL)
    assert resp.status_code == 200
    assert resp.json()["skill_name"] == "sparse_attention_v1"


def test_register_skill_writes_to_postgres_when_db_url_set():
    """Skipped: Postgres removed in open-source version."""
    pytest.skip("Postgres removed in open-source version")


def test_register_skill_postgres_failure_degrades_gracefully():
    """Skipped: Postgres removed in open-source version."""
    pytest.skip("Postgres removed in open-source version")
