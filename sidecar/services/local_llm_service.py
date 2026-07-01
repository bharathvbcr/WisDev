"""
services/local_llm_service.py — Local / self-hosted LLM backend for manuscript
drafting, used when no Gemini/Vertex cloud credentials are available.

It talks to an Ollama server (or any OpenAI-compatible endpoint: vLLM, LM Studio,
llama.cpp's server, etc.) via the OpenAI-compatible ``/v1/chat/completions`` API,
exposing just the slice of the ``GeminiService`` surface the manuscript router
uses — ``generate_text`` and ``generate_json`` — so it is a drop-in replacement at
those call sites. Provider selection is env-driven (see ``services.manuscript_llm``
and ``MANUSCRIPT_LLM_PROVIDER``); without a local backend configured the manuscript
endpoints continue to use Gemini exactly as before.

This module deliberately depends only on ``httpx`` (already a sidecar dependency)
so a manuscript-only deployment can draft fluent prose with a local model and no
cloud SDKs at all.
"""

from __future__ import annotations

import json
import os
import re
from typing import Any, Type

import httpx
import structlog
from pydantic import BaseModel

logger = structlog.get_logger(__name__)

_DEFAULT_BASE_URL = "http://localhost:11434"
_DEFAULT_MODEL = "llama3.1"


def _base_url() -> str:
    raw = (
        os.environ.get("LOCAL_LLM_BASE_URL")
        or os.environ.get("OLLAMA_BASE_URL")
        or _DEFAULT_BASE_URL
    ).strip().rstrip("/")
    # Accept either the server root (http://host:11434) or an explicit OpenAI base
    # (http://host:11434/v1) — normalize to the /v1 base used for chat completions.
    if raw.endswith("/v1"):
        return raw
    return raw + "/v1"


def _model() -> str:
    return (
        os.environ.get("LOCAL_LLM_MODEL")
        or os.environ.get("OLLAMA_MODEL")
        or _DEFAULT_MODEL
    ).strip()


def _api_key() -> str:
    # Ollama ignores the key; vLLM / LM Studio / hosted OpenAI-compatible servers
    # may require one. Default to a harmless placeholder so the header is always set.
    return (os.environ.get("LOCAL_LLM_API_KEY") or "local").strip()


def _extract_json(text: str) -> str:
    """Pull a single JSON object out of a model reply that may be fenced or
    prefixed with prose. Falls back to the raw text so validation can surface a
    precise error."""
    cleaned = (text or "").strip()
    if cleaned.startswith("```"):
        # Strip a ```json ... ``` (or ``` ... ```) fence.
        cleaned = re.sub(r"^```[a-zA-Z0-9]*\s*", "", cleaned)
        cleaned = re.sub(r"\s*```$", "", cleaned).strip()
    start = cleaned.find("{")
    end = cleaned.rfind("}")
    if start != -1 and end != -1 and end > start:
        return cleaned[start : end + 1]
    return cleaned


class LocalLLMService:
    """OpenAI-compatible local LLM client (Ollama / vLLM / LM Studio / ...)."""

    def available(self) -> bool:
        """True when a local backend is explicitly configured. Used by the provider
        selector so we never silently route to localhost when nothing is running."""
        provider = str(os.environ.get("MANUSCRIPT_LLM_PROVIDER", "")).strip().lower()
        return bool(
            os.environ.get("LOCAL_LLM_BASE_URL")
            or os.environ.get("OLLAMA_BASE_URL")
            or provider in {"local", "ollama"}
        )

    async def _chat(
        self,
        messages: list[dict[str, str]],
        temperature: float,
        max_tokens: int,
        timeout_s: float,
        response_format: dict[str, Any] | None = None,
    ) -> str:
        payload: dict[str, Any] = {
            "model": _model(),
            "messages": messages,
            "temperature": temperature,
            "max_tokens": max_tokens,
            "stream": False,
        }
        if response_format is not None:
            payload["response_format"] = response_format
        async with httpx.AsyncClient(timeout=timeout_s) as client:
            resp = await client.post(
                f"{_base_url()}/chat/completions",
                json=payload,
                headers={"Authorization": f"Bearer {_api_key()}"},
            )
            resp.raise_for_status()
            data = resp.json()
        choices = data.get("choices") or []
        if not choices:
            return ""
        message = (choices[0] or {}).get("message") or {}
        return str(message.get("content") or "")

    async def generate_text(
        self,
        prompt: str,
        temperature: float = 0.7,
        max_tokens: int = 2048,
        timeout_s: float = 30.0,
        **_ignored: Any,
    ) -> str:
        """Plain text generation. Extra Gemini-specific kwargs (thinking_budget,
        latency_budget_ms, retry_profile, request_class, ...) are accepted and
        ignored so this is a drop-in for ``gemini_service.generate_text``."""
        text = await self._chat(
            [{"role": "user", "content": prompt}],
            temperature,
            max_tokens,
            timeout_s,
        )
        return text.strip()

    async def generate_json(
        self,
        prompt: str,
        response_model: Type[BaseModel],
        temperature: float = 0.3,
        max_tokens: int = 4096,
        timeout_s: float = 60.0,
        **_ignored: Any,
    ) -> BaseModel:
        """Structured generation validated against ``response_model``. Asks the
        model for a JSON object matching the schema and validates the reply."""
        schema = response_model.model_json_schema()
        instruction = (
            f"{prompt}\n\nRespond with ONLY a single JSON object that conforms to this "
            f"JSON schema. No markdown, no code fence, no commentary:\n{json.dumps(schema)}"
        )
        text = await self._chat(
            [{"role": "user", "content": instruction}],
            temperature,
            max_tokens,
            timeout_s,
            response_format={"type": "json_object"},
        )
        return response_model.model_validate_json(_extract_json(text))


local_llm_service = LocalLLMService()
