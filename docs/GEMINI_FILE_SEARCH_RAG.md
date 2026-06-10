# Gemini File Search Multimodal RAG

WisDev Arc can ground `/wisdev/rag/answer` with Gemini API File Search stores from the Go orchestrator. Go remains the canonical RAG owner; clients only send typed options, and Python remains limited to worker-side ML tasks.

## Configuration

Use Google Cloud Secret Manager or server-side environment variables for `GOOGLE_API_KEY` / `GEMINI_API_KEY`. Do not expose these values to the browser or client code.

```env
RAG_FILE_SEARCH_STORE_NAMES=fileSearchStores/my-store
RAG_FILE_SEARCH_METADATA_FILTER=department = "research"
RAG_FILE_SEARCH_TOP_K=8
RAG_FILE_SEARCH_MODEL=
RAG_FILE_SEARCH_MULTIMODAL=true
RAG_FILE_SEARCH_REQUIRED=false
RAG_FILE_SEARCH_EMBEDDING_MODEL=
```

`RAG_FILE_SEARCH_STORE_NAMES` accepts a comma-separated list. `RAG_FILE_SEARCH_REQUIRED=true` makes a File Search failure fail the request instead of falling back to local RAG. Leave `RAG_FILE_SEARCH_EMBEDDING_MODEL` unset to use the standard embedding model from `scholar_models.json`.

## Request Override

Callers can override the server default per answer request:

```json
{
  "query": "Which microscopy figures support the mechanism?",
  "fileSearch": {
    "enabled": true,
    "storeNames": ["fileSearchStores/my-store"],
    "metadataFilter": "paper_type = \"supplement\"",
    "topK": 8,
    "multimodal": true
  }
}
```

The response includes `metadata.fileSearch`, citation page ranges when Gemini returns them, image/media attribution when present, and custom metadata from retrieved chunks.

## Operational Notes

Gemini API File Search is a Gemini Developer API feature, not a Vertex AI backend feature. The Go client therefore uses the existing server-side Gemini API key resolution path for File Search even when normal structured generation is using Vertex AI.

Index stores with the standard embedding model configured in `scholar_models.json` for text and image retrieval. Audio and video are not supported by Gemini File Search at this time.
