import json
from pathlib import Path

from main import app


def _manifest_python_paths() -> set[str]:
    manifest_path = Path(__file__).resolve().parents[3] / 'config' / 'endpoints.manifest.json'
    manifest = json.loads(manifest_path.read_text(encoding='utf-8'))
    return {
        entry['path']
        for entry in manifest['entries']
        if entry.get('runtime') == 'python_sidecar'
    }


def test_manifest_python_routes_are_mounted() -> None:
    mounted_paths = {
        route.path
        for route in app.routes
        if getattr(route, 'path', None)
    }
    assert _manifest_python_paths().issubset(mounted_paths)
