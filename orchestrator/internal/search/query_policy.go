package search

import (
	"regexp"
	"sort"
	"strings"
)

// QueryIntent classifies academic search intent.
type QueryIntent string

const (
	IntentPapers      QueryIntent = "papers"
	IntentDefinition  QueryIntent = "definition"
	IntentComparison  QueryIntent = "comparison"
	IntentReview      QueryIntent = "review"
	IntentMethodology QueryIntent = "methodology"
	IntentTrends      QueryIntent = "trends"
	IntentGeneral     QueryIntent = "general"
)

// SpecializedMetadata carries domain-specific hints for precached queries.
type SpecializedMetadata struct {
	ModelType       string   `json:"modelType,omitempty"`
	Frameworks      []string `json:"frameworks,omitempty"`
	SuggestedModels []string `json:"suggestedModels,omitempty"`
	InputFormats    []string `json:"inputFormats,omitempty"`
}

// EnhancedQuery is a precached or expanded query payload.
type EnhancedQuery struct {
	Original            string               `json:"original"`
	Expanded            string               `json:"expanded"`
	Intent              QueryIntent          `json:"intent"`
	Keywords            []string             `json:"keywords"`
	Synonyms            []string             `json:"synonyms"`
	SpecializedMetadata *SpecializedMetadata   `json:"specializedMetadata,omitempty"`
}

// QueryExpansionCategories groups structured expansion terms.
type QueryExpansionCategories struct {
	Synonyms        []string `json:"synonyms"`
	MeshTerms       []string `json:"meshTerms"`
	RelatedConcepts []string `json:"relatedConcepts"`
	BroaderTerms    []string `json:"broaderTerms"`
}

// QueryExpansion is structured expansion output.
type QueryExpansion struct {
	Original      string                   `json:"original"`
	Expansions    QueryExpansionCategories `json:"expansions"`
	ExpandedQuery string                   `json:"expandedQuery"`
	Confidence    float64                  `json:"confidence,omitempty"`
}

// SpecificityResult describes whether a query is overly specific.
type SpecificityResult struct {
	IsSpecific bool   `json:"isSpecific"`
	Reason     string `json:"reason"`
	Suggestion string `json:"suggestion,omitempty"`
}

// QueryEnhancementStrategy is a broader-search retry strategy.
type QueryEnhancementStrategy struct {
	Name    string   `json:"name"`
	Queries []string `json:"queries"`
	Reason  string   `json:"reason"`
}

// StaticLookupResult returns static table matches for a query.
type StaticLookupResult struct {
	MeshTerms       []string `json:"meshTerms"`
	RelatedConcepts []string `json:"relatedConcepts"`
	BroaderTerms    []string `json:"broaderTerms"`
}

var meshMappings = map[string][]string{
	"heart attack":      {"myocardial infarction", "acute coronary syndrome"},
	"stroke":            {"cerebrovascular accident", "brain infarction"},
	"high blood pressure": {"hypertension", "arterial hypertension"},
	"diabetes":          {"diabetes mellitus", "glycemic disorder"},
	"cancer":            {"neoplasm", "malignancy", "carcinoma"},
	"depression":        {"depressive disorder", "major depressive disorder"},
	"anxiety":           {"anxiety disorder", "generalized anxiety"},
	"alzheimer":         {"alzheimer disease", "alzheimer dementia", "ad"},
	"parkinson":         {"parkinson disease", "parkinsonian disorder"},
	"obesity":           {"overweight", "adiposity", "body mass index"},
	"inflammation":      {"inflammatory response", "inflammatory process"},
	"infection":         {"infectious disease", "microbial infection"},
	"pain":              {"nociception", "chronic pain", "acute pain"},
	"fever":             {"pyrexia", "hyperthermia"},
	"headache":          {"cephalalgia", "migraine"},
}

var relatedConcepts = map[string][]string{
	"fairness":          {"algorithmic bias", "ai ethics", "model fairness", "equitable ai"},
	"explainability":    {"interpretability", "xai", "model transparency", "black box"},
	"privacy":           {"data protection", "differential privacy", "anonymization"},
	"transformer":       {"attention mechanism", "self-attention", "encoder-decoder"},
	"bert":              {"transformer", "language model", "pre-training", "fine-tuning"},
	"gpt":               {"autoregressive", "language generation", "prompt engineering"},
	"diffusion":         {"denoising", "score matching", "generative model"},
	"gan":               {"generative adversarial", "discriminator", "generator"},
	"reinforcement":     {"reward learning", "policy gradient", "q-learning"},
	"reward learning":   {"reinforcement learning", "policy gradient", "q-learning"},
	"covid":             {"sars-cov-2", "coronavirus", "pandemic", "covid-19"},
	"vaccine":           {"immunization", "vaccination", "mrna", "antibody response"},
	"climate":           {"global warming", "greenhouse gas", "carbon emission"},
}

var broaderTerms = map[string][]string{
	"resnet":          {"convolutional neural network", "image classification", "deep learning"},
	"bert":            {"natural language processing", "language understanding"},
	"gpt":             {"language model", "text generation", "neural network"},
	"yolo":            {"object detection", "computer vision", "real-time detection"},
	"unet":            {"image segmentation", "medical imaging", "encoder-decoder"},
	"vae":             {"generative model", "variational inference", "latent space"},
	"attention":       {"sequence modeling", "neural network architecture"},
	"dropout":         {"regularization", "neural network training"},
	"backpropagation": {"neural network training", "gradient descent"},
	"reward learning": {"reinforcement learning"},
}

var precachedQueryExpansions = map[string]EnhancedQuery{
	"machine learning": {
		Original: "machine learning",
		Expanded: "machine learning deep learning neural networks supervised learning unsupervised learning",
		Intent:   IntentPapers,
		Keywords: []string{"machine learning", "deep learning", "neural networks", "ML", "AI"},
		Synonyms: []string{"ML", "statistical learning", "artificial intelligence", "predictive modeling"},
	},
	"deep learning": {
		Original: "deep learning",
		Expanded: "deep learning neural networks convolutional networks recurrent networks transformer",
		Intent:   IntentPapers,
		Keywords: []string{"deep learning", "neural networks", "CNN", "RNN", "transformer"},
		Synonyms: []string{"DL", "neural network learning", "representation learning"},
	},
	"natural language processing": {
		Original: "natural language processing",
		Expanded: "natural language processing NLP text mining computational linguistics language models",
		Intent:   IntentPapers,
		Keywords: []string{"NLP", "text analysis", "language model", "tokenization", "parsing"},
		Synonyms: []string{"NLP", "computational linguistics", "text mining", "language understanding"},
	},
	"transformer": {
		Original: "transformer",
		Expanded: "transformer attention mechanism self-attention BERT GPT language model",
		Intent:   IntentPapers,
		Keywords: []string{"transformer", "attention", "self-attention", "encoder-decoder", "BERT", "GPT"},
		Synonyms: []string{"attention model", "transformer architecture", "sequence-to-sequence"},
	},
	"large language model": {
		Original: "large language model",
		Expanded: "large language model LLM GPT BERT foundation model pretrained language model",
		Intent:   IntentPapers,
		Keywords: []string{"LLM", "GPT", "language model", "foundation model", "pretrained"},
		Synonyms: []string{"LLM", "foundation model", "pretrained language model", "generative AI"},
	},
	"computer vision": {
		Original: "computer vision",
		Expanded: "computer vision image recognition object detection image segmentation visual recognition",
		Intent:   IntentPapers,
		Keywords: []string{"computer vision", "image recognition", "object detection", "CNN", "visual"},
		Synonyms: []string{"CV", "image analysis", "visual computing", "image understanding"},
	},
	"reinforcement learning": {
		Original: "reinforcement learning",
		Expanded: "reinforcement learning reward learning policy gradient Q-learning deep RL",
		Intent:   IntentPapers,
		Keywords: []string{"reinforcement learning", "RL", "reward", "policy", "Q-learning", "agent"},
		Synonyms: []string{"RL", "reward learning", "sequential decision making"},
	},
	"climate change": {
		Original: "climate change",
		Expanded: "climate change global warming greenhouse gas carbon emission climate adaptation",
		Intent:   IntentPapers,
		Keywords: []string{"climate change", "global warming", "carbon", "greenhouse", "emissions"},
		Synonyms: []string{"global warming", "climate crisis", "environmental change"},
	},
	"covid": {
		Original: "covid",
		Expanded: "COVID-19 SARS-CoV-2 coronavirus pandemic respiratory infection",
		Intent:   IntentPapers,
		Keywords: []string{"COVID-19", "coronavirus", "SARS-CoV-2", "pandemic", "respiratory"},
		Synonyms: []string{"COVID-19", "SARS-CoV-2", "novel coronavirus", "2019-nCoV"},
	},
	"cancer": {
		Original: "cancer",
		Expanded: "cancer oncology tumor neoplasm carcinoma malignancy cancer treatment",
		Intent:   IntentPapers,
		Keywords: []string{"cancer", "oncology", "tumor", "carcinoma", "malignancy"},
		Synonyms: []string{"neoplasm", "malignant tumor", "oncological disease"},
	},
	"diabetes": {
		Original: "diabetes",
		Expanded: "diabetes mellitus glucose insulin glycemic control type 2 diabetes",
		Intent:   IntentPapers,
		Keywords: []string{"diabetes", "glucose", "insulin", "glycemic", "metabolic"},
		Synonyms: []string{"diabetes mellitus", "glycemic disorder", "insulin resistance"},
	},
	"neural network": {
		Original: "neural network",
		Expanded: "neural network artificial neural network deep neural network feedforward backpropagation",
		Intent:   IntentPapers,
		Keywords: []string{"neural network", "ANN", "deep network", "perceptron", "activation"},
		Synonyms: []string{"ANN", "artificial neural network", "connectionist model"},
	},
	"blockchain": {
		Original: "blockchain",
		Expanded: "blockchain distributed ledger cryptocurrency smart contract decentralized",
		Intent:   IntentPapers,
		Keywords: []string{"blockchain", "distributed ledger", "cryptocurrency", "smart contract"},
		Synonyms: []string{"distributed ledger technology", "DLT", "decentralized system"},
	},
	"cybersecurity": {
		Original: "cybersecurity",
		Expanded: "cybersecurity information security network security threat detection intrusion detection",
		Intent:   IntentPapers,
		Keywords: []string{"cybersecurity", "security", "threat", "intrusion", "malware"},
		Synonyms: []string{"information security", "network security", "cyber defense"},
	},
	"quantum computing": {
		Original: "quantum computing",
		Expanded: "quantum computing qubit quantum algorithm quantum supremacy quantum circuit",
		Intent:   IntentPapers,
		Keywords: []string{"quantum computing", "qubit", "quantum algorithm", "superposition"},
		Synonyms: []string{"quantum computation", "quantum information", "quantum processor"},
	},
	"protein folding": {
		Original: "protein folding",
		Expanded: "protein folding protein structure AlphaFold molecular dynamics secondary structure",
		Intent:   IntentPapers,
		Keywords: []string{"protein folding", "protein structure", "AlphaFold", "molecular dynamics"},
		Synonyms: []string{"protein structure prediction", "protein conformation", "folding kinetics"},
	},
	"gene therapy": {
		Original: "gene therapy",
		Expanded: "gene therapy CRISPR gene editing viral vector genetic modification",
		Intent:   IntentPapers,
		Keywords: []string{"gene therapy", "CRISPR", "gene editing", "viral vector", "genetic"},
		Synonyms: []string{"genetic therapy", "gene transfer", "genetic modification"},
	},
	"drug discovery": {
		Original: "drug discovery",
		Expanded: "drug discovery pharmaceutical screening molecular docking drug target",
		Intent:   IntentPapers,
		Keywords: []string{"drug discovery", "pharmaceutical", "screening", "molecular", "target"},
		Synonyms: []string{"drug development", "pharmaceutical research", "lead optimization"},
	},
	"sustainable energy": {
		Original: "sustainable energy",
		Expanded: "sustainable energy renewable energy solar power wind energy clean energy",
		Intent:   IntentPapers,
		Keywords: []string{"sustainable energy", "renewable", "solar", "wind", "clean energy"},
		Synonyms: []string{"renewable energy", "clean energy", "green energy", "alternative energy"},
	},
	"autonomous vehicles": {
		Original: "autonomous vehicles",
		Expanded: "autonomous vehicles self-driving car autonomous driving ADAS lidar",
		Intent:   IntentPapers,
		Keywords: []string{"autonomous vehicles", "self-driving", "ADAS", "lidar", "perception"},
		Synonyms: []string{"self-driving cars", "driverless vehicles", "automated driving"},
	},
	"pytorch": {
		Original: "pytorch",
		Expanded: "pytorch deep learning framework neural network training torch",
		Intent:   IntentPapers,
		Keywords: []string{"PyTorch", "deep learning", "neural network", "torch"},
		Synonyms: []string{"torch", "deep learning framework", "python ML framework"},
		SpecializedMetadata: &SpecializedMetadata{
			ModelType:       "ml",
			Frameworks:      []string{"PyTorch"},
			SuggestedModels: []string{"ResNet", "Transformer", "BERT"},
		},
	},
	"tensorflow": {
		Original: "tensorflow",
		Expanded: "tensorflow deep learning machine learning framework keras neural network",
		Intent:   IntentPapers,
		Keywords: []string{"TensorFlow", "Keras", "deep learning", "machine learning"},
		Synonyms: []string{"tf", "deep learning framework", "google ML framework"},
		SpecializedMetadata: &SpecializedMetadata{
			ModelType:       "ml",
			Frameworks:      []string{"TensorFlow", "Keras"},
			SuggestedModels: []string{"Transformer", "CNN", "RNN"},
		},
	},
	"named entity recognition": {
		Original: "named entity recognition",
		Expanded: "named entity recognition NER information extraction sequence labeling NLP",
		Intent:   IntentPapers,
		Keywords: []string{"NER", "named entity recognition", "information extraction", "NLP"},
		Synonyms: []string{"entity extraction", "sequence labeling", "token classification"},
		SpecializedMetadata: &SpecializedMetadata{
			ModelType:       "nlp",
			Frameworks:      []string{"spaCy", "HuggingFace"},
			SuggestedModels: []string{"BERT", "RoBERTa", "BiLSTM-CRF"},
		},
	},
	"object detection": {
		Original: "object detection",
		Expanded: "object detection YOLO bounding box computer vision detection model",
		Intent:   IntentPapers,
		Keywords: []string{"object detection", "YOLO", "bounding box", "computer vision"},
		Synonyms: []string{"detection model", "visual object detection", "instance detection"},
		SpecializedMetadata: &SpecializedMetadata{
			ModelType:       "ml",
			Frameworks:      []string{"PyTorch", "TensorFlow"},
			SuggestedModels: []string{"YOLO", "Faster R-CNN", "DETR"},
		},
	},
	"fasta format": {
		Original: "fasta format",
		Expanded: "fasta format nucleotide protein sequence bioinformatics file format",
		Intent:   IntentPapers,
		Keywords: []string{"FASTA", "sequence", "bioinformatics", "nucleotide", "protein"},
		Synonyms: []string{"sequence file format", "biosequence format", "genomic sequence format"},
		SpecializedMetadata: &SpecializedMetadata{
			ModelType:    "bio",
			InputFormats: []string{"FASTA"},
		},
	},
	"sequence alignment": {
		Original: "sequence alignment",
		Expanded: "sequence alignment pairwise alignment multiple alignment BLAST bioinformatics",
		Intent:   IntentPapers,
		Keywords: []string{"sequence alignment", "BLAST", "pairwise alignment", "MSA"},
		Synonyms: []string{"pairwise alignment", "multiple sequence alignment", "alignment algorithm"},
		SpecializedMetadata: &SpecializedMetadata{
			ModelType:    "bio",
			InputFormats: []string{"FASTA", "FASTQ"},
		},
	},
	"rna sequencing": {
		Original: "rna sequencing",
		Expanded: "rna sequencing RNA-seq transcriptome analysis gene expression profiling",
		Intent:   IntentPapers,
		Keywords: []string{"RNA-seq", "transcriptome", "gene expression", "sequencing"},
		Synonyms: []string{"transcriptomics", "rna-seq", "expression profiling"},
		SpecializedMetadata: &SpecializedMetadata{
			ModelType:    "bio",
			InputFormats: []string{"FASTQ", "BAM"},
		},
	},
	"dicom": {
		Original: "dicom",
		Expanded: "dicom digital imaging and communications in medicine medical imaging standard",
		Intent:   IntentPapers,
		Keywords: []string{"DICOM", "medical imaging", "radiology", "PACS"},
		Synonyms: []string{"digital imaging standard", "medical image format", "radiology format"},
		SpecializedMetadata: &SpecializedMetadata{
			ModelType:    "medical",
			InputFormats: []string{"DICOM"},
		},
	},
	"radiomics": {
		Original: "radiomics",
		Expanded: "radiomics quantitative imaging feature extraction medical image biomarkers",
		Intent:   IntentPapers,
		Keywords: []string{"radiomics", "quantitative imaging", "image biomarkers", "medical imaging"},
		Synonyms: []string{"imaging biomarkers", "quantitative image analysis", "medical image features"},
		SpecializedMetadata: &SpecializedMetadata{
			ModelType:    "medical",
			Frameworks:   []string{"PyRadiomics"},
			InputFormats: []string{"DICOM", "NIfTI"},
		},
	},
}

var (
	yearPattern      = regexp.MustCompile(`\b(19|20)\d{2}\b`)
	booleanPattern   = regexp.MustCompile(`\b(AND|OR)\b`)
)

func lookupTable(term string, table map[string][]string) []string {
	key := strings.ToLower(strings.TrimSpace(term))
	if key == "" {
		return nil
	}
	if values, ok := table[key]; ok {
		return append([]string(nil), values...)
	}
	return nil
}

// LookupMesh returns MeSH mappings for a term.
func LookupMesh(term string) []string {
	return lookupTable(term, meshMappings)
}

// LookupRelated returns related concept mappings for a term.
func LookupRelated(term string) []string {
	return lookupTable(term, relatedConcepts)
}

// LookupBroader returns broader term mappings for a term.
func LookupBroader(term string) []string {
	return lookupTable(term, broaderTerms)
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

// LookupStaticExpansions aggregates static table matches for query tokens.
func LookupStaticExpansions(query string) StaticLookupResult {
	normalized := strings.ToLower(strings.TrimSpace(query))
	if normalized == "" {
		return StaticLookupResult{}
	}

	mesh := make([]string, 0)
	related := make([]string, 0)
	broader := make([]string, 0)

	tokens := strings.Fields(normalized)
	keys := append([]string{normalized}, tokens...)
	seen := make(map[string]struct{})
	for _, key := range keys {
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		mesh = append(mesh, LookupMesh(key)...)
		related = append(related, LookupRelated(key)...)
		broader = append(broader, LookupBroader(key)...)
	}

	return StaticLookupResult{
		MeshTerms:       uniqueStrings(mesh),
		RelatedConcepts: uniqueStrings(related),
		BroaderTerms:    uniqueStrings(broader),
	}
}

func cloneEnhancedQuery(entry EnhancedQuery) EnhancedQuery {
	out := entry
	out.Keywords = append([]string(nil), entry.Keywords...)
	out.Synonyms = append([]string(nil), entry.Synonyms...)
	if entry.SpecializedMetadata != nil {
		meta := *entry.SpecializedMetadata
		meta.Frameworks = append([]string(nil), meta.Frameworks...)
		meta.SuggestedModels = append([]string(nil), meta.SuggestedModels...)
		meta.InputFormats = append([]string(nil), meta.InputFormats...)
		out.SpecializedMetadata = &meta
	}
	return out
}

func matchesPrecachedKey(normalized, key string) bool {
	return normalized == key ||
		strings.HasPrefix(normalized, key+" ") ||
		strings.HasSuffix(normalized, " "+key) ||
		strings.Contains(normalized, " "+key+" ") ||
		strings.Contains(normalized, key)
}

// GetPrecachedExpansion returns a precached expansion when available.
func GetPrecachedExpansion(query string) *EnhancedQuery {
	normalized := strings.ToLower(strings.TrimSpace(query))
	if normalized == "" {
		return nil
	}

	if entry, ok := precachedQueryExpansions[normalized]; ok {
		result := cloneEnhancedQuery(entry)
		result.Original = query
		return &result
	}

	for key, entry := range precachedQueryExpansions {
		if matchesPrecachedKey(normalized, key) {
			result := cloneEnhancedQuery(entry)
			result.Original = query
			result.Expanded = query + " " + entry.Expanded
			return &result
		}
	}
	return nil
}

// EmbeddingTargets returns normalized terms used for precached embedding warmup.
func EmbeddingTargets() []string {
	targets := make(map[string]struct{})
	add := func(value string) {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if len(normalized) > 1 {
			targets[normalized] = struct{}{}
		}
	}

	for key, entry := range precachedQueryExpansions {
		add(key)
		add(entry.Expanded)
		for _, keyword := range entry.Keywords {
			add(keyword)
		}
		for _, synonym := range entry.Synonyms {
			add(synonym)
		}
	}
	for key, values := range meshMappings {
		add(key)
		for _, value := range values {
			add(value)
		}
	}
	for key, values := range relatedConcepts {
		add(key)
		for _, value := range values {
			add(value)
		}
	}
	for key, values := range broaderTerms {
		add(key)
		for _, value := range values {
			add(value)
		}
	}

	out := make([]string, 0, len(targets))
	for target := range targets {
		out = append(out, target)
	}
	sort.Strings(out)
	return out
}

// AnalyzeQuerySpecificity detects overly specific queries.
func AnalyzeQuerySpecificity(query string) SpecificityResult {
	words := strings.Fields(strings.TrimSpace(query))

	if yearPattern.MatchString(query) {
		return SpecificityResult{
			IsSpecific: true,
			Reason:     "Query contains specific year",
			Suggestion: "Try removing the year for more results",
		}
	}
	if len(words) > 6 {
		return SpecificityResult{
			IsSpecific: true,
			Reason:     "Query has many terms",
			Suggestion: "Try using fewer, more essential keywords",
		}
	}
	if booleanPattern.MatchString(query) {
		return SpecificityResult{
			IsSpecific: true,
			Reason:     "Query uses Boolean operators",
			Suggestion: "Try a simpler query without AND/OR",
		}
	}
	if strings.Contains(query, `"`) {
		return SpecificityResult{
			IsSpecific: true,
			Reason:     "Query requires exact phrase match",
			Suggestion: "Try removing quotes for more results",
		}
	}
	for _, word := range words {
		if len(word) > 20 {
			return SpecificityResult{
				IsSpecific: true,
				Reason:     "Query contains very long technical terms",
				Suggestion: "Try breaking down technical terms",
			}
		}
	}

	return SpecificityResult{
		IsSpecific: false,
		Reason:     "Query appears well-balanced",
	}
}

// GenerateQueryVariationsForCoverage builds query variants for broader coverage.
func GenerateQueryVariationsForCoverage(query string, expansion QueryExpansion, maxVariations int) []string {
	if maxVariations <= 0 {
		maxVariations = 3
	}

	variations := []string{query}

	if len(expansion.Expansions.Synonyms) > 0 {
		synTerms := expansion.Expansions.Synonyms
		if len(synTerms) > 2 {
			synTerms = synTerms[:2]
		}
		variations = append(variations, query+" OR "+strings.Join(synTerms, " OR "))
	}
	if len(expansion.Expansions.BroaderTerms) > 0 {
		broader := expansion.Expansions.BroaderTerms[0]
		if broader != "" && !strings.EqualFold(broader, query) {
			variations = append(variations, broader)
		}
	}
	if len(expansion.Expansions.MeshTerms) > 0 {
		mesh := expansion.Expansions.MeshTerms
		if len(mesh) > 2 {
			mesh = mesh[:2]
		}
		variations = append(variations, strings.Join(mesh, " "))
	}
	if len(expansion.Expansions.RelatedConcepts) >= 2 {
		related := expansion.Expansions.RelatedConcepts
		if len(related) > 2 {
			related = related[:2]
		}
		variations = append(variations, strings.Join(related, " "))
	}

	seen := make(map[string]struct{}, len(variations))
	unique := make([]string, 0, len(variations))
	for _, variation := range variations {
		if variation == "" {
			continue
		}
		if _, ok := seen[variation]; ok {
			continue
		}
		seen[variation] = struct{}{}
		unique = append(unique, variation)
	}
	if len(unique) > maxVariations {
		return unique[:maxVariations]
	}
	return unique
}

// GenerateBroaderStrategies returns broader retry strategies.
func GenerateBroaderStrategies(query string, expansion QueryExpansion) []QueryEnhancementStrategy {
	strategies := make([]QueryEnhancementStrategy, 0, 4)
	words := strings.Fields(strings.TrimSpace(query))

	if len(words) > 2 {
		important := make([]string, 0, len(words))
		for _, word := range words {
			if len(word) > 3 {
				important = append(important, word)
			}
		}
		sort.SliceStable(important, func(i, j int) bool {
			return len(important[i]) > len(important[j])
		})
		if len(important) > 2 {
			important = important[:2]
		}
		if len(important) >= 2 {
			strategies = append(strategies, QueryEnhancementStrategy{
				Name:    "Simplified",
				Queries: []string{strings.Join(important, " ")},
				Reason:  "Removed specific terms for broader results",
			})
		}
	}

	if len(expansion.Expansions.BroaderTerms) > 0 {
		broader := expansion.Expansions.BroaderTerms
		if len(broader) > 2 {
			broader = broader[:2]
		}
		strategies = append(strategies, QueryEnhancementStrategy{
			Name:    "Generalized",
			Queries: append([]string(nil), broader...),
			Reason:  "Using broader category terms",
		})
	}

	if len(expansion.Expansions.RelatedConcepts) > 0 {
		related := expansion.Expansions.RelatedConcepts
		if len(related) > 2 {
			related = related[:2]
		}
		strategies = append(strategies, QueryEnhancementStrategy{
			Name:    "Related Topics",
			Queries: append([]string(nil), related...),
			Reason:  "Searching related research areas",
		})
	}

	if len(words) >= 3 {
		keywords := make([]string, 0, 2)
		for _, word := range words {
			if len(word) > 4 {
				keywords = append(keywords, word)
			}
		}
		if len(keywords) > 2 {
			keywords = keywords[:2]
		}
		if len(keywords) > 0 {
			strategies = append(strategies, QueryEnhancementStrategy{
				Name:    "Keyword Focus",
				Queries: keywords,
				Reason:  "Searching individual key concepts",
			})
		}
	}

	return strategies
}

// GetOptimizedQuerySet returns prioritized query variants.
func GetOptimizedQuerySet(query string, expansion QueryExpansion, resultsNeeded int) []string {
	if resultsNeeded <= 0 {
		resultsNeeded = 10
	}

	specificity := AnalyzeQuerySpecificity(query)
	variations := GenerateQueryVariationsForCoverage(query, expansion, 4)

	if specificity.IsSpecific && resultsNeeded > 20 {
		broader := GenerateBroaderStrategies(query, expansion)
		broaderQueries := make([]string, 0)
		for _, strategy := range broader {
			broaderQueries = append(broaderQueries, strategy.Queries...)
		}
		combined := append(variations, broaderQueries...)
		if len(combined) > 5 {
			return combined[:5]
		}
		return combined
	}

	return variations
}

// ValidQueryIntents returns allowed intent labels.
func ValidQueryIntents() []QueryIntent {
	return []QueryIntent{
		IntentPapers,
		IntentDefinition,
		IntentComparison,
		IntentReview,
		IntentMethodology,
		IntentTrends,
		IntentGeneral,
	}
}

// NormalizeQueryIntent validates and normalizes an intent string.
func NormalizeQueryIntent(intent string) (QueryIntent, bool) {
	normalized := QueryIntent(strings.ToLower(strings.TrimSpace(intent)))
	for _, valid := range ValidQueryIntents() {
		if normalized == valid {
			return normalized, true
		}
	}
	return IntentPapers, false
}

// DetectQueryIntentHeuristic ports the former frontend Phase-1 regex intent
// classifier. Returns (intent, true) on a clear pattern match.
func DetectQueryIntentHeuristic(query string) (QueryIntent, bool) {
	query = strings.TrimSpace(query)
	if query == "" {
		return IntentGeneral, false
	}
	lower := strings.ToLower(query)

	if intentDefinitionPattern.MatchString(query) ||
		strings.HasPrefix(lower, "define ") ||
		strings.Contains(lower, "meaning of") {
		return IntentDefinition, true
	}

	if strings.Contains(lower, " vs ") || strings.Contains(lower, " versus ") ||
		intentDifferencePattern.MatchString(query) ||
		intentComparePattern.MatchString(query) {
		return IntentComparison, true
	}

	if intentReviewLeadPattern.MatchString(query) ||
		intentReviewOfPattern.MatchString(query) ||
		intentOverviewPattern.MatchString(query) ||
		strings.Contains(lower, "state of the art") ||
		strings.Contains(lower, "state-of-the-art") {
		return IntentReview, true
	}

	if intentHowToPattern.MatchString(query) ||
		strings.HasPrefix(lower, "protocol for ") ||
		intentMethodsForPattern.MatchString(query) {
		return IntentMethodology, true
	}

	if intentTrendsPattern.MatchString(query) ||
		strings.Contains(lower, "trend") ||
		strings.Contains(lower, "advances in") {
		return IntentTrends, true
	}

	return IntentPapers, false
}

var (
	intentDefinitionPattern  = regexp.MustCompile(`(?i)^what (is|are) `)
	intentDifferencePattern  = regexp.MustCompile(`(?i)difference between .+ and `)
	intentComparePattern     = regexp.MustCompile(`(?i)compare .+ (to|with|and) `)
	intentReviewLeadPattern  = regexp.MustCompile(`(?i)^(literature |systematic )?review (of|on) `)
	intentReviewOfPattern    = regexp.MustCompile(`(?i)\breview (of|on) `)
	intentOverviewPattern    = regexp.MustCompile(`(?i)^overview (of|on) `)
	intentHowToPattern       = regexp.MustCompile(`(?i)^how (to|do|can) `)
	intentMethodsForPattern  = regexp.MustCompile(`(?i)^methods? for `)
	intentTrendsPattern      = regexp.MustCompile(`(?i)\b(202[3-9]|recent|latest|emerging|new) `)
)
