import asyncio
import json
from unittest.mock import AsyncMock, MagicMock, patch

import grpc
import pytest

import grpc_server
from grpc_server import LLMServiceServicer, _abort_with_typed_error
from proto import llm_v1_pb2 as llm_pb2


class AbortCalled(RuntimeError):
    def __init__(self, status_code, details):
        super().__init__(details)
        self.status_code = status_code
        self.details = details


class DummyContext:
    async def abort(self, status_code, details):
        raise AbortCalled(status_code, details)


@pytest.mark.asyncio
async def test_abort_with_typed_error_uses_standard_envelope():
    context = DummyContext()

    with pytest.raises(AbortCalled) as exc_info:
        await _abort_with_typed_error(
            context,
            grpc.StatusCode.INVALID_ARGUMENT,
            'INVALID_PROMPT',
            'prompt is required',
            'trace-1',
            400,
            {'field': 'prompt'},
        )

    payload = json.loads(exc_info.value.details)
    assert payload['ok'] is False
    assert payload['traceId'] == 'trace-1'
    assert payload['error']['code'] == 'INVALID_PROMPT'
    assert payload['error']['details']['field'] == 'prompt'


@pytest.mark.asyncio
async def test_generate_rejects_empty_prompt():
    servicer = LLMServiceServicer()
    request = llm_pb2.GenerateRequest(prompt='   ')
    context = DummyContext()

    with pytest.raises(AbortCalled) as exc_info:
        await servicer.Generate(request, context)

    assert exc_info.value.status_code == grpc.StatusCode.INVALID_ARGUMENT
    payload = json.loads(exc_info.value.details)
    assert payload['error']['code'] == 'INVALID_PROMPT'


@pytest.mark.asyncio
async def test_generate_stream_returns_multiple_chunks():
    servicer = LLMServiceServicer()
    request = llm_pb2.GenerateRequest(prompt='describe the experiment')
    context = DummyContext()

    async def mock_stream(*args, **kwargs):
        yield "Chunk 1"
        yield "Chunk 2"

    with patch('grpc_server.GeminiService.generate_stream', side_effect=mock_stream):
        chunks = [chunk async for chunk in servicer.GenerateStream(request, context)]

    assert len(chunks) == 3 # 2 data chunks + 1 final empty chunk
    assert chunks[0].delta == "Chunk 1"
    assert chunks[1].delta == "Chunk 2"
    assert chunks[2].done is True
    assert chunks[2].finish_reason == 'stop'


@pytest.mark.asyncio
async def test_structured_output_rejects_invalid_schema():
    servicer = LLMServiceServicer()
    request = llm_pb2.StructuredRequest(prompt='return json', json_schema='{bad')
    context = DummyContext()

    with pytest.raises(AbortCalled) as exc_info:
        await servicer.StructuredOutput(request, context)

    assert exc_info.value.status_code == grpc.StatusCode.INVALID_ARGUMENT
    payload = json.loads(exc_info.value.details)
    assert payload['error']['code'] == 'INVALID_JSON_SCHEMA'


@pytest.mark.asyncio
async def test_structured_output_serializes_mapping_response():
    servicer = LLMServiceServicer()
    request = llm_pb2.StructuredRequest(prompt='return json', json_schema='{"type":"object"}')
    context = DummyContext()

    with patch('grpc_server.GeminiService.generate_structured', new_callable=AsyncMock) as mock_generate:
        mock_generate.return_value = {'answer': '42'}
        response = await servicer.StructuredOutput(request, context)

    assert json.loads(response.json_result) == {'answer': '42'}
    assert response.schema_valid is True


@pytest.mark.asyncio
async def test_serve_async_registers_llm_service():
    mock_server = MagicMock()
    mock_server.start = AsyncMock()
    mock_server.add_insecure_port = MagicMock(return_value=50052)
    mock_server.wait_for_termination = AsyncMock(side_effect=asyncio.CancelledError())

    with patch('grpc.aio.server', return_value=mock_server) as mock_server_factory:
        with patch('grpc_server.llm_pb2_grpc.add_LLMServiceServicer_to_server') as mock_register:
            with pytest.raises(asyncio.CancelledError):
                await grpc_server.serve_async()

    mock_server_factory.assert_called_once()
    mock_register.assert_called_once()
