package wisdev

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

type artifactSchemaMetadataDocument struct {
	Version     string                              `json:"version"`
	Type        any                                 `json:"type"`
	Properties  map[string]artifactSchemaNode       `json:"properties"`
	Definitions map[string]artifactSchemaDefinition `json:"definitions"`
	Required    []string                            `json:"required"`
}

type artifactSchemaDefinition struct {
	Type       any                           `json:"type"`
	Ref        string                        `json:"$ref"`
	Properties map[string]artifactSchemaNode `json:"properties"`
	Items      *artifactSchemaNode           `json:"items"`
	Required   []string                      `json:"required"`
}

type artifactSchemaNode struct {
	Type       any                           `json:"type"`
	Ref        string                        `json:"$ref"`
	Properties map[string]artifactSchemaNode `json:"properties"`
	Items      *artifactSchemaNode           `json:"items"`
	Required   []string                      `json:"required"`
}

var (
	artifactSchemaMetadataOnce sync.Once
	artifactSchemaMetadata     map[string]any
	artifactSchemaDocumentOnce sync.Once
	artifactSchemaDocCache     artifactSchemaMetadataDocument
	artifactSchemaDocErr       error
)

func ArtifactSchemaMetadata() map[string]any {
	artifactSchemaMetadataOnce.Do(func() {
		bundles := []string{
			"paperBundle",
			"citationBundle",
			"citationTrustBundle",
			"reasoningBundle",
			"claimEvidenceArtifact",
		}
		version := ARTIFACT_SCHEMA_VERSION

		if doc, err := canonicalArtifactSchemaDocument(); err == nil {
			if strings.TrimSpace(doc.Version) != "" {
				version = doc.Version
			}
			if len(doc.Properties) > 0 {
				keys := make([]string, 0, len(doc.Properties))
				for key := range doc.Properties {
					if key == "action" || key == "schemaVersion" || key == "artifacts" {
						continue
					}
					keys = append(keys, key)
				}
				sort.Strings(keys)
				if len(keys) > 0 {
					bundles = keys
				}
			}
		}

		artifactSchemaMetadata = map[string]any{
			"version": version,
			"bundles": bundles,
			"legacyKeys": []string{
				"papers",
				"retrievalTrace",
				"retrievalStrategies",
				"queryUsed",
				"traceId",
				"citations",
				"canonicalSources",
				"verifiedRecords",
				"branches",
				"reasoningVerification",
				"claimEvidenceTable",
			},
		}
	})
	return artifactSchemaMetadata
}

func loadArtifactSchemaMetadataDocument() (artifactSchemaMetadataDocument, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return artifactSchemaMetadataDocument{}, fmt.Errorf("resolve artifact schema path: missing caller")
	}
	path, err := resolveArtifactSchemaPath(filepath.Dir(file))
	if err != nil {
		return artifactSchemaMetadataDocument{}, err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return artifactSchemaMetadataDocument{}, err
	}
	var doc artifactSchemaMetadataDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return artifactSchemaMetadataDocument{}, err
	}
	return doc, nil
}

func resolveArtifactSchemaPath(startDir string) (string, error) {
	dir := filepath.Clean(startDir)
	for {
		path := filepath.Join(dir, "schema", "artifact_schema_v1.json")
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not locate schema/artifact_schema_v1.json from %s", startDir)
		}
		dir = parent
	}
}

func canonicalArtifactSchemaDocument() (artifactSchemaMetadataDocument, error) {
	artifactSchemaDocumentOnce.Do(func() {
		artifactSchemaDocCache, artifactSchemaDocErr = loadArtifactSchemaMetadataDocument()
	})
	return artifactSchemaDocCache, artifactSchemaDocErr
}

func artifactSchemaNodeTypes(node artifactSchemaNode) []string {
	switch v := node.Type.(type) {
	case string:
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func resolveArtifactSchemaNode(doc artifactSchemaMetadataDocument, node artifactSchemaNode) artifactSchemaNode {
	if !strings.HasPrefix(node.Ref, "#/definitions/") {
		return node
	}
	name := strings.TrimPrefix(node.Ref, "#/definitions/")
	if def, ok := doc.Definitions[name]; ok {
		return artifactSchemaNode{
			Type:       def.Type,
			Ref:        def.Ref,
			Properties: def.Properties,
			Items:      def.Items,
			Required:   def.Required,
		}
	}
	return node
}

func artifactSchemaNodeAllowsString(node artifactSchemaNode) bool {
	for _, typ := range artifactSchemaNodeTypes(node) {
		if typ == "string" {
			return true
		}
	}
	return false
}

func validateArtifactSchemaValue(doc artifactSchemaMetadataDocument, node artifactSchemaNode, value any, path string) error {
	resolved := resolveArtifactSchemaNode(doc, node)
	nodeTypes := artifactSchemaNodeTypes(resolved)
	isObject := len(resolved.Properties) > 0 || len(resolved.Required) > 0
	for _, typ := range nodeTypes {
		if typ == "object" {
			isObject = true
			break
		}
	}
	if isObject {
		m, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		for _, field := range resolved.Required {
			childPath := field
			if path != "" {
				childPath = path + "." + field
			}
			childNode, _ := resolved.Properties[field]
			childValue, ok := m[field]
			if !ok || childValue == nil {
				return fmt.Errorf("artifact schema violation: %s is required", childPath)
			}
			if artifactSchemaNodeAllowsString(resolveArtifactSchemaNode(doc, childNode)) && strings.TrimSpace(AsOptionalString(childValue)) == "" {
				return fmt.Errorf("artifact schema violation: %s is required", childPath)
			}
		}
		for field, childNode := range resolved.Properties {
			childValue, ok := m[field]
			if !ok {
				continue
			}
			childPath := field
			if path != "" {
				childPath = path + "." + field
			}
			if err := validateArtifactSchemaValue(doc, childNode, childValue, childPath); err != nil {
				return err
			}
		}
		return nil
	}
	for _, typ := range nodeTypes {
		if typ == "array" {
			switch items := value.(type) {
			case []any:
				for idx, item := range items {
					if resolved.Items == nil {
						continue
					}
					if err := validateArtifactSchemaValue(doc, *resolved.Items, item, fmt.Sprintf("%s[%d]", path, idx)); err != nil {
						return err
					}
				}
			case []map[string]any:
				for idx, item := range items {
					if resolved.Items == nil {
						continue
					}
					if err := validateArtifactSchemaValue(doc, *resolved.Items, item, fmt.Sprintf("%s[%d]", path, idx)); err != nil {
						return err
					}
				}
			}
			return nil
		}
	}
	return nil
}

func stepArtifactSetSchemaEnvelope(set StepArtifactSet) map[string]any {
	envelope := map[string]any{
		"action":        set.Action,
		"schemaVersion": ARTIFACT_SCHEMA_VERSION,
		"artifacts":     set.Artifacts,
	}
	if set.PaperBundle != nil {
		envelope["paperBundle"] = map[string]any{
			"papers":              mapsToAny(sourcesToArtifactMaps(set.PaperBundle.Papers)),
			"retrievalStrategies": stringSliceToAny(set.PaperBundle.RetrievalStrategies),
			"retrievalTrace":      mapsToAny(set.PaperBundle.RetrievalTrace),
			"queryUsed":           set.PaperBundle.QueryUsed,
			"traceId":             set.PaperBundle.TraceID,
		}
	}
	if set.CitationBundle != nil {
		envelope["citationBundle"] = map[string]any{
			"citations":        mapsToAny(typedCitationsToMaps(set.CitationBundle.Citations)),
			"canonicalSources": mapsToAny(typedCitationsToMaps(set.CitationBundle.CanonicalSources)),
			"verifiedRecords":  mapsToAny(typedCitationsToMaps(set.CitationBundle.VerifiedRecords)),
			"resolvedCount":    set.CitationBundle.ResolvedCount,
			"validCount":       set.CitationBundle.ValidCount,
			"invalidCount":     set.CitationBundle.InvalidCount,
			"duplicateCount":   set.CitationBundle.DuplicateCount,
		}
	}
	if set.CitationTrustBundle != nil {
		envelope["citationTrustBundle"] = citationTrustBundleToMap(set.CitationTrustBundle)
	}
	if set.ReasoningBundle != nil {
		reasoningBundle := map[string]any{
			"branches":              mapsToAny(typedBranchesToMaps(set.ReasoningBundle.Branches)),
			"minimumReasoningPaths": set.ReasoningBundle.MinimumReasoningPaths,
		}
		if set.ReasoningBundle.Verification != nil {
			reasoningBundle["verification"] = map[string]any{
				"totalBranches":     set.ReasoningBundle.Verification.TotalBranches,
				"verifiedBranches":  set.ReasoningBundle.Verification.VerifiedBranches,
				"rejectedBranches":  set.ReasoningBundle.Verification.RejectedBranches,
				"readyForSynthesis": set.ReasoningBundle.Verification.ReadyForSynthesis,
			}
		}
		envelope["reasoningBundle"] = reasoningBundle
	}
	if set.ClaimEvidenceArtifact != nil {
		envelope["claimEvidenceArtifact"] = map[string]any{
			"table":    set.ClaimEvidenceArtifact.Table,
			"rowCount": set.ClaimEvidenceArtifact.RowCount,
		}
	}
	return envelope
}

func validateStepArtifactSetAgainstCanonicalSchema(set StepArtifactSet) error {
	doc, err := canonicalArtifactSchemaDocument()
	if err != nil {
		return nil
	}
	if set.CitationBundle != nil {
		for i, source := range set.CitationBundle.CanonicalSources {
			if strings.TrimSpace(source.Title) == "" {
				return fmt.Errorf("artifact schema violation: citationBundle.canonicalSources[%d].title is required", i)
			}
		}
	}
	if set.CitationBundle != nil && set.CitationTrustBundle == nil {
		return fmt.Errorf("artifact schema violation: citationTrustBundle is required when citationBundle is present")
	}
	return validateArtifactSchemaValue(doc, artifactSchemaNode{
		Type:       doc.Type,
		Properties: doc.Properties,
		Required:   doc.Required,
	}, stepArtifactSetSchemaEnvelope(set), "")
}

func ensurePlanArtifactState(plan *PlanState) {
	if plan == nil {
		return
	}
	if plan.StepArtifacts == nil {
		plan.StepArtifacts = make(map[string]StepArtifactSet)
	}
}

func sourceToArtifactMap(source Source) map[string]any {
	artifact := map[string]any{
		"id":            source.ID,
		"title":         source.Title,
		"abstract":      firstNonEmpty(source.Summary, source.Title),
		"summary":       source.Summary,
		"link":          source.Link,
		"doi":           source.DOI,
		"arxivId":       source.ArxivID,
		"source":        source.Source,
		"sourceApis":    source.SourceApis,
		"authors":       source.Authors,
		"year":          source.Year,
		"score":         source.Score,
		"citationCount": source.CitationCount,
	}
	if strings.TrimSpace(source.FullText) != "" {
		artifact["fullText"] = source.FullText
	}
	if len(source.StructureMap) > 0 {
		artifact["structureMap"] = append([]any(nil), source.StructureMap...)
	}
	return artifact
}

func sourcesToArtifactMaps(sources []Source) []map[string]any {
	out := make([]map[string]any, 0, len(sources))
	for _, source := range sources {
		out = append(out, sourceToArtifactMap(source))
	}
	return out
}

func mapsToAny(items []map[string]any) []any {
	out := make([]any, 0, len(items))
	for _, item := range items {
		out = append(out, item)
	}
	return out
}

func firstArtifactMaps(value any) []map[string]any {
	switch v := value.(type) {
	case []map[string]any:
		return v
	case []any:
		out := make([]map[string]any, 0, len(v))
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	default:
		return nil
	}
}

func firstArtifactValue(values ...any) any {
	for _, value := range values {
		switch typed := value.(type) {
		case nil:
			continue
		case []any:
			if len(typed) > 0 {
				return typed
			}
		default:
			return value
		}
	}
	return nil
}

func toArtifactAnySlice(value any) []any {
	switch typed := value.(type) {
	case nil:
		return nil
	case []any:
		return append([]any(nil), typed...)
	default:
		return []any{typed}
	}
}

func hypothesesToBranches(raw any) []ReasoningBranch {
	branches := make([]ReasoningBranch, 0)
	switch v := raw.(type) {
	case []Hypothesis:
		for _, hypothesis := range v {
			branches = append(branches, ReasoningBranch{
				Claim:                   hypothesis.Claim,
				FalsifiabilityCondition: hypothesis.FalsifiabilityCondition,
				SupportScore:            hypothesis.ConfidenceThreshold,
				IsTerminated:            hypothesis.IsTerminated,
				Source:                  "hypothesis",
			})
		}
	case []any:
		for _, item := range v {
			if branch, ok := item.(map[string]any); ok {
				branches = append(branches, reasoningBranchFromMap(branch))
			}
		}
	}
	return branches
}

func ArtifactMapsToSources(items []map[string]any) []Source {
	out := make([]Source, 0, len(items))
	for _, item := range items {
		out = append(out, Source{
			ID:            AsOptionalString(item["id"]),
			Title:         AsOptionalString(item["title"]),
			Summary:       firstNonEmpty(AsOptionalString(item["summary"]), AsOptionalString(item["abstract"])),
			Link:          firstNonEmpty(AsOptionalString(item["link"]), AsOptionalString(item["url"])),
			DOI:           AsOptionalString(item["doi"]),
			ArxivID:       firstNonEmpty(AsOptionalString(item["arxivId"]), AsOptionalString(item["arxiv"])),
			Source:        AsOptionalString(item["source"]),
			SourceApis:    toStringSlice(item["sourceApis"]),
			Authors:       toStringSlice(item["authors"]),
			Year:          toInt(item["year"]),
			Score:         toFloat(item["score"]),
			CitationCount: toInt(item["citationCount"]),
			FullText:      firstNonEmpty(AsOptionalString(item["fullText"]), AsOptionalString(item["full_text"])),
			StructureMap:  toArtifactAnySlice(firstArtifactValue(item["structureMap"], item["structure_map"])),
		})
	}
	return out
}

func canonicalCitationFromMap(record map[string]any) CanonicalCitation {
	item := CanonicalCitation{
		ID:                     AsOptionalString(record["id"]),
		Title:                  AsOptionalString(record["title"]),
		DOI:                    AsOptionalString(record["doi"]),
		ArxivID:                firstNonEmpty(AsOptionalString(record["arxivId"]), AsOptionalString(record["arxiv_id"]), AsOptionalString(record["arxiv"])),
		CanonicalID:            firstNonEmpty(AsOptionalString(record["canonicalId"]), AsOptionalString(record["canonical_id"])),
		Authors:                toStringSlice(record["authors"]),
		Year:                   toInt(record["year"]),
		Resolved:               toBool(record["resolved"]),
		Verified:               toBool(record["verified"]),
		VerificationStatus:     CitationVerificationStatus(firstNonEmpty(AsOptionalString(record["verificationStatus"]), AsOptionalString(record["verification_status"]))),
		SourceAuthority:        firstNonEmpty(AsOptionalString(record["sourceAuthority"]), AsOptionalString(record["source_authority"])),
		ResolutionEngine:       firstNonEmpty(AsOptionalString(record["resolutionEngine"]), AsOptionalString(record["resolution_engine"]), AsOptionalString(record["engine"])),
		ResolverAgreementCount: toInt(firstArtifactValue(record["resolverAgreementCount"], record["resolver_agreement_count"])),
		ResolverConflict:       toBool(firstArtifactValue(record["resolverConflict"], record["resolver_conflict"])),
		ConflictNote:           firstNonEmpty(AsOptionalString(record["conflictNote"]), AsOptionalString(record["conflict_note"])),
		LandingURL:             firstNonEmpty(AsOptionalString(record["landingUrl"]), AsOptionalString(record["landing_url"])),
		SemanticScholarID:      firstNonEmpty(AsOptionalString(record["semanticScholarId"]), AsOptionalString(record["semantic_scholar_id"])),
		OpenAlexID:             firstNonEmpty(AsOptionalString(record["openAlexId"]), AsOptionalString(record["open_alex_id"])),
		ProvenanceHash:         firstNonEmpty(AsOptionalString(record["provenanceHash"]), AsOptionalString(record["provenance_hash"])),
	}
	return canonicalCitationWithTrustDefaults(item)
}

func citationMapsToTyped(records []map[string]any) []CanonicalCitation {
	out := make([]CanonicalCitation, 0, len(records))
	for _, record := range records {
		out = append(out, canonicalCitationFromMap(record))
	}
	return out
}

func typedCitationsToMaps(records []CanonicalCitation) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		record = canonicalCitationWithTrustDefaults(record)
		out = append(out, map[string]any{
			"id":                     record.ID,
			"title":                  record.Title,
			"doi":                    record.DOI,
			"arxivId":                record.ArxivID,
			"canonicalId":            record.CanonicalID,
			"authors":                record.Authors,
			"year":                   record.Year,
			"resolved":               record.Resolved,
			"verified":               record.Verified,
			"verificationStatus":     record.VerificationStatus,
			"sourceAuthority":        record.SourceAuthority,
			"resolutionEngine":       record.ResolutionEngine,
			"resolverAgreementCount": record.ResolverAgreementCount,
			"resolverConflict":       record.ResolverConflict,
			"conflictNote":           record.ConflictNote,
			"landingUrl":             record.LandingURL,
			"semanticScholarId":      record.SemanticScholarID,
			"openAlexId":             record.OpenAlexID,
			"provenanceHash":         record.ProvenanceHash,
		})
	}
	return out
}

func reasoningBranchFromMap(branch map[string]any) ReasoningBranch {
	return ReasoningBranch{
		Claim:                   AsOptionalString(branch["claim"]),
		FalsifiabilityCondition: AsOptionalString(branch["falsifiabilityCondition"]),
		SupportScore:            toFloat(branch["supportScore"]),
		IsTerminated:            toBool(branch["isTerminated"]),
		Source:                  AsOptionalString(branch["source"]),
	}
}

func typedBranchesToMaps(branches []ReasoningBranch) []map[string]any {
	out := make([]map[string]any, 0, len(branches))
	for _, branch := range branches {
		out = append(out, map[string]any{
			"claim":                   branch.Claim,
			"falsifiabilityCondition": branch.FalsifiabilityCondition,
			"supportScore":            branch.SupportScore,
			"isTerminated":            branch.IsTerminated,
			"source":                  branch.Source,
		})
	}
	return out
}

func hasResultKey(result map[string]any, key string) bool {
	if result == nil {
		return false
	}
	_, ok := result[key]
	return ok
}

func retrievalTraceFromResult(result map[string]any) []map[string]any {
	return firstArtifactMaps(result["retrievalTrace"])
}

func populateRetrievePaperArtifacts(bundle *PaperArtifactBundle, artifacts map[string]any) {
	if bundle == nil || artifacts == nil {
		return
	}
	if len(bundle.RetrievalStrategies) > 0 {
		artifacts["retrievalStrategies"] = stringSliceToAny(bundle.RetrievalStrategies)
	}
	if len(bundle.RetrievalTrace) > 0 {
		artifacts["retrievalTrace"] = mapsToAny(bundle.RetrievalTrace)
	}
	if strings.TrimSpace(bundle.QueryUsed) != "" {
		artifacts["queryUsed"] = bundle.QueryUsed
	}
	if strings.TrimSpace(bundle.TraceID) != "" {
		artifacts["traceId"] = bundle.TraceID
	}
}

func validateEmitterIngressKeys(action string, result map[string]any) error {
	if len(result) == 0 {
		return nil
	}
	switch action {
	case "research.retrievePapers":
		if !hasResultKey(result, "papers") {
			return fmt.Errorf("ingress artifact contract mismatch for %s: missing key papers", action)
		}
	case "research.resolveCanonicalCitations":
		if !hasResultKey(result, "canonicalSources") && !hasResultKey(result, "citations") {
			return fmt.Errorf("ingress artifact contract mismatch for %s: missing key canonicalSources", action)
		}
	case "research.verifyCitations":
		if !hasResultKey(result, "verifiedRecords") && !hasResultKey(result, "citations") {
			return fmt.Errorf("ingress artifact contract mismatch for %s: missing key verifiedRecords", action)
		}
	case "research.proposeHypotheses", "research.generateHypotheses":
		if !hasResultKey(result, "branches") && !hasResultKey(result, "hypotheses") {
			return fmt.Errorf("ingress artifact contract mismatch for %s: missing key branches", action)
		}
	case "research.verifyReasoningPaths":
		hasSummary := hasResultKey(result, "reasoningVerification") || hasResultKey(result, "totalBranches") || hasResultKey(result, "verifiedBranches") || hasResultKey(result, "rejectedBranches") || hasResultKey(result, "readyForSynthesis")
		if !hasSummary {
			return fmt.Errorf("ingress artifact contract mismatch for %s: missing key reasoningVerification", action)
		}
		if !hasResultKey(result, "branches") && !hasResultKey(result, "hypotheses") {
			return fmt.Errorf("ingress artifact contract mismatch for %s: missing key branches", action)
		}
	case "research.buildClaimEvidenceTable":
		if !hasResultKey(result, "claimEvidenceTable") && !hasResultKey(result, "table") {
			return fmt.Errorf("ingress artifact contract mismatch for %s: missing key claimEvidenceTable", action)
		}
	}
	return nil
}

func initStepArtifactSet(step PlanStep, result map[string]any, sources []Source) StepArtifactSet {
	artifactSet := StepArtifactSet{
		StepID:    step.ID,
		Action:    step.Action,
		Artifacts: make(map[string]any),
		CreatedAt: NowMillis(),
	}
	for key, value := range result {
		artifactSet.Artifacts[key] = value
	}
	if len(sources) > 0 {
		artifactSet.PaperBundle = &PaperArtifactBundle{Papers: sources}
		artifactSet.Artifacts["papers"] = mapsToAny(sourcesToArtifactMaps(sources))
	}
	return artifactSet
}

func annotateArtifactNormalizationFailure(artifactSet StepArtifactSet, stage string, err error) StepArtifactSet {
	if artifactSet.Artifacts == nil {
		artifactSet.Artifacts = make(map[string]any)
	}
	artifactSet.Artifacts["artifactNormalizationDegraded"] = true
	artifactSet.Artifacts["artifactNormalizationStage"] = strings.TrimSpace(stage)
	if err != nil {
		artifactSet.Artifacts["artifactNormalizationError"] = err.Error()
		code := "artifact_normalization_failed"
		switch strings.TrimSpace(stage) {
		case "ingress_validation":
			code = "artifact_ingress_contract_mismatch"
		case "schema_validation":
			code = "artifact_schema_violation"
		}
		artifactSet.Artifacts["artifactNormalizationErrorCode"] = code
	}
	return artifactSet
}

func normalizeStepArtifacts(step PlanStep, result map[string]any, sources []Source) (StepArtifactSet, error) {
	artifactSet := initStepArtifactSet(step, result, sources)
	if err := validateEmitterIngressKeys(step.Action, result); err != nil {
		return annotateArtifactNormalizationFailure(artifactSet, "ingress_validation", err), err
	}

	switch step.Action {
	case "research.retrievePapers":
		if artifactSet.PaperBundle == nil {
			papers := ArtifactMapsToSources(firstArtifactMaps(result["papers"]))
			if len(papers) > 0 {
				artifactSet.PaperBundle = &PaperArtifactBundle{Papers: papers}
			}
		}
		if artifactSet.PaperBundle != nil {
			artifactSet.PaperBundle.RetrievalStrategies = toStringSlice(result["retrievalStrategies"])
			artifactSet.PaperBundle.RetrievalTrace = retrievalTraceFromResult(result)
			artifactSet.PaperBundle.QueryUsed = AsOptionalString(result["queryUsed"])
			artifactSet.PaperBundle.TraceID = AsOptionalString(result["traceId"])
			populateRetrievePaperArtifacts(artifactSet.PaperBundle, artifactSet.Artifacts)
		}
	case "research.generateHypotheses", "research.proposeHypotheses":
		branches := hypothesesToBranches(result["hypotheses"])
		if len(branches) > 0 {
			artifactSet.ReasoningBundle = &ReasoningArtifactBundle{Branches: branches}
			artifactSet.Artifacts["branches"] = mapsToAny(typedBranchesToMaps(branches))
		}
	case "research.resolveCanonicalCitations":
		canonical := citationMapsToTyped(firstArtifactMaps(result["canonicalSources"]))
		if len(canonical) > 0 {
			artifactSet.CitationBundle = &CitationArtifactBundle{
				Citations:        canonical,
				CanonicalSources: canonical,
				ResolvedCount:    toInt(result["resolvedCount"]),
				DuplicateCount:   toInt(result["duplicateCount"]),
			}
			artifactSet.Artifacts["canonicalSources"] = mapsToAny(typedCitationsToMaps(canonical))
			artifactSet.Artifacts["citations"] = mapsToAny(typedCitationsToMaps(canonical))
		}
		artifactSet.CitationTrustBundle = buildCitationTrustBundleFromResult(result, canonical)
		if artifactSet.CitationTrustBundle != nil {
			artifactSet.Artifacts["citationTrustBundle"] = citationTrustBundleToMap(artifactSet.CitationTrustBundle)
		}
	case "research.verifyCitations":
		verified := citationMapsToTyped(firstArtifactMaps(result["verifiedRecords"]))
		if len(verified) > 0 {
			artifactSet.CitationBundle = &CitationArtifactBundle{
				Citations:       verified,
				VerifiedRecords: verified,
				ValidCount:      toInt(result["validCount"]),
				InvalidCount:    toInt(result["invalidCount"]),
				DuplicateCount:  toInt(result["duplicateCount"]),
			}
			artifactSet.Artifacts["verifiedRecords"] = mapsToAny(typedCitationsToMaps(verified))
			artifactSet.Artifacts["citations"] = mapsToAny(typedCitationsToMaps(verified))
			if artifactSet.PaperBundle == nil {
				artifactSet.Artifacts["papers"] = mapsToAny(typedCitationsToMaps(verified))
			}
		}
		artifactSet.CitationTrustBundle = buildCitationTrustBundleFromResult(result, verified)
		if artifactSet.CitationTrustBundle != nil {
			artifactSet.Artifacts["citationTrustBundle"] = citationTrustBundleToMap(artifactSet.CitationTrustBundle)
		}
	case "research.buildClaimEvidenceTable":
		if table, ok := result["table"].(string); ok && strings.TrimSpace(table) != "" {
			artifactSet.ClaimEvidenceArtifact = &ClaimEvidenceArtifact{
				Table:    table,
				RowCount: toInt(result["rowCount"]),
			}
			artifactSet.Artifacts["claimEvidenceTable"] = map[string]any{
				"table":    table,
				"rowCount": toInt(result["rowCount"]),
			}
		}
	case "research.verifyReasoningPaths":
		verification := &ReasoningVerification{
			TotalBranches:     toInt(result["totalBranches"]),
			VerifiedBranches:  toInt(result["verifiedBranches"]),
			RejectedBranches:  toInt(result["rejectedBranches"]),
			ReadyForSynthesis: toBool(result["readyForSynthesis"]),
		}
		if artifactSet.ReasoningBundle == nil {
			artifactSet.ReasoningBundle = &ReasoningArtifactBundle{}
		}
		artifactSet.ReasoningBundle.Verification = verification
		artifactSet.Artifacts["reasoningVerification"] = map[string]any{
			"totalBranches":     verification.TotalBranches,
			"verifiedBranches":  verification.VerifiedBranches,
			"rejectedBranches":  verification.RejectedBranches,
			"readyForSynthesis": verification.ReadyForSynthesis,
		}
	case "research.search", "research.snowballCitations", "research.selectPrimarySource":
		// These actions primarily produce a PaperBundle (sources).
		// Line 182-185 already populates PaperBundle if sources are present,
		// but providing an explicit case here for future-proofing and consistency.
		if artifactSet.PaperBundle == nil && len(sources) > 0 {
			artifactSet.PaperBundle = &PaperArtifactBundle{Papers: sources}
		}
	}
	if err := validateStepArtifactSetAgainstCanonicalSchema(artifactSet); err != nil {
		return annotateArtifactNormalizationFailure(initStepArtifactSet(step, result, sources), "schema_validation", err), err
	}
	return artifactSet, nil
}

func injectStepArtifacts(session *AgentSession, step PlanStep, payload map[string]any) {
	if session == nil || session.Plan == nil {
		return
	}
	ensurePlanArtifactState(session.Plan)
	dependencyArtifacts := make(map[string]map[string]any)
	for _, depID := range step.DependsOnStepIDs {
		artifactSet, ok := session.Plan.StepArtifacts[depID]
		if !ok {
			continue
		}
		dependencyArtifacts[depID] = artifactSet.Artifacts
		if _, ok := payload["papers"]; !ok {
			if artifactSet.PaperBundle != nil && len(artifactSet.PaperBundle.Papers) > 0 {
				payload["papers"] = mapsToAny(sourcesToArtifactMaps(artifactSet.PaperBundle.Papers))
			} else if artifactSet.CitationBundle != nil && len(artifactSet.CitationBundle.Citations) > 0 {
				payload["papers"] = mapsToAny(typedCitationsToMaps(artifactSet.CitationBundle.Citations))
			} else if papers := firstArtifactMaps(artifactSet.Artifacts["papers"]); len(papers) > 0 {
				payload["papers"] = mapsToAny(papers)
			} else if citations := firstArtifactMaps(artifactSet.Artifacts["citations"]); len(citations) > 0 {
				payload["papers"] = mapsToAny(citations)
			}
		}
		if _, ok := payload["retrievalStrategies"]; !ok {
			if artifactSet.PaperBundle != nil && len(artifactSet.PaperBundle.RetrievalStrategies) > 0 {
				payload["retrievalStrategies"] = stringSliceToAny(artifactSet.PaperBundle.RetrievalStrategies)
			} else if strategies, ok := artifactSet.Artifacts["retrievalStrategies"]; ok {
				payload["retrievalStrategies"] = strategies
			}
		}
		if _, ok := payload["retrievalTrace"]; !ok {
			if artifactSet.PaperBundle != nil && len(artifactSet.PaperBundle.RetrievalTrace) > 0 {
				payload["retrievalTrace"] = mapsToAny(artifactSet.PaperBundle.RetrievalTrace)
			} else if trace, ok := artifactSet.Artifacts["retrievalTrace"]; ok {
				payload["retrievalTrace"] = trace
			}
		}
		if _, ok := payload["queryUsed"]; !ok {
			if artifactSet.PaperBundle != nil && strings.TrimSpace(artifactSet.PaperBundle.QueryUsed) != "" {
				payload["queryUsed"] = artifactSet.PaperBundle.QueryUsed
			} else if queryUsed, ok := artifactSet.Artifacts["queryUsed"]; ok {
				payload["queryUsed"] = queryUsed
			}
		}
		if _, ok := payload["traceId"]; !ok {
			if artifactSet.PaperBundle != nil && strings.TrimSpace(artifactSet.PaperBundle.TraceID) != "" {
				payload["traceId"] = artifactSet.PaperBundle.TraceID
			} else if traceID, ok := artifactSet.Artifacts["traceId"]; ok {
				payload["traceId"] = traceID
			}
		}
		if _, ok := payload["citations"]; !ok {
			if artifactSet.CitationBundle != nil && len(artifactSet.CitationBundle.Citations) > 0 {
				payload["citations"] = mapsToAny(typedCitationsToMaps(artifactSet.CitationBundle.Citations))
			} else if citations := firstArtifactMaps(artifactSet.Artifacts["citations"]); len(citations) > 0 {
				payload["citations"] = mapsToAny(citations)
			}
		}
		if _, ok := payload["canonicalSources"]; !ok {
			if artifactSet.CitationBundle != nil && len(artifactSet.CitationBundle.CanonicalSources) > 0 {
				payload["canonicalSources"] = mapsToAny(typedCitationsToMaps(artifactSet.CitationBundle.CanonicalSources))
			} else if canonical := firstArtifactMaps(artifactSet.Artifacts["canonicalSources"]); len(canonical) > 0 {
				payload["canonicalSources"] = mapsToAny(canonical)
			}
		}
		if _, ok := payload["verifiedRecords"]; !ok {
			if artifactSet.CitationBundle != nil && len(artifactSet.CitationBundle.VerifiedRecords) > 0 {
				payload["verifiedRecords"] = mapsToAny(typedCitationsToMaps(artifactSet.CitationBundle.VerifiedRecords))
			} else if verified := firstArtifactMaps(artifactSet.Artifacts["verifiedRecords"]); len(verified) > 0 {
				payload["verifiedRecords"] = mapsToAny(verified)
			}
		}
		if _, ok := payload["citationTrustBundle"]; !ok {
			if artifactSet.CitationTrustBundle != nil {
				payload["citationTrustBundle"] = citationTrustBundleToMap(artifactSet.CitationTrustBundle)
			} else if trust, ok := artifactSet.Artifacts["citationTrustBundle"].(map[string]any); ok {
				payload["citationTrustBundle"] = trust
			}
		}
		if _, ok := payload["branches"]; !ok {
			if artifactSet.ReasoningBundle != nil && len(artifactSet.ReasoningBundle.Branches) > 0 {
				payload["branches"] = mapsToAny(typedBranchesToMaps(artifactSet.ReasoningBundle.Branches))
			} else if branches := firstArtifactMaps(artifactSet.Artifacts["branches"]); len(branches) > 0 {
				payload["branches"] = mapsToAny(branches)
			}
		}
		if _, ok := payload["reasoningVerification"]; !ok {
			if artifactSet.ReasoningBundle != nil && artifactSet.ReasoningBundle.Verification != nil {
				payload["reasoningVerification"] = map[string]any{
					"totalBranches":     artifactSet.ReasoningBundle.Verification.TotalBranches,
					"verifiedBranches":  artifactSet.ReasoningBundle.Verification.VerifiedBranches,
					"rejectedBranches":  artifactSet.ReasoningBundle.Verification.RejectedBranches,
					"readyForSynthesis": artifactSet.ReasoningBundle.Verification.ReadyForSynthesis,
				}
			} else if verification, ok := artifactSet.Artifacts["reasoningVerification"]; ok {
				payload["reasoningVerification"] = verification
			}
		}
		if _, ok := payload["claimEvidenceTable"]; !ok {
			if artifactSet.ClaimEvidenceArtifact != nil {
				payload["claimEvidenceTable"] = map[string]any{
					"table":    artifactSet.ClaimEvidenceArtifact.Table,
					"rowCount": artifactSet.ClaimEvidenceArtifact.RowCount,
				}
			} else if claimTable, ok := artifactSet.Artifacts["claimEvidenceTable"]; ok {
				payload["claimEvidenceTable"] = claimTable
			}
		}
	}
	if len(dependencyArtifacts) > 0 {
		payload["dependencyArtifacts"] = dependencyArtifacts
	}
}

func persistStepArtifacts(session *AgentSession, step PlanStep, artifactSet StepArtifactSet) {
	if session == nil || session.Plan == nil {
		return
	}
	hasTyped := artifactSet.PaperBundle != nil || artifactSet.CitationBundle != nil || artifactSet.CitationTrustBundle != nil || artifactSet.ReasoningBundle != nil || artifactSet.ClaimEvidenceArtifact != nil
	if !hasTyped && len(artifactSet.Artifacts) == 0 {
		return
	}
	ensurePlanArtifactState(session.Plan)
	session.Plan.StepArtifacts[step.ID] = artifactSet
	EnsureSessionArchitectureState(session)
	if session.MemoryTiers == nil {
		return
	}
	body, err := json.Marshal(artifactSet)
	if err != nil {
		body = []byte(fmt.Sprintf("%v", artifactSet))
	}
	session.MemoryTiers.ArtifactMemory = appendUniqueMemoryEntry(session.MemoryTiers.ArtifactMemory, MemoryEntry{
		ID:        "artifact_" + step.ID,
		Type:      "plan_artifact",
		Content:   string(body),
		CreatedAt: NowMillis(),
	})
}

func artifactKeys(artifactSet StepArtifactSet) []string {
	keys := make([]string, 0)
	if artifactSet.PaperBundle != nil && len(artifactSet.PaperBundle.Papers) > 0 {
		keys = append(keys, "paperBundle", "papers")
	}
	if artifactSet.PaperBundle != nil && len(artifactSet.PaperBundle.RetrievalStrategies) > 0 {
		keys = append(keys, "retrievalStrategies")
	}
	if artifactSet.PaperBundle != nil && len(artifactSet.PaperBundle.RetrievalTrace) > 0 {
		keys = append(keys, "retrievalTrace")
	}
	if artifactSet.PaperBundle != nil && strings.TrimSpace(artifactSet.PaperBundle.QueryUsed) != "" {
		keys = append(keys, "queryUsed")
	}
	if artifactSet.PaperBundle != nil && strings.TrimSpace(artifactSet.PaperBundle.TraceID) != "" {
		keys = append(keys, "traceId")
	}
	if artifactSet.CitationBundle != nil {
		if len(artifactSet.CitationBundle.Citations) > 0 {
			keys = append(keys, "citationBundle", "citations")
		}
		if len(artifactSet.CitationBundle.CanonicalSources) > 0 {
			keys = append(keys, "canonicalSources")
		}
		if len(artifactSet.CitationBundle.VerifiedRecords) > 0 {
			keys = append(keys, "verifiedRecords")
		}
	}
	if artifactSet.CitationTrustBundle != nil {
		keys = append(keys, "citationTrustBundle")
	}
	if artifactSet.ReasoningBundle != nil {
		if len(artifactSet.ReasoningBundle.Branches) > 0 {
			keys = append(keys, "reasoningBundle", "branches")
		}
		if artifactSet.ReasoningBundle.Verification != nil {
			keys = append(keys, "reasoningVerification")
		}
	}
	if artifactSet.ClaimEvidenceArtifact != nil {
		keys = append(keys, "claimEvidenceArtifact", "claimEvidenceTable")
	}
	for key := range artifactSet.Artifacts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return dedupeStrings(keys)
}

func dedupeStrings(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	var prev string
	for _, item := range items {
		if len(out) == 0 || item != prev {
			out = append(out, item)
			prev = item
		}
	}
	return out
}

func toStringSlice(value any) []string {
	switch v := value.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if text := strings.TrimSpace(fmt.Sprint(item)); text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func toInt(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

func toFloat(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	default:
		return 0
	}
}

func toBool(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	default:
		return false
	}
}
