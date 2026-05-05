"""
conftest.py - Adds both the repo root and the sidecar package directory to
sys.path so tests can import using either form:
  from services.model_router import ModelRouter   (repo-root-relative)
  from services.ai_generation_service import AiGenerationService        (cloudrun/-relative)
regardless of which directory pytest is invoked from.
"""
import os
from unittest.mock import AsyncMock
# Set environment variables BEFORE importing anything that might read them
os.environ["EMBEDDING_PROVIDER"] = "azure_openai"
os.environ["AZURE_OPENAI_ENDPOINT"] = "https://example.openai.azure.com"
os.environ["AZURE_OPENAI_API_KEY"] = "test-key"
os.environ["AZURE_OPENAI_EMBEDDING_DEPLOYMENT"] = "embed-prod"
os.environ["EMBEDDING_OUTPUT_DIMENSION"] = "768"

import pathlib
import sys
import importlib

# Define paths correctly relative to this file (which is in sidecar/tests/)
_HERE = pathlib.Path(__file__).resolve().parent
_PACKAGE = _HERE.parent # sidecar
_REPO_ROOT = _PACKAGE.parent.parent # root

# Add the package root to sys.path so 'import services', 'import routers' work
if str(_PACKAGE) not in sys.path:
    sys.path.insert(0, str(_PACKAGE))

# Bind 'main' to the sidecar main module
if 'main' not in sys.modules:
    try:
        sys.path.insert(0, str(_PACKAGE))
        import main
        sys.modules['main'] = main
    except ImportError:
        pass

import pytest


@pytest.fixture(autouse=True)
def grpc_mocks(monkeypatch):
    import main

    monkeypatch.setattr(main, "_grpc_sidecar_ready", AsyncMock(return_value=(True, "")))
    monkeypatch.setattr(main, "_grpc_sidecar_health", AsyncMock(return_value=("ok", "")))
    monkeypatch.setattr(main, "_start_grpc_server", lambda: None)
    monkeypatch.setattr(main, "_wait_for_grpc_sidecar_ready", AsyncMock())
    yield

def pytest_configure(config):
    config.addinivalue_line("markers", "asyncio: mark test as async")
