"""Empty-text degradation ladder for structured output.

Models with aggressive thinking can return empty text (finish_reason=MAX_TOKENS)
on every identical retry; generate_structured must *minimize* the thinking config
on the first empty-text failure — removing it entirely does not disable thinking
on Gemini 3.x, so it must be pinned to the minimum instead — and fall back to a
non-lite model on the next.
"""

from __future__ import annotations

from types import SimpleNamespace
from unittest.mock import MagicMock

import pytest

import services.gemini_service as gemini_service
from services.gemini_service import (
    EmptyStructuredTextError,
    GeminiService,
    _extract_response_diagnostics,
    _is_empty_text_error,
    _minimized_thinking_config,
    _normalize_structured_json_response,
    _require_non_empty_text_with_diagnostics,
    _structured_empty_text_fallback_model,
)

LITE_MODEL = "gemini-3.1-flash-lite"
FALLBACK_MODEL = "gemini-test-standard"


def test_is_empty_text_error_matches_only_empty_text_runtime_errors():
    assert _is_empty_text_error(RuntimeError("Gemini returned empty text"))
    assert not _is_empty_text_error(RuntimeError("quota exceeded"))
    assert not _is_empty_text_error(ValueError("Gemini returned empty text"))
    # The richer EmptyStructuredTextError still matches: it is a RuntimeError
    # whose message contains "returned empty text".
    assert _is_empty_text_error(
        EmptyStructuredTextError("Gemini returned empty text", safety_blocked=True)
    )


def test_extract_response_diagnostics_flags_safety_finish_reason():
    response = SimpleNamespace(
        candidates=[SimpleNamespace(finish_reason="FinishReason.SAFETY")],
        prompt_feedback=None,
    )
    diag = _extract_response_diagnostics(response)
    assert diag["finish_reason"] == "SAFETY"
    assert diag["safety_blocked"] is True


def test_extract_response_diagnostics_flags_prompt_block_reason():
    response = SimpleNamespace(
        candidates=[],
        prompt_feedback=SimpleNamespace(block_reason="PROHIBITED_CONTENT"),
    )
    diag = _extract_response_diagnostics(response)
    assert diag["block_reason"] == "PROHIBITED_CONTENT"
    assert diag["safety_blocked"] is True


def test_extract_response_diagnostics_flags_blocked_safety_rating():
    response = SimpleNamespace(
        candidates=[
            SimpleNamespace(
                finish_reason="STOP",
                safety_ratings=[
                    SimpleNamespace(category="HARM_CATEGORY_DANGEROUS", blocked=True)
                ],
            )
        ],
        prompt_feedback=None,
    )
    diag = _extract_response_diagnostics(response)
    assert diag["safety_blocked"] is True
    assert "HARM_CATEGORY_DANGEROUS" in diag["safety_categories"]


def test_extract_response_diagnostics_benign_empty_is_not_safety():
    # MAX_TOKENS (thinking burned the budget) is empty-but-retryable, not a block.
    response = SimpleNamespace(
        candidates=[SimpleNamespace(finish_reason="MAX_TOKENS")],
        prompt_feedback=SimpleNamespace(block_reason="BLOCK_REASON_UNSPECIFIED"),
    )
    diag = _extract_response_diagnostics(response)
    assert diag["finish_reason"] == "MAX_TOKENS"
    assert diag["safety_blocked"] is False


def test_require_non_empty_text_with_diagnostics_returns_text():
    response = SimpleNamespace(text="hello world", candidates=[], prompt_feedback=None)
    assert _require_non_empty_text_with_diagnostics(response, "Gemini") == "hello world"


def test_require_non_empty_text_with_diagnostics_raises_safety_aware_on_empty():
    # The plain-text generate path must surface a safety block, not an opaque empty.
    response = SimpleNamespace(
        text="",
        candidates=[SimpleNamespace(finish_reason="SAFETY")],
        prompt_feedback=None,
    )
    with pytest.raises(EmptyStructuredTextError) as excinfo:
        _require_non_empty_text_with_diagnostics(response, "Gemini")
    assert excinfo.value.safety_blocked is True
    assert excinfo.value.finish_reason == "SAFETY"
    # A genuinely empty (non-safety) response still raises, but not flagged blocked.
    benign = SimpleNamespace(
        text="   ", candidates=[SimpleNamespace(finish_reason="MAX_TOKENS")], prompt_feedback=None
    )
    with pytest.raises(EmptyStructuredTextError) as benign_exc:
        _require_non_empty_text_with_diagnostics(benign, "Gemini")
    assert benign_exc.value.safety_blocked is False


def test_normalize_structured_json_response_raises_safety_aware_empty_text():
    response = SimpleNamespace(
        text=None,
        parsed=None,
        candidates=[SimpleNamespace(finish_reason="SAFETY", content=None)],
        prompt_feedback=None,
    )
    with pytest.raises(EmptyStructuredTextError) as excinfo:
        _normalize_structured_json_response(response, "Gemini")
    assert excinfo.value.safety_blocked is True
    assert excinfo.value.finish_reason == "SAFETY"


def test_minimized_thinking_config_pins_minimum_per_model_class(monkeypatch):
    # Echo the thinking kwargs so the resolved knob is inspectable without the SDK.
    monkeypatch.setattr(
        gemini_service, "build_optional_thinking_config", lambda kwargs, **_: dict(kwargs)
    )

    # Gemini 3.x uses thinking_level: removing the field would revert to the
    # model default (thinking on), so the retry must pin MINIMAL, not drop.
    assert _minimized_thinking_config("gemini-3.5-flash", operation="t") == {
        "thinking_level": "MINIMAL"
    }
    # Gemini 2.5 flash can disable thinking entirely → budget 0.
    assert _minimized_thinking_config("gemini-2.5-flash", operation="t") == {
        "thinking_budget": 0
    }
    # Gemini 2.5 pro cannot disable → pinned to its minimum budget (128).
    assert _minimized_thinking_config("gemini-2.5-pro", operation="t") == {
        "thinking_budget": 128
    }
    # A model with no thinking knob returns None → caller drops the field.
    assert _minimized_thinking_config("some-nonthinking-model", operation="t") is None


@pytest.mark.asyncio
async def test_generate_structured_drops_thinking_when_model_has_no_knob(monkeypatch):
    # A model that supports no thinking knob has nothing to minimize, so the
    # retry must fall back to removing the field entirely.
    svc, attempts = _make_service(monkeypatch)
    monkeypatch.setattr(gemini_service, "_supports_thinking_level", lambda model: False)
    monkeypatch.setattr(gemini_service, "_supports_thinking_budget", lambda model: False)

    outcomes = [RuntimeError("Gemini returned empty text"), ('{"ok": 3}', False)]

    def fake_normalize(resp, source):
        outcome = outcomes.pop(0)
        if isinstance(outcome, Exception):
            raise outcome
        return outcome

    monkeypatch.setattr(
        gemini_service, "_normalize_structured_json_response", fake_normalize
    )
    monkeypatch.setattr(gemini_service, "_extract_generation_usage", lambda resp: (0, 0))

    result = await svc.generate_structured("prompt", {"type": "object"})

    assert result == '{"ok": 3}'
    assert len(attempts) == 2
    assert "thinking_config" not in attempts[1]["config"]


def test_structured_empty_text_fallback_model_escalates_to_heavier(monkeypatch):
    monkeypatch.setattr(gemini_service, "GEMINI_STANDARD_MODEL", FALLBACK_MODEL)
    monkeypatch.setattr(gemini_service, "GEMINI_HEAVY_MODEL", "gemini-test-heavy")

    # Lite climbs the ladder, standard first.
    assert _structured_empty_text_fallback_model(LITE_MODEL) == FALLBACK_MODEL
    # Non-lite, non-heavy models now escalate straight to heavy (previously "").
    assert _structured_empty_text_fallback_model("gemini-2.5-flash") == "gemini-test-heavy"
    assert _structured_empty_text_fallback_model(FALLBACK_MODEL) == "gemini-test-heavy"
    # Already at the heavy tier: nothing heavier to escalate to.
    assert _structured_empty_text_fallback_model("gemini-test-heavy") == ""
    assert _structured_empty_text_fallback_model("") == ""

    # Lite falls through to heavy when standard matches the failing model.
    monkeypatch.setattr(gemini_service, "GEMINI_STANDARD_MODEL", LITE_MODEL)
    assert _structured_empty_text_fallback_model(LITE_MODEL) == "gemini-test-heavy"

    # No distinct heavier model configured at all.
    monkeypatch.setattr(gemini_service, "GEMINI_HEAVY_MODEL", "")
    assert _structured_empty_text_fallback_model(LITE_MODEL) == ""
    assert _structured_empty_text_fallback_model("gemini-2.5-flash") == ""


def _make_service(monkeypatch) -> tuple[GeminiService, list[dict]]:
    """Builds a GeminiService whose native client records each attempt."""
    monkeypatch.setattr(gemini_service, "GEMINI_STANDARD_MODEL", FALLBACK_MODEL)
    monkeypatch.setattr(gemini_service, "GEMINI_HEAVY_MODEL", "gemini-test-heavy")
    monkeypatch.setattr(gemini_service, "_jitter", lambda value: 0)
    # Force a thinking config into the structured request regardless of the
    # SDK's model-support tables.
    monkeypatch.setattr(
        gemini_service,
        "_resolve_reasoning_controls",
        lambda *args, **kwargs: {"thinking_budget": None, "thinking_level": "high"},
    )
    monkeypatch.setattr(gemini_service, "_supports_thinking_level", lambda model: True)
    monkeypatch.setattr(gemini_service, "_supports_thinking_budget", lambda model: False)
    monkeypatch.setattr(
        gemini_service,
        "build_optional_thinking_config",
        lambda kwargs, **_: dict(kwargs),
    )
    # Make the built config inspectable: it is just the kwargs dict.
    monkeypatch.setattr(
        gemini_service,
        "_build_native_generate_config",
        lambda config_kwargs, operation: dict(config_kwargs),
    )

    attempts: list[dict] = []

    def fake_generate_content(*, model, contents, config):
        attempts.append({"model": model, "config": config})
        return MagicMock(name="response")

    svc = GeminiService.__new__(GeminiService)
    svc.model = LITE_MODEL
    svc._client = MagicMock()
    svc._client.models.generate_content = fake_generate_content
    return svc, attempts


@pytest.mark.asyncio
async def test_generate_structured_drops_thinking_then_falls_back_model(monkeypatch):
    svc, attempts = _make_service(monkeypatch)

    empty_text = RuntimeError("Gemini returned empty text")
    outcomes = [empty_text, empty_text, ('{"ok": true}', False)]

    def fake_normalize(resp, source):
        outcome = outcomes.pop(0)
        if isinstance(outcome, Exception):
            raise outcome
        return outcome

    monkeypatch.setattr(
        gemini_service, "_normalize_structured_json_response", fake_normalize
    )
    monkeypatch.setattr(
        gemini_service, "_extract_generation_usage", lambda resp: (1, 2)
    )

    result = await svc.generate_structured("prompt", {"type": "object"})

    assert result == '{"ok": true}'
    assert len(attempts) == 3
    # Attempt 1: lite model, thinking config attached.
    assert attempts[0]["model"] == LITE_MODEL
    assert "thinking_config" in attempts[0]["config"]
    # Attempt 2: same model, thinking pinned to the minimum (NOT removed — a
    # thinking-level model keeps reasoning by default when the field is absent).
    assert attempts[1]["model"] == LITE_MODEL
    assert attempts[1]["config"]["thinking_config"] == {"thinking_level": "MINIMAL"}
    # Attempt 3: non-lite fallback model, minimized thinking carried forward.
    assert attempts[2]["model"] == FALLBACK_MODEL
    assert attempts[2]["config"]["thinking_config"] == {"thinking_level": "MINIMAL"}


@pytest.mark.asyncio
async def test_generate_structured_recovers_after_thinking_drop_alone(monkeypatch):
    svc, attempts = _make_service(monkeypatch)

    outcomes = [RuntimeError("Gemini returned empty text"), ('{"ok": 1}', False)]

    def fake_normalize(resp, source):
        outcome = outcomes.pop(0)
        if isinstance(outcome, Exception):
            raise outcome
        return outcome

    monkeypatch.setattr(
        gemini_service, "_normalize_structured_json_response", fake_normalize
    )
    monkeypatch.setattr(
        gemini_service, "_extract_generation_usage", lambda resp: (0, 0)
    )

    result = await svc.generate_structured("prompt", {"type": "object"})

    assert result == '{"ok": 1}'
    assert len(attempts) == 2
    assert attempts[1]["model"] == LITE_MODEL
    assert attempts[1]["config"]["thinking_config"] == {"thinking_level": "MINIMAL"}


@pytest.mark.asyncio
async def test_generate_structured_safety_block_fails_fast(monkeypatch):
    """A safety-blocked empty response must not retry or escalate the model."""
    svc, attempts = _make_service(monkeypatch)

    def fake_normalize(resp, source):
        raise EmptyStructuredTextError(
            "Gemini returned empty text",
            finish_reason="SAFETY",
            safety_blocked=True,
        )

    monkeypatch.setattr(
        gemini_service, "_normalize_structured_json_response", fake_normalize
    )

    with pytest.raises(EmptyStructuredTextError) as excinfo:
        await svc.generate_structured("prompt", {"type": "object"})

    assert excinfo.value.safety_blocked is True
    # Exactly one provider call: no wasted retries or model escalation.
    assert len(attempts) == 1
    assert attempts[0]["model"] == LITE_MODEL


@pytest.mark.asyncio
async def test_generate_stream_raises_safety_aware_on_empty_stream(monkeypatch):
    """A stream that yields no text (blocked candidate) must raise, not finish silently."""
    svc, _ = _make_service(monkeypatch)

    blocked_chunk = SimpleNamespace(
        text="",
        candidates=[SimpleNamespace(finish_reason="SAFETY")],
        prompt_feedback=None,
    )

    async def fake_stream_coro(*_args, **_kwargs):
        async def _gen():
            yield blocked_chunk

        return _gen()

    svc._client.aio.models.generate_content_stream = fake_stream_coro

    collected = []
    with pytest.raises(EmptyStructuredTextError) as excinfo:
        async for piece in svc.generate_stream("prompt"):
            collected.append(piece)

    assert collected == []  # no text surfaced before the block was detected
    assert excinfo.value.safety_blocked is True
    assert excinfo.value.finish_reason == "SAFETY"


@pytest.mark.asyncio
async def test_generate_stream_completes_on_benign_empty_stream(monkeypatch):
    """A non-safety empty stream must complete as before (no new failure mode)."""
    svc, _ = _make_service(monkeypatch)

    benign_chunk = SimpleNamespace(
        text="",
        candidates=[SimpleNamespace(finish_reason="MAX_TOKENS")],
        prompt_feedback=None,
    )

    async def fake_stream_coro(*_args, **_kwargs):
        async def _gen():
            yield benign_chunk

        return _gen()

    svc._client.aio.models.generate_content_stream = fake_stream_coro

    # Completes without raising and yields nothing (prior contract preserved).
    collected = [piece async for piece in svc.generate_stream("prompt")]
    assert collected == []


@pytest.mark.asyncio
async def test_generate_stream_yields_text_when_present(monkeypatch):
    """A stream that yields text completes normally (no spurious empty-stream raise)."""
    svc, _ = _make_service(monkeypatch)

    chunks = [SimpleNamespace(text="alpha"), SimpleNamespace(text="beta")]

    async def fake_stream_coro(*_args, **_kwargs):
        async def _gen():
            for chunk in chunks:
                yield chunk

        return _gen()

    svc._client.aio.models.generate_content_stream = fake_stream_coro

    collected = [piece async for piece in svc.generate_stream("prompt")]
    assert collected == ["alpha", "beta"]


@pytest.mark.asyncio
async def test_generate_structured_non_empty_errors_keep_config(monkeypatch):
    svc, attempts = _make_service(monkeypatch)

    outcomes = [RuntimeError("transient parse failure"), ('{"ok": 2}', False)]

    def fake_normalize(resp, source):
        outcome = outcomes.pop(0)
        if isinstance(outcome, Exception):
            raise outcome
        return outcome

    monkeypatch.setattr(
        gemini_service, "_normalize_structured_json_response", fake_normalize
    )
    monkeypatch.setattr(
        gemini_service, "_extract_generation_usage", lambda resp: (0, 0)
    )

    result = await svc.generate_structured("prompt", {"type": "object"})

    assert result == '{"ok": 2}'
    assert len(attempts) == 2
    # Non-empty-text errors must not trigger the degradation ladder.
    assert attempts[1]["model"] == LITE_MODEL
    assert "thinking_config" in attempts[1]["config"]


def _make_text_service(monkeypatch) -> tuple[GeminiService, list[dict]]:
    monkeypatch.setattr(gemini_service, "_jitter", lambda value: 0)
    monkeypatch.setattr(
        gemini_service,
        "_resolve_reasoning_controls",
        lambda *args, **kwargs: {"thinking_budget": None, "thinking_level": "high"},
    )
    monkeypatch.setattr(gemini_service, "_supports_thinking_level", lambda model: True)
    monkeypatch.setattr(gemini_service, "_supports_thinking_budget", lambda model: False)
    monkeypatch.setattr(
        gemini_service,
        "build_optional_thinking_config",
        lambda kwargs, **_: dict(kwargs),
    )
    monkeypatch.setattr(
        gemini_service,
        "_build_native_generate_config",
        lambda config_kwargs, operation: dict(config_kwargs),
    )

    attempts: list[dict] = []

    def fake_generate_content(*, model, contents, config):
        attempts.append({"model": model, "config": config})
        return MagicMock(name="response")

    svc = GeminiService.__new__(GeminiService)
    svc.model = LITE_MODEL
    svc._client = MagicMock()
    svc._client.models.generate_content = fake_generate_content
    return svc, attempts


@pytest.mark.asyncio
async def test_generate_text_native_drops_thinking_then_falls_back_model(monkeypatch):
    monkeypatch.setattr(gemini_service, "GEMINI_STANDARD_MODEL", FALLBACK_MODEL)
    monkeypatch.setattr(gemini_service, "GEMINI_HEAVY_MODEL", "gemini-test-heavy")
    svc, attempts = _make_text_service(monkeypatch)

    empty_text = RuntimeError("Gemini returned empty text")
    outcomes = [empty_text, empty_text, ("hello", 1, 2)]

    def fake_require_non_empty(resp, source):
        outcome = outcomes.pop(0)
        if isinstance(outcome, Exception):
            raise outcome
        return outcome[0]

    monkeypatch.setattr(
        gemini_service, "_require_non_empty_text_with_diagnostics", fake_require_non_empty
    )
    monkeypatch.setattr(
        gemini_service, "_extract_generation_usage", lambda resp: (1, 2)
    )

    result = await svc._generate_text_native_with_usage(
        "prompt",
        temperature=0.2,
        max_tokens=128,
        timeout_s=30.0,
        service_tier=None,
    )

    assert result.text == "hello"
    assert len(attempts) == 3
    assert attempts[0]["model"] == LITE_MODEL
    assert "thinking_config" in attempts[0]["config"]
    assert attempts[1]["model"] == LITE_MODEL
    assert attempts[1]["config"]["thinking_config"] == {"thinking_level": "MINIMAL"}
    assert attempts[2]["model"] == FALLBACK_MODEL
    assert attempts[2]["config"]["thinking_config"] == {"thinking_level": "MINIMAL"}


@pytest.mark.asyncio
async def test_generate_text_native_drops_thinking_on_empty_text(monkeypatch):
    svc, attempts = _make_text_service(monkeypatch)

    outcomes = [RuntimeError("Gemini returned empty text"), ("hello", 1, 2)]

    def fake_require_non_empty(resp, source):
        outcome = outcomes.pop(0)
        if isinstance(outcome, Exception):
            raise outcome
        return outcome[0]

    monkeypatch.setattr(
        gemini_service, "_require_non_empty_text_with_diagnostics", fake_require_non_empty
    )
    monkeypatch.setattr(
        gemini_service, "_extract_generation_usage", lambda resp: (1, 2)
    )

    result = await svc._generate_text_native_with_usage(
        "prompt",
        temperature=0.2,
        max_tokens=128,
        timeout_s=30.0,
        service_tier=None,
    )

    assert result.text == "hello"
    assert len(attempts) == 2
    assert "thinking_config" in attempts[0]["config"]
    assert attempts[1]["config"]["thinking_config"] == {"thinking_level": "MINIMAL"}
