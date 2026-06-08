"""
Unit test conftest — minimal setup that does NOT import `main` or any module
that requires grpc / opentelemetry. Overrides the parent conftest's autouse
grpc_mocks fixture so unit tests are isolated from infrastructure deps.
"""
import os
import pathlib
import sys

import pytest

# Set env vars before any imports that read them
os.environ.setdefault("EMBEDDING_PROVIDER", "azure_openai")
os.environ.setdefault("AZURE_OPENAI_ENDPOINT", "https://example.openai.azure.com")
os.environ.setdefault("AZURE_OPENAI_API_KEY", "test-key")
os.environ.setdefault("AZURE_OPENAI_EMBEDDING_DEPLOYMENT", "embed-prod")
os.environ.setdefault("EMBEDDING_OUTPUT_DIMENSION", "768")

# Add the python_sidecar package root to sys.path so `import services.*` works
_PACKAGE = pathlib.Path(__file__).resolve().parent.parent.parent  # …/python_sidecar
if str(_PACKAGE) not in sys.path:
    sys.path.insert(0, str(_PACKAGE))


@pytest.fixture(autouse=True)
def grpc_mocks():
    """Override the parent conftest's autouse fixture — unit tests don't need grpc."""
    yield
