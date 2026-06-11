package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/llm"
	llmv1 "github.com/bharathvbcr/wisdev-arc/orchestrator/proto/llm"
	agent "github.com/bharathvbcr/wisdev-arc/orchestrator/pkg/wisdev"
)

// Follow-up chat: a results-mode panel that answers questions about the
// finished run conversationally, grounded only in the already-retrieved
// sources. Escalating to a fresh research run stays one keystroke away
// (Ctrl+R) for questions the current corpus cannot answer.

const (
	maxChatSources       = 12
	maxChatSourceSummary = 400
	maxChatAnswerContext = 4000
	maxChatHistoryTurns  = 6
	maxChatMessages      = 40
	chatRequestTimeout   = 60 * time.Second
)

type tuiChatMessage struct {
	role string // "you" | "wisdev" | "error"
	text string
}

// buildChatSourceContext renders the retrieved papers as a numbered source
// list whose [n] indices match the Sources pane, so chat citations stay
// consistent with the rest of the results view.
func buildChatSourceContext(result *agent.YOLOResult) string {
	if result == nil || len(result.Papers) == 0 {
		return "(no sources were retrieved)"
	}
	var b strings.Builder
	for i, paper := range result.Papers {
		if i >= maxChatSources {
			fmt.Fprintf(&b, "(%d additional sources omitted)\n", len(result.Papers)-maxChatSources)
			break
		}
		summary := strings.TrimSpace(paper.Abstract)
		if len(summary) > maxChatSourceSummary {
			summary = summary[:maxChatSourceSummary] + "…"
		}
		meta := ""
		if len(paper.Authors) > 0 {
			meta = " | Authors: " + strings.Join(paper.Authors, ", ")
		}
		if paper.Year > 0 {
			meta += fmt.Sprintf(" | Year: %d", paper.Year)
		}
		fmt.Fprintf(&b, "[%d] %s%s\nSummary: %s\n\n", i+1, strings.TrimSpace(paper.Title), meta, summary)
	}
	return b.String()
}

// buildChatPrompt assembles the grounded follow-up prompt from the original
// question, the synthesized answer, the numbered sources, and recent turns.
func buildChatPrompt(result *agent.YOLOResult, originalQuery string, history []tuiChatMessage, question string) string {
	answer := ""
	if result != nil {
		answer = strings.TrimSpace(result.FinalAnswer)
	}
	if len(answer) > maxChatAnswerContext {
		answer = answer[:maxChatAnswerContext] + "…"
	}
	if answer == "" {
		answer = "(no synthesized answer available)"
	}

	var convo strings.Builder
	start := len(history) - maxChatHistoryTurns*2
	if start < 0 {
		start = 0
	}
	for _, msg := range history[start:] {
		switch msg.role {
		case "you":
			convo.WriteString("Researcher: " + msg.text + "\n")
		case "wisdev":
			convo.WriteString("Assistant: " + msg.text + "\n")
		}
	}
	if convo.Len() == 0 {
		convo.WriteString("(no prior follow-up turns)\n")
	}

	return fmt.Sprintf(`You are a research assistant answering follow-up questions about a completed literature research run.

Original research question: %q

Synthesized report:
%s

Numbered sources (the ONLY evidence you may use):
%s

Conversation so far:
%s
New follow-up question: %q

Instructions:
1. Answer ONLY from the numbered sources above. Do not use outside knowledge and do not invent studies, statistics, authors, or effect sizes.
2. Cite the supporting source after each factual claim using its bracketed number, e.g. [2] or [1][3].
3. If the sources do not cover the question, say so plainly and suggest rephrasing or running a new research pass instead of guessing.
4. Be concise and direct by default: a few short paragraphs or a brief list. If the researcher explicitly asks for longer-form writing (for example an extended introduction, background, or related-work section), write the longer piece they asked for — still grounded in the numbered sources with [n] citations.`,
		strings.TrimSpace(originalQuery), answer, buildChatSourceContext(result), convo.String(), strings.TrimSpace(question))
}

// askFollowUpChat records the question and answers it asynchronously from the
// already-retrieved sources, notifying the event loop when the reply lands.
func (s *tuiState) askFollowUpChat(parentCtx context.Context, question string) {
	question = strings.TrimSpace(question)
	if question == "" {
		return
	}
	s.logMutex.Lock()
	if s.chatBusy {
		s.logMutex.Unlock()
		return
	}
	s.chatHistory = append(s.chatHistory, tuiChatMessage{role: "you", text: question})
	if len(s.chatHistory) > maxChatMessages {
		s.chatHistory = s.chatHistory[len(s.chatHistory)-maxChatMessages:]
	}
	s.chatBusy = true
	gen := s.chatGen
	s.logMutex.Unlock()
	s.chatScrollOffset = 0

	prompt := buildChatPrompt(s.result, s.currentResultQuery(), s.chatTranscript(), question)
	go func() {
		ctx, cancel := context.WithTimeout(parentCtx, chatRequestTimeout)
		defer cancel()
		client := resolveResearchLLMClient()
		resp, err := client.Generate(ctx, &llmv1.GenerateRequest{
			Prompt: prompt,
			Model:  llm.ResolveLightModel(),
		})

		var reply tuiChatMessage
		if err != nil {
			reply = tuiChatMessage{role: "error", text: "Follow-up failed: " + err.Error()}
		} else if text := strings.TrimSpace(resp.GetText()); text != "" {
			reply = tuiChatMessage{role: "wisdev", text: text}
		} else {
			reply = tuiChatMessage{role: "error", text: "Follow-up returned an empty response; try rephrasing."}
		}

		s.logMutex.Lock()
		if s.chatGen == gen { // drop replies that outlived a chat reset
			s.chatHistory = append(s.chatHistory, reply)
			s.chatBusy = false
		}
		s.logMutex.Unlock()
		select {
		case s.eventCh <- tuiEvent{eventType: eventRunUpdate}:
		default:
		}
	}()
}

// currentResultQuery returns the research question behind the result on
// screen, preferring the result's own record so runs loaded from disk chat
// against the right question.
func (s *tuiState) currentResultQuery() string {
	if s.result != nil {
		if q := strings.TrimSpace(s.result.OriginalQuery); q != "" {
			return q
		}
	}
	return strings.TrimSpace(s.originalQuery)
}

// chatIsBusy reports whether a follow-up reply is still in flight.
func (s *tuiState) chatIsBusy() bool {
	s.logMutex.Lock()
	defer s.logMutex.Unlock()
	return s.chatBusy
}

// chatTranscript returns a snapshot of the chat history safe to read outside
// the event loop.
func (s *tuiState) chatTranscript() []tuiChatMessage {
	s.logMutex.Lock()
	defer s.logMutex.Unlock()
	out := make([]tuiChatMessage, len(s.chatHistory))
	copy(out, s.chatHistory)
	return out
}

func (s *tuiState) resetFollowUpChat() {
	s.logMutex.Lock()
	s.chatHistory = nil
	s.chatBusy = false
	s.chatGen++
	s.logMutex.Unlock()
	s.chatOn = false
	s.chatInput = ""
	s.chatCursorPos = 0
	s.chatScrollOffset = 0
}

// buildChatLines renders the transcript into wrapped, styled terminal lines.
func buildChatLines(s *tuiState, width int) []string {
	theme := activeTheme()
	if width < 20 {
		width = 20
	}
	transcript := s.chatTranscript()

	var lines []string
	if len(transcript) == 0 {
		lines = append(lines,
			theme.DimText+" Ask anything about this run's findings — answers cite the"+ansiReset,
			theme.DimText+" retrieved sources by [n]. Ctrl+R turns the typed question"+ansiReset,
			theme.DimText+" into a full new research run instead."+ansiReset,
		)
		return lines
	}
	for _, msg := range transcript {
		prefix := "You:    "
		style := theme.InputActive
		switch msg.role {
		case "wisdev":
			prefix = "WisDev: "
			style = ""
		case "error":
			prefix = "Error:  "
			style = theme.StatusError
		}
		wrapped := wrapText(msg.text, width-len(prefix)-1)
		for i, line := range wrapped {
			lead := prefix
			if i > 0 {
				lead = strings.Repeat(" ", len(prefix))
			}
			if style != "" {
				lines = append(lines, " "+style+lead+line+ansiReset)
			} else {
				lines = append(lines, " "+lead+line)
			}
		}
		lines = append(lines, "")
	}
	s.logMutex.Lock()
	busy := s.chatBusy
	s.logMutex.Unlock()
	if busy {
		lines = append(lines, " "+theme.DimText+"WisDev: thinking…"+ansiReset)
	}
	return lines
}
