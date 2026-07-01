package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunGuideListsEveryCommand(t *testing.T) {
	var out bytes.Buffer
	if err := runGuide(&out); err != nil {
		t.Fatalf("runGuide: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"wisdev \"question\"", "wisdev yolo", "wisdev max", "wisdev docgen", "wisdev demo",
		"wisdev tui", "wisdev mcp", "wisdev setup", "wisdev serve",
		"wisdev check", "wisdev sources", "wisdev version", "wisdev update",
		"wisdev help", "wisdev guide", "WISDEV_UNLEASHED",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("guide output missing %q", want)
		}
	}
}

func TestMaxAndGuideCommandWiring(t *testing.T) {
	for _, cmd := range []string{"max", "guide", "commands"} {
		if !isKnownCommand(cmd) {
			t.Errorf("%q should be a known command", cmd)
		}
	}

	// `wisdev max "question"` must dispatch to max, not be rewritten into a
	// bare search for the words "max question".
	args := normalizeInvocation([]string{"max", "some question"})
	if args[0] != "max" {
		t.Fatalf("max invocation rewritten: %v", args)
	}

	args = normalizeInvocation([]string{"commands"})
	if args[0] != "guide" {
		t.Fatalf("commands alias not normalized to guide: %v", args)
	}

	for _, topic := range []string{"max", "guide", "commands"} {
		if _, ok := commandHelp[topic]; !ok {
			t.Errorf("missing help topic for %q", topic)
		}
	}
}
