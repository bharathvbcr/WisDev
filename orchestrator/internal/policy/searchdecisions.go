package policy

// Search-decision policy: Go-owned port of the browser-side "AI decision engine"
// that decided quality mode, providers, filters, agentic mode, scope, and the
// multi-tab search strategy in Quick Mode.
//
// CANONICAL OWNER: Go. Ported value-for-value from:
//   - frontend/services/aiDecisionEngine.ts        (decision functions)
//   - frontend/services/domainDetectionService.ts  (DOMAIN_KEYWORDS + keyword scoring)
//   - frontend/services/copilotComplexityDetector.ts (complexity indicators)
//   - frontend/lib/apiProviders.ts                 (FIELD_PROVIDER_PRESETS)
//
// Migration: "Thin the frontend, consolidate orchestration in Go" — Phase 2
// (search coordination / provider selection). The frontend must POST
// /api/search/decisions instead of computing these decisions locally; on
// transport failure it falls back to server defaults by omitting explicit
// decisions from the search request (policy still never runs in the browser).
//
// Parity note: keyword tables and heuristics are intentionally ported
// bug-for-bug (e.g. the case-insensitive boolean-operator regex also matches a
// lowercase "and" inside a sentence) so behavior does not shift mid-migration.
// Behavior changes belong in a separate, deliberate change with its own tests.

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// nowFunc is indirected for deterministic tests of date-relative filters.
var nowFunc = time.Now

// =============================================================================
// Types (JSON shapes mirror frontend/services/aiDecisionEngine.ts)
// =============================================================================

// SearchDecisionPreferences carries the user context that influences decisions.
type SearchDecisionPreferences struct {
	PreferredSources []string `json:"preferredSources,omitempty"`
	ResearchField    string   `json:"researchField,omitempty"`
	// ResearchMode, when set, prefers that mode's provider set over domain defaults
	// (preferred sources still win). Tier gates access via ModeProviders.
	ResearchMode string `json:"researchMode,omitempty"`
	// SubscriptionTier gates research-mode provider access (default free).
	SubscriptionTier string `json:"subscriptionTier,omitempty"`
}

// SearchDecisionFilters mirrors Partial<AdvancedFilters> as populated by the
// browser engine.
type SearchDecisionFilters struct {
	DateFrom         string   `json:"dateFrom,omitempty"`
	DateTo           string   `json:"dateTo,omitempty"`
	PublicationType  string   `json:"publicationType,omitempty"`
	OpenAccessOnly   bool     `json:"openAccessOnly,omitempty"`
	FieldOfStudy     string   `json:"fieldOfStudy,omitempty"`
	SemanticKeywords []string `json:"semanticKeywords,omitempty"`
}

// SearchTabFilters mirrors the per-tab filters in MultiTabSearchStrategy.
type SearchTabFilters struct {
	Databases   []string `json:"databases,omitempty"`
	TrialStatus []string `json:"trialStatus,omitempty"`
}

// SearchTabPlan is one auto-created search tab.
type SearchTabPlan struct {
	Type         string            `json:"type"`
	Query        string            `json:"query,omitempty"`
	BooleanQuery string            `json:"booleanQuery,omitempty"`
	Filters      *SearchTabFilters `json:"filters,omitempty"`
	Priority     int               `json:"priority"`
}

// MultiTabStrategy mirrors MultiTabSearchStrategy.
type MultiTabStrategy struct {
	Tabs      []SearchTabPlan `json:"tabs"`
	Reasoning string          `json:"reasoning"`
}

// SearchDecisions mirrors AIDecisions.
type SearchDecisions struct {
	QualityMode      QualityMode           `json:"qualityMode"`
	Providers        []string              `json:"providers"`
	Filters          SearchDecisionFilters `json:"filters"`
	UseAgentic       bool                  `json:"useAgentic"`
	Scope            string                `json:"scope"`     // focused | comprehensive | exhaustive
	Timeframe        string                `json:"timeframe"` // alltime | 5years | 1year
	Reasoning        string                `json:"reasoning"`
	MultiTabStrategy *MultiTabStrategy     `json:"multiTabStrategy,omitempty"`
}

// DomainDetection mirrors DomainDetectionResult (keyword path only; the
// LLM-assisted path runs server-side elsewhere and is not part of decisions).
type DomainDetection struct {
	PrimaryDomain    string
	SecondaryDomains []string
	Confidence       float64
	Keywords         []string
}

// ComplexityAnalysis mirrors the browser ComplexityAnalysis.
type ComplexityAnalysis struct {
	IsComplex      bool
	ComplexityType string // simple | comparative | multi-step | synthesis | analysis
	Confidence     float64
	Indicators     []string
}

// =============================================================================
// Domain keyword tables (ported verbatim from domainDetectionService.ts)
// =============================================================================

var domainKeywords = map[string][]string{
	"medicine": {
		"clinical", "patient", "treatment", "therapy", "disease", "diagnosis",
		"medical", "health", "hospital", "drug", "pharmaceutical", "cancer",
		"surgery", "symptom", "pathology", "epidemiology", "vaccine", "infection",
		"disorder", "syndrome", "chronic", "acute", "prognosis", "mortality",
		"morbidity", "trial", "placebo", "dosage", "efficacy", "safety",
		"cardiac", "pulmonary", "neurological", "oncology", "pediatric", "geriatric",
		"diabetes", "hypertension", "obesity", "covid", "virus", "bacteria",
		"sleep", "insomnia", "circadian", "fatigue",
	},
	"cs": {
		"algorithm", "machine learning", "deep learning", "neural network",
		"artificial intelligence", "ai", "ml", "nlp", "computer vision",
		"software", "programming", "database", "cloud", "distributed",
		"transformer", "bert", "gpt", "llm", "language model", "embedding",
		"classification", "clustering", "regression", "optimization",
		"data structure", "complexity", "parallel", "concurrent", "blockchain",
		"cybersecurity", "encryption", "network", "protocol", "api",
		"reinforcement learning", "generative", "diffusion", "attention",
	},
	"social": {
		"psychology", "sociology", "behavior", "cognitive", "social",
		"economic", "political", "policy", "society", "culture", "demographic",
		"survey", "interview", "ethnography", "qualitative", "quantitative",
		"education", "learning", "development", "mental health", "wellbeing",
		"gender", "inequality", "migration", "urbanization", "poverty",
		"governance", "democracy", "conflict", "cooperation", "institution",
	},
	"climate": {
		"climate", "environment", "carbon", "emission", "greenhouse",
		"temperature", "warming", "sea level", "ice", "arctic", "antarctic",
		"biodiversity", "ecosystem", "species", "habitat", "conservation",
		"renewable", "energy", "solar", "wind", "sustainability", "pollution",
		"deforestation", "ocean", "atmosphere", "weather", "drought", "flood",
	},
	"neuro": {
		"brain", "neuron", "neural", "cortex", "hippocampus", "amygdala",
		"synapse", "neurotransmitter", "dopamine", "serotonin", "cognition",
		"cognitive", "memory", "attention", "perception", "consciousness", "sleep",
		"eeg", "fmri", "neuroimaging", "electrophysiology", "optogenetics",
		"alzheimer", "parkinson", "dementia", "stroke", "epilepsy",
	},
	"physics": {
		"quantum", "particle", "photon", "electron", "proton", "neutron",
		"energy", "mass", "force", "field", "wave", "frequency", "amplitude",
		"relativity", "spacetime", "gravity", "electromagnetic", "nuclear",
		"thermodynamics", "entropy", "superconductor", "semiconductor",
		"laser", "optics", "plasma", "fusion", "fission", "accelerator",
		"materials", "nanotechnology", "condensed matter", "string theory",
	},
	"biology": {
		"gene", "protein", "dna", "rna", "cell", "molecular", "genetic",
		"genome", "transcription", "translation", "mutation", "expression",
		"enzyme", "metabolism", "pathway", "signaling", "receptor",
		"crispr", "sequencing", "bioinformatics", "proteomics", "genomics",
		"evolution", "phylogenetic", "species", "organism", "microbiome",
		"immunology", "antibody", "antigen", "cytokine", "stem cell",
	},
	"humanities": {
		"philosophy", "ethics", "moral", "aesthetic", "epistemology",
		"history", "historical", "ancient", "medieval", "modern", "century",
		"literature", "literary", "narrative", "poetry", "fiction", "author",
		"art", "artistic", "music", "visual", "performance", "culture",
		"religion", "theology", "spirituality", "ritual", "belief",
		"language", "linguistic", "semantics", "discourse", "rhetoric",
	},
	"mathematics": {
		"theorem", "proof", "conjecture", "lemma", "corollary", "axiom",
		"algebra", "topology", "geometry", "calculus", "analysis",
		"number theory", "combinatorics", "graph theory", "probability",
		"statistics", "stochastic", "differential equation", "linear algebra",
		"optimization", "manifold", "group theory", "ring", "field theory",
	},
	"engineering": {
		"mechanical", "civil", "electrical", "aerospace", "robotics",
		"structural", "industrial", "manufacturing", "materials science",
		"control system", "signal processing", "power system", "antenna",
		"circuit", "thermodynamics", "fluid dynamics", "finite element",
		"cad", "embedded system", "automation", "sensor", "actuator",
	},
	"chemistry": {
		"chemical", "molecule", "compound", "reaction", "catalyst",
		"synthesis", "polymer", "organic", "inorganic", "analytical",
		"electrochemistry", "spectroscopy", "crystallography", "solvent",
		"bond", "ion", "acid", "base", "oxidation", "reduction",
		"nanoparticle", "biomolecular", "pharmaceutical chemistry",
	},
	"economics": {
		"market", "trade", "fiscal", "monetary", "inflation", "gdp",
		"macroeconomics", "microeconomics", "econometrics", "supply",
		"demand", "equilibrium", "game theory", "auction", "welfare",
		"labor economics", "behavioral economics", "financial economics",
		"public economics", "development economics", "international trade",
	},
	"law": {
		"legal", "statute", "regulation", "jurisdiction", "court",
		"constitutional", "criminal law", "civil law", "contract",
		"tort", "intellectual property", "patent", "copyright",
		"human rights", "international law", "legislation", "judicial",
		"compliance", "liability", "arbitration", "litigation",
	},
	"education": {
		"pedagogy", "curriculum", "instruction", "assessment", "classroom",
		"student", "teacher", "learning outcomes", "educational technology",
		"higher education", "k-12", "stem education", "literacy",
		"special education", "distance learning", "mooc", "elearning",
		"formative assessment", "summative assessment", "scaffolding",
	},
	"environmental_science": {
		"pollution", "waste management", "water quality", "air quality",
		"soil contamination", "remediation", "environmental impact",
		"sustainability", "circular economy", "life cycle assessment",
		"ecological footprint", "carbon sequestration", "wetland",
		"groundwater", "ozone", "particulate matter", "toxicology",
		"environmental policy", "green chemistry", "bioremediation",
	},
	"materials_science": {
		"alloy", "ceramic", "composite", "biomaterial", "thin film",
		"crystal structure", "microstructure", "tensile strength",
		"corrosion", "fatigue", "fracture", "hardness", "elasticity",
		"semiconductor", "photovoltaic", "battery material", "superconductor",
		"metamaterial", "smart material", "graphene", "carbon nanotube",
	},
	"agriculture": {
		"crop", "soil", "irrigation", "fertilizer", "pesticide",
		"livestock", "aquaculture", "agroecology", "food security",
		"precision agriculture", "plant breeding", "seed", "harvest",
		"drought resistance", "yield", "organic farming", "sustainable agriculture",
		"agronomy", "horticulture", "post-harvest", "food processing",
	},
	"linguistics": {
		"phonology", "morphology", "syntax", "pragmatics", "sociolinguistics",
		"psycholinguistics", "computational linguistics", "corpus",
		"phonetics", "dialect", "bilingual", "multilingual", "translation",
		"language acquisition", "second language", "discourse analysis",
		"lexicon", "grammar", "typology", "endangered language",
	},
}

// domainProviders mirrors DOMAIN_PROVIDERS in aiDecisionEngine.ts.
var domainProviders = map[string][]string{
	"medicine":   {"pubmed", "semanticscholar", "europepmc", "biorxiv"},
	"cs":         {"arxiv", "semanticscholar", "dblp", "paperswithcode"},
	"ml":         {"arxiv", "semanticscholar", "paperswithcode", "openalex"},
	"biomedical": {"pubmed", "semanticscholar", "europepmc", "biorxiv"},
	"physics":    {"arxiv", "nasaads", "semanticscholar"},
	"math":       {"arxiv", "semanticscholar", "repec"},
	"economics":  {"repec", "ssrn", "semanticscholar", "openalex"},
	"philosophy": {"philpapers", "semanticscholar", "openalex"},
	"engineering": {"ieee", "semanticscholar", "openalex", "arxiv"},
	"astronomy":  {"nasaads", "arxiv", "semanticscholar"},
	"general":    {"semanticscholar", "openalex", "core"},
}

// fieldProviderPresets mirrors FIELD_PROVIDER_PRESETS in apiProviders.ts.
var fieldProviderPresets = map[string][]string{
	"computer-science":        {"semanticscholar", "openalex", "dblp", "arxiv", "paperswithcode", "ieee"},
	"machine-learning":        {"semanticscholar", "openalex", "arxiv", "paperswithcode", "dblp"},
	"artificial-intelligence": {"semanticscholar", "openalex", "arxiv", "paperswithcode", "dblp"},
	"medicine":                {"semanticscholar", "openalex", "pubmed", "europepmc", "biorxiv", "clinicalTrials", "scopus"},
	"biology":                 {"semanticscholar", "openalex", "europepmc", "biorxiv", "pubmed"},
	"neuroscience":            {"semanticscholar", "openalex", "pubmed", "europepmc", "biorxiv"},
	"physics":                 {"semanticscholar", "openalex", "arxiv", "crossref", "nasaads"},
	"astronomy":               {"semanticscholar", "openalex", "nasaads", "arxiv"},
	"mathematics":             {"semanticscholar", "openalex", "arxiv", "crossref"},
	"chemistry":               {"semanticscholar", "openalex", "crossref", "europepmc", "scopus"},
	"engineering":             {"semanticscholar", "openalex", "crossref", "arxiv", "base", "ieee", "patentsview"},
	"social-sciences":         {"semanticscholar", "openalex", "crossref", "core", "base", "ssrn", "repec"},
	"economics":               {"semanticscholar", "openalex", "crossref", "ssrn", "repec", "dimensions"},
	"finance":                 {"semanticscholar", "openalex", "crossref", "ssrn", "repec"},
	"law":                     {"semanticscholar", "openalex", "crossref", "ssrn", "base"},
	"philosophy":              {"semanticscholar", "openalex", "philpapers", "crossref"},
	"clinical":                {"semanticscholar", "openalex", "pubmed", "europepmc", "clinicalTrials"},
	"patents":                 {"patentsview", "dimensions"},
	"open-access":             {"doaj", "core", "base", "arxiv", "biorxiv"},
	"general":                 {"semanticscholar", "openalex", "crossref", "core", "base", "doaj"},
}

// FieldRecommendations mirrors getFieldRecommendations: unknown fields resolve
// to the "general" preset. Field names are normalized to preset keys.
func FieldRecommendations(field string) []string {
	normalized := strings.ToLower(strings.TrimSpace(field))
	normalized = strings.ReplaceAll(normalized, " ", "-")
	if preset, ok := fieldProviderPresets[normalized]; ok {
		return append([]string(nil), preset...)
	}
	return append([]string(nil), fieldProviderPresets["general"]...)
}

// AllFieldProviderPresets returns a copy of the canonical field→provider map
// for GET /api/config/providers (frontend display presets must not drift).
func AllFieldProviderPresets() map[string][]string {
	out := make(map[string][]string, len(fieldProviderPresets))
	for field, providers := range fieldProviderPresets {
		out[field] = append([]string(nil), providers...)
	}
	return out
}

// providerDisplayNames feeds human-readable reasoning strings (subset of
// API_PROVIDERS[].name in apiProviders.ts; IDs fall through unchanged).
var providerDisplayNames = map[string]string{
	"semanticscholar": "Semantic Scholar",
	"openalex":        "OpenAlex",
	"pubmed":          "PubMed",
	"europepmc":       "Europe PMC",
	"biorxiv":         "bioRxiv",
	"arxiv":           "arXiv",
	"dblp":            "DBLP",
	"paperswithcode":  "Papers with Code",
	"nasaads":         "NASA ADS",
	"repec":           "RePEc",
	"ssrn":            "SSRN",
	"philpapers":      "PhilPapers",
	"ieee":            "IEEE Xplore",
	"core":            "CORE",
	"crossref":        "Crossref",
	"clinicalTrials":  "ClinicalTrials.gov",
	"scopus":          "Scopus",
	"base":            "BASE",
	"patentsview":     "PatentsView",
	"dimensions":      "Dimensions",
	"doaj":            "DOAJ",
}

// =============================================================================
// Complexity analysis (ported from copilotComplexityDetector.ts)
// =============================================================================

var (
	comparativeIndicators = []string{
		"compare", "versus", "vs", "vs.", "difference between", "better than",
		"more effective", "advantages", "disadvantages", "pros and cons",
		"trade-off", "tradeoff", "contrast", "similarities", "differences",
	}
	multiStepIndicators = []string{
		"how to", "step by step", "first...then", "after which", "subsequently",
		"followed by", "and then", "process of", "workflow", "pipeline",
	}
	synthesisIndicators = []string{
		"summarize all", "overall trend", "across multiple", "state of the art",
		"latest research", "comprehensive overview", "review of", "what are the",
		"all the", "entire", "everything about",
	}
	analysisIndicators = []string{
		"analyze", "evaluate", "implications", "impact of", "effect of",
		"relationship between", "correlation", "causation", "why does",
		"how does", "explain why", "critique", "assess",
	}
	simpleIndicators = []string{
		"what is", "who wrote", "who authored", "when was", "what year",
		"how much", "how many", "define", "explain",
	}
	complexityTechnicalTerms = []string{
		"methodology", "empirical", "hypothesis", "statistical", "meta-analysis",
		"systematic review", "algorithm", "implementation", "architecture", "framework",
	}
)

// AnalyzeQueryComplexity ports analyzeQueryComplexity value-for-value.
func AnalyzeQueryComplexity(query string) ComplexityAnalysis {
	lowerQuery := strings.ToLower(strings.TrimSpace(query))
	var indicators []string
	complexityScore := 0.0
	complexityType := "simple"

	appendMatches := func(list []string) []string {
		var matches []string
		for _, indicator := range list {
			if strings.Contains(lowerQuery, indicator) {
				matches = append(matches, indicator)
			}
		}
		return matches
	}

	if m := appendMatches(comparativeIndicators); len(m) > 0 {
		complexityScore += 3
		complexityType = "comparative"
		indicators = append(indicators, m...)
	}
	if m := appendMatches(multiStepIndicators); len(m) > 0 {
		complexityScore += 2
		if complexityType == "simple" {
			complexityType = "multi-step"
		}
		indicators = append(indicators, m...)
	}
	if m := appendMatches(synthesisIndicators); len(m) > 0 {
		complexityScore += 2
		if complexityType == "simple" {
			complexityType = "synthesis"
		}
		indicators = append(indicators, m...)
	}
	if m := appendMatches(analysisIndicators); len(m) > 0 {
		complexityScore += 1.5
		if complexityType == "simple" {
			complexityType = "analysis"
		}
		indicators = append(indicators, m...)
	}
	var simpleMatches []string
	for _, indicator := range simpleIndicators {
		if strings.HasPrefix(lowerQuery, indicator) {
			simpleMatches = append(simpleMatches, indicator)
		}
	}
	if len(simpleMatches) > 0 && complexityScore == 0 {
		complexityType = "simple"
		indicators = append(indicators, simpleMatches...)
	}

	wordCount := len(strings.Fields(query))
	questionCount := strings.Count(query, "?")
	if questionCount > 1 {
		complexityScore++
	}
	if wordCount > 15 && complexityScore == 0 {
		complexityScore += 0.5
	}
	techCount := 0.0
	for _, term := range complexityTechnicalTerms {
		if strings.Contains(lowerQuery, term) {
			techCount++
		}
	}
	if techCount > 0 {
		complexityScore += minFloat(techCount*0.5, 1)
	}

	confidence := minFloat(complexityScore/5, 1)
	return ComplexityAnalysis{
		IsComplex:      complexityScore >= 1.5,
		ComplexityType: complexityType,
		Confidence:     confidence,
		Indicators:     dedupeStrings(indicators),
	}
}

// =============================================================================
// Domain detection (keyword path, ported from domainDetectionService.ts)
// =============================================================================

var nonAlnumSplit = regexp.MustCompile(`[^a-z0-9]+`)

var stemReplacements = []*regexp.Regexp{
	regexp.MustCompile(`ions?$`),
	regexp.MustCompile(`ities?$`),
	regexp.MustCompile(`ically?$`),
	regexp.MustCompile(`ive?$`),
	regexp.MustCompile(`ing?$`),
	regexp.MustCompile(`ed?$`),
	regexp.MustCompile(`ly?$`),
	regexp.MustCompile(`ness?$`),
}

func stemWord(word string) string {
	for _, re := range stemReplacements {
		word = re.ReplaceAllString(word, "")
	}
	return word
}

func matchesKeyword(word, keyword string) bool {
	if word == keyword {
		return true
	}
	if strings.Contains(keyword, " ") {
		return strings.Contains(word, keyword) || strings.Contains(keyword, word)
	}
	wordStem := stemWord(word)
	keywordStem := stemWord(keyword)
	return len(wordStem) >= 4 && len(keywordStem) >= 4 &&
		(strings.HasPrefix(wordStem, keywordStem) || strings.HasPrefix(keywordStem, wordStem))
}

// DetectDomainFromKeywords ports detectDomainFromKeywords (keyword scoring only).
func DetectDomainFromKeywords(query string) DomainDetection {
	normalizedQuery := strings.ToLower(query)
	queryWords := splitWords(normalizedQuery)

	domainScores := map[string]int{}
	matchedKeywords := map[string][]string{}

	for domain, keywords := range domainKeywords {
		for _, keyword := range keywords {
			if strings.Contains(keyword, " ") && strings.Contains(normalizedQuery, keyword) {
				domainScores[domain] += 5
				matchedKeywords[domain] = append(matchedKeywords[domain], keyword)
				continue
			}
			for _, word := range queryWords {
				if word == keyword {
					domainScores[domain] += 2
					matchedKeywords[domain] = append(matchedKeywords[domain], keyword)
					break
				} else if matchesKeyword(word, keyword) {
					domainScores[domain]++
					matchedKeywords[domain] = append(matchedKeywords[domain], keyword)
					break
				}
			}
		}
	}

	type scored struct {
		domain string
		score  int
	}
	var sortedDomains []scored
	for domain, score := range domainScores {
		if score > 0 {
			sortedDomains = append(sortedDomains, scored{domain, score})
		}
	}
	sort.SliceStable(sortedDomains, func(i, j int) bool {
		if sortedDomains[i].score != sortedDomains[j].score {
			return sortedDomains[i].score > sortedDomains[j].score
		}
		return sortedDomains[i].domain < sortedDomains[j].domain // deterministic tie-break
	})

	if len(sortedDomains) == 0 {
		kw := queryWords
		if len(kw) > 5 {
			kw = kw[:5]
		}
		return DomainDetection{
			PrimaryDomain: "general",
			Confidence:    0.5,
			Keywords:      append([]string(nil), kw...),
		}
	}

	primary := sortedDomains[0]
	totalScore := 0
	for _, s := range sortedDomains {
		totalScore += s.score
	}
	confidence := minFloat(0.95, 0.5+(float64(primary.score)/float64(totalScore+5))*0.5)

	var secondary []string
	for _, s := range sortedDomains[1:] {
		if float64(s.score) >= float64(primary.score)*0.3 {
			secondary = append(secondary, s.domain)
		}
	}

	return DomainDetection{
		PrimaryDomain:    primary.domain,
		SecondaryDomains: secondary,
		Confidence:       confidence,
		Keywords:         matchedKeywords[primary.domain],
	}
}

// =============================================================================
// Decision functions (ported from aiDecisionEngine.ts)
// =============================================================================

// DecideQualityModeForQuery ports decideQualityMode.
func DecideQualityModeForQuery(query string) QualityMode {
	wordCount := len(strings.Fields(query))
	complexity := AnalyzeQueryComplexity(query)

	if complexity.ComplexityType == "simple" || wordCount < 10 {
		return QualityFast
	}
	if complexity.ComplexityType == "comparative" ||
		complexity.ComplexityType == "synthesis" ||
		complexity.ComplexityType == "multi-step" ||
		wordCount > 20 {
		return QualityQuality
	}
	return QualityBalanced
}

// DecideProvidersForQuery ports decideProviders.
func DecideProvidersForQuery(query string, prefs SearchDecisionPreferences) []string {
	if len(prefs.PreferredSources) > 0 {
		var preferred []string
		for _, id := range prefs.PreferredSources {
			if isKnownProviderID(id) {
				preferred = append(preferred, id)
			}
		}
		if len(preferred) > 0 {
			return preferred
		}
	}

	if modeRaw := strings.TrimSpace(prefs.ResearchMode); modeRaw != "" {
		mode, _ := NormalizeResearchMode(modeRaw)
		tier, _ := NormalizeSubscriptionTier(prefs.SubscriptionTier)
		if providers := ModeProviders(mode, tier); len(providers) > 0 {
			out := append([]string(nil), providers...)
			if len(out) > 4 {
				out = out[:4]
			}
			return out
		}
	}

	detection := DetectDomainFromKeywords(query)
	providers, ok := domainProviders[detection.PrimaryDomain]
	if !ok {
		providers = domainProviders["general"]
	}
	providers = append([]string(nil), providers...)

	if strings.TrimSpace(prefs.ResearchField) != "" {
		if fieldProviders := FieldRecommendations(prefs.ResearchField); len(fieldProviders) > 0 {
			providers = fieldProviders
		}
	}

	if len(providers) < 2 {
		general := domainProviders["general"]
		providers = append(providers, general[:2-len(providers)]...)
	}
	if len(providers) > 4 {
		providers = providers[:4]
	}
	return providers
}

// yearPattern mirrors /\b(19|20)\d{2}\b/g.
var yearPattern = regexp.MustCompile(`\b(19|20)\d{2}\b`)

// ExtractFiltersFromQuery ports extractFilters.
func ExtractFiltersFromQuery(query string) SearchDecisionFilters {
	var filters SearchDecisionFilters
	lowerQuery := strings.ToLower(query)

	years := yearPattern.FindAllString(query, -1)
	if len(years) > 0 {
		minYear, maxYear := 9999, 0
		for _, y := range years {
			n, err := strconv.Atoi(y)
			if err != nil {
				continue
			}
			if n < minYear {
				minYear = n
			}
			if n > maxYear {
				maxYear = n
			}
		}
		if maxYear-minYear <= 5 {
			filters.DateFrom = strconv.Itoa(minYear)
			filters.DateTo = strconv.Itoa(maxYear)
		} else {
			filters.DateFrom = strconv.Itoa(maxYear)
		}
	}

	if strings.Contains(lowerQuery, "recent") || strings.Contains(lowerQuery, "latest") || strings.Contains(lowerQuery, "new") {
		filters.DateFrom = strconv.Itoa(nowFunc().Year() - 5)
	}

	switch {
	case strings.Contains(lowerQuery, "review") || strings.Contains(lowerQuery, "systematic review"):
		filters.PublicationType = "Review"
	case strings.Contains(lowerQuery, "clinical trial") || strings.Contains(lowerQuery, "trial"):
		filters.PublicationType = "Clinical Trial"
	case strings.Contains(lowerQuery, "meta-analysis"):
		filters.PublicationType = "Meta-Analysis"
	}

	if strings.Contains(lowerQuery, "open access") || strings.Contains(lowerQuery, "open-access") {
		filters.OpenAccessOnly = true
	}

	detection := DetectDomainFromKeywords(query)
	if detection.PrimaryDomain != "general" {
		fieldMap := map[string]string{
			"medicine":   "Medicine",
			"cs":         "Computer Science",
			"ml":         "Machine Learning",
			"biomedical": "Biomedical",
			"physics":    "Physics",
			"math":       "Mathematics",
			"economics":  "Economics",
			"philosophy": "Philosophy",
			"engineering": "Engineering",
			"astronomy":  "Astronomy",
		}
		if mapped, ok := fieldMap[detection.PrimaryDomain]; ok {
			filters.FieldOfStudy = mapped
		} else {
			filters.FieldOfStudy = detection.PrimaryDomain
		}
	}

	if len(detection.Keywords) > 0 {
		unique := dedupeStrings(detection.Keywords)
		if len(unique) > 10 {
			unique = unique[:10]
		}
		filters.SemanticKeywords = unique
	}

	return filters
}

// DecideScopeForQuery ports decideScope.
func DecideScopeForQuery(query string) string {
	lowerQuery := strings.ToLower(query)
	for _, kw := range []string{"all", "everything", "comprehensive", "complete", "exhaustive"} {
		if strings.Contains(lowerQuery, kw) {
			return "exhaustive"
		}
	}
	for _, kw := range []string{"specific", "exact", "precise", "particular"} {
		if strings.Contains(lowerQuery, kw) {
			return "focused"
		}
	}
	return "comprehensive"
}

// DecideTimeframeForQuery owns Quick Mode session timeframe hints previously in
// frontend/services/quickModeService.ts deriveTimeframe.
func DecideTimeframeForQuery(query string) string {
	lowerQuery := strings.ToLower(strings.TrimSpace(query))
	for _, kw := range []string{"recent", "latest", "2024", "2025", "2026"} {
		if strings.Contains(lowerQuery, kw) {
			return "1year"
		}
	}
	for _, kw := range []string{"history", "historical", "origin", "foundational"} {
		if strings.Contains(lowerQuery, kw) {
			return "alltime"
		}
	}
	return "5years"
}

func buildReasoning(qualityMode QualityMode, providers []string, filters SearchDecisionFilters, useAgentic bool, scope, query string) string {
	var parts []string

	wordCount := len(strings.Fields(query))
	switch qualityMode {
	case QualityFast:
		parts = append(parts, fmt.Sprintf("Fast mode for quick results (%d words)", wordCount))
	case QualityQuality:
		parts = append(parts, "Quality mode for comprehensive analysis")
	default:
		parts = append(parts, "Balanced mode for optimal speed and quality")
	}

	if len(providers) > 0 {
		names := make([]string, 0, len(providers))
		for _, p := range providers {
			if name, ok := providerDisplayNames[p]; ok {
				names = append(names, name)
			} else {
				names = append(names, p)
			}
		}
		parts = append(parts, fmt.Sprintf("%d sources: %s", len(providers), strings.Join(names, ", ")))
	}

	var filterParts []string
	if filters.DateFrom != "" {
		filterParts = append(filterParts, "from "+filters.DateFrom)
	}
	if filters.PublicationType != "" {
		filterParts = append(filterParts, strings.ToLower(filters.PublicationType))
	}
	if filters.OpenAccessOnly {
		filterParts = append(filterParts, "open access only")
	}
	if len(filterParts) > 0 {
		parts = append(parts, "Filters: "+strings.Join(filterParts, ", "))
	}

	if useAgentic {
		parts = append(parts, "Using agentic RAG for complex query")
	}
	switch scope {
	case "exhaustive":
		parts = append(parts, "Exhaustive search scope")
	case "focused":
		parts = append(parts, "Focused search scope")
	}

	return strings.Join(parts, ". ")
}

// booleanOperatorPattern mirrors /(AND|OR|NOT|\(|\))/i — including its known
// looseness (matches lowercase "and"/"or"/"not" anywhere). See parity note.
var booleanOperatorPattern = regexp.MustCompile(`(?i)(AND|OR|NOT|\(|\))`)
var quotedPhrasePattern = regexp.MustCompile(`"[^"]+"`)
var technicalTermPattern = regexp.MustCompile(`(?i)\b(algorithm|method|technique|approach|framework|model|system)\b`)

var multiTabMedicalKeywords = []string{
	"clinical trial", "randomized", "placebo", "intervention", "treatment",
	"patient", "disease", "diagnosis", "therapy", "medication", "drug",
	"covid", "cancer", "diabetes", "hypertension", "depression",
}

// DecideMultiTabStrategyForQuery ports decideMultiTabStrategy.
func DecideMultiTabStrategyForQuery(query string, prefs SearchDecisionPreferences) MultiTabStrategy {
	lowerQuery := strings.ToLower(query)
	var tabs []SearchTabPlan
	var reasoningParts []string

	tabs = append(tabs, SearchTabPlan{Type: "question", Query: query, Priority: 10})
	reasoningParts = append(reasoningParts, "Question search for semantic discovery")

	hasBooleanOperators := booleanOperatorPattern.MatchString(query)
	hasQuotes := quotedPhrasePattern.MatchString(query)
	technicalTerms := technicalTermPattern.MatchString(query)

	if hasBooleanOperators || (hasQuotes && technicalTerms) {
		tab := SearchTabPlan{
			Type:     "keyword",
			Filters:  &SearchTabFilters{Databases: DecideProvidersForQuery(query, prefs)},
			Priority: 8,
		}
		if hasBooleanOperators {
			tab.BooleanQuery = query
		} else {
			tab.Query = query
		}
		tabs = append(tabs, tab)
		reasoningParts = append(reasoningParts, "Boolean/keyword search for precise terminology")
	}

	isMedicalQuery := false
	for _, kw := range multiTabMedicalKeywords {
		if strings.Contains(lowerQuery, kw) {
			isMedicalQuery = true
			break
		}
	}
	detection := DetectDomainFromKeywords(query)
	isMedicalDomain := detection.PrimaryDomain == "medicine" || detection.PrimaryDomain == "biomedical" ||
		containsString(detection.SecondaryDomains, "medicine") || containsString(detection.SecondaryDomains, "biomedical")

	if isMedicalQuery || isMedicalDomain {
		tabs = append(tabs, SearchTabPlan{
			Type:  "clinical-trials",
			Query: query,
			Filters: &SearchTabFilters{
				TrialStatus: []string{"Recruiting", "Active, not recruiting", "Completed"},
			},
			Priority: 7,
		})
		reasoningParts = append(reasoningParts, "Clinical trials registry search for medical research")
	}

	plural := ""
	if len(tabs) != 1 {
		plural = "s"
	}
	return MultiTabStrategy{
		Tabs:      tabs,
		Reasoning: fmt.Sprintf("Auto-created %d search tab%s: %s", len(tabs), plural, strings.Join(reasoningParts, "; ")),
	}
}

// DecideSearchParameters is the composite entry point mirroring makeAIDecisions,
// with the multi-tab strategy included so one round-trip serves Quick Mode.
func DecideSearchParameters(query string, prefs SearchDecisionPreferences) SearchDecisions {
	qualityMode := DecideQualityModeForQuery(query)
	providers := DecideProvidersForQuery(query, prefs)
	filters := ExtractFiltersFromQuery(query)
	useAgentic := AnalyzeQueryComplexity(query).IsComplex
	scope := DecideScopeForQuery(query)
	timeframe := DecideTimeframeForQuery(query)
	reasoning := buildReasoning(qualityMode, providers, filters, useAgentic, scope, query)
	multiTab := DecideMultiTabStrategyForQuery(query, prefs)

	return SearchDecisions{
		QualityMode:      qualityMode,
		Providers:        providers,
		Filters:          filters,
		UseAgentic:       useAgentic,
		Scope:            scope,
		Timeframe:        timeframe,
		Reasoning:        reasoning,
		MultiTabStrategy: &multiTab,
	}
}

// =============================================================================
// Relevance thresholds (ported from MODE_THRESHOLDS in relevanceService.ts)
// =============================================================================

// relevanceThresholds is the minimum relevance score by search mode.
// PRECISION note preserved from the TS source: raised from 30-50 to 45-55 to
// filter out tangentially-related papers.
var relevanceThresholds = map[string]int{
	"academic": 55,
	"web":      45,
	"hybrid":   50,
}

// RelevanceThreshold resolves the minimum relevance score for a search mode.
// Unknown/empty modes resolve to the hybrid threshold.
func RelevanceThreshold(searchMode string) int {
	if v, ok := relevanceThresholds[strings.ToLower(strings.TrimSpace(searchMode))]; ok {
		return v
	}
	return relevanceThresholds["hybrid"]
}

// =============================================================================
// Helpers
// =============================================================================

func splitWords(normalizedQuery string) []string {
	var words []string
	for _, w := range nonAlnumSplit.Split(normalizedQuery, -1) {
		if w != "" {
			words = append(words, w)
		}
	}
	return words
}

func isKnownProviderID(id string) bool {
	if _, ok := providerDisplayNames[id]; ok {
		return true
	}
	for _, cfg := range apiProviders {
		if cfg.ID == id {
			return true
		}
	}
	return false
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	var out []string
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func containsString(list []string, target string) bool {
	for _, s := range list {
		if s == target {
			return true
		}
	}
	return false
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
