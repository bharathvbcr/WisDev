# ScholarLM Adapter Boundary

This directory documents the compatibility boundary back to ScholarLM.

The open-source agent should not import ScholarLM frontend, Firebase auth, app feature flags, or proprietary deployment assumptions. ScholarLM should eventually depend on WisDev through one of these thin surfaces:

- HTTP API compatibility routes.
- Generated protobuf clients.
- A future public Go package under `orchestrator/pkg/wisdev`.

Current copied code still contains some ScholarLM-era configuration names for backward compatibility. Those names should remain accepted aliases while open-source-native config moves to `config/wisdev.example.yaml`.

