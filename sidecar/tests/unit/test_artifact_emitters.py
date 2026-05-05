"""
tests/unit/test_artifact_emitters.py

Unit tests for artifacts/emitters.py.

Verifies that each emitter:
- Correctly populates the right typed bundle from representative LLM output.
- Produces a flatten_to_legacy() dict with the exact keys Go's normalizer expects.
- Tolerates partial / malformed raw dicts gracefully (no crashes, sensible defaults).
- The dispatch helper (emit_for_action) routes to the correct emitter.
"""

from __future__ import annotations

import pytest

from artifacts.emitters import (
    emit_build_claim_evidence_table,
    emit_for_action,
    emit_paper_search,
    emit_propose_hypotheses,
    emit_resolve_citations,
    emit_verify_citations,
    emit_verify_reasoning_paths,
    flatten_to_legacy,
)


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _citation_map(idx: int, *, doi: str = "", arxiv: str = "", verified: bool = True) -> dict:
    return {
        "id": f"c{idx}",
        "title": f"Citation {idx}",
        "doi": doi or f"10.0/{idx}",
        "arxivId": arxiv,
        "authors": ["Author A"],
        "year": 2020 + idx,
        "resolved": True,
        "verified": verified,
    }


def _branch_map(claim: str, score: float = 0.8, terminated: bool = False) -> dict:
    return {
        "claim": claim,
        "falsifiabilityCondition": "if X",
        "supportScore": score,
        "isTerminated": terminated,
    }


# ---------------------------------------------------------------------------
# emit_resolve_citations
# ---------------------------------------------------------------------------


class TestEmitResolveCitations:
    def test_full_payload(self):
        raw = {
            "canonicalSources": [_citation_map(1), _citation_map(2)],
            "resolvedCount": 2,
            "duplicateCount": 0,
        }
        env = emit_resolve_citations(raw)
        assert env.action == "research.resolveCanonicalCitations"
        assert env.citation_bundle is not None
        assert len(env.citation_bundle.citations) == 2
        assert env.citation_bundle.resolved_count == 2

    def test_flatten_has_canonical_sources(self):
        raw = {"canonicalSources": [_citation_map(1)], "resolvedCount": 1}
        leg = flatten_to_legacy(emit_resolve_citations(raw))
        assert "canonicalSources" in leg
        assert "citations" in leg  # Go normalizer reads both
        assert leg["resolvedCount"] == 1

    def test_falls_back_to_citations_key(self):
        raw = {"citations": [_citation_map(1)]}
        env = emit_resolve_citations(raw)
        assert env.citation_bundle is not None
        assert len(env.citation_bundle.citations) == 1

    def test_empty_payload(self):
        env = emit_resolve_citations({})
        assert env.citation_bundle is None
        assert isinstance(env.artifacts.get("canonicalSources"), list)
        assert isinstance(env.artifacts.get("citations"), list)
        assert isinstance(env.artifacts.get("resolvedCount"), int)

    def test_preserves_extra_keys_in_artifacts(self):
        raw = {"canonicalSources": [_citation_map(1)], "confidence": 0.95}
        leg = flatten_to_legacy(emit_resolve_citations(raw))
        assert leg["confidence"] == 0.95

    def test_malformed_citation_entry(self):
        # Non-dict entries should be silently skipped.
        raw = {"canonicalSources": ["not-a-dict", None, _citation_map(1)]}
        env = emit_resolve_citations(raw)
        assert env.citation_bundle is not None
        assert len(env.citation_bundle.citations) == 1

    def test_rejects_missing_required_title(self):
        raw = {"canonicalSources": [{"id": "c1", "doi": "10.0/1"}]}
        with pytest.raises(ValueError, match="citationBundle.citations\\[0\\].title is required"):
            emit_resolve_citations(raw)


# ---------------------------------------------------------------------------
# emit_verify_citations
# ---------------------------------------------------------------------------


class TestEmitVerifyCitations:
    def test_verified_records_populated(self):
        raw = {
            "verifiedRecords": [_citation_map(1, verified=True), _citation_map(2, verified=False)],
            "validCount": 1,
            "invalidCount": 1,
            "duplicateCount": 0,
        }
        env = emit_verify_citations(raw)
        assert env.citation_bundle is not None
        assert len(env.citation_bundle.verified_records) == 2
        assert env.citation_bundle.valid_count == 1

    def test_flatten_has_verified_records(self):
        raw = {"verifiedRecords": [_citation_map(1)], "validCount": 1, "invalidCount": 0}
        leg = flatten_to_legacy(emit_verify_citations(raw))
        assert "verifiedRecords" in leg
        assert "citations" in leg
        assert leg["validCount"] == 1
        assert leg["invalidCount"] == 0

    def test_empty_payload(self):
        env = emit_verify_citations({})
        assert env.citation_bundle is None
        leg = flatten_to_legacy(env)
        assert isinstance(leg.get("verifiedRecords"), list)
        assert isinstance(leg.get("citations"), list)
        assert isinstance(leg.get("validCount"), int)
        assert isinstance(leg.get("invalidCount"), int)

    def test_snake_case_keys(self):
        raw = {"verifiedRecords": [_citation_map(1)], "valid_count": 1, "invalid_count": 0}
        env = emit_verify_citations(raw)
        assert env.citation_bundle is not None
        assert env.citation_bundle.valid_count == 1


# ---------------------------------------------------------------------------
# emit_propose_hypotheses
# ---------------------------------------------------------------------------


class TestEmitProposeHypotheses:
    def test_hypotheses_key(self):
        raw = {
            "hypotheses": [
                {"claim": "H1", "falsifiabilityCondition": "if A", "confidenceThreshold": 0.8},
                {"claim": "H2", "supportScore": 0.6},
            ]
        }
        env = emit_propose_hypotheses(raw)
        assert env.action == "research.proposeHypotheses"
        assert env.reasoning_bundle is not None
        assert len(env.reasoning_bundle.branches) == 2

    def test_branches_key_alias(self):
        raw = {"branches": [_branch_map("H1")]}
        env = emit_propose_hypotheses(raw)
        assert env.reasoning_bundle is not None
        assert env.reasoning_bundle.branches[0].claim == "H1"

    def test_flatten_outputs_branches(self):
        raw = {"hypotheses": [_branch_map("H1"), _branch_map("H2")]}
        leg = flatten_to_legacy(emit_propose_hypotheses(raw))
        assert "branches" in leg
        assert len(leg["branches"]) == 2
        assert leg["branches"][0]["claim"] == "H1"

    def test_empty(self):
        env = emit_propose_hypotheses({"hypotheses": []})
        assert env.reasoning_bundle is None
        leg = flatten_to_legacy(env)
        assert isinstance(leg.get("branches"), list)


# ---------------------------------------------------------------------------
# emit_verify_reasoning_paths
# ---------------------------------------------------------------------------


class TestEmitVerifyReasoningPaths:
    def test_full_payload(self):
        raw = {
            "totalBranches": 3,
            "verifiedBranches": 2,
            "rejectedBranches": 1,
            "readyForSynthesis": True,
            "branches": [_branch_map("H1", 0.9), _branch_map("H2", 0.7)],
        }
        env = emit_verify_reasoning_paths(raw)
        assert env.reasoning_bundle is not None
        assert env.reasoning_bundle.verification is not None
        assert env.reasoning_bundle.verification.total_branches == 3
        assert env.reasoning_bundle.verification.ready_for_synthesis is True
        assert len(env.reasoning_bundle.branches) == 2

    def test_flatten_output_keys(self):
        raw = {
            "totalBranches": 2,
            "verifiedBranches": 1,
            "rejectedBranches": 1,
            "readyForSynthesis": False,
            "branches": [_branch_map("H1", 0.9)],
        }
        leg = flatten_to_legacy(emit_verify_reasoning_paths(raw))
        assert "branches" in leg
        assert "reasoningVerification" in leg
        rv = leg["reasoningVerification"]
        assert rv["totalBranches"] == 2
        assert rv["readyForSynthesis"] is False

    def test_no_verification_numbers(self):
        # Verification only from branches (no explicit summary).
        raw = {"branches": [_branch_map("H1", 0.9)]}
        env = emit_verify_reasoning_paths(raw)
        # Verification should be None (no summary counts provided).
        assert env.reasoning_bundle is not None
        assert env.reasoning_bundle.verification is None

    def test_empty(self):
        env = emit_verify_reasoning_paths({})
        assert env.reasoning_bundle is None
        leg = flatten_to_legacy(env)
        assert isinstance(leg.get("branches"), list)
        assert isinstance(leg.get("reasoningVerification"), dict)


# ---------------------------------------------------------------------------
# emit_build_claim_evidence_table
# ---------------------------------------------------------------------------


class TestEmitBuildClaimEvidenceTable:
    def test_table_present(self):
        raw = {"table": "| Claim | Evidence |\n|---|---|\n| A | B |", "rowCount": 1}
        env = emit_build_claim_evidence_table(raw)
        assert env.claim_evidence_artifact is not None
        assert env.claim_evidence_artifact.row_count == 1

    def test_flatten_has_claim_evidence_table(self):
        raw = {"table": "T", "rowCount": 3}
        leg = flatten_to_legacy(emit_build_claim_evidence_table(raw))
        assert "claimEvidenceTable" in leg
        assert leg["claimEvidenceTable"]["rowCount"] == 3
        assert leg["claimEvidenceTable"]["table"] == "T"

    def test_empty(self):
        env = emit_build_claim_evidence_table({})
        assert env.claim_evidence_artifact is None
        leg = flatten_to_legacy(env)
        assert isinstance(leg.get("claimEvidenceTable"), dict)
        assert set(leg["claimEvidenceTable"].keys()) == {"table", "rowCount"}

    def test_claim_evidence_table_key_alias(self):
        # Some consumers nest the table under claimEvidenceTable.
        raw = {"claimEvidenceTable": "| A | B |", "rowCount": 2}
        env = emit_build_claim_evidence_table(raw)
        assert env.claim_evidence_artifact is not None
        assert env.claim_evidence_artifact.row_count == 2

    def test_rejects_blank_required_table(self):
        with pytest.raises(ValueError, match="claimEvidenceArtifact.table"):
            emit_build_claim_evidence_table({"table": "   ", "rowCount": 1})


# ---------------------------------------------------------------------------
# emit_paper_search
# ---------------------------------------------------------------------------


class TestEmitPaperSearch:
    def test_papers_populated(self):
        raw = {
            "action": "research.search",
            "papers": [_citation_map(1), _citation_map(2)],
        }
        env = emit_paper_search(raw)
        assert env.paper_bundle is not None
        assert len(env.paper_bundle.papers) == 2

    def test_flatten_has_papers_key(self):
        raw = {"papers": [_citation_map(1)]}
        leg = flatten_to_legacy(emit_paper_search(raw))
        assert "papers" in leg
        assert leg["papers"][0]["title"] == "Citation 1"
        assert "arxivId" in leg["papers"][0]

    def test_empty(self):
        env = emit_paper_search({})
        assert env.paper_bundle is None

    def test_rejects_missing_required_title_from_schema(self):
        with pytest.raises(ValueError, match="paperBundle.papers\\[0\\]\\.title"):
            emit_paper_search({"papers": [{"id": "p1", "doi": "10.0/1"}]})


# ---------------------------------------------------------------------------
# emit_for_action dispatch
# ---------------------------------------------------------------------------


class TestEmitForActionDispatch:
    @pytest.mark.parametrize(
        "action,raw,expected_bundle",
        [
            (
                "research.resolveCanonicalCitations",
                {"canonicalSources": [_citation_map(1)]},
                "citation_bundle",
            ),
            (
                "research.verifyCitations",
                {"verifiedRecords": [_citation_map(1)]},
                "citation_bundle",
            ),
            (
                "research.proposeHypotheses",
                {"hypotheses": [_branch_map("H1")]},
                "reasoning_bundle",
            ),
            (
                "research.generateHypotheses",
                {"hypotheses": [_branch_map("H2")]},
                "reasoning_bundle",
            ),
            (
                "research.verifyReasoningPaths",
                {
                    "totalBranches": 1,
                    "verifiedBranches": 1,
                    "rejectedBranches": 0,
                    "readyForSynthesis": True,
                    "branches": [_branch_map("H1")],
                },
                "reasoning_bundle",
            ),
            (
                "research.buildClaimEvidenceTable",
                {"table": "T"},
                "claim_evidence_artifact",
            ),
        ],
    )
    def test_dispatch_correct_bundle(self, action, raw, expected_bundle):
        env = emit_for_action(action, raw)
        assert getattr(env, expected_bundle) is not None, (
            f"Expected {expected_bundle} for action={action}"
        )

    def test_dispatch_unknown_action_passthrough(self):
        raw = {"someKey": "value"}
        env = emit_for_action("unknown.action", raw)
        assert env.paper_bundle is None
        assert env.citation_bundle is None
        assert env.reasoning_bundle is None
        assert env.claim_evidence_artifact is None
        assert env.artifacts["someKey"] == "value"

    def test_dispatch_preserves_action_name(self):
        env = emit_for_action("research.resolveCanonicalCitations", {})
        assert env.action == "research.resolveCanonicalCitations"


class TestEmitterShapeRegression:
    def test_canonical_sources_key_shape_is_stable(self):
        leg = flatten_to_legacy(emit_resolve_citations({"canonicalSources": "bad-shape"}))
        assert isinstance(leg["canonicalSources"], list)
        assert isinstance(leg["citations"], list)

    def test_verified_records_key_shape_is_stable(self):
        leg = flatten_to_legacy(emit_verify_citations({"verifiedRecords": "bad-shape"}))
        assert isinstance(leg["verifiedRecords"], list)
        assert isinstance(leg["citations"], list)

    def test_branches_key_shape_is_stable(self):
        leg = flatten_to_legacy(emit_propose_hypotheses({"branches": "bad-shape"}))
        assert isinstance(leg["branches"], list)

    def test_reasoning_verification_shape_is_stable(self):
        leg = flatten_to_legacy(emit_verify_reasoning_paths({"reasoningVerification": "bad-shape"}))
        assert isinstance(leg["reasoningVerification"], dict)
        assert set(leg["reasoningVerification"].keys()) == {
            "totalBranches",
            "verifiedBranches",
            "rejectedBranches",
            "readyForSynthesis",
        }

    def test_claim_evidence_table_shape_is_stable(self):
        leg = flatten_to_legacy(emit_build_claim_evidence_table({"claimEvidenceTable": 123}))
        assert isinstance(leg["claimEvidenceTable"], dict)
        assert set(leg["claimEvidenceTable"].keys()) == {"table", "rowCount"}
