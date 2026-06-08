from typing import Optional

from fastapi import APIRouter, HTTPException
from pydantic import BaseModel, Field

from services.deepagents_service import (
    DeepAgentsTimeoutError,
    DeepAgentsUnavailableError,
    get_deepagents_capabilities,
    run_deep_agent,
)

router = APIRouter()

class AgentCard(BaseModel):
    agentId: str
    name: str
    version: str
    protocol: str
    capabilities: int


class DeepAgentExecuteRequest(BaseModel):
    query: str = Field(..., min_length=3, max_length=5000)
    system_prompt: Optional[str] = Field(default=None, max_length=2000)
    model: Optional[str] = Field(default=None, max_length=200)
    session_id: Optional[str] = Field(default=None, alias="sessionId")
    user_id: Optional[str] = Field(default=None, alias="userId")
    papers: list[dict] = Field(default_factory=list)
    enable_wisdev_tools: bool = Field(default=True, alias="enableWisdevTools")
    max_execution_ms: Optional[int] = Field(default=None, alias="maxExecutionMs")
    allowlisted_tools: Optional[list[str]] = Field(default=None, alias="allowlistedTools")
    require_human_confirmation: bool = Field(default=False, alias="requireHumanConfirmation")
    confirmed_actions: list[str] = Field(default_factory=list, alias="confirmedActions")


class DeepAgentExecuteResponse(BaseModel):
    output: str
    backend: str
    model: Optional[str] = None
    tools_enabled: bool = Field(default=False, alias="toolsEnabled")
    tool_count: int = Field(default=0, alias="toolCount")
    allowlisted_tools: list[str] = Field(default_factory=list, alias="allowlistedTools")
    require_human_confirmation: bool = Field(default=False, alias="requireHumanConfirmation")
    success: bool = True


@router.get("/wisdev/deep-agents/capabilities")
async def deepagents_capabilities():
    return get_deepagents_capabilities()

@router.get("/wisdev/agent/card")
async def get_agent_card():
    """Return the ADK Agent Card for the Python sidecar."""
    return {
        "agentId": "wisdev-python-worker",
        "name": "WisDev Python Sidecar",
        "version": "1.1.0",
        "protocol": "refined",
        "capabilities": 3, # PDF, Embeddings, BM25
    }


@router.post("/wisdev/deep-agents/execute", response_model=DeepAgentExecuteResponse)
async def execute_with_deepagents(payload: DeepAgentExecuteRequest):
    try:
        result = await run_deep_agent(
            query=payload.query,
            system_prompt=payload.system_prompt,
            model=payload.model,
            session_id=payload.session_id,
            user_id=payload.user_id,
            papers=payload.papers,
            enable_wisdev_tools=payload.enable_wisdev_tools,
            max_execution_ms=payload.max_execution_ms,
            allowlisted_tools=payload.allowlisted_tools,
            require_human_confirmation=payload.require_human_confirmation,
            confirmed_actions=payload.confirmed_actions,
        )
    except DeepAgentsTimeoutError as exc:
        raise HTTPException(status_code=504, detail=str(exc)) from exc
    except DeepAgentsUnavailableError as exc:
        raise HTTPException(status_code=503, detail=str(exc)) from exc
    except Exception as exc:
        raise HTTPException(status_code=500, detail=f"deepagents_execution_failed: {exc}") from exc

    return DeepAgentExecuteResponse(
        output=result.get("output", ""),
        backend=result.get("backend", "deepagents"),
        model=result.get("model"),
        toolsEnabled=bool(result.get("toolsEnabled", False)),
        toolCount=int(result.get("toolCount", 0)),
        allowlistedTools=list(result.get("allowlistedTools", [])),
        requireHumanConfirmation=bool(result.get("requireHumanConfirmation", False)),
        success=True,
    )
