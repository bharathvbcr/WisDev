"""
Minimal compatibility shim for tests that patch ``fitz.open``.

If PyMuPDF is installed elsewhere, import it transparently. Otherwise expose an
``open`` symbol that raises at runtime unless the test patches it.
"""

from __future__ import annotations

try:  # pragma: no cover - exercised only when PyMuPDF is installed
    from pymupdf import open as open  # type: ignore[attr-defined]
except Exception:  # pragma: no cover - default lightweight fallback
    def open(*args, **kwargs):  # type: ignore[no-redef]
        raise ImportError("PyMuPDF is not installed")
