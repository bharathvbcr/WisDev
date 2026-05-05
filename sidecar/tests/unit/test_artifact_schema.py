"""
tests/unit/test_artifact_schema.py

Unit tests for artifacts/schema.py.

Verifies:
- Model construction with snake_case and camelCase field names.
- Roundtrip serialization (model → dict → model) preserves camelCase aliases.
- flatten_to_legacy produces the exact keys Go's normalizeStepArtifacts expects.
- Partial / missing field tolerance (no crash, sensible defaults).
"""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from artifacts.schema import (
    ARTIFACT_SCHEMA_VERSION,
    CanonicalCitation,
    CitationArtifactBundle,
    ClaimEvidenceArtifact,
    PaperArtifactBundle,
    ReasoningArtifactBundle,
    ReasoningBranch,
    ReasoningVerification,
    StepArtifactEnvelope,
    flatten_to_legacy,
)


# ---------------------------------------------------------------------------
# CanonicalCitation
# ---------------------------------------------------------------------------


class TestCanonicalCitation:
    def test_snake_case_construction(self):
        c = CanonicalCitation.model_validate(
            {
                "id": "arc123",
                "title": "Test Paper",
                "arxiv_id": "2301.00001",
                "canonical_id": "doi:10.1/x",
                "authors": ["Alice", "Bob"],
                "year": 2023,
                "resolved": True,
                "verified": False,
            }
        )
        assert c.id == "arc123"
        assert c.arxiv_id == "2301.00001"
        assert c.authors == ["Alice", "Bob"]

    def test_camel_case_construction(self):
        c = CanonicalCitation.model_validate(
            {
                "id": "arc456",
                "title": "Another Paper",
                "arxivId": "2302.00002",
                "canonicalId": "doi:10.2/y",
                "authors": ["Carol"],
                "year": 2024,
                "resolved": False,
                "verified": True,
            }
        )
        assert c.arxiv_id == "2302.00002"
        assert c.canonical_id == "doi:10.2/y"
        assert c.verified is True

    def test_model_dump_legacy_keys(self):
        c = CanonicalCitation.model_validate(
            {
                "id": "x",
                "title": "T",
                "doi": "10.0/z",
                "arxivId": "0000.00001",
                "canonicalId": "cid",
                "authors": ["D"],
                "year": 2020,
                "resolved": True,
                "verified": True,
            }
        )
        legacy = c.model_dump_legacy()
        # All camelCase keys that Go's normalizeStepArtifacts reads.
        assert legacy["arxivId"] == "0000.00001"
        assert legacy["canonicalId"] == "cid"
        assert legacy["resolved"] is True
        assert legacy["year"] == 2020

    def test_defaults_on_empty(self):
        c = CanonicalCitation.model_validate({"title": "Minimal"})
        assert c.id is None
        assert c.doi is None
        assert c.authors == []
        assert c.resolved is False

    def test_roundtrip_via_json(self):
        c = CanonicalCitation.model_validate(
            {"title": "RT Paper", "arxivId": "1234.56789", "verified": True}
        )
        dumped = c.model_dump(by_alias=True)
        restored = CanonicalCitation.model_validate(dumped)
        assert restored.arxiv_id == c.arxiv_id
        assert restored.verified is True


# ---------------------------------------------------------------------------
# CitationArtifactBundle
# ---------------------------------------------------------------------------


class TestCitationArtifactBundle:
    def _make_citation(self, idx: int) -> CanonicalCitation:
        return CanonicalCitation.model_validate(
            {
                "id": f"c{idx}",
                "title": f"Citation {idx}",
                "doi": f"10.0/{idx}",
                "resolved": True,
                "verified": True,
            }
        )

    def test_primary_prefers_verified_records(self):
        c1 = self._make_citation(1)
        c2 = self._make_citation(2)
        bundle = CitationArtifactBundle.model_validate(
            {"citations": [c1], "verifiedRecords": [c2]}
        )
        assert bundle.primary == [c2]

    def test_primary_falls_back_to_canonical(self):
        c1 = self._make_citation(1)
        c2 = self._make_citation(2)
        bundle = CitationArtifactBundle.model_validate(
            {"citations": [c1], "canonicalSources": [c2]}
        )
        assert bundle.primary == [c2]

    def test_primary_falls_back_to_citations(self):
        c1 = self._make_citation(1)
        bundle = CitationArtifactBundle.model_validate({"citations": [c1]})
        assert bundle.primary == [c1]

    def test_camel_case_construction(self):
        bundle = CitationArtifactBundle.model_validate(
            {
                "citations": [{"title": "C1", "arxivId": "1234"}],
                "canonicalSources": [],
                "verifiedRecords": [],
                "resolvedCount": 3,
                "validCount": 2,
                "invalidCount": 1,
                "duplicateCount": 0,
            }
        )
        assert bundle.resolved_count == 3
        assert bundle.valid_count == 2
        assert len(bundle.citations) == 1
        assert bundle.citations[0].arxiv_id == "1234"

    def test_defaults(self):
        bundle = CitationArtifactBundle.model_validate({})
        assert bundle.citations == []
        assert bundle.resolved_count == 0


# ---------------------------------------------------------------------------
# ReasoningBranch / ReasoningVerification / ReasoningArtifactBundle
# ---------------------------------------------------------------------------


class TestReasoningSchemas:
    def test_branch_snake_and_camel(self):
        b_snake = ReasoningBranch.model_validate(
            {
                "claim": "C1",
                "falsifiabilityCondition": "if X then Y",
                "supportScore": 0.85,
                "isTerminated": False,
            }
        )
        b_camel = ReasoningBranch.model_validate(
            {
                "claim": "C1",
                "falsifiabilityCondition": "if X then Y",
                "supportScore": 0.85,
                "isTerminated": False,
            }
        )
        assert b_snake.support_score == b_camel.support_score
        assert b_snake.falsifiability_condition == b_camel.falsifiability_condition

    def test_branch_legacy_keys(self):
        b = ReasoningBranch.model_validate({"claim": "X", "supportScore": 0.7})
        leg = b.model_dump_legacy()
        assert "falsifiabilityCondition" in leg
        assert "supportScore" in leg
        assert leg["supportScore"] == 0.7

    def test_verification_defaults(self):
        v = ReasoningVerification.model_validate({})
        assert v.total_branches == 0
        assert v.ready_for_synthesis is False

    def test_verification_legacy_keys(self):
        v = ReasoningVerification.model_validate(
            {
                "totalBranches": 5,
                "verifiedBranches": 3,
                "rejectedBranches": 2,
                "readyForSynthesis": True,
            }
        )
        leg = v.model_dump_legacy()
        assert leg["totalBranches"] == 5
        assert leg["readyForSynthesis"] is True

    def test_bundle_nesting(self):
        bundle = ReasoningArtifactBundle.model_validate(
            {
                "branches": [{"claim": "Hypothesis A", "supportScore": 0.9}],
                "verification": {
                    "totalBranches": 1,
                    "verifiedBranches": 1,
                    "readyForSynthesis": True,
                },
            }
        )
        assert len(bundle.branches) == 1
        assert bundle.verification is not None
        assert bundle.verification.ready_for_synthesis is True


# ---------------------------------------------------------------------------
# ClaimEvidenceArtifact
# ---------------------------------------------------------------------------


class TestClaimEvidenceArtifact:
    def test_basic(self):
        a = ClaimEvidenceArtifact.model_validate(
            {"table": "| Claim | Evidence |\n|---|---|\n| A | B |", "rowCount": 1}
        )
        leg = a.model_dump_legacy()
        assert leg["table"].startswith("| Claim")
        assert leg["rowCount"] == 1

    def test_camel_alias(self):
        a = ClaimEvidenceArtifact.model_validate({"table": "T", "rowCount": 5})
        assert a.row_count == 5


# ---------------------------------------------------------------------------
# StepArtifactEnvelope + flatten_to_legacy
# ---------------------------------------------------------------------------


class TestStepArtifactEnvelope:
    def _make_citation(self, idx: int) -> CanonicalCitation:
        return CanonicalCitation.model_validate(
            {"id": f"c{idx}", "title": f"C{idx}", "doi": f"10.0/{idx}"}
        )

    def test_schema_version_present(self):
        env = StepArtifactEnvelope.model_validate({"action": "research.test"})
        assert env.schema_version == ARTIFACT_SCHEMA_VERSION

    def test_flatten_empty(self):
        env = StepArtifactEnvelope.model_validate({"action": "research.test"})
        leg = flatten_to_legacy(env)
        # No typed bundles → passthrough only.
        assert leg == {}

    def test_flatten_paper_bundle(self):
        c = self._make_citation(1)
        env = StepArtifactEnvelope.model_validate(
            {"action": "research.search", "paperBundle": {"papers": [c]}}
        )
        leg = flatten_to_legacy(env)
        assert "papers" in leg
        assert len(leg["papers"]) == 1
        paper = leg["papers"][0]
        assert paper["title"] == "C1"
        assert "arxivId" in paper  # camelCase key expected by Go

    def test_flatten_citation_bundle_verified_records(self):
        c = self._make_citation(1)
        bundle = CitationArtifactBundle.model_validate(
            {
                "citations": [c],
                "verifiedRecords": [c],
                "validCount": 1,
                "invalidCount": 0,
                "duplicateCount": 0,
            }
        )
        env = StepArtifactEnvelope.model_validate(
            {"action": "research.verifyCitations", "citationBundle": bundle}
        )
        leg = flatten_to_legacy(env)
        assert "verifiedRecords" in leg
        assert "citations" in leg
        assert leg["validCount"] == 1

    def test_flatten_citation_bundle_canonical_sources(self):
        c = self._make_citation(2)
        bundle = CitationArtifactBundle.model_validate(
            {
                "citations": [c],
                "canonicalSources": [c],
                "resolvedCount": 1,
            }
        )
        env = StepArtifactEnvelope.model_validate(
            {"action": "research.resolveCanonicalCitations", "citationBundle": bundle}
        )
        leg = flatten_to_legacy(env)
        assert "canonicalSources" in leg
        assert leg["resolvedCount"] == 1

    def test_flatten_reasoning_bundle_with_branches(self):
        b = ReasoningBranch.model_validate({"claim": "H1", "supportScore": 0.8})
        bundle = ReasoningArtifactBundle.model_validate(
            {
                "branches": [b],
                "verification": {
                    "totalBranches": 1,
                    "verifiedBranches": 1,
                    "readyForSynthesis": True,
                },
            }
        )
        env = StepArtifactEnvelope.model_validate(
            {"action": "research.verifyReasoningPaths", "reasoningBundle": bundle}
        )
        leg = flatten_to_legacy(env)
        assert "branches" in leg
        assert "reasoningVerification" in leg
        assert leg["reasoningVerification"]["readyForSynthesis"] is True

    def test_flatten_claim_evidence_artifact(self):
        art = ClaimEvidenceArtifact.model_validate({"table": "| A | B |", "rowCount": 2})
        env = StepArtifactEnvelope.model_validate(
            {"action": "research.buildClaimEvidenceTable", "claimEvidenceArtifact": art}
        )
        leg = flatten_to_legacy(env)
        assert "claimEvidenceTable" in leg
        assert leg["claimEvidenceTable"]["rowCount"] == 2

    def test_flatten_legacy_passthrough(self):
        """Keys in artifacts dict are passed through unchanged."""
        env = StepArtifactEnvelope.model_validate(
            {
                "action": "research.test",
                "artifacts": {"customKey": "value", "confidence": 0.9},
            }
        )
        leg = flatten_to_legacy(env)
        assert leg["customKey"] == "value"
        assert leg["confidence"] == 0.9

    def test_flatten_typed_overrides_legacy_on_conflict(self):
        """Typed bundle should win over legacy artifacts for the same key."""
        c = self._make_citation(3)
        bundle = CitationArtifactBundle.model_validate({"citations": [c]})
        env = StepArtifactEnvelope.model_validate(
            {
                "action": "research.test",
                "citationBundle": bundle,
                "artifacts": {"citations": []},  # stale legacy entry
            }
        )
        leg = flatten_to_legacy(env)
        # Typed bundle overwrites the legacy empty list.
        assert len(leg["citations"]) == 1


class TestCanonicalJsonSchemaParity:
    def _load_canonical_schema(self) -> dict:
        schema_path = (
            Path(__file__).resolve().parents[4] / "schema" / "artifact_schema_v1.json"
        )
        return json.loads(schema_path.read_text(encoding="utf-8"))

    def test_schema_version_matches_python_constant(self):
        schema = self._load_canonical_schema()
        assert schema["version"] == ARTIFACT_SCHEMA_VERSION

    def test_schema_exposes_typed_bundle_properties(self):
        schema = self._load_canonical_schema()
        props = schema["properties"]
        assert "paperBundle" in props
        assert "citationBundle" in props
        assert "reasoningBundle" in props
        assert "claimEvidenceArtifact" in props
