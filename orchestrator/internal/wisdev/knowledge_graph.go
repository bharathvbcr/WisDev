package wisdev

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
)

// KnowledgeGraphService manages the persistence of research findings and dead ends.
type KnowledgeGraphService struct {
	db DBProvider
}

func NewKnowledgeGraphService(db DBProvider) *KnowledgeGraphService {
	return &KnowledgeGraphService{db: db}
}

// SaveFinding records a high-confidence finding into the knowledge graph.
// When embedding is empty the vector column is stored as NULL; callers that can
// reach the embed sidecar should pass a 768-d vector from EmbedTextBatch.
func (s *KnowledgeGraphService) SaveFinding(ctx context.Context, projectID, userID string, hyp *Hypothesis, embedding []float64) error {
	if s.db == nil {
		return nil
	}
	if hyp == nil {
		return fmt.Errorf("hypothesis is required")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return fmt.Errorf("user_id is required")
	}

	entityID := uuid.New().String()
	paperIDs := make([]string, len(hyp.Evidence))
	for i, e := range hyp.Evidence {
		paperIDs[i] = e.SourceID
	}

	attributes, _ := json.Marshal(map[string]any{
		"confidence":     hyp.ConfidenceScore,
		"evidence_count": hyp.EvidenceCount,
		"category":       hyp.Category,
	})

	var embeddingArg any
	if len(embedding) > 0 {
		embeddingArg = formatPGVectorLiteral(embedding)
	}

	query := `
		INSERT INTO knowledge_entities (id, project_id, user_id, type, name, description, papers, attributes, embedding, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`
	_, err := s.db.Exec(ctx, query,
		entityID,
		projectID,
		userID,
		"finding",
		hyp.Text,
		hyp.Claim,
		paperIDs,
		attributes,
		embeddingArg,
		time.Now(),
	)
	return err
}

// RecordDeadEnd marks a hypothesis as a dead end to avoid redundant future work.
func (s *KnowledgeGraphService) RecordDeadEnd(ctx context.Context, userID string, query string, hyp *Hypothesis) error {
	if s.db == nil {
		return nil
	}
	if hyp == nil {
		return fmt.Errorf("hypothesis is required")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return fmt.Errorf("user_id is required")
	}

	sql := `
		INSERT INTO research_dead_ends (user_id, query, hypothesis, evidence_count, reasoning, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`
	_, err := s.db.Exec(ctx, sql,
		userID,
		query,
		hyp.Text,
		hyp.EvidenceCount,
		fmt.Sprintf("Low confidence (%.2f) despite %d papers found.", hyp.ConfidenceScore, hyp.EvidenceCount),
		time.Now(),
	)
	return err
}

// GetRelevantDeadEnds retrieves similar dead ends to prune the current search tree.
func (s *KnowledgeGraphService) GetRelevantDeadEnds(ctx context.Context, userID string, query string) ([]string, error) {
	if s.db == nil {
		return []string{}, nil
	}

	// Simple keyword match for now. In production, this would use semantic similarity.
	rows, err := s.db.Query(ctx,
		`SELECT hypothesis FROM research_dead_ends WHERE user_id = $1 AND query LIKE $2 ESCAPE '\' LIMIT 10`,
		userID, "%"+escapeLikePattern(query)+"%",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		results = append(results, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

// GetRelevantPastFindings retrieves insights from previous quests for a new query.
// Findings are tenant-scoped by user_id (mirroring research_dead_ends).
func (s *KnowledgeGraphService) GetRelevantPastFindings(ctx context.Context, userID string, query string, embedding []float64) ([]string, error) {
	if s.db == nil {
		return []string{}, nil
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return []string{}, nil
	}

	// 1. Use vector similarity if embedding is provided; skip NULL vectors so
	// they cannot win an arbitrary top-N over real embeddings.
	if len(embedding) > 0 {
		sql := `
			SELECT name FROM knowledge_entities
			WHERE type = 'finding' AND user_id = $2 AND embedding IS NOT NULL
			ORDER BY embedding <=> $1::vector
			LIMIT 5
		`
		rows, err := s.db.Query(ctx, sql, formatPGVectorLiteral(embedding), userID)
		if err != nil {
			slog.Warn("knowledge graph vector query failed",
				"component", "wisdev.knowledge_graph",
				"operation", "get_relevant_past_findings",
				"error_code", "vector_query_failed",
				"result", "fallback_keyword",
				"error", err.Error(),
			)
		} else {
			defer rows.Close()
			var results []string
			for rows.Next() {
				var name string
				if err := rows.Scan(&name); err != nil {
					return nil, err
				}
				results = append(results, name)
			}
			if err := rows.Err(); err != nil {
				return nil, err
			}
			if len(results) > 0 {
				return results, nil
			}
		}
	}

	// 2. Fallback to keyword match (still tenant-scoped).
	rows, err := s.db.Query(ctx,
		`SELECT name FROM knowledge_entities WHERE type = 'finding' AND user_id = $1 AND name LIKE $2 ESCAPE '\' LIMIT 5`,
		userID, "%"+escapeLikePattern(query)+"%",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		results = append(results, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

func formatPGVectorLiteral(embedding []float64) string {
	if len(embedding) == 0 {
		return "[]"
	}
	var b strings.Builder
	b.Grow(len(embedding) * 8)
	b.WriteByte('[')
	for i, v := range embedding {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(fmt.Sprintf("%g", v))
	}
	b.WriteByte(']')
	return b.String()
}

// escapeLikePattern escapes \, %, and _ so user query text cannot broaden
// tenant-scoped LIKE matches via wildcard injection.
func escapeLikePattern(s string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(s)
}
