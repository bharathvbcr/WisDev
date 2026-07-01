"""Slim ASGI app exposing ONLY the manuscript (DocGen) router.

Run a manuscript-only sidecar without dragging in ``ml_router`` (numpy/heavy-ML)
or the rest of the stack — useful when all you need is section drafting/review:

    uvicorn manuscript_app:app --port 8090

The package ``routers/__init__.py`` is already lazy (PEP 562), and
``routers.manuscript_router`` only imports ``services.gemini_service`` — neither
pulls in numpy — so this entrypoint starts with a minimal dependency footprint.
"""

from __future__ import annotations

from fastapi import FastAPI

from routers.manuscript_router import router as manuscript_router

app = FastAPI(title="WisDev Manuscript Sidecar", version="1.0.0")
app.include_router(manuscript_router)


@app.get("/health")
async def health() -> dict[str, str]:
    return {"status": "ok", "service": "manuscript"}
