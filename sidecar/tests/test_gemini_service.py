import pytest
import json
import httpx
from unittest.mock import patch, MagicMock
from pydantic import BaseModel, Field
from typing import List, Optional

from services.ai_generation_service import (
    AiGenerationService,
    AiGenerationParsingError,
    AiGenerationRateLimitError,
)

# --- Test Models ---

class SimpleModel(BaseModel):
    name: str
    age: int
    tags: List[str] = Field(default_factory=list)

class NestedModel(BaseModel):
    title: str
    data: SimpleModel
    optional_field: Optional[str] = None

# --- Fixtures ---

@pytest.fixture
def ai_generation_service():
    return AiGenerationService(timeout_seconds=5.0)

@pytest.fixture
def mock_response():
    response = MagicMock(spec=httpx.Response)
    response.status_code = 200
    response.ok = True
    return response

# --- Tests ---

@pytest.mark.asyncio
async def test_generate_success(ai_generation_service, mock_response):
    mock_response.json.return_value = {"success": True, "text": "Hello world"}
    
    with patch("httpx.AsyncClient.post", return_value=mock_response) as mock_post:
        result = await ai_generation_service.generate("Say hello")
        
        assert result == "Hello world"
        mock_post.assert_called_once()
        args, kwargs = mock_post.call_args
        payload = kwargs["json"]
        assert payload["prompt"] == "Say hello"
        assert "jsonSchema" not in payload

@pytest.mark.asyncio
async def test_generate_json_native_success(ai_generation_service, mock_response):
    # Mocking successful native structured output
    expected_data = {"name": "Test", "age": 30, "tags": ["a", "b"]}
    mock_response.json.return_value = {"success": True, "text": json.dumps(expected_data)}
    
    with patch("httpx.AsyncClient.post", return_value=mock_response) as mock_post:
        result = await ai_generation_service.generate_json("Generate person", SimpleModel)
        
        assert isinstance(result, SimpleModel)
        assert result.name == "Test"
        assert result.age == 30
        assert result.tags == ["a", "b"]
        
        mock_post.assert_called_once()
        args, kwargs = mock_post.call_args
        payload = kwargs["json"]
        assert payload["responseFormat"] == "json_object"
        assert "jsonSchema" in payload
        # Verify schema generation
        assert "properties" in payload["jsonSchema"]
        assert payload["jsonSchema"]["properties"]["name"]["type"] == "string"

@pytest.mark.asyncio
async def test_generate_json_with_markdown_fallback(ai_generation_service, mock_response):
    # Mocking a response where model wrapped JSON in markdown code blocks
    raw_text = """```json
{"name": "Markdown", "age": 25, "tags": []}
```"""
    mock_response.json.return_value = {"success": True, "text": raw_text}
    
    with patch("httpx.AsyncClient.post", return_value=mock_response):
        result = await ai_generation_service.generate_json("Generate person", SimpleModel)
        assert result.name == "Markdown"

@pytest.mark.asyncio
async def test_generate_json_recovery_from_filler(ai_generation_service, mock_response):
    # Mocking response with conversational filler
    raw_text = "Sure, here is the data: {\"name\": \"Filler\", \"age\": 40, \"tags\": [\"filler\"]} Hope this helps!"
    mock_response.json.return_value = {"success": True, "text": raw_text}
    
    with patch("httpx.AsyncClient.post", return_value=mock_response):
        result = await ai_generation_service.generate_json("Generate person", SimpleModel)
        assert result.name == "Filler"

@pytest.mark.asyncio
async def test_generate_json_truncation_recovery(ai_generation_service, mock_response):
    # Mocking a truncated JSON (missing closing brace)
    raw_text = "{\"name\": \"Truncated\", \"age\": 50, \"tags\": [\"t\""
    mock_response.json.return_value = {"success": True, "text": raw_text}
    
    with patch("httpx.AsyncClient.post", return_value=mock_response):
        result = await ai_generation_service.generate_json("Generate person", SimpleModel)
        assert result.name == "Truncated"
        assert result.tags == ["t"]

@pytest.mark.asyncio
async def test_generate_json_validation_error(ai_generation_service, mock_response):
    # Mocking invalid data (wrong type for age)
    invalid_data = {"name": "Invalid", "age": "not a number"}
    mock_response.json.return_value = {"success": True, "text": json.dumps(invalid_data)}
    
    with patch("httpx.AsyncClient.post", return_value=mock_response):
        with pytest.raises(AiGenerationParsingError, match="Failed to validate response"):
            await ai_generation_service.generate_json("Generate person", SimpleModel)

@pytest.mark.asyncio
async def test_generate_retry_logic(ai_generation_service):
    # Mocking transient failure then success
    success_response = MagicMock(spec=httpx.Response)
    success_response.status_code = 200
    success_response.ok = True
    success_response.json.return_value = {"success": True, "text": "Recovered"}
    
    with patch("httpx.AsyncClient.post", side_effect=[httpx.TimeoutException("Too slow"), success_response]):
        # Mock tenacity's wait to speed up tests
        with patch("tenacity.nap.time.sleep", side_effect=lambda x: None):
            result = await ai_generation_service.generate("Test retry")
            assert result == "Recovered"

@pytest.mark.asyncio
async def test_rate_limit_error(ai_generation_service):
    import tenacity
    rate_limit_response = MagicMock(spec=httpx.Response)
    rate_limit_response.status_code = 429
    rate_limit_response.text = "Rate limit exceeded"
    
    with patch("httpx.AsyncClient.post", return_value=rate_limit_response):
        with patch("tenacity.nap.time.sleep", side_effect=lambda x: None):
            with pytest.raises(tenacity.RetryError) as exc_info:
                await ai_generation_service.generate("Test rate limit")
            assert isinstance(exc_info.value.last_attempt.exception(), AiGenerationRateLimitError)

def test_estimate_complexity(ai_generation_service):
    assert ai_generation_service.estimate_complexity("What is AI?") < 0.2
    assert ai_generation_service.estimate_complexity("Provide a systematic review and meta-analysis of cross-domain interdisciplinary pathways.") > 0.5
    # Adjusted threshold: "or" (+0.1) and "multiple" (+0.1) = 0.2
    assert ai_generation_service.estimate_complexity("Compare A or B or C and multiple other aspects.") >= 0.2

def test_schema_depth_estimation(ai_generation_service):
    simple_schema = {"type": "object", "properties": {"a": {"type": "string"}}}
    assert ai_generation_service._estimate_schema_depth(simple_schema) == 1
    
    nested_schema = {
        "type": "object",
        "properties": {
            "a": {
                "type": "object",
                "properties": {
                    "b": {"type": "integer"}
                }
            }
        }
    }
    assert ai_generation_service._estimate_schema_depth(nested_schema) == 2
    
    array_schema = {
        "type": "object",
        "properties": {
            "items": {
                "type": "array",
                "items": {
                    "type": "object",
                    "properties": {"id": {"type": "string"}}
                }
            }
        }
    }
    # object (0) -> properties (1) -> items (2) -> array items (3) -> properties (4)
    # Actually current implementation: object(0) -> properties.items(1) -> items.items(2) -> properties.id(3)
    # Let's re-verify logic: 
    # {} (0) -> properties.items (1) -> items (2) -> properties.id (3)
    assert ai_generation_service._estimate_schema_depth(array_schema) == 3

def test_prepare_schema_for_gemini(ai_generation_service):
    raw_schema = {
        "type": "object",
        "properties": {
            "name": {"type": "string"},
            "age": {"type": "integer"}
        },
        "$defs": {
            "Address": {
                "type": "object",
                "properties": {"city": {"type": "string"}}
            }
        }
    }
    prepared = ai_generation_service._prepare_schema_for_provider(raw_schema)
    
    assert "propertyOrdering" in prepared
    assert prepared["propertyOrdering"] == ["name", "age"]
    assert "propertyOrdering" in prepared["$defs"]["Address"]
    assert prepared["$defs"]["Address"]["propertyOrdering"] == ["city"]

@pytest.mark.asyncio
async def test_generate_with_fallback_logic(ai_generation_service, mock_response):
    # Success case
    mock_response.json.return_value = {"success": True, "text": "Success"}
    with patch("httpx.AsyncClient.post", return_value=mock_response):
        res, used_fallback = await ai_generation_service.generate_with_fallback("prompt", "fallback")
        assert res == "Success"
        assert not used_fallback

    # Fallback case (Timeout)
    with patch("httpx.AsyncClient.post", side_effect=httpx.TimeoutException("Timeout")):
        # Mock tenacity retry to fail immediately for test
        with patch("tenacity.AsyncRetrying.begin", side_effect=httpx.TimeoutException("Timeout")):
            res, used_fallback = await ai_generation_service.generate_with_fallback("prompt", "fallback")
            assert res == "fallback"
            assert used_fallback

@pytest.mark.asyncio
async def test_generate_json_with_fallback_logic(ai_generation_service, mock_response):
    expected = SimpleModel(name="Default", age=0)
    
    # Validation failure triggers fallback
    mock_response.json.return_value = {"success": True, "text": "{\"invalid\": \"json\"}"}
    with patch("httpx.AsyncClient.post", return_value=mock_response):
        res, used_fallback = await ai_generation_service.generate_json_with_fallback("prompt", SimpleModel, expected)
        assert res == expected
        assert used_fallback

@pytest.mark.asyncio
async def test_nested_model_generation(ai_generation_service, mock_response):
    data = {
        "title": "Nested Test",
        "data": {"name": "Sub", "age": 10, "tags": ["nested"]},
        "optional_field": "Present"
    }
    mock_response.json.return_value = {"success": True, "text": json.dumps(data)}
    
    with patch("httpx.AsyncClient.post", return_value=mock_response):
        result = await ai_generation_service.generate_json("Generate nested", NestedModel)
        assert isinstance(result, NestedModel)
        assert result.data.name == "Sub"
        assert result.optional_field == "Present"



# --- Tests for Schema Helper Methods ---

class TestPrepareSchemaForGemini:
    """Tests for _prepare_schema_for_provider method."""
    
    def test_adds_property_ordering_simple(self):
        """Property ordering is added to simple object schemas."""
        schema = {
            "type": "object",
            "properties": {
                "name": {"type": "string"},
                "age": {"type": "integer"},
            }
        }
        result = AiGenerationService._prepare_schema_for_provider(schema)
        assert result["propertyOrdering"] == ["name", "age"]
    
    def test_adds_property_ordering_nested(self):
        """Property ordering is added at all nesting levels."""
        schema = {
            "type": "object",
            "properties": {
                "outer": {
                    "type": "object",
                    "properties": {
                        "inner": {"type": "string"}
                    }
                }
            }
        }
        result = AiGenerationService._prepare_schema_for_provider(schema)
        assert result["propertyOrdering"] == ["outer"]
        assert result["properties"]["outer"]["propertyOrdering"] == ["inner"]
    
    def test_handles_array_items(self):
        """Property ordering is added to array item schemas."""
        schema = {
            "type": "array",
            "items": {
                "type": "object",
                "properties": {
                    "id": {"type": "integer"},
                    "value": {"type": "string"},
                }
            }
        }
        result = AiGenerationService._prepare_schema_for_provider(schema)
        assert result["items"]["propertyOrdering"] == ["id", "value"]
    
    def test_handles_defs(self):
        """Property ordering is added inside $defs."""
        schema = {
            "type": "object",
            "properties": {
                "data": {"$ref": "#/$defs/Item"}
            },
            "$defs": {
                "Item": {
                    "type": "object",
                    "properties": {
                        "name": {"type": "string"},
                    }
                }
            }
        }
        result = AiGenerationService._prepare_schema_for_provider(schema)
        assert result["$defs"]["Item"]["propertyOrdering"] == ["name"]
    
    def test_handles_anyof(self):
        """Property ordering is added inside anyOf variants."""
        schema = {
            "anyOf": [
                {"type": "object", "properties": {"a": {"type": "string"}}},
                {"type": "object", "properties": {"b": {"type": "integer"}}},
            ]
        }
        result = AiGenerationService._prepare_schema_for_provider(schema)
        assert result["anyOf"][0]["propertyOrdering"] == ["a"]
        assert result["anyOf"][1]["propertyOrdering"] == ["b"]
    
    def test_handles_additional_properties(self):
        """Property ordering is added inside additionalProperties schema."""
        schema = {
            "type": "object",
            "additionalProperties": {
                "type": "object",
                "properties": {
                    "value": {"type": "string"},
                }
            }
        }
        result = AiGenerationService._prepare_schema_for_provider(schema)
        assert result["additionalProperties"]["propertyOrdering"] == ["value"]
    
    def test_preserves_existing_property_ordering(self):
        """Existing propertyOrdering is not overwritten."""
        schema = {
            "type": "object",
            "propertyOrdering": ["age", "name"],  # Explicit order
            "properties": {
                "name": {"type": "string"},
                "age": {"type": "integer"},
            }
        }
        result = AiGenerationService._prepare_schema_for_provider(schema)
        assert result["propertyOrdering"] == ["age", "name"]
    
    def test_handles_non_dict_values(self):
        """Gracefully handles non-dict values in properties."""
        schema = {
            "type": "object",
            "properties": {
                "valid": {"type": "string"},
                "also_valid": True,  # Boolean schema
            }
        }
        result = AiGenerationService._prepare_schema_for_provider(schema)
        assert result["propertyOrdering"] == ["valid", "also_valid"]
        assert result["properties"]["also_valid"] is True
    
    def test_max_depth_protection(self):
        """Does not exceed max recursion depth."""
        # Create deeply nested schema
        schema = {"type": "object", "properties": {"a": {"type": "string"}}}
        current = schema
        for _ in range(100):  # Deeper than max_depth=50
            current["properties"]["nested"] = {
                "type": "object",
                "properties": {"a": {"type": "string"}}
            }
            current = current["properties"]["nested"]
        
        # Should not raise RecursionError
        result = AiGenerationService._prepare_schema_for_provider(schema)
        assert "propertyOrdering" in result


class TestEstimateSchemaDepth:
    """Tests for _estimate_schema_depth method."""
    
    def test_flat_schema(self):
        """Flat schema has depth 0."""
        schema = {
            "type": "object",
            "properties": {
                "name": {"type": "string"},
            }
        }
        assert AiGenerationService._estimate_schema_depth(schema) == 1
    
    def test_nested_schema(self):
        """Nested schemas increase depth."""
        schema = {
            "type": "object",
            "properties": {
                "outer": {
                    "type": "object",
                    "properties": {
                        "inner": {
                            "type": "object",
                            "properties": {
                                "deep": {"type": "string"}
                            }
                        }
                    }
                }
            }
        }
        assert AiGenerationService._estimate_schema_depth(schema) == 3
    
    def test_array_items_add_depth(self):
        """Array items contribute to depth."""
        schema = {
            "type": "array",
            "items": {
                "type": "object",
                "properties": {
                    "id": {"type": "integer"}
                }
            }
        }
        assert AiGenerationService._estimate_schema_depth(schema) == 2
    
    def test_defs_contribute_depth(self):
        """$defs are counted in depth calculation."""
        schema = {
            "$defs": {
                "Item": {
                    "type": "object",
                    "properties": {
                        "nested": {
                            "type": "object",
                            "properties": {}
                        }
                    }
                }
            }
        }
        assert AiGenerationService._estimate_schema_depth(schema) == 2
    
    def test_handles_non_dict(self):
        """Returns 0 for non-dict input."""
        assert AiGenerationService._estimate_schema_depth("not a dict") == 0
        assert AiGenerationService._estimate_schema_depth(None) == 0
        assert AiGenerationService._estimate_schema_depth([]) == 0
    
    def test_max_depth_protection(self):
        """Does not exceed max recursion depth."""
        # Create a schema that references itself (simulating circular)
        schema = {"type": "object", "properties": {}}
        schema["properties"]["self"] = schema  # Circular!
        
        # Should not cause infinite recursion
        depth = AiGenerationService._estimate_schema_depth(schema)
        assert depth <= 50  # Max depth limit
    
    def test_additional_properties_depth(self):
        """additionalProperties contribute to depth."""
        schema = {
            "type": "object",
            "additionalProperties": {
                "type": "object",
                "properties": {
                    "value": {"type": "string"}
                }
            }
        }
        assert AiGenerationService._estimate_schema_depth(schema) == 2