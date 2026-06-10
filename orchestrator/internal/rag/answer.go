package rag

import "strings"

type AnswerClaim struct {
	Text        string   `json:"text"`
	EvidenceIDs []string `json:"evidenceIds"`        // Links to EvidenceFinding.ID
	Confidence  float64  `json:"confidence"`         // Inherited from supporting evidence
	BeliefID    string   `json:"beliefId,omitempty"` // Which belief this supports
	Unsupported bool     `json:"unsupported"`        // No evidence backs this claim
}

type AnswerSection struct {
	Heading   string        `json:"heading"`
	Sentences []AnswerClaim `json:"sentences"`
}

type StructuredAnswer struct {
	Sections []AnswerSection `json:"sections"`
	Text     string          `json:"text,omitempty"`
}

func (answer *StructuredAnswer) RenderText() string {
	if answer == nil {
		return ""
	}
	if text := strings.TrimSpace(answer.Text); text != "" {
		return text
	}
	return RenderAnswerSections(answer.Sections)
}

func RenderAnswerSections(sections []AnswerSection) string {
	return RenderAnswerSectionsWithCitations(sections, nil)
}

// RenderAnswerSectionsWithCitations renders structured answer sections and appends
// inline citation parentheticals from resolve when evidence IDs are present.
func RenderAnswerSectionsWithCitations(sections []AnswerSection, resolve func(evidenceIDs []string) string) string {
	var text strings.Builder
	for _, section := range sections {
		heading := strings.TrimSpace(section.Heading)
		if heading != "" {
			text.WriteString("## " + heading + "\n\n")
		}
		for _, sentence := range section.Sentences {
			claimText := strings.TrimSpace(sentence.Text)
			if claimText == "" {
				continue
			}
			if resolve != nil {
				if cite := strings.TrimSpace(resolve(sentence.EvidenceIDs)); cite != "" && !strings.Contains(claimText, cite) {
					if !strings.HasSuffix(claimText, ".") && !strings.HasSuffix(claimText, "!") && !strings.HasSuffix(claimText, "?") {
						claimText += "."
					}
					claimText += " " + cite
				}
			}
			text.WriteString(claimText + " ")
		}
		text.WriteString("\n\n")
	}
	return strings.TrimSpace(text.String())
}
