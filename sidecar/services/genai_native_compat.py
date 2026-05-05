from __future__ import annotations

from contextlib import suppress
from typing import Any, Callable, Iterable

from pydantic import ValidationError

_DEFAULT_API_VERSION = "v1"


def native_client_http_options(*, api_version: str = _DEFAULT_API_VERSION) -> Any | None:
    try:
        from google.genai import types as genai_types  # type: ignore[import]
    except ImportError:
        return None

    http_options_cls = getattr(genai_types, "HttpOptions", None)
    if http_options_cls is None:
        return None
    try:
        return http_options_cls(api_version=api_version)
    except Exception:
        return None


def _unsupported_config_fields(
    exc: Exception, *, optional_fields: Iterable[str]
) -> set[str]:
    unsupported: set[str] = set()
    if isinstance(exc, ValidationError):
        with suppress(Exception):
            for error in exc.errors():
                if str(error.get("type", "")).endswith("extra_forbidden"):
                    loc = error.get("loc") or ()
                    if loc:
                        unsupported.add(str(loc[0]))

    message = str(exc)
    for field_name in optional_fields:
        if (
            f"unexpected keyword argument '{field_name}'" in message
            or f"{field_name}\n  Extra inputs are not permitted" in message
        ):
            unsupported.add(field_name)

    return unsupported


def _build_compatible_config(
    config_cls: type,
    config_kwargs: dict[str, Any],
    *,
    optional_fields: Iterable[str],
    operation: str,
    on_dropped_fields: Callable[[str, list[str]], None] | None = None,
) -> Any:
    pending_kwargs = dict(config_kwargs)
    optional_field_set = frozenset(optional_fields)
    dropped_fields: list[str] = []
    while True:
        try:
            config = config_cls(**pending_kwargs)
            if dropped_fields and on_dropped_fields is not None:
                on_dropped_fields(operation, dropped_fields)
            return config
        except Exception as exc:
            unsupported = sorted(
                field_name
                for field_name in _unsupported_config_fields(
                    exc, optional_fields=optional_field_set
                )
                if field_name in pending_kwargs and field_name in optional_field_set
            )
            if not unsupported:
                raise
            for field_name in unsupported:
                pending_kwargs.pop(field_name, None)
                if field_name not in dropped_fields:
                    dropped_fields.append(field_name)


def build_generate_content_config(
    config_kwargs: dict[str, Any],
    *,
    optional_fields: Iterable[str],
    operation: str,
    on_dropped_fields: Callable[[str, list[str]], None] | None = None,
) -> Any:
    from google.genai.types import GenerateContentConfig  # type: ignore[import]

    return _build_compatible_config(
        GenerateContentConfig,
        config_kwargs,
        optional_fields=optional_fields,
        operation=operation,
        on_dropped_fields=on_dropped_fields,
    )


def build_embed_content_config(
    config_kwargs: dict[str, Any],
    *,
    optional_fields: Iterable[str],
    operation: str,
    on_dropped_fields: Callable[[str, list[str]], None] | None = None,
) -> Any:
    from google.genai.types import EmbedContentConfig  # type: ignore[import]

    return _build_compatible_config(
        EmbedContentConfig,
        config_kwargs,
        optional_fields=optional_fields,
        operation=operation,
        on_dropped_fields=on_dropped_fields,
    )


def build_optional_thinking_config(
    thinking_kwargs: dict[str, Any],
    *,
    operation: str,
    on_import_error: Callable[[str], None] | None = None,
    on_compat_error: Callable[[str, Exception], None] | None = None,
) -> Any | None:
    if not thinking_kwargs:
        return None
    try:
        from google.genai.types import ThinkingConfig  # type: ignore[import]
    except ImportError:
        if on_import_error is not None:
            on_import_error(operation)
        return None

    try:
        return ThinkingConfig(**thinking_kwargs)
    except Exception as exc:
        if on_compat_error is not None:
            on_compat_error(operation, exc)
        return None
