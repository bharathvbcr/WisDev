package wisdev

import (
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/evidence"
)

var positionalCitationBracketPattern = regexp.MustCompile(`\[(.*?)\]`)

// sanitizeOutOfRangePositionalCitations removes numeric [n] markers whose index
// falls outside the section's ordered claim-packet list (the same bounds
// resolvePositionalPacketIDs uses). Literal packet-ID tokens and in-range [n]
// markers are preserved. Returns the cleaned content and the out-of-range
// marker numbers that were dropped.
func sanitizeOutOfRangePositionalCitations(content string, orderedPackets []evidence.EvidencePacket) (string, []int) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return content, nil
	}
	packetCount := len(orderedPackets)
	allowedPacketIDs := make(map[string]struct{}, packetCount)
	for _, packet := range orderedPackets {
		allowedPacketIDs[packet.PacketID] = struct{}{}
	}

	dropped := make([]int, 0)
	changed := false
	cleaned := positionalCitationBracketPattern.ReplaceAllStringFunc(trimmed, func(match string) string {
		inner := strings.TrimSpace(match[1 : len(match)-1])
		if inner == "" {
			return match
		}
		tokens := strings.FieldsFunc(inner, func(r rune) bool {
			return r == ',' || r == ';' || r == '|' || unicode.IsSpace(r)
		})
		kept := make([]string, 0, len(tokens))
		for _, token := range tokens {
			token = strings.TrimSpace(token)
			if token == "" {
				continue
			}
			if n, err := strconv.Atoi(token); err == nil {
				if n < 1 || n > packetCount {
					dropped = append(dropped, n)
					changed = true
					continue
				}
				kept = append(kept, token)
				continue
			}
			if _, ok := allowedPacketIDs[token]; ok {
				kept = append(kept, token)
				continue
			}
			// Non-numeric, non-packet token — leave bracket unchanged.
			return match
		}
		if len(kept) == 0 {
			changed = true
			return ""
		}
		if len(kept) == len(tokens) {
			return match
		}
		changed = true
		return "[" + strings.Join(kept, ", ") + "]"
	})
	if !changed {
		return content, nil
	}
	cleaned = collapseCitationWhitespace(cleaned)
	return cleaned, uniqueInts(dropped)
}

func collapseCitationWhitespace(content string) string {
	content = regexp.MustCompile(`[ \t]{2,}`).ReplaceAllString(content, " ")
	content = regexp.MustCompile(`[ \t]+([.,;:!?])`).ReplaceAllString(content, "$1")
	return strings.TrimSpace(content)
}

func uniqueInts(values []int) []int {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[int]struct{}, len(values))
	out := make([]int, 0, len(values))
	for _, v := range values {
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// applyCitationMarkerHygiene drops clearly invalid out-of-range [n] markers from
// section content, logs what was removed, and rebuilds paragraph metadata when
// the body changed.
func applyCitationMarkerHygiene(section *SectionDraftArtifact, claimPackets []evidence.EvidencePacket) {
	if section == nil || strings.TrimSpace(section.Content) == "" {
		return
	}
	if len(claimPackets) == 0 {
		claimPackets = orderedPacketsFromSectionIDs(section.ClaimPacketIDs)
	}
	sanitized, dropped := sanitizeOutOfRangePositionalCitations(section.Content, claimPackets)
	if len(dropped) == 0 {
		return
	}
	section.Content = sanitized
	slog.Warn("manuscript dropped out-of-range citation markers",
		"component", manuscriptLogComponent,
		"operation", "citation_marker_hygiene",
		"section_id", section.SectionID,
		"packet_count", len(claimPackets),
		"dropped_markers", dropped,
	)
	if rebuilt := buildContentParagraphs(section.SectionID, sanitized, claimPackets); len(rebuilt) > 0 {
		section.Paragraphs = rebuilt
		section.ClaimProvenance = buildClaimProvenance(rebuilt, claimPackets)
		section.ContradictionMap = buildContradictionMap(rebuilt, claimPackets)
	}
}

func orderedPacketsFromSectionIDs(packetIDs []string) []evidence.EvidencePacket {
	out := make([]evidence.EvidencePacket, len(packetIDs))
	for i, id := range packetIDs {
		out[i] = evidence.EvidencePacket{PacketID: id}
	}
	return out
}
