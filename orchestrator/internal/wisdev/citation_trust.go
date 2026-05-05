package wisdev

import (
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/evidence/citations"
)

func NormalizeCitationVerificationStatus(raw string, verified, resolved bool) CitationVerificationStatus {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(CitationStatusVerified):
		return CitationStatusVerified
	case string(CitationStatusAmbiguous), "duplicate":
		return CitationStatusAmbiguous
	case string(CitationStatusRejected), "invalid", "unresolved":
		return CitationStatusRejected
	}
	switch {
	case verified:
		return CitationStatusVerified
	case resolved:
		return CitationStatusAmbiguous
	default:
		return CitationStatusRejected
	}
}

func canonicalCitationWithTrustDefaults(record CanonicalCitation) CanonicalCitation {
	record.VerificationStatus = NormalizeCitationVerificationStatus(string(record.VerificationStatus), record.Verified, record.Resolved)
	if record.ResolutionEngine == "" {
		record.ResolutionEngine = "wisdev"
	}
	if record.SourceAuthority == "" {
		switch {
		case strings.TrimSpace(record.DOI) != "":
			record.SourceAuthority = "doi"
		case strings.TrimSpace(record.ArxivID) != "":
			record.SourceAuthority = "arxiv"
		default:
			record.SourceAuthority = "heuristic"
		}
	}
	if record.ResolverAgreementCount == 0 && record.VerificationStatus == CitationStatusVerified {
		record.ResolverAgreementCount = 1
	}
	if record.CanonicalID == "" {
		record.CanonicalID = firstNonEmpty(strings.TrimSpace(record.DOI), strings.TrimSpace(record.ArxivID), strings.TrimSpace(record.Title))
	}
	if record.LandingURL == "" {
		switch {
		case strings.TrimSpace(record.DOI) != "":
			record.LandingURL = "https://doi.org/" + strings.TrimSpace(record.DOI)
		case strings.TrimSpace(record.ArxivID) != "":
			record.LandingURL = "https://arxiv.org/abs/" + strings.TrimSpace(record.ArxivID)
		}
	}
	if record.ProvenanceHash == "" {
		record.ProvenanceHash = ComputeTraceIntegrityHash(map[string]any{
			"id":                 record.ID,
			"title":              record.Title,
			"doi":                record.DOI,
			"arxivId":            record.ArxivID,
			"canonicalId":        record.CanonicalID,
			"sourceAuthority":    record.SourceAuthority,
			"resolutionEngine":   record.ResolutionEngine,
			"verificationStatus": record.VerificationStatus,
			"resolverConflict":   record.ResolverConflict,
			"resolverAgreement":  record.ResolverAgreementCount,
			"semanticScholarId":  record.SemanticScholarID,
			"openAlexId":         record.OpenAlexID,
			"landingUrl":         record.LandingURL,
		})
	}
	return record
}

func citationTrustBlockingIssues(records []CanonicalCitation, issues []string) []string {
	out := make([]string, 0, len(issues)+len(records))
	for _, issue := range issues {
		if trimmed := strings.TrimSpace(issue); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	for _, record := range records {
		switch canonicalCitationWithTrustDefaults(record).VerificationStatus {
		case CitationStatusAmbiguous:
			key := firstNonEmpty(record.CanonicalID, record.DOI, record.ArxivID, record.Title, record.ID)
			note := strings.TrimSpace(record.ConflictNote)
			if note == "" {
				note = "ambiguous citation record"
			}
			out = append(out, "ambiguous:"+key+":"+note)
		case CitationStatusRejected:
			key := firstNonEmpty(record.CanonicalID, record.DOI, record.ArxivID, record.Title, record.ID)
			note := strings.TrimSpace(record.ConflictNote)
			if note == "" {
				note = "rejected citation record"
			}
			out = append(out, "rejected:"+key+":"+note)
		}
	}
	return dedupeTrimmedStrings(out)
}

func BuildCitationTrustBundle(records []CanonicalCitation, trace []map[string]any, issues []string) *CitationTrustBundle {
	if len(records) == 0 && len(trace) == 0 && len(issues) == 0 {
		return nil
	}
	normalized := make([]CanonicalCitation, 0, len(records))
	verifiedCount := 0
	ambiguousCount := 0
	rejectedCount := 0
	for _, record := range records {
		item := canonicalCitationWithTrustDefaults(record)
		switch item.VerificationStatus {
		case CitationStatusVerified:
			verifiedCount++
		case CitationStatusAmbiguous:
			ambiguousCount++
		default:
			rejectedCount++
		}
		normalized = append(normalized, item)
	}
	if len(trace) == 0 {
		trace = resolverTraceFromCitations(normalized)
	}
	blocking := citationTrustBlockingIssues(normalized, issues)
	promotionGate := buildCitationPromotionGate(normalized, blocking)
	return &CitationTrustBundle{
		Citations:         normalized,
		VerifiedCount:     verifiedCount,
		AmbiguousCount:    ambiguousCount,
		RejectedCount:     rejectedCount,
		ResolverTrace:     trace,
		PromotionEligible: toBool(promotionGate["promoted"]),
		PromotionGate:     promotionGate,
		BlockingIssues:    blocking,
	}
}

func citationTrustBundleToMap(bundle *CitationTrustBundle) map[string]any {
	if bundle == nil {
		return nil
	}
	trace := make([]any, 0, len(bundle.ResolverTrace))
	for _, item := range bundle.ResolverTrace {
		trace = append(trace, item)
	}
	blocking := make([]any, 0, len(bundle.BlockingIssues))
	for _, issue := range bundle.BlockingIssues {
		blocking = append(blocking, issue)
	}
	return map[string]any{
		"citations":         mapsToAny(typedCitationsToMaps(bundle.Citations)),
		"verifiedCount":     bundle.VerifiedCount,
		"ambiguousCount":    bundle.AmbiguousCount,
		"rejectedCount":     bundle.RejectedCount,
		"resolverTrace":     trace,
		"promotionEligible": bundle.PromotionEligible,
		"promotionGate":     cloneAnyMap(bundle.PromotionGate),
		"blockingIssues":    blocking,
	}
}

func citationTrustBundleFromMap(raw map[string]any, fallback []CanonicalCitation) *CitationTrustBundle {
	if raw == nil {
		return BuildCitationTrustBundle(fallback, nil, nil)
	}
	records := citationMapsToTyped(firstArtifactMaps(raw["citations"]))
	if len(records) == 0 {
		records = append(records, fallback...)
	}
	trace := firstArtifactMaps(raw["resolverTrace"])
	issues := toStringSlice(raw["blockingIssues"])
	bundle := BuildCitationTrustBundle(records, trace, issues)
	if bundle == nil {
		return nil
	}
	if count := toInt(raw["verifiedCount"]); count > 0 {
		bundle.VerifiedCount = count
	}
	if count := toInt(raw["ambiguousCount"]); count > 0 {
		bundle.AmbiguousCount = count
	}
	if count := toInt(raw["rejectedCount"]); count > 0 {
		bundle.RejectedCount = count
	}
	if rawEligible, ok := raw["promotionEligible"]; ok {
		bundle.PromotionEligible = toBool(rawEligible)
	}
	if gate, ok := raw["promotionGate"].(map[string]any); ok {
		bundle.PromotionGate = cloneAnyMap(gate)
	}
	return bundle
}

func buildCitationTrustBundleFromResult(result map[string]any, fallback []CanonicalCitation) *CitationTrustBundle {
	if raw, ok := result["citationTrustBundle"].(map[string]any); ok {
		return citationTrustBundleFromMap(raw, fallback)
	}
	return BuildCitationTrustBundle(fallback, firstArtifactMaps(result["resolverTrace"]), toStringSlice(result["issues"]))
}

func citationTrustBundlePromotionEligible(bundle *CitationTrustBundle) bool {
	return bundle != nil && bundle.PromotionEligible
}

func resolverTraceFromCitations(records []CanonicalCitation) []map[string]any {
	if len(records) == 0 {
		return nil
	}
	trace := make([]map[string]any, 0, len(records))
	for _, record := range records {
		item := canonicalCitationWithTrustDefaults(record)
		trace = append(trace, map[string]any{
			"id":          item.ID,
			"authority":   item.SourceAuthority,
			"engine":      item.ResolutionEngine,
			"status":      item.VerificationStatus,
			"canonicalId": item.CanonicalID,
		})
	}
	return trace
}

func buildCitationPromotionGate(records []CanonicalCitation, blocking []string) map[string]any {
	resolved := make([]citations.ResolvedCitation, 0, len(records))
	singleVerified := len(records) == 1
	allRecordsHaveResolverAgreement := len(records) > 0
	for _, record := range records {
		item := canonicalCitationWithTrustDefaults(record)
		if item.VerificationStatus != CitationStatusVerified {
			singleVerified = false
		}
		if item.VerificationStatus != CitationStatusVerified || item.ResolverAgreementCount < 2 || item.ResolverConflict {
			allRecordsHaveResolverAgreement = false
		}
		resolved = append(resolved, citations.ResolvedCitation{
			CanonicalID:          item.CanonicalID,
			Title:                item.Title,
			DOI:                  item.DOI,
			ArxivID:              item.ArxivID,
			OpenAlexID:           item.OpenAlexID,
			SemanticScholarID:    item.SemanticScholarID,
			LandingURL:           item.LandingURL,
			Year:                 item.Year,
			Authors:              append([]string(nil), item.Authors...),
			Resolved:             item.Resolved,
			ResolutionEngine:     item.ResolutionEngine,
			ResolutionConfidence: 1.0,
		})
	}
	verdict := citations.EvaluatePromotion(resolved, 2)
	promoted := len(blocking) == 0 && (verdict.Promoted || allRecordsHaveResolverAgreement)
	consensusMode := "blocked"
	switch {
	case verdict.Promoted:
		consensusMode = "multi_source"
	case allRecordsHaveResolverAgreement:
		consensusMode = "record_level_multi_source"
	case singleVerified:
		consensusMode = "single_source_insufficient"
	}
	if len(blocking) > 0 {
		consensusMode = "blocked"
	}
	conflictNote := strings.TrimSpace(verdict.ConflictNote)
	if !promoted && len(blocking) > 0 {
		conflictNote = firstNonEmpty(conflictNote, "citation trust bundle contains blocking issues")
	} else if promoted && allRecordsHaveResolverAgreement && !verdict.Promoted {
		conflictNote = ""
	}
	return map[string]any{
		"promoted":         promoted,
		"consensusMode":    consensusMode,
		"canonicalId":      verdict.Canonical.CanonicalID,
		"agreementSources": stringSliceToAny(append([]string(nil), verdict.AgreementSources...)),
		"conflictNote":     conflictNote,
		"blockingIssues":   stringSliceToAny(blocking),
	}
}

func dedupeTrimmedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func stringSliceToAny(values []string) []any {
	if len(values) == 0 {
		return []any{}
	}
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}
