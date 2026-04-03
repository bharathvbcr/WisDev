package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
	"strings"
)


func cloneAnyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

type queryIntroductionPaper struct {
	Title       string   `json:"title"`
	Summary     string   `json:"summary"`
	Abstract    string   `json:"abstract"`
	Authors     []string `json:"authors"`
	Year        int      `json:"year"`
	Publication string   `json:"publication"`
	SourceApis  []string `json:"sourceApis"`
}

type batchSummaryPaper struct {
	PaperID  string   `json:"paper_id"`
	Title    string   `json:"title"`
	Abstract string   `json:"abstract"`
	Authors  []string `json:"authors"`
	Year     int      `json:"year"`
}

func decodeStrictJSONBody(body io.Reader, target any) error {
	decoder := json.NewDecoder(body)
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("unexpected extra JSON content")
		}
		return err
	}
	return nil
}

func mapPythonErrorToHTTP(err error) (int, string) {
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "not found") {
		return http.StatusNotFound, "NOT_FOUND"
	}
	if strings.Contains(msg, "permission") || strings.Contains(msg, "unauthorized") {
		return http.StatusUnauthorized, "UNAUTHORIZED"
	}
	if strings.Contains(msg, "invalid") || strings.Contains(msg, "bad request") {
		return http.StatusBadRequest, "INVALID_REQUEST"
	}
	return http.StatusInternalServerError, "INTERNAL_ERROR"
}

func newTraceID() string {
	return wisdev.NewTraceID()
}
