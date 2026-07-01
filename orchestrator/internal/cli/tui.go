package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
	agent "github.com/bharathvbcr/wisdev-arc/orchestrator/pkg/wisdev"
	"golang.org/x/term"
)

const (
	maxLogEntries      = 100
	maxHistoryEntries  = 50
	activeElementCount = 6
	settingsPerRow     = 3
	minTermWidth       = 65
	minTermHeight      = 15
	maxFilterMsgLen    = 96
)

type tuiMode int

const (
	modeInput tuiMode = iota
	modeRunning
	modeResults
)

type tuiResultPane int

const (
	resultPaneAll tuiResultPane = iota
	resultPaneAnswer
	resultPaneHypotheses
	resultPaneQueries
	resultPaneSources
	resultPaneCompare
	resultPaneReasoning
)

type tuiProvider struct {
	name       string
	code       string
	enabled    bool
	lastStatus string
}

type tuiHistoryEntry struct {
	Query     string    `json:"query"`
	Timestamp time.Time `json:"timestamp"`
}

type tuiLogEntry struct {
	msg string
	tag string
}

type tuiState struct {
	mode                tuiMode
	query               string
	activeElement       int // 0 = query input, 1 = providers checklist, 2 = settings row, 3 = output path, 4 = "Start Research" button, 5 = "Exit" button
	providers           []tuiProvider
	providerIdx         int // currently highlighted provider in list
	validationMsg       string
	maxIterations       int
	requestedIterations int
	disablePlanning     bool
	enableQueryEnhance  bool
	enableHypotheses    bool
	deepSearch          bool
	longFormReport      bool
	generateDoc         bool
	offlineMode         bool
	originalQuery       string
	preparedQuery       string
	detectedDomain      string
	seedQueries         []string
	bypassSearchCache   bool
	llmBackend          string
	manuscriptPath      string // last manuscript written by the docGen toggle, for the results status line
	activeSetting       int    // 0=iterations 1=planning 2=offline 3=enhance 4=hypotheses 5=deep 6=longform 7=docgen
	outputPath          string
	showHelp            bool
	completedElapsed    time.Duration
	output              io.Writer
	terminalSize        func() (int, int, error)
	needsRender         bool

	// History state
	history            []tuiHistoryEntry
	historyIdx         int
	showHistoryBrowser bool
	historyBrowserIdx  int

	// Recent saved runs browser (input mode, Ctrl+O)
	showRecentRuns bool
	recentRuns     []recentRunEntry
	recentRunsIdx  int

	// Cursor state
	cursorPos           int
	outputPathCursorPos int
	queryUndoStack      []string
	queryRedoStack      []string

	// Exit confirm state
	pendingExit   bool
	pendingExitAt time.Time

	// Running state
	runningTask             string
	researchStartTime       time.Time
	elapsedTime             time.Duration
	iterations              int
	executedQueries         int
	papersFound             int
	degradedSteps           int
	autoDomainPresetApplied bool
	logs                    []tuiLogEntry
	logMutex                sync.Mutex
	cancelFunc              context.CancelFunc
	runError                error
	eventCh                 chan tuiEvent
	executedProviders       []string
	providerCounts          map[string]int
	logScrollOffset         int
	logScrollLocked         bool
	paused                  bool
	pauseCh                 chan struct{}
	resumeCh                chan struct{}

	// Results state
	result          *agent.YOLOResult
	resultPane      tuiResultPane
	scrollOffset    int
	saveMsg         string
	saveMsgAt       time.Time
	pendingSave     bool
	pendingSaveType string
	prevResult      *agent.YOLOResult
	beliefScores    []float64

	// Results filter state
	resultFilter       string
	resultFilterOn     bool
	resultFilterMatch  []int
	resultFilterCursor int

	// Citation jump state (results mode: [n] -> source paper)
	citationJumpOn    bool
	citationJumpInput string

	// Follow-up chat state (results mode: f -> grounded Q&A over current sources)
	chatOn             bool
	chatInput          string
	chatCursorPos      int
	chatHistory        []tuiChatMessage // guarded by logMutex
	chatBusy           bool             // guarded by logMutex
	chatGen            int              // guarded by logMutex; invalidates stale replies
	chatScrollOffset   int              // lines scrolled up from the transcript bottom
	keepPrevResultOnce bool

	// Provider filter state
	providerFilter    string
	providerFiltering bool

	// Paper details popup
	showPaperDetail bool
	paperDetailIdx  int

	// Session restore state
	showSessionRestorePrompt bool
	sessionQueryPreview      string

	// Batch Mode state
	batchMode     bool
	batchQueries  []string
	batchQueryIdx int

	// Suppression flag
	noBell bool

	// Terminal integration state (title / taskbar progress / chime)
	lastTermTitle     string
	lastTaskbarSeq    string
	bellWriter        io.Writer
	nativeTitle       bool
	disableTaskbarOSC bool

	// Cache rendered lines
	cachedResultLines  []string
	cachedResultPane   tuiResultPane
	cachedResultWidth  int
	cachedResultFilter string
	lastTerminalWidth  int
	lastTerminalHeight int
}

type tuiEventType int

const (
	eventKey tuiEventType = iota
	eventTick
	eventLog
	eventRunUpdate
	eventResize
	eventProviderHealth
)

type tuiEvent struct {
	eventType tuiEventType
	keyBytes  []byte
	// eventProviderHealth payload: result of one provider ping.
	providerCode   string
	providerStatus string
}

func defaultTUIProviders() []tuiProvider {
	registry := search.BuildRegistry()
	names := make([]string, 0, len(registry.All()))
	for _, provider := range registry.All() {
		names = append(names, provider.Name())
	}
	sort.Strings(names)

	providers := make([]tuiProvider, 0, len(names))
	for _, name := range names {
		enabled := name == "openalex" || name == "arxiv" || name == "semantic_scholar" || name == "pubmed"
		providers = append(providers, tuiProvider{
			name:    name,
			code:    name,
			enabled: enabled,
		})
	}
	if len(providers) == 0 {
		providers = []tuiProvider{
			{name: "OpenAlex", code: "openalex", enabled: true},
			{name: "arXiv", code: "arxiv", enabled: true},
			{name: "PubMed", code: "pubmed", enabled: false},
		}
	}
	return providers
}

func deleteWordLeft(text string, pos int) (string, int) {
	if pos <= 0 {
		return text, pos
	}
	i := pos - 1
	for i >= 0 && text[i] == ' ' {
		i--
	}
	for i >= 0 && text[i] != ' ' {
		i--
	}
	newPos := i + 1
	newText := text[:newPos] + text[pos:]
	return newText, newPos
}

func (s *tuiState) setSaveMsg(msg string) {
	s.saveMsg = msg
	if msg != "" {
		s.saveMsgAt = time.Now()
	} else {
		s.saveMsgAt = time.Time{}
	}
}

func parseLogScore(msg string) (float64, bool) {
	lower := strings.ToLower(msg)
	if !strings.Contains(lower, "score") && !strings.Contains(lower, "belief") && !strings.Contains(lower, "confidence") && !strings.Contains(lower, "hypothesis") {
		return 0, false
	}
	var current []byte
	foundFloat := false
	var floatVal float64
	for i := 0; i < len(msg); i++ {
		c := msg[i]
		if (c >= '0' && c <= '9') || c == '.' || c == '-' {
			current = append(current, c)
		} else {
			if len(current) > 0 {
				if val, err := strconv.ParseFloat(string(current), 64); err == nil {
					if val >= 0.0 && val <= 10.0 {
						floatVal = val
						foundFloat = true
					}
				}
				current = nil
			}
		}
	}
	if len(current) > 0 {
		if val, err := strconv.ParseFloat(string(current), 64); err == nil {
			if val >= 0.0 && val <= 10.0 {
				floatVal = val
				foundFloat = true
			}
		}
	}
	return floatVal, foundFloat
}

func renderSparkline(scores []float64) string {
	if len(scores) == 0 {
		return ""
	}
	blocks := []rune{' ', '▂', '▃', '▄', '▅', '▆', '▇', '█'}
	min := scores[0]
	max := scores[0]
	for _, v := range scores {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	var sb strings.Builder
	for _, v := range scores {
		if max == min {
			sb.WriteRune(blocks[3])
			continue
		}
		ratio := (v - min) / (max - min)
		idx := int(ratio * float64(len(blocks)-1))
		if idx < 0 {
			idx = 0
		}
		if idx >= len(blocks) {
			idx = len(blocks) - 1
		}
		sb.WriteRune(blocks[idx])
	}
	return sb.String()
}

func (s *tuiState) saveQueryUndoState() {
	if len(s.queryUndoStack) > 0 && s.queryUndoStack[len(s.queryUndoStack)-1] == s.query {
		return
	}
	s.queryUndoStack = append(s.queryUndoStack, s.query)
	if len(s.queryUndoStack) > 20 {
		s.queryUndoStack = s.queryUndoStack[1:]
	}
	s.queryRedoStack = nil
}

func (s *tuiState) performUndo() {
	if len(s.queryUndoStack) == 0 {
		return
	}
	s.queryRedoStack = append(s.queryRedoStack, s.query)
	if len(s.queryRedoStack) > 20 {
		s.queryRedoStack = s.queryRedoStack[1:]
	}
	lastIdx := len(s.queryUndoStack) - 1
	prev := s.queryUndoStack[lastIdx]
	s.queryUndoStack = s.queryUndoStack[:lastIdx]
	s.query = prev
	s.cursorPos = len(s.query)
}

func (s *tuiState) performRedo() {
	if len(s.queryRedoStack) == 0 {
		return
	}
	s.queryUndoStack = append(s.queryUndoStack, s.query)
	if len(s.queryUndoStack) > 20 {
		s.queryUndoStack = s.queryUndoStack[1:]
	}
	lastIdx := len(s.queryRedoStack) - 1
	next := s.queryRedoStack[lastIdx]
	s.queryRedoStack = s.queryRedoStack[:lastIdx]
	s.query = next
	s.cursorPos = len(s.query)
}

func (s *tuiState) toggleActiveSetting() {
	switch s.activeSetting {
	case 1:
		s.disablePlanning = !s.disablePlanning
	case 2:
		s.offlineMode = !s.offlineMode
		if s.offlineMode {
			for idx := range s.providers {
				s.providers[idx].enabled = false
			}
		} else {
			if s.enabledProviderCount() == 0 {
				s.providers = defaultTUIProviders()
			}
		}
	case 3:
		s.enableQueryEnhance = !s.enableQueryEnhance
	case 4:
		s.enableHypotheses = !s.enableHypotheses
	case 5:
		s.deepSearch = !s.deepSearch
	case 6:
		s.longFormReport = !s.longFormReport
	case 7:
		s.generateDoc = !s.generateDoc
	}
}

// manuscriptOutputPath picks where the docGen toggle writes the generated
// manuscript. When the user set an export output path it places the manuscript
// beside it with a "-manuscript.md" suffix; otherwise it falls back to a
// timestamped file in the working directory.
func (s *tuiState) manuscriptOutputPath() string {
	base := strings.TrimSpace(s.outputPath)
	if base == "" {
		return fmt.Sprintf("wisdev-manuscript-%d.md", time.Now().UnixMilli())
	}
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	return stem + "-manuscript.md"
}

// composeFollowUpQuery builds the task for a follow-up run: the new question
// carries the previous research question as context so query preparation and
// planning stay anchored to the original topic.
func composeFollowUpQuery(prevQuery, followUp string) string {
	prevQuery = strings.TrimSpace(prevQuery)
	followUp = strings.TrimSpace(followUp)
	if followUp == "" {
		return prevQuery
	}
	if prevQuery == "" || strings.EqualFold(prevQuery, followUp) {
		return followUp
	}
	return fmt.Sprintf("%s (follow-up to: %s)", followUp, prevQuery)
}

// startFollowUpResearch launches a new run for a follow-up question typed in
// results mode, keeping the previous result so the Compare pane shows what the
// follow-up changed.
func (s *tuiState) startFollowUpResearch(ctx context.Context, followUp string) {
	followUp = strings.TrimSpace(followUp)
	if followUp == "" {
		return
	}
	s.savePreviousResult()
	s.keepPrevResultOnce = true
	s.saveQueryUndoState()
	s.query = composeFollowUpQuery(s.currentResultQuery(), followUp)
	s.cursorPos = len(s.query)
	s.setSaveMsg("")
	s.startResearch(ctx)
}

func (s *tuiState) getResultLines(width int) []string {
	if s.cachedResultLines != nil && s.cachedResultPane == s.resultPane && s.cachedResultWidth == width && s.cachedResultFilter == s.resultFilter {
		return s.cachedResultLines
	}
	lines := buildTUIResultLines(s, width, s.resultPane)
	s.cachedResultLines = lines
	s.cachedResultPane = s.resultPane
	s.cachedResultWidth = width
	s.cachedResultFilter = s.resultFilter
	return lines
}

func (s *tuiState) availableResultPanes() []tuiResultPane {
	panes := []tuiResultPane{resultPaneAll, resultPaneAnswer, resultPaneHypotheses, resultPaneQueries, resultPaneSources}
	if s.result != nil {
		panes = append(panes, resultPaneCompare)
		panes = append(panes, resultPaneReasoning)
	}
	return panes
}

func (s *tuiState) cycleResultPane(delta int) {
	panes := s.availableResultPanes()
	for i, pane := range panes {
		if pane == s.resultPane {
			s.resultPane = panes[(i+delta+len(panes))%len(panes)]
			s.scrollOffset = 0
			s.cachedResultLines = nil
			if s.resultPane == resultPaneSources {
				s.scrollSelectedPaperIntoView()
			}
			return
		}
	}
}

func (s *tuiState) savePreviousResult() {
	if s.result == nil {
		return
	}
	copy := *s.result
	s.prevResult = &copy
}

func (s *tuiState) resultsHome() {
	s.resultPane = resultPaneAll
	s.scrollOffset = 0
	s.paperDetailIdx = 0
	s.cachedResultLines = nil
}

func (s *tuiState) resultsFooterShortcut() string {
	return "Tab/[ ] panes  j/k scroll  h=home  v=reasoning  y=hypotheses  o=open [n]  f=follow-up  /=filter  s/e/b/t/w export  c=copy  r/E re-run  ?=help"
}

func (s *tuiState) runningFooterShortcut() string {
	return "ESC=abort  P=pause  k/↑=older log  j/↓=newer log  ?=help"
}

// runningLogViewport returns how many activity-log rows fit on the running
// screen for the given terminal height, counting every chrome row the frame
// actually draws (including the optional domain/enhanced/degraded/sparkline
// rows). Undercounting chrome makes the frame taller than the terminal and
// the overflow scrolls the alternate screen, clipping the top border — most
// visible on macOS Terminal.app's default 80x24 window.
// inputElementIsTextField reports whether the focused element in modeInput is a
// free-text field — the query (0) or the output path (3) — where printable keys
// must be inserted as characters rather than consumed as single-letter
// shortcuts (e.g. 'h'/'?'), so the user can type words like "how" or end a
// research question with "?".
func (s *tuiState) inputElementIsTextField() bool {
	return s.activeElement == 0 || s.activeElement == 3
}

// focusedTextFieldEmpty reports whether the focused modeInput text field is
// still empty. It lets '?' open help on a fresh screen while still typing a
// literal '?' once the user has started writing (so a question can end in '?').
func (s *tuiState) focusedTextFieldEmpty() bool {
	switch s.activeElement {
	case 0:
		return s.query == ""
	case 3:
		return s.outputPath == ""
	}
	return false
}

func (s *tuiState) runningLogViewport(height int) int {
	// border, query, phase, progress, papers, divider, log header, hint,
	// footer border, brand bar, status bar
	chrome := 11
	if strings.TrimSpace(s.detectedDomain) != "" {
		chrome++
	}
	if s.preparedQuery != "" && s.preparedQuery != s.originalQuery {
		chrome++
	}
	if s.degradedSteps > 0 {
		chrome++
	}
	if len(s.beliefScores) > 0 {
		chrome++
	}
	viewport := height - chrome
	if viewport < 1 {
		viewport = 1
	}
	return viewport
}

func (s *tuiState) clampLogScrollOffset() {
	_, height, err := s.currentTerminalSize()
	if err != nil || height <= 0 {
		height = 24
	}
	viewport := s.runningLogViewport(height)
	s.logMutex.Lock()
	logCount := len(s.logs)
	s.logMutex.Unlock()
	maxOffset := logCount - viewport
	if maxOffset < 0 {
		maxOffset = 0
	}
	if s.logScrollOffset > maxOffset {
		s.logScrollOffset = maxOffset
	}
	if s.logScrollOffset < 0 {
		s.logScrollOffset = 0
	}
}

func (s *tuiState) batchProgressLabel() string {
	if !s.batchMode || len(s.batchQueries) == 0 {
		return ""
	}
	return fmt.Sprintf("Batch %d/%d", s.batchQueryIdx+1, len(s.batchQueries))
}

func (s *tuiState) saveSession() {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".wisdev")
	_ = os.MkdirAll(dir, 0755)

	var provs []tuiSessionProv
	for _, p := range s.providers {
		provs = append(provs, tuiSessionProv{Code: p.code, Enabled: p.enabled})
	}

	sess := tuiSession{
		Query:              s.query,
		Providers:          provs,
		MaxIterations:      s.maxIterations,
		DisablePlanning:    s.disablePlanning,
		OfflineMode:        s.offlineMode,
		EnableQueryEnhance: s.enableQueryEnhance,
		EnableHypotheses:   s.enableHypotheses,
		DeepSearch:         s.deepSearch,
		LongFormReport:     s.longFormReport,
		GenerateDoc:        s.generateDoc,
	}

	data, err := json.Marshal(sess)
	if err == nil {
		_ = os.WriteFile(sessionFilePath(), data, 0644)
	}
}

func (s *tuiState) clearSession() {
	_ = os.Remove(sessionFilePath())
}

func (s *tuiState) checkSessionRestore() {
	data, err := os.ReadFile(sessionFilePath())
	if err != nil {
		return
	}
	var sess tuiSession
	if err := json.Unmarshal(data, &sess); err == nil && sess.Query != "" {
		s.showSessionRestorePrompt = true
		s.sessionQueryPreview = sess.Query
	}
}

func (s *tuiState) restoreSession() {
	data, err := os.ReadFile(sessionFilePath())
	if err != nil {
		return
	}
	var sess tuiSession
	if err := json.Unmarshal(data, &sess); err == nil {
		s.query = sess.Query
		s.cursorPos = len(s.query)
		s.maxIterations = sess.MaxIterations
		s.disablePlanning = sess.DisablePlanning
		s.offlineMode = sess.OfflineMode
		s.enableQueryEnhance = sess.EnableQueryEnhance
		s.enableHypotheses = sess.EnableHypotheses
		s.deepSearch = sess.DeepSearch
		s.longFormReport = sess.LongFormReport
		s.generateDoc = sess.GenerateDoc

		for _, sp := range sess.Providers {
			for idx, p := range s.providers {
				if p.code == sp.Code {
					s.providers[idx].enabled = sp.Enabled
					break
				}
			}
		}
	}
}

type tuiSession struct {
	Query              string           `json:"query"`
	Providers          []tuiSessionProv `json:"providers"`
	MaxIterations      int              `json:"max_iterations"`
	DisablePlanning    bool             `json:"disable_planning"`
	OfflineMode        bool             `json:"offline_mode"`
	EnableQueryEnhance bool             `json:"enable_query_enhance"`
	EnableHypotheses   bool             `json:"enable_hypotheses"`
	DeepSearch         bool             `json:"deep_search"`
	LongFormReport     bool             `json:"long_form_report"`
	GenerateDoc        bool             `json:"generate_doc"`
}

type tuiSessionProv struct {
	Code    string `json:"code"`
	Enabled bool   `json:"enabled"`
}

func (s *tuiState) checkProvidersHealth() {
	endpoints := map[string]string{
		"arxiv":            "https://export.arxiv.org/api/query?search_query=all:test&max_results=1",
		"crossref":         "https://api.crossref.org/works?rows=1",
		"pubmed":           "https://eutils.ncbi.nlm.nih.gov/entrez/eutils/esearch.fcgi?db=pubmed&term=test&retmax=1",
		"openalex":         "https://api.openalex.org/works?per_page=1",
		"biorxiv":          "https://api.biorxiv.org/details/biorxiv/2026-01-01/2026-01-02/0",
		"medrxiv":          "https://api.biorxiv.org/details/medrxiv/2026-01-01/2026-01-02/0",
		"clinical_trials":  "https://clinicaltrials.gov/api/v2/studies?pageSize=1",
		"doaj":             "https://doaj.org/api/v2/search/articles/test?pageSize=1",
		"semantic_scholar": "https://api.semanticscholar.org/graph/v1/paper/search?query=test&limit=1",
		"europe_pmc":       "https://www.ebi.ac.uk/europepmc/webservices/rest/search?query=test&pageSize=1",
	}

	for idx := range s.providers {
		p := &s.providers[idx]
		url, ok := endpoints[p.code]
		if !ok {
			p.lastStatus = "grey"
			continue
		}

		go func(prov *tuiProvider, pingUrl string) {
			client := &http.Client{
				Timeout: 1500 * time.Millisecond,
			}
			start := time.Now()
			resp, err := client.Get(pingUrl)
			duration := time.Since(start)

			status := "green"
			if err != nil {
				status = "red"
			} else {
				_ = resp.Body.Close()
				if duration > 800*time.Millisecond {
					status = "yellow"
				}
			}
			// Deliver the result through the event loop instead of writing
			// prov.lastStatus from this goroutine: render() reads that field
			// concurrently, so the direct write was a data race.
			select {
			case s.eventCh <- tuiEvent{eventType: eventProviderHealth, providerCode: prov.code, providerStatus: status}:
			default:
			}
		}(p, url)
	}
}

func (s *tuiState) saveResultsCSV() {
	savedPath, err := saveTUIResultCSV(s.outputPath, s.runningTask, s.result)
	if err != nil {
		s.setSaveMsg("Error: " + err.Error())
		return
	}
	s.setSaveMsg("Saved CSV " + savedPath)
}

func (s *tuiState) copyPaperBibTeXToClipboard() {
	if s.result == nil || s.paperDetailIdx < 0 || s.paperDetailIdx >= len(s.result.Papers) {
		return
	}
	paper := s.result.Papers[s.paperDetailIdx]
	content := formatPaperBibTeX(paper, s.paperDetailIdx)
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	fmt.Printf("\033]52;c;%s\007", encoded)
	s.setSaveMsg("Copied BibTeX for selected paper.")
}

// jumpToCitation resolves the typed citation number against the answer's
// bibliography section and opens the matching paper's detail popup in the
// Sources pane. Unknown numbers surface a status message instead of failing.
func (s *tuiState) jumpToCitation() {
	input := strings.TrimSpace(s.citationJumpInput)
	if input == "" {
		return
	}
	number, err := strconv.Atoi(input)
	if err != nil || number <= 0 {
		s.setSaveMsg(fmt.Sprintf("Invalid citation number %q.", input))
		return
	}
	if s.result == nil || len(s.result.Papers) == 0 {
		s.setSaveMsg("No sources available for citation jump.")
		return
	}
	mapping := buildCitationPaperMap(s.result)
	paperIdx, ok := mapping[number]
	if !ok {
		// Fallback: bibliography numbering follows the sorted source list, so
		// [n] usually equals Papers[n-1] when title matching fails.
		if len(mapping) == 0 && number <= len(s.result.Papers) {
			paperIdx = number - 1
		} else {
			s.setSaveMsg(fmt.Sprintf("No source matched citation [%d].", number))
			return
		}
	}
	if paperIdx < 0 || paperIdx >= len(s.result.Papers) {
		s.setSaveMsg(fmt.Sprintf("No source matched citation [%d].", number))
		return
	}
	s.resultPane = resultPaneSources
	s.paperDetailIdx = paperIdx
	s.cachedResultLines = nil
	s.scrollSelectedPaperIntoView()
	s.showPaperDetail = true
	s.setSaveMsg(fmt.Sprintf("Citation [%d] -> %s", number, truncateVisible(strings.TrimSpace(s.result.Papers[paperIdx].Title), 60)))
}

func fuzzyMatchScore(s, pattern string) int {
	idxs := fuzzyMatchIndexes(s, pattern)
	if len(idxs) == 0 {
		return -1
	}
	spread := idxs[len(idxs)-1] - idxs[0]
	return spread*1000 + idxs[0]
}

func (s *tuiState) updateResultFilterMatches() {
	if s.resultFilter == "" {
		s.resultFilterMatch = nil
		s.resultFilterCursor = 0
		return
	}
	width, _, err := s.currentTerminalSize()
	if err != nil || width <= 0 {
		width = 80
	}
	lines := s.getResultLines(width)

	type scoredMatch struct {
		idx   int
		score int
	}
	var matches []scoredMatch
	q := strings.ToLower(s.resultFilter)
	for idx, line := range lines {
		plain := strings.ToLower(removeEscapeSequences(line))
		if score := fuzzyMatchScore(plain, q); score >= 0 {
			matches = append(matches, scoredMatch{idx: idx, score: score})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score == matches[j].score {
			return matches[i].idx < matches[j].idx
		}
		return matches[i].score < matches[j].score
	})
	s.resultFilterMatch = make([]int, 0, len(matches))
	for _, m := range matches {
		s.resultFilterMatch = append(s.resultFilterMatch, m.idx)
	}
	if len(matches) > 0 {
		if s.resultFilterCursor >= len(matches) {
			s.resultFilterCursor = 0
		}
		s.scrollOffset = s.resultFilterMatch[s.resultFilterCursor]
	} else {
		s.resultFilterCursor = 0
	}
}

func fuzzyMatch(s, pattern string) bool {
	return len(fuzzyMatchIndexes(s, pattern)) > 0 || pattern == ""
}

func fuzzyMatchIndexes(s, pattern string) []int {
	if pattern == "" {
		return nil
	}
	s = strings.ToLower(s)
	pattern = strings.ToLower(pattern)
	idxs := make([]int, 0, len(pattern))
	pIdx := 0
	for i := 0; i < len(s) && pIdx < len(pattern); i++ {
		if s[i] == pattern[pIdx] {
			idxs = append(idxs, i)
			pIdx++
		}
	}
	if pIdx != len(pattern) {
		return nil
	}
	return idxs
}

func highlightFuzzyMatch(line, pattern string) string {
	plain := removeEscapeSequences(line)
	idxs := fuzzyMatchIndexes(plain, pattern)
	if len(idxs) == 0 {
		return line
	}
	highlight := activeTheme().Highlight
	matched := make(map[int]struct{}, len(idxs))
	for _, i := range idxs {
		matched[i] = struct{}{}
	}
	var b strings.Builder
	for i := 0; i < len(plain); i++ {
		if _, ok := matched[i]; ok {
			b.WriteString(highlight)
			b.WriteByte(plain[i])
			b.WriteString(ansiReset)
		} else {
			b.WriteByte(plain[i])
		}
	}
	return b.String()
}

func moveWordRight(s string, pos int) int {
	if pos >= len(s) {
		return len(s)
	}
	i := pos
	for i < len(s) && (s[i] == ' ' || isPunct(s[i])) {
		i++
	}
	for i < len(s) && s[i] != ' ' && !isPunct(s[i]) {
		i++
	}
	return i
}

func moveWordLeft(s string, pos int) int {
	if pos <= 0 {
		return 0
	}
	i := pos - 1
	for i >= 0 && (s[i] == ' ' || isPunct(s[i])) {
		i--
	}
	for i >= 0 && s[i] != ' ' && !isPunct(s[i]) {
		i--
	}
	return i + 1
}

func isPunct(c byte) bool {
	return (c >= 33 && c <= 47) || (c >= 58 && c <= 64) || (c >= 91 && c <= 96) || (c >= 123 && c <= 126)
}

func providerTypeIcon(code string) string {
	switch code {
	case "arxiv", "biorxiv", "medrxiv":
		return "P"
	case "pubmed", "europe_pmc", "clinical_trials":
		return "M"
	default:
		return "G"
	}
}

func providerHealthDot(status string) string {
	theme := activeTheme()
	switch status {
	case "green":
		return theme.HealthOK + "+" + ansiReset
	case "yellow":
		return theme.HealthWarn + "!" + ansiReset
	case "red":
		return theme.HealthBad + "x" + ansiReset
	default:
		return theme.HealthUnknown + "-" + ansiReset
	}
}

func (s *tuiState) dynamicFooterShortcut() string {
	switch s.activeElement {
	case 0:
		return "Enter=run  Tab=next  Ctrl+P/N/↑↓=history  Ctrl+O=recent runs  Ctrl+Z/Y=undo/redo  ?=help"
	case 1:
		return "Space=toggle  a=all  presets:b/c/p/g/x  /=filter  Tab=next  ?=help"
	case 2:
		return "←→=fields  ↑↓=rows  Space=toggle  1-9/+-=max iter  Tab=next  ?=help"
	case 3:
		return "Tab=next  Ctrl+Left/Right=jump word  ?=help"
	default:
		return "Enter=select  Tab=next  ?=help"
	}
}

func (s *tuiState) saveBatchResults() {
	base := s.outputPath
	if base == "" {
		base = "result.md"
	}
	ext := filepath.Ext(base)
	prefix := strings.TrimSuffix(base, ext)
	numberedPath := fmt.Sprintf("%s_%d%s", prefix, s.batchQueryIdx+1, ext)

	oldPath := s.outputPath
	s.outputPath = numberedPath
	s.saveResults()
	s.outputPath = oldPath
}

// insertIntoActiveTextField inserts text (typically a paste) into whichever
// free-text field currently has focus, returning true if it was consumed.
// Numeric-only fields (citation jump) and non-text screens are skipped, so a
// stray paste there is swallowed rather than mis-typed.
func (s *tuiState) insertIntoActiveTextField(text string) bool {
	if text == "" {
		return false
	}
	switch {
	case s.mode == modeResults && s.chatOn:
		if s.chatCursorPos < 0 {
			s.chatCursorPos = 0
		}
		if s.chatCursorPos > len(s.chatInput) {
			s.chatCursorPos = len(s.chatInput)
		}
		s.chatInput = s.chatInput[:s.chatCursorPos] + text + s.chatInput[s.chatCursorPos:]
		s.chatCursorPos += len(text)
		return true
	case s.mode == modeResults && s.resultFilterOn:
		s.resultFilter += text
		s.updateResultFilterMatches()
		s.cachedResultLines = nil
		return true
	case s.mode == modeInput && s.providerFiltering:
		s.providerFilter += text
		s.providerIdx = 0
		s.clampProviderIdx()
		return true
	case s.mode == modeInput && s.activeElement == 0:
		s.validationMsg = ""
		s.insertQueryChar(text)
		return true
	case s.mode == modeInput && s.activeElement == 3:
		s.insertOutputPathChar(text)
		return true
	}
	return false
}

func (s *tuiState) insertQueryChar(str string) {
	s.saveQueryUndoState()
	if s.cursorPos < 0 {
		s.cursorPos = 0
	}
	if s.cursorPos > len(s.query) {
		s.cursorPos = len(s.query)
	}
	s.query = s.query[:s.cursorPos] + str + s.query[s.cursorPos:]
	s.cursorPos += len(str)
}

func (s *tuiState) deleteQueryChar() {
	s.saveQueryUndoState()
	if s.cursorPos >= 0 && s.cursorPos < len(s.query) {
		next := nextCharBoundary(s.query, s.cursorPos)
		s.query = s.query[:s.cursorPos] + s.query[next:]
	}
}

func (s *tuiState) backspaceQueryChar() {
	s.saveQueryUndoState()
	if s.cursorPos > 0 && s.cursorPos <= len(s.query) {
		prev := prevCharBoundary(s.query, s.cursorPos)
		s.query = s.query[:prev] + s.query[s.cursorPos:]
		s.cursorPos = prev
	}
}

func (s *tuiState) insertOutputPathChar(str string) {
	if s.outputPathCursorPos < 0 {
		s.outputPathCursorPos = 0
	}
	if s.outputPathCursorPos > len(s.outputPath) {
		s.outputPathCursorPos = len(s.outputPath)
	}
	s.outputPath = s.outputPath[:s.outputPathCursorPos] + str + s.outputPath[s.outputPathCursorPos:]
	s.outputPathCursorPos += len(str)
}

func (s *tuiState) deleteOutputPathChar() {
	if s.outputPathCursorPos >= 0 && s.outputPathCursorPos < len(s.outputPath) {
		next := nextCharBoundary(s.outputPath, s.outputPathCursorPos)
		s.outputPath = s.outputPath[:s.outputPathCursorPos] + s.outputPath[next:]
	}
}

func (s *tuiState) backspaceOutputPathChar() {
	if s.outputPathCursorPos > 0 && s.outputPathCursorPos <= len(s.outputPath) {
		prev := prevCharBoundary(s.outputPath, s.outputPathCursorPos)
		s.outputPath = s.outputPath[:prev] + s.outputPath[s.outputPathCursorPos:]
		s.outputPathCursorPos = prev
	}
}

func (s *tuiState) loadHistory() {
	path := historyFilePath()
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()

	var tempHistory []tuiHistoryEntry
	dec := json.NewDecoder(file)
	for dec.More() {
		var entry tuiHistoryEntry
		if err := dec.Decode(&entry); err == nil {
			if strings.TrimSpace(entry.Query) != "" {
				tempHistory = append(tempHistory, entry)
			}
		}
	}
	if len(tempHistory) > maxHistoryEntries {
		tempHistory = tempHistory[len(tempHistory)-maxHistoryEntries:]
	}
	s.history = tempHistory
	s.historyIdx = len(s.history)
}

func (s *tuiState) appendHistory(query string) {
	query = strings.TrimSpace(query)
	if query == "" {
		return
	}
	if len(s.history) > 0 && s.history[len(s.history)-1].Query == query {
		return
	}

	entry := tuiHistoryEntry{
		Query:     query,
		Timestamp: time.Now(),
	}
	s.history = append(s.history, entry)
	s.historyIdx = len(s.history)

	path := historyFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer file.Close()

	enc := json.NewEncoder(file)
	_ = enc.Encode(entry)
}

func (s *tuiState) resolvedSavePath(saveType string) string {
	target := strings.TrimSpace(s.outputPath)
	if saveType == "bib" {
		if target == "" {
			target = defaultTUIResultFile(s.runningTask, "bib")
		} else {
			ext := strings.ToLower(filepath.Ext(target))
			switch ext {
			case ".md", ".json", ".csv":
				target = strings.TrimSuffix(target, ext) + ".bib"
			case "":
				target += ".bib"
			}
		}
	} else if saveType == "json" {
		if target == "" {
			target = defaultTUIResultFile(s.runningTask, "json")
		} else if strings.HasSuffix(strings.ToLower(target), ".md") {
			target = strings.TrimSuffix(target, filepath.Ext(target)) + ".json"
		}
	} else if saveType == "csv" {
		if target == "" {
			target = defaultTUIResultFile(s.runningTask, "csv")
		} else {
			ext := strings.ToLower(filepath.Ext(target))
			switch ext {
			case ".md", ".json", ".bib":
				target = strings.TrimSuffix(target, ext) + ".csv"
			case "":
				target += ".csv"
			}
		}
	} else if saveType == "html" {
		if target == "" {
			target = defaultTUIResultFile(s.runningTask, "html")
		} else {
			ext := strings.ToLower(filepath.Ext(target))
			switch ext {
			case ".md", ".json", ".bib", ".csv":
				target = strings.TrimSuffix(target, ext) + ".html"
			case "":
				target += ".html"
			}
		}
	} else {
		if target == "" {
			target = defaultTUIResultFile(s.runningTask, "md")
		}
	}
	return target
}

func (s *tuiState) matchingProviders() []tuiProvider {
	if !s.providerFiltering || s.providerFilter == "" {
		return s.providers
	}
	var res []tuiProvider
	filter := strings.ToLower(s.providerFilter)
	for _, p := range s.providers {
		if strings.Contains(strings.ToLower(p.name), filter) || strings.Contains(strings.ToLower(providerDisplayName(p.name)), filter) {
			res = append(res, p)
		}
	}
	return res
}

func (s *tuiState) addLog(msg string, tag string) {
	s.logMutex.Lock()
	s.logs = append(s.logs, tuiLogEntry{msg: msg, tag: tag})
	if len(s.logs) > maxLogEntries {
		s.logs = s.logs[len(s.logs)-maxLogEntries:]
	}
	lower := strings.ToLower(msg)
	for _, p := range s.providers {
		disp := providerDisplayName(p.name)
		if strings.Contains(lower, strings.ToLower(p.name)) || strings.Contains(lower, strings.ToLower(disp)) {
			found := false
			for _, ep := range s.executedProviders {
				if ep == disp {
					found = true
					break
				}
			}
			if !found {
				s.executedProviders = append(s.executedProviders, disp)
			}
		}
	}
	s.logMutex.Unlock()
	select {
	case s.eventCh <- tuiEvent{eventType: eventLog}:
	default:
	}
}

type tuiLogHandler struct {
	state *tuiState
}

func (h *tuiLogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return true
}

func (h *tuiLogHandler) Handle(ctx context.Context, r slog.Record) error {
	msg := r.Message
	var step, reason, stage, component, operation string
	var extraAttrs []slog.Attr

	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "step":
			step = a.Value.String()
		case "reason":
			reason = a.Value.String()
		case "stage":
			stage = a.Value.String()
		case "component":
			component = a.Value.String()
		case "operation":
			operation = a.Value.String()
		case "index", "iteration":
			h.state.logMutex.Lock()
			if val, ok := a.Value.Any().(int64); ok {
				h.state.iterations = int(val)
			} else if val, ok := a.Value.Any().(int); ok {
				h.state.iterations = val
			}
			h.state.logMutex.Unlock()
		case "total_unique_count":
			h.state.logMutex.Lock()
			if val, ok := a.Value.Any().(int64); ok {
				h.state.papersFound = int(val)
			} else if val, ok := a.Value.Any().(int); ok {
				h.state.papersFound = val
			}
			h.state.logMutex.Unlock()
		case "queryCount":
			h.state.logMutex.Lock()
			if val, ok := a.Value.Any().(int64); ok {
				h.state.executedQueries += int(val)
			} else if val, ok := a.Value.Any().(int); ok {
				h.state.executedQueries += val
			}
			h.state.logMutex.Unlock()
		default:
			extraAttrs = append(extraAttrs, a)
		}
		return true
	})

	var attrs []string
	if strings.HasPrefix(component, "wisdev.") {
		for _, a := range extraAttrs {
			if shouldIncludeAutonomousLogAttr(a.Key) {
				attrs = append(attrs, fmt.Sprintf("%s=%v", a.Key, a.Value.Any()))
			}
		}
	}

	levelName := r.Level.String()
	degraded := slogRecordDegraded(map[string]any{
		"stage":   stage,
		"message": msg,
	})
	showAutonomous := strings.HasPrefix(component, "wisdev.") && r.Level >= slog.LevelWarn

	var formatted string
	var tag string
	if showAutonomous {
		formatted, tag = formatAutonomousSlogRecord(levelName, stage, operation, msg, attrs, reason, degraded)
	} else {
		if step != "" {
			formatted = fmt.Sprintf("[%s] %s", step, msg)
		} else {
			formatted = msg
		}
		if reason != "" {
			formatted += " — " + reason
		}
		tag = "I"
		switch r.Level {
		case slog.LevelError:
			tag = "E"
		case slog.LevelWarn:
			tag = "W"
		default:
			lower := strings.ToLower(formatted)
			if strings.Contains(lower, "error") || strings.Contains(lower, "fail") {
				tag = "E"
			} else if strings.Contains(lower, "warning") || strings.Contains(lower, "warn") {
				tag = "W"
			}
		}
	}

	if showAutonomous || shouldShowTUILog(formatted) {
		h.state.addLog(formatted, tag)
	}

	if val, ok := parseLogScore(formatted); ok {
		h.state.logMutex.Lock()
		h.state.beliefScores = append(h.state.beliefScores, val)
		if len(h.state.beliefScores) > 12 {
			h.state.beliefScores = h.state.beliefScores[1:]
		}
		h.state.logMutex.Unlock()
	}

	return nil
}

func (h *tuiLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h
}

func (h *tuiLogHandler) WithGroup(name string) slog.Handler {
	return h
}

func runTUI(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	offline := fs.Bool("offline", false, "run without network-backed search providers")
	demo := fs.Bool("demo", false, "prefill hackathon demo query (implies --offline)")
	autostart := fs.Bool("autostart", false, "start research immediately when a query is set")
	queryFlag := fs.String("query", "", "pre-fill the research question")
	outputFlag := fs.String("output", "", "save results to this markdown file (also bound to s in results)")
	iterationsFlag := fs.Int("iterations", 0, "pre-set max loop iterations (1-12)")
	exhaustiveFlag := fs.Bool("exhaustive", false, "run all max iterations before early stop")
	biomedicalFlag := fs.Bool("biomedical", false, "start with biomedical provider preset")
	csFlag := fs.Bool("cs", false, "start with computer science provider preset")
	noEnhanceFlag := fs.Bool("no-enhance", false, "disable query grammar/typo enhancement")
	freshFlag := fs.Bool("fresh", false, "bypass search cache for a fresh retrieval pass")
	batchFlag := fs.String("batch", "", "file of newline-delimited queries for batch mode")
	noBellFlag := fs.Bool("no-bell", false, "disable terminal bell on research completion")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if plainUI() {
		return fmt.Errorf("TUI disabled in plain mode (unset WISDEV_PLAIN / NO_COLOR)")
	}

	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return fmt.Errorf("TUI requires an interactive terminal")
	}

	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("failed to make raw terminal: %w", err)
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	// Enter alt screen, hide cursor, and enable mouse reporting (button tracking
	// 1000 + SGR encoding 1006) so the scroll wheel is delivered to us instead of
	// leaking through as stray keystrokes — the cause of "weird" scrolling on
	// macOS Terminal.app. (Hold Option to select text while reporting is on.)
	// Also enable bracketed paste (2004) so the terminal wraps pasted text in
	// \033[200~ … \033[201~; we insert that as literal text instead of letting
	// each character trigger a shortcut, and it lets Cmd/Ctrl+V work even when
	// the host binds those to "paste" rather than sending a raw Ctrl+V byte.
	fmt.Print("\033[?1049h\033[?25l\033[?1000h\033[?1006h\033[?2004h\033[2J\033[H")
	defer fmt.Print("\033[?2004l\033[?1000l\033[?1006l\033[?25h\033[?1049l")

	// Rename the terminal tab/window while the TUI owns it; clear any taskbar
	// progress and restore the previous title on exit. The title is set both
	// via OSC and Win32 (ConPTY forwards either) so it works across hosts.
	originalConsoleTitle := getConsoleTitleNative()
	fmt.Print(saveTerminalTitleSequence())
	fmt.Print(terminalTitleSequence(termTitleBase))
	setConsoleTitleNative(termTitleBase)
	defer func() {
		cleanup := restoreTerminalTitleSequence()
		if taskbarProgressSupported() {
			cleanup = taskbarProgressSequence(taskbarStateClear, 0) + cleanup
		}
		fmt.Print(cleanup)
		if originalConsoleTitle != "" {
			setConsoleTitleNative(originalConsoleTitle)
		}
	}()

	initialQuery := strings.TrimSpace(*queryFlag)
	offlineMode := *offline || *demo
	if *demo && initialQuery == "" {
		initialQuery = defaultDemoQuery
	}

	eventCh := make(chan tuiEvent, 100)
	state := &tuiState{
		mode:                modeInput,
		query:               initialQuery,
		cursorPos:           len(initialQuery),
		activeElement:       0,
		providers:           defaultTUIProviders(),
		maxIterations:       defaultLocalMaxIterations(6),
		disablePlanning:     false,
		enableQueryEnhance:  true,
		enableHypotheses:    true,
		deepSearch:          false,
		generateDoc:         false,
		offlineMode:         offlineMode,
		activeSetting:       0,
		outputPath:          strings.TrimSpace(*outputFlag),
		outputPathCursorPos: len(strings.TrimSpace(*outputFlag)),
		output:              stdout,
		terminalSize: func() (int, int, error) {
			return term.GetSize(int(os.Stdout.Fd()))
		},
		eventCh:         eventCh,
		logScrollLocked: true,
		nativeTitle:     true,
		// iTerm2 renders OSC 9 payloads as desktop notifications and Apple
		// Terminal does not understand OSC 9;4 at all — only emit taskbar
		// progress on terminals known to support it.
		disableTaskbarOSC: !taskbarProgressSupported(),
	}
	researchLLMClient := resolveResearchLLMClient()
	state.llmBackend = describeLLMBackend(researchLLMClient)
	state.loadHistory()
	state.checkSessionRestore()
	if !offlineMode {
		state.checkProvidersHealth()
		// Resolve the live local model (e.g. what Ollama actually has loaded)
		// in the background and refresh the backend label when it differs.
		state.refreshLLMBackendLive(researchLLMClient)
	}

	if *noEnhanceFlag {
		state.enableQueryEnhance = false
	}
	if *exhaustiveFlag {
		state.deepSearch = true
	}
	if *noBellFlag {
		state.noBell = true
	}
	if batchPath := strings.TrimSpace(*batchFlag); batchPath != "" {
		data, err := os.ReadFile(batchPath)
		if err != nil {
			return fmt.Errorf("read batch file: %w", err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if q := strings.TrimSpace(line); q != "" {
				state.batchQueries = append(state.batchQueries, q)
			}
		}
		if len(state.batchQueries) == 0 {
			return fmt.Errorf("batch file contained no queries")
		}
		state.batchMode = true
		state.query = state.batchQueries[0]
		state.cursorPos = len(state.query)
	}

	if n := *iterationsFlag; n > 0 {
		if n > 12 {
			n = 12
		}
		state.maxIterations = n
	}
	if *biomedicalFlag && !offlineMode {
		state.toggleBiomedicalProviderPreset()
	}
	if *csFlag && !offlineMode {
		state.toggleCSProviderPreset()
	}
	if *freshFlag {
		state.bypassSearchCache = true
	}

	go func() {
		// Large enough that a typical bracketed paste arrives in one read; a key
		// press is still delivered as its own short chunk.
		var buf [8192]byte
		for {
			n, readErr := os.Stdin.Read(buf[:])
			if readErr != nil {
				break
			}
			keyBytes := make([]byte, n)
			copy(keyBytes, buf[:n])
			eventCh <- tuiEvent{eventType: eventKey, keyBytes: keyBytes}
		}
	}()

	// Redraw immediately when the terminal window is resized (SIGWINCH on
	// unix; no-op on Windows, where the tick loop below catches size changes).
	watchTerminalResize(eventCh)

	tickerCtx, tickerCancel := context.WithCancel(context.Background())
	defer tickerCancel()
	go func(ctx context.Context) {
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				eventCh <- tuiEvent{eventType: eventTick}
			}
		}
	}(tickerCtx)

	state.render()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if (*autostart || *demo || state.batchMode) && strings.TrimSpace(state.query) != "" {
		state.startResearch(ctx)
	}

	for ev := range eventCh {
		var shouldRender bool

		if ev.eventType == eventResize {
			state.render()
			continue
		}
		if ev.eventType == eventTick {
			if state.saveMsg != "" && !state.saveMsgAt.IsZero() && time.Since(state.saveMsgAt) > 4*time.Second {
				state.saveMsg = ""
				state.saveMsgAt = time.Time{}
				shouldRender = true
			}
			if state.mode == modeRunning {
				shouldRender = true
			}
			// Cross-platform resize safety net: redraw if the terminal size
			// changed since the last frame, even when SIGWINCH is unavailable
			// (Windows) or while idle in input mode.
			if w, h, err := state.currentTerminalSize(); err == nil && w > 0 && h > 0 {
				if w != state.lastTerminalWidth || h != state.lastTerminalHeight {
					shouldRender = true
				}
			}
			if shouldRender {
				state.render()
			}
			continue
		}
		if ev.eventType == eventProviderHealth {
			for idx := range state.providers {
				if state.providers[idx].code == ev.providerCode {
					state.providers[idx].lastStatus = ev.providerStatus
					break
				}
			}
			state.render()
			continue
		}
		if ev.eventType == eventRunUpdate {
			state.render()
			continue
		}
		if ev.eventType == eventLog {
			if state.mode == modeRunning {
				state.render()
			}
			continue
		}

		if ev.eventType == eventKey {
			b := ev.keyBytes
			if len(b) == 1 && b[0] == 3 { // Ctrl+C
				break
			}
			// Intercept terminal mouse reports before key parsing. Map the wheel
			// to the existing Up/Down scroll handling and swallow every other
			// mouse event, so clicks and drags never reach the key switch as
			// garbage (the "acting weird on scroll" symptom on Terminal.app).
			if isMouse, wheelUp, wheelDown := classifyMouseEvent(b); isMouse {
				if state.mode == modeInput || (!wheelUp && !wheelDown) {
					continue // nothing to scroll, or a non-wheel button: drop it
				}
				if wheelUp {
					b = []byte{27, '[', 'A'} // synthesize Up arrow
				} else {
					b = []byte{27, '[', 'B'} // synthesize Down arrow
				}
			}
			// Paste: a bracketed-paste burst (terminal injected the clipboard) or
			// a literal Ctrl+V the host did not bind to paste. Sanitize to a single
			// line and insert into the focused text field; swallow it otherwise so
			// the bytes never reach the key parser as shortcuts.
			if pasted, isPaste := pasteFromInput(b); isPaste {
				if state.insertIntoActiveTextField(pasted) {
					state.render()
				}
				continue
			}
			if state.showHelp {
				state.showHelp = false
				state.render()
				continue
			}

			// Clear exit confirmation on non-ESC keys
			if state.pendingExit && (len(b) != 1 || b[0] != 27) {
				state.pendingExit = false
				state.validationMsg = ""
			}

			// Overlay 0: Session restore prompt
			if state.mode == modeInput && state.showSessionRestorePrompt {
				if len(b) == 1 {
					key := b[0]
					if key == 'y' || key == 'Y' {
						state.restoreSession()
						state.showSessionRestorePrompt = false
						state.clearSession()
					} else if key == 'n' || key == 'N' || key == 27 { // ESC or n/N
						state.showSessionRestorePrompt = false
						state.clearSession()
					}
				}
				state.render()
				continue
			}

			// Overlay 1: History Browser
			if state.mode == modeInput && state.showHistoryBrowser {
				if len(b) == 1 {
					key := b[0]
					if key == 27 || key == 'h' {
						state.showHistoryBrowser = false
					} else if key == 13 || key == 10 { // Enter
						if len(state.history) > 0 && state.historyBrowserIdx >= 0 && state.historyBrowserIdx < len(state.history) {
							state.saveQueryUndoState()
							state.query = state.history[state.historyBrowserIdx].Query
							state.cursorPos = len(state.query)
							state.validationMsg = ""
						}
						state.showHistoryBrowser = false
					} else if key == 'j' {
						if state.historyBrowserIdx < len(state.history)-1 {
							state.historyBrowserIdx++
						}
					} else if key == 'k' {
						if state.historyBrowserIdx > 0 {
							state.historyBrowserIdx--
						}
					}
				} else if len(b) > 2 && b[0] == 27 && b[1] == '[' {
					switch b[2] {
					case 'A': // Up
						if state.historyBrowserIdx > 0 {
							state.historyBrowserIdx--
						}
					case 'B': // Down
						if state.historyBrowserIdx < len(state.history)-1 {
							state.historyBrowserIdx++
						}
					}
				}
				state.render()
				continue
			}

			// Overlay 1b: Recent saved runs browser
			if state.mode == modeInput && state.showRecentRuns {
				if len(b) == 1 {
					key := b[0]
					if key == 27 || key == 15 { // ESC / Ctrl+O
						state.showRecentRuns = false
					} else if key == 13 || key == 10 { // Enter
						if len(state.recentRuns) > 0 && state.recentRunsIdx >= 0 && state.recentRunsIdx < len(state.recentRuns) {
							state.openRecentRun(state.recentRuns[state.recentRunsIdx])
						}
						state.showRecentRuns = false
					} else if key == 'j' {
						if state.recentRunsIdx < len(state.recentRuns)-1 {
							state.recentRunsIdx++
						}
					} else if key == 'k' {
						if state.recentRunsIdx > 0 {
							state.recentRunsIdx--
						}
					}
				} else if len(b) > 2 && b[0] == 27 && b[1] == '[' {
					switch b[2] {
					case 'A': // Up
						if state.recentRunsIdx > 0 {
							state.recentRunsIdx--
						}
					case 'B': // Down
						if state.recentRunsIdx < len(state.recentRuns)-1 {
							state.recentRunsIdx++
						}
					}
				}
				state.render()
				continue
			}

			// Overlay 2: Paper details popup
			if state.mode == modeResults && state.showPaperDetail {
				if len(b) == 1 && (b[0] == 'c' || b[0] == 'C') {
					state.copyPaperBibTeXToClipboard()
					state.render()
					continue
				}
				state.showPaperDetail = false
				state.render()
				continue
			}

			// Overlay 3: Results filter input
			if state.mode == modeResults && state.resultFilterOn {
				if len(b) == 1 {
					key := b[0]
					if key == 27 { // ESC
						state.resultFilterOn = false
						state.resultFilter = ""
						state.resultFilterMatch = nil
						state.resultFilterCursor = 0
					} else if key == 13 || key == 10 { // Enter
						state.resultFilterOn = false
						state.updateResultFilterMatches()
						if len(state.resultFilterMatch) > 0 {
							state.scrollOffset = state.resultFilterMatch[state.resultFilterCursor]
						}
						state.cachedResultLines = nil
					} else if key == 127 || key == 8 { // Backspace
						if len(state.resultFilter) > 0 {
							state.resultFilter = state.resultFilter[:prevCharBoundary(state.resultFilter, len(state.resultFilter))]
						}
						state.updateResultFilterMatches()
					} else if key >= 32 && key <= 126 {
						state.resultFilter += string(key)
						state.updateResultFilterMatches()
						state.cachedResultLines = nil
					}
				}
				state.render()
				continue
			}

			// Overlay 3b: Citation jump input ([n] -> source paper)
			if state.mode == modeResults && state.citationJumpOn {
				if len(b) == 1 {
					key := b[0]
					if key == 27 { // ESC
						state.citationJumpOn = false
						state.citationJumpInput = ""
					} else if key == 13 || key == 10 { // Enter
						state.citationJumpOn = false
						state.jumpToCitation()
						state.citationJumpInput = ""
					} else if key == 127 || key == 8 { // Backspace
						if len(state.citationJumpInput) > 0 {
							state.citationJumpInput = state.citationJumpInput[:len(state.citationJumpInput)-1]
						}
					} else if key >= '0' && key <= '9' && len(state.citationJumpInput) < 3 {
						state.citationJumpInput += string(key)
					}
				}
				state.render()
				continue
			}

			// Overlay 3c: Follow-up chat (f -> grounded Q&A over current sources)
			if state.mode == modeResults && state.chatOn {
				if scrollDelta := parseMouseScrollDelta(b); scrollDelta != 0 {
					state.chatScrollOffset -= scrollDelta
					if state.chatScrollOffset < 0 {
						state.chatScrollOffset = 0
					}
					state.render()
					continue
				}
				if len(b) == 1 {
					key := b[0]
					if key == 27 { // ESC
						state.chatOn = false
						state.chatInput = ""
						state.chatCursorPos = 0
					} else if key == 13 || key == 10 { // Enter
						question := strings.TrimSpace(state.chatInput)
						if question != "" && !state.chatIsBusy() {
							state.chatInput = ""
							state.chatCursorPos = 0
							state.askFollowUpChat(ctx, question)
						}
					} else if key == 18 { // Ctrl+R — escalate question to a full research run
						question := strings.TrimSpace(state.chatInput)
						if question != "" {
							state.chatOn = false
							state.chatInput = ""
							state.chatCursorPos = 0
							state.startFollowUpResearch(ctx, question)
						}
					} else if key == 127 || key == 8 { // Backspace
						if state.chatCursorPos > 0 && state.chatCursorPos <= len(state.chatInput) {
							prev := prevCharBoundary(state.chatInput, state.chatCursorPos)
							state.chatInput = state.chatInput[:prev] + state.chatInput[state.chatCursorPos:]
							state.chatCursorPos = prev
						}
					} else if key == 1 { // Ctrl+A (Home)
						state.chatCursorPos = 0
					} else if key == 5 { // Ctrl+E (End)
						state.chatCursorPos = len(state.chatInput)
					} else if key == 11 { // Ctrl+K (Clear to end)
						state.chatInput = state.chatInput[:state.chatCursorPos]
					} else if key == 23 { // Ctrl+W (Delete word left)
						state.chatInput, state.chatCursorPos = deleteWordLeft(state.chatInput, state.chatCursorPos)
					} else if key >= 32 && key <= 126 {
						state.chatInput = state.chatInput[:state.chatCursorPos] + string(key) + state.chatInput[state.chatCursorPos:]
						state.chatCursorPos++
					}
				} else if len(b) == 4 && b[0] == 27 && b[1] == '[' && b[3] == '~' {
					switch b[2] {
					case '3': // Delete key
						if state.chatCursorPos >= 0 && state.chatCursorPos < len(state.chatInput) {
							next := nextCharBoundary(state.chatInput, state.chatCursorPos)
							state.chatInput = state.chatInput[:state.chatCursorPos] + state.chatInput[next:]
						}
					case '5': // PgUp — page toward older messages
						state.chatScrollOffset += 10
					case '6': // PgDn — page toward the latest reply
						state.chatScrollOffset -= 10
						if state.chatScrollOffset < 0 {
							state.chatScrollOffset = 0
						}
					}
				} else if len(b) > 2 && b[0] == 27 && b[1] == '[' {
					switch b[2] {
					case 'A': // Up — scroll transcript toward older messages
						state.chatScrollOffset++
					case 'B': // Down — back toward the latest reply
						if state.chatScrollOffset > 0 {
							state.chatScrollOffset--
						}
					case 'C': // Right — move input cursor
						if state.chatCursorPos < len(state.chatInput) {
							state.chatCursorPos = nextCharBoundary(state.chatInput, state.chatCursorPos)
						}
					case 'D': // Left — move input cursor
						if state.chatCursorPos > 0 {
							state.chatCursorPos = prevCharBoundary(state.chatInput, state.chatCursorPos)
						}
					case 'H': // Home
						state.chatCursorPos = 0
					case 'F': // End
						state.chatCursorPos = len(state.chatInput)
					}
				}
				state.render()
				continue
			}

			// Overlay 4: Provider checklist filter input
			if state.mode == modeInput && state.providerFiltering {
				if len(b) == 1 {
					key := b[0]
					if key == 27 { // ESC
						state.providerFiltering = false
						state.providerFilter = ""
						state.providerIdx = 0
					} else if key == 13 || key == 10 { // Enter
						state.providerFiltering = false
						state.clampProviderIdx()
					} else if key == 127 || key == 8 { // Backspace
						if len(state.providerFilter) > 0 {
							state.providerFilter = state.providerFilter[:prevCharBoundary(state.providerFilter, len(state.providerFilter))]
							state.providerIdx = 0
							state.clampProviderIdx()
						}
					} else if key >= 32 && key <= 126 {
						state.providerFilter += string(key)
						state.providerIdx = 0
						state.clampProviderIdx()
					}
				}
				state.render()
				continue
			}

			// SGR mouse scroll in Results mode
			if scrollDelta := parseMouseScrollDelta(b); scrollDelta != 0 && state.mode == modeResults {
				state.scrollResultsBy(scrollDelta, 0)
				state.render()
				continue
			}

			// Core TUI Key Processing
			if state.mode == modeInput {
				if len(b) == 1 {
					key := b[0]
					if key == 27 { // ESC
						if state.pendingExit && time.Since(state.pendingExitAt) < 2*time.Second {
							break
						} else {
							state.pendingExit = true
							state.pendingExitAt = time.Now()
							state.validationMsg = "Exit WisDev? Press ESC again to confirm, any other key to cancel."
						}
					} else if key == '?' && (!state.inputElementIsTextField() || state.focusedTextFieldEmpty()) {
						state.showHelp = true
					} else if key == 'h' && !state.inputElementIsTextField() {
						if len(state.history) > 0 {
							state.showHistoryBrowser = true
							state.historyBrowserIdx = len(state.history) - 1
						}
					} else if key == 15 { // Ctrl+O — recent saved runs browser
						state.recentRuns = listRecentRunFiles(".", maxRecentRuns)
						if len(state.recentRuns) > 0 {
							state.showRecentRuns = true
							state.recentRunsIdx = 0
						} else {
							state.validationMsg = "No saved runs found (export JSON with 'e' after a run)."
						}
					} else if key == 9 { // Tab
						state.focusNext()
					} else if key == 16 && state.activeElement == 0 { // Ctrl+P
						if state.historyIdx > 0 {
							state.saveQueryUndoState()
							state.historyIdx--
							state.query = state.history[state.historyIdx].Query
							state.cursorPos = len(state.query)
						}
					} else if key == 14 && state.activeElement == 0 { // Ctrl+N
						if state.historyIdx < len(state.history)-1 {
							state.saveQueryUndoState()
							state.historyIdx++
							state.query = state.history[state.historyIdx].Query
							state.cursorPos = len(state.query)
						} else if state.historyIdx == len(state.history)-1 {
							state.saveQueryUndoState()
							state.historyIdx++
							state.query = ""
							state.cursorPos = 0
						}
					} else if key == 26 && state.activeElement == 0 { // Ctrl+Z
						state.performUndo()
					} else if key == 25 && state.activeElement == 0 { // Ctrl+Y
						state.performRedo()
					} else if key == 1 { // Ctrl+A (Home)
						if state.activeElement == 0 {
							state.cursorPos = 0
						} else if state.activeElement == 3 {
							state.outputPathCursorPos = 0
						}
					} else if key == 5 { // Ctrl+E (End)
						if state.activeElement == 0 {
							state.cursorPos = len(state.query)
						} else if state.activeElement == 3 {
							state.outputPathCursorPos = len(state.outputPath)
						}
					} else if key == 11 { // Ctrl+K (Clear to end)
						if state.activeElement == 0 {
							state.saveQueryUndoState()
							state.query = state.query[:state.cursorPos]
						} else if state.activeElement == 3 {
							state.outputPath = state.outputPath[:state.outputPathCursorPos]
						}
					} else if key == 23 { // Ctrl+W (Delete word left)
						if state.activeElement == 0 {
							state.saveQueryUndoState()
							state.query, state.cursorPos = deleteWordLeft(state.query, state.cursorPos)
						} else if state.activeElement == 3 {
							state.outputPath, state.outputPathCursorPos = deleteWordLeft(state.outputPath, state.outputPathCursorPos)
						}
					} else if key == 4 { // Ctrl+D (Delete char at cursor)
						if state.activeElement == 0 {
							state.deleteQueryChar()
						} else if state.activeElement == 3 {
							state.deleteOutputPathChar()
						}
					} else if key == 13 || key == 10 { // Enter
						if state.activeElement == 0 || state.activeElement == 4 { // Start button is index 4
							if strings.TrimSpace(state.query) != "" {
								state.startResearch(ctx)
							} else {
								state.validationMsg = "Warning: Research question cannot be empty!"
							}
						} else if state.activeElement == 2 {
							state.toggleActiveSetting()
						} else if state.activeElement == 5 { // Exit button is index 5
							break
						}
					} else if key == 127 || key == 8 { // Backspace
						if state.activeElement == 0 {
							state.validationMsg = ""
							state.backspaceQueryChar()
						} else if state.activeElement == 3 {
							state.backspaceOutputPathChar()
						}
					} else if key == 'b' && state.activeElement == 1 {
						state.toggleBiomedicalProviderPreset()
					} else if key == 'c' && state.activeElement == 1 {
						state.toggleCSProviderPreset()
					} else if key == 'p' && state.activeElement == 1 {
						state.togglePhysicsProviderPreset()
					} else if key == 'g' && state.activeElement == 1 {
						state.toggleGeneralProviderPreset()
					} else if key == 'x' && state.activeElement == 1 {
						state.togglePreprintProviderPreset()
					} else if key == 'a' && state.activeElement == 1 {
						anyEnabled := false
						for _, p := range state.providers {
							if p.enabled {
								anyEnabled = true
								break
							}
						}
						for idx := range state.providers {
							state.providers[idx].enabled = !anyEnabled
						}
					} else if key == '/' && state.activeElement == 1 {
						state.providerFiltering = true
						state.providerFilter = ""
						state.providerIdx = 0
					} else if key == 32 { // Space
						if state.activeElement == 1 {
							matching := state.matchingProviders()
							if len(matching) > 0 && state.providerIdx >= 0 && state.providerIdx < len(matching) {
								code := matching[state.providerIdx].code
								for idx, p := range state.providers {
									if p.code == code {
										state.providers[idx].enabled = !state.providers[idx].enabled
										break
									}
								}
							}
						} else if state.activeElement == 2 && state.activeSetting == 1 {
							state.disablePlanning = !state.disablePlanning
						} else if state.activeElement == 2 && state.activeSetting == 2 {
							state.offlineMode = !state.offlineMode
						} else if state.activeElement == 2 && state.activeSetting == 3 {
							state.enableQueryEnhance = !state.enableQueryEnhance
						} else if state.activeElement == 2 && state.activeSetting == 4 {
							state.enableHypotheses = !state.enableHypotheses
						} else if state.activeElement == 2 && state.activeSetting == 5 {
							state.deepSearch = !state.deepSearch
						} else if state.activeElement == 2 && state.activeSetting == 6 {
							state.longFormReport = !state.longFormReport
						} else if state.activeElement == 2 && state.activeSetting == 7 {
							state.generateDoc = !state.generateDoc
						} else if state.activeElement == 0 {
							state.validationMsg = ""
							state.insertQueryChar(" ")
						} else if state.activeElement == 3 {
							state.insertOutputPathChar(" ")
						}
					} else if key >= '1' && key <= '9' && state.activeElement == 2 && state.activeSetting == 0 {
						state.maxIterations = int(key - '0')
					} else if (key == '+' || key == '=') && state.activeElement == 2 && state.activeSetting == 0 {
						if state.maxIterations < 12 {
							state.maxIterations++
						}
					} else if key == '-' && state.activeElement == 2 && state.activeSetting == 0 {
						if state.maxIterations > 1 {
							state.maxIterations--
						}
					} else if key >= 32 && key <= 126 { // Printable ASCII
						if state.activeElement == 0 {
							state.validationMsg = ""
							state.insertQueryChar(string(key))
						} else if state.activeElement == 3 {
							state.insertOutputPathChar(string(key))
						}
					}
				} else if len(b) == 3 && b[0] == 27 && b[1] == '[' && b[2] == 'Z' {
					state.focusPrevious()
				} else if dir, ok := parseCtrlArrow(b); ok {
					if state.activeElement == 0 {
						if dir > 0 {
							state.cursorPos = moveWordRight(state.query, state.cursorPos)
						} else {
							state.cursorPos = moveWordLeft(state.query, state.cursorPos)
						}
					} else if state.activeElement == 3 {
						if dir > 0 {
							state.outputPathCursorPos = moveWordRight(state.outputPath, state.outputPathCursorPos)
						} else {
							state.outputPathCursorPos = moveWordLeft(state.outputPath, state.outputPathCursorPos)
						}
					}
				} else if len(b) > 2 && b[0] == 27 && b[1] == '[' {
					// Arrow keys
					switch b[2] {
					case 'A': // Up
						switch state.activeElement {
						case 0:
							if len(state.history) > 0 {
								state.showHistoryBrowser = true
								state.historyBrowserIdx = len(state.history) - 1
							}
						case 1:
							cols := state.providerGridColumns()
							if state.providerIdx >= cols {
								state.providerIdx -= cols
							} else {
								state.activeElement = 0
							}
						case 2:
							if state.activeSetting >= settingsPerRow {
								state.activeSetting = moveSettingUp(state.activeSetting)
							} else {
								state.activeElement = 1
								state.providerIdx = 0
								state.clampProviderIdx()
							}
						case 3:
							state.activeElement = 2
							state.activeSetting = 6
						case 4, 5:
							state.activeElement = 3
						}
					case 'B': // Down
						switch state.activeElement {
						case 0:
							state.activeElement = 1
							state.providerIdx = 0
							state.clampProviderIdx()
						case 1:
							cols := state.providerGridColumns()
							matching := state.matchingProviders()
							if state.providerIdx+cols < len(matching) {
								state.providerIdx += cols
							} else {
								state.activeElement = 2
								state.activeSetting = 0
							}
						case 2:
							if next := moveSettingDown(state.activeSetting); next != state.activeSetting {
								state.activeSetting = next
							} else {
								state.activeElement = 3
							}
						case 3:
							state.activeElement = 4
						}
					case 'C': // Right
						switch state.activeElement {
						case 0:
							if state.cursorPos < len(state.query) {
								state.cursorPos = nextCharBoundary(state.query, state.cursorPos)
							}
						case 1:
							matching := state.matchingProviders()
							if state.providerIdx < len(matching)-1 {
								state.providerIdx++
							}
						case 2:
							state.activeSetting = moveSettingRight(state.activeSetting)
						case 3:
							if state.outputPathCursorPos < len(state.outputPath) {
								state.outputPathCursorPos = nextCharBoundary(state.outputPath, state.outputPathCursorPos)
							}
						case 4:
							state.activeElement = 5
						}
					case 'D': // Left
						switch state.activeElement {
						case 0:
							if state.cursorPos > 0 {
								state.cursorPos = prevCharBoundary(state.query, state.cursorPos)
							}
						case 1:
							if state.providerIdx > 0 {
								state.providerIdx--
							}
						case 2:
							state.activeSetting = moveSettingLeft(state.activeSetting)
						case 3:
							if state.outputPathCursorPos > 0 {
								state.outputPathCursorPos = prevCharBoundary(state.outputPath, state.outputPathCursorPos)
							}
						case 5:
							state.activeElement = 4
						}
					case 'H': // Home key
						if state.activeElement == 0 {
							state.cursorPos = 0
						} else if state.activeElement == 3 {
							state.outputPathCursorPos = 0
						}
					case 'F': // End key
						if state.activeElement == 0 {
							state.cursorPos = len(state.query)
						} else if state.activeElement == 3 {
							state.outputPathCursorPos = len(state.outputPath)
						}
					}
				} else if len(b) == 4 && b[0] == 27 && b[1] == '[' && b[2] == '3' && b[3] == '~' { // Delete key
					if state.activeElement == 0 {
						state.deleteQueryChar()
					} else if state.activeElement == 3 {
						state.deleteOutputPathChar()
					}
				}
			} else if state.mode == modeRunning {
				if len(b) == 1 {
					key := b[0]
					if key == 27 { // ESC to cancel
						if state.cancelFunc != nil {
							state.cancelFunc()
						}
					} else if key == 'P' || key == 'p' {
						state.paused = !state.paused
					} else if key == 'k' {
						state.logScrollLocked = false
						state.logScrollOffset++
						state.clampLogScrollOffset()
					} else if key == 'j' {
						state.logScrollLocked = false
						state.logScrollOffset--
						if state.logScrollOffset < 0 {
							state.logScrollOffset = 0
							state.logScrollLocked = true
						}
					}
				} else if len(b) > 2 && b[0] == 27 && b[1] == '[' {
					switch b[2] {
					case 'A': // Up arrow — older log lines
						state.logScrollLocked = false
						state.logScrollOffset++
						state.clampLogScrollOffset()
					case 'B': // Down arrow — newer log lines
						state.logScrollLocked = false
						state.logScrollOffset--
						if state.logScrollOffset < 0 {
							state.logScrollOffset = 0
							state.logScrollLocked = true
						}
					}
				}
			} else if state.mode == modeResults {
				// Handle ESC cancel when pending confirmation save
				if state.pendingSave && len(b) == 1 && b[0] == 27 {
					state.pendingSave = false
					state.saveMsg = ""
					state.render()
					continue
				}

				if len(b) == 1 {
					key := b[0]
					if key == 27 || key == 'q' {
						state.mode = modeInput
						state.result = nil
						state.activeElement = 0
						state.saveMsg = ""
						state.resultFilter = ""
						state.resultFilterOn = false
						state.resultFilterMatch = nil
					} else if key == 13 || key == 10 { // Enter
						if state.resultPane == resultPaneSources && state.result != nil && len(state.result.Papers) > 0 {
							state.showPaperDetail = true
						} else {
							state.mode = modeInput
							state.result = nil
							state.activeElement = 0
							state.saveMsg = ""
							state.resultFilter = ""
							state.resultFilterOn = false
							state.resultFilterMatch = nil
						}
					} else if key == 'r' {
						state.savePreviousResult()
						state.saveMsg = ""
						state.startResearch(ctx)
					} else if key == 'E' {
						state.savePreviousResult()
						state.saveMsg = ""
						state.startResearchExhaustive(ctx)
					} else if key == 'R' {
						state.mode = modeInput
						state.result = nil
						state.activeElement = 0
						state.saveMsg = ""
						state.resultFilter = ""
						state.resultFilterOn = false
						state.resultFilterMatch = nil
					} else if key == 's' || key == 19 { // s or Ctrl+S
						if state.pendingSave && state.pendingSaveType == "md" {
							state.saveResults()
							state.pendingSave = false
						} else {
							state.pendingSave = true
							state.pendingSaveType = "md"
							target := state.resolvedSavePath("md")
							state.saveMsg = fmt.Sprintf("Save to: %s ? [s=confirm, ESC=cancel]", target)
						}
					} else if key == 'e' { // e
						if state.pendingSave && state.pendingSaveType == "json" {
							state.saveResultsJSON()
							state.pendingSave = false
						} else {
							state.pendingSave = true
							state.pendingSaveType = "json"
							target := state.resolvedSavePath("json")
							state.saveMsg = fmt.Sprintf("Save JSON to: %s ? [e=confirm, ESC=cancel]", target)
						}
					} else if key == 'b' { // b
						if state.pendingSave && state.pendingSaveType == "bib" {
							state.saveResultsBibTeX()
							state.pendingSave = false
						} else {
							state.pendingSave = true
							state.pendingSaveType = "bib"
							target := state.resolvedSavePath("bib")
							state.saveMsg = fmt.Sprintf("Save BibTeX to: %s ? [b=confirm, ESC=cancel]", target)
						}
					} else if key == 't' { // CSV/TSV export
						if state.pendingSave && state.pendingSaveType == "csv" {
							state.saveResultsCSV()
							state.pendingSave = false
						} else {
							state.pendingSave = true
							state.pendingSaveType = "csv"
							target := state.resolvedSavePath("csv")
							state.saveMsg = fmt.Sprintf("Save CSV to: %s ? [t=confirm, ESC=cancel]", target)
						}
					} else if key == 'w' { // Self-contained HTML report export
						if state.pendingSave && state.pendingSaveType == "html" {
							state.saveResultsHTML()
							state.pendingSave = false
						} else {
							state.pendingSave = true
							state.pendingSaveType = "html"
							target := state.resolvedSavePath("html")
							state.saveMsg = fmt.Sprintf("Save HTML to: %s ? [w=confirm, ESC=cancel]", target)
						}
					} else if key == 'c' { // Copy to clipboard (OSC 52)
						state.copyResultsToClipboard()
					} else if key == '/' { // Results filter trigger
						state.resultFilterOn = true
						state.resultFilter = ""
						state.resultFilterMatch = nil
						state.resultFilterCursor = 0
					} else if key == 'o' { // Citation jump: open source for inline [n]
						if state.result != nil && len(state.result.Papers) > 0 {
							state.citationJumpOn = true
							state.citationJumpInput = ""
						} else {
							state.setSaveMsg("No sources available for citation jump.")
						}
					} else if key == 'f' { // Follow-up chat: grounded Q&A over current sources
						state.chatOn = true
						state.chatInput = ""
						state.chatCursorPos = 0
						state.chatScrollOffset = 0
					} else if key == 'n' && len(state.resultFilterMatch) > 0 {
						state.resultFilterCursor = (state.resultFilterCursor + 1) % len(state.resultFilterMatch)
						state.scrollOffset = state.resultFilterMatch[state.resultFilterCursor]
						state.clampResultsScrollOffset()
					} else if key == 'N' && len(state.resultFilterMatch) > 0 {
						state.resultFilterCursor = (state.resultFilterCursor - 1 + len(state.resultFilterMatch)) % len(state.resultFilterMatch)
						state.scrollOffset = state.resultFilterMatch[state.resultFilterCursor]
						state.clampResultsScrollOffset()
					} else if key == 'v' { // Toggle reasoning trace view
						if state.resultPane == resultPaneReasoning {
							state.resultPane = resultPaneAll
						} else {
							state.resultPane = resultPaneReasoning
						}
						state.scrollOffset = 0
						state.cachedResultLines = nil
						state.resultFilter = ""
						state.resultFilterMatch = nil
					} else if key == 'y' { // Toggle hypotheses & beliefs view
						if state.resultPane == resultPaneHypotheses {
							state.resultPane = resultPaneAll
						} else {
							state.resultPane = resultPaneHypotheses
						}
						state.scrollOffset = 0
						state.cachedResultLines = nil
						state.resultFilter = ""
						state.resultFilterMatch = nil
					} else if key == 'h' {
						state.resultsHome()
					} else if key == 9 {
						state.cycleResultPane(1)
						state.resultFilter = ""
						state.resultFilterMatch = nil
					} else if key == 'j' || key == 'd' {
						if state.resultPane == resultPaneSources && state.result != nil && len(state.result.Papers) > 0 {
							if state.paperDetailIdx < len(state.result.Papers)-1 {
								state.paperDetailIdx++
								state.scrollSelectedPaperIntoView()
							}
						} else {
							state.scrollResultsBy(1, 0)
						}
					} else if key == 'k' || key == 'u' {
						if state.resultPane == resultPaneSources && state.result != nil && len(state.result.Papers) > 0 {
							if state.paperDetailIdx > 0 {
								state.paperDetailIdx--
								state.scrollSelectedPaperIntoView()
							}
						} else {
							state.scrollResultsBy(-1, 0)
						}
					} else if key == '[' {
						state.cycleResultPane(-1)
						state.resultFilter = ""
						state.resultFilterMatch = nil
					} else if key == ']' {
						state.cycleResultPane(1)
						state.resultFilter = ""
						state.resultFilterMatch = nil
					} else if key == 'g' {
						state.scrollOffset = 0
						state.paperDetailIdx = 0
						state.scrollSelectedPaperIntoView()
					} else if key == 'G' {
						state.scrollOffset = 1<<31 - 1
						if state.result != nil {
							state.paperDetailIdx = len(state.result.Papers) - 1
						}
						state.scrollSelectedPaperIntoView()
					}
				} else if len(b) == 4 && b[0] == 27 && b[1] == '[' && b[3] == '~' {
					switch b[2] {
					case '5':
						state.scrollResultsBy(0, -1)
					case '6':
						state.scrollResultsBy(0, 1)
					}
				} else if len(b) > 2 && b[0] == 27 && b[1] == '[' {
					switch b[2] {
					case 'A':
						if state.resultPane == resultPaneSources && state.result != nil && len(state.result.Papers) > 0 {
							if state.paperDetailIdx > 0 {
								state.paperDetailIdx--
								state.scrollSelectedPaperIntoView()
							}
						} else {
							state.scrollResultsBy(-1, 0)
						}
					case 'B':
						if state.resultPane == resultPaneSources && state.result != nil && len(state.result.Papers) > 0 {
							if state.paperDetailIdx < len(state.result.Papers)-1 {
								state.paperDetailIdx++
								state.scrollSelectedPaperIntoView()
							}
						} else {
							state.scrollResultsBy(1, 0)
						}
					}
				}
			}
		}

		state.render()
	}

	return nil
}

func (s *tuiState) copyResultsToClipboard() {
	lines := buildTUIResultLines(s, 80, s.resultPane)
	var plainLines []string
	for _, l := range lines {
		plainLines = append(plainLines, removeEscapeSequences(l))
	}
	content := strings.Join(plainLines, "\n")
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	fmt.Printf("\033]52;c;%s\007", encoded)

	tempDir := filepath.Join(os.TempDir(), "wisdev")
	_ = os.MkdirAll(tempDir, 0o755)
	tempFile := filepath.Join(tempDir, "clipboard-fallback.txt")
	err := os.WriteFile(tempFile, []byte(content), 0o644)
	if err == nil {
		s.saveMsg = "Copied (OSC52). Fallback saved to " + tempFile
	} else {
		s.saveMsg = "Copied (OSC52)"
	}
}

func (s *tuiState) startResearch(parentCtx context.Context) {
	s.startResearchWithOptions(parentCtx, false)
}

func (s *tuiState) startResearchExhaustive(parentCtx context.Context) {
	s.startResearchWithOptions(parentCtx, true)
}

func (s *tuiState) startResearchWithOptions(parentCtx context.Context, forceExhaustive bool) {
	if forceExhaustive {
		s.deepSearch = true
		s.bypassSearchCache = true
	}
	if s.originalQuery != strings.TrimSpace(s.query) && !s.keepPrevResultOnce {
		s.prevResult = nil
	}
	s.keepPrevResultOnce = false
	s.cachedResultLines = nil
	s.resetFollowUpChat()
	s.saveSession()

	s.validationMsg = ""
	s.mode = modeRunning
	s.requestedIterations = s.maxIterations
	autoExhaustive := false
	if s.maxIterations >= 8 && !s.deepSearch && !forceExhaustive {
		s.deepSearch = true
		autoExhaustive = true
	}
	s.originalQuery = strings.TrimSpace(s.query)
	task := s.originalQuery
	s.preparedQuery = s.originalQuery
	s.seedQueries = nil
	if task != "" {
		llmClient := resolveResearchLLMClient()
		s.refreshLLMBackendLive(llmClient)
		prepCtx, prepCancel := context.WithTimeout(parentCtx, 20*time.Second)
		prepared := prepareResearchQueryWithLLM(prepCtx, task, llmClient, !s.enableQueryEnhance)
		prepCancel()
		s.preparedQuery = prepared.Corrected
		s.detectedDomain = prepared.Domain
		if s.detectedDomain == "" {
			s.detectedDomain = inferResearchDomain(task)
		}
		s.seedQueries = append([]string(nil), prepared.SeedQueries...)
		if strings.TrimSpace(prepared.SearchQuery) != "" {
			task = prepared.SearchQuery
		} else if strings.TrimSpace(s.preparedQuery) != "" {
			task = s.preparedQuery
		}
	}
	s.maybeApplyAutoProviderPreset()
	s.runningTask = task
	s.logs = []tuiLogEntry{{msg: "Initialising agent swarm...", tag: "I"}}
	s.logs = append(s.logs, tuiLogEntry{msg: "Synthesis pipeline: inline-citations-v5 | LLM backend: " + strings.TrimSpace(s.llmBackend), tag: "I"})
	if autoExhaustive {
		s.logs = append(s.logs, tuiLogEntry{msg: fmt.Sprintf("Auto-enabled Exhaustive for %d max iterations.", s.maxIterations), tag: "I"})
	}
	if s.preparedQuery != "" && s.preparedQuery != s.originalQuery {
		s.logs = append(s.logs, tuiLogEntry{msg: "Query enhanced: " + s.preparedQuery, tag: "I"})
	}
	s.iterations = 0
	s.executedQueries = 0
	s.papersFound = 0
	s.degradedSteps = 0
	s.elapsedTime = 0
	s.runError = nil
	s.result = nil
	s.saveMsg = ""
	s.resultPane = resultPaneAll
	s.researchStartTime = time.Now()
	s.logScrollOffset = 0
	s.logScrollLocked = true
	s.paused = false
	s.executedProviders = nil
	s.providerCounts = nil
	s.notifyRunUpdate()

	trimmedQuery := strings.TrimSpace(s.query)
	if trimmedQuery != "" {
		s.appendHistory(trimmedQuery)
	}

	runCtx, runCancel := context.WithCancel(parentCtx)
	s.cancelFunc = runCancel

	var enabled []string
	for _, p := range s.providers {
		if p.enabled {
			enabled = append(enabled, p.code)
		}
	}

	agentOpts := []agent.Option{}
	llmClient := resolveResearchLLMClient()
	agentOpts = append(agentOpts, agent.WithLLMClient(llmClient))
	switch {
	case s.offlineMode:
		agentOpts = append(agentOpts, agent.WithNoSearchProviders())
		s.addLog("Offline mode: network providers disabled.", "I")
	case len(enabled) > 0:
		agentOpts = append(agentOpts, agent.WithProviderNames(enabled...))
	default:
		agentOpts = append(agentOpts, agent.WithNoSearchProviders())
	}

	go func() {
		oldLogger := slog.Default()
		tuiHandler := &tuiLogHandler{state: s}
		slog.SetDefault(slog.New(tuiHandler))
		defer slog.SetDefault(oldLogger)

		startTime := time.Now()
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()

		done := make(chan struct{})
		var result *agent.YOLOResult
		var runErr error
		var manuscriptPath string
		var manuscriptErr error

		go func() {
			defer close(done)
			maxUniquePapers := s.maxIterations * 4
			hitsPerSearch := 3
			minIterations := 0
			if s.deepSearch {
				maxUniquePapers = s.maxIterations * 10
				hitsPerSearch = 5
				minIterations = s.maxIterations
			}
			runErr = withGlobalResearchLLMClient(llmClient, func() error {
				var innerErr error
				result, innerErr = agent.NewAgent(agentOpts...).RunYOLO(runCtx, agent.YOLORequest{
					Task:                task,
					OriginalQuery:       s.originalQuery,
					PreparedQuery:       s.preparedQuery,
					SeedQueries:         append([]string(nil), s.seedQueries...),
					Domain:              s.detectedDomain,
					MaxIterations:       s.maxIterations,
					MinIterations:       minIterations,
					MaxSearchTerms:      s.maxIterations,
					HitsPerSearch:       hitsPerSearch,
					MaxUniquePapers:     maxUniquePapers,
					DisablePlanning:     s.disablePlanning,
					DisableHypotheses:   !s.enableHypotheses,
					DisableQueryEnhance: !s.enableQueryEnhance,
					BypassSearchCache:   s.bypassSearchCache,
					LongFormReport:      s.longFormReport,
					OnProgress: func(event agent.ProgressEvent) {
						s.handleProgressEvent(event)
					},
				})
				if innerErr != nil {
					return innerErr
				}
				// Optional docGen step: turn the research result into a grounded
				// manuscript using the same pipeline as `wisdev docgen`, and write it
				// beside the export target. A manuscript failure is non-fatal — the
				// research already succeeded, so we surface a warning instead of
				// failing the whole run.
				if s.generateDoc && result != nil {
					s.addLog("DocGen: generating grounded manuscript…", "I")
					rendered, _, derr := generateManuscriptFromResearch(runCtx, io.Discard, task, result, "markdown", "", s.offlineMode, manuscriptControls{})
					if derr != nil {
						manuscriptErr = derr
					} else {
						path := s.manuscriptOutputPath()
						if werr := os.WriteFile(path, []byte(rendered), 0o644); werr != nil {
							manuscriptErr = werr
						} else {
							manuscriptPath = path
						}
					}
				}
				return nil
			})
		}()

		for {
			select {
			case <-done:
				s.logMutex.Lock()
				s.runError = runErr
				s.result = result
				s.completedElapsed = time.Since(startTime)
				if runErr != nil {
					s.logs = append(s.logs, tuiLogEntry{msg: fmt.Sprintf("Error: %v", runErr), tag: "E"})
				} else {
					s.logs = append(s.logs, tuiLogEntry{msg: "Research loop complete.", tag: "I"})
					s.manuscriptPath = manuscriptPath
					if manuscriptErr != nil {
						s.logs = append(s.logs, tuiLogEntry{msg: fmt.Sprintf("DocGen warning: manuscript not generated: %v", manuscriptErr), tag: "W"})
					} else if manuscriptPath != "" {
						s.logs = append(s.logs, tuiLogEntry{msg: fmt.Sprintf("DocGen: manuscript written to %s", manuscriptPath), tag: "I"})
					}
					if result != nil {
						s.iterations = result.Iterations
						s.papersFound = result.PapersFound
						if pq := strings.TrimSpace(result.PreparedQuery); pq != "" {
							s.preparedQuery = pq
						}
						if oq := strings.TrimSpace(result.OriginalQuery); oq != "" {
							s.originalQuery = oq
						}
						if d := strings.TrimSpace(result.DetectedDomain); d != "" {
							s.detectedDomain = d
						}
					}
				}
				s.logMutex.Unlock()

				if s.batchMode {
					s.saveBatchResults()
					if s.batchQueryIdx+1 < len(s.batchQueries) {
						s.batchQueryIdx++
						s.query = s.batchQueries[s.batchQueryIdx]
						s.cursorPos = len(s.query)
						s.result = nil
						s.runError = nil
						s.iterations = 0
						s.papersFound = 0
						s.degradedSteps = 0
						s.executedQueries = 0
						s.executedProviders = nil
						s.providerCounts = nil
						s.logs = nil
						s.cachedResultLines = nil
						s.beliefScores = nil
						s.clearSession()
						go s.startResearch(context.Background())
						s.notifyRunUpdate()
						return
					}
					s.setSaveMsg(fmt.Sprintf("Batch complete (%d queries). Review results or press Enter for a new search.", len(s.batchQueries)))
				}

				s.mode = modeResults
				s.scrollOffset = 0
				s.paperDetailIdx = 0
				s.cachedResultLines = nil
				s.clearSession()

				s.playCompletionChime(runErr == nil)
				if runErr == nil && !s.batchMode {
					s.setSaveMsg(fmt.Sprintf("✓ Research complete in %.1fs", s.completedElapsed.Seconds()))
				}

				if !s.batchMode && strings.TrimSpace(s.outputPath) != "" {
					s.saveResults()
				}
				s.notifyRunUpdate()
				return
			case <-ticker.C:
				s.logMutex.Lock()
				if !s.paused {
					s.elapsedTime = time.Since(startTime)
				} else {
					startTime = startTime.Add(250 * time.Millisecond)
				}
				s.logMutex.Unlock()
				s.notifyRunUpdate()
			case <-runCtx.Done():
				s.logMutex.Lock()
				s.logs = append(s.logs, tuiLogEntry{msg: "Cancelled by user.", tag: "W"})
				s.logMutex.Unlock()
				s.mode = modeInput
				s.notifyRunUpdate()
				return
			}
		}
	}()
}

func (s *tuiState) notifyRunUpdate() {
	if s == nil || s.eventCh == nil {
		return
	}
	select {
	case s.eventCh <- tuiEvent{eventType: eventRunUpdate}:
	default:
	}
}

func (s *tuiState) scrollResultsBy(lineDelta int, pageDelta int) {
	if pageDelta != 0 {
		_, height, err := s.currentTerminalSize()
		if err != nil {
			height = 24
		}
		pageSize := resultsViewportHeight(height, s.saveMsg != "" || s.resultFilterOn || s.citationJumpOn)
		lineDelta = pageDelta * pageSize
	}
	s.scrollOffset += lineDelta
	s.clampResultsScrollOffset()
}

func (s *tuiState) clampResultsScrollOffset() {
	width, height, err := s.currentTerminalSize()
	if err != nil || width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	viewport := resultsViewportHeight(height, s.saveMsg != "" || s.resultFilterOn || s.citationJumpOn)
	lines := s.getResultLines(width)
	maxOffset := len(lines) - viewport
	if maxOffset < 0 {
		maxOffset = 0
	}
	if s.scrollOffset > maxOffset {
		s.scrollOffset = maxOffset
	}
	if s.scrollOffset < 0 {
		s.scrollOffset = 0
	}
}

func (s *tuiState) scrollSelectedPaperIntoView() {
	if s.resultPane != resultPaneSources || s.result == nil || len(s.result.Papers) == 0 {
		return
	}
	width, height, err := s.currentTerminalSize()
	if err != nil || width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	viewport := resultsViewportHeight(height, s.saveMsg != "" || s.resultFilterOn || s.citationJumpOn)
	lines := s.getResultLines(width)

	targetPrefix := fmt.Sprintf("  [%d] ", s.paperDetailIdx+1)
	targetLineIdx := -1
	for idx, line := range lines {
		if strings.HasPrefix(line, targetPrefix) {
			targetLineIdx = idx
			break
		}
	}
	if targetLineIdx == -1 {
		return
	}

	nextPrefix := fmt.Sprintf("  [%d] ", s.paperDetailIdx+2)
	endLineIdx := len(lines) - 1
	for idx := targetLineIdx + 1; idx < len(lines); idx++ {
		if strings.HasPrefix(lines[idx], nextPrefix) || strings.HasPrefix(lines[idx], "  … ") {
			endLineIdx = idx - 1
			break
		}
	}

	blockHeight := endLineIdx - targetLineIdx + 1

	if s.paperDetailIdx == 0 {
		s.scrollOffset = 0
	} else if targetLineIdx < s.scrollOffset {
		s.scrollOffset = targetLineIdx
	} else if endLineIdx >= s.scrollOffset+viewport {
		if blockHeight <= viewport {
			s.scrollOffset = endLineIdx - viewport + 1
		} else {
			s.scrollOffset = targetLineIdx
		}
	}
	s.clampResultsScrollOffset()
}

func (s *tuiState) saveResults() {
	savedPath, err := saveTUIResult(s.outputPath, s.runningTask, s.result, s.completedElapsed, s.runError, s.logs)
	if err != nil {
		s.saveMsg = "Error: " + err.Error()
		return
	}
	s.saveMsg = "Saved " + savedPath
}

func (s *tuiState) saveResultsJSON() {
	savedPath, err := saveTUIResultJSON(s.outputPath, s.runningTask, s.result, s.completedElapsed, s.runError)
	if err != nil {
		s.saveMsg = "Error: " + err.Error()
		return
	}
	s.saveMsg = "Saved JSON " + savedPath
}

func (s *tuiState) saveResultsBibTeX() {
	savedPath, err := saveTUIResultBibTeX(s.outputPath, s.runningTask, s.result)
	if err != nil {
		s.saveMsg = "Error: " + err.Error()
		return
	}
	s.saveMsg = "Saved BibTeX " + savedPath
}

func (s *tuiState) saveResultsHTML() {
	savedPath, err := saveTUIResultHTML(s.outputPath, s.runningTask, s.result, s.completedElapsed)
	if err != nil {
		s.saveMsg = "Error: " + err.Error()
		return
	}
	s.saveMsg = "Saved HTML " + savedPath
}

func (s *tuiState) currentTerminalSize() (int, int, error) {
	if s != nil && s.terminalSize != nil {
		return s.terminalSize()
	}
	return term.GetSize(int(os.Stdout.Fd()))
}

type tuiRenderer struct {
	buf             *bytes.Buffer
	row             int
	width           int
	height          int
	theme           tuiTheme
	hasScrollbar    bool
	scrollbarTrack  bool
	scrollOffset    int
	totalLines      int
	viewportHeight  int
	drawnLinesCount int
	maxRow          int
}

// beginLine reserves the next screen row for a draw helper. It reports false
// once the frame has filled the terminal so an over-tall layout truncates at
// the bottom instead of scrolling the alternate screen — every scrolled row
// pushes the frame's top border off-screen, the classic clipped-TUI symptom
// on small terminal windows.
func (r *tuiRenderer) beginLine() bool {
	if r.maxRow > 0 && r.row >= r.maxRow {
		return false
	}
	r.row++
	return true
}

func (r *tuiRenderer) drawBorder(label string) {
	if !r.beginLine() {
		return
	}
	leftPadding := 2
	labelWidth := visibleWidth(label)
	rightPadding := r.width - labelWidth - leftPadding - 2
	if rightPadding < 0 {
		rightPadding = 0
	}
	r.buf.WriteString(r.theme.Border + "┌")
	r.buf.WriteString(strings.Repeat("─", leftPadding))
	r.buf.WriteString(r.theme.BorderLabel + " " + label + " ")
	r.buf.WriteString(r.theme.Border)
	r.buf.WriteString(strings.Repeat("─", rightPadding))
	r.buf.WriteString("┐\033[0m\n")
}

func (r *tuiRenderer) drawFooterBorder() {
	if !r.beginLine() {
		return
	}
	r.buf.WriteString(r.theme.Border + "└")
	r.buf.WriteString(strings.Repeat("─", r.width-2))
	r.buf.WriteString("┘\033[0m\n")
}

func (r *tuiRenderer) drawScholarLMBar(theme tuiTheme) {
	if r.width < 20 {
		return
	}
	if !r.beginLine() {
		return
	}
	r.buf.WriteString(scholarLMBrandingBarContent(r.width, theme) + "\n")
}

func (r *tuiRenderer) drawDivider() {
	if !r.beginLine() {
		return
	}
	r.buf.WriteString(r.theme.Border + "├")
	r.buf.WriteString(strings.Repeat("─", r.width-2))
	r.buf.WriteString("┤\033[0m\n")
}

func (r *tuiRenderer) scrollbarGutter() string {
	if !r.hasScrollbar {
		return ""
	}
	if !r.scrollbarTrack {
		return " "
	}
	lineIdx := r.drawnLinesCount
	h := (r.viewportHeight * r.viewportHeight) / r.totalLines
	if h < 1 {
		h = 1
	}
	y := 0
	if r.totalLines > r.viewportHeight {
		y = (r.scrollOffset * (r.viewportHeight - h)) / (r.totalLines - r.viewportHeight)
	}
	r.drawnLinesCount++
	if lineIdx >= y && lineIdx < y+h {
		return r.theme.Scrollbar + "█" + ansiReset
	}
	return r.theme.DimText + "░" + ansiReset
}

func (r *tuiRenderer) drawLine(content string, colorCode string) {
	if !r.beginLine() {
		return
	}
	gutter := r.scrollbarGutter()
	gutterWidth := visibleWidth(gutter)
	// │ + space + content + gutter + │ == width
	available := r.width - 3 - gutterWidth
	if available < 1 {
		available = 1
	}
	if visibleWidth(content) > available {
		// Pre-wrapped result lines should already fit; truncate only as a last resort without splitting ANSI.
		content = truncateVisible(content, available)
	}
	padding := available - visibleWidth(content)
	if padding < 0 {
		padding = 0
	}

	r.buf.WriteString(r.theme.Border + "│ \033[0m" + colorCode + content + "\033[0m" + strings.Repeat(" ", padding) + gutter + r.theme.Border + "│\033[0m\n")
}

func paneContentColumn(visibleOffset int) int {
	return 3 + visibleOffset
}

// renderTextInput draws the caret inside the line so it cannot drift from typed text.
func renderTextInput(prompt, value string, cursorPos int, active bool, theme tuiTheme) string {
	if cursorPos < 0 {
		cursorPos = 0
	}
	if cursorPos > len(value) {
		cursorPos = len(value)
	}
	color := theme.InputIdle
	if active {
		color = theme.InputActive
	}
	before := value[:cursorPos]
	after := value[cursorPos:]
	caret := ""
	if active {
		caret = "\033[7m|\033[0m" + color
	}
	return color + prompt + before + caret + after + ansiReset
}

func (r *tuiRenderer) drawHintLine(text string) {
	r.drawLine(" "+text, r.theme.DimText)
}

func sectionFocusPrefix(active bool) string {
	if active {
		return "› "
	}
	return "  "
}

func resultsViewportHeight(termHeight int, extraFooterRow bool) int {
	chrome := 10
	if extraFooterRow {
		chrome++
	}
	viewport := termHeight - chrome
	if viewport < 1 {
		return 1
	}
	return viewport
}

func (s *tuiState) focusLabel() string {
	if s.mode != modeInput {
		return ""
	}
	switch s.activeElement {
	case 0:
		return "question"
	case 1:
		return "providers"
	case 2:
		return "settings"
	case 3:
		return "save path"
	case 4:
		return "start"
	case 5:
		return "exit"
	default:
		return ""
	}
}

// flushFrame writes a fully-composed frame to the terminal without a
// full-screen clear. It erases each line's tail in place (\033[K) so a shorter
// new line cleanly overwrites a longer previous one — avoiding the flicker that
// a per-frame \033[2J causes on terminals lacking synchronized output, such as
// macOS Terminal.app — and drops the newline after the final row so the bottom
// line never lands on the bottom margin and scrolls the top of the frame
// off-screen. Callers must already terminate the frame with \033[J so any rows
// left over below a now-shorter frame are cleared.
func (s *tuiState) flushFrame(buf *bytes.Buffer) {
	// Lines end in \r\n, not bare \n: term.MakeRaw clears OPOST on unix, so
	// the tty stops translating LF into CRLF and a bare \n moves down a row
	// without returning the carriage — on macOS/Linux every line would start
	// where the previous one ended, staircasing the whole frame. Windows
	// ConPTY keeps output processing, which is why this only shows on mac.
	out := bytes.ReplaceAll(buf.Bytes(), []byte("\n"), []byte("\033[K\r\n"))
	if i := bytes.LastIndex(out, []byte("\033[K\r\n")); i >= 0 {
		// Replace the final "\033[K\r\n" with "\033[K": erase the last row's
		// tail but emit no newline, keeping the cursor off the bottom margin.
		tail := append([]byte("\033[K"), out[i+5:]...)
		out = append(out[:i:i], tail...)
	}
	// Bracket the frame in BSU/ESU so terminals with synchronized output
	// (iTerm2, Ghostty, WezTerm, kitty) repaint it atomically; terminals that
	// do not know the private mode ignore it.
	framed := make([]byte, 0, len(out)+16)
	framed = append(framed, "\033[?2026h"...)
	framed = append(framed, out...)
	framed = append(framed, "\033[?2026l"...)
	if s.output != nil {
		s.output.Write(framed)
	} else {
		os.Stdout.Write(framed)
	}
}

func (s *tuiState) render() {
	var width, height int
	if s.terminalSize != nil {
		w, h, err := s.terminalSize()
		if err == nil {
			width, height = w, h
		}
	}
	if width <= 0 || height <= 0 {
		width, height = 80, 24
	}
	if s.lastTerminalWidth != 0 && s.lastTerminalWidth != width {
		s.cachedResultLines = nil
	}
	s.lastTerminalWidth = width
	s.lastTerminalHeight = height

	if seq := s.terminalStatusSequence(time.Now()); seq != "" {
		if s.output != nil {
			io.WriteString(s.output, seq)
		} else {
			os.Stdout.WriteString(seq)
		}
	}

	var buf bytes.Buffer
	buf.WriteString("\033[H")

	if width < minTermWidth || height < minTermHeight {
		if s.mode == modeRunning {
			var compactBuf bytes.Buffer
			compactBuf.WriteString("\033[H")
			frame := tuiSpinnerFrame(time.Now())
			compactBuf.WriteString(fmt.Sprintf("%s Running... [Ctrl+C to cancel] %d papers found", frame, s.papersFound))
			compactBuf.WriteString("\033[J")
			s.flushFrame(&compactBuf)
			return
		}

		wStatus := "✗"
		if width >= minTermWidth {
			wStatus = "✓"
		}
		hStatus := "✗"
		if height >= minTermHeight {
			hStatus = "✓"
		}

		var boxBuf bytes.Buffer
		boxBuf.WriteString("\033[H")
		if width >= minTermWidth {
			// The 65-column box fits (we are here because the height is too
			// small), so render it as before.
			boxBuf.WriteString("┌─────────────────────────────────────────────────────────────┐\n")
			boxBuf.WriteString("│              Terminal window is too small!                  │\n")
			boxBuf.WriteString("├─────────────────────────────────────────────────────────────┤\n")
			boxBuf.WriteString(fmt.Sprintf("│  Width:  %3d / 65   %s                                       │\n", width, wStatus))
			boxBuf.WriteString(fmt.Sprintf("│  Height: %3d / 15   %s                                       │\n", height, hStatus))
			boxBuf.WriteString("│                                                             │\n")
			boxBuf.WriteString("│  Please enlarge your terminal window to resume.             │\n")
			boxBuf.WriteString("└─────────────────────────────────────────────────────────────┘\n")
		} else {
			// Too narrow for the fixed box; emit plain lines truncated to the
			// terminal width so nothing wraps and garbles the screen.
			for _, ln := range []string{
				"Terminal too small",
				fmt.Sprintf("Width  %d/%d %s", width, minTermWidth, wStatus),
				fmt.Sprintf("Height %d/%d %s", height, minTermHeight, hStatus),
				"Please enlarge the window.",
			} {
				boxBuf.WriteString(truncateVisible(ln, width))
				boxBuf.WriteString("\n")
			}
		}

		boxBuf.WriteString("\033[J")
		s.flushFrame(&boxBuf)
		return
	}

	theme := activeTheme()

	r := &tuiRenderer{
		buf:    &buf,
		width:  width,
		height: height,
		theme:  theme,
		// Reserve the bottom two rows for the brand and status bars so a
		// saturated layout cannot push them past the last row and scroll
		// the alternate screen (clipping the top border).
		maxRow: height - 2,
	}

	drawBorder := r.drawBorder
	drawFooterBorder := r.drawFooterBorder
	drawDivider := r.drawDivider
	drawLine := r.drawLine

	// Home the cursor only; per-line erase happens in flushFrame so we avoid the
	// full-screen \033[2J that flickers on terminals without synchronized output.
	buf.WriteString("\033[H")

	if s.showHelp {
		drawBorder("WisDev TUI — Keyboard Help")
		var lines []string
		lines = append(lines, "  "+theme.InputActive+"Global Keys:"+ansiReset)
		lines = append(lines, "    ?                   Show / hide this help overlay")
		lines = append(lines, "    Ctrl+C              Force exit application immediately")
		lines = append(lines, "")

		if s.mode == modeInput {
			lines = append(lines, "  "+theme.InputActive+"Input Mode Keys:"+ansiReset)
			lines = append(lines, "    Tab / Shift+Tab     Move focus between elements")
			lines = append(lines, "    Enter               Start research run (when start button/query focused)")
			lines = append(lines, "    Esc / q             Exit application (from main menu)")
			lines = append(lines, "    Up / Down / Ctrl+P  Browse query history browser / previous query")
			lines = append(lines, "    Ctrl+O              Browse recent saved runs (open in results view)")
			lines = append(lines, "    Ctrl+Z / Ctrl+Y     Undo / Redo query edits")
			lines = append(lines, "    Ctrl+Left / Right   Jump word-by-word in query/path input")
			lines = append(lines, "    Backspace / Delete  Delete characters")
			lines = append(lines, "")
			lines = append(lines, "  "+theme.InputActive+"Provider Grid Keys:"+ansiReset)
			lines = append(lines, "    Space               Toggle selected provider checkbox")
			lines = append(lines, "    a                   Toggle all providers on/off")
			lines = append(lines, "    b                   Apply biomedical provider preset")
			lines = append(lines, "    c                   Apply computer science provider preset")
			lines = append(lines, "    p                   Apply physics provider preset")
			lines = append(lines, "    g                   Apply general academic provider preset")
			lines = append(lines, "    x                   Apply preprints provider preset")
			lines = append(lines, "    /                   Filter provider grid list")
			lines = append(lines, "")
			lines = append(lines, "  "+theme.InputActive+"Settings Panel Keys:"+ansiReset)
			lines = append(lines, "    Space               Toggle selected boolean setting")
			lines = append(lines, "    1-9 / +/-           Set max iterations directly")
			lines = append(lines, "    Left / Right        Move between settings in the same row")
			lines = append(lines, "    Up / Down           Move between settings rows")
		} else if s.mode == modeRunning {
			lines = append(lines, "  "+theme.InputActive+"Running Mode Keys:"+ansiReset)
			lines = append(lines, "    Esc                 Cancel current research run immediately")
			lines = append(lines, "    P / p               Pause / resume research execution")
			lines = append(lines, "    k / Up              Scroll to older log lines")
			lines = append(lines, "    j / Down            Scroll to newer log lines")
		} else if s.mode == modeResults {
			lines = append(lines, "  "+theme.InputActive+"Results Mode Keys:"+ansiReset)
			lines = append(lines, "    Tab / [ / ]         Cycle results panes (All, Answer, Hypotheses, Queries, Sources, Compare, Reasoning)")
			lines = append(lines, "    v                   Toggle the reasoning trace view (ReAct timeline)")
			lines = append(lines, "    y                   Toggle the hypotheses & belief confidence view")
			lines = append(lines, "    j / k / d / u       Scroll results content (down / up / page down / page up)")
			lines = append(lines, "    g / G               Scroll to home (top) / scroll to end (bottom)")
			lines = append(lines, "    o                   Open the source paper behind an inline citation [n]")
			lines = append(lines, "    /                   Filter results content by term")
			lines = append(lines, "    n / N               Navigate next / previous match in filter")
			lines = append(lines, "    h                   Results Home: Go to All pane & reset scroll")
			lines = append(lines, "    s / e / b / t / w   Save markdown / JSON / BibTeX / CSV / HTML export")
			lines = append(lines, "    c                   Copy all results (markdown format) to clipboard")
			lines = append(lines, "    f                   Follow-up chat: ask about results, grounded in sources")
			lines = append(lines, "                        (in chat: Enter=ask, Ctrl+R=full research run, ESC=back)")
			lines = append(lines, "    r                   Re-run same query with current settings")
			lines = append(lines, "    R                   Go back to Input Mode to edit query/settings")
			lines = append(lines, "    E                   Re-run with Exhaustive (deep search) mode enabled")
			lines = append(lines, "    Enter / Esc / q     Return to Input Mode for a new search")
		}

		for _, line := range lines {
			drawLine(" "+line, theme.InputIdle)
		}
		for _, line := range scholarLMBrandingTUICallout(theme) {
			drawLine(" "+line, "")
		}
		r.drawHintLine("Press any key to close help")
		drawFooterBorder()
		buf.WriteString("\033[J")
		s.flushFrame(&buf)
		return
	}

	if s.showHistoryBrowser {
		drawBorder("Research History Browser")
		if len(s.history) == 0 {
			drawLine("  No search history yet.", theme.DimText)
		} else {
			displayCount := height - 8
			if displayCount < 1 {
				displayCount = 1
			}

			if s.historyBrowserIdx < 0 {
				s.historyBrowserIdx = 0
			}
			if s.historyBrowserIdx >= len(s.history) {
				s.historyBrowserIdx = len(s.history) - 1
			}

			startIndex := s.historyBrowserIdx - displayCount/2
			if startIndex < 0 {
				startIndex = 0
			}
			endIndex := startIndex + displayCount
			if endIndex > len(s.history) {
				endIndex = len(s.history)
				startIndex = endIndex - displayCount
				if startIndex < 0 {
					startIndex = 0
				}
			}

			for idx := startIndex; idx < endIndex; idx++ {
				entry := s.history[idx]
				timeStr := entry.Timestamp.Format("2006-01-02 15:04:05")
				line := fmt.Sprintf("  [%s] %s", timeStr, entry.Query)

				if idx == s.historyBrowserIdx {
					drawLine("> "+line, theme.InputActive)
				} else {
					drawLine("  "+line, theme.InputIdle)
				}
			}
		}
		r.drawHintLine("Up/Down navigate  Enter select  ESC/h close")
		drawFooterBorder()
		buf.WriteString("\033[J")
		s.flushFrame(&buf)
		return
	}

	if s.showRecentRuns {
		drawBorder("Recent Saved Runs")
		if len(s.recentRuns) == 0 {
			drawLine("  No saved runs found (wisdev-results/ or legacy wisdev-result-*.json).", theme.DimText)
		} else {
			if s.recentRunsIdx < 0 {
				s.recentRunsIdx = 0
			}
			if s.recentRunsIdx >= len(s.recentRuns) {
				s.recentRunsIdx = len(s.recentRuns) - 1
			}
			for idx, entry := range s.recentRuns {
				line := "  " + recentRunLabel(entry, width)
				if idx == s.recentRunsIdx {
					drawLine("> "+line, theme.InputActive)
				} else {
					drawLine("  "+line, theme.InputIdle)
				}
			}
		}
		r.drawHintLine("Up/Down navigate  Enter open in results view  ESC close")
		drawFooterBorder()
		buf.WriteString("\033[J")
		s.flushFrame(&buf)
		return
	}

	if s.showPaperDetail && s.result != nil && s.paperDetailIdx >= 0 && s.paperDetailIdx < len(s.result.Papers) {
		paper := s.result.Papers[s.paperDetailIdx]
		drawBorder("Paper Details")

		drawLine(" Title:", theme.StatusInfo)
		for _, wl := range wrapText(paper.Title, width-8) {
			drawLine("   "+hyperlinkedPaperTitle(paper, wl), theme.BorderLabel)
		}
		drawLine("", "")

		drawLine(" Authors:", theme.StatusInfo)
		authorsStr := strings.Join(paper.Authors, ", ")
		for _, wl := range wrapText(authorsStr, width-8) {
			drawLine("   "+wl, "")
		}
		drawLine("", "")

		if paper.Venue != "" || paper.Year > 0 {
			drawLine(fmt.Sprintf(" Venue: %s (%d)", paper.Venue, paper.Year), "")
		}
		if paper.CitationCount > 0 {
			drawLine(fmt.Sprintf(" Citations: %d", paper.CitationCount), "")
		}
		drawLine("", "")

		drawLine(" Links:", theme.StatusInfo)
		if paper.DOI != "" {
			doiUrl := formatPaperDOI(paper.DOI)
			drawLine(fmt.Sprintf("   DOI: \033]8;;%s\007%s\033]8;;\007 (%s)", doiUrl, paper.DOI, doiUrl), "")
		}
		if paper.ArxivID != "" {
			arxivUrl := "https://arxiv.org/abs/" + paper.ArxivID
			drawLine(fmt.Sprintf("   arXiv: \033]8;;%s\007%s\033]8;;\007", arxivUrl, paper.ArxivID), "")
		}
		if link := paperSourceURL(paper); link != "" {
			drawLine(fmt.Sprintf("   Source: \033]8;;%s\007%s\033]8;;\007", link, link), "")
		}
		drawLine("", "")

		if paper.Abstract != "" {
			drawLine(" Abstract:", theme.StatusInfo)
			for _, wl := range wrapText(paper.Abstract, width-8) {
				drawLine("   "+wl, "")
			}
		}

		r.drawHintLine("c=copy BibTeX  |  any key=close")
		drawFooterBorder()
		buf.WriteString("\033[J")
		s.flushFrame(&buf)
		return
	}

	if s.mode == modeInput {
		if s.showSessionRestorePrompt {
			drawBorder("Session Restore Prompt")
			drawLine(" Previous session found:", theme.StatusInfo)
			qPreview := truncateVisible(s.sessionQueryPreview, width-10)
			drawLine(fmt.Sprintf("   Query: %q", qPreview), theme.InputActive)
			drawLine("", "")
			drawLine(" Restore this session? [y = Yes, n = No/Discard]", theme.StatusWarn)
			r.drawHintLine("y=restore  n=discard")
			drawFooterBorder()
			buf.WriteString("\033[J")
			s.flushFrame(&buf)
			return
		}

		drawBorder(fmt.Sprintf("WisDev Research  v%s  ·  try ScholarLM", Version))
		// The full input layout needs ~25 rows and macOS Terminal.app defaults
		// to 80x24; below this height drop the decorative rows so the frame
		// fits the screen instead of truncating the controls at the bottom.
		compactInput := height < 30
		// Trident banner on the first screen only when there is vertical room;
		// it embeds the tagline, so the compact line is the short-terminal fallback.
		if height >= 32 {
			for _, line := range renderWisDevBanner(width-4, theme) {
				drawLine(" "+line, "")
			}
		} else {
			drawLine(" Plan · Search · Synthesize", theme.DimText)
		}
		if !compactInput {
			for _, line := range scholarLMBrandingTUICallout(theme) {
				drawLine(" "+line, "")
			}
		}
		drawDivider()

		queryPrompt := sectionFocusPrefix(s.activeElement == 0) + "Research Question: "
		if strings.TrimSpace(s.query) == "" && s.activeElement != 0 {
			drawLine(queryPrompt+"(Tab to focus, then type)", theme.DimText)
		} else {
			drawLine(renderTextInput(queryPrompt, s.query, s.cursorPos, s.activeElement == 0, theme), "")
		}

		if s.validationMsg != "" {
			drawLine(" "+s.validationMsg, theme.StatusError)
		}
		drawDivider()

		enabledCount := s.enabledProviderCount()
		presetHint := ""
		if s.biomedicalPresetActive() {
			presetHint = " [biomedical preset]"
		} else if s.csPresetActive() {
			presetHint = " [cs preset]"
		} else if s.physicsPresetActive() {
			presetHint = " [physics preset]"
		} else if s.generalPresetActive() {
			presetHint = " [general preset]"
		} else if s.preprintPresetActive() {
			presetHint = " [preprints preset]"
		}
		providerHeaderStyle := theme.StatusWarn
		if s.activeElement == 1 {
			providerHeaderStyle = theme.InputActive
		}
		if s.providerFiltering {
			drawLine(renderTextInput(sectionFocusPrefix(s.activeElement == 1)+"Filter: ", s.providerFilter, len(s.providerFilter), true, theme), "")
		} else {
			headerText := fmt.Sprintf("Search Providers: %d selected%s (Space toggle, a all, b/c/p/g/x presets)", enabledCount, presetHint)
			drawLine(sectionFocusPrefix(s.activeElement == 1)+headerText, providerHeaderStyle)
			if s.activeElement == 1 && !compactInput {
				drawLine("   Presets: b=biomedical  c=cs  p=physics  g=general  x=preprints", theme.HintText)
			}
		}

		if s.offlineMode {
			drawLine(" Offline mode enabled — providers disabled for smoke testing.", theme.StatusWarn)
		} else if enabledCount == 0 {
			drawLine(" No providers selected; run will use offline/no-search mode.", theme.StatusError)
		}

		columns := providerGridColumnsForWidth(width)

		matching := s.matchingProviders()
		s.clampProviderIdx()
		var provLine strings.Builder
		for idx, p := range matching {
			checkbox := "[ ]"
			if p.enabled && !s.offlineMode {
				checkbox = "[x]"
			}
			provColor := theme.ProviderOff
			if s.offlineMode {
				provColor = theme.DimText
			}
			if s.activeElement == 1 && s.providerIdx == idx {
				provColor = theme.ProviderFocus
			} else if p.enabled {
				provColor = theme.ProviderOn
			}
			dispName := providerDisplayName(p.name)
			if len(dispName) > 14 {
				dispName = dispName[:13] + "…"
			}
			icon := providerTypeIcon(p.code)
			dot := providerHealthDot(p.lastStatus)
			provLine.WriteString(fmt.Sprintf("%s %s[%s]%s %s%-14s\033[0m ", provColor, checkbox, icon, dot, provColor, dispName))

			if (idx+1)%columns == 0 || idx == len(matching)-1 {
				drawLine("   "+provLine.String(), "")
				provLine.Reset()
			}
		}
		if len(matching) == 0 {
			drawLine("   No matching search providers found.", theme.DimText)
		}
		drawDivider()

		settingsHeaderStyle := theme.StatusWarn
		if s.activeElement == 2 {
			settingsHeaderStyle = theme.InputActive
		}
		drawLine(sectionFocusPrefix(s.activeElement == 2)+"Run Settings: ←→ field  ↑↓ row  Space toggle  1-9/+- max iter", settingsHeaderStyle)

		highlightSetting := func(text string, setting int) string {
			if s.activeElement == 2 && s.activeSetting == setting {
				return theme.InputActive + text + ansiReset
			}
			return text
		}
		onOff := func(enabled bool) string {
			if enabled {
				return "on"
			}
			return "off"
		}

		rowOne := strings.Join([]string{
			highlightSetting(fmt.Sprintf(" Max iter: <%d>", s.maxIterations), 0),
			highlightSetting(fmt.Sprintf(" Planning: %s", onOff(!s.disablePlanning)), 1),
			highlightSetting(fmt.Sprintf(" Offline: %s", onOff(s.offlineMode)), 2),
		}, "   ")
		rowTwo := strings.Join([]string{
			highlightSetting(fmt.Sprintf(" Enhance: %s", onOff(s.enableQueryEnhance)), 3),
			highlightSetting(fmt.Sprintf(" Hypotheses: %s", onOff(s.enableHypotheses)), 4),
			highlightSetting(fmt.Sprintf(" Exhaustive: %s", onOff(s.deepSearch)), 5),
		}, "   ")
		rowThree := strings.Join([]string{
			highlightSetting(fmt.Sprintf(" Long-form: %s", onOff(s.longFormReport)), 6),
			highlightSetting(fmt.Sprintf(" DocGen: %s", onOff(s.generateDoc)), 7),
		}, "   ")

		drawLine("   "+rowOne, "")
		drawLine("   "+rowTwo, "")
		drawLine("   "+rowThree, "")
		if backend := strings.TrimSpace(s.llmBackend); backend != "" {
			drawLine("   LLM backend: "+backend, theme.DimText)
		}
		if s.enableQueryEnhance && strings.TrimSpace(s.query) != "" {
			preview := prepareResearchQuery(s.query)
			if preview.Changed || preview.Domain != "" {
				previewLine := " Preview:"
				if preview.Changed {
					previewLine += " " + preview.Corrected
				}
				if preview.Domain != "" {
					previewLine += " [domain=" + preview.Domain + "]"
				}
				drawLine("   "+previewLine, theme.DimText)
			}
		}
		var settingHint string
		if s.activeElement == 2 {
			switch s.activeSetting {
			case 0:
				settingHint = "Max iterations: Maximum number of research loops to run (1-12)."
			case 1:
				settingHint = "Planning: Enable/disable multi-step orchestrator task planning."
			case 2:
				settingHint = "Offline: Bypass search network requests for local-only execution."
			case 3:
				settingHint = "Enhance: correct spelling/grammar via AI with offline fallback before search."
			case 4:
				settingHint = "Hypotheses: Allow generating candidate hypotheses during research."
			case 5:
				settingHint = "Exhaustive: Run all max iterations without early convergence stops."
			case 6:
				settingHint = "Long-form: Write extended Introduction and Background sections in the report."
			case 7:
				settingHint = "DocGen: After research, generate a grounded manuscript from the retrieved papers and save it alongside the export."
			}
		} else {
			settingHint = "Exhaustive runs all max iterations before early stop."
		}
		if !compactInput {
			drawLine("   "+settingHint, theme.DimText)
		}
		drawDivider()

		pathPrompt := sectionFocusPrefix(s.activeElement == 3) + "Save Path: "
		if s.outputPath == "" && s.activeElement != 3 {
			autoPath := s.resolvedSavePath("md")
			drawLine(pathPrompt+autoPath+"  (auto)", theme.DimText)
		} else {
			drawLine(renderTextInput(pathPrompt, s.outputPath, s.outputPathCursorPos, s.activeElement == 3, theme), "")
		}
		drawDivider()

		startBtnStyle := theme.InputIdle + "[ Start Research ]"
		if s.activeElement == 4 {
			startBtnStyle = theme.BtnPrimary + "[ Start Research ]" + ansiReset
		}
		exitBtnStyle := theme.InputIdle + "[ Exit ]"
		if s.activeElement == 5 {
			exitBtnStyle = theme.BtnDanger + "[ Exit ]" + ansiReset
		}

		drawLine("     "+startBtnStyle+"       "+exitBtnStyle, "")
		r.drawHintLine(s.dynamicFooterShortcut() + "  |  ?=help")
		drawFooterBorder()

	} else if s.mode == modeRunning {
		runningTitle := "WisDev — Running"
		if batchLbl := s.batchProgressLabel(); batchLbl != "" {
			runningTitle = "WisDev — Running (" + batchLbl + ")"
		}
		drawBorder(runningTitle + "  ·  ScholarLM for the full UI")
		if s.batchMode && len(s.batchQueries) > 0 {
			drawLine(fmt.Sprintf(" Batch Progress: %s  |  Query: %s", s.batchProgressLabel(), s.runningTask), theme.BorderLabel)
		} else {
			drawLine(" Query: "+s.runningTask, theme.BorderLabel)
		}
		if domain := strings.TrimSpace(s.detectedDomain); domain != "" {
			drawLine(" Domain: "+domain, theme.StatusInfo)
		}
		if s.preparedQuery != "" && s.preparedQuery != s.originalQuery {
			drawLine(" Enhanced: "+s.preparedQuery, theme.DimText)
		}

		elapsedStr := fmt.Sprintf("%.1fs", s.elapsedTime.Seconds())
		frame := tuiSpinnerFrame(time.Now())

		s.logMutex.Lock()
		phase := inferRunPhaseFromLogs(s.logs, s.iterations)
		if s.paused {
			phase = "PAUSED"
		}
		s.logMutex.Unlock()

		row1 := fmt.Sprintf("  %s  Phase: %s  |  Elapsed: %s", frame, phase, elapsedStr)
		drawLine(row1, theme.StatusWarn)

		progress := renderProgressBar(s.iterations, s.requestedIterations, width-16, s.elapsedTime)
		drawLine("  Iteration "+progress, theme.StatusInfo)

		s.logMutex.Lock()
		providersStr := formatProviderCounts(s.providerCounts, width-30)
		if providersStr == "" {
			providersStr = strings.Join(s.executedProviders, ", ")
		}
		s.logMutex.Unlock()
		if providersStr == "" {
			providersStr = "none"
		}
		row3 := fmt.Sprintf("  Papers: %d  |  Queries: %d  |  Providers: %s", s.papersFound, s.executedQueries, providersStr)
		drawLine(row3, theme.DimText)
		if s.degradedSteps > 0 {
			drawLine(fmt.Sprintf("  ⚠ degraded: %d step(s) used heuristic fallbacks (LLM unavailable/limited)", s.degradedSteps), theme.StatusWarn)
		}
		if spark := renderSparkline(s.beliefScores); spark != "" {
			drawLine("  Convergence: "+spark, theme.DimText)
		}
		drawDivider()

		drawLine(" Live activity log:", theme.StatusInfo)
		s.logMutex.Lock()
		logCount := len(s.logs)
		maxLogsToDisplay := s.runningLogViewport(height)

		startIndex := logCount - maxLogsToDisplay - s.logScrollOffset
		if startIndex < 0 {
			startIndex = 0
		}
		endIndex := startIndex + maxLogsToDisplay
		if endIndex > logCount {
			endIndex = logCount
		}

		for i := startIndex; i < endIndex; i++ {
			entry := s.logs[i]
			color := theme.LogInfo
			tagStr := "[I]"
			switch entry.tag {
			case "E":
				color = theme.LogError
				tagStr = "[E]"
			case "W":
				color = theme.LogWarn
				tagStr = "[W]"
			case "D":
				color = theme.LogDebug
				tagStr = "[D]"
			}
			drawLine("  > "+tagStr+" "+entry.msg, color)
		}
		for i := endIndex - startIndex; i < maxLogsToDisplay; i++ {
			drawLine("", "")
		}
		s.logMutex.Unlock()

		r.drawHintLine(s.runningFooterShortcut())
		drawFooterBorder()

	} else if s.mode == modeResults && s.chatOn {
		drawBorder("WisDev — Follow-up Chat  ·  grounded in retrieved sources")
		sourceCount := 0
		if s.result != nil {
			sourceCount = len(s.result.Papers)
		}
		drawLine(fmt.Sprintf(" Answers cite the %d retrieved sources by [n]; outside knowledge is off.", sourceCount), theme.DimText)
		drawDivider()

		chatLines := buildChatLines(s, width-8)
		viewport := resultsViewportHeight(height, true) - 1
		if viewport < 3 {
			viewport = 3
		}
		maxOffset := len(chatLines) - viewport
		if maxOffset < 0 {
			maxOffset = 0
		}
		if s.chatScrollOffset > maxOffset {
			s.chatScrollOffset = maxOffset
		}
		start := len(chatLines) - viewport - s.chatScrollOffset
		if start < 0 {
			start = 0
		}
		end := start + viewport
		if end > len(chatLines) {
			end = len(chatLines)
		}
		for _, line := range chatLines[start:end] {
			drawLine(line, "")
		}
		for i := end - start; i < viewport; i++ {
			drawLine("", "")
		}

		drawDivider()
		drawLine(renderTextInput(" Ask: ", s.chatInput, s.chatCursorPos, true, theme), "")
		r.drawHintLine("Enter=ask  Ctrl+R=new research run from question  ↑/↓ PgUp/PgDn=scroll  ESC=back to results")
		drawFooterBorder()
	} else if s.mode == modeResults {
		drawBorder("WisDev — Results  ·  ScholarLM for cloud sync & review")
		drawLine(" "+renderResultPaneTabs(s, s.resultPane), "")
		drawDivider()
		lines := s.getResultLines(width)

		displayHeight := resultsViewportHeight(height, s.saveMsg != "" || s.resultFilterOn || s.citationJumpOn)

		r.hasScrollbar = len(lines) > displayHeight
		r.totalLines = len(lines)
		r.viewportHeight = displayHeight
		r.scrollOffset = s.scrollOffset
		r.drawnLinesCount = 0
		r.scrollbarTrack = false

		if s.scrollOffset > len(lines)-displayHeight {
			s.scrollOffset = len(lines) - displayHeight
		}
		if s.scrollOffset < 0 {
			s.scrollOffset = 0
		}

		endIdx := s.scrollOffset + displayHeight
		if endIdx > len(lines) {
			endIdx = len(lines)
		}

		r.scrollbarTrack = true
		for idx := s.scrollOffset; idx < endIdx; idx++ {
			line := lines[idx]
			lineStyle := ""
			if s.resultFilter != "" {
				plain := removeEscapeSequences(line)
				if fuzzyMatch(plain, s.resultFilter) {
					line = highlightFuzzyMatch(line, s.resultFilter)
				}
				if len(s.resultFilterMatch) > 0 && s.resultFilterCursor >= 0 && s.resultFilterCursor < len(s.resultFilterMatch) {
					if s.resultFilterMatch[s.resultFilterCursor] == idx {
						lineStyle = theme.InputActive
					}
				}
			}
			if s.resultPane == resultPaneSources && s.result != nil && len(s.result.Papers) > 0 {
				plain := removeEscapeSequences(line)
				paperIdx := -1
				if strings.HasPrefix(strings.TrimSpace(plain), "[") {
					parts := strings.Split(strings.TrimSpace(plain), "]")
					if len(parts) > 0 {
						numStr := strings.TrimPrefix(parts[0], "[")
						if num, err := strconv.Atoi(numStr); err == nil {
							paperIdx = num - 1
						}
					}
				}
				if paperIdx == s.paperDetailIdx {
					drawLine(line, theme.InputActive)
					continue
				}
			}
			drawLine(line, lineStyle)
		}
		for idx := endIdx - s.scrollOffset; idx < displayHeight; idx++ {
			drawLine("", "")
		}
		r.scrollbarTrack = false

		if s.citationJumpOn {
			drawDivider()
			drawLine(renderTextInput(" Open citation [n]: ", s.citationJumpInput, len(s.citationJumpInput), true, theme), "")
		} else if s.resultFilterOn {
			drawDivider()
			drawLine(renderTextInput(" Filter: ", s.resultFilter, len(s.resultFilter), true, theme), "")
		} else if s.saveMsg != "" {
			drawDivider()
			color := theme.StatusInfo
			if strings.HasPrefix(s.saveMsg, "Error") {
				color = theme.StatusError
			}
			drawLine(" "+s.saveMsg, color)
		}

		scrollHint := ""
		if s.resultFilter != "" {
			scrollHint = fmt.Sprintf("filtered: %q (%d matches)  |  ", s.resultFilter, len(s.resultFilterMatch))
		}
		if len(lines) > displayHeight {
			startLine := s.scrollOffset + 1
			endLine := endIdx
			if endLine < startLine {
				endLine = startLine
			}
			scrollHint += fmt.Sprintf("lines %d-%d of %d  |  ", startLine, endLine, len(lines))
		}
		r.drawHintLine(scrollHint + s.resultsFooterShortcut())
		drawFooterBorder()
	}

	drawStatusBar := func() {
		if !r.beginLine() {
			return
		}
		historyCount := len(s.history)
		timeStr := time.Now().Format("2006-01-02 15:04:05")

		var statusText string
		if s.mode == modeInput {
			statusText = fmt.Sprintf(" WisDev v%s  |  focus: %s  |  history: %d  |  %s  |  ?=help", Version, s.focusLabel(), historyCount, timeStr)
		} else if s.mode == modeRunning {
			batchPart := ""
			if lbl := s.batchProgressLabel(); lbl != "" {
				batchPart = lbl + "  |  "
			}
			statusText = fmt.Sprintf(" WisDev v%s  |  %srunning...  |  %s  |  ESC=abort", Version, batchPart, timeStr)
		} else {
			statusText = fmt.Sprintf(" WisDev v%s  |  results  |  %s  |  Enter=new search", Version, timeStr)
		}

		available := width
		// Measure by display width, not bytes: a byte-length slice can cut a
		// multibyte rune in half and mis-pads the reverse-video bar so it does
		// not span the full row.
		if visibleWidth(statusText) > available {
			statusText = truncateVisible(statusText, available)
		}
		padding := available - visibleWidth(statusText)
		if padding < 0 {
			padding = 0
		}
		buf.WriteString("\033[7m" + statusText + strings.Repeat(" ", padding) + "\033[0m\n")
	}
	// The two rows reserved by the content clamp belong to these bars;
	// raise the cap so they can draw, still guarded against overflow.
	r.maxRow = r.height
	r.drawScholarLMBar(theme)
	drawStatusBar()

	// Text fields render an inline caret; keep the hardware cursor hidden.
	buf.WriteString("\033[?25l\033[J")

	s.flushFrame(&buf)
}

func wrapText(text string, width int) []string {
	var lines []string
	paragraphs := strings.Split(text, "\n")
	for _, p := range paragraphs {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			lines = append(lines, "")
			continue
		}
		words := strings.Fields(trimmed)
		if len(words) == 0 {
			continue
		}
		currentLine := words[0]
		for _, word := range words[1:] {
			if visibleWidth(currentLine)+1+visibleWidth(word) > width {
				lines = append(lines, currentLine)
				currentLine = word
			} else {
				currentLine += " " + word
			}
		}
		lines = append(lines, currentLine)
	}
	return lines
}

func removeEscapeSequences(str string) string {
	var dst []byte
	i := 0
	n := len(str)
	for i < n {
		if str[i] == 27 { // ESC
			if i+1 < n {
				if str[i+1] == '[' { // CSI
					i += 2
					for i < n {
						c := str[i]
						i++
						if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
							break
						}
					}
					continue
				} else if str[i+1] == ']' { // OSC
					i += 2
					for i < n {
						if str[i] == 7 { // BEL
							i++
							break
						}
						if str[i] == 27 && i+1 < n && str[i+1] == '\\' { // ST
							i += 2
							break
						}
						i++
					}
					continue
				}
			}
			i++
			continue
		}
		dst = append(dst, str[i])
		i++
	}
	return string(dst)
}

func runeDisplayWidth(r rune) int {
	if r < 0x20 || r == 0x7f {
		return 0
	}
	// Zero-width: combining marks, variation selectors (FE0F picks the
	// emoji glyph), ZWSP/ZWNJ/ZWJ, word joiner. Counting these as 1
	// pads lines short and misaligns the right border on accented
	// titles and emoji.
	if r == 0x200B || r == 0x200C || r == 0x200D || r == 0x2060 || (r >= 0xFE00 && r <= 0xFE0F) {
		return 0
	}
	if unicode.In(r, unicode.Mn, unicode.Me) {
		return 0
	}
	switch {
	case r >= 0x1100 && r <= 0x115F,
		r >= 0x2E80 && r <= 0xA4CF,
		r >= 0xAC00 && r <= 0xD7A3,
		r >= 0xF900 && r <= 0xFAFF,
		r >= 0xFE10 && r <= 0xFE19,
		r >= 0xFE30 && r <= 0xFE6F,
		r >= 0xFF00 && r <= 0xFF60,
		r >= 0xFFE0 && r <= 0xFFE6:
		return 2
	case r >= 0x1F000 && r <= 0x1FAFF:
		return 2
	default:
		if unicode.Is(unicode.Han, r) {
			return 2
		}
		return 1
	}
}

// classifyMouseEvent reports whether a raw input chunk is a terminal mouse
// report and, if so, whether it encodes a wheel-up or wheel-down. It understands
// both the SGR encoding (ESC [ < Cb ; Cx ; Cy M|m) and the legacy X10 encoding
// (ESC [ M Cb Cx Cy). Non-wheel mouse events (clicks, drags, motion) return
// isMouse=true with both wheel flags false so the caller can swallow them rather
// than letting their bytes reach the key parser. Modifier bits (shift/meta/ctrl)
// are masked off before classifying the button.
func classifyMouseEvent(b []byte) (isMouse, wheelUp, wheelDown bool) {
	var code int
	switch {
	case len(b) >= 4 && b[0] == 27 && b[1] == '[' && b[2] == '<': // SGR
		i := 3
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			code = code*10 + int(b[i]-'0')
			i++
		}
		if i >= len(b) || b[i] != ';' { // not a well-formed SGR mouse report
			return false, false, false
		}
	case len(b) >= 6 && b[0] == 27 && b[1] == '[' && b[2] == 'M': // legacy X10
		code = int(b[3]) - 32
	default:
		return false, false, false
	}
	if code&0x40 == 0 { // bit 6 marks a wheel event; otherwise it's a button/move
		return true, false, false
	}
	switch code & 0x03 {
	case 0:
		return true, true, false // wheel up
	case 1:
		return true, false, true // wheel down
	default:
		return true, false, false // horizontal wheel: swallow
	}
}

func visibleWidth(str string) int {
	plain := removeEscapeSequences(str)
	width := 0
	for _, r := range plain {
		width += runeDisplayWidth(r)
	}
	return width
}

func padOrTruncateVisible(str string, width int) string {
	if width <= 0 {
		return ""
	}
	current := visibleWidth(str)
	if current > width {
		return truncateVisible(str, width)
	}
	if current < width {
		return str + strings.Repeat(" ", width-current)
	}
	return str
}

func truncateVisible(str string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(str)
	var result []rune
	visibleCount := 0
	i := 0
	n := len(runes)
	for i < n {
		if runes[i] == 27 { // ESC
			if i+1 < n {
				if runes[i+1] == '[' { // CSI
					result = append(result, runes[i], runes[i+1])
					i += 2
					for i < n {
						r := runes[i]
						result = append(result, r)
						i++
						if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
							break
						}
					}
					continue
				} else if runes[i+1] == ']' { // OSC
					result = append(result, runes[i], runes[i+1])
					i += 2
					for i < n {
						r := runes[i]
						result = append(result, r)
						if r == 7 { // BEL
							i++
							break
						}
						if r == 27 && i+1 < n && runes[i+1] == '\\' { // ST
							result = append(result, '\\')
							i += 2
							break
						}
						i++
					}
					continue
				}
			}
			result = append(result, runes[i])
			i++
			continue
		}

		r := runes[i]
		w := runeDisplayWidth(r)
		if visibleCount+w <= limit-1 {
			result = append(result, r)
			visibleCount += w
			i++
		} else if visibleCount < limit {
			// Check if there are more visible characters left
			hasMore := false
			tempI := i
			for tempI < n {
				if runes[tempI] == 27 {
					if tempI+1 < n {
						if runes[tempI+1] == '[' {
							tempI += 2
							for tempI < n {
								c := runes[tempI]
								tempI++
								if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
									break
								}
							}
							continue
						} else if runes[tempI+1] == ']' {
							tempI += 2
							for tempI < n {
								if runes[tempI] == 7 {
									tempI++
									break
								}
								if runes[tempI] == 27 && tempI+1 < n && runes[tempI+1] == '\\' {
									tempI += 2
									break
								}
								tempI++
							}
							continue
						}
					}
					tempI++
					continue
				}
				hasMore = true
				break
			}
			if hasMore {
				result = append(result, '…')
				result = append(result, []rune("\033[0m")...)
				// Close any OSC 8 hyperlink whose terminator was truncated
				// away, or everything drawn afterwards stays clickable. A
				// spare terminator on a balanced string is a no-op.
				if strings.Contains(str, "\033]8;") {
					result = append(result, []rune("\033]8;;\007")...)
				}
				break
			} else {
				result = append(result, r)
				visibleCount += w
				i++
			}
		} else {
			break
		}
	}
	return string(result)
}

func (s *tuiState) enabledProviderCount() int {
	count := 0
	for _, p := range s.providers {
		if p.enabled {
			count++
		}
	}
	return count
}

func (s *tuiState) focusNext() {
	s.activeElement = (s.activeElement + 1) % activeElementCount
	if s.activeElement == 1 {
		s.clampProviderIdx()
	}
	if !s.pendingExit {
		s.validationMsg = ""
	}
}

func (s *tuiState) focusPrevious() {
	s.activeElement = (s.activeElement - 1 + activeElementCount) % activeElementCount
	if s.activeElement == 1 {
		s.clampProviderIdx()
	}
	if !s.pendingExit {
		s.validationMsg = ""
	}
}

func providerGridColumnsForWidth(width int) int {
	colWidth := 24
	columns := (width - 6) / colWidth
	if columns < 2 {
		columns = 2
	}
	if columns > 4 {
		columns = 4
	}
	return columns
}

func (s *tuiState) providerGridColumns() int {
	width, _, err := s.currentTerminalSize()
	if err != nil || width <= 0 {
		width = 80
	}
	return providerGridColumnsForWidth(width)
}

func (s *tuiState) clampProviderIdx() {
	matching := s.matchingProviders()
	if len(matching) == 0 {
		s.providerIdx = 0
		return
	}
	if s.providerIdx >= len(matching) {
		s.providerIdx = len(matching) - 1
	}
	if s.providerIdx < 0 {
		s.providerIdx = 0
	}
}

func moveSettingRight(setting int) int {
	switch setting {
	case 0:
		return 1
	case 1:
		return 2
	case 2:
		return 0
	case 3:
		return 4
	case 4:
		return 5
	case 5:
		return 3
	case 6:
		return 7 // third row has two settings (Long-form, DocGen)
	case 7:
		return 6
	default:
		return setting
	}
}

func moveSettingLeft(setting int) int {
	switch setting {
	case 0:
		return 2
	case 1:
		return 0
	case 2:
		return 1
	case 3:
		return 5
	case 4:
		return 3
	case 5:
		return 4
	case 6:
		return 7 // third row has two settings (Long-form, DocGen)
	case 7:
		return 6
	default:
		return setting
	}
}

func moveSettingDown(setting int) int {
	if setting < settingsPerRow {
		return setting + settingsPerRow
	}
	if setting < 2*settingsPerRow {
		// Third row has two settings: Long-form (6) and DocGen (7). Map the first
		// two columns of the middle row down onto them; the third column clamps to 7.
		if next := setting + settingsPerRow; next <= 7 {
			return next
		}
		return 7
	}
	return setting
}

func moveSettingUp(setting int) int {
	if setting == 6 {
		return 3
	}
	if setting == 7 {
		return 4
	}
	if setting >= settingsPerRow {
		return setting - settingsPerRow
	}
	return setting
}

func shouldShowTUILog(msg string) bool {
	lower := strings.ToLower(msg)
	noisy := []string{
		"academic provider search lifecycle",
		"wisdev search query completed",
		"wisdev search batch started",
		"provider_dispatch",
		"provider=",
	}
	for _, fragment := range noisy {
		if strings.Contains(lower, fragment) {
			return false
		}
	}
	interesting := []string{
		"loop iteration",
		"starting autonomous",
		"query corrected",
		"query_prepared",
		"search_result_admitted",
		"search_batch",
		"synthesis",
		"sufficiency",
		"heuristic",
		"fallback",
		"hypothesis",
		"belief",
		"converged",
		"complete",
		"error",
		"cancelled",
		"initialis",
		"offline mode",
		"degraded",
	}
	for _, fragment := range interesting {
		if strings.Contains(lower, fragment) {
			return true
		}
	}
	return len(msg) <= maxFilterMsgLen
}

func renderProgressBar(value, max, width int, elapsed time.Duration) string {
	if max <= 0 {
		max = 1
	}
	if value < 0 {
		value = 0
	}
	if value > max {
		value = max
	}

	pct := (value * 100) / max

	etaStr := "ETA --"
	if value > 0 && value < max && elapsed > 0 {
		avgTime := elapsed / time.Duration(value)
		rem := max - value
		eta := avgTime * time.Duration(rem)
		secs := int(eta.Seconds())
		if secs < 60 {
			etaStr = fmt.Sprintf("ETA ~%ds", secs)
		} else {
			etaStr = fmt.Sprintf("ETA ~%dm%ds", secs/60, secs%60)
		}
	} else if value >= max {
		etaStr = "done"
	}

	extra := fmt.Sprintf(" %d%%  %s", pct, etaStr)
	barWidth := width - len(extra)
	if barWidth < 5 {
		barWidth = 5
	}

	filled := (value * barWidth) / max
	if filled > barWidth {
		filled = barWidth
	}

	var head string
	if filled > 0 && filled < barWidth && value < max {
		frames := []string{"▌", "▍", "▎", "▏"}
		frameIdx := int(time.Now().UnixNano()/250000000) % len(frames)
		head = frames[frameIdx]
		filled--
	}

	theme := activeTheme()
	filledPart := theme.Accent + strings.Repeat("█", filled) + head + ansiReset
	emptyPart := theme.DimText + strings.Repeat("░", barWidth-filled-len(head)) + ansiReset
	bar := "[" + filledPart + emptyPart + "]"
	return bar + extra
}

func (p tuiResultPane) next() tuiResultPane {
	return (p + 1) % 7
}

func (p tuiResultPane) prev() tuiResultPane {
	return (p + 6) % 7
}

func (p tuiResultPane) label() string {
	switch p {
	case resultPaneAnswer:
		return "Answer"
	case resultPaneHypotheses:
		return "Hypotheses"
	case resultPaneQueries:
		return "Queries"
	case resultPaneSources:
		return "Sources"
	case resultPaneCompare:
		return "Compare"
	case resultPaneReasoning:
		return "Reasoning"
	default:
		return "All"
	}
}

func renderResultPaneTabs(s *tuiState, active tuiResultPane) string {
	labels := s.availableResultPanes()
	var parts []string
	theme := activeTheme()
	for _, pane := range labels {
		label := pane.label()
		badge := ""
		if s.result != nil {
			switch pane {
			case resultPaneAll:
				badge = " ●"
			case resultPaneAnswer:
				lineCount := len(strings.Split(s.result.FinalAnswer, "\n"))
				if s.result.FinalAnswer != "" {
					badge = fmt.Sprintf(" (%d lines)", lineCount)
				}
			case resultPaneHypotheses:
				if count := len(s.result.Hypotheses); count > 0 {
					badge = fmt.Sprintf(" (%d)", count)
				}
			case resultPaneQueries:
				if count := len(s.result.ExecutedQueries); count > 0 {
					badge = fmt.Sprintf(" (%d)", count)
				}
			case resultPaneSources:
				if count := len(s.result.Papers); count > 0 {
					badge = fmt.Sprintf(" (%d)", count)
				}
			case resultPaneCompare:
				if s.prevResult != nil {
					badge = " ●"
				}
			case resultPaneReasoning:
				if count := len(s.result.ReasoningTrace); count > 0 {
					badge = fmt.Sprintf(" (%d)", count)
				}
			}
		}

		fullLabel := label + badge
		if pane == active {
			parts = append(parts, theme.TabActive+"["+fullLabel+"]"+ansiReset)
		} else {
			parts = append(parts, theme.TabInactive+fullLabel+ansiReset)
		}
	}
	return strings.Join(parts, "  ")
}

func inferRunPhase(logs []tuiLogEntry, iterations int) string {
	if len(logs) == 0 {
		return "Initialising"
	}
	last := strings.ToLower(logs[len(logs)-1].msg)
	switch {
	case strings.Contains(last, "hypothesis"), strings.Contains(last, "plan"), strings.Contains(last, "branch"):
		return "Planning"
	case strings.Contains(last, "search"), strings.Contains(last, "retriev"), strings.Contains(last, "query"), strings.Contains(last, "provider"):
		return "Searching"
	case strings.Contains(last, "synth"), strings.Contains(last, "answer"), strings.Contains(last, "belief"), strings.Contains(last, "verif"):
		return "Synthesizing"
	case iterations > 0:
		return fmt.Sprintf("Iteration %d", iterations)
	default:
		return "Running"
	}
}

func providerDisplayName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "arxiv":
		return "arXiv"
	case "openalex":
		return "OpenAlex"
	case "pubmed":
		return "PubMed"
	case "crossref":
		return "Crossref"
	case "semantic_scholar":
		return "Semantic Scholar"
	case "europe_pmc":
		return "Europe PMC"
	case "biorxiv":
		return "bioRxiv"
	case "medrxiv":
		return "medRxiv"
	case "clinical_trials":
		return "ClinicalTrials"
	case "doaj":
		return "DOAJ"
	default:
		if name == "" {
			return "unknown"
		}
		return name
	}
}

func parseCtrlArrow(b []byte) (int, bool) {
	if len(b) >= 6 && b[0] == 27 && b[1] == '[' && b[2] == '1' && b[3] == ';' && b[4] == '5' {
		switch b[5] {
		case 'C':
			return 1, true
		case 'D':
			return -1, true
		}
	}
	if len(b) == 4 && b[0] == 27 && b[1] == '[' && b[2] == '5' {
		switch b[3] {
		case 'C':
			return 1, true
		case 'D':
			return -1, true
		}
	}
	return 0, false
}
