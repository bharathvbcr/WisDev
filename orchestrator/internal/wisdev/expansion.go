package wisdev

import (
	"strings"
)

// ==========================================
// Local Synonym-Based Query Expansion
// ==========================================

// SCIENTIFIC_SYNONYMS maps common academic terms to their synonyms and
// related phrases. The map is keyed by lowercase terms; lookup should
// always use lowercased input.
var SCIENTIFIC_SYNONYMS = map[string][]string{
	// Computer science & AI
	"machine learning":            {"ML", "deep learning", "neural network", "artificial intelligence"},
	"artificial intelligence":     {"AI", "machine learning", "ML", "neural network"},
	"deep learning":               {"neural network", "machine learning", "representation learning"},
	"natural language processing": {"NLP", "text mining", "computational linguistics"},
	"natural language":            {"NLP", "text mining", "language model", "LLM"},
	"computer vision":             {"image recognition", "object detection", "visual computing"},
	"reinforcement learning":      {"RL", "reward learning", "policy optimization"},
	"transformer":                 {"attention mechanism", "self-attention", "BERT", "GPT"},
	"large language model":        {"LLM", "foundation model", "generative AI", "GPT"},
	"neural network":              {"deep learning", "artificial neural network", "ANN"},

	// Medicine & clinical
	"clinical trial":      {"randomized controlled trial", "RCT", "clinical study"},
	"meta-analysis":       {"systematic review", "meta analysis", "evidence synthesis"},
	"covid":               {"SARS-CoV-2", "coronavirus", "COVID-19"},
	"cancer":              {"neoplasm", "malignancy", "tumor", "carcinoma", "oncology"},
	"breast cancer":       {"mammary carcinoma", "breast neoplasm", "breast tumor"},
	"lung cancer":         {"pulmonary carcinoma", "NSCLC", "SCLC", "lung neoplasm"},
	"diabetes":            {"diabetes mellitus", "DM", "T2DM", "type 2 diabetes", "hyperglycemia"},
	"heart":               {"cardiac", "cardiovascular", "coronary"},
	"heart attack":        {"myocardial infarction", "MI", "cardiac arrest", "coronary event"},
	"heart failure":       {"cardiac failure", "CHF", "HF", "congestive heart failure"},
	"high blood pressure": {"hypertension", "elevated BP", "HTN"},
	"stroke":              {"cerebrovascular accident", "CVA", "brain attack", "ischemic stroke"},
	"alzheimer":           {"alzheimer disease", "AD", "dementia", "neurodegenerative"},
	"parkinson":           {"parkinson disease", "PD", "parkinsonian"},
	"depression":          {"major depressive disorder", "MDD", "clinical depression"},
	"anxiety":             {"anxiety disorder", "GAD", "generalized anxiety"},
	"flu":                 {"influenza", "H1N1", "seasonal flu"},
	"infection":           {"infectious disease", "pathogen", "bacterial infection", "viral infection"},
	"obesity":             {"overweight", "BMI", "adiposity", "metabolic syndrome"},
	"brain":               {"neural", "cerebral", "neurological", "cognitive"},
	"gene":                {"genetic", "genomic", "genome", "DNA"},
	"drug":                {"pharmaceutical", "pharmacological", "medication", "therapeutic agent"},
	"surgery":             {"surgical", "operative", "intervention", "procedure"},
	"vaccine":             {"vaccination", "immunization", "inoculation"},
	"antibiotic":          {"antimicrobial", "antibacterial", "bactericidal"},
	"inflammation":        {"inflammatory", "immune response", "cytokine"},

	// Research methodology terms
	"effectiveness": {"efficacy", "therapeutic effect", "clinical outcome"},
	"side effects":  {"adverse effects", "adverse events", "AE", "toxicity"},
	"treatment":     {"therapy", "intervention", "therapeutic", "management"},
	"study":         {"trial", "research", "investigation", "analysis"},
	"patients":      {"subjects", "participants", "cohort", "population"},
	"randomized":    {"RCT", "randomized controlled trial", "randomised"},
	"observational": {"cohort study", "case-control", "cross-sectional"},

	// Biology & life sciences
	"protein":    {"proteomic", "enzyme", "polypeptide", "amino acid"},
	"cell":       {"cellular", "cytology", "cell biology"},
	"evolution":  {"evolutionary", "phylogenetic", "natural selection", "adaptation"},
	"ecology":    {"ecological", "ecosystem", "biodiversity", "habitat"},
	"microbiome": {"gut flora", "microbiota", "microbial community"},

	// Physics & engineering
	"quantum":          {"quantum mechanics", "quantum computing", "quantum physics"},
	"renewable energy": {"solar energy", "wind energy", "clean energy", "sustainable energy"},
	"climate change":   {"global warming", "greenhouse effect", "carbon emissions", "climate crisis"},
	"nanotechnology":   {"nanomaterial", "nanoparticle", "nanoscale"},

	// Social sciences & statistics
	"regression":     {"linear regression", "logistic regression", "statistical model"},
	"survey":         {"questionnaire", "cross-sectional study", "poll"},
	"cognitive":      {"cognition", "mental process", "perception", "attention"},
	"education":      {"pedagogy", "learning", "teaching", "instructional"},
	"economics":      {"economic", "macroeconomics", "microeconomics", "fiscal"},
	"psychology":     {"psychological", "behavioral", "mental health"},
	"sustainability": {"sustainable development", "environmental impact", "green"},
	"blockchain":     {"distributed ledger", "cryptocurrency", "smart contract", "DeFi"},
	"robotics":       {"robot", "autonomous system", "mechatronics", "actuator"},
}

// applyMultiSourceScoreBoost boosts the Score of papers that appear in 2 or
// more sources (i.e. SourceCount >= 2) by +0.2. Papers that already have a
// positive base score benefit from the boost; papers with score 0 receive 0.2.
// This is applied after deduplication so that SourceCount has been computed.
func applyMultiSourceScoreBoost(papers []Source) []Source {
	const boost = 0.2
	for i := range papers {
		if papers[i].SourceCount >= 2 {
			papers[i].Score += boost
		}
	}
	return papers
}

// expandQuery performs local synonym-based query expansion. It scans the
// lowercased query for known synonym keys and appends up to 2 synonyms per
// matched term. It also infers a broad intent category and extracts keywords.
func ExpandQuery(query string) EnhancedQuery {
	lower := strings.ToLower(query)

	var matchedSynonyms []string
	var expandedParts []string
	expandedParts = append(expandedParts, query) // start with original

	for term, synonyms := range SCIENTIFIC_SYNONYMS {
		if strings.Contains(lower, term) {
			// Take up to 2 synonyms per matched term.
			count := 2
			if len(synonyms) < count {
				count = len(synonyms)
			}
			for i := 0; i < count; i++ {
				expandedParts = append(expandedParts, synonyms[i])
				matchedSynonyms = append(matchedSynonyms, synonyms[i])
			}
		}
	}

	expanded := strings.Join(expandedParts, " OR ")

	intent := detectIntent(lower)
	keywords := extractKeywords(lower)

	return EnhancedQuery{
		Original: query,
		Expanded: expanded,
		Intent:   intent,
		Keywords: keywords,
		Synonyms: matchedSynonyms,
	}
}

// detectIntent classifies the query into a broad intent category based on
// keyword heuristics.
func detectIntent(lower string) string {
	medicalTerms := []string{
		"clinical", "patient", "disease", "treatment", "therapy", "medical",
		"drug", "surgery", "vaccine", "diagnosis", "symptom", "cancer",
		"diabetes", "heart", "covid", "antibiotic",
	}
	for _, t := range medicalTerms {
		if strings.Contains(lower, t) {
			return "medical"
		}
	}

	csTerms := []string{
		"algorithm", "machine learning", "neural", "deep learning", "NLP",
		"software", "computer", "programming", "data structure", "transformer",
		"large language model",
	}
	for _, t := range csTerms {
		if strings.Contains(lower, strings.ToLower(t)) {
			return "computer_science"
		}
	}

	implTerms := []string{
		"implementation", "framework", "library", "Tool", "system design",
		"architecture", "benchmark", "evaluation", "performance",
	}
	for _, t := range implTerms {
		if strings.Contains(lower, t) {
			return "implementation"
		}
	}

	reviewTerms := []string{
		"review", "survey", "meta-analysis", "systematic", "overview",
		"state of the art", "literature",
	}
	for _, t := range reviewTerms {
		if strings.Contains(lower, t) {
			return "review"
		}
	}

	return "academic"
}

// extractKeywords splits the query into individual significant words
// (longer than 3 characters), removing common stop words.
func extractKeywords(lower string) []string {
	stopWords := map[string]bool{
		"the": true, "and": true, "for": true, "with": true,
		"that": true, "this": true, "from": true, "what": true,
		"which": true, "have": true, "been": true, "their": true,
		"about": true, "into": true, "more": true, "some": true,
		"than": true, "them": true, "then": true, "these": true,
		"they": true, "were": true, "will": true, "each": true,
		"does": true, "how": true, "its": true, "also": true,
		"between": true, "using": true, "based": true, "such": true,
		"over": true, "after": true, "through": true,
	}

	words := strings.Fields(lower)
	var keywords []string
	seen := make(map[string]bool)
	for _, w := range words {
		// Strip basic punctuation from edges.
		w = strings.Trim(w, ".,;:!?\"'()[]{}/-")
		if len(w) > 3 && !stopWords[w] && !seen[w] {
			keywords = append(keywords, w)
			seen[w] = true
		}
	}
	return keywords
}
