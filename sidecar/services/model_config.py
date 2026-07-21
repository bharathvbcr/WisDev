"""Shared model configuration loader for the Python sidecar."""

from __future__ import annotations

import json
import logging
import os
from functools import lru_cache
from pathlib import Path
from typing import Any

logger = logging.getLogger(__name__)

MODEL_CONFIG_FILE = "scholar_models.json"

_TIER_ENV_KEYS: dict[str, tuple[str, ...]] = {
    "light": ("AI_MODEL_LIGHT_ID", "GEMINI_LIGHT_MODEL"),
    "standard": (
        "AI_MODEL_STANDARD_ID",
        "AI_MODEL_BALANCED_ID",
        "GEMINI_STANDARD_MODEL",
    ),
    "heavy": ("AI_MODEL_HEAVY_ID", "GEMINI_HEAVY_MODEL"),
}
_EMBEDDING_ENV_KEYS: dict[str, tuple[str, ...]] = {
    "primary": ("EMBEDDING_MODEL_PRIMARY_ID",),
    "standard": ("EMBEDDING_MODEL_STANDARD_ID",),
    "fallback": ("EMBEDDING_MODEL_FALLBACK_ID",),
}
_LOCATION_ENV_KEYS: dict[str, tuple[str, ...]] = {
    "generative": ("GOOGLE_CLOUD_LOCATION", "GOOGLE_CLOUD_REGION"),
    "embedding": ("GOOGLE_CLOUD_EMBEDDING_LOCATION", "GOOGLE_CLOUD_EMBEDDING_REGION"),
}


def _dedupe_paths(paths: list[Path]) -> list[Path]:
    seen: set[str] = set()
    unique: list[Path] = []
    for path in paths:
        key = str(path)
        if key in seen:
            continue
        seen.add(key)
        unique.append(path)
    return unique


def _candidate_config_paths() -> list[Path]:
    candidates: list[Path] = []
    explicit = os.getenv("SCHOLAR_MODELS_CONFIG", "").strip()
    if explicit:
        candidates.append(Path(explicit))

    cwd = Path.cwd()
    candidates.extend(parent / MODEL_CONFIG_FILE for parent in (cwd, *cwd.parents))

    module_path = Path(__file__).resolve()
    candidates.extend(parent / MODEL_CONFIG_FILE for parent in module_path.parents)
    return _dedupe_paths(candidates)


def _coerce_model_config(raw: dict[str, Any]) -> dict[str, Any]:
    tiers = raw.get("tiers")
    if not isinstance(tiers, dict):
        tiers = {
            key: raw.get(key)
            for key in ("light", "standard", "heavy")
            if raw.get(key)
        }
    tiers = {
        key: str(tiers.get(key) or "").strip()
        for key in ("light", "standard", "heavy")
    }
    if not all(tiers.values()):
        raise ValueError("missing required tiers: light, standard, heavy")

    embeddings = raw.get("embeddings")
    if not isinstance(embeddings, dict):
        embeddings = {}
    embeddings = {
        key: str(embeddings.get(key) or "").strip()
        for key in ("primary", "standard", "fallback")
    }
    locations = raw.get("locations")
    if not isinstance(locations, dict):
        locations = {}
    locations = {
        key: str(locations.get(key) or "").strip()
        for key in ("generative", "embedding")
    }
    return {
        **raw,
        "tiers": tiers,
        "embeddings": embeddings,
        "locations": locations,
    }


@lru_cache(maxsize=1)
def load_model_config() -> dict[str, Any]:
    for path in _candidate_config_paths():
        if not path.is_file():
            continue
        try:
            with path.open("r", encoding="utf-8") as handle:
                raw = json.load(handle)
            if not isinstance(raw, dict):
                raise ValueError("model config must be a JSON object")
            config = _coerce_model_config(raw)
            logger.info("loaded scholar model config", extra={"path": str(path)})
            return config
        except Exception as exc:
            logger.warning(
                "failed to load scholar model config",
                extra={"path": str(path), "error": str(exc)},
            )
    logger.error("could not load scholar model config")
    return {"tiers": {}, "embeddings": {}}


def clear_model_config_cache() -> None:
    load_model_config.cache_clear()


def _first_env(env: dict[str, str] | None, keys: tuple[str, ...]) -> str:
    observed = env if env is not None else os.environ
    for key in keys:
        value = str(observed.get(key, "")).strip()
        if value:
            return value
    return ""


def normalize_generation_tier(tier: str | None) -> str:
    normalized = str(tier or "").strip().lower()
    if normalized == "balanced":
        return "standard"
    if normalized in {"light", "standard", "heavy"}:
        return normalized
    return "standard"


def get_model_tier(tier: str, default: str = "") -> str:
    normalized = normalize_generation_tier(tier)
    config = load_model_config()
    tiers = config.get("tiers") if isinstance(config, dict) else {}
    if isinstance(tiers, dict):
        value = str(tiers.get(normalized) or "").strip()
        if value:
            return value
    return default


def resolve_generation_model_id(
    tier: str | None,
    *,
    env: dict[str, str] | None = None,
    default: str = "",
) -> str:
    normalized = normalize_generation_tier(tier)
    env_model = _first_env(env, _TIER_ENV_KEYS.get(normalized, ()))
    if env_model:
        return env_model
    shared_default = _first_env(env, ("AI_MODEL_DEFAULT_ID",))
    if shared_default:
        return shared_default
    return get_model_tier(normalized, default)


def get_embedding_model(
    key: str,
    *,
    env: dict[str, str] | None = None,
    default: str = "",
) -> str:
    normalized = str(key or "").strip().lower() or "standard"
    env_model = _first_env(env, _EMBEDDING_ENV_KEYS.get(normalized, ()))
    if env_model:
        return env_model

    config = load_model_config()
    embeddings = config.get("embeddings") if isinstance(config, dict) else {}
    if isinstance(embeddings, dict):
        value = str(embeddings.get(normalized) or "").strip()
        if value:
            return value
    return default


def get_model_location(
    key: str,
    *,
    env: dict[str, str] | None = None,
    default: str = "",
) -> str:
    normalized = str(key or "").strip().lower() or "generative"
    env_location = _first_env(env, _LOCATION_ENV_KEYS.get(normalized, ()))
    if env_location:
        return env_location

    config = load_model_config()
    locations = config.get("locations") if isinstance(config, dict) else {}
    if isinstance(locations, dict):
        value = str(locations.get(normalized) or "").strip()
        if value:
            return value
    return default
