package rag

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"math"
	"sort"
	"strings"
	"unicode"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

const (
	defaultChunkSizeTokens = 180
	defaultChunkOverlap    = 32
	focusedPacketLimit     = 6
	globalPacketLimit      = 8
	maxPacketChars         = 900
	maxGroundingChars      = 2000
	lightEmbeddingDims     = 64
)

type evidencePacket struct {
	PaperOrdinal int
	PaperID      string
	PaperTitle   string
	SourceKind   string
	Section      string
	Text         string
	Score        float64
}

type synthesisPlan struct {
	Query          string
	QueryUsed      string
	GlobalIntent   bool
	MemoryPrimer   *ResearchMemoryPrimer
	RaptorOverview string
	Packets        []evidencePacket
}

type packetCandidate struct {
	evidencePacket
}

func buildSynthesisPlan(
	ctx context.Context,
	query string,
	queryUsed string,
	papers []search.Paper,
	primer *ResearchMemoryPrimer,
	global bool,
	raptor *RaptorService,
) synthesisPlan {
	effectiveQueryUsed := strings.TrimSpace(queryUsed)
	if effectiveQueryUsed == "" {
		effectiveQueryUsed = strings.TrimSpace(query)
	}

	packets := buildEvidencePackets(effectiveQueryUsed, papers, primer, global)
	overview := ""
	if global {
		overview = buildRaptorOverview(ctx, packets, raptor)
	}

	return synthesisPlan{
		Query:          strings.TrimSpace(query),
		QueryUsed:      effectiveQueryUsed,
		GlobalIntent:   global,
		MemoryPrimer:   primer,
		RaptorOverview: overview,
		Packets:        packets,
	}
}

func buildEvidencePackets(query string, papers []search.Paper, primer *ResearchMemoryPrimer, global bool) []evidencePacket {
	if len(papers) == 0 {
		return nil
	}

	candidates := make([]packetCandidate, 0, len(papers)*2)
	for i, paper := range papers {
		candidates = append(candidates, extractCandidatesForPaper(paper, i+1)...)
	}
	if len(candidates) == 0 {
		return nil
	}

	bm25 := NewBM25()
	scoringQuery := memoryAugmentedQuery(query, primer)
	texts := make([]string, len(candidates))
	for i := range candidates {
		texts[i] = candidates[i].Text
	}
	scores := bm25.Score(scoringQuery, texts)
	hints := collectMemoryHintTerms(primer)
	for i := range candidates {
		candidates[i].Score = scores[i] + packetSourceBoost(candidates[i].SourceKind) + memoryHintBoost(candidates[i].Text, hints)
	}

	perPaper := make(map[int][]packetCandidate, len(papers))
	for _, candidate := range candidates {
		perPaper[candidate.PaperOrdinal] = append(perPaper[candidate.PaperOrdinal], candidate)
	}

	selected := make([]evidencePacket, 0, len(perPaper))
	extras := make([]packetCandidate, 0, len(candidates))
	for paperOrdinal := 1; paperOrdinal <= len(papers); paperOrdinal++ {
		group := perPaper[paperOrdinal]
		if len(group) == 0 {
			continue
		}
		sort.SliceStable(group, func(i, j int) bool {
			if group[i].Score == group[j].Score {
				return len(group[i].Text) > len(group[j].Text)
			}
			return group[i].Score > group[j].Score
		})
		selected = append(selected, group[0].evidencePacket)
		if len(group) > 1 {
			extras = append(extras, group[1:]...)
		}
	}

	sort.SliceStable(selected, func(i, j int) bool { return selected[i].Score > selected[j].Score })
	limit := focusedPacketLimit
	if global {
		limit = globalPacketLimit
	}
	if len(selected) > limit {
		selected = append([]evidencePacket(nil), selected[:limit]...)
	}

	if len(selected) < limit && len(extras) > 0 {
		sort.SliceStable(extras, func(i, j int) bool { return extras[i].Score > extras[j].Score })
		seen := make(map[string]struct{}, len(selected))
		for _, packet := range selected {
			seen[packetDedupKey(packet)] = struct{}{}
		}
		for _, candidate := range extras {
			key := packetDedupKey(candidate.evidencePacket)
			if _, ok := seen[key]; ok {
				continue
			}
			selected = append(selected, candidate.evidencePacket)
			seen[key] = struct{}{}
			if len(selected) >= limit {
				break
			}
		}
	}

	return selected
}

func buildRaptorOverview(ctx context.Context, packets []evidencePacket, raptor *RaptorService) string {
	if raptor == nil || len(packets) < 3 {
		return ""
	}

	byPaper := make(map[string][]ChunkDetails)
	for i, packet := range packets {
		chunks := byPaper[packet.PaperID]
		chunks = append(chunks, ChunkDetails{
			ID:        packet.PaperID + "_packet_" + strings.TrimSpace(packet.Section) + "_" + strings.TrimSpace(packet.SourceKind),
			Content:   packet.Text,
			Embedding: lightweightTextEmbedding(packet.Text),
			CharStart: i,
			CharEnd:   i + len([]rune(packet.Text)),
		})
		byPaper[packet.PaperID] = chunks
	}

	requests := make([]PaperChunksRequest, 0, len(byPaper))
	for paperID, chunks := range byPaper {
		requests = append(requests, PaperChunksRequest{
			PaperID: paperID,
			Chunks:  chunks,
		})
	}
	if len(requests) < 2 {
		return ""
	}

	tree, err := raptor.BuildTree(ctx, requests, 2)
	if err != nil || tree == nil || tree.Root == nil {
		return ""
	}
	return truncateContextRune(strings.TrimSpace(tree.Root.Content), maxPacketChars)
}

func extractCandidatesForPaper(paper search.Paper, ordinal int) []packetCandidate {
	if fullText := strings.TrimSpace(paper.FullText); fullText != "" {
		chunks := AdaptiveChunking(fullText, paper.ID, defaultChunkSizeTokens, defaultChunkOverlap)
		candidates := make([]packetCandidate, 0, len(chunks))
		for _, chunk := range chunks {
			content := strings.TrimSpace(chunk.Content)
			if len(content) < 40 {
				continue
			}
			candidates = append(candidates, packetCandidate{
				evidencePacket: evidencePacket{
					PaperOrdinal: ordinal,
					PaperID:      paper.ID,
					PaperTitle:   paper.Title,
					SourceKind:   "full_text_chunk",
					Section:      inferChunkSection(content),
					Text:         truncateContextRune(content, maxPacketChars),
				},
			})
		}
		if len(candidates) > 0 {
			return candidates
		}
	}

	if abstract := strings.TrimSpace(paper.Abstract); abstract != "" {
		return []packetCandidate{{
			evidencePacket: evidencePacket{
				PaperOrdinal: ordinal,
				PaperID:      paper.ID,
				PaperTitle:   paper.Title,
				SourceKind:   "abstract",
				Section:      "abstract",
				Text:         truncateContextRune(abstract, maxPacketChars),
			},
		}}
	}

	if len(paper.StructureMap) > 0 {
		if b, err := json.Marshal(paper.StructureMap); err == nil {
			return []packetCandidate{{
				evidencePacket: evidencePacket{
					PaperOrdinal: ordinal,
					PaperID:      paper.ID,
					PaperTitle:   paper.Title,
					SourceKind:   "structure_map",
					Section:      "structure",
					Text:         truncateContextRune(string(b), maxPacketChars),
				},
			}}
		}
	}

	if title := strings.TrimSpace(paper.Title); title != "" {
		return []packetCandidate{{
			evidencePacket: evidencePacket{
				PaperOrdinal: ordinal,
				PaperID:      paper.ID,
				PaperTitle:   paper.Title,
				SourceKind:   "title_only",
				Section:      "general",
				Text:         title,
			},
		}}
	}

	return nil
}

func inferChunkSection(text string) string {
	firstLine := strings.TrimSpace(text)
	if idx := strings.IndexRune(firstLine, '\n'); idx >= 0 {
		firstLine = strings.TrimSpace(firstLine[:idx])
	}
	if section := detectSectionType(firstLine); section != "" {
		return section
	}
	return "general"
}

func memoryAugmentedQuery(query string, primer *ResearchMemoryPrimer) string {
	base := strings.TrimSpace(query)
	hints := collectMemoryHintTerms(primer)
	if len(hints) == 0 {
		return base
	}
	return strings.TrimSpace(base + " " + strings.Join(hints, " "))
}

func collectMemoryHintTerms(primer *ResearchMemoryPrimer) []string {
	if primer == nil {
		return nil
	}
	terms := make([]string, 0, len(primer.RelatedTopics)+len(primer.RelatedMethods)+len(primer.RecommendedQueries))
	terms = append(terms, primer.RelatedTopics...)
	terms = append(terms, primer.RelatedMethods...)
	terms = append(terms, primer.RecommendedQueries...)
	seen := make(map[string]struct{}, len(terms))
	out := make([]string, 0, len(terms))
	for _, term := range terms {
		normalized := strings.TrimSpace(term)
		if len(normalized) < 3 {
			continue
		}
		key := strings.ToLower(normalized)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func memoryHintBoost(text string, hints []string) float64 {
	if len(hints) == 0 {
		return 0
	}
	lower := strings.ToLower(text)
	boost := 0.0
	for _, hint := range hints {
		if strings.Contains(lower, strings.ToLower(hint)) {
			boost += 0.04
		}
		if boost >= 0.16 {
			return 0.16
		}
	}
	return boost
}

func packetSourceBoost(kind string) float64 {
	switch strings.TrimSpace(kind) {
	case "full_text_chunk":
		return 0.08
	case "abstract":
		return 0.03
	default:
		return 0
	}
}

func packetDedupKey(packet evidencePacket) string {
	return strings.TrimSpace(packet.PaperID) + "|" + strings.TrimSpace(packet.SourceKind) + "|" + strings.TrimSpace(packet.Text)
}

func paperGroundingText(paper search.Paper, maxChars int) string {
	if maxChars <= 0 {
		maxChars = maxGroundingChars
	}

	parts := make([]string, 0, 4)
	if title := strings.TrimSpace(paper.Title); title != "" {
		parts = append(parts, title)
	}
	if abstract := strings.TrimSpace(paper.Abstract); abstract != "" {
		parts = append(parts, abstract)
	}
	if fullText := strings.TrimSpace(paper.FullText); fullText != "" {
		parts = append(parts, truncateContextRune(fullText, maxChars))
	}
	if len(parts) <= 1 && len(paper.StructureMap) > 0 {
		if b, err := json.Marshal(paper.StructureMap); err == nil {
			parts = append(parts, truncateContextRune(string(b), maxChars/2))
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func lightweightTextEmbedding(text string) []float64 {
	vector := make([]float64, lightEmbeddingDims)
	for _, token := range tokenizeForEmbedding(text) {
		hasher := fnv.New64a()
		_, _ = hasher.Write([]byte(token))
		sum := hasher.Sum64()
		idx := int(sum % lightEmbeddingDims)
		sign := 1.0
		if (sum>>8)&1 == 1 {
			sign = -1.0
		}
		vector[idx] += sign
	}

	var norm float64
	for _, value := range vector {
		norm += value * value
	}
	if norm == 0 {
		return vector
	}
	norm = math.Sqrt(norm)
	for i := range vector {
		vector[i] /= norm
	}
	return vector
}

func tokenizeForEmbedding(text string) []string {
	tokens := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	out := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if len(token) < 3 {
			continue
		}
		if _, isStop := stopWords[token]; isStop {
			continue
		}
		out = append(out, token)
	}
	return out
}
