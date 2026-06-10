package wisdev

import (
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"sync"
)

// PRM reward logging for calibration. Every process-reward decision (judge or
// heuristic) and every run outcome is appended as JSONL so the accumulated
// (step context, scores, outcome) triples can later train or calibrate a
// learned reward model. Set WISDEV_PRM_LOG_PATH to override the destination
// or "off" to disable.

const defaultPRMLogPath = "wisdev_prm_rewards.jsonl"

// PRMRewardRecord is one logged process-reward decision or run outcome.
type PRMRewardRecord struct {
	Timestamp  int64    `json:"timestamp"`
	Kind       string   `json:"kind"`   // "step" or "outcome"
	Source     string   `json:"source"` // "judge" or "heuristic"
	SessionID  string   `json:"sessionId,omitempty"`
	Query      string   `json:"query,omitempty"`
	Iteration  int      `json:"iteration,omitempty"`
	Queries    []string `json:"queries,omitempty"`
	PaperCount int      `json:"paperCount,omitempty"`
	Reward     float64  `json:"reward"`
	Coverage   float64  `json:"coverage,omitempty"`
}

var prmLogMu sync.Mutex

func prmLogPath() string {
	path := strings.TrimSpace(os.Getenv("WISDEV_PRM_LOG_PATH"))
	switch strings.ToLower(path) {
	case "off", "false", "0", "disabled":
		return ""
	case "":
		return defaultPRMLogPath
	}
	return path
}

// appendPRMRewardRecord appends one record to the PRM calibration log.
// Best-effort: logging failures never affect the research loop.
func appendPRMRewardRecord(record PRMRewardRecord) {
	path := prmLogPath()
	if path == "" {
		return
	}
	record.Timestamp = NowMillis()
	line, err := json.Marshal(record)
	if err != nil {
		return
	}

	prmLogMu.Lock()
	defer prmLogMu.Unlock()
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		slog.Debug("PRM reward log unavailable", "component", "wisdev.prm", "path", path, "error", err)
		return
	}
	defer fh.Close()
	_, _ = fh.Write(append(line, '\n'))
}
