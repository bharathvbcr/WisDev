package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/config"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/storage"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
)

var (
	version = "dev"

	styleTitle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#7C3AED")).
			MarginBottom(1)

	styleSubtitle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#9CA3AF")).
			MarginBottom(1)

	styleSuccess = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#10B981")).
			Bold(true)

	styleError = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#EF4444")).
			Bold(true)

	styleInfo = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#3B82F6"))

	styleDim = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6B7280"))

	styleLabel = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#A78BFA")).
			Bold(true)

	styleValue = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#E5E7EB"))

	styleBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#4B5563")).
			Padding(1, 2).
			Margin(1)

	styleTableHeader = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#A78BFA")).
				Padding(0, 2)

	styleTableCell = lipgloss.NewStyle().
			Padding(0, 2)
)

func loadConfig(path string) (*config.Config, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	return config.Load(path)
}

func printBanner() {
	fmt.Println(styleTitle.Render("⚡ WisDev Research Agent"))
	fmt.Println(styleSubtitle.Render("Terminal-first AI research assistant"))
	fmt.Println()
}

func printConfig(cfg *config.Config) {
	apiKeyMasked := "***"
	if cfg.LLM.APIKey != "" && len(cfg.LLM.APIKey) > 8 {
		apiKeyMasked = cfg.LLM.APIKey[:4] + "..." + cfg.LLM.APIKey[len(cfg.LLM.APIKey)-4:]
	}
	if cfg.LLM.APIKey == "" {
		apiKeyMasked = styleError.Render("(not set)")
	}

	box := styleBox.Render(
		styleLabel.Render("Provider: ") + styleValue.Render(cfg.LLM.Provider) + "\n" +
			styleLabel.Render("Model: ") + styleValue.Render(cfg.LLM.Model) + "\n" +
			styleLabel.Render("API Key: ") + styleValue.Render(apiKeyMasked) + "\n" +
			styleLabel.Render("Storage: ") + styleValue.Render(cfg.Storage.Type) + "\n" +
			styleLabel.Render("Mode: ") + styleValue.Render(cfg.Agent.Mode) + "\n" +
			styleLabel.Render("Max Steps: ") + styleValue.Render(fmt.Sprintf("%d", cfg.Agent.MaxSteps)) + "\n" +
			styleLabel.Render("Token Budget: ") + styleValue.Render(fmt.Sprintf("%d", cfg.Agent.TokenBudget)) + "\n" +
			styleLabel.Render("OTel: ") + styleValue.Render(fmt.Sprintf("%v", cfg.Observability.EnableOTEL)),
	)
	fmt.Println(box)
}

func printSessions(sessions []*storage.Session) {
	if len(sessions) == 0 {
		fmt.Println(styleDim.Render("No sessions found."))
		return
	}

	headers := []string{"Session ID", "User", "Status", "Created", "Updated"}
	rows := make([][]string, len(sessions))
	for i, s := range sessions {
		id := s.SessionID
		if len(id) > 12 {
			id = id[:12] + "..."
		}
		rows[i] = []string{
			id,
			s.UserID,
			s.Status,
			s.CreatedAt.Format("2006-01-02 15:04"),
			s.UpdatedAt.Format("2006-01-02 15:04"),
		}
	}

	widths := make([]int, len(headers))
	for j, h := range headers {
		widths[j] = len(h)
		for _, r := range rows {
			if len(r[j]) > widths[j] {
				widths[j] = len(r[j])
			}
		}
	}

	headerRow := make([]string, len(headers))
	for j, h := range headers {
		headerRow[j] = styleTableHeader.Render(h)
	}
	fmt.Println(lipgloss.JoinHorizontal(lipgloss.Top, headerRow...))

	for _, r := range rows {
		cells := make([]string, len(r))
		for j, c := range r {
			cells[j] = styleTableCell.Render(c)
		}
		fmt.Println(lipgloss.JoinHorizontal(lipgloss.Top, cells...))
	}
}

func printHelp() {
	fmt.Println(styleTitle.Render("⚡ WisDev Research Agent"))
	fmt.Println()
	fmt.Println(styleLabel.Render("USAGE:"))
	fmt.Println("  wisdev <command> [flags]")
	fmt.Println()
	fmt.Println(styleLabel.Render("COMMANDS:"))
	fmt.Println("  run       Execute a research task")
	fmt.Println("  server    Start the WisDev HTTP/gRPC server")
	fmt.Println("  session   Manage research sessions")
	fmt.Println("  config    Show or validate configuration")
	fmt.Println("  version   Show version information")
	fmt.Println()
	fmt.Println(styleLabel.Render("EXAMPLES:"))
	fmt.Println("  wisdev run -q \"CRISPR gene therapy for sickle cell\"")
	fmt.Println("  wisdev run -q \"quantum error correction\" --mode guided")
	fmt.Println("  wisdev server --port 8081")
	fmt.Println("  wisdev session list")
	fmt.Println("  wisdev config validate")
	fmt.Println()
	fmt.Println(styleDim.Render("Run 'wisdev <command> --help' for more information on a command."))
}

type researchProgressMsg struct {
	phase      string
	iterations int
	papers     int
	message    string
}

type researchModel struct {
	spinner    spinner.Model
	query      string
	config     *config.Config
	sessionID  string
	status     string
	phase      string
	iterations int
	papers     int
	message    string
	startTime  time.Time
	done       bool
	err        error
	result     *wisdev.LoopResult
}

func initialResearchModel(query string, cfg *config.Config) researchModel {
	s := spinner.New()
	s.Spinner = spinner.Points
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("#7C3AED"))

	return researchModel{
		spinner:   s,
		query:     query,
		config:    cfg,
		sessionID: wisdev.NewTraceID(),
		startTime: time.Now(),
		phase:     "initializing",
	}
}

func (m researchModel) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		func() tea.Msg {
			llmClient := llm.NewClient()
			searchReg := search.BuildRegistry()
			loop := wisdev.NewAutonomousLoop(searchReg, llmClient)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()

			result, err := loop.Run(ctx, wisdev.LoopRequest{
				Query:         m.query,
				ProjectID:     m.sessionID,
				MaxIterations: m.config.Agent.MaxSteps,
			})
			if err != nil {
				return researchErrMsg{err}
			}
			return researchDoneMsg{result: result}
		},
	)
}

func (m researchModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC || msg.String() == "q" {
			return m, tea.Quit
		}
		return m, nil
	case researchDoneMsg:
		m.done = true
		m.result = msg.result
		if msg.result != nil {
			m.status = "complete"
			m.iterations = msg.result.Iterations
			m.papers = len(msg.result.Papers)
		}
		return m, tea.Quit
	case researchErrMsg:
		m.done = true
		m.err = msg.err
		m.status = "failed"
		return m, tea.Quit
	case researchProgressMsg:
		m.phase = msg.phase
		m.iterations = msg.iterations
		m.papers = msg.papers
		m.message = msg.message
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	default:
		return m, nil
	}
}

func (m researchModel) View() string {
	var b strings.Builder

	b.WriteString(styleTitle.Render("⚡ WisDev Research Agent"))
	b.WriteString("\n")
	b.WriteString(styleSubtitle.Render(fmt.Sprintf("Query: %s", m.query)))
	b.WriteString("\n\n")

	if !m.done {
		b.WriteString(m.spinner.View())
		b.WriteString(" ")
		b.WriteString(styleInfo.Render(m.phase))
		b.WriteString("\n\n")

		if m.message != "" {
			b.WriteString(styleDim.Render(m.message))
			b.WriteString("\n\n")
		}

		b.WriteString(styleDim.Render(fmt.Sprintf("Session: %s", m.sessionID)))
		b.WriteString("\n")
		b.WriteString(styleDim.Render(fmt.Sprintf("Elapsed: %s", time.Since(m.startTime).Round(time.Second))))

		if m.iterations > 0 {
			b.WriteString("\n")
			b.WriteString(styleDim.Render(fmt.Sprintf("Iterations: %d/%d", m.iterations, m.config.Agent.MaxSteps)))
		}
		if m.papers > 0 {
			b.WriteString("\n")
			b.WriteString(styleDim.Render(fmt.Sprintf("Papers found: %d", m.papers)))
		}
	} else if m.err != nil {
		b.WriteString(styleError.Render("✖ Research failed"))
		b.WriteString("\n")
		b.WriteString(styleDim.Render(m.err.Error()))
	} else {
		b.WriteString(styleSuccess.Render("✔ Research complete"))
		b.WriteString("\n\n")

		b.WriteString(styleLabel.Render("Session: ") + styleValue.Render(m.sessionID))
		b.WriteString("\n")
		b.WriteString(styleLabel.Render("Duration: ") + styleValue.Render(time.Since(m.startTime).Round(time.Second).String()))
		if m.result != nil {
			b.WriteString("\n")
			b.WriteString(styleLabel.Render("Iterations: ") + styleValue.Render(fmt.Sprintf("%d", m.result.Iterations)))
			b.WriteString("\n")
			b.WriteString(styleLabel.Render("Papers found: ") + styleValue.Render(fmt.Sprintf("%d", len(m.result.Papers))))
		}
	}

	b.WriteString("\n\n")
	b.WriteString(styleDim.Render("Press q or Ctrl+C to exit"))

	return b.String()
}

type researchDoneMsg struct{ result *wisdev.LoopResult }
type researchErrMsg struct{ err error }

func runResearch(query string, cfg *config.Config) error {
	model := initialResearchModel(query, cfg)
	p := tea.NewProgram(model, tea.WithAltScreen())
	finalModel, err := p.Run()
	if err != nil {
		return err
	}
	rm := finalModel.(researchModel)
	if rm.err != nil {
		return rm.err
	}
	return nil
}

func runResearchNoTUI(query string, cfg *config.Config) error {
	fmt.Println(styleInfo.Render("Starting research..."))
	fmt.Println(styleDim.Render(fmt.Sprintf("Query: %s", query)))
	fmt.Println(styleDim.Render(fmt.Sprintf("Session: %s", wisdev.NewTraceID())))
	fmt.Println()

	llmClient := llm.NewClient()
	searchReg := search.BuildRegistry()
	loop := wisdev.NewAutonomousLoop(searchReg, llmClient)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	result, err := loop.Run(ctx, wisdev.LoopRequest{
		Query:         query,
		ProjectID:     wisdev.NewTraceID(),
		MaxIterations: cfg.Agent.MaxSteps,
	})
	if err != nil {
		return err
	}

	fmt.Println()
	fmt.Println(styleSuccess.Render("✔ Research complete"))
	if result != nil {
		fmt.Println(styleLabel.Render("Iterations: ") + styleValue.Render(fmt.Sprintf("%d", result.Iterations)))
		fmt.Println(styleLabel.Render("Papers found: ") + styleValue.Render(fmt.Sprintf("%d", len(result.Papers))))
	}
	return nil
}
