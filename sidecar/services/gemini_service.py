# cloudrun/services/gemini_service.py
"""
Canonical Gemini LLM gateway for CloudRun services.
Priority: native google-genai SDK → Vertex AI proxy → exception.
All methods are async. Retries with exponential backoff + jitter.
"""

from __future__ import annotations

import asyncio
import hashlib
import json
import logging
import os
import random
import threading
import time
from contextlib import suppress
from typing import Any, AsyncIterator, Type

from pydantic import BaseModel
from services.genai_native_compat import (
    build_embed_content_config,
    build_generate_content_config,
    build_optional_thinking_config,
    native_client_http_options,
)
from services.secret_manager import (
    get_google_api_key,
    get_google_api_key_resolution,
    get_project_id_resolution,
)

logger = logging.getLogger(__name__)


def _load_model_tier(tier: str, default: str) -> str:
    for path in [
        os.getenv("WISDEV_MODEL_CONFIG", "").strip(),
        "wisdev_models.json",
        "../wisdev_models.json",
        "../../wisdev_models.json",
        "../../../wisdev_models.json",
        "scholar_models.json",
        "../scholar_models.json",
        "../../scholar_models.json",
        "../../../scholar_models.json",
    ]:
        if path and os.path.exists(path):
            with open(path, "r", encoding="utf-8") as f:
                models = json.load(f)
                return models.get(tier, default)
    return default


GEMINI_LIGHT_MODEL = os.getenv(
    "GEMINI_LIGHT_MODEL", _load_model_tier("light", "gemini-2.5-flash-lite")
)
GEMINI_STANDARD_MODEL = os.getenv(
    "GEMINI_STANDARD_MODEL", _load_model_tier("standard", "gemini-2.5-flash")
)
GEMINI_HEAVY_MODEL = os.getenv(
    "GEMINI_HEAVY_MODEL", _load_model_tier("heavy", "gemini-2.5-pro")
)
GEMINI_EMBED_STANDARD_MODEL = os.getenv(
    "EMBEDDING_MODEL_STANDARD_ID", "gemini-embedding-001"
)
GEMINI_EMBED_PRIMARY_MODEL = os.getenv(
    "EMBEDDING_MODEL_PRIMARY_ID", GEMINI_EMBED_STANDARD_MODEL
)
GEMINI_EMBED_FALLBACK_MODEL = os.getenv(
    "EMBEDDING_MODEL_FALLBACK_ID", "text-embedding-005"
)

_MAX_RETRIES = 3
_BASE_BACKOFF_S = 1.0
_RUNTIME_SERVICE_LOCK = threading.Lock()
_RUNTIME_DIAGNOSTICS_LOCK = threading.Lock()
_runtime_service_cache_key: tuple[str, str, str, str] | None = None
_runtime_service_cache: "GeminiService | None" = None
_last_runtime_diagnostics_signature: tuple[str, str, str, bool, str] | None = None
_PROCESS_START_MONOTONIC = time.monotonic()
_COLD_START_WINDOW_MS = 90_000
_DEFAULT_TEXT_LATENCY_BUDGET_MS = 30_000
_DEFAULT_STRUCTURED_LATENCY_BUDGET_MS = 45_000
_MIN_LATENCY_BUDGET_MS = 4_000
_MAX_LATENCY_BUDGET_MS = 90_000
_STANDARD_RETRY_WEIGHTS = (0.6, 0.25, 0.15)
_COLD_START_RETRY_WEIGHTS = (0.75, 0.17, 0.08)


class StructuredOutputRequiresNativeRuntimeError(RuntimeError):
    """Raised when structured output is requested on a non-native runtime."""


async def _run_sync_with_timeout(timeout_s: float, func, *args, **kwargs):
    task = asyncio.create_task(asyncio.to_thread(func, *args, **kwargs))
    try:
        return await asyncio.wait_for(task, timeout=timeout_s)
    except Exception:
        if not task.done():
            task.cancel()
            with suppress(asyncio.CancelledError):
                await task
        raise


def _jitter(base: float) -> float:
    return base + random.uniform(0, base * 0.5)


def _gemini_api_key() -> str:
    return get_google_api_key()


def _vertex_project() -> str:
    resolution = get_project_id_resolution()
    if resolution.get("projectConfigured"):
        project_source = str(resolution.get("projectSource") or "")
        if project_source.startswith("env:"):
            env_key = project_source.removeprefix("env:")
            return str(os.getenv(env_key, "")).strip()

    return str(resolution.get("projectId") or "").strip()


def _vertex_location() -> str:
    return (
        os.getenv("GOOGLE_CLOUD_LOCATION", "").strip()
        or os.getenv("GOOGLE_CLOUD_REGION", "").strip()
        or "us-central1"
    )


def _vertex_proxy_url() -> str:
    return os.getenv("VERTEX_PROXY_URL", "").strip().rstrip("/")


def _gemini_runtime_mode() -> str:
    mode = os.getenv("GEMINI_RUNTIME_MODE", "auto").strip().lower()
    if mode in {"auto", "native", "vertex_proxy"}:
        return mode
    logger.warning("Unknown GEMINI_RUNTIME_MODE=%s; defaulting to auto", mode)
    return "auto"


def _normalize_service_tier(service_tier: str | None) -> str | None:
    normalized = str(service_tier or "").strip().lower()
    if normalized in {"flex", "priority", "standard"}:
        return normalized
    return None


def _normalize_thinking_budget(thinking_budget: int | None) -> int | None:
    if thinking_budget is None:
        return None
    return int(thinking_budget)


def _supports_thinking_budget(model: str | None) -> bool:
    normalized = str(model or "").strip().lower()
    return normalized.startswith("gemini-2.5")


def _supports_thinking_level(model: str | None) -> bool:
    normalized = str(model or "").strip().lower()
    return normalized.startswith("gemini-3")


def _thinking_budget_limits(model: str | None) -> tuple[int, int, bool] | None:
    normalized = str(model or "").strip().lower()
    if normalized.startswith("gemini-2.5-flash-lite"):
        return 512, 24_576, True
    if normalized.startswith("gemini-2.5-flash"):
        return 1, 24_576, True
    if normalized.startswith("gemini-2.5-pro"):
        return 128, 32_768, False
    return None


def _service_uptime_ms() -> int:
    return int((time.monotonic() - _PROCESS_START_MONOTONIC) * 1000)


def _is_cold_start_window() -> bool:
    return _service_uptime_ms() < _COLD_START_WINDOW_MS


def _normalize_latency_budget_ms(
    latency_budget_ms: int | None,
    *,
    structured: bool,
) -> int:
    default_budget = (
        _DEFAULT_STRUCTURED_LATENCY_BUDGET_MS
        if structured
        else _DEFAULT_TEXT_LATENCY_BUDGET_MS
    )
    if latency_budget_ms is None:
        return default_budget
    try:
        return min(
            max(int(latency_budget_ms), _MIN_LATENCY_BUDGET_MS),
            _MAX_LATENCY_BUDGET_MS,
        )
    except (TypeError, ValueError):
        return default_budget


def _retry_timeout_s(latency_budget_ms: int, attempt: int) -> float:
    weights = (
        _COLD_START_RETRY_WEIGHTS
        if _is_cold_start_window()
        else _STANDARD_RETRY_WEIGHTS
    )
    weight = weights[min(attempt, len(weights) - 1)]
    timeout_s = max(4.0, (latency_budget_ms * weight) / 1000.0)
    return round(timeout_s, 3)


def _resolve_thinking_budget(
    model: str | None,
    thinking_budget: int | None,
    *,
    latency_budget_ms: int,
    structured: bool,
) -> int | None:
    if not _supports_thinking_budget(model):
        return None

    limits = _thinking_budget_limits(model)
    if limits is None:
        return None
    minimum, maximum, can_disable = limits
    normalized_budget = _normalize_thinking_budget(thinking_budget)

    if normalized_budget == -1:
        return -1
    if normalized_budget == 0:
        if can_disable:
            return 0
        logger.info(
            "Thinking cannot be disabled for model=%s; using minimum thinking budget=%s",
            model,
            minimum,
        )
        return minimum
    if normalized_budget is not None:
        return min(max(normalized_budget, minimum), maximum)

    if latency_budget_ms <= 12_000:
        return 0 if can_disable else minimum
    if latency_budget_ms <= 25_000:
        return min(max(1_024, minimum), maximum)
    return -1


def _resolve_thinking_level(
    model: str | None,
    thinking_budget: int | None,
    *,
    latency_budget_ms: int,
) -> str | None:
    if not _supports_thinking_level(model):
        return None
    normalized_budget = _normalize_thinking_budget(thinking_budget)
    if normalized_budget is None:
        normalized_budget = (
            0
            if latency_budget_ms <= 12_000
            else 1024
            if latency_budget_ms <= 25_000
            else 8192
        )
    if normalized_budget < 0:
        return "HIGH"
    if normalized_budget == 0:
        return "MINIMAL"
    if normalized_budget <= 1024:
        return "LOW"
    if normalized_budget <= 8192:
        return "MEDIUM"
    return "HIGH"


def _resolve_reasoning_controls(
    model: str | None,
    thinking_budget: int | None,
    *,
    latency_budget_ms: int,
    structured: bool,
) -> dict[str, int | str | None]:
    thinking_level = _resolve_thinking_level(
        model,
        thinking_budget,
        latency_budget_ms=latency_budget_ms,
    )
    if thinking_level is not None:
        return {"thinking_budget": None, "thinking_level": thinking_level}
    return {
        "thinking_budget": _resolve_thinking_budget(
            model,
            thinking_budget,
            latency_budget_ms=latency_budget_ms,
            structured=structured,
        ),
        "thinking_level": None,
    }


def _log_budget_event(event: str, **fields: Any) -> None:
    logger.info("%s | %s", event, json.dumps(fields, sort_keys=True, default=str))


def _service_tier_for_wisdev(mode: str | None, interactive: bool = False) -> str | None:
    normalized_mode = str(mode or "").strip().lower()
    if interactive:
        return "priority"
    if normalized_mode in {"yolo", "autonomous"}:
        return "flex"
    if normalized_mode in {"guided", "constraint"}:
        return "standard"
    return None


def _native_generate_config_supports(field_name: str) -> bool:
    """Return whether the installed google-genai config model accepts a field."""
    try:
        from google.genai.types import GenerateContentConfig  # type: ignore[import]
    except ImportError:
        return False

    model_fields = getattr(GenerateContentConfig, "model_fields", None)
    if isinstance(model_fields, dict):
        return field_name in model_fields
    return hasattr(GenerateContentConfig, field_name)


_OPTIONAL_NATIVE_GENERATE_CONFIG_FIELDS = frozenset({"service_tier"})
_OPTIONAL_NATIVE_EMBED_CONFIG_FIELDS = frozenset({"task_type"})
 

def _log_generate_config_compat(operation: str, dropped_fields: list[str]) -> None:
    logger.warning(
        "gemini_native_generate_config_compat | %s",
        json.dumps(
            {
                "operation": operation,
                "dropped_fields": dropped_fields,
            },
            sort_keys=True,
            default=str,
        ),
    )


def _log_embed_config_compat(operation: str, dropped_fields: list[str]) -> None:
    logger.warning(
        "gemini_native_embed_config_compat | %s",
        json.dumps(
            {
                "operation": operation,
                "dropped_fields": dropped_fields,
            },
            sort_keys=True,
            default=str,
        ),
    )


def _build_native_generate_config(config_kwargs: dict[str, Any], *, operation: str) -> Any:
    return build_generate_content_config(
        config_kwargs,
        optional_fields=_OPTIONAL_NATIVE_GENERATE_CONFIG_FIELDS,
        operation=operation,
        on_dropped_fields=_log_generate_config_compat,
    )


def _build_native_embed_config(config_kwargs: dict[str, Any], *, operation: str) -> Any:
    return build_embed_content_config(
        config_kwargs,
        optional_fields=_OPTIONAL_NATIVE_EMBED_CONFIG_FIELDS,
        operation=operation,
        on_dropped_fields=_log_embed_config_compat,
    )


def _resolve_embedding_model(model: str | None) -> str:
    normalized = str(model or "").strip()
    if not normalized:
        return GEMINI_EMBED_STANDARD_MODEL

    alias = normalized.lower()
    if alias in {"primary"}:
        return GEMINI_EMBED_PRIMARY_MODEL
    if alias in {"standard", "balanced"}:
        return GEMINI_EMBED_STANDARD_MODEL
    if alias in {"fallback"}:
        return GEMINI_EMBED_FALLBACK_MODEL
    return normalized


def _gemini_runtime_source() -> str:
    api_key_resolution = get_google_api_key_resolution()
    project_configured = bool(_vertex_project())
    proxy_configured = bool(_vertex_proxy_url())

    if api_key_resolution.get("status") == "ok":
        return str(api_key_resolution.get("source") or "env")
    if project_configured:
        return "vertex_project"
    if proxy_configured:
        return "vertex_proxy"
    return "none"


def _gemini_api_key_fingerprint() -> str:
    api_key = _gemini_api_key()
    if not api_key:
        return ""
    return hashlib.sha256(api_key.encode("utf-8")).hexdigest()[:16]


def _runtime_service_key() -> tuple[str, str, str, str]:
    return (
        _gemini_runtime_mode(),
        _gemini_api_key_fingerprint(),
        _vertex_project(),
        _vertex_proxy_url(),
    )


def _gemini_runtime_unavailable_detail(
    source: str, mode: str, project_configured: bool, proxy_configured: bool
) -> str:
    if source in {"env", "secret_manager"}:
        return f"Gemini API key is configured from {source} but the native client is unavailable."
    if source == "vertex_project":
        if proxy_configured:
            return "Vertex project is configured but the native Gemini client is unavailable; proxy fallback was not usable."
        return (
            "Vertex project is configured but the native Gemini client is unavailable."
        )
    if source == "vertex_proxy":
        return "GEMINI_RUNTIME_MODE=vertex_proxy requires VERTEX_PROXY_URL."
    if mode == "native":
        return "GEMINI_RUNTIME_MODE=native requires a working GOOGLE_API_KEY or GOOGLE_CLOUD_PROJECT."
    if project_configured or proxy_configured:
        return "Gemini runtime is configured but unavailable."
    return "Set GOOGLE_API_KEY, GOOGLE_CLOUD_PROJECT, or VERTEX_PROXY_URL."


def _log_runtime_diagnostics_transition(diagnostics: dict[str, str | bool]) -> None:
    global _last_runtime_diagnostics_signature

    signature = (
        str(diagnostics.get("status") or ""),
        str(diagnostics.get("source") or ""),
        str(diagnostics.get("mode") or ""),
        bool(diagnostics.get("ready")),
        str(diagnostics.get("detail") or ""),
    )

    with _RUNTIME_DIAGNOSTICS_LOCK:
        if _last_runtime_diagnostics_signature == signature:
            return
        _last_runtime_diagnostics_signature = signature

    log_message = (
        "Gemini runtime status changed: status=%s source=%s mode=%s ready=%s "
        "project_configured=%s proxy_configured=%s detail=%s"
    )
    log_args = (
        signature[0],
        signature[1],
        signature[2],
        signature[3],
        bool(diagnostics.get("projectConfigured")),
        bool(diagnostics.get("proxyConfigured")),
        signature[4],
    )
    if bool(diagnostics.get("ready")):
        logger.info(log_message, *log_args)
    else:
        logger.warning(log_message, *log_args)


def get_gemini_runtime_diagnostics(
    service: "GeminiService | None" = None,
) -> dict[str, str | bool]:
    mode = _gemini_runtime_mode()
    source = _gemini_runtime_source()
    project_configured = bool(_vertex_project())
    proxy_configured = bool(_vertex_proxy_url())
    diagnostics: dict[str, str | bool]

    if source == "none":
        diagnostics = {
            "status": "missing",
            "source": source,
            "mode": mode,
            "projectConfigured": project_configured,
            "proxyConfigured": proxy_configured,
            "ready": False,
            "detail": _gemini_runtime_unavailable_detail(
                source, mode, project_configured, proxy_configured
            ),
        }
        _log_runtime_diagnostics_transition(diagnostics)
        return diagnostics

    active_service = service if service is not None else _get_runtime_service()
    ready = active_service.is_ready()
    status = "configured" if ready else "degraded"

    diagnostics = {
        "status": status,
        "source": source,
        "mode": mode,
        "projectConfigured": project_configured,
        "proxyConfigured": proxy_configured,
        "ready": ready,
        "detail": ""
        if ready
        else _gemini_runtime_unavailable_detail(
            source, mode, project_configured, proxy_configured
        ),
    }
    _log_runtime_diagnostics_transition(diagnostics)
    return diagnostics


def _cosine_similarity(a: list[float], b: list[float]) -> float:
    """Compute cosine similarity between two equal-length vectors."""
    if not a or not b or len(a) != len(b):
        return 0.0
    dot = sum(x * y for x, y in zip(a, b))
    norm_a = sum(x * x for x in a) ** 0.5
    norm_b = sum(x * x for x in b) ** 0.5
    if norm_a == 0 or norm_b == 0:
        return 0.0
    return dot / (norm_a * norm_b)


def _token_set(text: str) -> set[str]:
    """Return the set of lowercase alpha-numeric tokens longer than 2 chars."""
    import re

    return {t for t in re.findall(r"[a-z0-9]+", text.lower()) if len(t) > 2}


def _require_non_empty_text(text: Any, source: str) -> str:
    normalized = str(text or "").strip()
    if not normalized:
        raise RuntimeError(f"{source} returned empty text")
    return normalized


def _normalize_structured_json_text(text: Any, source: str) -> tuple[str, bool]:
    normalized = _require_non_empty_text(text, source)
    try:
        parsed = json.loads(normalized)
        return json.dumps(parsed, ensure_ascii=False, separators=(",", ":")), False
    except json.JSONDecodeError as exc:
        raise RuntimeError(f"{source} returned invalid JSON for structured output") from exc


def _safe_response_attr(value: Any, name: str) -> Any:
    if isinstance(value, dict):
        return value.get(name)
    try:
        return getattr(value, name)
    except Exception:
        return None


def _iter_response_sequence(value: Any) -> list[Any]:
    if value is None or isinstance(value, (str, bytes, bytearray)):
        return []
    if isinstance(value, list):
        return value
    if isinstance(value, tuple):
        return list(value)
    try:
        return list(value)
    except TypeError:
        return []


def _iter_structured_response_values(response: Any) -> list[Any]:
    values: list[Any] = []
    if isinstance(response, (dict, list, str, bytes, bytearray, BaseModel)):
        values.append(response)

    values.append(_safe_response_attr(response, "text"))
    values.append(_safe_response_attr(response, "parsed"))

    for candidate in _iter_response_sequence(_safe_response_attr(response, "candidates")):
        candidate_content = _safe_response_attr(candidate, "content")
        values.append(_safe_response_attr(candidate, "text"))
        values.append(_safe_response_attr(candidate, "parsed"))
        for content in (candidate_content, candidate):
            for part in _iter_response_sequence(_safe_response_attr(content, "parts")):
                values.append(_safe_response_attr(part, "text"))
                values.append(_safe_response_attr(part, "parsed"))
                values.append(_safe_response_attr(part, "json"))
                values.append(_safe_response_attr(part, "data"))

    return values


def _normalize_native_structured_json_text(text: Any, source: str) -> str:
    normalized = _require_non_empty_text(text, source)
    try:
        parsed = json.loads(normalized)
    except json.JSONDecodeError as exc:
        raise RuntimeError(
            f"{source} returned invalid native structured JSON"
        ) from exc
    return json.dumps(parsed, ensure_ascii=False, separators=(",", ":"))


def _json_text_from_structured_value(value: Any, source: str) -> tuple[str, bool] | None:
    if value is None:
        return None
    if isinstance(value, BaseModel):
        return value.model_dump_json(), True
    if isinstance(value, (dict, list)):
        return json.dumps(value, ensure_ascii=False, separators=(",", ":")), True

    model_dump = getattr(value, "model_dump", None)
    if callable(model_dump):
        dumped = model_dump()
        if isinstance(dumped, (dict, list)):
            return json.dumps(dumped, ensure_ascii=False, separators=(",", ":")), True

    if isinstance(value, (bytes, bytearray)):
        value = value.decode("utf-8", errors="replace")

    if isinstance(value, str):
        if not value.strip():
            return None
        return _normalize_native_structured_json_text(value, source), False

    return None


def _normalize_structured_json_response(response: Any, source: str) -> tuple[str, bool]:
    saw_non_empty = False
    for value in _iter_structured_response_values(response):
        if value is None:
            continue
        if isinstance(value, str) and not value.strip():
            continue
        saw_non_empty = True
        try:
            result = _json_text_from_structured_value(value, source)
        except RuntimeError:
            continue
        if result is not None:
            return result

    if saw_non_empty:
        raise RuntimeError(f"{source} returned invalid JSON for structured output")
    raise RuntimeError(f"{source} returned empty text")


def _prepare_schema_for_provider(schema: dict[str, Any]) -> dict[str, Any]:
    """Add propertyOrdering recursively so native structured output stays stable."""
    import copy

    prepared = copy.deepcopy(schema)

    def _annotate(node: Any) -> Any:
        if not isinstance(node, dict):
            return node
        if node.get("type") == "object" or "properties" in node:
            props = node.get("properties")
            if isinstance(props, dict):
                if "propertyOrdering" not in node:
                    node["propertyOrdering"] = list(props.keys())
                node["properties"] = {key: _annotate(value) for key, value in props.items()}
        if "items" in node:
            node["items"] = _annotate(node["items"])
        if "additionalProperties" in node and isinstance(node["additionalProperties"], dict):
            node["additionalProperties"] = _annotate(node["additionalProperties"])
        for meta_key in ("$defs", "definitions"):
            if meta_key in node and isinstance(node[meta_key], dict):
                node[meta_key] = {
                    key: _annotate(value) for key, value in node[meta_key].items()
                }
        for combo_key in ("anyOf", "oneOf", "allOf"):
            if combo_key in node and isinstance(node[combo_key], list):
                node[combo_key] = [_annotate(value) for value in node[combo_key]]
        return node

    return _annotate(prepared)


class GeminiService:
    """
    Wraps google-genai SDK with:
      - Native structured output (response_mime_type + response_json_schema)
      - Extended thinking via ThinkingConfig
      - Vertex AI proxy fallback
      - Per-call timeout + exponential backoff with jitter
    """

    def __init__(self, model: str = GEMINI_LIGHT_MODEL) -> None:
        self.model = model
        self._client = self._build_client()

    def is_ready(self) -> bool:
        """Return True if the service has valid credentials or a proxy URL."""
        return self._client is not None or bool(_vertex_proxy_url())

    async def warm_up(self, timeout_s: float = 10.0) -> dict[str, Any]:
        """Warm-up probe: forces lazy initialization paths and reports readiness.

        Call this during server startup to absorb cold-start latency from:
        - GCP ADC token acquisition (can take 2-5s first time)
        - SDK client initialization
        - First model list / health check

        Returns a diagnostic dict with timing information.
        """
        import time as _time

        start = _time.monotonic()
        result: dict[str, Any] = {
            "ready": False,
            "transport": "native_sdk" if self._client is not None else "vertex_proxy",
            "model": self.model,
            "cold_start_window": _is_cold_start_window(),
            "uptime_ms": _service_uptime_ms(),
        }

        try:
            if self._client is not None:
                # Force a lightweight generation to warm up the ADC token cache,
                # SDK connection pool, and model routing.
                probe_resp = await _run_sync_with_timeout(
                    timeout_s,
                    self._client.models.generate_content,
                    model=self.model,
                    contents="Reply with exactly: OK",
                    config=None,
                )
                result["ready"] = bool(probe_resp and probe_resp.text)
                result["probe_response"] = (probe_resp.text or "")[:20]
            elif _vertex_proxy_url():
                import httpx  # type: ignore[import]

                proxy_url = _vertex_proxy_url()
                async with httpx.AsyncClient(timeout=timeout_s) as client:
                    r = await client.get(f"{proxy_url}/health")
                    result["ready"] = r.status_code < 400
                    result["proxy_status"] = r.status_code
            else:
                result["ready"] = False
                result["detail"] = "no credentials or proxy configured"
        except Exception as exc:
            result["ready"] = False
            result["error"] = str(exc)
            logger.warning(
                "gemini_warm_up_failed | %s",
                json.dumps(result, sort_keys=True, default=str),
            )

        result["latency_ms"] = int((_time.monotonic() - start) * 1000)
        if result["ready"]:
            logger.info(
                "gemini_warm_up_success | %s",
                json.dumps(result, sort_keys=True, default=str),
            )
        return result

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

            # Prefer Vertex AI (GCP credits, no free-tier rate limits) over the
            # Gemini Developer API key. A GOOGLE_CLOUD_PROJECT value means the
            # caller has GCP credentials — use Vertex AI first regardless of
            # whether GOOGLE_API_KEY is also present in the environment.
            if project:
                logger.info("GeminiService using Vertex AI SDK (project: %s)", project)
                client_kwargs: dict[str, Any] = {
                    "vertexai": True,
                    "project": project,
                    "location": _vertex_location(),
                }
                if (http_options := native_client_http_options()) is not None:
                    client_kwargs["http_options"] = http_options
                return genai.Client(**client_kwargs)
            if api_key:
                logger.warning(
                    "GeminiService falling back to Gemini API key — "
                    "no GOOGLE_CLOUD_PROJECT configured. "
                    "Set GOOGLE_CLOUD_PROJECT to use Vertex AI credits instead."
                )
                client_kwargs = {"api_key": api_key}
                if (http_options := native_client_http_options()) is not None:
                    client_kwargs["http_options"] = http_options
                return genai.Client(**client_kwargs)
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
                "GEMINI_RUNTIME_MODE=native but no GOOGLE_CLOUD_PROJECT or GOOGLE_API_KEY is configured"
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
        service_tier: str | None = None,
        latency_budget_ms: int | None = None,
    ) -> BaseModel:
        """
        Generate structured output conforming to `response_model`.
        Uses native SDK if available, else Vertex proxy.
        """
        if self._client is not None:
            return await self._generate_native_structured(
                prompt,
                response_model,
                temperature,
                max_tokens,
                timeout_s,
                service_tier,
                latency_budget_ms=latency_budget_ms,
            )
        return await self._generate_via_vertex_proxy(
            prompt,
            response_model,
            temperature,
            max_tokens,
            service_tier,
            latency_budget_ms=latency_budget_ms,
        )

    async def generate_with_thinking(
        self,
        prompt: str,
        response_schema: Type[BaseModel],
        thinking_budget: int = 2048,
        timeout_s: float = 90.0,
        service_tier: str | None = None,
        latency_budget_ms: int | None = None,
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

            normalized_latency_budget_ms = _normalize_latency_budget_ms(
                latency_budget_ms, structured=True
            )
            resolved_thinking_budget = _resolve_thinking_budget(
                self.model,
                thinking_budget,
                latency_budget_ms=normalized_latency_budget_ms,
                structured=True,
            )
            prepared_schema = _prepare_schema_for_provider(
                response_schema.model_json_schema()
            )
            config_kwargs = dict(
                response_mime_type="application/json",
                response_json_schema=prepared_schema,
                temperature=0.3,
                max_output_tokens=8192,
            )
            if resolved_thinking_budget is not None:
                thinking_config = build_optional_thinking_config(
                    {"thinking_budget": resolved_thinking_budget},
                    operation="generate_with_thinking",
                    on_import_error=lambda _operation: logger.info(
                        "ThinkingConfig not available in SDK version; falling back to generate_json"
                    ),
                    on_compat_error=lambda _operation, exc: logger.warning(
                        "ThinkingConfig unavailable for generate_with_thinking (%s); falling back to generate_json",
                        exc,
                    ),
                )
                if thinking_config is None:
                    return await self.generate_json(
                        prompt,
                        response_schema,
                        timeout_s=timeout_s,
                        latency_budget_ms=latency_budget_ms,
                        service_tier=service_tier,
                    )
                config_kwargs["thinking_config"] = thinking_config
            if _normalize_service_tier(service_tier) and _native_generate_config_supports(
                "service_tier"
            ):
                config_kwargs["service_tier"] = _normalize_service_tier(service_tier)
            config = _build_native_generate_config(
                config_kwargs, operation="generate_with_thinking"
            )
            resp = await _run_sync_with_timeout(
                _retry_timeout_s(normalized_latency_budget_ms, 0),
                self._client.models.generate_content,
                model=self.model,
                contents=prompt,
                config=config,
            )
            return response_schema.model_validate_json(resp.text)

        except ImportError:
            logger.info("No native client dependencies; falling back to generate_json")
        except Exception as exc:
            logger.warning(
                "generate_with_thinking failed (%s); falling back to generate_json", exc
            )

        return await self.generate_json(
            prompt,
            response_schema,
            timeout_s=timeout_s,
            latency_budget_ms=latency_budget_ms,
            service_tier=service_tier,
        )

    async def generate_text(
        self,
        prompt: str,
        temperature: float = 0.7,
        max_tokens: int = 2048,
        timeout_s: float = 30.0,
        service_tier: str | None = None,
        thinking_budget: int | None = None,
        latency_budget_ms: int | None = None,
        retry_profile: str | None = None,
        request_class: str | None = None,
        trace_id: str | None = None,
    ) -> str:
        """Plain text generation (no structured output)."""
        if self._client is not None:
            return await self._generate_text_native(
                prompt,
                temperature,
                max_tokens,
                timeout_s,
                service_tier,
                thinking_budget=thinking_budget,
                latency_budget_ms=latency_budget_ms,
                retry_profile=retry_profile,
                request_class=request_class,
                trace_id=trace_id,
            )
        return await self._generate_text_proxy(
            prompt,
            temperature,
            max_tokens,
            service_tier,
            thinking_budget=thinking_budget,
            latency_budget_ms=latency_budget_ms,
            retry_profile=retry_profile,
            request_class=request_class,
            trace_id=trace_id,
        )

    async def generate_stream(
        self,
        prompt: str,
        temperature: float = 0.7,
        max_tokens: int = 2048,
        service_tier: str | None = None,
        thinking_budget: int | None = None,
        latency_budget_ms: int | None = None,
        trace_id: str | None = None,
    ) -> AsyncIterator[str]:
        """Stream text generation natively (if client is available).

        Applies a per-stream deadline derived from ``latency_budget_ms`` so the
        stream cannot stall the event loop indefinitely.  When the native SDK is
        unavailable the call degrades to a simulated chunked stream backed by
        ``generate_text`` (which already has full budget enforcement).
        """
        normalized_latency_budget_ms = _normalize_latency_budget_ms(
            latency_budget_ms, structured=False
        )
        reasoning_controls = _resolve_reasoning_controls(
            self.model,
            thinking_budget,
            latency_budget_ms=normalized_latency_budget_ms,
            structured=False,
        )
        log_context = {
            "trace_id": str(trace_id or "").strip(),
            "model": self.model,
            "latency_budget_ms": normalized_latency_budget_ms,
            "service_tier": _normalize_service_tier(service_tier),
            "thinking_budget": reasoning_controls["thinking_budget"],
            "thinking_level": reasoning_controls["thinking_level"],
            "transport": "native_sdk" if self._client is not None else "fallback_text",
            "startup_age_ms": _service_uptime_ms(),
            "cold_start_suspected": _is_cold_start_window(),
        }
        _log_budget_event("gemini_stream_start", **log_context)

        if self._client is None:
            # Fallback: degrade to a simulated chunked stream backed by
            # generate_text, which already has full budget enforcement.
            try:
                text = await self.generate_text(
                    prompt,
                    temperature,
                    max_tokens,
                    service_tier=service_tier,
                    thinking_budget=thinking_budget,
                    latency_budget_ms=latency_budget_ms,
                    trace_id=trace_id,
                )
                for chunk in text.split("\n\n"):
                    yield chunk + "\n\n"
                _log_budget_event(
                    "gemini_stream_success", **log_context, chunks="text_fallback"
                )
            except Exception as exc:
                _log_budget_event(
                    "gemini_stream_failed",
                    **log_context,
                    error=str(exc),
                    transport="fallback_text",
                )
                raise
            return

        config_kwargs: dict[str, Any] = dict(
            temperature=temperature, max_output_tokens=max_tokens
        )
        if (
            reasoning_controls["thinking_budget"] is not None
            or reasoning_controls["thinking_level"] is not None
        ) and (
            _supports_thinking_budget(self.model)
            or _supports_thinking_level(self.model)
        ):
            thinking_kwargs: dict[str, Any] = {}
            if reasoning_controls["thinking_budget"] is not None:
                thinking_kwargs["thinking_budget"] = reasoning_controls[
                    "thinking_budget"
                ]
            if reasoning_controls["thinking_level"] is not None:
                thinking_kwargs["thinking_level"] = reasoning_controls[
                    "thinking_level"
                ]
            thinking_config = build_optional_thinking_config(
                thinking_kwargs,
                operation="generate_stream",
                on_import_error=lambda _operation: logger.warning(
                    "ThinkingConfig unavailable in google-genai SDK; stream reasoning controls ignored"
                ),
                on_compat_error=lambda _operation, _exc: logger.warning(
                    "ThinkingConfig unavailable in google-genai SDK; stream reasoning controls ignored"
                ),
            )
            if thinking_config is not None:
                config_kwargs["thinking_config"] = thinking_config
        if _normalize_service_tier(service_tier) and _native_generate_config_supports(
            "service_tier"
        ):
            config_kwargs["service_tier"] = _normalize_service_tier(service_tier)
        config = _build_native_generate_config(
            config_kwargs, operation="generate_structured"
        )

        # Apply a hard deadline on the entire stream so a stalled Cloud Run
        # worker doesn't hold the event loop indefinitely.
        stream_timeout_s = _retry_timeout_s(normalized_latency_budget_ms, 0)
        chunk_count = 0
        try:
            stream_coro = self._client.aio.models.generate_content_stream(
                model=self.model,
                contents=prompt,
                config=config,
            )
            async for chunk in await asyncio.wait_for(
                stream_coro, timeout=stream_timeout_s
            ):
                if chunk.text:
                    chunk_count += 1
                    yield chunk.text
            _log_budget_event(
                "gemini_stream_success", **log_context, chunks=chunk_count
            )
        except asyncio.TimeoutError:
            _log_budget_event(
                "gemini_stream_timeout",
                **log_context,
                stream_timeout_s=stream_timeout_s,
                chunks_yielded=chunk_count,
            )
            raise
        except Exception as exc:
            _log_budget_event(
                "gemini_stream_failed",
                **log_context,
                error=str(exc),
                chunks_yielded=chunk_count,
            )
            raise

    async def generate_structured(
        self,
        prompt: str,
        json_schema: dict,
        temperature: float = 0.3,
        max_tokens: int = 2048,
        timeout_s: float = 60.0,
        service_tier: str | None = None,
        thinking_budget: int | None = None,
        retry_profile: str | None = None,
        request_class: str | None = None,
        trace_id: str | None = None,
        latency_budget_ms: int | None = None,
    ) -> str:
        """Generate structured output using a raw JSON schema dict.

        This path requires the native google-genai SDK so Gemini can enforce
        ``response_json_schema`` at generation time. Proxy mode is rejected
        because prompt-injected JSON is not an official structured-output
        implementation.
        """
        if not json_schema:
            raise ValueError("json_schema is required for structured generation")
        normalized_latency_budget_ms = _normalize_latency_budget_ms(
            latency_budget_ms, structured=True
        )
        reasoning_controls = _resolve_reasoning_controls(
            self.model,
            thinking_budget,
            latency_budget_ms=normalized_latency_budget_ms,
            structured=True,
        )
        resolved_thinking_budget = reasoning_controls["thinking_budget"]
        thinking_level = reasoning_controls["thinking_level"]
        runtime_diagnostics = get_gemini_runtime_diagnostics(self)
        log_context = {
            "trace_id": str(trace_id or "").strip(),
            "model": self.model,
            "timeout_s": timeout_s,
            "latency_budget_ms": normalized_latency_budget_ms,
            "service_tier": _normalize_service_tier(service_tier),
            "thinking_budget": resolved_thinking_budget,
            "thinking_level": thinking_level,
            "retry_profile": retry_profile,
            "request_class": request_class,
            "transport": "native_sdk" if self._client is not None else "vertex_proxy",
            "startup_age_ms": _service_uptime_ms(),
            "cold_start_window_ms": _COLD_START_WINDOW_MS,
            "cold_start_suspected": _is_cold_start_window(),
            "runtime_mode": runtime_diagnostics["mode"],
            "credential_source": runtime_diagnostics["source"],
        }
        _log_budget_event("gemini_structured_start", **log_context)
        if self._client is None:
            exc = StructuredOutputRequiresNativeRuntimeError(
                "structured output requires native Gemini runtime; "
                "vertex_proxy mode does not support response_json_schema"
            )
            _log_budget_event(
                "gemini_structured_failed",
                **log_context,
                stage="runtime_capability_check",
                error_code="STRUCTURED_OUTPUT_REQUIRES_NATIVE_RUNTIME",
                error=str(exc),
            )
            raise exc

        prepared_schema = (
            _prepare_schema_for_provider(json_schema) if json_schema else None
        )
        config_kwargs = dict(
            response_mime_type="application/json",
            temperature=temperature,
            max_output_tokens=max_tokens,
        )
        if prepared_schema is not None:
            config_kwargs["response_json_schema"] = prepared_schema
        if (resolved_thinking_budget is not None or thinking_level is not None) and (
            _supports_thinking_budget(self.model)
            or _supports_thinking_level(self.model)
        ):
            thinking_kwargs: dict[str, Any] = {}
            if resolved_thinking_budget is not None:
                thinking_kwargs["thinking_budget"] = resolved_thinking_budget
            if thinking_level is not None:
                thinking_kwargs["thinking_level"] = thinking_level
            thinking_config = build_optional_thinking_config(
                thinking_kwargs,
                operation="generate_structured",
                on_import_error=lambda _operation: logger.warning(
                    "ThinkingConfig unavailable in google-genai SDK; structured reasoning controls ignored"
                ),
                on_compat_error=lambda _operation, _exc: logger.warning(
                    "ThinkingConfig unavailable in google-genai SDK; structured reasoning controls ignored"
                ),
            )
            if thinking_config is not None:
                config_kwargs["thinking_config"] = thinking_config
        if _normalize_service_tier(service_tier) and _native_generate_config_supports(
            "service_tier"
        ):
            config_kwargs["service_tier"] = _normalize_service_tier(service_tier)
        config = _build_native_generate_config(
            config_kwargs, operation="generate_native_structured"
        )
        for attempt in range(_MAX_RETRIES):
            attempt_timeout_s = _retry_timeout_s(normalized_latency_budget_ms, attempt)
            try:
                resp = await _run_sync_with_timeout(
                    attempt_timeout_s,
                    self._client.models.generate_content,
                    model=self.model,
                    contents=prompt,
                    config=config,
                )
                text, native_payload = _normalize_structured_json_response(resp, "Gemini")
                if native_payload:
                    _log_budget_event(
                        "gemini_structured_native_payload_used",
                        **log_context,
                        attempt=attempt + 1,
                        attempt_timeout_s=attempt_timeout_s,
                        result_bytes=len(text),
                    )
                logger.info(
                    "gemini_structured_success | %s",
                    json.dumps(
                        {
                            **log_context,
                            "attempt": attempt + 1,
                            "attempt_timeout_s": attempt_timeout_s,
                            "result_bytes": len(text),
                        },
                        sort_keys=True,
                        default=str,
                    ),
                )
                return text
            except asyncio.TimeoutError as exc:
                logger.warning(
                    "gemini_structured_timeout | %s",
                    json.dumps(
                        {
                            **log_context,
                            "attempt": attempt + 1,
                            "attempt_timeout_s": attempt_timeout_s,
                            "error": str(exc) or "asyncio.TimeoutError",
                        },
                        sort_keys=True,
                        default=str,
                    ),
                )
                if attempt == _MAX_RETRIES - 1:
                    raise
            except Exception as exc:
                logger.warning(
                    "gemini_structured_failed | %s",
                    json.dumps(
                        {
                            **log_context,
                            "attempt": attempt + 1,
                            "attempt_timeout_s": attempt_timeout_s,
                            "error": str(exc),
                        },
                        sort_keys=True,
                        default=str,
                    ),
                )
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
        service_tier: str | None,
        latency_budget_ms: int | None = None,
        thinking_budget: int | None = None,
        trace_id: str | None = None,
    ) -> BaseModel:
        normalized_latency_budget_ms = _normalize_latency_budget_ms(
            latency_budget_ms, structured=True
        )
        reasoning_controls = _resolve_reasoning_controls(
            self.model,
            thinking_budget,
            latency_budget_ms=normalized_latency_budget_ms,
            structured=True,
        )
        resolved_thinking_budget = reasoning_controls["thinking_budget"]
        thinking_level = reasoning_controls["thinking_level"]
        log_context = {
            "trace_id": str(trace_id or "").strip(),
            "model": self.model,
            "latency_budget_ms": normalized_latency_budget_ms,
            "service_tier": _normalize_service_tier(service_tier),
            "thinking_budget": resolved_thinking_budget,
            "thinking_level": thinking_level,
            "transport": "native_sdk",
            "startup_age_ms": _service_uptime_ms(),
            "cold_start_suspected": _is_cold_start_window(),
        }
        _log_budget_event("gemini_native_structured_start", **log_context)
        prepared_schema = _prepare_schema_for_provider(
            response_model.model_json_schema()
        )
        config_kwargs: dict[str, Any] = dict(
            response_mime_type="application/json",
            response_json_schema=prepared_schema,
            temperature=temperature,
            max_output_tokens=max_tokens,
        )
        if (resolved_thinking_budget is not None or thinking_level is not None) and (
            _supports_thinking_budget(self.model)
            or _supports_thinking_level(self.model)
        ):
            thinking_kwargs: dict[str, Any] = {}
            if resolved_thinking_budget is not None:
                thinking_kwargs["thinking_budget"] = resolved_thinking_budget
            if thinking_level is not None:
                thinking_kwargs["thinking_level"] = thinking_level
            thinking_config = build_optional_thinking_config(
                thinking_kwargs,
                operation="generate_native_structured",
                on_import_error=lambda _operation: logger.warning(
                    "ThinkingConfig unavailable in google-genai SDK; native structured reasoning controls ignored"
                ),
                on_compat_error=lambda _operation, _exc: logger.warning(
                    "ThinkingConfig unavailable in google-genai SDK; native structured reasoning controls ignored"
                ),
            )
            if thinking_config is not None:
                config_kwargs["thinking_config"] = thinking_config
        if _normalize_service_tier(service_tier) and _native_generate_config_supports(
            "service_tier"
        ):
            config_kwargs["service_tier"] = _normalize_service_tier(service_tier)
        config = _build_native_generate_config(
            config_kwargs, operation="generate_text_native"
        )
        for attempt in range(_MAX_RETRIES):
            attempt_timeout_s = _retry_timeout_s(normalized_latency_budget_ms, attempt)
            attempt_log = {
                **log_context,
                "attempt": attempt + 1,
                "attempt_timeout_s": attempt_timeout_s,
            }
            try:
                resp = await _run_sync_with_timeout(
                    attempt_timeout_s,
                    self._client.models.generate_content,
                    model=self.model,
                    contents=prompt,
                    config=config,
                )
                normalized_text, native_payload = _normalize_structured_json_response(
                    resp, "Gemini"
                )
                if native_payload:
                    _log_budget_event(
                        "gemini_native_structured_payload_used",
                        **attempt_log,
                        result_bytes=len(normalized_text),
                    )
                parsed = response_model.model_validate_json(normalized_text)
                _log_budget_event(
                    "gemini_native_structured_success",
                    **attempt_log,
                    result_bytes=len(normalized_text),
                )
                return parsed
            except asyncio.TimeoutError:
                _log_budget_event("gemini_native_structured_timeout", **attempt_log)
                if attempt == _MAX_RETRIES - 1:
                    raise
            except Exception as exc:
                _log_budget_event(
                    "gemini_native_structured_failed",
                    **attempt_log,
                    error=str(exc),
                )
                if attempt == _MAX_RETRIES - 1:
                    raise
            await asyncio.sleep(_jitter(_BASE_BACKOFF_S * (2**attempt)))

        raise RuntimeError(
            "generate_json exhausted all retries"
        )  # unreachable but satisfies type checker

    async def _generate_text_native(
        self,
        prompt: str,
        temperature: float,
        max_tokens: int,
        timeout_s: float,
        service_tier: str | None,
        thinking_budget: int | None = None,
        latency_budget_ms: int | None = None,
        retry_profile: str | None = None,
        request_class: str | None = None,
        trace_id: str | None = None,
    ) -> str:
        normalized_latency_budget_ms = _normalize_latency_budget_ms(
            latency_budget_ms, structured=False
        )
        reasoning_controls = _resolve_reasoning_controls(
            self.model,
            thinking_budget,
            latency_budget_ms=normalized_latency_budget_ms,
            structured=False,
        )
        runtime_diagnostics = get_gemini_runtime_diagnostics(self)
        config_kwargs: dict[str, Any] = dict(
            temperature=temperature, max_output_tokens=max_tokens
        )
        if _normalize_service_tier(service_tier) and _native_generate_config_supports(
            "service_tier"
        ):
            config_kwargs["service_tier"] = _normalize_service_tier(service_tier)
        if (
            reasoning_controls["thinking_budget"] is not None
            or reasoning_controls["thinking_level"] is not None
        ) and (
            _supports_thinking_budget(self.model)
            or _supports_thinking_level(self.model)
        ):
            thinking_kwargs: dict[str, Any] = {}
            if reasoning_controls["thinking_budget"] is not None:
                thinking_kwargs["thinking_budget"] = reasoning_controls[
                    "thinking_budget"
                ]
            if reasoning_controls["thinking_level"] is not None:
                thinking_kwargs["thinking_level"] = reasoning_controls[
                    "thinking_level"
                ]
            thinking_config = build_optional_thinking_config(
                thinking_kwargs,
                operation="generate_text_native",
                on_import_error=lambda _operation: logger.warning(
                    "ThinkingConfig unavailable in google-genai SDK; text reasoning controls ignored"
                ),
                on_compat_error=lambda _operation, _exc: logger.warning(
                    "ThinkingConfig unavailable in google-genai SDK; text reasoning controls ignored"
                ),
            )
            if thinking_config is not None:
                config_kwargs["thinking_config"] = thinking_config
        config = _build_native_generate_config(
            config_kwargs, operation="generate_stream"
        )
        for attempt in range(_MAX_RETRIES):
            attempt_timeout_s = _retry_timeout_s(normalized_latency_budget_ms, attempt)
            try:
                resp = await _run_sync_with_timeout(
                    attempt_timeout_s,
                    self._client.models.generate_content,
                    model=self.model,
                    contents=prompt,
                    config=config,
                )
                return _require_non_empty_text(resp.text, "Gemini")
            except Exception as exc:
                if attempt == _MAX_RETRIES - 1:
                    raise
                logger.warning(
                    "gemini_text_failed | %s",
                    json.dumps(
                        {
                            "attempt": attempt + 1,
                            "attempt_timeout_s": attempt_timeout_s,
                            "budget_ms": normalized_latency_budget_ms,
                            "startup_age_ms": _service_uptime_ms(),
                            "cold_start_suspected": _is_cold_start_window(),
                            "runtime_mode": runtime_diagnostics["mode"],
                            "credential_source": runtime_diagnostics["source"],
                            "service_tier": _normalize_service_tier(service_tier),
                            "thinking_budget": reasoning_controls["thinking_budget"],
                            "thinking_level": reasoning_controls["thinking_level"],
                            "retry_profile": retry_profile,
                            "request_class": request_class,
                            "trace_id": str(trace_id or "").strip(),
                            "error": str(exc),
                        },
                        sort_keys=True,
                        default=str,
                    ),
                )
                await asyncio.sleep(_jitter(_BASE_BACKOFF_S * (2**attempt)))
        return ""

    async def _generate_via_vertex_proxy(
        self,
        prompt: str,
        response_model: Type[BaseModel],
        temperature: float,
        max_tokens: int,
        service_tier: str | None,
        latency_budget_ms: int | None = None,
        thinking_budget: int | None = None,
        trace_id: str | None = None,
    ) -> BaseModel:
        proxy_url = _vertex_proxy_url()
        if not proxy_url:
            raise RuntimeError(
                "No Gemini credentials configured and VERTEX_PROXY_URL is not set. "
                "Set GOOGLE_API_KEY, GOOGLE_CLOUD_PROJECT, or VERTEX_PROXY_URL."
            )
        import httpx  # type: ignore[import]

        normalized_latency_budget_ms = _normalize_latency_budget_ms(
            latency_budget_ms, structured=True
        )
        reasoning_controls = _resolve_reasoning_controls(
            self.model,
            thinking_budget,
            latency_budget_ms=normalized_latency_budget_ms,
            structured=True,
        )
        payload: dict[str, Any] = {
            "model": self.model,
            "prompt": prompt,
            "temperature": temperature,
            "max_tokens": max_tokens,
            "response_schema": _prepare_schema_for_provider(
                response_model.model_json_schema()
            ),
        }
        if _normalize_service_tier(service_tier):
            payload["service_tier"] = _normalize_service_tier(service_tier)
        if reasoning_controls["thinking_budget"] is not None:
            payload["thinking_budget"] = reasoning_controls["thinking_budget"]
        if reasoning_controls["thinking_level"] is not None:
            payload["thinking_level"] = reasoning_controls["thinking_level"]
        log_context = {
            "trace_id": str(trace_id or "").strip(),
            "model": self.model,
            "latency_budget_ms": normalized_latency_budget_ms,
            "service_tier": _normalize_service_tier(service_tier),
            "thinking_budget": reasoning_controls["thinking_budget"],
            "thinking_level": reasoning_controls["thinking_level"],
            "transport": "vertex_proxy",
            "startup_age_ms": _service_uptime_ms(),
            "cold_start_suspected": _is_cold_start_window(),
        }
        _log_budget_event("gemini_proxy_structured_start", **log_context)
        for attempt in range(_MAX_RETRIES):
            attempt_timeout_s = _retry_timeout_s(normalized_latency_budget_ms, attempt)
            attempt_log = {
                **log_context,
                "attempt": attempt + 1,
                "attempt_timeout_s": attempt_timeout_s,
            }
            try:
                timeout = httpx.Timeout(
                    timeout=attempt_timeout_s,
                    connect=min(10.0, attempt_timeout_s),
                )
                async with httpx.AsyncClient(timeout=timeout) as client:
                    r = await client.post(f"{proxy_url}/generate", json=payload)
                    r.raise_for_status()
                    parsed = response_model.model_validate(r.json())
                    _log_budget_event("gemini_proxy_structured_success", **attempt_log)
                    return parsed
            except Exception as exc:
                is_last = attempt == _MAX_RETRIES - 1
                _log_budget_event(
                    "gemini_proxy_structured_failed",
                    **attempt_log,
                    error=str(exc),
                    final=is_last,
                )
                if is_last:
                    raise
                await asyncio.sleep(_jitter(_BASE_BACKOFF_S * (2**attempt)))
        raise RuntimeError("Vertex proxy exhausted all retries")

    async def _generate_text_proxy(
        self,
        prompt: str,
        temperature: float,
        max_tokens: int,
        service_tier: str | None,
        thinking_budget: int | None = None,
        latency_budget_ms: int | None = None,
        retry_profile: str | None = None,
        request_class: str | None = None,
        trace_id: str | None = None,
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
        if _normalize_service_tier(service_tier):
            payload["service_tier"] = _normalize_service_tier(service_tier)
        reasoning_controls = _resolve_reasoning_controls(
            self.model,
            thinking_budget,
            latency_budget_ms=_normalize_latency_budget_ms(
                latency_budget_ms, structured=False
            ),
            structured=False,
        )
        if reasoning_controls["thinking_budget"] is not None:
            payload["thinking_budget"] = reasoning_controls["thinking_budget"]
        if reasoning_controls["thinking_level"] is not None:
            payload["thinking_level"] = reasoning_controls["thinking_level"]
        normalized_latency_budget_ms = _normalize_latency_budget_ms(
            latency_budget_ms, structured=False
        )
        for attempt in range(_MAX_RETRIES):
            attempt_timeout_s = _retry_timeout_s(normalized_latency_budget_ms, attempt)
            try:
                timeout = httpx.Timeout(
                    timeout=attempt_timeout_s,
                    connect=min(10.0, attempt_timeout_s),
                )
                async with httpx.AsyncClient(timeout=timeout) as client:
                    r = await client.post(f"{proxy_url}/generate-text", json=payload)
                    r.raise_for_status()
                    return _require_non_empty_text(
                        r.json().get("text", ""), "Vertex proxy"
                    )
            except Exception as exc:
                if attempt == _MAX_RETRIES - 1:
                    raise
                logger.warning(
                    "vertex_text_proxy_failed | %s",
                    json.dumps(
                        {
                            "attempt": attempt + 1,
                            "attempt_timeout_s": attempt_timeout_s,
                            "budget_ms": normalized_latency_budget_ms,
                            "service_tier": _normalize_service_tier(service_tier),
                            "thinking_budget": reasoning_controls["thinking_budget"],
                            "thinking_level": reasoning_controls["thinking_level"],
                            "retry_profile": retry_profile,
                            "request_class": request_class,
                            "trace_id": str(trace_id or "").strip(),
                            "error": str(exc),
                        },
                        sort_keys=True,
                        default=str,
                    ),
                )
                await asyncio.sleep(_jitter(_BASE_BACKOFF_S * (2**attempt)))
        raise RuntimeError("Vertex text proxy exhausted all retries")

    async def _embed_proxy(
        self,
        texts: list[str],
        model: str,
        task_type: str,
        latency_budget_ms: int | None = None,
    ) -> list[list[float]]:
        """Proxy path for embeddings when the native SDK is unavailable.

        Calls ``{VERTEX_PROXY_URL}/embed`` (the same Cloud Function used for
        text generation).  Falls back to returning empty vectors if the proxy
        URL is not configured so callers can degrade gracefully.
        """
        proxy_url = _vertex_proxy_url()
        if not proxy_url:
            raise RuntimeError(
                "Embeddings require either the native google-genai SDK "
                "(GOOGLE_CLOUD_PROJECT or GOOGLE_API_KEY) or VERTEX_PROXY_URL "
                "pointing to a Cloud Function that handles /embed."
            )
        import httpx  # type: ignore[import]

        normalized_latency_budget_ms = _normalize_latency_budget_ms(
            latency_budget_ms, structured=False
        )
        # Use a single attempt for embeddings — no per-token generation to retry.
        timeout_s = _retry_timeout_s(normalized_latency_budget_ms, 0)
        payload = {
            "texts": texts,
            "model": model,
            "task_type": task_type,
        }
        log_context = {
            "model": model,
            "latency_budget_ms": normalized_latency_budget_ms,
            "timeout_s": timeout_s,
            "count": len(texts),
            "transport": "vertex_proxy",
            "startup_age_ms": _service_uptime_ms(),
            "cold_start_suspected": _is_cold_start_window(),
        }
        _log_budget_event("gemini_embed_proxy_start", **log_context)
        try:
            timeout = httpx.Timeout(
                timeout=timeout_s,
                connect=min(10.0, timeout_s),
            )
            async with httpx.AsyncClient(timeout=timeout) as client:
                r = await client.post(f"{proxy_url}/embed", json=payload)
                r.raise_for_status()
                data = r.json()
                embeddings = data.get("embeddings", [])
                if not embeddings and texts:
                    raise RuntimeError(
                        f"Vertex proxy /embed returned no embeddings for {len(texts)} input(s)"
                    )
                _log_budget_event(
                    "gemini_embed_proxy_success",
                    **log_context,
                    result_count=len(embeddings),
                )
                return embeddings
        except Exception as exc:
            _log_budget_event(
                "gemini_embed_proxy_failed", **log_context, error=str(exc)
            )
            raise

    async def embed(
        self,
        text: str,
        model: str = GEMINI_EMBED_FALLBACK_MODEL,
        task_type: str = "RETRIEVAL_QUERY",
        latency_budget_ms: int | None = None,
    ) -> list[float]:
        """Generate a single embedding vector.

        Uses the native SDK when available (preferred — schema-enforced, Vertex AI
        usage accounting). Falls back to the Cloud Function proxy when ``_client`` is None
        (e.g. when ``GEMINI_RUNTIME_MODE=vertex_proxy``).
        """
        if self._client is None:
            results = await self._embed_proxy(
                [text], model, task_type, latency_budget_ms=latency_budget_ms
            )
            return results[0] if results else []

        config = _build_native_embed_config(
            {"task_type": task_type},
            operation="embed",
        )
        resolved_model = _resolve_embedding_model(model)
        normalized_latency_budget_ms = _normalize_latency_budget_ms(
            latency_budget_ms, structured=False
        )
        timeout_s = _retry_timeout_s(normalized_latency_budget_ms, 0)

        resp = await _run_sync_with_timeout(
            timeout_s,
            self._client.models.embed_content,
            model=resolved_model,
            contents=text,
            config=config,
        )
        if not resp.embeddings:
            return []
        return resp.embeddings[0].values

    async def embed_batch(
        self,
        texts: list[str],
        model: str = GEMINI_EMBED_FALLBACK_MODEL,
        task_type: str = "RETRIEVAL_DOCUMENT",
        latency_budget_ms: int | None = None,
    ) -> list[list[float]]:
        """Generate multiple embedding vectors in one call.

        Uses the native SDK when available. Falls back to the Cloud Function
        proxy when ``_client`` is None.
        """
        if not texts:
            return []
        if self._client is None:
            return await self._embed_proxy(
                texts, model, task_type, latency_budget_ms=latency_budget_ms
            )

        config = _build_native_embed_config(
            {"task_type": task_type},
            operation="embed_batch",
        )
        resolved_model = _resolve_embedding_model(model)
        normalized_latency_budget_ms = _normalize_latency_budget_ms(
            latency_budget_ms, structured=False
        )
        timeout_s = _retry_timeout_s(normalized_latency_budget_ms, 0)

        resp = await _run_sync_with_timeout(
            timeout_s,
            self._client.models.embed_content,
            model=resolved_model,
            contents=texts,
            config=config,
        )
        return [e.values for e in resp.embeddings]

    # ------------------------------------------------------------------
    # Diverse hypothesis generation
    # ------------------------------------------------------------------

    async def generate_diverse_hypotheses(
        self,
        query: str,
        n: int = 3,
        min_cosine_distance: float = 0.3,
        temperature: float = 0.8,
        service_tier: str | None = None,
    ) -> list[str]:
        """Generate n research hypotheses for *query* with a diversity filter.

        Strategy:
        1. Request a larger candidate pool (2× requested count) from the LLM
           with elevated temperature to boost lexical variety.
        2. If embeddings are available, apply a greedy maximum-margin selection:
           each accepted candidate must have cosine distance ≥ ``min_cosine_distance``
           from every already-accepted hypothesis.
        3. Fall back to simple deduplication when embeddings are unavailable.

        Returns at most ``n`` hypotheses, possibly fewer if the pool after
        deduplication is smaller.
        """
        from pydantic import BaseModel as _BaseModel

        class _HypothesisPool(_BaseModel):
            hypotheses: list[str]

        pool_size = max(n * 2, 6)
        prompt = (
            f"Generate {pool_size} distinct, falsifiable research hypotheses for the "
            f"following academic query. Each hypothesis must take a different theoretical "
            f"angle — do not repeat the same claim in different words.\n\n"
            f"Query: {query}\n\n"
            f"Use the provided structured response schema exactly. Populate the "
            f"hypotheses field with {pool_size} strings."
        )

        try:
            result = await self.generate_json(
                prompt,
                _HypothesisPool,
                temperature=temperature,
                service_tier=service_tier,
            )
            candidates: list[str] = result.hypotheses or []
        except Exception as exc:
            logger.warning("generate_diverse_hypotheses: LLM call failed: %s", exc)
            return []

        if not candidates:
            return []

        # ── Embedding-based diversity filter ──────────────────────────────────
        try:
            vectors = await self.embed_batch(
                candidates,
                task_type="RETRIEVAL_DOCUMENT",
            )
            selected_indices: list[int] = []
            selected_vecs: list[list[float]] = []
            for i, vec in enumerate(vectors):
                if len(selected_indices) >= n:
                    break
                too_similar = False
                for sv in selected_vecs:
                    if _cosine_similarity(vec, sv) > (1.0 - min_cosine_distance):
                        too_similar = True
                        break
                if not too_similar:
                    selected_indices.append(i)
                    selected_vecs.append(vec)
            return [candidates[i] for i in selected_indices]
        except Exception as exc:
            logger.info(
                "generate_diverse_hypotheses: embedding unavailable, falling back to "
                "dedup filter: %s",
                exc,
            )

        # ── Fallback: token-level deduplication ───────────────────────────────
        accepted: list[str] = []
        for cand in candidates:
            if len(accepted) >= n:
                break
            cand_tokens = _token_set(cand)
            duplicate = any(
                len(cand_tokens & _token_set(a))
                / max(len(cand_tokens | _token_set(a)), 1)
                > (1.0 - min_cosine_distance)
                for a in accepted
            )
            if not duplicate:
                accepted.append(cand)
        return accepted


gemini_service = GeminiService()
_runtime_service_cache = gemini_service
_runtime_service_cache_key = _runtime_service_key()


def _get_runtime_service() -> GeminiService:
    global _runtime_service_cache
    global _runtime_service_cache_key

    cache_key = _runtime_service_key()
    with _RUNTIME_SERVICE_LOCK:
        if _runtime_service_cache is None or _runtime_service_cache_key != cache_key:
            logger.info(
                "Refreshing Gemini runtime service cache: mode=%s source=%s project_configured=%s proxy_configured=%s",
                _gemini_runtime_mode(),
                _gemini_runtime_source(),
                bool(_vertex_project()),
                bool(_vertex_proxy_url()),
            )
            _runtime_service_cache = GeminiService()
            _runtime_service_cache_key = cache_key
        return _runtime_service_cache
