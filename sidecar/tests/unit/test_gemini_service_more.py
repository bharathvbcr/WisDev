from __future__ import annotations

import sys
import types
from types import SimpleNamespace
from unittest.mock import AsyncMock, patch

import pytest

from services.gemini_service import (
    GEMINI_EMBED_FALLBACK_MODEL,
    GEMINI_EMBED_PRIMARY_MODEL,
    GEMINI_EMBED_STANDARD_MODEL,
    GeminiService,
    StructuredOutputRequiresNativeRuntimeError,
    _resolve_embedding_model,
)


def test_resolve_embedding_model_aliases():
    assert _resolve_embedding_model("") == GEMINI_EMBED_STANDARD_MODEL
    assert _resolve_embedding_model("primary") == GEMINI_EMBED_PRIMARY_MODEL
    assert _resolve_embedding_model("standard") == GEMINI_EMBED_STANDARD_MODEL
    assert _resolve_embedding_model("balanced") == GEMINI_EMBED_STANDARD_MODEL
    assert _resolve_embedding_model("fallback") == GEMINI_EMBED_FALLBACK_MODEL
    assert _resolve_embedding_model("custom-model") == "custom-model"


def _install_httpx_client(monkeypatch, response_payload):
    class _Timeout:
        def __init__(self, timeout=None, connect=None):
            self.timeout = timeout
            self.connect = connect

    class _Response:
        def raise_for_status(self):
            return None

        def json(self):
            return response_payload

    class _Client:
        def __init__(self, timeout=None):
            self.timeout = timeout

        async def __aenter__(self):
            return self

        async def __aexit__(self, exc_type, exc, tb):
            return False

        async def post(self, url, json):
            return _Response()

    httpx_mod = types.ModuleType("httpx")
    httpx_mod.AsyncClient = _Client
    httpx_mod.Timeout = _Timeout
    monkeypatch.setitem(sys.modules, "httpx", httpx_mod)


@pytest.mark.asyncio
async def test_generate_structured_requires_native_runtime(monkeypatch):
    monkeypatch.setenv("VERTEX_PROXY_URL", "https://proxy.example")

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = None

    with pytest.raises(
        StructuredOutputRequiresNativeRuntimeError,
        match="requires native Gemini runtime",
    ):
        await svc.generate_structured("prompt", {"type": "object"})


@pytest.mark.asyncio
async def test_generate_structured_requires_schema(monkeypatch):
    monkeypatch.setenv("VERTEX_PROXY_URL", "https://proxy.example")

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = None

    with pytest.raises(ValueError, match="json_schema is required"):
        await svc.generate_structured("prompt", {})


@pytest.mark.asyncio
async def test_embed_proxy_handles_empty_vectors_and_embed_fallback(monkeypatch):
    monkeypatch.setenv("VERTEX_PROXY_URL", "https://proxy.example")
    _install_httpx_client(monkeypatch, {"embeddings": []})

    svc = GeminiService.__new__(GeminiService)
    svc.model = "test-model"
    svc._client = None

    with pytest.raises(RuntimeError, match="returned no embeddings"):
        await svc._embed_proxy(["hello"], "model", "RETRIEVAL_QUERY")

    _install_httpx_client(monkeypatch, {"embeddings": []})
    assert await svc._embed_proxy([], "model", "RETRIEVAL_QUERY") == []

    monkeypatch.setattr(svc, "_embed_proxy", AsyncMock(return_value=[]))
    assert await svc.embed("hello") == []
