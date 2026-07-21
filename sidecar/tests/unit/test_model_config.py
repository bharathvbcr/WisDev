import json

from services import model_config


def test_load_model_config_reads_nested_central_file(tmp_path, monkeypatch):
    config_file = tmp_path / "scholar_models.json"
    config_file.write_text(
        json.dumps(
            {
                "tiers": {
                    "light": "light-model",
                    "standard": "standard-model",
                    "heavy": "heavy-model",
                },
                "embeddings": {
                    "primary": "embed-primary",
                    "standard": "embed-standard",
                    "fallback": "embed-fallback",
                },
                "locations": {
                    "generative": "global",
                    "embedding": "global",
                },
            }
        ),
        encoding="utf-8",
    )
    model_config.clear_model_config_cache()
    monkeypatch.setattr(model_config, "_candidate_config_paths", lambda: [config_file])

    assert model_config.get_model_tier("light") == "light-model"
    assert model_config.get_model_tier("balanced") == "standard-model"
    assert model_config.get_embedding_model("primary") == "embed-primary"
    assert model_config.get_model_location("generative") == "global"

    model_config.clear_model_config_cache()


def test_resolve_generation_model_id_prefers_env_override(monkeypatch):
    monkeypatch.setenv("AI_MODEL_LIGHT_ID", "env-light")
    assert model_config.resolve_generation_model_id("light") == "env-light"


def test_get_model_location_prefers_env_override(monkeypatch):
    monkeypatch.setenv("GOOGLE_CLOUD_LOCATION", "us")
    assert model_config.get_model_location("generative") == "us"


def test_get_model_location_keeps_generation_env_out_of_embedding(monkeypatch):
    monkeypatch.setenv("GOOGLE_CLOUD_LOCATION", "us")
    monkeypatch.delenv("GOOGLE_CLOUD_EMBEDDING_LOCATION", raising=False)
    monkeypatch.delenv("GOOGLE_CLOUD_EMBEDDING_REGION", raising=False)

    assert model_config.get_model_location("embedding") == "global"


def test_load_model_config_accepts_legacy_top_level_file(tmp_path, monkeypatch):
    config_file = tmp_path / "scholar_models.json"
    config_file.write_text(
        json.dumps(
            {
                "light": "legacy-light",
                "standard": "legacy-standard",
                "heavy": "legacy-heavy",
            }
        ),
        encoding="utf-8",
    )
    model_config.clear_model_config_cache()
    monkeypatch.setattr(model_config, "_candidate_config_paths", lambda: [config_file])

    assert model_config.get_model_tier("heavy") == "legacy-heavy"

    model_config.clear_model_config_cache()
