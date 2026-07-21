package api

// POST /synthesis?action=verify-citations — Go-owned citation grounding and
// verification policy (frontend-thinning Phase 3 citation cutover).
// Ports match thresholds, confidence scoring, optional embedding similarity
// (via llm Embed / EmbedBatch → Python /ml/embed), and supporting-quote
// selection formerly in citationGroundingService / citationVerificationService.
// Browser keeps human-readable status copy and link rendering helpers.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	llmv1 "github.com/bharathvbcr/wisdev-arc/orchestrator/proto/llm"
)

const (
	verifyCiteMinConfidenceDefault = 0.5
	verifyCiteRAGThreshold         = 0.7
	verifyCiteTraceThreshold       = 0.6
	verifyCiteBestChunkMin         = 30 // 0-100 scale
	verifyCiteMaxBodyBytes         = 2 << 20
	verifyCiteEmbedTimeout         = 12 * time.Second
)

var (
	verifyCiteParenRE    = regexp.MustCompile(`\(([A-Z][a-zA-Z]+(?:\s+et\s+al\.)?(?:\s*,\s*\d{4})?)\)`)
	verifyCiteInlineRE   = regexp.MustCompile(`([A-Z][a-zA-Z]+(?:\s+et\s+al\.)?)\s*\((\d{4})\)`)
	verifyCiteNumberedRE = regexp.MustCompile(`\[(\d+)\]`)
	verifyCiteTitleRE    = regexp.MustCompile(`"([^"]{10,100})"`)
	verifyCiteSentenceRE = regexp.MustCompile(`([^.!?]*\[(\d+)\][^.!?]*[.!?]?)`)
)

// verifyCitationSource mirrors FE Source fields needed for grounding.
type verifyCitationSource struct {
	PaperID string `json:"paperId"`
	Title   string `json:"title"`
	Authors []struct {
		Name string `json:"name"`
	} `json:"authors"`
	Year        any    `json:"year"`
	Date        string `json:"date"`
	PublishDate *struct {
		Year int `json:"year"`
	} `json:"publishDate"`
	Link     string `json:"link"`
	Abstract string `json:"abstract"`
	Summary  string `json:"summary"`
}

type verifyCitationChunk struct {
	ID          string  `json:"id"`
	ChunkID     string  `json:"chunkId"`
	PaperID     string  `json:"paperId"`
	PaperIDAlt  string  `json:"paper_id"`
	ChunkIndex  int     `json:"chunkIndex"`
	ChunkIndex2 int     `json:"chunk_index"`
	Content     string  `json:"content"`
	SectionType string  `json:"sectionType"`
	SectionAlt  string  `json:"section_type"`
	Section     string  `json:"section"`
	Number      int     `json:"number"`
	PaperTitle  string  `json:"paperTitle"`
	Authors     string  `json:"authors"`
	Year        int     `json:"year"`
	SourcePage  int     `json:"sourcePage"`
	CharStart   int     `json:"sourceCharStart"`
	CharEnd     int     `json:"sourceCharEnd"`
	Similarity  float64 `json:"similarity"`
}

type verifyCitationsOptions struct {
	RemoveUnverified *bool   `json:"removeUnverified"`
	FlagUnverified   *bool   `json:"flagUnverified"`
	MinConfidence    float64 `json:"minConfidence"`
	MaxQuotes        int     `json:"maxQuotes"`
}

type verifyCitationsRequest struct {
	Mode         string                 `json:"mode"` // ground|section|verify|trace|quotes|claim-chunk|best-chunk|numbered|inline
	Text         string                 `json:"text"`
	CitationText string                 `json:"citationText"`
	PaperID      string                 `json:"paperId"`
	Claim        string                 `json:"claim"`
	ChunkContent string                 `json:"chunkContent"`
	Sources      []verifyCitationSource `json:"sources"`
	Chunks       []verifyCitationChunk  `json:"chunks"`
	Options      verifyCitationsOptions `json:"options"`
	Citations    []struct {
		Text    string `json:"text"`
		PaperID string `json:"paperId"`
		Claim   string `json:"claim"`
	} `json:"citations"`
	InlineCitations []struct {
		CitationNumber int    `json:"citationNumber"`
		PaperID        string `json:"paperId"`
		PaperTitle     string `json:"paperTitle"`
		Authors        string `json:"authors"`
		Year           int    `json:"year"`
		ChunkID        string `json:"chunkId"`
		ChunkContent   string `json:"chunkContent"`
		ClaimText      string `json:"claimText"`
		CharStart      int    `json:"charStart"`
		CharEnd        int    `json:"charEnd"`
		IsSynthesis    bool   `json:"isSynthesis"`
		SourceSection  string `json:"sourceSection"`
		SourcePage     int    `json:"sourcePage"`
	} `json:"inlineCitations"`
	StructuredCitations []struct {
		CitationID string `json:"citationId"`
		PaperID    string `json:"paperId"`
		Marker     string `json:"marker"`
		SectionID  string `json:"sectionId"`
		Claim      string `json:"claim,omitempty"`
	} `json:"structuredCitations"`
}

type extractedCitation struct {
	Text    string `json:"text"`
	PaperID string `json:"paperId,omitempty"`
	Title   string `json:"title,omitempty"`
	Authors string `json:"authors,omitempty"`
	Year    string `json:"year,omitempty"`
}

// FEGroundedCitation mirrors frontend citationGroundingService.GroundedCitation.
type FEGroundedCitation struct {
	Text          string                `json:"text"`
	PaperID       string                `json:"paperId,omitempty"`
	Title         string                `json:"title,omitempty"`
	Authors       string                `json:"authors,omitempty"`
	Year          string                `json:"year,omitempty"`
	Verified      bool                  `json:"verified"`
	MatchedSource *verifyCitationSource `json:"matchedSource,omitempty"`
	Confidence    float64               `json:"confidence"`
	MatchType     string                `json:"matchType"` // exact|fuzzy|partial|none
}

type FEChunkReference struct {
	ChunkID        string  `json:"chunkId"`
	PaperID        string  `json:"paperId"`
	ChunkIndex     int     `json:"chunkIndex"`
	CharStart      int     `json:"charStart"`
	CharEnd        int     `json:"charEnd"`
	Content        string  `json:"content"`
	RelevanceScore float64 `json:"relevanceScore"`
	UsedFor        string  `json:"usedFor,omitempty"`
	SectionType    string  `json:"sectionType,omitempty"`
}

type FESupportingQuote struct {
	Text           string  `json:"text"`
	ChunkID        string  `json:"chunkId"`
	CharStart      int     `json:"charStart"`
	CharEnd        int     `json:"charEnd"`
	RelevanceScore float64 `json:"relevanceScore"`
	PaperID        string  `json:"paperId"`
}

type FETracedCitation struct {
	CitationID         string              `json:"citationId"`
	CitationText       string              `json:"citationText"`
	PaperID            string              `json:"paperId"`
	Verified           bool                `json:"verified"`
	Confidence         float64             `json:"confidence"`
	SourceChunks       []FEChunkReference  `json:"sourceChunks"`
	SupportingQuotes   []FESupportingQuote `json:"supportingQuotes"`
	VerificationMethod string              `json:"verificationMethod"` // rag|grounding|manual
	Claim              string              `json:"claim,omitempty"`
}

type FEInlineCitation struct {
	CitationNumber     int    `json:"citationNumber"`
	PaperID            string `json:"paperId"`
	PaperTitle         string `json:"paperTitle"`
	Authors            string `json:"authors"`
	Year               int    `json:"year"`
	ChunkID            string `json:"chunkId"`
	ChunkContent       string `json:"chunkContent"`
	ClaimText          string `json:"claimText"`
	ConfidenceScore    int    `json:"confidenceScore"`
	CharStart          int    `json:"charStart"`
	CharEnd            int    `json:"charEnd"`
	IsSynthesis        bool   `json:"isSynthesis"`
	SourceSection      string `json:"sourceSection,omitempty"`
	SourcePage         int    `json:"sourcePage,omitempty"`
	SourceCharStart    int    `json:"sourceCharStart,omitempty"`
	SourceCharEnd      int    `json:"sourceCharEnd,omitempty"`
	VerificationMethod string `json:"verificationMethod,omitempty"`
	VerifiedAt         string `json:"verifiedAt,omitempty"`
}

func (h *SynthesisHandler) handleVerifyCitations(w http.ResponseWriter, r *http.Request) {
	var req verifyCitationsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, verifyCiteMaxBodyBytes)).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = "section"
	}

	ctx := r.Context()
	var payload any
	var errMsg string
	switch mode {
	case "ground":
		payload = h.verifyCiteGround(req)
	case "section":
		payload = map[string]any{"citations": h.verifyCiteSection(ctx, req)}
	case "verify":
		payload = h.verifyCiteSingle(ctx, req)
	case "trace":
		payload = map[string]any{"chunks": h.verifyCiteTrace(ctx, req)}
	case "quotes":
		payload = map[string]any{"quotes": h.verifyCiteQuotes(ctx, req)}
	case "trace-full":
		payload = h.verifyCiteTraced(ctx, req)
	case "claim-chunk":
		payload = map[string]any{"confidence": h.verifyCiteClaimChunk(ctx, req)}
	case "best-chunk":
		payload = h.verifyCiteBestChunk(ctx, req)
	case "numbered":
		cites, ungrounded := h.verifyCiteNumbered(req)
		payload = map[string]any{"citations": cites, "ungroundedNumbers": ungrounded}
	case "inline":
		payload = map[string]any{"citations": h.verifyCiteInline(ctx, req)}
	case "batch":
		payload = map[string]any{"citations": h.verifyCiteBatch(ctx, req)}
	case "structured":
		payload = map[string]any{"citations": h.verifyCiteStructured(ctx, req)}
	default:
		errMsg = "invalid mode"
	}
	if errMsg != "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, errMsg, map[string]any{
			"allowedModes": []string{
				"ground", "section", "verify", "trace", "quotes", "trace-full",
				"claim-chunk", "best-chunk", "numbered", "inline", "batch", "structured",
			},
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func (h *SynthesisHandler) verifyCiteGround(req verifyCitationsRequest) map[string]any {
	opts := req.Options
	removeUnverified := false
	if opts.RemoveUnverified != nil {
		removeUnverified = *opts.RemoveUnverified
	}
	flagUnverified := true
	if opts.FlagUnverified != nil {
		flagUnverified = *opts.FlagUnverified
	}
	minConf := opts.MinConfidence
	if minConf <= 0 {
		minConf = verifyCiteMinConfidenceDefault
	}

	extracted := extractCitationsFromText(req.Text)
	grounded := make([]FEGroundedCitation, 0, len(extracted))
	groundedText := req.Text
	removed := 0

	for _, cit := range extracted {
		match := matchCitationToSource(cit, req.Sources)
		verified := match.source != nil && IsCitationVerified(match.source, false, match.confidence, minConf)
		item := FEGroundedCitation{
			Text:       cit.Text,
			PaperID:    cit.PaperID,
			Title:      cit.Title,
			Authors:    cit.Authors,
			Year:       cit.Year,
			Verified:   verified,
			Confidence: match.confidence,
			MatchType:  match.matchType,
		}
		if match.source != nil {
			src := *match.source
			item.MatchedSource = &src
			if item.PaperID == "" {
				item.PaperID = src.PaperID
			}
		}
		grounded = append(grounded, item)

		if !verified {
			if removeUnverified {
				groundedText = strings.Replace(groundedText, cit.Text, "", 1)
				removed++
			} else if flagUnverified {
				groundedText = strings.Replace(groundedText, cit.Text, cit.Text+" [⚠️ unverified]", 1)
			}
		}
	}

	if removeUnverified {
		groundedText = collapseGroundingArtifacts(groundedText)
	}

	verifiedN := 0
	for _, c := range grounded {
		if c.Verified {
			verifiedN++
		}
	}
	return map[string]any{
		"originalText": req.Text,
		"groundedText": groundedText,
		"citations":    grounded,
		"stats": map[string]int{
			"total":      len(extracted),
			"verified":   verifiedN,
			"unverified": len(extracted) - verifiedN,
			"removed":    removed,
		},
	}
}

func (h *SynthesisHandler) verifyCiteSection(ctx context.Context, req verifyCitationsRequest) []FETracedCitation {
	extracted := extractCitationsFromText(req.Text)
	out := make([]FETracedCitation, 0, len(extracted))
	for _, cit := range extracted {
		resolved, byMarker := resolveCitationSource(cit, req.Sources)
		if resolved == nil {
			if cit.PaperID == "" && !(cit.Authors != "" && cit.Year != "") {
				continue // bare quoted strings — skip
			}
			out = append(out, FETracedCitation{
				CitationID:         newCitationID(),
				CitationText:       cit.Text,
				PaperID:            "",
				Verified:           false,
				Confidence:         0,
				SourceChunks:       []FEChunkReference{},
				SupportingQuotes:   []FESupportingQuote{},
				VerificationMethod: "manual",
			})
			continue
		}
		traced := h.buildTracedCitation(ctx, cit.Text, resolved.PaperID, req.Sources, req.Chunks, "")
		applyMarkerGroundingVerification(&traced, byMarker)
		out = append(out, traced)
	}
	return out
}

func (h *SynthesisHandler) verifyCiteSingle(ctx context.Context, req verifyCitationsRequest) map[string]any {
	citationText := strings.TrimSpace(req.CitationText)
	if citationText == "" {
		citationText = strings.TrimSpace(req.Text)
	}
	grounding := h.verifyCiteGround(verifyCitationsRequest{
		Text:    citationText,
		Sources: req.Sources,
		Options: verifyCitationsOptions{
			RemoveUnverified: boolPtr(false),
			FlagUnverified:   boolPtr(false),
			MinConfidence:    verifyCiteMinConfidenceDefault,
		},
	})
	citations, _ := grounding["citations"].([]FEGroundedCitation)
	var grounded *FEGroundedCitation
	for i := range citations {
		c := citations[i]
		if c.Text == citationText || strings.Contains(citationText, c.Text) {
			grounded = &c
			break
		}
	}
	if grounded != nil && grounded.Verified {
		return map[string]any{
			"verified":      true,
			"confidence":    grounded.Confidence,
			"matchedSource": grounded.MatchedSource,
			"method":        "grounding",
		}
	}

	paperID := strings.TrimSpace(req.PaperID)
	if paperID != "" && len(req.Chunks) > 0 {
		rag := h.ragVerifyAgainstChunks(ctx, citationText, paperID, req.Chunks)
		if rag.verified {
			var matched *verifyCitationSource
			for i := range req.Sources {
				if req.Sources[i].PaperID == paperID {
					src := req.Sources[i]
					matched = &src
					break
				}
			}
			return map[string]any{
				"verified":      true,
				"confidence":    rag.confidence,
				"matchedSource": matched,
				"method":        "rag",
			}
		}
	}

	conf := 0.0
	if grounded != nil {
		conf = grounded.Confidence
	}
	return map[string]any{
		"verified":   false,
		"confidence": conf,
		"method":     "none",
	}
}

func (h *SynthesisHandler) verifyCiteTrace(ctx context.Context, req verifyCitationsRequest) []FEChunkReference {
	paperID := strings.TrimSpace(req.PaperID)
	searchText := strings.TrimSpace(req.Claim)
	if searchText == "" {
		searchText = strings.TrimSpace(req.CitationText)
	}
	if searchText == "" {
		searchText = strings.TrimSpace(req.Text)
	}
	return h.findSimilarChunks(ctx, searchText, paperID, req.Chunks, 10, verifyCiteTraceThreshold, searchText)
}

func (h *SynthesisHandler) verifyCiteQuotes(ctx context.Context, req verifyCitationsRequest) []FESupportingQuote {
	maxQuotes := req.Options.MaxQuotes
	if maxQuotes <= 0 {
		maxQuotes = 5
	}
	claim := strings.TrimSpace(req.Claim)
	if claim == "" {
		claim = strings.TrimSpace(req.Text)
	}
	chunks := h.verifyCiteTrace(ctx, verifyCitationsRequest{
		PaperID: req.PaperID,
		Claim:   claim,
		Chunks:  req.Chunks,
	})
	if len(chunks) > maxQuotes {
		chunks = chunks[:maxQuotes]
	}
	quotes := make([]FESupportingQuote, 0, len(chunks))
	for _, chunk := range chunks {
		quotes = append(quotes, FESupportingQuote{
			Text:           pickBestQuote(chunk.Content, claim),
			ChunkID:        chunk.ChunkID,
			CharStart:      chunk.CharStart,
			CharEnd:        chunk.CharEnd,
			RelevanceScore: chunk.RelevanceScore,
			PaperID:        chunk.PaperID,
		})
	}
	return quotes
}

func (h *SynthesisHandler) verifyCiteTraced(ctx context.Context, req verifyCitationsRequest) FETracedCitation {
	citationText := strings.TrimSpace(req.CitationText)
	if citationText == "" {
		citationText = strings.TrimSpace(req.Text)
	}
	return h.buildTracedCitation(ctx, citationText, req.PaperID, req.Sources, req.Chunks, req.Claim)
}

func (h *SynthesisHandler) verifyCiteClaimChunk(ctx context.Context, req verifyCitationsRequest) int {
	claim := strings.TrimSpace(req.Claim)
	if claim == "" {
		claim = strings.TrimSpace(req.Text)
	}
	chunk := strings.TrimSpace(req.ChunkContent)
	if claim == "" || chunk == "" {
		return 0
	}
	sim := h.embeddingSimilarity(ctx, claim, chunk)
	if sim < 0 {
		// Keyword fallback when embeddings unavailable.
		sim = keywordOverlapScore(claim, chunk)
	}
	score := int(math.Round(math.Max(0, math.Min(100, sim*100))))
	return score
}

func (h *SynthesisHandler) verifyCiteBestChunk(ctx context.Context, req verifyCitationsRequest) map[string]any {
	claim := strings.TrimSpace(req.Claim)
	if claim == "" {
		claim = strings.TrimSpace(req.Text)
	}
	if claim == "" || len(req.Chunks) == 0 {
		return map[string]any{"chunk": nil, "confidence": 0}
	}

	type candidate struct {
		chunk        verifyCitationChunk
		keywordScore float64
	}
	claimWords := significantWords(claim)
	cands := make([]candidate, 0, len(req.Chunks))
	for _, chunk := range req.Chunks {
		score := keywordOverlapRatio(claimWords, significantWords(chunk.Content))
		if score > 0.2 {
			cands = append(cands, candidate{chunk: chunk, keywordScore: score})
		}
	}
	// Sort by keyword score desc, take top 5.
	for i := 0; i < len(cands); i++ {
		for j := i + 1; j < len(cands); j++ {
			if cands[j].keywordScore > cands[i].keywordScore {
				cands[i], cands[j] = cands[j], cands[i]
			}
		}
	}
	if len(cands) > 5 {
		cands = cands[:5]
	}

	var best *verifyCitationChunk
	bestConf := 0
	for _, c := range cands {
		conf := h.verifyCiteClaimChunk(ctx, verifyCitationsRequest{
			Claim:        claim,
			ChunkContent: c.chunk.Content,
		})
		if conf > bestConf {
			bestConf = conf
			ch := c.chunk
			best = &ch
		}
	}
	if best == nil || bestConf <= verifyCiteBestChunkMin {
		return map[string]any{"chunk": nil, "confidence": 0}
	}
	return map[string]any{"chunk": best, "confidence": bestConf}
}

func (h *SynthesisHandler) verifyCiteNumbered(req verifyCitationsRequest) ([]FEInlineCitation, []int) {
	chunkByNumber := make(map[int]verifyCitationChunk, len(req.Chunks))
	for _, c := range req.Chunks {
		if c.Number > 0 {
			chunkByNumber[c.Number] = c
		}
	}
	citations := make([]FEInlineCitation, 0)
	ungrounded := make([]int, 0)
	seen := make(map[int]bool)

	matches := verifyCiteSentenceRE.FindAllStringSubmatchIndex(req.Text, -1)
	for _, loc := range matches {
		if len(loc) < 6 {
			continue
		}
		sentence := strings.TrimSpace(req.Text[loc[2]:loc[3]])
		numStr := req.Text[loc[4]:loc[5]]
		citNum, err := strconv.Atoi(numStr)
		if err != nil {
			continue
		}
		chunk, ok := chunkByNumber[citNum]
		if !ok {
			if !seen[citNum] {
				ungrounded = append(ungrounded, citNum)
				seen[citNum] = true
			}
			continue
		}
		sentenceWords := significantWords(sentence)
		chunkWords := significantWords(chunk.Content)
		conf := 0
		if len(sentenceWords) > 0 {
			conf = int(math.Round(keywordOverlapRatio(sentenceWords, chunkWords) * 100))
		}
		citations = append(citations, FEInlineCitation{
			CitationNumber:     citNum,
			PaperID:            firstNonEmpty(chunk.PaperID, chunk.PaperIDAlt),
			PaperTitle:         chunk.PaperTitle,
			Authors:            chunk.Authors,
			Year:               chunk.Year,
			ChunkID:            firstNonEmpty(chunk.ChunkID, chunk.ID),
			ChunkContent:       chunk.Content,
			ClaimText:          sentence,
			ConfidenceScore:    conf,
			CharStart:          loc[0],
			CharEnd:            loc[1],
			IsSynthesis:        false,
			SourceSection:      firstNonEmpty(chunk.Section, chunk.SectionType, chunk.SectionAlt),
			SourcePage:         chunk.SourcePage,
			SourceCharStart:    chunk.CharStart,
			SourceCharEnd:      chunk.CharEnd,
			VerificationMethod: "auto",
		})
	}
	return citations, ungrounded
}

func (h *SynthesisHandler) verifyCiteInline(ctx context.Context, req verifyCitationsRequest) []FEInlineCitation {
	out := make([]FEInlineCitation, 0, len(req.InlineCitations))
	now := time.Now().UTC().Format(time.RFC3339)
	for _, cit := range req.InlineCitations {
		conf := h.verifyCiteClaimChunk(ctx, verifyCitationsRequest{
			Claim:        cit.ClaimText,
			ChunkContent: cit.ChunkContent,
		})
		out = append(out, FEInlineCitation{
			CitationNumber:     cit.CitationNumber,
			PaperID:            cit.PaperID,
			PaperTitle:         cit.PaperTitle,
			Authors:            cit.Authors,
			Year:               cit.Year,
			ChunkID:            cit.ChunkID,
			ChunkContent:       cit.ChunkContent,
			ClaimText:          cit.ClaimText,
			ConfidenceScore:    conf,
			CharStart:          cit.CharStart,
			CharEnd:            cit.CharEnd,
			IsSynthesis:        cit.IsSynthesis,
			SourceSection:      cit.SourceSection,
			SourcePage:         cit.SourcePage,
			VerificationMethod: "auto",
			VerifiedAt:         now,
		})
	}
	return out
}

func (h *SynthesisHandler) verifyCiteBatch(ctx context.Context, req verifyCitationsRequest) []FETracedCitation {
	out := make([]FETracedCitation, 0, len(req.Citations))
	for _, c := range req.Citations {
		out = append(out, h.buildTracedCitation(ctx, c.Text, c.PaperID, req.Sources, req.Chunks, c.Claim))
	}
	return out
}

func (h *SynthesisHandler) buildTracedCitation(
	ctx context.Context,
	citationText, paperID string,
	sources []verifyCitationSource,
	chunks []verifyCitationChunk,
	claim string,
) FETracedCitation {
	verification := h.verifyCiteSingle(ctx, verifyCitationsRequest{
		CitationText: citationText,
		PaperID:      paperID,
		Sources:      sources,
		Chunks:       chunks,
	})
	verified, _ := verification["verified"].(bool)
	confidence, _ := verification["confidence"].(float64)
	method, _ := verification["method"].(string)
	if method == "" || method == "none" {
		method = "manual"
	}

	searchClaim := claim
	if searchClaim == "" {
		searchClaim = citationText
	}
	sourceChunks := h.findSimilarChunks(ctx, searchClaim, paperID, chunks, 10, verifyCiteTraceThreshold, claim)
	quotes := h.verifyCiteQuotes(ctx, verifyCitationsRequest{
		PaperID: paperID,
		Claim:   searchClaim,
		Chunks:  chunks,
		Options: verifyCitationsOptions{MaxQuotes: 5},
	})

	return FETracedCitation{
		CitationID:         newCitationID(),
		CitationText:       citationText,
		PaperID:            paperID,
		Verified:           verified,
		Confidence:         confidence,
		SourceChunks:       sourceChunks,
		SupportingQuotes:   quotes,
		VerificationMethod: method,
		Claim:              claim,
	}
}

type ragVerifyResult struct {
	verified   bool
	confidence float64
}

func (h *SynthesisHandler) ragVerifyAgainstChunks(
	ctx context.Context,
	text, paperID string,
	chunks []verifyCitationChunk,
) ragVerifyResult {
	refs := h.findSimilarChunks(ctx, text, paperID, chunks, 5, verifyCiteRAGThreshold, "")
	if len(refs) == 0 {
		return ragVerifyResult{}
	}
	sum := 0.0
	for _, r := range refs {
		sum += r.RelevanceScore
	}
	avg := sum / float64(len(refs))
	return ragVerifyResult{verified: avg >= verifyCiteRAGThreshold, confidence: avg}
}

func (h *SynthesisHandler) findSimilarChunks(
	ctx context.Context,
	query, paperID string,
	chunks []verifyCitationChunk,
	limit int,
	threshold float64,
	usedFor string,
) []FEChunkReference {
	filtered := make([]verifyCitationChunk, 0, len(chunks))
	for _, c := range chunks {
		pid := firstNonEmpty(c.PaperID, c.PaperIDAlt)
		if paperID != "" && pid != "" && pid != paperID {
			continue
		}
		if strings.TrimSpace(c.Content) == "" {
			continue
		}
		filtered = append(filtered, c)
	}
	if len(filtered) == 0 || strings.TrimSpace(query) == "" {
		return []FEChunkReference{}
	}

	type scored struct {
		chunk verifyCitationChunk
		score float64
	}
	scoredChunks := make([]scored, 0, len(filtered))

	// Prefer embedding similarity when available; fall back to keyword overlap.
	queryEmb := h.embedOne(ctx, query)
	if len(queryEmb) > 0 {
		texts := make([]string, len(filtered))
		for i, c := range filtered {
			texts[i] = c.Content
		}
		embs := h.embedMany(ctx, texts)
		for i, c := range filtered {
			sim := 0.0
			if i < len(embs) && len(embs[i]) > 0 {
				sim = cosineSimilarity(queryEmb, embs[i])
			} else if c.Similarity > 0 {
				sim = c.Similarity
			} else {
				sim = keywordOverlapScore(query, c.Content)
			}
			if sim >= threshold {
				scoredChunks = append(scoredChunks, scored{chunk: c, score: sim})
			}
		}
	} else {
		for _, c := range filtered {
			sim := c.Similarity
			if sim <= 0 {
				sim = keywordOverlapScore(query, c.Content)
			}
			if sim >= threshold {
				scoredChunks = append(scoredChunks, scored{chunk: c, score: sim})
			}
		}
	}

	for i := 0; i < len(scoredChunks); i++ {
		for j := i + 1; j < len(scoredChunks); j++ {
			if scoredChunks[j].score > scoredChunks[i].score {
				scoredChunks[i], scoredChunks[j] = scoredChunks[j], scoredChunks[i]
			}
		}
	}
	if limit > 0 && len(scoredChunks) > limit {
		scoredChunks = scoredChunks[:limit]
	}

	out := make([]FEChunkReference, 0, len(scoredChunks))
	for _, s := range scoredChunks {
		c := s.chunk
		idx := c.ChunkIndex
		if idx == 0 && c.ChunkIndex2 != 0 {
			idx = c.ChunkIndex2
		}
		charStart := c.CharStart
		charEnd := c.CharEnd
		if charStart == 0 && charEnd == 0 {
			charStart = idx * 500
			charEnd = charStart + len(c.Content)
		}
		out = append(out, FEChunkReference{
			ChunkID:        firstNonEmpty(c.ChunkID, c.ID, fmt.Sprintf("chunk_%d", idx)),
			PaperID:        firstNonEmpty(c.PaperID, c.PaperIDAlt, paperID),
			ChunkIndex:     idx,
			CharStart:      charStart,
			CharEnd:        charEnd,
			Content:        c.Content,
			RelevanceScore: s.score,
			UsedFor:        usedFor,
			SectionType:    firstNonEmpty(c.SectionType, c.SectionAlt, c.Section),
		})
	}
	return out
}

func (h *SynthesisHandler) embeddingSimilarity(ctx context.Context, a, b string) float64 {
	embA := h.embedOne(ctx, a)
	embB := h.embedOne(ctx, b)
	if len(embA) == 0 || len(embB) == 0 {
		return -1
	}
	return cosineSimilarity(embA, embB)
}

func (h *SynthesisHandler) embedOne(ctx context.Context, text string) []float64 {
	if h == nil || h.llmClient == nil || strings.TrimSpace(text) == "" {
		return nil
	}
	embedder, ok := h.llmClient.(embedClient)
	if !ok {
		return nil
	}
	embedCtx, cancel := context.WithTimeout(ctx, verifyCiteEmbedTimeout)
	defer cancel()
	resp, err := embedder.Embed(embedCtx, &llmv1.EmbedRequest{Text: text})
	if err != nil || resp == nil {
		return nil
	}
	return float32SliceToFloat64(resp.GetEmbedding())
}

func (h *SynthesisHandler) embedMany(ctx context.Context, texts []string) [][]float64 {
	if h == nil || h.llmClient == nil || len(texts) == 0 {
		return nil
	}
	if batcher, ok := h.llmClient.(embedBatchClient); ok {
		embedCtx, cancel := context.WithTimeout(ctx, verifyCiteEmbedTimeout)
		defer cancel()
		resp, err := batcher.EmbedBatch(embedCtx, &llmv1.EmbedBatchRequest{Texts: texts})
		if err == nil && resp != nil {
			out := make([][]float64, 0, len(resp.GetEmbeddings()))
			for _, emb := range resp.GetEmbeddings() {
				out = append(out, float32SliceToFloat64(emb.GetValues()))
			}
			return out
		}
	}
	out := make([][]float64, len(texts))
	for i, t := range texts {
		out[i] = h.embedOne(ctx, t)
	}
	return out
}

func float32SliceToFloat64(in []float32) []float64 {
	if len(in) == 0 {
		return nil
	}
	out := make([]float64, len(in))
	for i, v := range in {
		out[i] = float64(v)
	}
	return out
}

// --- extraction / matching (ported from citationGroundingService) ---

func extractCitationsFromText(text string) []extractedCitation {
	citations := make([]extractedCitation, 0)
	seen := make(map[string]bool)

	add := func(c extractedCitation) {
		if c.Text == "" || seen[c.Text] {
			return
		}
		seen[c.Text] = true
		citations = append(citations, c)
	}

	for _, m := range verifyCiteParenRE.FindAllStringSubmatch(text, -1) {
		inner := m[1]
		year := ""
		if ym := regexp.MustCompile(`(\d{4})`).FindStringSubmatch(inner); len(ym) > 1 {
			year = ym[1]
		}
		authors := strings.TrimSpace(regexp.MustCompile(`,?\s*\d{4}`).ReplaceAllString(inner, ""))
		add(extractedCitation{Text: m[0], Authors: authors, Year: year})
	}
	for _, m := range verifyCiteInlineRE.FindAllStringSubmatch(text, -1) {
		add(extractedCitation{Text: m[0], Authors: m[1], Year: m[2]})
	}
	for _, m := range verifyCiteNumberedRE.FindAllStringSubmatch(text, -1) {
		add(extractedCitation{Text: m[0], PaperID: m[1]})
	}
	for _, m := range verifyCiteTitleRE.FindAllStringSubmatch(text, -1) {
		add(extractedCitation{Text: m[0], Title: m[1]})
	}
	return citations
}

type citationMatch struct {
	source     *verifyCitationSource
	confidence float64
	matchType  string
}

func matchCitationToSource(citation extractedCitation, sources []verifyCitationSource) citationMatch {
	if citation.Title != "" {
		titleLower := strings.ToLower(citation.Title)
		for i := range sources {
			s := &sources[i]
			st := strings.ToLower(strings.TrimSpace(s.Title))
			if st == titleLower {
				return citationMatch{source: s, confidence: 1.0, matchType: "exact"}
			}
		}
		for i := range sources {
			s := &sources[i]
			st := strings.ToLower(strings.TrimSpace(s.Title))
			if st != "" && (strings.Contains(st, titleLower) || strings.Contains(titleLower, st)) {
				return citationMatch{source: s, confidence: 0.8, matchType: "fuzzy"}
			}
		}
	}

	if citation.Authors != "" && citation.Year != "" {
		authorNeedle := strings.ToLower(strings.ReplaceAll(citation.Authors, " et al.", ""))
		authorNeedle = strings.TrimSpace(strings.ReplaceAll(authorNeedle, " et al", ""))
		for i := range sources {
			s := &sources[i]
			if sourceAuthorsString(s) != "" && strings.Contains(sourceAuthorsString(s), authorNeedle) && sourceYearString(s) == citation.Year {
				return citationMatch{source: s, confidence: 0.9, matchType: "exact"}
			}
		}
		for i := range sources {
			s := &sources[i]
			if strings.Contains(sourceAuthorsString(s), authorNeedle) {
				return citationMatch{source: s, confidence: 0.6, matchType: "partial"}
			}
		}
	}

	if citation.Authors != "" {
		authorNeedle := strings.ToLower(strings.ReplaceAll(citation.Authors, " et al.", ""))
		authorNeedle = strings.TrimSpace(strings.ReplaceAll(authorNeedle, " et al", ""))
		for i := range sources {
			s := &sources[i]
			if strings.Contains(sourceAuthorsString(s), authorNeedle) {
				return citationMatch{source: s, confidence: 0.4, matchType: "partial"}
			}
		}
	}

	return citationMatch{matchType: "none"}
}

func resolveCitationSource(citation extractedCitation, sources []verifyCitationSource) (*verifyCitationSource, bool) {
	if citation.PaperID != "" {
		for i := range sources {
			if sources[i].PaperID == citation.PaperID {
				return &sources[i], true
			}
		}
		idx, err := strconv.Atoi(citation.PaperID)
		if err == nil && idx >= 1 && idx <= len(sources) && sources[idx-1].PaperID != "" {
			return &sources[idx-1], true
		}
		return nil, false
	}

	if citation.Authors != "" && citation.Year != "" {
		authorNeedle := strings.ToLower(citation.Authors)
		authorNeedle = regexp.MustCompile(`\s+et\s+al\.?`).ReplaceAllString(authorNeedle, "")
		authorNeedle = strings.TrimSpace(authorNeedle)
		for i := range sources {
			s := &sources[i]
			if sourceYearString(s) != citation.Year {
				continue
			}
			for _, a := range s.Authors {
				if strings.Contains(strings.ToLower(a.Name), authorNeedle) {
					return s, false
				}
			}
		}
		return nil, false
	}

	if citation.Title != "" {
		titleLower := strings.ToLower(citation.Title)
		for i := range sources {
			s := &sources[i]
			st := strings.ToLower(s.Title)
			if st == titleLower || strings.Contains(st, titleLower) {
				return s, false
			}
		}
	}
	return nil, false
}

func sourceAuthorsString(s *verifyCitationSource) string {
	if s == nil || len(s.Authors) == 0 {
		return ""
	}
	parts := make([]string, 0, len(s.Authors))
	for _, a := range s.Authors {
		parts = append(parts, strings.ToLower(a.Name))
	}
	return strings.Join(parts, ", ")
}

func sourceYearString(s *verifyCitationSource) string {
	if s == nil {
		return ""
	}
	if s.PublishDate != nil && s.PublishDate.Year > 0 {
		return strconv.Itoa(s.PublishDate.Year)
	}
	switch y := s.Year.(type) {
	case float64:
		if y > 0 {
			return strconv.Itoa(int(y))
		}
	case string:
		if len(y) >= 4 {
			return y[:4]
		}
		return y
	case json.Number:
		return y.String()
	}
	if len(s.Date) >= 4 {
		return s.Date[:4]
	}
	return ""
}

func pickBestQuote(content, claim string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	sentences := regexp.MustCompile(`[.!?]+`).Split(content, -1)
	claimWords := significantWords(claim)
	best := ""
	maxMatches := 0
	for _, s := range sentences {
		s = strings.TrimSpace(s)
		if len(s) <= 20 {
			continue
		}
		matches := 0
		sw := significantWords(s)
		swSet := make(map[string]bool, len(sw))
		for _, w := range sw {
			swSet[w] = true
		}
		for _, w := range claimWords {
			if swSet[w] {
				matches++
			}
		}
		if matches > maxMatches {
			maxMatches = matches
			best = s
		}
	}
	if best != "" {
		return best
	}
	if len(content) > 200 {
		return content[:200]
	}
	return content
}

func significantWords(text string) []string {
	fields := strings.Fields(strings.ToLower(text))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimFunc(f, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
		if len(f) > 3 {
			out = append(out, f)
		}
	}
	return out
}

func keywordOverlapRatio(a, b []string) float64 {
	if len(a) == 0 {
		return 0
	}
	bSet := make(map[string]bool, len(b))
	for _, w := range b {
		bSet[w] = true
	}
	hits := 0
	for _, w := range a {
		if bSet[w] {
			hits++
		}
	}
	return float64(hits) / float64(len(a))
}

func keywordOverlapScore(a, b string) float64 {
	return keywordOverlapRatio(significantWords(a), significantWords(b))
}

func collapseGroundingArtifacts(text string) string {
	text = regexp.MustCompile(`\s{2,}`).ReplaceAllString(text, " ")
	text = regexp.MustCompile(`\.\s*\.`).ReplaceAllString(text, ".")
	return strings.TrimSpace(text)
}

func newCitationID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("cite_%d_%s", time.Now().UnixMilli(), hex.EncodeToString(b[:]))
}
