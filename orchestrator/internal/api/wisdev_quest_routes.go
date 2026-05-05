package api

import (
	"net/http"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

func (s *wisdevServer) registerQuestRoutes(mux *http.ServeMux, agentGateway *wisdev.AgentGateway) {
	mux.HandleFunc("/wisdev/quests", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethods": []string{http.MethodPost},
			})
			return
		}
		runtime := resolveQuestRuntime(agentGateway)
		if runtime == nil {
			WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "quest runtime unavailable", nil)
			return
		}

		var req wisdev.ResearchQuestRequest
		if err := decodeStrictJSONBody(r.Body, &req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{"error": err.Error()})
			return
		}
		userID, err := resolveAuthorizedUserID(r, strings.TrimSpace(req.UserID))
		if err != nil {
			WriteError(w, http.StatusForbidden, ErrUnauthorized, "access denied", nil)
			return
		}
		req.UserID = userID
		quest, err := runtime.StartQuest(r.Context(), req)
		if err != nil {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, err.Error(), map[string]any{"field": "query"})
			return
		}
		traceID := writeEnvelope(w, "quest", quest)
		s.journalEvent(
			"quest_started",
			"/wisdev/quests",
			traceID,
			quest.SessionID,
			quest.UserID,
			quest.QuestID,
			"",
			"Research quest started.",
			map[string]any{"questId": quest.QuestID},
			map[string]any{"mode": quest.Mode, "qualityMode": quest.QualityMode},
		)
	})

	mux.HandleFunc("/wisdev/quests/", func(w http.ResponseWriter, r *http.Request) {
		runtime := resolveQuestRuntime(agentGateway)
		if runtime == nil {
			WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "quest runtime unavailable", nil)
			return
		}

		questID, action, ok := parseQuestRoute(r.URL.Path)
		if !ok {
			WriteError(w, http.StatusNotFound, ErrNotFound, "quest route not found", nil)
			return
		}

		quest, err := runtime.LoadQuest(r.Context(), questID)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, ErrInternal, "failed to load quest", map[string]any{"error": err.Error(), "questId": questID})
			return
		}
		if quest == nil {
			WriteError(w, http.StatusNotFound, ErrNotFound, "quest not found", map[string]any{"questId": questID})
			return
		}
		if !requireOwnerAccess(w, r, quest.UserID) {
			return
		}

		switch action {
		case "events":
			if r.Method != http.MethodGet {
				WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{"allowedMethods": []string{http.MethodGet}})
				return
			}
			events, err := runtime.GetEvents(r.Context(), questID)
			if err != nil {
				WriteError(w, http.StatusInternalServerError, ErrInternal, "failed to load quest events", map[string]any{"error": err.Error(), "questId": questID})
				return
			}
			traceID := writeEnvelope(w, "events", events)
			s.journalEvent("quest_events_loaded", "/wisdev/quests/"+questID+"/events", traceID, quest.SessionID, quest.UserID, quest.QuestID, "", "Quest events loaded.", map[string]any{"questId": questID, "count": len(events)}, nil)
		case "resume":
			if r.Method != http.MethodPost {
				WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{"allowedMethods": []string{http.MethodPost}})
				return
			}
			var req wisdev.ResearchQuestRequest
			if r.Body != nil && r.ContentLength != 0 {
				if err := decodeStrictJSONBody(r.Body, &req); err != nil {
					WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{"error": err.Error()})
					return
				}
			}
			req.UserID = quest.UserID
			resumed, err := runtime.ResumeQuest(r.Context(), questID, req)
			if err != nil {
				WriteError(w, http.StatusBadRequest, ErrInvalidParameters, err.Error(), map[string]any{"questId": questID})
				return
			}
			traceID := writeEnvelope(w, "quest", resumed)
			s.journalEvent("quest_resumed", "/wisdev/quests/"+questID+"/resume", traceID, resumed.SessionID, resumed.UserID, resumed.QuestID, "", "Research quest resumed.", map[string]any{"questId": questID}, nil)
		case "artifacts":
			if r.Method != http.MethodGet {
				WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{"allowedMethods": []string{http.MethodGet}})
				return
			}
			artifacts, err := runtime.GetArtifacts(r.Context(), questID)
			if err != nil {
				WriteError(w, http.StatusInternalServerError, ErrInternal, "failed to load quest artifacts", map[string]any{"error": err.Error(), "questId": questID})
				return
			}
			traceID := writeEnvelope(w, "artifacts", artifacts)
			s.journalEvent("quest_artifacts_loaded", "/wisdev/quests/"+questID+"/artifacts", traceID, quest.SessionID, quest.UserID, quest.QuestID, "", "Quest artifacts loaded.", map[string]any{"questId": questID}, nil)
		default:
			WriteError(w, http.StatusNotFound, ErrNotFound, "quest route not found", map[string]any{"questId": questID, "action": action})
		}
	})
}

func resolveQuestRuntime(agentGateway *wisdev.AgentGateway) *wisdev.ResearchQuestRuntime {
	if agentGateway == nil {
		return nil
	}
	resolveUnifiedResearchRuntime(agentGateway)
	if agentGateway.QuestRuntime == nil {
		agentGateway.QuestRuntime = wisdev.NewResearchQuestRuntime(agentGateway)
	}
	return agentGateway.QuestRuntime
}

func resolveUnifiedResearchRuntime(agentGateway *wisdev.AgentGateway) *wisdev.UnifiedResearchRuntime {
	if agentGateway == nil {
		return nil
	}
	if agentGateway.Runtime == nil {
		if agentGateway.Loop == nil && agentGateway.SearchRegistry != nil {
			agentGateway.Loop = wisdev.NewAutonomousLoop(agentGateway.SearchRegistry, agentGateway.LLMClient)
		}
		if agentGateway.Loop != nil {
			agentGateway.Runtime = wisdev.NewUnifiedResearchRuntime(
				agentGateway.Loop,
				agentGateway.SearchRegistry,
				agentGateway.LLMClient,
				agentGateway.ProgrammaticLoopExecutor(),
			).WithDurableResearchState(agentGateway.StateStore, agentGateway.Journal)
		}
	}
	return agentGateway.Runtime
}

func parseQuestRoute(path string) (questID string, action string, ok bool) {
	trimmed := strings.TrimPrefix(path, "/wisdev/quests/")
	if trimmed == path || strings.TrimSpace(trimmed) == "" {
		return "", "", false
	}
	parts := strings.Split(strings.Trim(trimmed, "/"), "/")
	if len(parts) != 2 {
		return "", "", false
	}
	questID = strings.TrimSpace(parts[0])
	action = strings.TrimSpace(parts[1])
	if questID == "" || action == "" {
		return "", "", false
	}
	return questID, action, true
}
