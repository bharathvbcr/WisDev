import json
from unittest.mock import AsyncMock, patch

import grpc
import pytest

from grpc_server import LLMServiceServicer
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
async def test_generate_stream_splits_large_text():
    servicer = LLMServiceServicer()
    request = llm_pb2.GenerateRequest(prompt='Explain CRISPR in detail.')
    context = DummyContext()
    
    async def mock_stream(*args, **kwargs):
        yield "Part 1"
        yield "Part 2"

    with patch('grpc_server.GeminiService.generate_stream', side_effect=mock_stream):
        chunks = [chunk async for chunk in servicer.GenerateStream(request, context)]

    assert len(chunks) == 3
    assert chunks[0].delta == "Part 1"
    assert chunks[1].delta == "Part 2"
    assert all(not chunk.done for chunk in chunks[:-1])
    assert chunks[-1].done is True
    assert chunks[-1].finish_reason == 'stop'


@pytest.mark.asyncio
async def test_generate_rejects_empty_model_text():
    servicer = LLMServiceServicer()
    request = llm_pb2.GenerateRequest(prompt='Rank these search results.')
    context = DummyContext()

    with patch('grpc_server.GeminiService.generate_text', new_callable=AsyncMock) as mock_generate:
        mock_generate.return_value = '   '
        with pytest.raises(AbortCalled) as exc_info:
            await servicer.Generate(request, context)

    assert exc_info.value.status_code == grpc.StatusCode.INTERNAL
    payload = json.loads(exc_info.value.details)
    assert payload['ok'] is False
    assert payload['error']['code'] == 'GENERATE_FAILED'
    assert 'empty text response' in payload['error']['message']


@pytest.mark.asyncio
async def test_structured_output_serializes_mapping_results():
    servicer = LLMServiceServicer()
    request = llm_pb2.StructuredRequest(prompt='Return JSON.', json_schema='{"type":"object"}')
    context = DummyContext()

    with patch('grpc_server.GeminiService.generate_structured', new_callable=AsyncMock) as mock_generate:
        mock_generate.return_value = {'answer': 42}
        response = await servicer.StructuredOutput(request, context)

    assert response.schema_valid is True
    assert json.loads(response.json_result) == {'answer': 42}


@pytest.mark.asyncio
async def test_structured_output_rejects_invalid_schema():
    servicer = LLMServiceServicer()
    request = llm_pb2.StructuredRequest(prompt='Return JSON.', json_schema='{invalid')
    context = DummyContext()

    with pytest.raises(AbortCalled) as exc_info:
        await servicer.StructuredOutput(request, context)

    assert exc_info.value.status_code == grpc.StatusCode.INVALID_ARGUMENT
    payload = json.loads(exc_info.value.details)
    assert payload['ok'] is False
    assert payload['error']['code'] == 'INVALID_JSON_SCHEMA'
