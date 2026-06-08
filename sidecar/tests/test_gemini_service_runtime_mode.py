from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from services.gemini_service import GeminiService, _normalize_service_tier, _service_tier_for_wisdev


def test_gemini_service_vertex_proxy_mode_is_explicit():
    with patch.dict(
        "os.environ",
        {
            "GEMINI_RUNTIME_MODE": "vertex_proxy",
            "VERTEX_PROXY_URL": "https://proxy.example",
            "GOOGLE_API_KEY": "",
            "GOOGLE_CLOUD_PROJECT": "",
        },
        clear=False,
    ):
        with patch("services.gemini_service.logger.info") as mock_info:
            service = GeminiService()

    assert service._client is None
    mock_info.assert_any_call("GeminiService configured for vertex_proxy mode")


def test_gemini_service_native_mode_warns_without_sdk():
    real_import = __import__

    def fake_import(name, *args, **kwargs):
        if name == "google.genai":
            raise ImportError("sdk missing")
        return real_import(name, *args, **kwargs)

    with patch.dict(
        "os.environ",
        {
            "GEMINI_RUNTIME_MODE": "native",
            "VERTEX_PROXY_URL": "",
            "GOOGLE_API_KEY": "",
            "GOOGLE_CLOUD_PROJECT": "",
        },
        clear=False,
    ):
        with patch("builtins.__import__", side_effect=fake_import):
            with patch("services.gemini_service.logger.warning") as mock_warning:
                service = GeminiService()

    assert service._client is None
    mock_warning.assert_any_call("google-genai SDK not installed while GEMINI_RUNTIME_MODE=native")


def test_gemini_runtime_diagnostics_report_degraded_when_project_is_configured_but_client_is_unavailable():
    from services import gemini_service as gmod

    with patch.dict(
        "os.environ",
        {
            "GEMINI_RUNTIME_MODE": "auto",
            "GOOGLE_CLOUD_PROJECT": "test-project",
            "VERTEX_PROXY_URL": "",
            "GOOGLE_API_KEY": "",
        },
        clear=False,
    ):
        with patch.object(gmod.GeminiService, "_build_client", return_value=None):
            diagnostics = gmod.get_gemini_runtime_diagnostics()

    assert diagnostics["status"] == "degraded"
    assert diagnostics["source"] == "vertex_project"
    assert diagnostics["ready"] is False
    assert "native Gemini client is unavailable" in diagnostics["detail"]


def test_service_tier_helpers_map_wisdev_modes():
    assert _service_tier_for_wisdev("guided") == "standard"
    assert _service_tier_for_wisdev("yolo") == "flex"
    assert _service_tier_for_wisdev("guided", interactive=True) == "priority"
    assert _normalize_service_tier(" Priority ") == "priority"
    assert _normalize_service_tier("unknown") is None


@pytest.mark.asyncio
async def test_gemini_service_proxy_mode_rejects_empty_text():
    response = MagicMock()
    response.raise_for_status.return_value = None
    response.json.return_value = {"text": "   "}

    client = AsyncMock()
    client.post.return_value = response
    client.__aenter__.return_value = client
    client.__aexit__.return_value = None

    with patch.dict(
        "os.environ",
        {
            "GEMINI_RUNTIME_MODE": "vertex_proxy",
            "VERTEX_PROXY_URL": "https://proxy.example",
        },
        clear=False,
    ):
        with patch("httpx.AsyncClient", return_value=client):
            service = GeminiService()
            with pytest.raises(RuntimeError, match="Vertex proxy returned empty text"):
                await service.generate_text("rank these papers")


@pytest.mark.asyncio
async def test_gemini_service_proxy_includes_service_tier():
    response = MagicMock()
    response.raise_for_status.return_value = None
    response.json.return_value = {"text": "ok"}

    client = AsyncMock()
    client.post.return_value = response
    client.__aenter__.return_value = client
    client.__aexit__.return_value = None

    with patch.dict(
        "os.environ",
        {
            "GEMINI_RUNTIME_MODE": "vertex_proxy",
            "VERTEX_PROXY_URL": "https://proxy.example",
        },
        clear=False,
    ):
        with patch("httpx.AsyncClient", return_value=client):
            service = GeminiService()
            result = await service.generate_text("rank these papers", service_tier="flex")

    assert result == "ok"
    _, kwargs = client.post.call_args
    assert kwargs["json"]["service_tier"] == "flex"


@pytest.mark.asyncio
async def test_gemini_service_native_mode_rejects_empty_text():
    service = GeminiService()

    mock_client = MagicMock()
    mock_client.models.generate_content.return_value = MagicMock(text=" ")
    service._client = mock_client

    with pytest.raises(RuntimeError, match="Gemini returned empty text"):
        await service.generate_text("rank these papers")
