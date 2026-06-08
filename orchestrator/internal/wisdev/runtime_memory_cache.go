package wisdev

import (
	"errors"
	"path/filepath"
	"strings"
)

type persistedSessionSummaries struct {
	UserID    string           `json:"userId"`
	Summaries []map[string]any `json:"summaries"`
	UpdatedAt int64            `json:"updatedAt"`
}

type persistedProjectWorkspace struct {
	UserID    string         `json:"userId"`
	ProjectID string         `json:"projectId"`
	Payload   map[string]any `json:"payload"`
	UpdatedAt int64          `json:"updatedAt"`
}

func (s *RuntimeStateStore) SaveSessionSummaries(userID string, summaries []map[string]any) error {
	if s == nil {
		return errors.New("runtime state store is required")
	}
	normalizedUserID, err := normalizePersistenceKey("userId", userID)
	if err != nil {
		return err
	}
	if err := s.ensureStorage(); err != nil {
		return err
	}
	record := persistedSessionSummaries{
		UserID:    normalizedUserID,
		Summaries: append([]map[string]any(nil), summaries...),
		UpdatedAt: NowMillis(),
	}
	return s.writeJSONFile(s.pathFor("memory_session_summaries_"+normalizedUserID+".json"), record)
}

func (s *RuntimeStateStore) LoadSessionSummaries(userID string) ([]map[string]any, error) {
	if s == nil {
		return nil, errors.New("runtime state store is required")
	}
	normalizedUserID, err := normalizePersistenceKey("userId", userID)
	if err != nil {
		return nil, err
	}
	if err := s.ensureStorage(); err != nil {
		return nil, err
	}
	var record persistedSessionSummaries
	if err := s.readJSONFile(s.pathFor("memory_session_summaries_"+normalizedUserID+".json"), &record); err != nil {
		return nil, err
	}
	return append([]map[string]any(nil), record.Summaries...), nil
}

func (s *RuntimeStateStore) SaveProjectWorkspace(userID, projectID string, payload map[string]any) error {
	if s == nil {
		return errors.New("runtime state store is required")
	}
	normalizedUserID, err := normalizePersistenceKey("userId", userID)
	if err != nil {
		return err
	}
	normalizedProjectID, err := normalizePersistenceKey("projectId", projectID)
	if err != nil {
		return err
	}
	if err := s.ensureStorage(); err != nil {
		return err
	}
	record := persistedProjectWorkspace{
		UserID:    normalizedUserID,
		ProjectID: normalizedProjectID,
		Payload:   cloneAnyMap(payload),
		UpdatedAt: NowMillis(),
	}
	return s.writeJSONFile(s.pathFor("memory_project_workspace_"+normalizedUserID+"_"+normalizedProjectID+".json"), record)
}

func (s *RuntimeStateStore) LoadProjectWorkspace(userID, projectID string) (map[string]any, error) {
	if s == nil {
		return nil, errors.New("runtime state store is required")
	}
	normalizedUserID, err := normalizePersistenceKey("userId", userID)
	if err != nil {
		return nil, err
	}
	normalizedProjectID, err := normalizePersistenceKey("projectId", projectID)
	if err != nil {
		return nil, err
	}
	if err := s.ensureStorage(); err != nil {
		return nil, err
	}
	var record persistedProjectWorkspace
	if err := s.readJSONFile(s.pathFor("memory_project_workspace_"+normalizedUserID+"_"+normalizedProjectID+".json"), &record); err != nil {
		return nil, err
	}
	return cloneAnyMap(record.Payload), nil
}

func (s *RuntimeStateStore) SearchProjectWorkspaces(userID string) ([]map[string]any, error) {
	if s == nil {
		return nil, errors.New("runtime state store is required")
	}
	normalizedUserID, err := normalizePersistenceKey("userId", userID)
	if err != nil {
		return nil, err
	}
	if err := s.ensureStorage(); err != nil {
		return nil, err
	}
	pattern := s.pathFor("memory_project_workspace_" + normalizedUserID + "_*.json")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	workspaces := make([]map[string]any, 0, len(paths))
	for _, path := range paths {
		var record persistedProjectWorkspace
		if err := s.readJSONFile(path, &record); err != nil {
			continue
		}
		payload := cloneAnyMap(record.Payload)
		if strings.TrimSpace(AsOptionalString(payload["projectId"])) == "" {
			payload["projectId"] = record.ProjectID
		}
		workspaces = append(workspaces, payload)
	}
	return workspaces, nil
}
