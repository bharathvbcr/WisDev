"""
services/manuscript_llm.py — Provider selector for manuscript drafting.

The manuscript endpoints historically called ``gemini_service`` directly, so a
sidecar with no Gemini/Vertex credentials silently degraded to grounded scaffolds.
This selector lets a local LLM (Ollama / any OpenAI-compatible server) serve the
drafting, review, coordination and fact-check calls instead, chosen via the
``MANUSCRIPT_LLM_PROVIDER`` environment variable:

  - ``local`` / ``ollama``     -> always use the local backend
  - ``gemini`` / ``vertex`` / ``cloud`` -> always use Gemini/Vertex (the default)
  - unset (``auto``)           -> use the local backend when one is explicitly
                                  configured (LOCAL_LLM_BASE_URL / OLLAMA_BASE_URL),
                                  otherwise Gemini — preserving today's behavior.

Both backends expose the same ``generate_text`` / ``generate_json`` surface, so
callers select once and use the result uniformly.
"""

from __future__ import annotations

import os

from services.gemini_service import gemini_service
from services.local_llm_service import local_llm_service

_LOCAL = {"local", "ollama"}
_CLOUD = {"gemini", "vertex", "cloud"}


def manuscript_llm():
    """Return the configured LLM backend for manuscript drafting."""
    provider = str(os.environ.get("MANUSCRIPT_LLM_PROVIDER", "")).strip().lower()
    if provider in _LOCAL:
        return local_llm_service
    if provider in _CLOUD:
        return gemini_service
    # auto: prefer a local backend only when one is explicitly configured.
    if local_llm_service.available():
        return local_llm_service
    return gemini_service
