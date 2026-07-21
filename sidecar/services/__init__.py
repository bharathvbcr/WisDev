"""
WisDev Services Package.

Keep package import side effects minimal so retained submodules can be imported
without eagerly loading unrelated optional dependencies.
"""

from __future__ import annotations

from importlib import import_module
from typing import Any

__all__ = [
    "AiGenerationService",
    "SemanticCache",
    "ai_generation_service",
    "semantic_cache",
]


def __getattr__(name: str) -> Any:
    if name in {"AiGenerationService", "ai_generation_service"}:
        module = import_module(".ai_generation_service", __name__)
        return getattr(module, name)
    if name in {"SemanticCache", "semantic_cache"}:
        module = import_module(".semantic_cache", __name__)
        return getattr(module, name)
    raise AttributeError(f"module {__name__!r} has no attribute {name!r}")
