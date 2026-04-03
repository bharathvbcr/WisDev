package api

import (
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
)

// ==========================================
// TYPES
// ==========================================

// PaperEmbedding holds a paper ID and its embedding vector.
type PaperEmbedding struct {
	ID        string    `json:"id"`
	Embedding []float64 `json:"embedding"`
}

// PaperScore holds a paper ID and its computed similarity score.
type PaperScore struct {
	ID    string  `json:"id"`
	Score float64 `json:"score"`
}

// BatchSimilarityRequest is the request body for POST /v2/vector/batch-similarity.
type BatchSimilarityRequest struct {
	QueryEmbedding []float64        `json:"query_embedding"`
	Papers         []PaperEmbedding `json:"papers"`
}

// BatchSimilarityResponse is the response for POST /v2/vector/batch-similarity.
type BatchSimilarityResponse struct {
	Scores    []PaperScore `json:"scores"`
	LatencyMs int64        `json:"latency_ms"`
}

// FusePaper is a paper entry in a source list to be fused.
type FusePaper struct {
	PaperID string  `json:"paper_id"`
	DOI     string  `json:"doi"`
	Title   string  `json:"title"`
	Score   float64 `json:"score"`
}

// FuseSource is one ranked list from a single search source.
type FuseSource struct {
	Name   string      `json:"name"`
	Weight float64     `json:"weight"`
	IsBM25 bool        `json:"is_bm25"`
	Papers []FusePaper `json:"papers"`
}

// FuseRequest is the request body for POST /v2/vector/fuse.
type FuseRequest struct {
	// Mode is "rrf" (Reciprocal Rank Fusion) or "scores" (alpha-weighted).
	Mode string `json:"mode"`
	// Alpha is the vector source weight in scores mode; BM25 weight = 1-alpha.
	Alpha   float64      `json:"alpha"`
	Sources []FuseSource `json:"sources"`
	Limit   int          `json:"limit"`
}

// FusedPaper is a merged paper in the fusion response.
type FusedPaper struct {
	PaperID string  `json:"paper_id"`
	DOI     string  `json:"doi"`
	Title   string  `json:"title"`
	Score   float64 `json:"score"`
}

// FuseResponse is the response for POST /v2/vector/fuse.
type FuseResponse struct {
	Papers    []FusedPaper `json:"papers"`
	LatencyMs int64        `json:"latency_ms"`
}

// ==========================================
// MATH HELPERS
// ==========================================

// cosineSimilarity computes cosine similarity between two float64 vectors.
// Uses a single pass to compute dot product, normA², and normB² together.
// Returns 0 if either vector has zero norm (avoids NaN from 0/0).
// Go 1.22+ will auto-vectorize this tight loop on amd64 with -O2.
func cosineSimilarity(a, b []float64) float64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	if n == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := 0; i < n; i++ {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// minMaxNormalize normalizes PaperScore.Score values in place to the [0, 1] range.
// If all scores are identical (range == 0) the slice is left unchanged.
func minMaxNormalize(scores []PaperScore) {
	if len(scores) < 2 {
		return
	}
	minS, maxS := scores[0].Score, scores[0].Score
	for _, s := range scores[1:] {
		if s.Score < minS {
			minS = s.Score
		}
		if s.Score > maxS {
			maxS = s.Score
		}
	}
	rng := maxS - minS
	if rng <= 0 {
		return
	}
	for i := range scores {
		scores[i].Score = (scores[i].Score - minS) / rng
	}
}

// ==========================================
// DEDUP HELPER (shared with fusion)
// ==========================================

// fusionDedupeKey returns the canonical deduplication key for a FusePaper.
// DOI match takes priority over title match, mirroring deduplicatePapers in
// parallel_search_service.go.
func fusionDedupeKey(doi, title string) string {
	doi = strings.ToLower(strings.TrimSpace(doi))
	if doi != "" {
		return "doi:" + doi
	}
	nt := wisdev.NormalizeTitle(title)
	if nt == "" {
		return ""
	}
	return "title:" + nt
}

// ==========================================
// FUSION ALGORITHMS
// ==========================================

// rrfK is the standard RRF rank-smoothing constant.
const rrfK = 60.0

// fuseRRF applies Reciprocal Rank Fusion across all sources.
//
//	score(d) = Σ_source  weight(source) / (rrfK + rank(d, source))
//
// Weight defaults to 1.0 when ≤ 0. Rank is 1-based.
func fuseRRF(sources []FuseSource) map[string]float64 {
	scores := make(map[string]float64, 64)
	for _, src := range sources {
		w := src.Weight
		if w <= 0 {
			w = 1.0
		}
		for rank, p := range src.Papers {
			key := fusionDedupeKey(p.DOI, p.Title)
			if key == "" {
				continue
			}
			scores[key] += w / (rrfK + float64(rank+1))
		}
	}
	return scores
}

// fuseScores applies alpha-weighted score fusion.
// Vector sources use weight = alpha; BM25 sources use weight = 1 - alpha.
// Each source's scores are first normalized to [0, 1].
func fuseScores(sources []FuseSource, alpha float64) map[string]float64 {
	scores := make(map[string]float64, 64)
	for _, src := range sources {
		if len(src.Papers) == 0 {
			continue
		}
		// Compute min/max for normalization within this source.
		minS, maxS := src.Papers[0].Score, src.Papers[0].Score
		for _, p := range src.Papers[1:] {
			if p.Score < minS {
				minS = p.Score
			}
			if p.Score > maxS {
				maxS = p.Score
			}
		}
		rng := maxS - minS

		w := alpha
		if src.IsBM25 {
			w = 1.0 - alpha
		}
		if w < 0 {
			w = 0
		}

		for _, p := range src.Papers {
			key := fusionDedupeKey(p.DOI, p.Title)
			if key == "" {
				continue
			}
			normalized := p.Score
			if rng > 0 {
				normalized = (p.Score - minS) / rng
			}
			scores[key] += w * normalized
		}
	}
	return scores
}

// ==========================================
// HANDLERS
// ==========================================

// handleBatchSimilarity handles POST /v2/vector/batch-similarity.
//
// Request:
//
//	{
//	  "query_embedding": [float64, ...],   // len = embedding_dim (e.g. 768)
//	  "papers": [
//	    { "id": "abc", "embedding": [float64, ...] }
//	  ]
//	}
//
// Response:
//
//	{
//	  "scores": [{ "id": "abc", "score": 0.82 }],
//	  "latency_ms": 1
//	}
//
// Scores are min-max normalized to [0, 1] across the batch.
func HandleBatchSimilarity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}
	start := time.Now()

	var req BatchSimilarityRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}
	if len(req.QueryEmbedding) == 0 {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query_embedding is required and must be non-empty", map[string]any{
			"field": "query_embedding",
		})
		return
	}

	scores := make([]PaperScore, 0, len(req.Papers))
	skipped := 0
	for _, p := range req.Papers {
		if len(p.Embedding) == 0 {
			skipped++
			continue
		}
		scores = append(scores, PaperScore{
			ID:    p.ID,
			Score: cosineSimilarity(req.QueryEmbedding, p.Embedding),
		})
	}
	if skipped > 0 {
		slog.Warn("batch-similarity: skipped papers with empty embeddings", "count", skipped)
	}

	// Normalize to [0,1] so downstream thresholding (e.g. minSimilarity=0.45) is stable.
	minMaxNormalize(scores)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(BatchSimilarityResponse{
		Scores:    scores,
		LatencyMs: time.Since(start).Milliseconds(),
	}); err != nil {
		slog.Error("batch-similarity: encode error", "error", err)
	}
}

// handleFuseResults handles POST /v2/vector/fuse.
//
// Request:
//
//	{
//	  "mode":  "rrf" | "scores",
//	  "alpha": 0.6,
//	  "limit": 30,
//	  "sources": [
//	    { "name": "semantic_scholar", "weight": 1.0, "is_bm25": false,
//	      "papers": [{ "paper_id": "abc", "doi": "10.x", "title": "...", "score": 0.91 }] }
//	  ]
//	}
//
// Response: ranked fused papers with merged scores.
//
// Dedup policy: DOI match takes priority over normalized-title match.
func HandleFuseResults(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}
	start := time.Now()

	var req FuseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}
	if len(req.Sources) == 0 {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "sources must be non-empty", map[string]any{
			"field": "sources",
		})
		return
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 30
	}
	alpha := req.Alpha
	if alpha <= 0 || alpha > 1 {
		alpha = 0.6
	}

	// Compute fused scores.
	var scoreMap map[string]float64
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	switch mode {
	case "scores":
		scoreMap = fuseScores(req.Sources, alpha)
	default:
		// Default to RRF for "rrf" and any unrecognized mode.
		scoreMap = fuseRRF(req.Sources)
	}

	// Build metadata lookup by dedup key: first-seen paper wins for metadata.
	paperMeta := make(map[string]FusePaper, len(scoreMap))
	for _, src := range req.Sources {
		for _, p := range src.Papers {
			key := fusionDedupeKey(p.DOI, p.Title)
			if key == "" {
				continue
			}
			if _, exists := paperMeta[key]; !exists {
				paperMeta[key] = p
			}
		}
	}

	// Sort by descending fused score.
	type keyScore struct {
		key   string
		score float64
	}
	ranked := make([]keyScore, 0, len(scoreMap))
	for k, s := range scoreMap {
		ranked = append(ranked, keyScore{key: k, score: s})
	}
	sort.Slice(ranked, func(i, j int) bool {
		return ranked[i].score > ranked[j].score
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}

	// Build final output.
	papers := make([]FusedPaper, 0, len(ranked))
	for _, ks := range ranked {
		meta, ok := paperMeta[ks.key]
		if !ok {
			continue
		}
		papers = append(papers, FusedPaper{
			PaperID: meta.PaperID,
			DOI:     meta.DOI,
			Title:   meta.Title,
			Score:   ks.score,
		})
	}

	slog.Info("fuse-results completed",
		"mode", mode,
		"sources", len(req.Sources),
		"output_papers", len(papers),
		"latency_ms", time.Since(start).Milliseconds(),
	)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(FuseResponse{
		Papers:    papers,
		LatencyMs: time.Since(start).Milliseconds(),
	}); err != nil {
		slog.Error("fuse-results: encode error", "error", err)
	}
}
