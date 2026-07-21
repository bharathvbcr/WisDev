"""Router exports for Cloud Run API.

Routers are exposed lazily (PEP 562) so importing one router does not drag in
the others. In particular, importing ``routers.manuscript_router`` must not pull
in ``ml_router`` (and its numpy/heavy-ML service stack), which would otherwise
prevent a manuscript-only service from starting.
"""

__all__ = ['ml_router']


def __getattr__(name):
    if name == 'ml_router':
        from routers.ml_router import router
        return router
    raise AttributeError(f"module {__name__!r} has no attribute {name!r}")
