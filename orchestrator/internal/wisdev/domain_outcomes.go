package wisdev

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Cross-session domain learning. Each completed research run records its
// outcome (final confidence, paper yield) per detected domain; subsequent runs
// in the same domain warm-start from that history instead of starting cold.
// Backed by Redis when available, in-memory otherwise.

const (
	domainOutcomesRedisKey = "wisdev:domain_outcomes"
	domainOutcomeMaxRuns   = 200 // EMA horizon guard; stats are exponential anyway
	neutraldomainReward    = 0.5
)

// DomainOutcomeStats is the accumulated outcome history for one domain.
type DomainOutcomeStats struct {
	Runs      int     `json:"runs"`
	AvgReward float64 `json:"avgReward"` // exponential moving average of final confidence
	AvgPapers float64 `json:"avgPapers"`
	UpdatedAt int64   `json:"updatedAt"`
}

// DomainOutcomeStore records and serves per-domain research outcomes.
type DomainOutcomeStore struct {
	mu    sync.RWMutex
	stats map[string]DomainOutcomeStats
	rdb   redis.UniversalClient
}

// GlobalDomainOutcomes is the process-wide store; SetRedis is wired during
// gateway construction when Redis is configured.
var GlobalDomainOutcomes = NewDomainOutcomeStore()

func NewDomainOutcomeStore() *DomainOutcomeStore {
	return &DomainOutcomeStore{stats: make(map[string]DomainOutcomeStats)}
}

// SetRedis attaches persistence and loads any existing history.
func (s *DomainOutcomeStore) SetRedis(rdb redis.UniversalClient) {
	if s == nil || rdb == nil {
		return
	}
	s.mu.Lock()
	s.rdb = rdb
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	entries, err := rdb.HGetAll(ctx, domainOutcomesRedisKey).Result()
	if err != nil || len(entries) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for domain, raw := range entries {
		var stats DomainOutcomeStats
		if json.Unmarshal([]byte(raw), &stats) == nil {
			s.stats[domain] = stats
		}
	}
}

func normalizeOutcomeDomain(domain string) string {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return "general"
	}
	return domain
}

// Record folds one completed run into the domain's history.
func (s *DomainOutcomeStore) Record(ctx context.Context, domain string, finalReward float64, paperCount int) {
	if s == nil {
		return
	}
	domain = normalizeOutcomeDomain(domain)
	if finalReward < 0 {
		finalReward = 0
	}
	if finalReward > 1 {
		finalReward = 1
	}

	s.mu.Lock()
	stats := s.stats[domain]
	if stats.Runs == 0 {
		stats.AvgReward = finalReward
		stats.AvgPapers = float64(paperCount)
	} else {
		// EMA with alpha 0.2: responsive to drift, stable against outliers.
		stats.AvgReward = stats.AvgReward*0.8 + finalReward*0.2
		stats.AvgPapers = stats.AvgPapers*0.8 + float64(paperCount)*0.2
	}
	if stats.Runs < domainOutcomeMaxRuns {
		stats.Runs++
	}
	stats.UpdatedAt = NowMillis()
	s.stats[domain] = stats
	rdb := s.rdb
	s.mu.Unlock()

	slog.Debug("Recorded domain research outcome",
		"component", "wisdev.domain_learning",
		"domain", domain,
		"reward", finalReward,
		"paperCount", paperCount,
		"avgReward", stats.AvgReward,
		"runs", stats.Runs)

	if rdb != nil {
		if raw, err := json.Marshal(stats); err == nil {
			persistCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = rdb.HSet(persistCtx, domainOutcomesRedisKey, domain, string(raw)).Err()
		}
	}
	_ = ctx
}

// HistoricalReward returns the domain's EMA reward, or the neutral 0.5 when no
// history exists. Callers use values below ~0.45 as a "this domain has been
// underperforming — widen retrieval" signal.
func (s *DomainOutcomeStore) HistoricalReward(domain string) float64 {
	if s == nil {
		return neutraldomainReward
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	stats, ok := s.stats[normalizeOutcomeDomain(domain)]
	if !ok || stats.Runs == 0 {
		return neutraldomainReward
	}
	return stats.AvgReward
}
