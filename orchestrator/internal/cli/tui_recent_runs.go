package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// maxRecentRuns caps the past-runs browser list.
const maxRecentRuns = 5

// recentRunEntry is one saved wisdev-result-*.json run in the working
// directory, ready to reopen in the results view.
type recentRunEntry struct {
	Path    string
	Query   string
	ModTime time.Time
}

// listRecentRunFiles returns up to max saved wisdev-result-*.json exports in
// dir, most recent first by mtime, with the saved query parsed for display.
// Unreadable or non-result JSON files are skipped.
func listRecentRunFiles(dir string, max int) []recentRunEntry {
	if max <= 0 {
		max = maxRecentRuns
	}
	if strings.TrimSpace(dir) == "" {
		dir = "."
	}
	matches, err := filepath.Glob(filepath.Join(dir, "wisdev-result-*.json"))
	if err != nil || len(matches) == 0 {
		return nil
	}
	candidates := make([]recentRunEntry, 0, len(matches))
	for _, path := range matches {
		info, statErr := os.Stat(path)
		if statErr != nil || info.IsDir() {
			continue
		}
		candidates = append(candidates, recentRunEntry{Path: path, ModTime: info.ModTime()})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].ModTime.After(candidates[j].ModTime)
	})
	entries := make([]recentRunEntry, 0, max)
	for _, candidate := range candidates {
		export, parseErr := parseRecentRunFile(candidate.Path)
		if parseErr != nil {
			continue
		}
		candidate.Query = strings.TrimSpace(export.Query)
		entries = append(entries, candidate)
		if len(entries) >= max {
			break
		}
	}
	if len(entries) == 0 {
		return nil
	}
	return entries
}

// parseRecentRunFile reads one saved JSON export back into the public result
// envelope. It rejects files without a result payload so the results view
// never opens empty.
func parseRecentRunFile(path string) (*tuiResultExport, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var export tuiResultExport
	if err := json.Unmarshal(raw, &export); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	if export.Result == nil {
		return nil, fmt.Errorf("%s has no result payload", filepath.Base(path))
	}
	return &export, nil
}

// recentRunLabel renders one list row: date + saved query (or filename).
func recentRunLabel(entry recentRunEntry, width int) string {
	label := entry.Query
	if label == "" {
		label = filepath.Base(entry.Path)
	}
	stamp := entry.ModTime.Format("2006-01-02 15:04")
	if width < 24 {
		width = 24
	}
	return fmt.Sprintf("[%s] %s", stamp, truncateVisible(label, width-22))
}

// openRecentRun loads a saved run into the results view. The results screen
// only reads completed-run state (result, elapsed, task), so a reopened run
// behaves like a just-finished one.
func (s *tuiState) openRecentRun(entry recentRunEntry) {
	export, err := parseRecentRunFile(entry.Path)
	if err != nil {
		s.setSaveMsg("Error: " + err.Error())
		return
	}
	s.result = export.Result
	s.runError = nil
	s.runningTask = strings.TrimSpace(export.Query)
	if s.runningTask == "" {
		s.runningTask = strings.TrimSpace(export.Result.OriginalQuery)
	}
	s.completedElapsed = time.Duration(export.ElapsedSec * float64(time.Second))
	s.prevResult = nil
	s.degradedSteps = 0
	s.mode = modeResults
	s.resultPane = resultPaneAll
	s.scrollOffset = 0
	s.paperDetailIdx = 0
	s.cachedResultLines = nil
	s.resultFilter = ""
	s.resultFilterOn = false
	s.resultFilterMatch = nil
	s.setSaveMsg("Loaded saved run " + filepath.Base(entry.Path))
}
