from fastapi import APIRouter
from pydantic import BaseModel

router = APIRouter(prefix="/v2/agent")

class AgentCard(BaseModel):
    agentId: str
    name: str
    version: str
    protocol: str
    capabilities: int

@router.get("/card")
async def get_agent_card():
    """Return the ADK Agent Card for the Python sidecar."""
    return {
        "agentId": "wisdev-python-worker",
        "name": "ScholarLM Python Sidecar",
        "version": "1.1.0",
        "protocol": "refined",
        "capabilities": 3, # PDF, Embeddings, BM25
    }
