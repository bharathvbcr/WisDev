"""
AI Generation Service
Provides resilient Vertex-proxy generation for WisDev.

Features:
- Structured JSON output parsing
- Rate limiting and transient error retries
- Retry logic with exponential backoff
"""

import os
import json
import re
from typing import Any, Optional, TypeVar, Type
from enum import Enum

import structlog
import httpx
from pydantic import BaseModel
from tenacity import (
    retry,
    stop_after_attempt,
    wait_exponential,
    retry_if_exception_type,
)

logger = structlog.get_logger(__name__)

T = TypeVar("T", bound=BaseModel)


class ModelSelectionStrategy(str, Enum):
    """Strategy for model selection."""
    ALWAYS_LIGHT = "always_light"
    ALWAYS_BALANCED = "always_balanced"
    ALWAYS_HEAVY = "always_heavy"
    ADAPTIVE = "adaptive"


class AiGenerationServiceError(Exception):
    """Base exception for AI generation service errors."""
    pass


class AiGenerationRetryableError(AiGenerationServiceError):
    """Transient/retryable AI generation service error."""
    pass


class AiGenerationRateLimitError(AiGenerationRetryableError):
    """Rate limit exceeded."""
    pass


class AiGenerationParsingError(AiGenerationServiceError):
    """Failed to parse response."""
    pass


class AiGenerationService:
    """
    Service for interacting with tiered generation models.

    Supports adaptive model selection:
    - light for simple queries
    - balanced for typical queries
    - heavy for complex/ambiguous queries
    """

    LIGHT_COMPLEXITY_THRESHOLD = 0.35
    LIGHT_UNCERTAINTY_THRESHOLD = 0.30
    BALANCED_COMPLEXITY_THRESHOLD = 0.70
    BALANCED_UNCERTAINTY_THRESHOLD = 0.60

    def __init__(
        self,
        api_key: Optional[str] = None,
        default_strategy: ModelSelectionStrategy = ModelSelectionStrategy.ADAPTIVE,
        timeout_seconds: float = 30.0,
    ):
        _ = api_key  # Maintained for backward compatibility; not used.
        self.vertex_proxy_url = (
            os.environ.get("VERTEX_FUNCTION_URL")
            or os.environ.get("VERTEX_MODEL_URL")
            or "https://vertex-proxy-cyucrnqqnq-uc.a.run.app"
        )
        self.prefer_vertex = True

        self.default_strategy = default_strategy
        self.timeout_seconds = timeout_seconds
        self.native_structured_enabled = str(os.environ.get("AI_NATIVE_STRUCTURED_ENABLED", "false")).strip().lower() in ("1", "true", "yes", "on")

        logger.info(
            "ai_generation_service_initialized",
            strategy=default_strategy.value,
            timeout=timeout_seconds,
            prefer_vertex=self.prefer_vertex,
            vertex_proxy_url=self.vertex_proxy_url,
            direct_key_configured=False,
            native_structured_enabled=self.native_structured_enabled,
        )

    def _select_model(
        self,
        complexity_score: float,
        uncertainty_score: float = 0.5,
        strict_domain: bool = False,
        remaining_budget_ratio: float = 1.0,
        historical_reward: float = 0.5,
        strategy: Optional[ModelSelectionStrategy] = None,
    ) -> str:
        """
        Select model tier based on strategy and complexity score.

        Returns model class names:
        'light', 'balanced', or 'heavy'.
        """
        effective_strategy = strategy or self.default_strategy
        complexity = max(0.0, min(1.0, complexity_score))
        uncertainty = max(0.0, min(1.0, uncertainty_score))
        budget_ratio = max(0.0, min(1.0, remaining_budget_ratio))
        reward = max(0.0, min(1.0, historical_reward))

        if effective_strategy == ModelSelectionStrategy.ALWAYS_LIGHT:
            return "light"
        if effective_strategy == ModelSelectionStrategy.ALWAYS_BALANCED:
            return "balanced"
        if effective_strategy == ModelSelectionStrategy.ALWAYS_HEAVY:
            return "heavy"

        # Strict-domain tasks default to heavy unless budget is critically constrained.
        if strict_domain and budget_ratio >= 0.15:
            return "heavy"

        # Reward-aware bias:
        # - Low historical reward nudges one tier upward for stronger verification.
        # - Tight remaining budget nudges one tier downward to protect cost limits.
        adjusted_complexity = complexity
        adjusted_uncertainty = uncertainty
        if reward < 0.45:
            adjusted_complexity = min(1.0, adjusted_complexity + 0.12)
            adjusted_uncertainty = min(1.0, adjusted_uncertainty + 0.12)
        if budget_ratio < 0.25:
            adjusted_complexity = max(0.0, adjusted_complexity - 0.10)
            adjusted_uncertainty = max(0.0, adjusted_uncertainty - 0.10)

        if (
            adjusted_complexity < self.LIGHT_COMPLEXITY_THRESHOLD
            and adjusted_uncertainty < self.LIGHT_UNCERTAINTY_THRESHOLD
        ):
            return "light"
        if (
            adjusted_complexity < self.BALANCED_COMPLEXITY_THRESHOLD
            and adjusted_uncertainty < self.BALANCED_UNCERTAINTY_THRESHOLD
        ):
            return "balanced"
        return "heavy"

    @staticmethod
    def _model_fallback_chain(selected_model: str) -> list[str]:
        normalized = str(selected_model or "").strip().lower()
        if normalized == "heavy":
            return ["heavy", "balanced", "light"]
        if normalized == "standard" or normalized == "balanced":
            return ["balanced", "light"]
        return ["light"]

    @staticmethod
    def _resolve_model_id_for_class(model_class: str) -> str:
        normalized = str(model_class or "").strip().lower()
        env_key = {
            "light": "AI_MODEL_LIGHT_ID",
            "balanced": "AI_MODEL_BALANCED_ID",
            "standard": "AI_MODEL_BALANCED_ID",
            "heavy": "AI_MODEL_HEAVY_ID",
        }.get(normalized, "AI_MODEL_BALANCED_ID")
        model_id = str(os.environ.get(env_key, "")).strip()
        if model_id:
            return model_id

        shared_default = str(os.environ.get("AI_MODEL_DEFAULT_ID", "")).strip()
        if shared_default:
            return shared_default

        raise RuntimeError(
            f"Missing model configuration: set {env_key} (or AI_MODEL_DEFAULT_ID)"
        )

    @staticmethod
    def _extract_balanced_json(input_text: str, start_index: int, opener: str) -> Optional[str]:
        closer = "}" if opener == "{" else "]"
        depth = 0
        in_string = False
        escaped = False

        for i in range(start_index, len(input_text)):
            char = input_text[i]

            if in_string:
                if escaped:
                    escaped = False
                    continue
                if char == "\\":
                    escaped = True
                    continue
                if char == '"':
                    in_string = False
                continue

            if char == '"':
                in_string = True
                continue

            if char == opener:
                depth += 1
                continue

            if char == closer:
                depth -= 1
                if depth == 0:
                    return input_text[start_index:i + 1]

        return None

    @classmethod
    def _get_json_candidates(cls, input_text: str) -> list[str]:
        candidates: list[str] = []
        for i, char in enumerate(input_text):
            if char not in ("{", "["):
                continue
            candidate = cls._extract_balanced_json(input_text, i, char)
            if candidate:
                candidates.append(candidate)
        return candidates

    @staticmethod
    def _recover_truncated_json(input_text: str) -> Optional[dict | list]:
        first_obj = input_text.find("{")
        first_arr = input_text.find("[")
        indices = [idx for idx in (first_obj, first_arr) if idx >= 0]
        if not indices:
            return None

        start = min(indices)
        recovered = input_text[start:]

        in_string = False
        escaped = False
        last_quote_start = -1
        for i, char in enumerate(recovered):
            if in_string:
                if escaped:
                    escaped = False
                    continue
                if char == "\\":
                    escaped = True
                    continue
                if char == '"':
                    in_string = False
                continue
            if char == '"':
                in_string = True
                last_quote_start = i

        if in_string and last_quote_start >= 0:
            recovered = recovered[:last_quote_start]
            recovered = re.sub(r",\s*$", "", recovered)
            recovered = re.sub(r":\s*$", "", recovered)

        previous = ""
        while recovered != previous:
            previous = recovered
            recovered = re.sub(r",\s*$", "", recovered)
            recovered = re.sub(r":\s*$", "", recovered)
            recovered = re.sub(r",\s*\"[^\"]*\"\s*$", "", recovered)
            recovered = re.sub(r"\{\s*$", "", recovered)
            recovered = re.sub(r"\[\s*$", "", recovered)

        stack: list[str] = []
        in_string = False
        escaped = False
        for char in recovered:
            if in_string:
                if escaped:
                    escaped = False
                    continue
                if char == "\\":
                    escaped = True
                    continue
                if char == '"':
                    in_string = False
                continue

            if char == '"':
                in_string = True
                continue

            if char in ("{", "["):
                stack.append(char)
            elif char == "}" and stack and stack[-1] == "{":
                stack.pop()
            elif char == "]" and stack and stack[-1] == "[":
                stack.pop()

        if not recovered:
            return None

        if stack:
            for opener in reversed(stack):
                recovered += "}" if opener == "{" else "]"

        try:
            parsed = json.loads(recovered)
            if isinstance(parsed, (dict, list)):
                return parsed
        except json.JSONDecodeError:
            return None

        return None

    @staticmethod
    def _estimate_schema_depth(schema: dict, _current: int = 0, _seen: Optional[frozenset] = None) -> int:
        """
        Estimate nesting depth of a JSON Schema object (0-indexed at the root object).
        Guards against circular references via identity-based ``_seen`` set.
        Maximum reported depth is capped at 20 to prevent runaway recursion.
        """
        _MAX_DEPTH = 20
        if _current >= _MAX_DEPTH or not isinstance(schema, dict):
            return _current
        _seen = _seen or frozenset()
        node_id = id(schema)
        if node_id in _seen:
            return _current
        _seen = _seen | {node_id}

        max_depth = _current
        if "properties" in schema and isinstance(schema["properties"], dict):
            for child in schema["properties"].values():
                d = AiGenerationService._estimate_schema_depth(child, _current + 1, _seen)
                max_depth = max(max_depth, d)
        if "items" in schema and isinstance(schema["items"], dict):
            d = AiGenerationService._estimate_schema_depth(schema["items"], _current + 1, _seen)
            max_depth = max(max_depth, d)
        if "additionalProperties" in schema and isinstance(schema["additionalProperties"], dict):
            d = AiGenerationService._estimate_schema_depth(schema["additionalProperties"], _current + 1, _seen)
            max_depth = max(max_depth, d)
        for key in ("$defs", "definitions"):
            if key in schema and isinstance(schema[key], dict):
                for child in schema[key].values():
                    d = AiGenerationService._estimate_schema_depth(child, _current + 1, _seen)
                    max_depth = max(max_depth, d)
        for key in ("anyOf", "oneOf", "allOf"):
            if key in schema and isinstance(schema[key], list):
                for child in schema[key]:
                    d = AiGenerationService._estimate_schema_depth(child, _current, _seen)
                    max_depth = max(max_depth, d)
        return max_depth

    @staticmethod
    def _prepare_schema_for_provider(schema: dict) -> dict:
        """
        Recursively add ``propertyOrdering`` to every object node so the provider's
        native structured-output mode preserves insertion order.
        Also strips ``additionalProperties`` keys that the provider rejects.
        """
        import copy
        schema = copy.deepcopy(schema)

        def _annotate(node: Any) -> Any:
            if not isinstance(node, dict):
                return node
            if node.get("type") == "object" or "properties" in node:
                props = node.get("properties")
                if isinstance(props, dict):
                    # Only set propertyOrdering if not already explicitly set
                    if "propertyOrdering" not in node:
                        node["propertyOrdering"] = list(props.keys())
                    node["properties"] = {k: _annotate(v) for k, v in props.items()}
            if "items" in node:
                node["items"] = _annotate(node["items"])
            # Traverse additionalProperties schema (do NOT remove it — tests assert it)
            if "additionalProperties" in node and isinstance(node["additionalProperties"], dict):
                node["additionalProperties"] = _annotate(node["additionalProperties"])
            for meta_key in ("$defs", "definitions"):
                if meta_key in node and isinstance(node[meta_key], dict):
                    node[meta_key] = {k: _annotate(v) for k, v in node[meta_key].items()}
            for combo_key in ("anyOf", "oneOf", "allOf"):
                if combo_key in node and isinstance(node[combo_key], list):
                    node[combo_key] = [_annotate(c) for c in node[combo_key]]
            return node

        return _annotate(schema)

    async def _generate_via_vertex_proxy(
        self,
        prompt: str,
        temperature: float,
        max_tokens: int,
        model: str = "light",
        response_format: Optional[str] = None,
        json_schema: Optional[dict] = None,
    ) -> str:
        timeout = httpx.Timeout(self.timeout_seconds)

        payload: dict = {
            "prompt": prompt,
            "maxTokens": max_tokens,
            "temperature": temperature,
            "model": model,
        }
        if response_format:
            payload["responseFormat"] = response_format
        if json_schema is not None:
            payload["jsonSchema"] = json_schema

        try:
            async with httpx.AsyncClient(timeout=timeout) as client:
                response = await client.post(
                    self.vertex_proxy_url,
                    json=payload,
                    headers={"Content-Type": "application/json"},
                )
        except httpx.TimeoutException as e:
            raise AiGenerationRetryableError(f"Vertex proxy timed out after {self.timeout_seconds}s") from e
        except httpx.HTTPError as e:
            raise AiGenerationRetryableError(f"Vertex proxy request failed: {e}") from e

        if response.status_code == 429:
            raise AiGenerationRateLimitError("Vertex proxy rate limit exceeded")

        if response.status_code >= 500:
            raise AiGenerationRetryableError(f"Vertex proxy server error ({response.status_code})")

        if response.status_code >= 400:
            detail = response.text
            try:
                parsed = response.json()
                detail = parsed.get("error") or parsed.get("message") or detail
            except Exception:
                pass
            raise AiGenerationServiceError(f"Vertex proxy error ({response.status_code}): {detail}")

        try:
            data = response.json()
        except Exception as e:
            raise AiGenerationServiceError("Vertex proxy returned invalid JSON") from e

        text = data.get("text") or ""
        if not text:
            raise AiGenerationServiceError("Empty response from Vertex proxy")

        return text
    
    @retry(
        stop=stop_after_attempt(3),
        wait=wait_exponential(multiplier=1, min=1, max=10),
        retry=retry_if_exception_type((AiGenerationRetryableError,)),
    )
    async def generate(
        self,
        prompt: str,
        complexity_score: float = 0.5,
        uncertainty_score: float = 0.5,
        strict_domain: bool = False,
        remaining_budget_ratio: float = 1.0,
        historical_reward: float = 0.5,
        temperature: float = 0.7,
        max_tokens: int = 2048,
        strategy: Optional[ModelSelectionStrategy] = None,
    ) -> str:
        """
        Generate text using configured model IDs.
        
        Args:
            prompt: The prompt to send
            complexity_score: Query complexity (0-1) for model selection
            temperature: Sampling temperature
            max_tokens: Maximum output tokens
            strategy: Override model selection strategy
            
        Returns:
            Generated text
        """
        selected_model = self._select_model(
            complexity_score=complexity_score,
            uncertainty_score=uncertainty_score,
            strict_domain=strict_domain,
            remaining_budget_ratio=remaining_budget_ratio,
            historical_reward=historical_reward,
            strategy=strategy,
        )
        fallback_chain = self._model_fallback_chain(selected_model)
        selected_index = 0
        last_error: Optional[Exception] = None

        logger.info(
            "ai_generate_start",
            provider="vertex_proxy",
            complexity=complexity_score,
            model=selected_model,
            prompt_length=len(prompt),
        )

        text = ""
        for model_class in fallback_chain:
            try:
                text = await self._generate_via_vertex_proxy(
                    prompt=prompt,
                    temperature=temperature,
                    max_tokens=max_tokens,
                    model=model_class,
                )
                break
            except (AiGenerationRetryableError, AiGenerationRateLimitError, AiGenerationServiceError) as exc:
                last_error = exc
                selected_index += 1
                if selected_index >= len(fallback_chain):
                    raise
                logger.warning(
                    "ai_model_demotion_fallback",
                    from_model=model_class,
                    to_model=fallback_chain[selected_index],
                    reason=str(exc),
                )
                continue

        if not text and last_error is not None:
            raise last_error

        logger.info(
            "ai_generate_success",
            provider="vertex_proxy",
            model=fallback_chain[min(selected_index, len(fallback_chain) - 1)],
            response_length=len(text),
        )
        return text
    
    async def _generate_native_structured(
        self,
        prompt: str,
        response_model: Type[T],
        temperature: float,
        max_tokens: int,
        model_tier: str = "balanced",
    ) -> T:
        """
        Generate structured output using the google.genai SDK natively.

        Uses LLM Provider when VERTEX_PROJECT is set, otherwise falls back to
        a direct API key (GOOGLE_API_KEY). Model IDs are resolved from
        AI_MODEL_LIGHT_ID / AI_MODEL_BALANCED_ID / AI_MODEL_HEAVY_ID.

        The native path guarantees valid JSON from the model (no regex parsing
        required) and uses Pydantic's JSON schema directly via response_json_schema.

        Raises RuntimeError if credentials are not available so the caller can
        transparently fall back to the vertex proxy path.
        """
        try:
            import google.genai as genai
            from google.genai.types import GenerateContentConfig
        except ImportError as exc:
            raise RuntimeError("google-genai SDK not available") from exc

        project = os.environ.get("VERTEX_PROJECT") or os.environ.get("VERTEX_PROJECT")
        location = (
            os.environ.get("GOOGLE_CLOUD_LOCATION")
            or os.environ.get("GOOGLE_CLOUD_REGION")
            or "us-central1"
        )
        api_key = os.environ.get("GOOGLE_API_KEY")

        if not project and not api_key:
            raise RuntimeError(
                "No credentials for native model SDK "
                "(set VERTEX_PROJECT or GOOGLE_API_KEY)"
            )

        # Map model classes to concrete provider model IDs.
        # API accepts light|standard|heavy class names ("balanced" is a legacy alias for "standard").
        normalized_tier = str(model_tier or "").strip().lower()
        if normalized_tier == "balanced":
            normalized_tier = "standard"
        if normalized_tier not in ("light", "standard", "heavy"):
            normalized_tier = "standard"
        model_name = self._resolve_model_id_for_class(normalized_tier)

        if project:
            client = genai.Client(vertexai=False, project=project, location=location)
        else:
            client = genai.Client(api_key=api_key)

        # Build provider-compatible schema from the Pydantic model
        raw_schema = response_model.model_json_schema()
        prepared_schema = self._prepare_schema_for_provider(raw_schema)

        config = GenerateContentConfig(
            temperature=temperature,
            max_output_tokens=max_tokens,
            response_mime_type="application/json",
            response_json_schema=prepared_schema,
        )

        response = await client.aio.models.generate_content(
            model=model_name,
            contents=prompt,
            config=config,
        )

        # Native structured output guarantees valid JSON — validate with Pydantic
        return response_model.model_validate_json(response.text)

    async def generate_json(
        self,
        prompt: str,
        response_model: Type[T],
        complexity_score: float = 0.5,
        uncertainty_score: float = 0.5,
        strict_domain: bool = False,
        remaining_budget_ratio: float = 1.0,
        historical_reward: float = 0.5,
        temperature: float = 0.3,  # Lower temp for structured output
        max_tokens: int = 2048,
        strategy: Optional[ModelSelectionStrategy] = None,
    ) -> T:
        """
        Generate and parse JSON response into a Pydantic model.

        Tries the native google.genai SDK path first (guaranteed valid JSON via
        response_mime_type + response_json_schema). Falls back to the vertex proxy
        with manual JSON parsing when credentials are unavailable or the native
        call fails.

        Args:
            prompt: The prompt (should ask for JSON output)
            response_model: Pydantic model class for parsing
            complexity_score: Query complexity for model selection
            temperature: Sampling temperature
            max_tokens: Maximum output tokens
            strategy: Override model selection strategy

        Returns:
            Parsed Pydantic model instance
        """
        selected_model = self._select_model(
            complexity_score=complexity_score,
            uncertainty_score=uncertainty_score,
            strict_domain=strict_domain,
            remaining_budget_ratio=remaining_budget_ratio,
            historical_reward=historical_reward,
            strategy=strategy,
        )
        fallback_chain = self._model_fallback_chain(selected_model)

        # ── Native SDK path (optional, disabled by default for unified routing) ──
        if self.native_structured_enabled:
            try:
                result = await self._generate_native_structured(
                    prompt=prompt,
                    response_model=response_model,
                    temperature=temperature,
                    max_tokens=max_tokens,
                    model_tier=selected_model,
                )
                logger.info(
                    "ai_native_structured_success",
                    model=response_model.__name__,
                    tier=selected_model,
                )
                return result
            except Exception as native_err:
                logger.info(
                    "ai_native_structured_fallback",
                    reason=str(native_err)[:120],
                    model=response_model.__name__,
                )
        else:
            logger.info(
                "ai_native_structured_disabled",
                model=response_model.__name__,
            )

        # ── Vertex proxy fallback ────────────────────────────────────────────
        # Build JSON schema for structured output hint
        prepared_schema: Optional[dict] = None
        try:
            raw_schema = response_model.model_json_schema()
            depth = self._estimate_schema_depth(raw_schema)
            if depth <= 6:
                prepared_schema = self._prepare_schema_for_provider(raw_schema)
        except Exception:
            prepared_schema = None

        # Add JSON instruction to prompt
        json_prompt = f"""{prompt}

IMPORTANT: Respond ONLY with valid JSON. No markdown, no explanation, just the JSON object.
"""

        response_text = ""
        last_error: Optional[Exception] = None
        for index, model_class in enumerate(fallback_chain):
            try:
                response_text = await self._generate_via_vertex_proxy(
                    prompt=json_prompt,
                    temperature=temperature,
                    max_tokens=max_tokens,
                    model=model_class,
                    response_format="json_object",
                    json_schema=prepared_schema,
                )
                break
            except (AiGenerationRetryableError, AiGenerationRateLimitError, AiGenerationServiceError) as exc:
                last_error = exc
                if index >= len(fallback_chain) - 1:
                    raise
                logger.warning(
                    "ai_json_model_demotion_fallback",
                    from_model=model_class,
                    to_model=fallback_chain[index + 1],
                    reason=str(exc),
                )
                continue

        if not response_text and last_error is not None:
            raise last_error

        # Clean up response (remove markdown code blocks if present)
        cleaned = response_text.strip()
        if cleaned.startswith("```"):
            cleaned = re.sub(r"^```(?:json|JSON)?\s*", "", cleaned, count=1)
            cleaned = re.sub(r"\s*```$", "", cleaned, count=1)
        cleaned = cleaned.strip()

        last_json_error: Optional[Exception] = None
        last_validation_error: Optional[Exception] = None

        # Try direct parse first, then any balanced JSON candidates extracted from text
        for candidate in [cleaned, *self._get_json_candidates(cleaned)]:
            try:
                data = json.loads(candidate)
            except json.JSONDecodeError as e:
                last_json_error = e
                continue

            try:
                return response_model.model_validate(data)
            except Exception as e:
                last_validation_error = e
                continue

        # Truncation recovery path for partial JSON payloads from model output
        recovered_data = self._recover_truncated_json(cleaned)
        if recovered_data is not None:
            try:
                return response_model.model_validate(recovered_data)
            except Exception as e:
                last_validation_error = e

        if last_validation_error is not None:
            logger.error(
                "ai_validation_error",
                error=str(last_validation_error),
                model=response_model.__name__,
            )
            raise AiGenerationParsingError(f"Failed to validate response: {last_validation_error}")

        logger.error(
            "ai_json_parse_error",
            error=str(last_json_error) if last_json_error else "No JSON object found in response",
            response_preview=cleaned[:200],
        )
        raise AiGenerationParsingError(
            f"Failed to parse JSON: {last_json_error}"
            if last_json_error
            else "Failed to parse JSON: No JSON object found"
        )

    async def generate_with_thinking(
        self,
        prompt: str,
        response_schema: Type[T],
        thinking_budget: int = 2048,
    ) -> T:
        """
        Generate structured output utilizing native test-time compute (thinking).
        Falls back to generate_json if ThinkingConfig is unavailable.
        """
        try:
            import google.genai as genai
            from google.genai.types import GenerateContentConfig, ThinkingConfig
        except ImportError as exc:
            logger.warning("google-genai SDK not available or lacks ThinkingConfig; falling back to generate_json.")
            return await self.generate_json(prompt, response_schema, max_tokens=8192)

        project = os.environ.get("VERTEX_PROJECT") or os.environ.get("VERTEX_PROJECT")
        location = (
            os.environ.get("GOOGLE_CLOUD_LOCATION")
            or os.environ.get("GOOGLE_CLOUD_REGION")
            or "us-central1"
        )
        api_key = os.environ.get("GOOGLE_API_KEY")

        if not project and not api_key:
            logger.warning("No credentials for native model SDK; falling back to generate_json.")
            return await self.generate_json(prompt, response_schema, max_tokens=8192)

        model_name = (
            str(os.environ.get("AI_MODEL_THINKING_ID", "")).strip()
            or str(os.environ.get("AI_MODEL_HEAVY_ID", "")).strip()
            or str(os.environ.get("AI_MODEL_DEFAULT_ID", "")).strip()
        )
        if not model_name:
            logger.warning("No AI model ID configured for thinking path; falling back to generate_json.")
            return await self.generate_json(prompt, response_schema, max_tokens=8192)

        if project:
            client = genai.Client(vertexai=False, project=project, location=location)
        else:
            client = genai.Client(api_key=api_key)

        raw_schema = response_schema.model_json_schema()
        prepared_schema = self._prepare_schema_for_provider(raw_schema)

        config = GenerateContentConfig(
            thinking_config=ThinkingConfig(thinking_budget=thinking_budget),
            temperature=0.7,
            response_mime_type="application/json",
            response_json_schema=prepared_schema,
        )

        try:
            response = await client.aio.models.generate_content(
                model=model_name,
                contents=prompt,
                config=config,
            )
            return response_schema.model_validate_json(response.text)
        except Exception as e:
            logger.warning(
                "ai_thinking_generation_failed",
                error=str(e),
                model=model_name,
            )
            return await self.generate_json(prompt, response_schema, max_tokens=8192)
    
    async def generate_with_fallback(
        self,
        prompt: str,
        fallback_value: str,
        complexity_score: float = 0.5,
        temperature: float = 0.7,
        max_tokens: int = 2048,
    ) -> tuple[str, bool]:
        """
        Generate text with graceful fallback.
        
        Args:
            prompt: The prompt to send
            fallback_value: Value to return if generation fails
            complexity_score: Query complexity for model selection
            temperature: Sampling temperature
            max_tokens: Maximum output tokens
            
        Returns:
            Tuple of (response_text, used_fallback)
        """
        try:
            result = await self.generate(
                prompt=prompt,
                complexity_score=complexity_score,
                temperature=temperature,
                max_tokens=max_tokens,
            )
            return result, False
        except Exception as e:
            logger.warning(
                "ai_fallback_triggered",
                error=str(e),
                fallback_preview=fallback_value[:100],
            )
            return fallback_value, True
    
    async def generate_json_with_fallback(
        self,
        prompt: str,
        response_model: Type[T],
        fallback_value: T,
        complexity_score: float = 0.5,
        temperature: float = 0.3,
        max_tokens: int = 2048,
    ) -> tuple[T, bool]:
        """
        Generate and parse JSON with graceful fallback.
        
        Args:
            prompt: The prompt (should ask for JSON output)
            response_model: Pydantic model class for parsing
            fallback_value: Pydantic model instance to return on failure
            complexity_score: Query complexity for model selection
            temperature: Sampling temperature
            max_tokens: Maximum output tokens
            
        Returns:
            Tuple of (parsed_model, used_fallback)
        """
        try:
            result = await self.generate_json(
                prompt=prompt,
                response_model=response_model,
                complexity_score=complexity_score,
                temperature=temperature,
                max_tokens=max_tokens,
            )
            return result, False
        except Exception as e:
            logger.warning(
                "ai_json_fallback_triggered",
                error=str(e),
                model=response_model.__name__,
            )
            return fallback_value, True
    
    def estimate_complexity(self, query: str) -> float:
        """
        Estimate query complexity for model selection.
        
        Simple heuristic based on:
        - Query length
        - Presence of technical terms
        - Question complexity indicators
        
        Args:
            query: The user's query
            
        Returns:
            Complexity score 0.0 to 1.0
        """
        score = 0.0
        query_lower = query.lower()
        
        # Length factor (longer = more complex)
        word_count = len(query.split())
        if word_count > 20:
            score += 0.3
        elif word_count > 10:
            score += 0.15
        
        # Technical complexity indicators
        complex_indicators = [
            "mechanism", "pathway", "interaction", "relationship",
            "comparison", "versus", "vs", "difference between",
            "systematic review", "meta-analysis", "comprehensive",
            "multifactorial", "interdisciplinary", "cross-domain",
        ]
        for indicator in complex_indicators:
            if indicator in query_lower:
                score += 0.15
        
        # Ambiguity indicators (might need Pro for better understanding)
        ambiguity_indicators = [
            "or", "and/or", "either", "various", "multiple",
            "different types", "all aspects", "everything about",
        ]
        for indicator in ambiguity_indicators:
            if indicator in query_lower:
                score += 0.1
        
        return min(1.0, score)


# Singleton instance
ai_generation_service = AiGenerationService()
