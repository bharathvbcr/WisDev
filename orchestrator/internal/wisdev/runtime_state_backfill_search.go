package wisdev

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

func (s *RuntimeStateStore) SearchEvidenceDossiers(userID string) ([]map[string]any, error) {
	if s == nil {
		return nil, fmt.Errorf("runtime state store is required")
	}
	normalizedUserID, err := normalizePersistenceKey("userId", userID)
	if err != nil {
		return nil, err
	}
	if err := s.ensureStorage(); err != nil && s.db == nil {
		return nil, err
	}
	byID := map[string]map[string]any{}
	if s.db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		rows, queryErr := s.db.Query(ctx, `
SELECT dossier_id, payload_json
FROM wisdev_evidence_dossiers
WHERE user_id = $1
ORDER BY updated_at DESC
`, normalizedUserID)
		if queryErr == nil {
			defer rows.Close()
			for rows.Next() {
				var dossierID string
				var raw []byte
				if err := rows.Scan(&dossierID, &raw); err != nil {
					continue
				}
				payload := map[string]any{}
				if err := json.Unmarshal(raw, &payload); err != nil {
					continue
				}
				if _, ok := byID[dossierID]; !ok {
					byID[dossierID] = payload
				}
			}
		}
	}

	pattern := s.pathFor("evidence_dossier_*.json")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	for _, path := range paths {
		record := PersistedEvidenceDossier{}
		if err := s.readJSONFile(path, &record); err != nil {
			continue
		}
		if strings.TrimSpace(record.UserID) != normalizedUserID {
			continue
		}
		if _, ok := byID[record.DossierID]; ok {
			continue
		}
		byID[record.DossierID] = cloneAnyMap(record.Payload)
	}
	return mapValues(byID), nil
}

func (s *RuntimeStateStore) SearchQuestStates(userID string) ([]map[string]any, error) {
	if s == nil {
		return nil, fmt.Errorf("runtime state store is required")
	}
	normalizedUserID, err := normalizePersistenceKey("userId", userID)
	if err != nil {
		return nil, err
	}
	if err := s.ensureStorage(); err != nil && s.db == nil {
		return nil, err
	}
	byID := map[string]map[string]any{}
	if s.db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		rows, queryErr := s.db.Query(ctx, `
SELECT quest_id, payload_json
FROM wisdev_quest_states
WHERE user_id = $1
ORDER BY updated_at DESC
`, normalizedUserID)
		if queryErr == nil {
			defer rows.Close()
			for rows.Next() {
				var questID string
				var raw []byte
				if err := rows.Scan(&questID, &raw); err != nil {
					continue
				}
				payload := map[string]any{}
				if err := json.Unmarshal(raw, &payload); err != nil {
					continue
				}
				if _, ok := byID[questID]; !ok {
					byID[questID] = payload
				}
			}
		}
	}

	pattern := s.pathFor("quest_state_*.json")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	for _, path := range paths {
		record := PersistedQuestState{}
		if err := s.readJSONFile(path, &record); err != nil {
			continue
		}
		if strings.TrimSpace(record.UserID) != normalizedUserID {
			continue
		}
		if _, ok := byID[record.QuestID]; ok {
			continue
		}
		byID[record.QuestID] = cloneAnyMap(record.Payload)
	}
	return mapValues(byID), nil
}

func mapValues(input map[string]map[string]any) []map[string]any {
	values := make([]map[string]any, 0, len(input))
	for _, value := range input {
		values = append(values, value)
	}
	return values
}
