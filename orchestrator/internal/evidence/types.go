package evidence

type CanonicalIDs struct {
	DOI             string `json:"doi,omitempty"`
	Arxiv           string `json:"arxiv,omitempty"`
	OpenAlex        string `json:"openalex,omitempty"`
	SemanticScholar string `json:"semanticScholar,omitempty"`
	Crossref        string `json:"crossref,omitempty"`
}

type CanonicalCitationRecord struct {
	CanonicalID          string       `json:"canonicalId"`
	SourceIDs            CanonicalIDs `json:"sourceIds"`
	Title                string       `json:"title"`
	Authors              []string     `json:"authors,omitempty"`
	Venue                string       `json:"venue,omitempty"`
	Year                 int          `json:"year,omitempty"`
	Abstract             string       `json:"abstract,omitempty"`
	LandingURL           string       `json:"landingUrl,omitempty"`
	Resolved             bool         `json:"resolved"`
	ResolutionEngine     string       `json:"resolutionEngine"`
	ResolutionConfidence float64      `json:"resolutionConfidence"`
}

type EvidenceSpan struct {
	SourceCanonicalID string `json:"sourceCanonicalId"`
	Section           string `json:"section,omitempty"`
	Page              int    `json:"page,omitempty"`
	Snippet           string `json:"snippet"`
	Locator           string `json:"locator,omitempty"`
	Support           string `json:"support"`
}

type EvidencePacket struct {
	PacketID               string         `json:"packetId"`
	ClaimText              string         `json:"claimText"`
	ClaimType              string         `json:"claimType"`
	EvidenceSpans          []EvidenceSpan `json:"evidenceSpans"`
	SectionRelevance       []string       `json:"sectionRelevance,omitempty"`
	ContradictionPacketIDs []string       `json:"contradictionPacketIds,omitempty"`
	SourceClusterID        string         `json:"sourceClusterId,omitempty"`
	VisualEvidenceIDs      []string       `json:"visualEvidenceIds,omitempty"`
	MaterialKinds          []string       `json:"materialKinds,omitempty"`
	VerifierStatus         string         `json:"verifierStatus"`
	VerifierNotes          []string       `json:"verifierNotes,omitempty"`
	Confidence             float64        `json:"confidence"`
	CreatedAt              int64          `json:"createdAt"`
}

type VisualEvidence struct {
	VisualID          string   `json:"visualId"`
	SourceCanonicalID string   `json:"sourceCanonicalId,omitempty"`
	Kind              string   `json:"kind"`
	Title             string   `json:"title,omitempty"`
	Caption           string   `json:"caption,omitempty"`
	Locator           string   `json:"locator,omitempty"`
	SourcePacketIDs   []string `json:"sourcePacketIds,omitempty"`
}

type ManuscriptSourceCluster struct {
	ClusterID          string   `json:"clusterId"`
	Label              string   `json:"label"`
	Theme              string   `json:"theme,omitempty"`
	SourceCanonicalIDs []string `json:"sourceCanonicalIds"`
	PacketIDs          []string `json:"packetIds"`
}

type ManuscriptRawMaterialSet struct {
	RawMaterialSetID string                    `json:"rawMaterialSetId"`
	JobID            string                    `json:"jobId,omitempty"`
	Query            string                    `json:"query"`
	CanonicalSources []CanonicalCitationRecord `json:"canonicalSources"`
	ClaimPackets     []EvidencePacket          `json:"claimPackets"`
	SourceClusters   []ManuscriptSourceCluster `json:"sourceClusters,omitempty"`
	VisualEvidence   []VisualEvidence          `json:"visualEvidence,omitempty"`
	Gaps             []string                  `json:"gaps,omitempty"`
	CoverageMetrics  map[string]any            `json:"coverageMetrics"`
	CreatedAt        int64                     `json:"createdAt"`
	UpdatedAt        int64                     `json:"updatedAt"`
}

type Dossier struct {
	DossierID        string                    `json:"dossierId"`
	JobID            string                    `json:"jobId,omitempty"`
	Query            string                    `json:"query"`
	CanonicalSources []CanonicalCitationRecord `json:"canonicalSources"`
	VerifiedClaims   []EvidencePacket          `json:"verifiedClaims"`
	TentativeClaims  []EvidencePacket          `json:"tentativeClaims,omitempty"`
	Contradictions   []map[string]any          `json:"contradictions,omitempty"`
	Gaps             []string                  `json:"gaps,omitempty"`
	CoverageMetrics  map[string]any            `json:"coverageMetrics"`
	CreatedAt        int64                     `json:"createdAt"`
	UpdatedAt        int64                     `json:"updatedAt"`
}
