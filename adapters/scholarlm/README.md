# ScholarLM Adapter Boundary

This directory documents the adapter boundary back to ScholarLM.

The open-source agent should not import ScholarLM frontend, Firebase auth, app feature flags, or proprietary deployment assumptions. ScholarLM should eventually depend on WisDev through one of these thin surfaces:

- HTTP API adapter routes.
- Generated protobuf clients.
- A future public Go package under `orchestrator/pkg/wisdev`.

Open-source-native configuration lives in `config/wisdev.example.yaml`; ScholarLM-specific environment names should stay at this adapter boundary rather than leaking into core WisDev runtime packages.
