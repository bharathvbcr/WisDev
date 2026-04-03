from unittest.mock import patch

from telemetry import make_grpc_server_interceptor


def test_make_grpc_server_interceptor_uses_asyncio_interceptor():
    sentinel = object()

    with patch(
        "opentelemetry.instrumentation.grpc.aio_server_interceptor",
        return_value=sentinel,
    ) as mock_aio_interceptor:
        interceptor = make_grpc_server_interceptor()

    assert interceptor is sentinel
    mock_aio_interceptor.assert_called_once_with()
