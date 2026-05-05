"""Unit tests for services/gemini_service.py — all external I/O mocked."""

import asyncio
import json
import os
import sys
import tempfile
import pytest
from unittest.mock import AsyncMock, MagicMock, patch, call
from pydantic import BaseModel


# ---------------------------------------------------------------------------
# Helper model
# ---------------------------------------------------------------------------

class SimpleModel(BaseModel):
    value: str
    count: int = 0


# ---------------------------------------------------------------------------
# Module-level helper functions
# ---------------------------------------------------------------------------

class TestModuleFunctions:
    def test_load_model_tier_no_file_returns_default(self):
        from services.gemini_service import _load_model_tier
        with patch("os.path.exists", return_value=False):
            result = _load_model_tier("heavy", "gemini-fallback")
        assert result == "gemini-fallback"

    def test_load_model_tier_reads_json_file(self, tmp_path, monkeypatch):
        from services.gemini_service import _load_model_tier
        model_data = {"light": "gemini-2.0-flash", "heavy": "gemini-2.5-pro"}
        model_file = tmp_path / "wisdev_models.json"
        model_file.write_text(json.dumps(model_data))
        # Run from the tmp_path directory so the function finds "wisdev_models.json"
        monkeypatch.chdir(tmp_path)
        result = _load_model_tier("heavy", "fallback")
        assert result == "gemini-2.5-pro"

    def test_jitter_within_range(self):
        from services.gemini_service import _jitter
        for _ in range(20):
            result = _jitter(1.0)
            assert 1.0 <= result <= 1.5

    def test_gemini_api_key_from_env(self, monkeypatch):
        from services import gemini_service as gmod
        monkeypatch.setenv("GOOGLE_API_KEY", "test-key-abc")
        assert gmod._gemini_api_key() == "test-key-abc"

    def test_gemini_api_key_empty(self, monkeypatch):
        from services import gemini_service as gmod
        monkeypatch.delenv("GOOGLE_API_KEY", raising=False)
        assert gmod._gemini_api_key() == ""

    def test_gemini_api_key_from_secret_manager(self, monkeypatch):
        from services import gemini_service as gmod
        monkeypatch.delenv("GOOGLE_API_KEY", raising=False)
        monkeypatch.setattr(gmod, "get_google_api_key", lambda: "gsm-key")
        assert gmod._gemini_api_key() == "gsm-key"

    def test_gemini_runtime_diagnostics_reports_vertex_proxy(self, monkeypatch):
        from services import gemini_service as gmod
        from unittest.mock import patch
        monkeypatch.setenv("VERTEX_PROXY_URL", "https://proxy.example.com")
        monkeypatch.delenv("GOOGLE_CLOUD_PROJECT", raising=False)
        with patch("services.gemini_service.get_google_api_key_resolution", return_value={"status": "missing"}):
            with patch("services.gemini_service.get_project_id_resolution", return_value={"projectId": "", "projectConfigured": False, "projectSource": "none"}):
                diagnostics = gmod.get_gemini_runtime_diagnostics()
        assert diagnostics["status"] == "configured"
        assert diagnostics["source"] == "vertex_proxy"

    def test_vertex_project_from_google_cloud_project(self, monkeypatch):
        from services import gemini_service as gmod
        monkeypatch.setenv("GOOGLE_CLOUD_PROJECT", "my-project")
        monkeypatch.delenv("GCLOUD_PROJECT", raising=False)
        assert gmod._vertex_project() == "my-project"

    def test_vertex_project_from_gcloud_project(self, monkeypatch):
        from services import gemini_service as gmod
        monkeypatch.delenv("GOOGLE_CLOUD_PROJECT", raising=False)
        monkeypatch.setenv("GCLOUD_PROJECT", "gcloud-proj")
        assert gmod._vertex_project() == "gcloud-proj"

    def test_vertex_proxy_url_from_env(self, monkeypatch):
        from services import gemini_service as gmod
        monkeypatch.setenv("VERTEX_PROXY_URL", "https://proxy.example.com/")
        assert gmod._vertex_proxy_url() == "https://proxy.example.com"  # trailing slash stripped

    def test_gemini_runtime_mode_auto(self, monkeypatch):
        from services import gemini_service as gmod
        monkeypatch.setenv("GEMINI_RUNTIME_MODE", "auto")
        assert gmod._gemini_runtime_mode() == "auto"

    def test_gemini_runtime_mode_native(self, monkeypatch):
        from services import gemini_service as gmod
        monkeypatch.setenv("GEMINI_RUNTIME_MODE", "native")
        assert gmod._gemini_runtime_mode() == "native"

    def test_gemini_runtime_mode_vertex_proxy(self, monkeypatch):
        from services import gemini_service as gmod
        monkeypatch.setenv("GEMINI_RUNTIME_MODE", "vertex_proxy")
        assert gmod._gemini_runtime_mode() == "vertex_proxy"

    def test_gemini_runtime_mode_unknown_defaults_to_auto(self, monkeypatch):
        from services import gemini_service as gmod
        monkeypatch.setenv("GEMINI_RUNTIME_MODE", "invalid_mode")
        result = gmod._gemini_runtime_mode()
        assert result == "auto"

    def test_require_non_empty_text_returns_stripped(self):
        from services.gemini_service import _require_non_empty_text
        assert _require_non_empty_text("  hello  ", "src") == "hello"

    def test_require_non_empty_text_raises_on_empty(self):
        from services.gemini_service import _require_non_empty_text
        with pytest.raises(RuntimeError, match="src returned empty text"):
            _require_non_empty_text("", "src")

    def test_require_non_empty_text_raises_on_whitespace(self):
        from services.gemini_service import _require_non_empty_text
        with pytest.raises(RuntimeError):
            _require_non_empty_text("   ", "source")

    def test_require_non_empty_text_raises_on_none(self):
        from services.gemini_service import _require_non_empty_text
        with pytest.raises(RuntimeError):
            _require_non_empty_text(None, "source")


# ---------------------------------------------------------------------------
# GeminiService initialization and is_ready
# ---------------------------------------------------------------------------

class TestGeminiServiceInit:
    def test_is_ready_no_client_no_proxy(self, monkeypatch):
        from services.gemini_service import GeminiService
        monkeypatch.delenv("VERTEX_PROXY_URL", raising=False)
        monkeypatch.delenv("GOOGLE_API_KEY", raising=False)
        monkeypatch.delenv("GOOGLE_CLOUD_PROJECT", raising=False)
        with patch("services.gemini_service._build_client" if False else "services.gemini_service.GeminiService._build_client", return_value=None):
            svc = GeminiService.__new__(GeminiService)
            svc.model = "test-model"
            svc._client = None
        assert svc.is_ready() is False

    def test_is_ready_with_proxy(self, monkeypatch):
        from services.gemini_service import GeminiService
        monkeypatch.setenv("VERTEX_PROXY_URL", "https://proxy.example.com")
        svc = GeminiService.__new__(GeminiService)
        svc.model = "test-model"
        svc._client = None
        assert svc.is_ready() is True

    def test_is_ready_with_client(self):
        from services.gemini_service import GeminiService
        svc = GeminiService.__new__(GeminiService)
        svc.model = "test-model"
        svc._client = MagicMock()
        assert svc.is_ready() is True


# ---------------------------------------------------------------------------
# _build_client branches
# ---------------------------------------------------------------------------

class TestBuildClient:
    def test_vertex_proxy_mode_with_url(self, monkeypatch):
        from services.gemini_service import GeminiService
        monkeypatch.setenv("GEMINI_RUNTIME_MODE", "vertex_proxy")
        monkeypatch.setenv("VERTEX_PROXY_URL", "https://proxy.example.com")
        monkeypatch.delenv("GOOGLE_API_KEY", raising=False)
        monkeypatch.delenv("GOOGLE_CLOUD_PROJECT", raising=False)
        svc = GeminiService(model="test-model")
        assert svc._client is None

    def test_vertex_proxy_mode_no_url(self, monkeypatch):
        from services.gemini_service import GeminiService
        monkeypatch.setenv("GEMINI_RUNTIME_MODE", "vertex_proxy")
        monkeypatch.delenv("VERTEX_PROXY_URL", raising=False)
        monkeypatch.delenv("GOOGLE_API_KEY", raising=False)
        monkeypatch.delenv("GOOGLE_CLOUD_PROJECT", raising=False)
        svc = GeminiService(model="test-model")
        assert svc._client is None

    def test_import_error_with_proxy_fallback(self, monkeypatch):
        from services.gemini_service import GeminiService
        monkeypatch.setenv("GEMINI_RUNTIME_MODE", "auto")
        monkeypatch.setenv("VERTEX_PROXY_URL", "https://proxy.example.com")
        monkeypatch.delenv("GOOGLE_API_KEY", raising=False)
        monkeypatch.delenv("GOOGLE_CLOUD_PROJECT", raising=False)

        original_import = __builtins__.__import__ if hasattr(__builtins__, '__import__') else __import__

        def mock_import(name, *args, **kwargs):
            if name == "google.genai":
                raise ImportError("no google-genai")
            return original_import(name, *args, **kwargs)

        with patch("builtins.__import__", side_effect=mock_import):
            svc = GeminiService(model="test-model")
        # With import error and proxy available, client is None
        assert svc._client is None

    def test_import_error_native_mode_warning(self, monkeypatch):
        from services.gemini_service import GeminiService
        monkeypatch.setenv("GEMINI_RUNTIME_MODE", "native")
        monkeypatch.delenv("VERTEX_PROXY_URL", raising=False)
        monkeypatch.delenv("GOOGLE_API_KEY", raising=False)
        monkeypatch.delenv("GOOGLE_CLOUD_PROJECT", raising=False)

        with patch("services.gemini_service.logger") as mock_logger:
            # Make google.genai import raise ImportError
            with patch.dict(sys.modules, {"google.genai": None}):
                svc = GeminiService(model="test-model")
        assert svc._client is None

    def test_no_credentials_auto_mode(self, monkeypatch):
        from services.gemini_service import GeminiService
        monkeypatch.setenv("GEMINI_RUNTIME_MODE", "auto")
        monkeypatch.delenv("VERTEX_PROXY_URL", raising=False)
        monkeypatch.delenv("GOOGLE_API_KEY", raising=False)
        monkeypatch.delenv("GOOGLE_CLOUD_PROJECT", raising=False)
        monkeypatch.delenv("GCLOUD_PROJECT", raising=False)

        mock_genai = MagicMock()
        # Make Client raise ValueError (no credentials)
        mock_genai.Client.side_effect = Exception("no credentials")
        with patch("services.gemini_service.get_project_id_resolution", return_value={"projectId": "", "projectConfigured": False, "projectSource": "none"}):
            with patch("services.gemini_service.get_google_api_key_resolution", return_value={"status": "missing"}):
                with patch.dict(sys.modules, {"google.genai": mock_genai}):
                    svc = GeminiService(model="test-model")
        assert svc._client is None


# ---------------------------------------------------------------------------
# generate_json — no client, no proxy → RuntimeError
# ---------------------------------------------------------------------------

class TestGenerateJsonErrors:
    @pytest.mark.asyncio
    async def test_no_client_no_proxy_raises(self, monkeypatch):
        from services.gemini_service import GeminiService
        monkeypatch.delenv("VERTEX_PROXY_URL", raising=False)
        svc = GeminiService.__new__(GeminiService)
        svc.model = "test"
        svc._client = None
        with pytest.raises(RuntimeError, match="VERTEX_PROXY_URL"):
            await svc.generate_json("prompt", SimpleModel)

    @pytest.mark.asyncio
    async def test_with_proxy_calls_vertex_method(self, monkeypatch):
        from services.gemini_service import GeminiService
        monkeypatch.setenv("VERTEX_PROXY_URL", "https://proxy.test.com")
        svc = GeminiService.__new__(GeminiService)
        svc.model = "test"
        svc._client = None

        expected = SimpleModel(value="result")
        with patch.object(svc, "_generate_via_vertex_proxy", AsyncMock(return_value=expected)):
            result = await svc.generate_json("prompt", SimpleModel)
        assert result == expected


# ---------------------------------------------------------------------------
# generate_with_thinking — no client delegates to generate_json
# ---------------------------------------------------------------------------

class TestGenerateWithThinking:
    @pytest.mark.asyncio
    async def test_no_client_delegates_to_generate_json(self, monkeypatch):
        from services.gemini_service import GeminiService
        svc = GeminiService.__new__(GeminiService)
        svc.model = "test"
        svc._client = None

        expected = SimpleModel(value="thinking_result")
        with patch.object(svc, "generate_json", AsyncMock(return_value=expected)):
            result = await svc.generate_with_thinking("prompt", SimpleModel)
        assert result == expected

    @pytest.mark.asyncio
    async def test_with_client_import_error_falls_back(self):
        from services.gemini_service import GeminiService
        svc = GeminiService.__new__(GeminiService)
        svc.model = "test"
        svc._client = MagicMock()

        expected = SimpleModel(value="fallback")
        with patch("services.gemini_service.asyncio.wait_for", side_effect=ImportError("no ThinkingConfig")):
            with patch.object(svc, "generate_json", AsyncMock(return_value=expected)):
                result = await svc.generate_with_thinking("prompt", SimpleModel)
        assert result == expected

    @pytest.mark.asyncio
    async def test_with_client_exception_falls_back(self):
        from services.gemini_service import GeminiService
        svc = GeminiService.__new__(GeminiService)
        svc.model = "test"
        svc._client = MagicMock()

        expected = SimpleModel(value="fallback_from_exc")
        with patch("services.gemini_service.asyncio.wait_for", side_effect=RuntimeError("API error")):
            with patch.object(svc, "generate_json", AsyncMock(return_value=expected)):
                result = await svc.generate_with_thinking("prompt", SimpleModel)
        assert result == expected


# ---------------------------------------------------------------------------
# generate_text — both paths
# ---------------------------------------------------------------------------

class TestGenerateText:
    @pytest.mark.asyncio
    async def test_no_client_no_proxy_raises(self, monkeypatch):
        from services.gemini_service import GeminiService
        monkeypatch.delenv("VERTEX_PROXY_URL", raising=False)
        svc = GeminiService.__new__(GeminiService)
        svc.model = "test"
        svc._client = None
        with pytest.raises(RuntimeError, match="VERTEX_PROXY_URL"):
            await svc.generate_text("prompt")

    @pytest.mark.asyncio
    async def test_no_client_calls_proxy(self, monkeypatch):
        from services.gemini_service import GeminiService
        monkeypatch.setenv("VERTEX_PROXY_URL", "https://proxy.test.com")
        svc = GeminiService.__new__(GeminiService)
        svc.model = "test"
        svc._client = None

        with patch.object(svc, "_generate_text_proxy", AsyncMock(return_value="result text")):
            result = await svc.generate_text("prompt")
        assert result == "result text"

    @pytest.mark.asyncio
    async def test_with_client_calls_native(self):
        from services.gemini_service import GeminiService
        svc = GeminiService.__new__(GeminiService)
        svc.model = "test"
        svc._client = MagicMock()

        with patch.object(svc, "_generate_text_native", AsyncMock(return_value="native text")):
            result = await svc.generate_text("prompt")
        assert result == "native text"


# ---------------------------------------------------------------------------
# _generate_via_vertex_proxy
# ---------------------------------------------------------------------------

class TestGenerateViaVertexProxy:
    @pytest.mark.asyncio
    async def test_no_proxy_url_raises(self, monkeypatch):
        from services.gemini_service import GeminiService
        monkeypatch.delenv("VERTEX_PROXY_URL", raising=False)
        svc = GeminiService.__new__(GeminiService)
        svc.model = "test"
        svc._client = None
        with pytest.raises(RuntimeError, match="VERTEX_PROXY_URL"):
            await svc._generate_via_vertex_proxy("prompt", SimpleModel, 0.3, 1024, None)

    @pytest.mark.asyncio
    async def test_success_path(self, monkeypatch):
        from services.gemini_service import GeminiService
        monkeypatch.setenv("VERTEX_PROXY_URL", "https://proxy.test.com")
        svc = GeminiService.__new__(GeminiService)
        svc.model = "test-model"
        svc._client = None

        mock_resp = MagicMock()
        mock_resp.json.return_value = {"value": "proxy_result", "count": 7}
        mock_resp.raise_for_status = MagicMock()

        with patch("httpx.AsyncClient") as mock_cls:
            mock_client = AsyncMock()
            mock_client.__aenter__ = AsyncMock(return_value=mock_client)
            mock_client.__aexit__ = AsyncMock(return_value=False)
            mock_client.post = AsyncMock(return_value=mock_resp)
            mock_cls.return_value = mock_client

            result = await svc._generate_via_vertex_proxy("prompt", SimpleModel, 0.3, 1024, None)

        assert isinstance(result, SimpleModel)
        assert result.value == "proxy_result"

    @pytest.mark.asyncio
    async def test_failure_raises_after_retries(self, monkeypatch):
        from services.gemini_service import GeminiService
        monkeypatch.setenv("VERTEX_PROXY_URL", "https://proxy.test.com")
        svc = GeminiService.__new__(GeminiService)
        svc.model = "test"
        svc._client = None

        with patch("httpx.AsyncClient") as mock_cls:
            mock_client = AsyncMock()
            mock_client.__aenter__ = AsyncMock(return_value=mock_client)
            mock_client.__aexit__ = AsyncMock(return_value=False)
            mock_client.post = AsyncMock(side_effect=RuntimeError("network error"))
            mock_cls.return_value = mock_client

            with patch("services.gemini_service.asyncio.sleep", AsyncMock()):
                with pytest.raises(RuntimeError):
                    await svc._generate_via_vertex_proxy("prompt", SimpleModel, 0.3, 1024, None)


# ---------------------------------------------------------------------------
# _generate_text_proxy
# ---------------------------------------------------------------------------

class TestGenerateTextProxy:
    @pytest.mark.asyncio
    async def test_no_proxy_raises(self, monkeypatch):
        from services.gemini_service import GeminiService
        monkeypatch.delenv("VERTEX_PROXY_URL", raising=False)
        svc = GeminiService.__new__(GeminiService)
        svc.model = "test"
        svc._client = None
        with pytest.raises(RuntimeError, match="VERTEX_PROXY_URL"):
            await svc._generate_text_proxy("prompt", 0.7, 1024, None)

    @pytest.mark.asyncio
    async def test_success_returns_text(self, monkeypatch):
        from services.gemini_service import GeminiService
        monkeypatch.setenv("VERTEX_PROXY_URL", "https://proxy.test.com")
        svc = GeminiService.__new__(GeminiService)
        svc.model = "test"
        svc._client = None

        mock_resp = MagicMock()
        mock_resp.json.return_value = {"text": "generated text here"}
        mock_resp.raise_for_status = MagicMock()

        with patch("httpx.AsyncClient") as mock_cls:
            mock_client = AsyncMock()
            mock_client.__aenter__ = AsyncMock(return_value=mock_client)
            mock_client.__aexit__ = AsyncMock(return_value=False)
            mock_client.post = AsyncMock(return_value=mock_resp)
            mock_cls.return_value = mock_client

            result = await svc._generate_text_proxy("prompt", 0.7, 1024, None)
        assert result == "generated text here"


# ---------------------------------------------------------------------------
# embed / embed_batch — no client raises
# ---------------------------------------------------------------------------

class TestEmbed:
    @pytest.mark.asyncio
    async def test_embed_no_client_raises(self):
        from services.gemini_service import GeminiService
        svc = GeminiService.__new__(GeminiService)
        svc.model = "test"
        svc._client = None
        with pytest.raises(RuntimeError, match="native google-genai SDK"):
            await svc.embed("hello world")

    @pytest.mark.asyncio
    async def test_embed_batch_empty_returns_empty(self):
        from services.gemini_service import GeminiService
        svc = GeminiService.__new__(GeminiService)
        svc.model = "test"
        svc._client = None
        result = await svc.embed_batch([])
        assert result == []

    @pytest.mark.asyncio
    async def test_embed_batch_no_client_raises(self):
        from services.gemini_service import GeminiService
        svc = GeminiService.__new__(GeminiService)
        svc.model = "test"
        svc._client = None
        with pytest.raises(RuntimeError, match="native google-genai SDK"):
            await svc.embed_batch(["text1", "text2"])


# ---------------------------------------------------------------------------
# generate_structured
# ---------------------------------------------------------------------------

class TestGenerateStructured:
    @pytest.mark.asyncio
    async def test_no_client_requires_native_runtime(self, monkeypatch):
        from services.gemini_service import (
            GeminiService,
            StructuredOutputRequiresNativeRuntimeError,
        )
        monkeypatch.setenv("VERTEX_PROXY_URL", "https://proxy.test.com")
        svc = GeminiService.__new__(GeminiService)
        svc.model = "test"
        svc._client = None

        proxy = AsyncMock(return_value='{"result": "ok"}')
        with patch.object(svc, "_generate_text_proxy", proxy):
            with pytest.raises(
                StructuredOutputRequiresNativeRuntimeError,
                match="requires native Gemini runtime",
            ):
                await svc.generate_structured("prompt", {"type": "object"})
        proxy.assert_not_awaited()
