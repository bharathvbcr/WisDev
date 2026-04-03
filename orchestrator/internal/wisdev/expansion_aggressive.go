package wisdev

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// ==========================================
// EXPANSION DICTIONARIES
// ==========================================

// synonymMap maps common academic terms to synonym lists.
// Port of SYNONYM_MAP from aggressiveQueryExpansionService.ts.
var synonymMap = map[string][]string{
	// AI / ML
	"machine learning":             {"ML", "statistical learning", "predictive modeling"},
	"deep learning":                {"DL", "neural network learning", "representation learning"},
	"artificial intelligence":      {"AI", "intelligent systems", "cognitive computing"},
	"natural language processing":  {"NLP", "text mining", "computational linguistics"},
	"computer vision":              {"CV", "image analysis", "visual computing"},
	"reinforcement learning":       {"RL", "reward learning", "sequential decision making"},
	"neural network":               {"ANN", "connectionist model", "deep network"},
	"transformer":                  {"attention model", "transformer architecture"},
	"large language model":         {"LLM", "foundation model", "pretrained language model"},
	"convolutional neural network": {"CNN", "ConvNet"},
	"recurrent neural network":     {"RNN", "sequence model"},
	// Medical
	"heart attack":        {"myocardial infarction", "MI", "acute coronary syndrome"},
	"stroke":              {"cerebrovascular accident", "CVA", "brain infarction"},
	"high blood pressure": {"hypertension", "HTN", "arterial hypertension"},
	"diabetes":            {"diabetes mellitus", "DM", "glycemic disorder"},
	"cancer":              {"neoplasm", "malignancy", "tumor", "carcinoma"},
	"depression":          {"depressive disorder", "MDD", "major depressive disorder"},
	"anxiety":             {"anxiety disorder", "GAD", "generalized anxiety"},
	"obesity":             {"overweight", "adiposity", "high BMI"},
	"inflammation":        {"inflammatory response", "inflammatory process"},
	// Research methods
	"randomized controlled trial": {"RCT", "randomized trial", "controlled study"},
	"systematic review":           {"SR", "evidence synthesis", "systematic analysis"},
	"meta-analysis":               {"meta analysis", "quantitative synthesis", "pooled analysis"},
	"cohort study":                {"cohort analysis", "longitudinal study", "follow-up study"},
	"case-control study":          {"case control", "retrospective study"},
	// Climate
	"climate change":   {"global warming", "climate crisis", "climatic shift"},
	"carbon emissions": {"CO2 emissions", "greenhouse gas", "carbon footprint"},
	"renewable energy": {"clean energy", "sustainable energy", "green energy"},
	"biodiversity":     {"biological diversity", "species diversity", "ecosystem diversity"},
}

// meshTermMap maps common clinical terms to MeSH (Medical Subject Headings).
// Port of MESH_TERM_MAP from aggressiveQueryExpansionService.ts.
var meshTermMap = map[string][]string{
	"heart attack": {"Myocardial Infarction", "Acute Coronary Syndrome"},
	"stroke":       {"Stroke", "Cerebrovascular Disorders", "Brain Ischemia"},
	"diabetes":     {"Diabetes Mellitus", "Diabetes Mellitus, Type 2", "Hyperglycemia"},
	"cancer":       {"Neoplasms", "Carcinoma", "Malignant Neoplasms"},
	"depression":   {"Depressive Disorder", "Depressive Disorder, Major"},
	"alzheimer":    {"Alzheimer Disease", "Dementia", "Cognitive Dysfunction"},
	"parkinson":    {"Parkinson Disease", "Parkinsonian Disorders"},
	"obesity":      {"Obesity", "Overweight", "Body Mass Index"},
	"hypertension": {"Hypertension", "Blood Pressure, High"},
	"covid":        {"COVID-19", "SARS-CoV-2", "Coronavirus Infections"},
	"vaccine":      {"Vaccines", "Immunization", "Vaccination"},
	"antibiotic":   {"Anti-Bacterial Agents", "Antibiotics", "Antimicrobial Agents"},
}

// abbreviationMap maps abbreviations to their full forms (bidirectional expand/contract).
// Port of ABBREVIATION_MAP from aggressiveQueryExpansionService.ts.
var abbreviationMap = map[string]string{
	"ml":   "machine learning",
	"ai":   "artificial intelligence",
	"dl":   "deep learning",
	"nlp":  "natural language processing",
	"cv":   "computer vision",
	"rl":   "reinforcement learning",
	"cnn":  "convolutional neural network",
	"rnn":  "recurrent neural network",
	"lstm": "long short-term memory",
	"gpt":  "generative pre-trained transformer",
	"bert": "bidirectional encoder representations from transformers",
	"llm":  "large language model",
	"rag":  "retrieval augmented generation",
	"rct":  "randomized controlled trial",
	"mi":   "myocardial infarction",
	"copd": "chronic obstructive pulmonary disease",
	"adhd": "attention deficit hyperactivity disorder",
	"mri":  "magnetic resonance imaging",
	"ct":   "computed tomography",
}

// conceptMap maps concepts to related concepts for conceptual expansion.
// Port of CONCEPT_MAP from aggressiveQueryExpansionService.ts.
var conceptMap = map[string][]string{
	"transformer":    {"attention mechanism", "self-attention", "encoder-decoder", "sequence model"},
	"bert":           {"language model", "pre-training", "masked language model", "fine-tuning"},
	"gpt":            {"autoregressive", "language generation", "causal language model"},
	"diffusion":      {"denoising", "score matching", "generative model", "latent space"},
	"fairness":       {"algorithmic bias", "AI ethics", "equitable AI", "discrimination"},
	"explainability": {"interpretability", "XAI", "model transparency", "black box"},
	"privacy":        {"data protection", "differential privacy", "anonymization", "GDPR"},
	"federated":      {"distributed learning", "decentralized", "edge computing"},
	"multimodal":     {"vision-language", "cross-modal", "multi-modal learning"},
}

// termHierarchy maps terms to their broader and narrower equivalents.
// Port of TERM_HIERARCHY from aggressiveQueryExpansionService.ts.
type termHierarchyEntry struct {
	broader  []string
	narrower []string
}

var termHierarchy = map[string]termHierarchyEntry{
	"deep learning": {
		broader:  []string{"machine learning", "artificial intelligence"},
		narrower: []string{"convolutional networks", "recurrent networks", "transformers", "GANs"},
	},
	"natural language processing": {
		broader:  []string{"artificial intelligence", "computational linguistics"},
		narrower: []string{"sentiment analysis", "named entity recognition", "machine translation", "question answering"},
	},
	"computer vision": {
		broader:  []string{"artificial intelligence", "image processing"},
		narrower: []string{"object detection", "image segmentation", "facial recognition", "video analysis"},
	},
	"cancer": {
		broader:  []string{"oncology", "disease", "pathology"},
		narrower: []string{"breast cancer", "lung cancer", "leukemia", "melanoma"},
	},
	"diabetes": {
		broader:  []string{"metabolic disease", "endocrine disorder"},
		narrower: []string{"type 1 diabetes", "type 2 diabetes", "gestational diabetes", "diabetic neuropathy"},
	},
}

// stopWordsExpansion is the set of stopwords used for keyword extraction.
var stopWordsExpansion = map[string]bool{
	"the": true, "a": true, "an": true, "in": true, "on": true,
	"at": true, "for": true, "to": true, "of": true, "and": true,
	"or": true, "with": true, "by": true, "is": true, "are": true,
}

// ==========================================
// TYPES
// ==========================================

// QueryVariation is one variant of an expanded query.
type QueryVariation struct {
	Query       string `json:"query"`
	Strategy    string `json:"strategy"`
	Priority    int    `json:"priority"`
	TargetAPI   string `json:"target_api,omitempty"`
	Description string `json:"description,omitempty"`
}

// AggressiveExpansionRequest is the request body for POST /v2/expand/aggressive.
type AggressiveExpansionRequest struct {
	Query                string   `json:"query"`
	MaxVariations        int      `json:"max_variations"`
	IncludeMeSH          bool     `json:"include_mesh"`
	IncludeAbbreviations bool     `json:"include_abbreviations"`
	IncludeTemporal      bool     `json:"include_temporal"`
	TargetAPIs           []string `json:"target_apis"`
}

// AggressiveExpansionResponse is the response for POST /v2/expand/aggressive.
type AggressiveExpansionResponse struct {
	Original   string            `json:"original"`
	Variations []QueryVariation  `json:"variations"`
	Metadata   expansionMetadata `json:"metadata"`
	LatencyMs  int64             `json:"latency_ms"`
}

type expansionMetadata struct {
	TotalVariations   int      `json:"total_variations"`
	Strategies        []string `json:"strategies"`
	EstimatedCoverage float64  `json:"estimated_coverage"`
	PrimaryKeywords   []string `json:"primary_keywords"`
}

// SPLADEExpansionRequest is the request body for POST /v2/expand/splade.
type SPLADEExpansionRequest struct {
	Query string `json:"query"`
}

// SPLADEExpansionResponse wraps the existing EnhancedQuery with typed fields.
type SPLADEExpansionResponse struct {
	Original  string   `json:"original"`
	Expanded  string   `json:"expanded"`
	Intent    string   `json:"intent"`
	Keywords  []string `json:"keywords"`
	Synonyms  []string `json:"synonyms"`
	LatencyMs int64    `json:"latency_ms"`
}

// ==========================================
// REDIS CACHE HELPERS
// ==========================================

func aggressiveCacheKey(query string, maxVariations int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("expand:v1:%s:%d", query, maxVariations)))
	return fmt.Sprintf("expand_aggressive:%x", h[:8])
}

func getAggressiveCache(rdb redis.UniversalClient, query string, maxVariations int) (*AggressiveExpansionResponse, bool) {
	if rdb == nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	val, err := rdb.Get(ctx, aggressiveCacheKey(query, maxVariations)).Result()
	if err != nil {
		return nil, false
	}
	var result AggressiveExpansionResponse
	if err := json.Unmarshal([]byte(val), &result); err != nil {
		return nil, false
	}
	return &result, true
}

func setAggressiveCache(rdb redis.UniversalClient, query string, maxVariations int, result *AggressiveExpansionResponse) {
	if rdb == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		data, err := json.Marshal(result)
		if err == nil {
			rdb.Set(ctx, aggressiveCacheKey(query, maxVariations), string(data), time.Hour)
		}
	}()
}

// ==========================================
// STRATEGY IMPLEMENTATIONS
// ==========================================

// s2SynonymVariations — Strategy 2 (priority 9): replace matched terms with synonyms.
func s2SynonymVariations(query string) []QueryVariation {
	var out []QueryVariation
	for term, synonyms := range synonymMap {
		if strings.Contains(query, strings.ToLower(term)) {
			for _, syn := range synonyms[:min2(2, len(synonyms))] {
				newQ := strings.Replace(query, strings.ToLower(term), strings.ToLower(syn), 1)
				if newQ != query {
					out = append(out, QueryVariation{
						Query:       newQ,
						Strategy:    "synonym",
						Priority:    9,
						Description: fmt.Sprintf("Synonym: %s → %s", term, syn),
					})
				}
			}
		}
	}
	return limitVariations(out, 3)
}

// s3MeSHVariations — Strategy 3 (priority 8): replace terms with MeSH headings.
func s3MeSHVariations(query string) []QueryVariation {
	var out []QueryVariation
	for term, meshTerms := range meshTermMap {
		if strings.Contains(query, strings.ToLower(term)) {
			for _, mesh := range meshTerms[:min2(2, len(meshTerms))] {
				newQ := strings.Replace(query, strings.ToLower(term), strings.ToLower(mesh), 1)
				if newQ != query {
					out = append(out, QueryVariation{
						Query:       newQ,
						Strategy:    "mesh",
						Priority:    8,
						TargetAPI:   "pubmed",
						Description: fmt.Sprintf("MeSH: %s → %s", term, mesh),
					})
				}
			}
		}
	}
	return limitVariations(out, 2)
}

// wordBoundaryRe returns a regex matching whole word occurrences of word (case-insensitive).
// Compiles a new regexp per call — used only for abbreviation expansion which is a small set.
func wordBoundaryRe(word string) *regexp.Regexp {
	re, err := regexp.Compile(`(?i)\b` + regexp.QuoteMeta(word) + `\b`)
	if err != nil {
		return nil
	}
	return re
}

// s4AbbreviationVariations — Strategy 4 (priority 8/7): expand abbreviations and contract full forms.
func s4AbbreviationVariations(query string) []QueryVariation {
	var out []QueryVariation
	words := strings.Fields(query)

	for _, word := range words {
		lower := strings.ToLower(word)
		// Expand abbreviation → full form.
		if full, ok := abbreviationMap[lower]; ok {
			re := wordBoundaryRe(word)
			if re != nil {
				newQ := re.ReplaceAllStringFunc(query, func(_ string) string { return full })
				out = append(out, QueryVariation{
					Query:       newQ,
					Strategy:    "abbreviation",
					Priority:    8,
					Description: fmt.Sprintf("Expanded: %s → %s", word, full),
				})
			}
		}
	}

	// Contract full form → abbreviation (first match only).
	for abbrev, full := range abbreviationMap {
		if strings.Contains(strings.ToLower(query), full) {
			newQ := strings.ReplaceAll(strings.ToLower(query), full, strings.ToUpper(abbrev))
			out = append(out, QueryVariation{
				Query:       newQ,
				Strategy:    "abbreviation",
				Priority:    7,
				Description: fmt.Sprintf("Abbreviated: %s → %s", full, abbrev),
			})
			break // one contraction per query
		}
	}

	return limitVariations(out, 2)
}

// s5BooleanVariations — Strategy 5 (priority 7): build "term OR synonym" Boolean queries.
func s5BooleanVariations(query string) []QueryVariation {
	for term, synonyms := range synonymMap {
		if strings.Contains(query, strings.ToLower(term)) && len(synonyms) > 0 {
			orGroup := fmt.Sprintf("(%s OR %s)", term, synonyms[0])
			newQ := strings.Replace(query, strings.ToLower(term), orGroup, 1)
			return []QueryVariation{{
				Query:       newQ,
				Strategy:    "boolean",
				Priority:    7,
				Description: fmt.Sprintf("Boolean OR: %s", term),
			}}
		}
	}
	return nil
}

// s6KeywordOnly — Strategy 6 (priority 7): strip stopwords, return core keywords.
func s6KeywordOnly(query string) *QueryVariation {
	words := strings.Fields(query)
	var keywords []string
	for _, w := range words {
		if len(w) > 2 && !stopWordsExpansion[strings.ToLower(w)] {
			keywords = append(keywords, w)
		}
	}
	if len(keywords) >= 2 && len(keywords) < len(words) {
		v := QueryVariation{
			Query:       strings.Join(keywords, " "),
			Strategy:    "keyword_only",
			Priority:    7,
			Description: "Core keywords only",
		}
		return &v
	}
	return nil
}

// s7ExactPhrase — Strategy 7 (priority 6): wrap query in double quotes for exact match.
func s7ExactPhrase(query string, wordCount int) *QueryVariation {
	if wordCount >= 2 && wordCount <= 5 {
		v := QueryVariation{
			Query:       `"` + query + `"`,
			Strategy:    "phrase",
			Priority:    6,
			Description: "Exact phrase match",
		}
		return &v
	}
	return nil
}

// s8BroaderVariations — Strategy 8 (priority 6): substitute matched term with broader terms.
func s8BroaderVariations(query string) []QueryVariation {
	var out []QueryVariation
	for term, hier := range termHierarchy {
		if strings.Contains(query, strings.ToLower(term)) {
			for _, broader := range hier.broader[:min2(1, len(hier.broader))] {
				out = append(out, QueryVariation{
					Query:       broader,
					Strategy:    "broader",
					Priority:    6,
					Description: fmt.Sprintf("Broader: %s → %s", term, broader),
				})
			}
		}
	}
	return limitVariations(out, 2)
}

// s9NarrowerVariations — Strategy 9 (priority 5): append narrower terms to query.
func s9NarrowerVariations(query string) []QueryVariation {
	var out []QueryVariation
	for term, hier := range termHierarchy {
		if strings.Contains(query, strings.ToLower(term)) {
			for _, narrower := range hier.narrower[:min2(2, len(hier.narrower))] {
				out = append(out, QueryVariation{
					Query:       query + " " + narrower,
					Strategy:    "narrower",
					Priority:    5,
					Description: fmt.Sprintf("Narrower: + %s", narrower),
				})
			}
		}
	}
	return limitVariations(out, 2)
}

// s10ConceptualVariations — Strategy 10 (priority 5): replace concept with related concepts.
func s10ConceptualVariations(query string) []QueryVariation {
	var out []QueryVariation
	for concept, related := range conceptMap {
		if strings.Contains(query, strings.ToLower(concept)) {
			for _, rel := range related[:min2(2, len(related))] {
				newQ := strings.Replace(query, strings.ToLower(concept), rel, 1)
				out = append(out, QueryVariation{
					Query:       newQ,
					Strategy:    "conceptual",
					Priority:    5,
					Description: fmt.Sprintf("Concept: %s → %s", concept, rel),
				})
			}
		}
	}
	return limitVariations(out, 2)
}

// s11TemporalVariations — Strategy 11 (priority 4): add year and "recent advances" suffix.
func s11TemporalVariations(query string) []QueryVariation {
	year := time.Now().Year()
	return []QueryVariation{
		{
			Query:       fmt.Sprintf("%s %d", query, year),
			Strategy:    "temporal",
			Priority:    4,
			Description: fmt.Sprintf("Recent: %d", year),
		},
		{
			Query:       query + " recent advances",
			Strategy:    "temporal",
			Priority:    4,
			Description: "Recent advances",
		},
	}
}

// s12PermutationVariations — Strategy 12 (priority 4): reorder words.
// For 2-word queries: swap. For 3-word queries: rotate first word to end.
func s12PermutationVariations(words []string) []QueryVariation {
	switch len(words) {
	case 2:
		return []QueryVariation{{
			Query:       words[1] + " " + words[0],
			Strategy:    "permutation",
			Priority:    4,
			Description: "Word order swap",
		}}
	case 3:
		rotated := strings.Join(append(words[1:], words[0]), " ")
		return []QueryVariation{{
			Query:       rotated,
			Strategy:    "permutation",
			Priority:    4,
			Description: "Word rotation",
		}}
	}
	return nil
}

// s13APIVariations — Strategy 13 (priority 3): API-specific query formatting.
func s13APIVariations(query string, apis []string) []QueryVariation {
	var out []QueryVariation
	for _, api := range apis {
		switch api {
		case "pubmed":
			out = append(out, QueryVariation{
				Query:       query + "[Title/Abstract]",
				Strategy:    "api_optimized",
				Priority:    3,
				TargetAPI:   "pubmed",
				Description: "PubMed field tag",
			})
		case "arxiv":
			out = append(out, QueryVariation{
				Query:       "all:" + query,
				Strategy:    "api_optimized",
				Priority:    3,
				TargetAPI:   "arxiv",
				Description: "arXiv all fields",
			})
		}
	}
	return out
}

// s14NgramVariations — Strategy 14 (priority 3): extract leading and trailing 2-grams.
func s14NgramVariations(words []string) []QueryVariation {
	if len(words) < 3 {
		return nil
	}
	bigram1 := words[0] + " " + words[1]
	bigram2 := words[len(words)-2] + " " + words[len(words)-1]

	out := []QueryVariation{{
		Query:       bigram1,
		Strategy:    "ngram",
		Priority:    3,
		Description: "2-gram (start)",
	}}
	if bigram2 != bigram1 {
		out = append(out, QueryVariation{
			Query:       bigram2,
			Strategy:    "ngram",
			Priority:    3,
			Description: "2-gram (end)",
		})
	}
	return out
}

// ==========================================
// HELPERS
// ==========================================

// deduplicateQueryVariations removes QueryVariation entries with duplicate
// normalized (lowercased + trimmed) query strings. First-seen wins.
func deduplicateQueryVariations(vs []QueryVariation) []QueryVariation {
	seen := make(map[string]bool, len(vs))
	out := make([]QueryVariation, 0, len(vs))
	for _, v := range vs {
		key := strings.ToLower(strings.TrimSpace(v.Query))
		if !seen[key] {
			seen[key] = true
			out = append(out, v)
		}
	}
	return out
}

// extractPrimaryKeywords returns up to 5 significant words from the query.
func extractPrimaryKeywords(query string) []string {
	words := strings.Fields(strings.ToLower(query))
	var out []string
	for _, w := range words {
		if len(w) > 2 && !stopWordsExpansion[w] {
			out = append(out, w)
			if len(out) == 5 {
				break
			}
		}
	}
	return out
}

// calculateCoverageEstimate is a heuristic matching the TS formula:
//
//	(min(variations/15,1)*0.6 + min(strategies/10,1)*0.4)
func calculateCoverageEstimate(variationCount, strategyCount int) float64 {
	v := float64(variationCount) / 15.0
	if v > 1 {
		v = 1
	}
	s := float64(strategyCount) / 10.0
	if s > 1 {
		s = 1
	}
	result := v*0.6 + s*0.4
	// Round to 2 decimal places.
	return float64(int(result*100)) / 100
}

// min2 returns the minimum of two ints. Avoids importing math for a trivial op.
func min2(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// limitVariations truncates vs to at most n items.
func limitVariations(vs []QueryVariation, n int) []QueryVariation {
	if len(vs) > n {
		return vs[:n]
	}
	return vs
}

// ==========================================
// CORE EXPANSION FUNCTION
// ==========================================

// generateAggressiveExpansion runs all 14 dictionary-based strategies against
// query and returns deduplicated, priority-sorted QueryVariation slice.
// NOTE: The AI-enhanced generation path (geminiGenerate) is NOT included here;
// that path remains in the TypeScript client.
func GenerateAggressiveExpansion(rdb redis.UniversalClient, query string, maxVariations int, includeMeSH, includeAbbrev, includeTemporal bool, targetAPIs []string) AggressiveExpansionResponse {
	start := time.Now()

	// 0. Cache Check
	if cached, ok := getAggressiveCache(rdb, query, maxVariations); ok {
		return *cached
	}

	cleanQuery := strings.ToLower(strings.TrimSpace(query))
	words := strings.Fields(cleanQuery)

	var variations []QueryVariation
	var strategiesUsed []string

	// S1: Original (priority 10)
	variations = append(variations, QueryVariation{Query: query, Strategy: "original", Priority: 10, Description: "Original query"})
	strategiesUsed = append(strategiesUsed, "original")

	// S2: Synonym
	if sv := s2SynonymVariations(cleanQuery); len(sv) > 0 {
		variations = append(variations, sv...)
		strategiesUsed = append(strategiesUsed, "synonym")
	}

	// S3: MeSH
	if includeMeSH {
		if mv := s3MeSHVariations(cleanQuery); len(mv) > 0 {
			variations = append(variations, mv...)
			strategiesUsed = append(strategiesUsed, "mesh")
		}
	}

	// S4: Abbreviations
	if includeAbbrev {
		if av := s4AbbreviationVariations(cleanQuery); len(av) > 0 {
			variations = append(variations, av...)
			strategiesUsed = append(strategiesUsed, "abbreviation")
		}
	}

	// S5: Boolean OR
	if bv := s5BooleanVariations(cleanQuery); len(bv) > 0 {
		variations = append(variations, bv...)
		strategiesUsed = append(strategiesUsed, "boolean")
	}

	// S6: Keyword-only
	if kv := s6KeywordOnly(cleanQuery); kv != nil {
		variations = append(variations, *kv)
		strategiesUsed = append(strategiesUsed, "keyword_only")
	}

	// S7: Exact phrase
	if pv := s7ExactPhrase(query, len(words)); pv != nil {
		variations = append(variations, *pv)
		strategiesUsed = append(strategiesUsed, "phrase")
	}

	// S8: Broader terms
	if bv := s8BroaderVariations(cleanQuery); len(bv) > 0 {
		variations = append(variations, bv...)
		strategiesUsed = append(strategiesUsed, "broader")
	}

	// S9: Narrower terms
	if nv := s9NarrowerVariations(cleanQuery); len(nv) > 0 {
		variations = append(variations, nv...)
		strategiesUsed = append(strategiesUsed, "narrower")
	}

	// S10: Conceptual
	if cv := s10ConceptualVariations(cleanQuery); len(cv) > 0 {
		variations = append(variations, cv...)
		strategiesUsed = append(strategiesUsed, "conceptual")
	}

	// S11: Temporal
	if includeTemporal {
		tv := s11TemporalVariations(query)
		variations = append(variations, tv...)
		strategiesUsed = append(strategiesUsed, "temporal")
	}

	// S12: Permutations (2–4 words only)
	if len(words) >= 2 && len(words) <= 4 {
		if pv := s12PermutationVariations(words); len(pv) > 0 {
			variations = append(variations, pv...)
			strategiesUsed = append(strategiesUsed, "permutation")
		}
	}

	// S13: API-specific
	if len(targetAPIs) > 0 {
		av := s13APIVariations(query, targetAPIs)
		if len(av) > 0 {
			variations = append(variations, av...)
			strategiesUsed = append(strategiesUsed, "api_optimized")
		}
	}

	// S14: N-grams
	if ngv := s14NgramVariations(words); len(ngv) > 0 {
		variations = append(variations, ngv...)
		strategiesUsed = append(strategiesUsed, "ngram")
	}

	// Dedup, sort by descending priority, limit.
	variations = deduplicateQueryVariations(variations)
	sort.Slice(variations, func(i, j int) bool {
		return variations[i].Priority > variations[j].Priority
	})
	if len(variations) > maxVariations {
		variations = variations[:maxVariations]
	}

	finalRes := AggressiveExpansionResponse{
		Original:   query,
		Variations: variations,
		Metadata: expansionMetadata{
			TotalVariations:   len(variations),
			Strategies:        strategiesUsed,
			EstimatedCoverage: calculateCoverageEstimate(len(variations), len(strategiesUsed)),
			PrimaryKeywords:   extractPrimaryKeywords(cleanQuery),
		},
		LatencyMs: time.Since(start).Milliseconds(),
	}
	setAggressiveCache(rdb, query, maxVariations, &finalRes)
	return finalRes
}

// ==========================================
// HANDLERS
// ==========================================

// handleAggressiveExpansion handles POST /v2/expand/aggressive.
//
// Applies all 14 dictionary-based expansion strategies and returns
// deduplicated, priority-sorted query variations. Results are cached
// in Redis for 1 hour keyed by SHA-256(query+maxVariations).

// handleSPLADEExpansion handles POST /v2/expand/splade.
//
// Thin wrapper around the existing expandQuery function in query_expansion.go,
// returning per-term weights and the expanded query string in a typed response.
