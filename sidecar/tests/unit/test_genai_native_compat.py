from __future__ import annotations

import sys
import types

from services import genai_native_compat as compat


def _install_google_genai_modules(monkeypatch, *, genai_types: types.ModuleType) -> None:
    genai_module = types.ModuleType("google.genai")
    setattr(genai_module, "types", genai_types)

    google_module = types.ModuleType("google")
    setattr(google_module, "genai", genai_module)

    monkeypatch.setitem(sys.modules, "google", google_module)
    monkeypatch.setitem(sys.modules, "google.genai", genai_module)
    monkeypatch.setitem(sys.modules, "google.genai.types", genai_types)


def test_build_generate_content_config_drops_optional_fields(monkeypatch):
    genai_types = types.ModuleType("google.genai.types")

    class StrictGenerateContentConfig:
        def __init__(self, **kwargs):
            if "service_tier" in kwargs:
                raise TypeError("unexpected keyword argument 'service_tier'")
            self.kwargs = kwargs

    genai_types.GenerateContentConfig = StrictGenerateContentConfig
    _install_google_genai_modules(monkeypatch, genai_types=genai_types)

    dropped: list[tuple[str, list[str]]] = []
    config = compat.build_generate_content_config(
        {"temperature": 0.3, "service_tier": "standard"},
        optional_fields={"service_tier"},
        operation="generate_text",
        on_dropped_fields=lambda operation, fields: dropped.append((operation, fields)),
    )

    assert config.kwargs == {"temperature": 0.3}
    assert dropped == [("generate_text", ["service_tier"])]


def test_build_embed_content_config_drops_optional_fields(monkeypatch):
    genai_types = types.ModuleType("google.genai.types")

    class StrictEmbedContentConfig:
        def __init__(self, **kwargs):
            if "task_type" in kwargs:
                raise TypeError("unexpected keyword argument 'task_type'")
            self.kwargs = kwargs

    genai_types.EmbedContentConfig = StrictEmbedContentConfig
    _install_google_genai_modules(monkeypatch, genai_types=genai_types)

    dropped: list[tuple[str, list[str]]] = []
    config = compat.build_embed_content_config(
        {"task_type": "retrieval_document"},
        optional_fields={"task_type"},
        operation="embed",
        on_dropped_fields=lambda operation, fields: dropped.append((operation, fields)),
    )

    assert config.kwargs == {}
    assert dropped == [("embed", ["task_type"])]


def test_build_optional_thinking_config_reports_compat_error(monkeypatch):
    genai_types = types.ModuleType("google.genai.types")

    class StrictThinkingConfig:
        def __init__(self, **kwargs):
            raise TypeError("unexpected keyword argument 'thinking_level'")

    genai_types.ThinkingConfig = StrictThinkingConfig
    _install_google_genai_modules(monkeypatch, genai_types=genai_types)

    captured: list[tuple[str, str]] = []
    config = compat.build_optional_thinking_config(
        {"thinking_level": "high"},
        operation="generate_structured",
        on_compat_error=lambda operation, exc: captured.append((operation, str(exc))),
    )

    assert config is None
    assert captured == [
        ("generate_structured", "unexpected keyword argument 'thinking_level'")
    ]
