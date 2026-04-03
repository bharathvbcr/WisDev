package wisdev

import (
	"context"
	"time"
)

// ==========================================
// TYPES & STRUCTURES
// ==========================================

type FailPhaseHotspot struct {
	Phase string `json:"phase"`
	Count int    `json:"count"`
}

type ExtensionTelemetry struct {
	TotalEvents   int      `json:"totalEvents"`
	AvgFocusCount float64  `json:"avgFocusCount"`
	AvgQueryCount float64  `json:"avgQueryCount"`
	TopSources    []string `json:"topSources"`
}

type WisdevOptimizationSignals struct {
	ProviderLatencyTrend map[string]float64 `json:"providerLatencyTrend"`
	FailPhaseHotspots    []FailPhaseHotspot `json:"failPhaseHotspots"`
	QuerySuccessRate     float64            `json:"querySuccessRate"`
	ExtensionTelemetry   ExtensionTelemetry `json:"extensionTelemetry"`
}

// ==========================================
// CORE LOG INGESTION LOGIC
// ==========================================

// CollectWisdevOptimizationSignals aggregates high-throughput telemetry streams into actionable signals.
func CollectWisdevOptimizationSignals(ctx context.Context, db DBProvider) WisdevOptimizationSignals {
	signals := WisdevOptimizationSignals{
		ProviderLatencyTrend: make(map[string]float64),
		FailPhaseHotspots:    []FailPhaseHotspot{},
		QuerySuccessRate:     1.0,
	}

	if db == nil {
		return signals
	}

	// 1. Calculate Query Success Rate from Journal
	// Success = status 'ok', Error = status 'error'
	var total, errors int
	err := db.QueryRow(ctx, `
		SELECT 
			COUNT(*),
			COUNT(*) FILTER (WHERE status = 'error')
		FROM wisdev_runtime_journal_v2
		WHERE event_type = 'search' AND created_at > $1
	`, time.Now().Add(-24*time.Hour).UnixMilli()).Scan(&total, &errors)

	if err == nil && total > 0 {
		signals.QuerySuccessRate = float64(total-errors) / float64(total)
	}

	// 2. Identify Fail Phase Hotspots
	rows, err := db.Query(ctx, `
		SELECT 
			payload_json->>'phase' as phase,
			COUNT(*) as count
		FROM wisdev_runtime_journal_v2
		WHERE status = 'error' AND created_at > $1
		GROUP BY phase
		ORDER BY count DESC
		LIMIT 6
	`, time.Now().Add(-24*time.Hour).UnixMilli())

	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var h FailPhaseHotspot
			if err := rows.Scan(&h.Phase, &h.Count); err == nil {
				if h.Phase == "" {
					h.Phase = "unknown"
				}
				signals.FailPhaseHotspots = append(signals.FailPhaseHotspots, h)
			}
		}
	}

	// 3. Provider Latency Trends
	// We'll use a simplified version that averages latency from the payload if present
	rows, err = db.Query(ctx, `
		SELECT 
			payload_json->>'provider' as provider,
			AVG((payload_json->>'latencyMs')::float) as avg_latency
		FROM wisdev_runtime_journal_v2
		WHERE event_type = 'search' AND status = 'ok' AND created_at > $1
		GROUP BY provider
	`, time.Now().Add(-24*time.Hour).UnixMilli())

	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var provider string
			var avg float64
			if err := rows.Scan(&provider, &avg); err == nil && provider != "" {
				signals.ProviderLatencyTrend[provider] = avg
			}
		}
	}

	// 4. Extension Telemetry
	err = db.QueryRow(ctx, `
		SELECT 
			COUNT(*),
			AVG((payload_json->>'focusCount')::float),
			AVG((payload_json->>'queryCount')::float)
		FROM wisdev_runtime_journal_v2
		WHERE event_type = 'extension_event' AND created_at > $1
	`, time.Now().Add(-7*24*time.Hour).UnixMilli()).Scan(
		&signals.ExtensionTelemetry.TotalEvents,
		&signals.ExtensionTelemetry.AvgFocusCount,
		&signals.ExtensionTelemetry.AvgQueryCount,
	)

	return signals
}
