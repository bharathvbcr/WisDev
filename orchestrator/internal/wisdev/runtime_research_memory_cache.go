package wisdev

import "errors"

type persistedResearchMemoryState struct {
	UserID    string               `json:"userId"`
	ProjectID string               `json:"projectId,omitempty"`
	State     *ResearchMemoryState `json:"state"`
	UpdatedAt int64                `json:"updatedAt"`
}

func (s *RuntimeStateStore) SaveResearchMemoryState(userID, projectID string, state *ResearchMemoryState) error {
	if s == nil {
		return errors.New("runtime state store is required")
	}
	normalizedUserID, err := normalizePersistenceKey("userId", userID)
	if err != nil {
		return err
	}
	normalizedProjectID := ""
	if projectID != "" {
		normalizedProjectID, err = normalizePersistenceKey("projectId", projectID)
		if err != nil {
			return err
		}
	}
	if err := s.ensureStorage(); err != nil {
		return err
	}
	record := persistedResearchMemoryState{
		UserID:    normalizedUserID,
		ProjectID: normalizedProjectID,
		State:     state,
		UpdatedAt: NowMillis(),
	}
	name := "research_memory_user_" + normalizedUserID + ".json"
	if normalizedProjectID != "" {
		name = "research_memory_project_" + normalizedUserID + "_" + normalizedProjectID + ".json"
	}
	return s.writeJSONFile(s.pathFor(name), record)
}

func (s *RuntimeStateStore) LoadResearchMemoryState(userID, projectID string) (*ResearchMemoryState, error) {
	if s == nil {
		return nil, errors.New("runtime state store is required")
	}
	normalizedUserID, err := normalizePersistenceKey("userId", userID)
	if err != nil {
		return nil, err
	}
	normalizedProjectID := ""
	if projectID != "" {
		normalizedProjectID, err = normalizePersistenceKey("projectId", projectID)
		if err != nil {
			return nil, err
		}
	}
	if err := s.ensureStorage(); err != nil {
		return nil, err
	}
	name := "research_memory_user_" + normalizedUserID + ".json"
	if normalizedProjectID != "" {
		name = "research_memory_project_" + normalizedUserID + "_" + normalizedProjectID + ".json"
	}
	var record persistedResearchMemoryState
	if err := s.readJSONFile(s.pathFor(name), &record); err != nil {
		return &ResearchMemoryState{}, nil
	}
	if record.State == nil {
		return &ResearchMemoryState{}, nil
	}
	return record.State, nil
}
