package wisdev

import (
	"context"
	"log/slog"
	"strings"
)

// applySteeringSignal applies a mid-session user steering signal to the current loop state.
func (l *AutonomousLoop) applySteeringSignal(ctx context.Context, signal SteeringSignal, pendingQueries *[]string, bs *BeliefState, executedQueries []string) {
	if signal.Type == "" {
		return
	}

	slog.Info("Applying mid-session user steering signal", "type", signal.Type, "payload", signal.Payload)

	switch signal.Type {
	case "redirect":
		// Clear pending queries and insert the new ones
		*pendingQueries = make([]string, 0)
		for _, q := range signal.Queries {
			trimmed := strings.TrimSpace(q)
			if trimmed != "" {
				*pendingQueries = append(*pendingQueries, trimmed)
			}
		}
		slog.Info("Redirected pending queries based on user steering", "newCount", len(*pendingQueries))

	case "focus":
		// Prepend focus queries
		var newPending []string
		for _, q := range signal.Queries {
			trimmed := strings.TrimSpace(q)
			if trimmed != "" && !containsNormalizedLoopQuery(executedQueries, trimmed) {
				newPending = append(newPending, trimmed)
			}
		}
		*pendingQueries = append(newPending, *pendingQueries...)
		slog.Info("Focused pending queries based on user steering", "prependedCount", len(newPending))

	case "exclude":
		// Remove queries matching exclusion terms
		var filtered []string
		for _, q := range *pendingQueries {
			exclude := false
			lowerQ := strings.ToLower(q)
			for _, term := range signal.Queries {
				if strings.Contains(lowerQ, strings.ToLower(strings.TrimSpace(term))) {
					exclude = true
					break
				}
			}
			if !exclude {
				filtered = append(filtered, q)
			}
		}
		*pendingQueries = filtered

	case "approve":
		// Force belief to verified
		if bs != nil {
			for _, b := range bs.Beliefs {
				if strings.Contains(strings.ToLower(b.Claim), strings.ToLower(strings.TrimSpace(signal.Payload))) {
					b.Confidence = 1.0
					b.Status = BeliefStatusActive
					b.Triangulated = true
					slog.Info("Steering: manually approved belief", "beliefID", b.ID)
				}
			}
		}

	case "reject":
		// Force belief to refuted
		if bs != nil {
			for _, b := range bs.Beliefs {
				if strings.Contains(strings.ToLower(b.Claim), strings.ToLower(strings.TrimSpace(signal.Payload))) {
					b.Confidence = 0.0
					b.Status = BeliefStatusRefuted
					slog.Info("Steering: manually rejected belief", "beliefID", b.ID)
				}
			}
		}
	}
}
