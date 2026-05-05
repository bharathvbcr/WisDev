from __future__ import annotations

from types import SimpleNamespace
from unittest.mock import AsyncMock, MagicMock, patch

import pytest
from pydantic import BaseModel

from services.gemini_service import GeminiService

from .test_gemini_service_additional import _install_fake_google_modules


class _SchemaModel(BaseModel):
    payload: str


@pytest.mark.asyncio
async def test_generate_structured_prepares_schema_for_native_sdk(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = MagicMock()

    runner = AsyncMock(return_value=SimpleNamespace(text='{"payload":"ok"}'))
    with patch("services.gemini_service._run_sync_with_timeout", runner):
        result = await svc.generate_structured(
            "prompt",
            {
                "type": "object",
                "properties": {
                    "payload": {
                        "type": "object",
                        "properties": {
                            "value": {"type": "string"},
                        },
                    },
                    "status": {"type": "string"},
                },
            },
        )

    assert result == '{"payload":"ok"}'
    schema = runner.await_args.kwargs["config"].kwargs["response_json_schema"]
    assert schema["propertyOrdering"] == ["payload", "status"]
    assert schema["properties"]["payload"]["propertyOrdering"] == ["value"]


@pytest.mark.asyncio
async def test_generate_native_structured_prepares_response_model_schema(monkeypatch):
    _install_fake_google_modules(monkeypatch)

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = MagicMock()

    runner = AsyncMock(return_value=SimpleNamespace(text='{"payload":"ok"}'))
    with patch("services.gemini_service._run_sync_with_timeout", runner):
        result = await svc._generate_native_structured(
            "prompt",
            _SchemaModel,
            0.2,
            32,
            0.5,
            None,
        )

    assert result.payload == "ok"
    schema = runner.await_args.kwargs["config"].kwargs["response_json_schema"]
    assert schema["propertyOrdering"] == ["payload"]
