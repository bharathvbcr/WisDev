package rag

type AnswerClaim struct {
	Text        string   `json:"text"`
	EvidenceIDs []string `json:"evidenceIds"`   // Links to EvidenceFinding.ID
	Confidence  float64  `json:"confidence"`    // Inherited from supporting evidence
	BeliefID    string   `json:"beliefId,omitempty"` // Which belief this supports
	Unsupported bool     `json:"unsupported"`   // No evidence backs this claim
}

type AnswerSection struct {
	Heading   string        `json:"heading"`
	Sentences []AnswerClaim `json:"sentences"`
}

type StructuredAnswer struct {
	Sections  []AnswerSection `json:"sections"`
	PlainText string          `json:"plainText"` // For backward compatibility
}
