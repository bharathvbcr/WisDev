package wisdev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type PostgresQuestStore struct {
	db DBProvider
}

func NewPostgresQuestStore(db DBProvider) *PostgresQuestStore {
	return &PostgresQuestStore{db: db}
}

func normalizeOptionalQuestUserID(raw string) any {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	if _, err := uuid.Parse(trimmed); err != nil {
		return nil
	}
	return trimmed
}

func (s *PostgresQuestStore) SaveQuestState(ctx context.Context, quest *QuestState) error {
	if s == nil || s.db == nil {
		return errors.New("quest store database unavailable")
	}
	if quest == nil {
		return errors.New("quest is required")
	}

	questID := firstNonEmpty(quest.ID, quest.SessionID, quest.QuestID)
	if questID == "" {
		return errors.New("quest id is required")
	}
	if quest.UpdatedAt == 0 {
		quest.UpdatedAt = NowMillis()
	}

	raw, err := json.Marshal(quest)
	if err != nil {
		return fmt.Errorf("marshal quest state: %w", err)
	}

	_, err = s.db.Exec(ctx, `
INSERT INTO wisdev_quest_states (quest_id, user_id, payload_json, updated_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (quest_id) DO UPDATE SET
	user_id = EXCLUDED.user_id,
	payload_json = EXCLUDED.payload_json,
	updated_at = EXCLUDED.updated_at
`, questID, normalizeOptionalQuestUserID(quest.UserID), raw, quest.UpdatedAt)
	if err != nil {
		return fmt.Errorf("save quest state: %w", err)
	}
	return nil
}

func (s *PostgresQuestStore) LoadQuestState(ctx context.Context, questID string) (*QuestState, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("quest store database unavailable")
	}
	questID = strings.TrimSpace(questID)
	if questID == "" {
		return nil, errors.New("quest id is required")
	}

	var raw []byte
	if err := s.db.QueryRow(ctx, `
SELECT payload_json
FROM wisdev_quest_states
WHERE quest_id = $1
`, questID).Scan(&raw); err != nil {
		return nil, err
	}

	var quest QuestState
	if err := json.Unmarshal(raw, &quest); err != nil {
		return nil, fmt.Errorf("decode quest state: %w", err)
	}
	if quest.ID == "" {
		quest.ID = questID
	}
	if quest.SessionID == "" {
		quest.SessionID = questID
	}
	return &quest, nil
}

func (s *PostgresQuestStore) SaveIteration(ctx context.Context, questID string, iter IterationRecord) error {
	if s == nil || s.db == nil {
		return errors.New("quest store database unavailable")
	}
	questID = strings.TrimSpace(questID)
	if questID == "" {
		return errors.New("quest id is required")
	}
	if iter.Timestamp.IsZero() {
		iter.Timestamp = time.Now()
	}

	raw, err := json.Marshal(iter)
	if err != nil {
		return fmt.Errorf("marshal iteration: %w", err)
	}

	_, err = s.db.Exec(ctx, `
INSERT INTO wisdev_quest_iterations (quest_id, iteration, payload_json, created_at)
VALUES ($1, $2, $3, $4)
`, questID, iter.Iteration, raw, iter.Timestamp.UnixMilli())
	if err != nil {
		return fmt.Errorf("save quest iteration: %w", err)
	}
	return nil
}
