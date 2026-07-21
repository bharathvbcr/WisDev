package cli

import (
	"fmt"
	"strings"
	"testing"

	agent "github.com/bharathvbcr/wisdev-arc/orchestrator/pkg/wisdev"
)

func chatTestResult() *agent.YOLOResult {
	return &agent.YOLOResult{
		FinalAnswer:   "Sleep consolidates memory.",
		OriginalQuery: "sleep and memory",
		Papers: []agent.Paper{
			{ID: "p1", Title: "Sleep Study", Abstract: "REM matters.", Authors: []string{"Ada"}, Year: 2024},
			{ID: "p2", Title: "Memory Review", Abstract: "Consolidation overview."},
		},
	}
}

func TestBuildChatSourceContextNumbersMatchSourcesPane(t *testing.T) {
	ctxText := buildChatSourceContext(chatTestResult())
	if !strings.Contains(ctxText, "[1] Sleep Study") {
		t.Fatalf("expected first source numbered [1]: %q", ctxText)
	}
	if !strings.Contains(ctxText, "[2] Memory Review") {
		t.Fatalf("expected second source numbered [2]: %q", ctxText)
	}
	if !strings.Contains(ctxText, "Authors: Ada") || !strings.Contains(ctxText, "Year: 2024") {
		t.Fatalf("expected bibliographic metadata in source context: %q", ctxText)
	}
}

func TestBuildChatSourceContextCapsSourceCount(t *testing.T) {
	result := &agent.YOLOResult{}
	for i := 0; i < maxChatSources+5; i++ {
		result.Papers = append(result.Papers, agent.Paper{Title: fmt.Sprintf("Paper %d", i+1)})
	}
	ctxText := buildChatSourceContext(result)
	if !strings.Contains(ctxText, fmt.Sprintf("[%d] Paper %d", maxChatSources, maxChatSources)) {
		t.Fatalf("expected source %d to be included", maxChatSources)
	}
	if strings.Contains(ctxText, fmt.Sprintf("[%d]", maxChatSources+1)) {
		t.Fatalf("expected sources beyond cap to be omitted: %q", ctxText)
	}
	if !strings.Contains(ctxText, "5 additional sources omitted") {
		t.Fatalf("expected omission notice: %q", ctxText)
	}
}

func TestBuildChatPromptIsGroundedAndConversational(t *testing.T) {
	history := []tuiChatMessage{
		{role: "you", text: "What about REM?"},
		{role: "wisdev", text: "REM matters [1]."},
	}
	prompt := buildChatPrompt(chatTestResult(), "sleep and memory", history, "And deep sleep?")

	for _, want := range []string{
		`"sleep and memory"`,
		"Sleep consolidates memory.",
		"[1] Sleep Study",
		"Researcher: What about REM?",
		"Assistant: REM matters [1].",
		`"And deep sleep?"`,
		"Answer ONLY from the numbered sources",
		"If the sources do not cover the question",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestBuildChatPromptHandlesMissingResult(t *testing.T) {
	prompt := buildChatPrompt(nil, "q", nil, "anything?")
	if !strings.Contains(prompt, "(no sources were retrieved)") {
		t.Fatalf("expected no-sources marker: %q", prompt)
	}
	if !strings.Contains(prompt, "(no synthesized answer available)") {
		t.Fatalf("expected no-answer marker: %q", prompt)
	}
}

func TestResetFollowUpChatInvalidatesInFlightReplies(t *testing.T) {
	s := &tuiState{
		chatOn:           true,
		chatInput:        "pending",
		chatHistory:      []tuiChatMessage{{role: "you", text: "q"}},
		chatBusy:         true,
		chatScrollOffset: 4,
	}
	genBefore := s.chatGen
	s.resetFollowUpChat()
	if s.chatOn || s.chatInput != "" || s.chatScrollOffset != 0 {
		t.Fatal("expected chat UI state to be cleared")
	}
	if s.chatIsBusy() {
		t.Fatal("expected busy flag to clear")
	}
	if len(s.chatTranscript()) != 0 {
		t.Fatal("expected transcript to be cleared")
	}
	if s.chatGen != genBefore+1 {
		t.Fatalf("expected generation bump, got %d -> %d", genBefore, s.chatGen)
	}
}

func TestCurrentResultQueryPrefersResultRecord(t *testing.T) {
	s := &tuiState{originalQuery: "stale query", result: chatTestResult()}
	if got := s.currentResultQuery(); got != "sleep and memory" {
		t.Fatalf("expected result's original query, got %q", got)
	}
	s.result = nil
	if got := s.currentResultQuery(); got != "stale query" {
		t.Fatalf("expected fallback to state query, got %q", got)
	}
}
