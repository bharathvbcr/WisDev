"""Dedicated FastAPI entrypoint for the Azure compute container app."""
from __future__ import annotations

from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware

from routers.azure_compute_router import router as azure_compute_router
from routers.azure_compute_router import raptor_service, tree_cache
from services.bm25_service import get_bm25_index
from services.embedding_service import embedding_service

ALLOWED_ORIGINS = [
    "http://localhost:3000",
    "http://localhost:5173",
]


def create_app() -> FastAPI:
    app = FastAPI(
        title="WisDev Azure Compute",
        description="Chunking, Azure embeddings, RAPTOR helpers, and BM25.",
        version="1.1.0",
    )

    app.add_middleware(
        CORSMiddleware,
        allow_origins=ALLOWED_ORIGINS,
        allow_credentials=True,
        allow_methods=["*"],
        allow_headers=["*"],
    )
    app.include_router(azure_compute_router)

    @app.get("/")
    async def root():
        return {
            "service": "wisdev-azure-compute",
            "version": "1.1.0",
            "embeddingProvider": embedding_service.provider,
        }

    @app.get("/health")
    async def health():
        bm25_index = get_bm25_index()
        return {
            "status": "healthy" if embedding_service.is_ready() else "degraded",
            "model_loaded": embedding_service.is_ready(),
            "services": {
                "embedding": embedding_service.is_ready(),
                "raptor": True,
                "cache": True,
                "bm25": not bm25_index.is_empty or bm25_index.size == 0,
            },
            "state": {
                "liveTrees": len(raptor_service.trees),
                "cachedTrees": len(tree_cache._local_cache),
                "bm25Documents": bm25_index.size,
            },
        }

    return app


app = create_app()
