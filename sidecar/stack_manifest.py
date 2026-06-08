from __future__ import annotations

import os
import copy
from typing import Any

from stack_contract import ENDPOINTS_DEFAULT_OVERLAY, ENDPOINTS_MANIFEST, ENDPOINTS_MANIFEST_VERSION


def current_overlay_name() -> str:
    requested = str(os.environ.get("ENDPOINTS_MANIFEST_ENV", ENDPOINTS_DEFAULT_OVERLAY) or "").strip()
    if requested and requested in ENDPOINTS_MANIFEST.get("overlays", {}):
        return requested
    return ENDPOINTS_DEFAULT_OVERLAY


def current_overlay() -> dict[str, Any]:
    overlays = ENDPOINTS_MANIFEST.get("overlays", {})
    return copy.deepcopy(
        overlays.get(
            current_overlay_name(),
            overlays.get(ENDPOINTS_DEFAULT_OVERLAY, {}),
        )
    )


def resolve_env(name: str) -> str:
    explicit = str(os.environ.get(name, "") or "").strip()
    if explicit:
        return explicit
    overlay_env = current_overlay().get("env", {})
    return str(overlay_env.get(name, "") or "").strip()


def resolve_service_base_url(service_id: str) -> str:
    overlay_urls = current_overlay().get("serviceBaseUrls", {})
    overlay_url = str(overlay_urls.get(service_id, "") or "").rstrip("/")
    if overlay_url:
        return overlay_url

    service = ENDPOINTS_MANIFEST.get("services", {}).get(service_id, {})
    return str(service.get("defaultBaseUrl", "") or "").rstrip("/")


def resolve_listen_port(service_id: str, port_name: str) -> int:
    if port_name == "http":
        explicit = str(os.environ.get("PORT", "") or "").strip()
        if explicit.isdigit():
            return int(explicit)

    service = ENDPOINTS_MANIFEST.get("services", {}).get(service_id, {})
    listen_ports = service.get("listenPorts", {})
    return int(listen_ports.get(port_name, 0) or 0)


def validate_service(service_id: str) -> None:
    if int(ENDPOINTS_MANIFEST.get("version", 0) or 0) != int(ENDPOINTS_MANIFEST_VERSION):
        raise RuntimeError("generated stack manifest version mismatch")

    services = ENDPOINTS_MANIFEST.get("services", {})
    service = services.get(service_id)
    if not service:
        raise RuntimeError(f"unknown service id: {service_id}")

    for env_key in service.get("requiredEnv", []):
        if not resolve_env(str(env_key)):
            raise RuntimeError(f"missing required manifest env {env_key} for {service_id}")
