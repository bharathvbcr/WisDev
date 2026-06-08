"""Tests for services/__init__.py lazy-loader."""

import pytest
import sys


def test_import_ai_generation_class_via_package():
    import services
    cls = services.AiGenerationService
    from services.ai_generation_service import AiGenerationService
    assert cls is AiGenerationService


def test_import_semantic_cache_class_via_package():
    import services
    cls = services.SemanticCache
    from services.semantic_cache import SemanticCache
    assert cls is SemanticCache


def test_import_ai_generation_instance_via_submodule():
    # Access the instance through the submodule directly (the reliable path)
    from services.ai_generation_service import ai_generation_service
    from services.ai_generation_service import AiGenerationService
    assert isinstance(ai_generation_service, AiGenerationService)


def test_import_semantic_cache_instance_via_submodule():
    from services.semantic_cache import semantic_cache
    from services.semantic_cache import SemanticCache
    assert isinstance(semantic_cache, SemanticCache)


def test_unknown_attribute_raises_attribute_error():
    import services
    with pytest.raises(AttributeError):
        _ = services.NonExistentClass


def test_all_exports_listed():
    import services
    assert "AiGenerationService" in services.__all__
    assert "SemanticCache" in services.__all__
    assert "ai_generation_service" in services.__all__
    assert "semantic_cache" in services.__all__
