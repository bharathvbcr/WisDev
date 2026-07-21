import asyncio

from services.gemini_service import _exception_log_message


def test_exception_log_message_names_empty_timeout() -> None:
    assert _exception_log_message(asyncio.TimeoutError()) == "asyncio.TimeoutError"


def test_exception_log_message_preserves_exception_message() -> None:
    assert _exception_log_message(RuntimeError("upstream failed")) == "upstream failed"


def test_exception_log_message_falls_back_to_type_name() -> None:
    class EmptyMessageError(Exception):
        def __str__(self) -> str:
            return ""

    assert _exception_log_message(EmptyMessageError()) == "EmptyMessageError"
