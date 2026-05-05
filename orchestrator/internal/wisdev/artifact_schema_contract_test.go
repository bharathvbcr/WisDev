package wisdev

import (
	"encoding/json"
	"os"
	"testing"
)

type artifactSchemaDocument struct {
	Version     string         `json:"version"`
	Definitions map[string]any `json:"definitions"`
	Properties  map[string]any `json:"properties"`
}

func loadArtifactSchema(t *testing.T) artifactSchemaDocument {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	path, err := resolveArtifactSchemaPath(wd)
	if err != nil {
		t.Fatalf("resolve artifact schema: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read artifact schema: %v", err)
	}
	var doc artifactSchemaDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("decode artifact schema: %v", err)
	}
	return doc
}

func TestArtifactSchemaVersionMatchesGoConstant(t *testing.T) {
	doc := loadArtifactSchema(t)
	if doc.Version != ARTIFACT_SCHEMA_VERSION {
		t.Fatalf("schema version mismatch: schema=%q go=%q", doc.Version, ARTIFACT_SCHEMA_VERSION)
	}
}

func TestArtifactSchemaExposesTypedBundles(t *testing.T) {
	doc := loadArtifactSchema(t)
	required := []string{"paperBundle", "citationBundle", "citationTrustBundle", "reasoningBundle", "claimEvidenceArtifact"}
	for _, key := range required {
		if _, ok := doc.Properties[key]; !ok {
			t.Fatalf("schema missing required property %q", key)
		}
	}
}

func TestArtifactSchemaCitationTrustBundleDefinition(t *testing.T) {
	doc := loadArtifactSchema(t)
	defs, ok := doc.Definitions["CitationTrustBundle"].(map[string]any)
	if !ok {
		t.Fatalf("schema missing CitationTrustBundle definition")
	}
	props, ok := defs["properties"].(map[string]any)
	if !ok {
		t.Fatalf("CitationTrustBundle definition missing properties")
	}
	for _, key := range []string{"citations", "verifiedCount", "ambiguousCount", "rejectedCount", "resolverTrace", "promotionEligible", "blockingIssues"} {
		if _, ok := props[key]; !ok {
			t.Fatalf("CitationTrustBundle definition missing field %q", key)
		}
	}
}

func TestArtifactSchemaPaperArtifactBundleDefinition(t *testing.T) {
	doc := loadArtifactSchema(t)
	defs, ok := doc.Definitions["PaperArtifactBundle"].(map[string]any)
	if !ok {
		t.Fatalf("schema missing PaperArtifactBundle definition")
	}
	props, ok := defs["properties"].(map[string]any)
	if !ok {
		t.Fatalf("PaperArtifactBundle definition missing properties")
	}
	for _, key := range []string{"papers", "retrievalStrategies", "retrievalTrace", "queryUsed", "traceId"} {
		if _, ok := props[key]; !ok {
			t.Fatalf("PaperArtifactBundle definition missing field %q", key)
		}
	}
}

func TestArtifactSchemaReasoningBranchHasSource(t *testing.T) {
	doc := loadArtifactSchema(t)
	defs, ok := doc.Definitions["ReasoningBranch"].(map[string]any)
	if !ok {
		t.Fatalf("schema missing ReasoningBranch definition")
	}
	props, ok := defs["properties"].(map[string]any)
	if !ok {
		t.Fatalf("ReasoningBranch definition missing properties")
	}
	if _, ok := props["source"]; !ok {
		t.Fatalf("ReasoningBranch definition missing source field")
	}
}

func TestArtifactSchemaMetadataMatchesCanonicalSchema(t *testing.T) {
	doc := loadArtifactSchema(t)
	meta := ArtifactSchemaMetadata()

	if meta["version"] != doc.Version {
		t.Fatalf("artifact schema metadata version mismatch: meta=%q schema=%q", meta["version"], doc.Version)
	}
	bundles, ok := meta["bundles"].([]string)
	if !ok {
		t.Fatalf("artifact schema metadata bundles have wrong type: %T", meta["bundles"])
	}
	for _, key := range bundles {
		if _, ok := doc.Properties[key]; !ok {
			t.Fatalf("artifact schema metadata bundle %q missing from canonical schema", key)
		}
	}
}
