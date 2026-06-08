package wisdev

import (
	"context"
	"encoding/json"
	"fmt"
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
func (s *KnowledgeGraphService) SaveFinding(ctx context.Context, projectID string, hyp *Hypothesis) error {
	if s.db == nil {
		return nil
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

	query := `
		INSERT INTO knowledge_entities (id, project_id, type, name, description, papers, attributes, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`
	_, err := s.db.Exec(ctx, query,
		entityID,
		projectID,
		"finding",
		hyp.Text,
		hyp.Claim,
		paperIDs,
		attributes,
		time.Now(),
	)
	return err
}

// RecordDeadEnd marks a hypothesis as a dead end to avoid redundant future work.
func (s *KnowledgeGraphService) RecordDeadEnd(ctx context.Context, userID string, query string, hyp *Hypothesis) error {
	if s.db == nil {
		return nil
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
	rows, err := s.db.Query(ctx, "SELECT hypothesis FROM research_dead_ends WHERE user_id = $1 AND query LIKE $2 LIMIT 10", userID, "%"+query+"%")
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
	return results, nil
}

// GetRelevantPastFindings retrieves insights from previous quests for a new query.
func (s *KnowledgeGraphService) GetRelevantPastFindings(ctx context.Context, userID string, query string, embedding []float64) ([]string, error) {
	if s.db == nil {
		return []string{}, nil
	}

	// 1. Use vector similarity if embedding is provided
	if len(embedding) > 0 {
		sql := "SELECT name FROM knowledge_entities WHERE type = 'finding' ORDER BY embedding <=> $1 LIMIT 5"
		rows, err := s.db.Query(ctx, sql, embedding)
		if err == nil {
			defer rows.Close()
			var results []string
			for rows.Next() {
				var name string
				if err := rows.Scan(&name); err == nil {
					results = append(results, name)
				}
			}
			if len(results) > 0 {
				return results, nil
			}
		}
	}

	// 2. Fallback to keyword match
	rows, err := s.db.Query(ctx, "SELECT name FROM knowledge_entities WHERE type = 'finding' AND name LIKE $1 LIMIT 5", "%"+query+"%")
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
	return results, nil
}
