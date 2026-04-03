# sidecar/services/gemini_service.py
"""
Canonical Gemini LLM gateway for CloudRun services.
Priority: native google-genai SDK → LLM Provider proxy → exception.
All methods are async. Retries with exponential backoff + jitter.
"""

from __future__ import annotations

import asyncio
import json
import logging
import os
import random
from typing import Any, AsyncIterator, Type

from pydantic import BaseModel

logger = logging.getLogger(__name__)


def _load_model_tier(tier: str, default: str) -> str:
    for path in [
        "scholar_models.json",
        "../scholar_models.json",
        "../../scholar_models.json",
        "../../../scholar_models.json",
    ]:
        if os.path.exists(path):
            with open(path, "r", encoding="utf-8") as f:
                models = json.load(f)
                return models.get(tier, default)
    return default


GEMINI_LIGHT_MODEL = os.getenv(
    "GEMINI_LIGHT_MODEL", _load_model_tier("light", "gemini-2.5-flash-lite")
)
GEMINI_HEAVY_MODEL = os.getenv(
    "GEMINI_HEAVY_MODEL", _load_model_tier("heavy", "gemini-2.5-pro")
)

_MAX_RETRIES = 3
_BASE_BACKOFF_S = 1.0


def _jitter(base: float) -> float:
    return base + random.uniform(0, base * 0.5)


def _gemini_api_key() -> str:
    return os.getenv("GOOGLE_API_KEY", "").strip()


def _vertex_project() -> str:
    return (os.getenv("VERTEX_PROJECT") or os.getenv("VERTEX_PROJECT") or "").strip()


def _vertex_proxy_url() -> str:
    return os.getenv("VERTEX_PROXY_URL", "").strip().rstrip("/")


def _gemini_runtime_mode() -> str:
    mode = os.getenv("GEMINI_RUNTIME_MODE", "auto").strip().lower()
    if mode in {"auto", "native", "vertex_proxy"}:
        return mode
    logger.warning("Unknown GEMINI_RUNTIME_MODE=%s; defaulting to auto", mode)
    return "auto"


def _require_non_empty_text(text: Any, source: str) -> str:
    normalized = str(text or "").strip()
    if not normalized:
        raise RuntimeError(f"{source} returned empty text")
    return normalized


class GeminiService:
    """
    Wraps google-genai SDK with:
      - Native structured output (response_mime_type + response_json_schema)
      - Extended thinking via ThinkingConfig
      - LLM Provider proxy fallback
      - Per-call timeout + exponential backoff with jitter
    """

    def __init__(self, model: str = GEMINI_LIGHT_MODEL) -> None:
        self.model = model
        self._client = self._build_client()

    def is_ready(self) -> bool:
        """Return True if the service has valid credentials or a proxy URL."""
        return self._client is not None or bool(_vertex_proxy_url())

    # ------------------------------------------------------------------
    # Client construction
    # ------------------------------------------------------------------

    def _build_client(self) -> Any:
        mode = _gemini_runtime_mode()
        api_key = _gemini_api_key()
        project = _vertex_project()
        proxy_url = _vertex_proxy_url()

        if mode == "vertex_proxy":
            if proxy_url:
                logger.info("GeminiService configured for vertex_proxy mode")
            else:
                logger.warning(
                    "GeminiService configured for vertex_proxy mode but VERTEX_PROXY_URL is not set"
                )
            return None

        try:
            import google.genai as genai  # type: ignore[import]

            if api_key:
                return genai.Client(api_key=api_key)
            if project:
                return genai.Client(
                    vertexai=False, project=project, location="us-central1"
                )
        except ImportError:
            if proxy_url:
                logger.info(
                    "google-genai SDK not installed; using configured Vertex proxy"
                )
            elif mode == "native":
                logger.warning(
                    "google-genai SDK not installed while GEMINI_RUNTIME_MODE=native"
                )
        except Exception as exc:
            if proxy_url:
                logger.warning(
                    "google-genai client init failed; using configured Vertex proxy: %s",
                    exc,
                )
            else:
                logger.warning("google-genai client init failed: %s", exc)
            return None

        if proxy_url:
            logger.info("No native Gemini credentials configured; using Vertex proxy")
        elif mode == "native":
            logger.warning(
                "GEMINI_RUNTIME_MODE=native but no GOOGLE_API_KEY or VERTEX_PROJECT is configured"
            )
        return None

    # ------------------------------------------------------------------
    # Public API
    # ------------------------------------------------------------------

    async def generate_json(
        self,
        prompt: str,
        response_model: Type[BaseModel],
        temperature: float = 0.3,
        max_tokens: int = 4096,
        timeout_s: float = 60.0,
    ) -> BaseModel:
        """
        Generate structured output conforming to `response_model`.
        Uses native SDK if available, else Vertex proxy.
        """
        if self._client is not None:
            return await self._generate_native_structured(
                prompt, response_model, temperature, max_tokens, timeout_s
            )
        return await self._generate_via_vertex_proxy(
            prompt, response_model, temperature, max_tokens
        )

    async def generate_with_thinking(
        self,
        prompt: str,
        response_schema: Type[BaseModel],
        thinking_budget: int = 2048,
        timeout_s: float = 90.0,
    ) -> BaseModel:
        """
        Extended thinking (Gemini 2.5 Pro / Flash Thinking).
        Falls back to generate_json() when ThinkingConfig is unavailable.
        """
        if self._client is None:
            logger.info("No native client; skipping thinking, using generate_json")
            return await self.generate_json(prompt, response_schema)

        try:
            import google.genai as genai  # noqa: F401  type: ignore[import]
            from google.genai.types import GenerateContentConfig, ThinkingConfig  # type: ignore[import]

            config = GenerateContentConfig(
                thinking_config=ThinkingConfig(thinking_budget=thinking_budget),
                response_mime_type="application/json",
                response_json_schema=response_schema.model_json_schema(),
                temperature=0.3,
                max_output_tokens=8192,
            )
            resp = await asyncio.wait_for(
                asyncio.to_thread(
                    self._client.models.generate_content,
                    model=self.model,
                    contents=prompt,
                    config=config,
                ),
                timeout=timeout_s,
            )
            return response_schema.model_validate_json(resp.text)

        except ImportError:
            logger.info(
                "ThinkingConfig not available in SDK version; falling back to generate_json"
            )
        except Exception as exc:
            logger.warning(
                "generate_with_thinking failed (%s); falling back to generate_json", exc
            )

        return await self.generate_json(prompt, response_schema, timeout_s=timeout_s)

    async def generate_text(
        self,
        prompt: str,
        temperature: float = 0.7,
        max_tokens: int = 2048,
        timeout_s: float = 30.0,
    ) -> str:
        """Plain text generation (no structured output)."""
        if self._client is not None:
            return await self._generate_text_native(
                prompt, temperature, max_tokens, timeout_s
            )
        return await self._generate_text_proxy(prompt, temperature, max_tokens)

    async def generate_stream(
        self,
        prompt: str,
        temperature: float = 0.7,
        max_tokens: int = 2048,
    ) -> AsyncIterator[str]:
        """Stream text generation natively (if client is available)."""
        if self._client is None:
            # Fallback to simulated stream if no native client
            text = await self.generate_text(prompt, temperature, max_tokens)
            # Use a local split to avoid circular import if needed,
            # but we'll implement a simple one here.
            for chunk in text.split("\n\n"):
                yield chunk + "\n\n"
            return

        from google.genai.types import GenerateContentConfig

        config = GenerateContentConfig(
            temperature=temperature, max_output_tokens=max_tokens
        )

        try:
            async for chunk in await self._client.aio.models.generate_content_stream(
                model=self.model,
                contents=prompt,
                config=config,
            ):
                if chunk.text:
                    yield chunk.text
        except Exception as exc:
            logger.warning("Native stream failed: %s", exc)
            raise

    async def generate_structured(
        self,
        prompt: str,
        json_schema: dict,
        temperature: float = 0.3,
        max_tokens: int = 2048,
        timeout_s: float = 60.0,
    ) -> str:
        """Generate structured output using a raw JSON schema dict."""
        if self._client is None:
            # Fallback to manual parsing if proxy is available
            text = await self._generate_text_proxy(prompt, temperature, max_tokens)
            return text

        from google.genai.types import GenerateContentConfig  # type: ignore[import]

        config = GenerateContentConfig(
            response_mime_type="application/json",
            response_json_schema=json_schema,
            temperature=temperature,
            max_output_tokens=max_tokens,
        )
        for attempt in range(_MAX_RETRIES):
            try:
                resp = await asyncio.wait_for(
                    asyncio.to_thread(
                        self._client.models.generate_content,
                        model=self.model,
                        contents=prompt,
                        config=config,
                    ),
                    timeout=timeout_s,
                )
                return _require_non_empty_text(resp.text, "Gemini")
            except Exception:
                if attempt == _MAX_RETRIES - 1:
                    raise
                await asyncio.sleep(_jitter(_BASE_BACKOFF_S * (2**attempt)))
        return ""

    # ------------------------------------------------------------------
    # Private helpers
    # ------------------------------------------------------------------

    async def _generate_native_structured(
        self,
        prompt: str,
        response_model: Type[BaseModel],
        temperature: float,
        max_tokens: int,
        timeout_s: float,
    ) -> BaseModel:
        from google.genai.types import GenerateContentConfig  # type: ignore[import]

        config = GenerateContentConfig(
            response_mime_type="application/json",
            response_json_schema=response_model.model_json_schema(),
            temperature=temperature,
            max_output_tokens=max_tokens,
        )
        for attempt in range(_MAX_RETRIES):
            try:
                resp = await asyncio.wait_for(
                    asyncio.to_thread(
                        self._client.models.generate_content,
                        model=self.model,
                        contents=prompt,
                        config=config,
                    ),
                    timeout=timeout_s,
                )
                return response_model.model_validate_json(resp.text)
            except asyncio.TimeoutError:
                logger.warning(
                    "Gemini attempt %d timed out after %.1fs", attempt + 1, timeout_s
                )
                if attempt == _MAX_RETRIES - 1:
                    raise
            except Exception as exc:
                logger.warning(
                    "Gemini structured attempt %d failed: %s", attempt + 1, exc
                )
                if attempt == _MAX_RETRIES - 1:
                    raise
            await asyncio.sleep(_jitter(_BASE_BACKOFF_S * (2**attempt)))

        raise RuntimeError(
            "generate_json exhausted all retries"
        )  # unreachable but satisfies type checker

    async def _generate_text_native(
        self, prompt: str, temperature: float, max_tokens: int, timeout_s: float
    ) -> str:
        from google.genai.types import GenerateContentConfig  # type: ignore[import]

        config = GenerateContentConfig(
            temperature=temperature, max_output_tokens=max_tokens
        )
        for attempt in range(_MAX_RETRIES):
            try:
                resp = await asyncio.wait_for(
                    asyncio.to_thread(
                        self._client.models.generate_content,
                        model=self.model,
                        contents=prompt,
                        config=config,
                    ),
                    timeout=timeout_s,
                )
                return _require_non_empty_text(resp.text, "Gemini")
            except Exception as exc:
                if attempt == _MAX_RETRIES - 1:
                    raise
                logger.warning("Gemini text attempt %d failed: %s", attempt + 1, exc)
                await asyncio.sleep(_jitter(_BASE_BACKOFF_S * (2**attempt)))
        return ""

    async def _generate_via_vertex_proxy(
        self,
        prompt: str,
        response_model: Type[BaseModel],
        temperature: float,
        max_tokens: int,
    ) -> BaseModel:
        proxy_url = _vertex_proxy_url()
        if not proxy_url:
            raise RuntimeError(
                "No Gemini credentials configured and VERTEX_PROXY_URL is not set. "
                "Set GOOGLE_API_KEY, VERTEX_PROJECT, or VERTEX_PROXY_URL."
            )
        import httpx  # type: ignore[import]

        payload = {
            "model": self.model,
            "prompt": prompt,
            "temperature": temperature,
            "max_tokens": max_tokens,
            "response_schema": response_model.model_json_schema(),
        }
        for attempt in range(_MAX_RETRIES):
            try:
                async with httpx.AsyncClient(timeout=60.0) as client:
                    r = await client.post(f"{proxy_url}/generate", json=payload)
                    r.raise_for_status()
                    return response_model.model_validate(r.json())
            except Exception as exc:
                if attempt == _MAX_RETRIES - 1:
                    raise
                logger.warning("Vertex proxy attempt %d failed: %s", attempt + 1, exc)
                await asyncio.sleep(_jitter(_BASE_BACKOFF_S * (2**attempt)))
        raise RuntimeError("Vertex proxy exhausted all retries")

    async def _generate_text_proxy(
        self, prompt: str, temperature: float, max_tokens: int
    ) -> str:
        proxy_url = _vertex_proxy_url()
        if not proxy_url:
            raise RuntimeError("No Gemini credentials and no VERTEX_PROXY_URL")
        import httpx  # type: ignore[import]

        payload = {
            "model": self.model,
            "prompt": prompt,
            "temperature": temperature,
            "max_tokens": max_tokens,
        }
        async with httpx.AsyncClient(timeout=30.0) as client:
            r = await client.post(f"{proxy_url}/generate-text", json=payload)
            r.raise_for_status()
            return _require_non_empty_text(r.json().get("text", ""), "Vertex proxy")

    async def embed(
        self,
        text: str,
        model: str = "text-embedding-004",
        task_type: str = "RETRIEVAL_QUERY",
    ) -> list[float]:
        """Generate a single embedding vector."""
        if self._client is None:
            raise RuntimeError(
                "Embeddings require native google-genai SDK (Google API Key or GCP Project)"
            )

        from google.genai.types import EmbedContentConfig  # type: ignore[import]

        config = EmbedContentConfig(task_type=task_type)

        resp = await asyncio.to_thread(
            self._client.models.embed_content, model=model, contents=text, config=config
        )
        if not resp.embeddings:
            return []
        return resp.embeddings[0].values

    async def embed_batch(
        self,
        texts: list[str],
        model: str = "text-embedding-004",
        task_type: str = "RETRIEVAL_DOCUMENT",
    ) -> list[list[float]]:
        """Generate multiple embedding vectors in one call."""
        if not texts:
            return []
        if self._client is None:
            raise RuntimeError("Embeddings require native google-genai SDK")

        from google.genai.types import EmbedContentConfig  # type: ignore[import]

        config = EmbedContentConfig(task_type=task_type)

        resp = await asyncio.to_thread(
            self._client.models.embed_content,
            model=model,
            contents=texts,
            config=config,
        )
        return [e.values for e in resp.embeddings]


gemini_service = GeminiService()
