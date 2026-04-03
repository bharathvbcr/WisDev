# ScholarLM Python ML Sidecar

Specialized ML worker service for heavy compute primitives. Stateless and optimized for Container Service.

## Responsibilities
- **PDF Extraction:** Extracts structured text and metadata from academic PDFs using PyMuPDF.
- **Embeddings:** High-throughput text embedding generation.
- **LLM Serving:** Provides a gRPC interface for heavy LLM generation tasks (used by Go).
- **Computer Vision:** (Optional) Multi-modal analysis of figures and tables in papers.

## Architecture
- **Language:** Python (ML Worker)
- **Framework:** FastAPI
- **Protocol:** HTTP for primitives, gRPC for LLM generation.

## Environment Variables
- `PORT`: Listen port (default: 8080)
- `VERTEX_PROJECT`: Project ID for LLM Provider and OpenTelemetry.
- `VERTEX_FUNCTION_URL`: (Legacy) URL for cloud-proxied Gemini calls.

## Endpoints
- `POST /ml/pdf`: Extract text from base64 PDF.
- `POST /ml/embed`: Generate embedding vector for text.
- `GRPC :50051`: LLM service implementation.
