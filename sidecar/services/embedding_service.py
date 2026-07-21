"""Provider-aware embedding service with Azure OpenAI recovery logic."""
from __future__ import annotations

import asyncio
import logging
import os
from dataclasses import dataclass

import httpx

from services.gemini_service import gemini_service

logger = logging.getLogger(__name__)

try:
    import tiktoken
except ImportError:  # pragma: no cover
    tiktoken = None

AZURE_PROVIDER_ALIASES = {"azure_openai", "azure", "azureopenai"}
DEFAULT_API_VERSION = "2024-10-21"
DEFAULT_BATCH_SIZE = 8
DEFAULT_OUTPUT_DIMENSION = 768
DEFAULT_MAX_INPUT_TOKENS = 8000
MAX_RETRIES = 3


class AzureEmbeddingBadRequest(RuntimeError):
    """Raised when Azure rejects an embedding input as invalid."""


@dataclass(slots=True)
class _PendingEmbedding:
    index: int
    text: str


def _load_encoding():
    if tiktoken is None:
        return None
    try:
        return tiktoken.encoding_for_model("text-embedding-3-large")
    except Exception:
        return tiktoken.get_encoding("cl100k_base")


class EmbeddingService:
    """Generate embeddings via Azure OpenAI or Gemini, depending on env."""

    def __init__(self) -> None:
        self.provider = (os.getenv("EMBEDDING_PROVIDER") or "gemini").strip().lower()
        self.endpoint = (os.getenv("AZURE_OPENAI_ENDPOINT") or "").strip().rstrip("/")
        self.api_key = (os.getenv("AZURE_OPENAI_API_KEY") or "").strip()
        self.deployment = (
            os.getenv("AZURE_OPENAI_EMBEDDING_DEPLOYMENT")
            or os.getenv("AZURE_OPENAI_DEPLOYMENT")
            or os.getenv("EMBEDDING_MODEL_STANDARD_ID")
            or ""
        ).strip()
        self.api_version = (os.getenv("AZURE_OPENAI_API_VERSION") or DEFAULT_API_VERSION).strip()
        self.batch_size = max(
            1,
            int(os.getenv("AZURE_OPENAI_EMBED_BATCH_SIZE") or DEFAULT_BATCH_SIZE),
        )
        self.output_dimension = max(
            1,
            int(os.getenv("EMBEDDING_OUTPUT_DIMENSION") or DEFAULT_OUTPUT_DIMENSION),
        )
        self.max_input_tokens = max(
            256,
            int(os.getenv("AZURE_OPENAI_EMBED_MAX_INPUT_TOKENS") or DEFAULT_MAX_INPUT_TOKENS),
        )
        self.client = httpx.AsyncClient(timeout=httpx.Timeout(60.0, connect=10.0))
        self._encoding = _load_encoding()

    def is_ready(self) -> bool:
        if self.provider in AZURE_PROVIDER_ALIASES:
            return bool(self.endpoint and self.api_key and self.deployment)
        return gemini_service.is_ready()

    async def close(self) -> None:
        await self.client.aclose()

    def count_tokens(self, text: str) -> int:
        normalized = self._normalize_text(text)
        if not normalized:
            return 0
        if self._encoding is None:
            return max(1, len(normalized) // 4)
        return len(self._encoding.encode(normalized))

    async def embed_single_async(self, text: str) -> list[float]:
        vectors = await self.embed_batch_async([text])
        return vectors[0] if vectors else self._zero_vector()

    async def embed_batch_async(self, texts: list[str]) -> list[list[float]]:
        if not texts:
            return []
        if self.provider in AZURE_PROVIDER_ALIASES:
            return await self._embed_batch_via_azure(texts)
        return await self._embed_batch_via_gemini(texts)

    async def _embed_batch_via_gemini(self, texts: list[str]) -> list[list[float]]:
        results = [self._zero_vector() for _ in texts]
        pending = []
        for index, text in enumerate(texts):
            normalized = self._normalize_text(text)
            if normalized:
                pending.append(_PendingEmbedding(index=index, text=normalized))

        if not pending:
            return results

        embeddings = await gemini_service.embed_batch([item.text for item in pending])
        for item, vector in zip(pending, embeddings):
            results[item.index] = vector
        return results

    async def _embed_batch_via_azure(self, texts: list[str]) -> list[list[float]]:
        if not self.is_ready():
            raise RuntimeError(
                "Azure OpenAI embedding is not configured. "
                "Set AZURE_OPENAI_ENDPOINT, AZURE_OPENAI_API_KEY, and AZURE_OPENAI_EMBEDDING_DEPLOYMENT."
            )

        results = [self._zero_vector() for _ in texts]
        pending = []
        for index, text in enumerate(texts):
            normalized = self._normalize_text(text)
            if normalized:
                pending.append(_PendingEmbedding(index=index, text=self._prepare_text(normalized)))

        if not pending:
            return results

        for start in range(0, len(pending), self.batch_size):
            batch = pending[start : start + self.batch_size]
            recovered = await self._embed_with_recovery(batch)
            for index, vector in recovered.items():
                results[index] = vector

        return results

    async def _embed_with_recovery(self, batch: list[_PendingEmbedding]) -> dict[int, list[float]]:
        texts = [item.text for item in batch]
        try:
            embeddings = await self._request_embeddings(texts)
            return {item.index: embedding for item, embedding in zip(batch, embeddings)}
        except AzureEmbeddingBadRequest as exc:
            if len(batch) == 1:
                recovered = await self._recover_single_embedding(batch[0], exc)
                return {batch[0].index: recovered}

            midpoint = max(1, len(batch) // 2)
            left = await self._embed_with_recovery(batch[:midpoint])
            right = await self._embed_with_recovery(batch[midpoint:])
            left.update(right)
            return left

    async def _recover_single_embedding(
        self,
        item: _PendingEmbedding,
        original_error: AzureEmbeddingBadRequest,
    ) -> list[float]:
        logger.warning(
            "Azure embedding rejected a single chunk; attempting recovery (%s)",
            str(original_error)[:200],
        )
        for candidate in self._iter_recovery_candidates(item.text):
            try:
                embeddings = await self._request_embeddings([candidate])
                logger.warning(
                    "Recovered Azure embedding after truncation for chunk %s",
                    item.index,
                )
                return embeddings[0]
            except AzureEmbeddingBadRequest:
                continue

        logger.error(
            "Falling back to zero vector after persistent Azure embedding rejection for chunk %s",
            item.index,
        )
        return self._zero_vector()

    async def _request_embeddings(self, texts: list[str]) -> list[list[float]]:
        url = f"{self.endpoint}/openai/deployments/{self.deployment}/embeddings"
        headers = {
            "api-key": self.api_key,
            "Content-Type": "application/json",
        }
        payload: dict[str, object] = {"input": texts}
        if self.output_dimension > 0:
            payload["dimensions"] = self.output_dimension

        last_error = "unknown_error"
        for attempt in range(1, MAX_RETRIES + 1):
            try:
                response = await self.client.post(
                    url,
                    params={"api-version": self.api_version},
                    json=payload,
                    headers=headers,
                )
            except httpx.HTTPError as exc:
                last_error = str(exc)
                if attempt == MAX_RETRIES:
                    break
                await asyncio.sleep(0.5 * attempt)
                continue

            if response.status_code == 400:
                raise AzureEmbeddingBadRequest(self._extract_error_message(response))

            if response.status_code in {408, 429} or response.status_code >= 500:
                last_error = self._extract_error_message(response)
                if attempt == MAX_RETRIES:
                    break
                await asyncio.sleep(0.5 * attempt)
                continue

            response.raise_for_status()
            payload_json = response.json()
            data = payload_json.get("data")
            if not isinstance(data, list):
                raise RuntimeError("Azure OpenAI embedding response is missing data")

            ordered = sorted(data, key=lambda item: int(item.get("index", 0)))
            embeddings = [item.get("embedding") for item in ordered]
            if len(embeddings) != len(texts) or any(not isinstance(vec, list) for vec in embeddings):
                raise RuntimeError("Azure OpenAI embedding response shape mismatch")
            return embeddings

        raise RuntimeError(f"Azure OpenAI embedding failed: {last_error}")

    def _iter_recovery_candidates(self, text: str):
        token_count = self.count_tokens(text)
        budgets: list[int] = []
        for candidate in (
            min(self.max_input_tokens * 3 // 4, token_count - 1),
            self.max_input_tokens // 2,
            self.max_input_tokens // 4,
        ):
            if candidate > 0 and candidate < token_count:
                budgets.append(candidate)

        seen: set[str] = set()
        for budget in budgets:
            reduced = self._truncate_to_tokens(text, budget)
            if reduced and reduced != text and reduced not in seen:
                seen.add(reduced)
                yield reduced

    def _prepare_text(self, text: str) -> str:
        return self._truncate_to_tokens(text, self.max_input_tokens)

    def _truncate_to_tokens(self, text: str, max_tokens: int) -> str:
        if max_tokens <= 0:
            return ""
        if self._encoding is None:
            max_chars = max_tokens * 4
            return text[:max_chars].strip()
        tokens = self._encoding.encode(text)
        if len(tokens) <= max_tokens:
            return text
        return self._encoding.decode(tokens[:max_tokens]).strip()

    @staticmethod
    def _normalize_text(text: str) -> str:
        return str(text or "").replace("\x00", " ").strip()

    def _zero_vector(self) -> list[float]:
        return [0.0] * self.output_dimension

    @staticmethod
    def _extract_error_message(response: httpx.Response) -> str:
        try:
            payload = response.json()
        except ValueError:
            return response.text[:400]

        error = payload.get("error")
        if isinstance(error, dict):
            message = error.get("message") or error.get("code") or payload
            return str(message)[:400]
        return str(error or payload)[:400]


embedding_service = EmbeddingService()
