from __future__ import annotations

import configparser
import os
from pathlib import Path
import sys
import threading
import time
from contextlib import contextmanager
from dataclasses import dataclass
from typing import Iterable

import structlog

logger = structlog.get_logger(__name__)

_SECRET_TTL_SECONDS = 300
_SECRET_MISS_TTL_SECONDS = 15
_SM_CLIENT_RETRY_SECONDS = 30
_PROJECT_TTL_SECONDS = 300
_PROJECT_MISS_TTL_SECONDS = 30
_cache_lock = threading.Lock()
_client_lock = threading.Lock()
_project_lock = threading.Lock()
_secret_cache: dict[str, "CachedSecret"] = {}
_sm_client = None
_sm_client_init_failed = False
_sm_client_retry_at = 0.0
_project_resolution: "CachedProjectResolution | None" = None


@dataclass
class CachedSecret:
    value: str
    expires_at: float


@dataclass
class CachedProjectResolution:
    value: str
    source: str
    expires_at: float


_PROJECT_ENV_KEYS = (
    "GOOGLE_CLOUD_PROJECT",
    "GCLOUD_PROJECT",
    "GCP_PROJECT_ID",
    "CLOUDSDK_CORE_PROJECT",
)
_LOCAL_PROTO_PARENT = Path(__file__).resolve().parents[1]
_LOCAL_PROTO_DIR = _LOCAL_PROTO_PARENT / "proto"


def _project_id_from_env() -> tuple[str, str]:
    for key in _PROJECT_ENV_KEYS:
        value = str(os.environ.get(key, "")).strip()
        if value:
            return value, f"env:{key}"
    return "", "none"


def _gcloud_config_dir() -> str:
    explicit = str(os.environ.get("CLOUDSDK_CONFIG", "")).strip()
    if explicit:
        return explicit

    appdata = str(os.environ.get("APPDATA", "")).strip()
    if appdata:
        return os.path.join(appdata, "gcloud")

    xdg_config_home = str(os.environ.get("XDG_CONFIG_HOME", "")).strip()
    if xdg_config_home:
        return os.path.join(xdg_config_home, "gcloud")

    return os.path.join(os.path.expanduser("~"), ".config", "gcloud")


def _project_id_from_gcloud_config() -> tuple[str, str]:
    config_dir = _gcloud_config_dir()
    active_config = "default"
    active_config_path = os.path.join(config_dir, "active_config")
    try:
        with open(active_config_path, "r", encoding="utf-8") as handle:
            active_name = handle.read().strip()
        if active_name:
            active_config = active_name
    except OSError:
        pass

    config_path = os.path.join(config_dir, "configurations", f"config_{active_config}")
    parser = configparser.ConfigParser()
    try:
        with open(config_path, "r", encoding="utf-8") as handle:
            parser.read_file(handle)
    except OSError:
        return "", "none"
    except configparser.Error:
        return "", "none"

    project_id = parser.get("core", "project", fallback="").strip()
    if project_id:
        return project_id, "gcloud_config"
    return "", "none"


def _project_id_from_adc() -> tuple[str, str]:
    try:
        import google.auth

        _credentials, project_id = google.auth.default(
            scopes=("https://www.googleapis.com/auth/cloud-platform",)
        )
    except Exception:
        return "", "none"

    normalized = str(project_id or "").strip()
    if normalized:
        return normalized, "adc"
    return "", "none"


def _resolve_project_id() -> tuple[str, str]:
    global _project_resolution

    now = time.time()
    env_project_id, env_source = _project_id_from_env()
    if env_project_id:
        with _project_lock:
            _project_resolution = CachedProjectResolution(
                value=env_project_id,
                source=env_source,
                expires_at=now + _PROJECT_TTL_SECONDS,
            )
        return env_project_id, env_source

    with _project_lock:
        if (
            _project_resolution
            and _project_resolution.expires_at > now
            and not _project_resolution.source.startswith("env:")
        ):
            return _project_resolution.value, _project_resolution.source

    for resolver in (_project_id_from_gcloud_config, _project_id_from_adc):
        project_id, source = resolver()
        if project_id:
            with _project_lock:
                _project_resolution = CachedProjectResolution(
                    value=project_id,
                    source=source,
                    expires_at=now + _PROJECT_TTL_SECONDS,
                )
            return project_id, source

    with _project_lock:
        _project_resolution = CachedProjectResolution(
            value="",
            source="none",
            expires_at=now + _PROJECT_MISS_TTL_SECONDS,
        )
    return "", "none"


def _project_id() -> str:
    project_id, _source = _resolve_project_id()
    return project_id


def _project_id_source() -> str:
    project_id_value, source = _resolve_project_id()
    _ = project_id_value
    return source


def get_project_id_resolution() -> dict[str, str | bool]:
    project_id_value, source = _resolve_project_id()
    return {
        "projectConfigured": bool(project_id_value),
        "projectId": project_id_value,
        "projectSource": source,
    }


def _skip_secret_manager() -> bool:
    flag = str(os.environ.get("SKIP_GCP_SECRET_MANAGER", "")).strip().lower()
    if flag in {"1", "true", "yes", "on"}:
        return True
    if os.environ.get("PYTEST_CURRENT_TEST"):
        return True
    return False


def _iter_secret_name_overrides(name: str, aliases: Iterable[str]) -> list[str]:
    candidates: list[str] = []
    for key in (name, *aliases):
        override = str(os.environ.get(f"{key}_SECRET_NAME", "")).strip()
        if override:
            candidates.append(override)
    return candidates


def _iter_secret_names(name: str, aliases: Iterable[str], explicit_secret_names: Iterable[str]) -> list[str]:
    ordered: list[str] = []
    seen: set[str] = set()

    for candidate in [*_iter_secret_name_overrides(name, aliases), *explicit_secret_names, name, *aliases]:
        normalized = str(candidate or "").strip()
        if normalized and normalized not in seen:
            seen.add(normalized)
            ordered.append(normalized)

    return ordered


def _is_local_proto_module(module: object) -> bool:
    module_path = str(getattr(module, "__file__", "") or "").strip()
    if not module_path:
        return False

    try:
        resolved = Path(module_path).resolve()
    except OSError:
        return False

    return resolved == _LOCAL_PROTO_DIR / "__init__.py" or _LOCAL_PROTO_DIR in resolved.parents


@contextmanager
def _without_local_proto_shadow():
    """Avoid importing the sidecar's local `proto` package as google's proto-plus."""
    original_sys_path = list(sys.path)
    original_proto_modules = {
        name: module
        for name, module in list(sys.modules.items())
        if (name == "proto" or name.startswith("proto."))
        and _is_local_proto_module(module)
    }

    try:
        sys.path = [
            entry
            for entry in original_sys_path
            if str(entry or "").strip()
            and Path(entry).resolve() != _LOCAL_PROTO_PARENT
        ]
        for name in list(sys.modules):
            if name in original_proto_modules:
                sys.modules.pop(name, None)
        yield
    finally:
        for name in list(sys.modules):
            if (name == "proto" or name.startswith("proto.")) and name not in original_proto_modules:
                sys.modules.pop(name, None)
        sys.modules.update(original_proto_modules)
        sys.path = original_sys_path


def _cache_get(cache_key: str) -> str | None:
    with _cache_lock:
        entry = _secret_cache.get(cache_key)
        if entry and entry.expires_at > time.time():
            return entry.value
        if entry:
            _secret_cache.pop(cache_key, None)
    return None


def _cache_put(cache_key: str, value: str, ttl_seconds: float) -> None:
    with _cache_lock:
        _secret_cache[cache_key] = CachedSecret(value=value, expires_at=time.time() + ttl_seconds)


def _secret_resource_path(project_id: str, secret_name: str) -> str:
    if secret_name.startswith("projects/"):
        return secret_name
    return f"projects/{project_id}/secrets/{secret_name}/versions/latest"


def _get_sm_client():
    global _sm_client, _sm_client_init_failed, _sm_client_retry_at
    if _sm_client is not None:
        return _sm_client
    if _sm_client_init_failed and time.time() < _sm_client_retry_at:
        return None

    with _client_lock:
        if _sm_client is not None:
            return _sm_client
        if _sm_client_init_failed and time.time() < _sm_client_retry_at:
            return None
        try:
            with _without_local_proto_shadow():
                from google.cloud import secretmanager

                _sm_client = secretmanager.SecretManagerServiceClient()
            _sm_client_init_failed = False
            _sm_client_retry_at = 0.0
            return _sm_client
        except Exception as exc:  # pragma: no cover - import/ADC failures are environment-specific
            _sm_client_init_failed = True
            _sm_client_retry_at = time.time() + _SM_CLIENT_RETRY_SECONDS
            logger.warning("secret_manager_client_init_failed", error=str(exc))
            return None


def _fetch_secret_from_manager(secret_name: str) -> str:
    project_id = _project_id()
    if not project_id or _skip_secret_manager():
        return ""

    cache_key = f"{project_id}:{secret_name}"
    cached = _cache_get(cache_key)
    if cached is not None:
        return cached

    client = _get_sm_client()
    if client is None:
        _cache_put(cache_key, "", _SECRET_MISS_TTL_SECONDS)
        return ""

    secret_path = _secret_resource_path(project_id, secret_name)
    try:
        response = client.access_secret_version(request={"name": secret_path})
        value = response.payload.data.decode("utf-8").strip()
        _cache_put(cache_key, value, _SECRET_TTL_SECONDS)
        return value
    except Exception as exc:  # pragma: no cover - external service failure
        message = str(exc)
        if "404" not in message and "not found" not in message.lower():
            logger.warning("secret_manager_access_failed", secret=secret_name, error=message)
        _cache_put(cache_key, "", _SECRET_MISS_TTL_SECONDS)
        return ""


def get_secret(name: str, *, aliases: Iterable[str] = (), secret_names: Iterable[str] = ()) -> str:
    for key in (name, *aliases):
        value = str(os.environ.get(key, "")).strip()
        if value:
            return value

    for secret_name in _iter_secret_names(name, aliases, secret_names):
        value = _fetch_secret_from_manager(secret_name)
        if value:
            return value

    return ""


def _resolve_google_api_key(*, prefer_secret_manager: bool) -> tuple[str, str]:
    env_candidates = (
        ("GOOGLE_API_KEY", str(os.environ.get("GOOGLE_API_KEY", "")).strip()),
        ("GEMINI_API_KEY", str(os.environ.get("GEMINI_API_KEY", "")).strip()),
    )
    secret_candidates = _iter_secret_names(
        "GOOGLE_API_KEY",
        ("GEMINI_API_KEY",),
        ("GOOGLE_API_KEY", "GEMINI_API_KEY"),
    )

    if prefer_secret_manager and not _skip_secret_manager() and _project_id():
        for secret_name in secret_candidates:
            value = _fetch_secret_from_manager(secret_name)
            if value:
                return value, "secret_manager"

    for key, value in env_candidates:
        if value:
            return value, "env"

    if not prefer_secret_manager:
        for secret_name in secret_candidates:
            value = _fetch_secret_from_manager(secret_name)
            if value:
                return value, "secret_manager"

    return "", "none"


def get_google_api_key() -> str:
    value, _source = _resolve_google_api_key(prefer_secret_manager=True)
    return value


def get_google_api_key_resolution() -> dict[str, str | bool]:
    project_id = _project_id()
    project_source = _project_id_source()
    secret_manager_enabled = not _skip_secret_manager()

    value, source = _resolve_google_api_key(prefer_secret_manager=secret_manager_enabled)
    if value:
        return {
            "status": "ok",
            "source": source,
            "secretManagerEnabled": secret_manager_enabled,
            "projectConfigured": bool(project_id),
            "projectSource": project_source,
        }

    if not secret_manager_enabled:
        return {
            "status": "skipped",
            "source": "none",
            "secretManagerEnabled": False,
            "projectConfigured": bool(project_id),
            "projectSource": project_source,
        }

    return {
        "status": "missing",
        "source": "none",
        "secretManagerEnabled": secret_manager_enabled,
        "projectConfigured": bool(project_id),
        "projectSource": project_source,
    }


def prime_google_api_key() -> dict[str, str | bool]:
    resolution = get_google_api_key_resolution()
    if resolution["status"] == "ok" and resolution["source"] == "secret_manager":
        get_google_api_key()
    return resolution


def clear_secret_cache() -> None:
    global _project_resolution
    with _cache_lock:
        _secret_cache.clear()
    with _project_lock:
        _project_resolution = None
