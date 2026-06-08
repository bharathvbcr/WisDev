package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/evidence"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

type wisdevServer struct {
	gateway *wisdev.AgentGateway
	rag     *RAGHandler
}

type steeringSignalRequest struct {
	SessionID string   `json:"sessionId"`
	Type      string   `json:"type"`
	Payload   string   `json:"payload"`
	Queries   []string `json:"queries"`
}

var steeringAdmission = newSteeringAdmissionLimiter(60, time.Minute)

type steeringAdmissionLimiter struct {
	mu       sync.Mutex
	limit    int
	window   time.Duration
	requests map[string][]time.Time
}

func newSteeringAdmissionLimiter(limit int, window time.Duration) *steeringAdmissionLimiter {
	return &steeringAdmissionLimiter{limit: limit, window: window, requests: make(map[string][]time.Time)}
}

func (s *wisdevServer) journalEvent(
	eventType string,
	path string,
	traceID string,
	sessionID string,
	userID string,
	planID string,
	stepID string,
	summary string,
	payload map[string]any,
	metadata map[string]any,
) {
	if s.gateway == nil || s.gateway.Journal == nil {
		return
	}
	s.gateway.Journal.Append(wisdev.RuntimeJournalEntry{
		EventID:   wisdev.NewTraceID(),
		TraceID:   traceID,
		SessionID: strings.TrimSpace(sessionID),
		UserID:    strings.TrimSpace(userID),
		PlanID:    strings.TrimSpace(planID),
		StepID:    strings.TrimSpace(stepID),
		EventType: eventType,
		Path:      path,
		Status:    "ok",
		CreatedAt: time.Now().UnixMilli(),
		Summary:   summary,
		Payload:   cloneAnyMap(payload),
		Metadata:  cloneAnyMap(metadata),
	})
}

func RegisterWisDevRoutes(mux *http.ServeMux, agentGateway *wisdev.AgentGateway, ragHandler *RAGHandler, paper2SkillHandler http.HandlerFunc) {
	s := &wisdevServer{
		gateway: agentGateway,
		rag:     ragHandler,
	}
	hypothesisHandler := &WisDevHandler{
		gateway: agentGateway,
		rag:     ragHandler,
	}

	agentID := "unknown"
	if agentGateway != nil && agentGateway.ADKRuntime != nil {
		agentID = agentGateway.ADKRuntime.Config.Runtime.AgentID
	}
	slog.Info("Registering WisDev routes", "agentId", agentID)

	if s.rag != nil {
		mux.HandleFunc("/wisdev/multi-agent", s.rag.HandleMultiAgent)
	}
	mux.HandleFunc("/wisdev/hypothesis/", hypothesisHandler.HandleWisDevHypotheses)

	s.registerSessionRoutes(mux, agentGateway)
	s.registerQuestioningRoutes(mux, agentGateway)
	s.registerPlanRoutes(mux, agentGateway)
	s.registerToolRoutes(mux, agentGateway)
	s.registerSearchRoutes(mux, agentGateway)
	s.registerRAGRoutes(mux, agentGateway)
	s.registerResearchRoutes(mux, agentGateway)
	s.registerObserveRoutes(mux, agentGateway)
	s.registerContractRoutes(mux, agentGateway)
	s.registerDraftingRoutes(mux, agentGateway)
	s.registerFullPaperRoutes(mux, agentGateway)
	s.registerHealthRoutes(mux, agentGateway)
	s.registerEvidenceRoutes(mux, agentGateway)
	s.registerQuestRoutes(mux, agentGateway)
	s.registerPolicyRoutes(mux, agentGateway)
	s.registerExtraRoutes(mux, agentGateway)
	s.registerSteeringRoutes(mux)
	registerWisDevJobRoutes(mux)

	if paper2SkillHandler != nil {
		mux.HandleFunc("/wisdev/paper2skill", paper2SkillHandler)
	}
}

func (s *wisdevServer) registerSteeringRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/wisdev/steering", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		if err := validateSteeringAdmission(r); err != nil {
			WriteError(w, steeringAdmissionHTTPStatus(err), ErrInvalidParameters, err.Error(), nil)
			return
		}
		var req steeringSignalRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{"error": err.Error()})
			return
		}
		resp, err := s.acceptSteeringSignal(r, req)
		if err != nil {
			WriteError(w, steeringSignalHTTPStatus(err), steeringSignalErrorCode(err), err.Error(), map[string]any{
				"allowedTypes": []string{"redirect", "focus", "exclude", "approve", "reject"},
			})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(resp)
	})

	upgrader := websocket.Upgrader{CheckOrigin: steeringWebSocketOriginAllowed}
	mux.HandleFunc("/wisdev/steering/ws", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodGet,
			})
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			slog.Warn("failed to upgrade WisDev steering websocket",
				"component", "wisdev.api",
				"operation", "steering.websocket",
				"error", err,
			)
			return
		}
		defer conn.Close()

		for {
			var req steeringSignalRequest
			if err := conn.ReadJSON(&req); err != nil {
				return
			}
			if err := validateSteeringAdmission(r); err != nil {
				_ = conn.WriteJSON(map[string]any{"status": "error", "error": err.Error()})
				continue
			}
			resp, err := s.acceptSteeringSignal(r, req)
			if err != nil {
				_ = conn.WriteJSON(map[string]any{"status": "error", "error": err.Error()})
				continue
			}
			if err := conn.WriteJSON(resp); err != nil {
				return
			}
		}
	})
}

func (s *wisdevServer) acceptSteeringSignal(r *http.Request, req steeringSignalRequest) (map[string]any, error) {
	signalType := strings.ToLower(strings.TrimSpace(req.Type))
	switch signalType {
	case "redirect", "focus", "exclude", "approve", "reject":
	default:
		return nil, fmt.Errorf("unsupported steering signal type")
	}
	sessionID := strings.TrimSpace(req.SessionID)
	userID, err := s.authorizeSteeringSignal(r, sessionID)
	if err != nil {
		return nil, err
	}
	signal := wisdev.SteeringSignal{
		Type:      signalType,
		Payload:   strings.TrimSpace(req.Payload),
		Queries:   normalizeStringSlice(req.Queries),
		Timestamp: time.Now().UnixMilli(),
	}
	delivered, err := wisdev.QueueSteeringSignal(sessionID, signal)
	if err != nil {
		return nil, err
	}
	delivery := "queued"
	if delivered {
		delivery = "delivered"
	}
	s.journalEvent(
		wisdev.EventWisDevSteeringSignal,
		"/wisdev/steering",
		"",
		sessionID,
		userID,
		"",
		"",
		"received WisDev steering signal",
		map[string]any{
			"type":      signal.Type,
			"payload":   signal.Payload,
			"queries":   signal.Queries,
			"timestamp": signal.Timestamp,
			"delivery":  delivery,
		},
		map[string]any{"delivery": delivery},
	)
	return map[string]any{
		"status":    "accepted",
		"sessionId": sessionID,
		"type":      signal.Type,
		"delivery":  delivery,
	}, nil
}

func (s *wisdevServer) authorizeSteeringSignal(r *http.Request, sessionID string) (string, error) {
	if strings.TrimSpace(sessionID) == "" {
		return "", fmt.Errorf("sessionId is required")
	}
	userID, err := resolveAuthorizedUserID(r, "")
	if err != nil {
		return "", err
	}
	if s.gateway == nil || s.gateway.StateStore == nil {
		return "", fmt.Errorf("steering session store is unavailable")
	}
	session, err := s.gateway.StateStore.LoadAgentSession(sessionID)
	if err != nil {
		return "", fmt.Errorf("session not found")
	}
	ownerID := strings.TrimSpace(wisdev.AsOptionalString(session["userId"]))
	if ownerID == "" {
		return "", fmt.Errorf("session owner unavailable")
	}
	if userID != ownerID && userID != "admin" && userID != "internal-service" {
		return "", fmt.Errorf("access denied")
	}
	return userID, nil
}

func steeringWebSocketOriginAllowed(r *http.Request) bool {
	return steeringOriginAllowed(r)
}

func validateSteeringAdmission(r *http.Request) error {
	if !steeringOriginAllowed(r) {
		return fmt.Errorf("steering origin is not allowed")
	}
	host := steeringClientHost(r)
	if host == "" {
		host = "unknown"
	}
	if !steeringAdmission.allow(host, time.Now()) {
		return fmt.Errorf("steering rate limit exceeded")
	}
	return nil
}

func steeringAdmissionHTTPStatus(err error) int {
	if err != nil && strings.Contains(err.Error(), "origin") {
		return http.StatusForbidden
	}
	return http.StatusTooManyRequests
}

func steeringSignalHTTPStatus(err error) int {
	if err == nil {
		return http.StatusOK
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "authentication"), strings.Contains(msg, "access denied"), strings.Contains(msg, "owner unavailable"):
		return http.StatusForbidden
	case strings.Contains(msg, "not found"):
		return http.StatusNotFound
	case strings.Contains(msg, "unavailable"):
		return http.StatusServiceUnavailable
	default:
		return http.StatusBadRequest
	}
}

func steeringSignalErrorCode(err error) ErrorCode {
	switch steeringSignalHTTPStatus(err) {
	case http.StatusForbidden:
		return ErrUnauthorized
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusServiceUnavailable:
		return ErrServiceUnavailable
	default:
		return ErrInvalidParameters
	}
}

func steeringOriginAllowed(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Host, r.Host)
}

func steeringClientHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func (l *steeringAdmissionLimiter) allow(key string, now time.Time) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-l.window)
	current := l.requests[key]
	kept := current[:0]
	for _, at := range current {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	if len(kept) >= l.limit {
		l.requests[key] = kept
		return false
	}
	kept = append(kept, now)
	l.requests[key] = kept
	return true
}

func registerWisDevJobRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/wisdev/job", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			WisDevJobHandler(w, r)
		default:
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
		}
	})

	mux.HandleFunc("/wisdev/job/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/stream") {
			WisDevStreamHandler(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/cancel") {
			WisDevJobCancelHandler(w, r)
			return
		}
		WisDevJobStatusHandler(w, r)
	})
}

func buildRagAnswerPayload(query string, domain string, papers []wisdev.Source) map[string]any {
	committee := buildMultiAgentCommitteeResult(query, domain, papers, 2, true)
	paperPayload := committee["papers"]
	citations := committee["citations"]
	answer := wisdev.AsOptionalString(committee["answer"])
	ragJobID := "rag_" + wisdev.NewTraceID()

	// Build evidence dossier for response payload. Keep the endpoint functional if this step fails.
	evidenceDossier, err := evidence.BuildDossier(ragJobID, query, convertWisdevSourcesToSearchPapers(papers))
	if err != nil {
		slog.Warn("failed to build evidence dossier", "query", query, "error", err)
	}

	rawCitations, _ := citations.([]map[string]any)
	claims := make([]map[string]any, 0, len(rawCitations))
	for _, citation := range rawCitations {
		claims = append(claims, map[string]any{
			"claim": citation["claim"],
			"source": map[string]any{
				"id":         citation["sourceId"],
				"title":      citation["sourceTitle"],
				"confidence": citation["confidence"],
			},
		})
	}
	verification := buildEvidenceGatePayload(claims, 0)
	return map[string]any{
		"query":     query,
		"answer":    answer,
		"papers":    paperPayload,
		"citations": citations,
		"committee": committee,
		"verificationSummary": map[string]any{
			"status":         verification["verdict"],
			"strictGatePass": verification["strictGatePass"],
			"citationBacked": len(rawCitations) > 0,
			"claimCount":     verification["claimCount"],
		},
		"evidenceBundle": map[string]any{
			"canonicalSources": paperPayload,
			"findings":         citations,
		},
		"evidenceDossier": map[string]any{
			"dossierId":        evidenceDossier.DossierID,
			"query":            evidenceDossier.Query,
			"canonicalSources": evidenceDossier.CanonicalSources,
			"verifiedClaims":   evidenceDossier.VerifiedClaims,
			"coverageMetrics":  evidenceDossier.CoverageMetrics,
			"createdAt":        evidenceDossier.CreatedAt,
			"updatedAt":        evidenceDossier.UpdatedAt,
		},
		"workerMetadata": map[string]any{
			"documentWorker": "python-docling",
			"sourceOfTruth":  "go-control-plane",
			"retrievalMode":  "committee_hybrid",
		},
	}
}

func convertWisdevSourcesToSearchPapers(sources []wisdev.Source) []search.Paper {
	if len(sources) == 0 {
		return []search.Paper{}
	}
	papers := make([]search.Paper, 0, len(sources))
	for _, source := range sources {
		papers = append(papers, search.Paper{
			ID:                       source.ID,
			Title:                    source.Title,
			Abstract:                 firstNonEmptyString(source.Abstract, source.Summary),
			Link:                     firstNonEmptyString(source.Link, source.URL),
			DOI:                      source.DOI,
			ArxivID:                  source.ArxivID,
			Source:                   source.Source,
			SourceApis:               append([]string(nil), source.SourceApis...),
			Authors:                  append([]string(nil), source.Authors...),
			Year:                     source.Year,
			Month:                    source.Month,
			Venue:                    firstNonEmptyString(source.Publication, source.SiteName),
			Keywords:                 append([]string(nil), source.Keywords...),
			CitationCount:            source.CitationCount,
			ReferenceCount:           source.ReferenceCount,
			InfluentialCitationCount: source.InfluentialCitationCount,
			OpenAccessUrl:            source.OpenAccessUrl,
			PdfUrl:                   source.PdfUrl,
			Score:                    source.Score,
			FullText:                 source.FullText,
			StructureMap:             append([]any(nil), source.StructureMap...),
		})
	}
	return papers
}
