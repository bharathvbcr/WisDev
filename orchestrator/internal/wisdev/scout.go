package wisdev

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type SourceCluster struct {
	ClusterID   string   `json:"clusterId"`
	Topic       string   `json:"topic"`
	PaperIDs    []string `json:"paperIds"`
	NoveltyFlag bool     `json:"noveltyFlag"`
}

type ScoutResult struct {
	Query           string          `json:"query"`
	SourceClusters  []SourceCluster `json:"sourceClusters"`
	TotalRetrieved  int             `json:"totalRetrieved"`
	NoveltySignal   float64         `json:"noveltySignal"`
	DecomposedTasks []ResearchTask  `json:"decomposedTasks"`
	SessionID       string          `json:"sessionId"`
	CreatedAt       int64           `json:"createdAt"`
}

type ScoutService struct {
	rdb     redis.UniversalClient
	planner *PlannerService
}

func NewScoutService(rdb redis.UniversalClient, planner *PlannerService) *ScoutService {
	return &ScoutService{rdb: rdb, planner: planner}
}

func (s *ScoutService) Run(ctx context.Context, sessionID, query, domain, model string) (ScoutResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return ScoutResult{}, fmt.Errorf("query is required")
	}

	session := &AgentSession{
		SessionID:      sessionID,
		OriginalQuery:  query,
		CorrectedQuery: query,
		DetectedDomain: domain,
		Mode:           WisDevModeGuided,
		ServiceTier:    ServiceTierStandard,
	}
	params := defaultRetrievePapersPlanParams(session)
	params["query"] = query
	params["limit"] = 24
	queryUsed, opts := resolveRetrievePapersSearchOptions(params, session, false)
	papers, _, err := runRetrievePapers(ctx, s.rdb, queryUsed, opts)
	if err != nil {
		return ScoutResult{}, err
	}

	clusters := buildSourceClusters(papers)
	noveltySignal := 0.0
	if len(clusters) > 0 {
		novelCount := 0
		for _, cluster := range clusters {
			if cluster.NoveltyFlag {
				novelCount++
			}
		}
		noveltySignal = float64(novelCount) / float64(len(clusters))
	}

	tasks := make([]ResearchTask, 0)
	if s.planner != nil {
		decomposed, planErr := s.planner.DecomposeTask(ctx, query, domain, model)
		if planErr == nil {
			tasks = decomposed
		}
	}

	return ScoutResult{
		Query:           queryUsed,
		SourceClusters:  clusters,
		TotalRetrieved:  len(papers),
		NoveltySignal:   ClampFloat(noveltySignal, 0, 1),
		DecomposedTasks: tasks,
		SessionID:       sessionID,
		CreatedAt:       time.Now().UnixMilli(),
	}, nil
}

func buildSourceClusters(papers []Source) []SourceCluster {
	clusterMap := make(map[string]*SourceCluster)
	for _, paper := range papers {
		topic := inferTopic(paper)
		if topic == "" {
			topic = "general"
		}
		if _, exists := clusterMap[topic]; !exists {
			clusterMap[topic] = &SourceCluster{
				ClusterID: fmt.Sprintf("cluster_%s", topic),
				Topic:     topic,
				PaperIDs:  make([]string, 0, 8),
			}
		}
		paperID := strings.TrimSpace(paper.ID)
		if paperID == "" {
			paperID = strings.TrimSpace(paper.DOI)
		}
		if paperID == "" {
			paperID = strings.TrimSpace(paper.Title)
		}
		if paperID != "" {
			clusterMap[topic].PaperIDs = append(clusterMap[topic].PaperIDs, paperID)
		}
	}

	clusters := make([]SourceCluster, 0, len(clusterMap))
	for _, cluster := range clusterMap {
		cluster.NoveltyFlag = len(cluster.PaperIDs) < 2
		clusters = append(clusters, *cluster)
	}
	sort.Slice(clusters, func(i, j int) bool {
		return clusters[i].Topic < clusters[j].Topic
	})
	return clusters
}

// inferTopic maps a paper to a canonical scientific domain by scoring keyword overlap
// between the paper's title+summary and a set of domain signal words.
// Returns the highest-scoring domain name, or "general" if no domain matches.
func inferTopic(paper Source) string {
	text := strings.ToLower(strings.TrimSpace(strings.Join([]string{paper.Title, paper.Summary}, " ")))
	if text == "" {
		return "general"
	}

	// Domain → representative signal keywords.
	// Each match adds 1 point; the domain with the most matches wins.
	domainSignals := map[string][]string{
		"machine_learning":  {"neural", "deep learning", "transformer", "attention", "gradient", "backprop", "cnn", "rnn", "lstm", "reinforcement", "embedding", "training", "inference", "llm", "generative"},
		"nlp":               {"language model", "text", "tokenization", "bert", "gpt", "sentiment", "parsing", "translation", "summarization", "named entity", "question answering"},
		"computer_vision":   {"image", "visual", "object detection", "segmentation", "convolutional", "pixel", "recognition", "siamese", "diffusion", "multimodal"},
		"biomedical":        {"clinical", "patient", "drug", "disease", "therapy", "treatment", "cancer", "gene", "protein", "mrna", "clinical trial", "biomarker", "pharmacology"},
		"genomics":          {"genome", "genomic", "sequencing", "snp", "variant", "rna", "dna", "expression", "mutation", "phenotype", "gwas", "crispr"},
		"neuroscience":      {"neuron", "brain", "cortex", "synapse", "dopamine", "fmri", "eeg", "neural circuit", "cognition", "hippocampus"},
		"physics":           {"quantum", "photon", "entanglement", "topology", "condensed matter", "astrophysics", "relativity", "electromagnetic", "particle"},
		"chemistry":         {"molecule", "reaction", "catalyst", "polymer", "synthesis", "spectroscopy", "thermodynamic", "solvent", "binding affinity"},
		"mathematics":       {"theorem", "proof", "conjecture", "algebraic", "topology", "combinatorics", "differential equation", "stochastic", "optimization"},
		"economics":         {"economic", "market", "gdp", "policy", "inflation", "fiscal", "monetary", "trade", "finance", "regression", "causal"},
		"climate_science":   {"climate", "temperature", "carbon", "greenhouse", "emissions", "atmosphere", "precipitation", "sea level", "drought"},
		"materials_science": {"material", "nanoparticle", "semiconductor", "alloy", "thin film", "composite", "crystalline", "conductivity"},
		"robotics":          {"robot", "autonomous", "manipulation", "locomotion", "sensor", "actuator", "planning", "slam"},
		"algorithms":        {"algorithm", "complexity", "heuristic", "approximation", "graph theory", "sorting", "dynamic programming", "np-hard"},
	}

	bestDomain := "general"
	bestScore := 0

	for domain, signals := range domainSignals {
		score := 0
		for _, sig := range signals {
			if strings.Contains(text, sig) {
				score++
			}
		}
		if score > bestScore {
			bestScore = score
			bestDomain = domain
		}
	}

	return bestDomain
}
