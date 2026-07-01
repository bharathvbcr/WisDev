package cli

import "strings"

var suggestCommands = []string{
	"search", "ask", "run", "yolo", "max", "docgen", "docugen", "demo", "mcp", "mcp-config", "setup",
	"doctor", "check", "providers", "sources", "version", "serve", "help", "tui", "ui",
	"update", "upgrade", "guide", "commands",
}

func suggestCommand(input string) string {
	input = strings.ToLower(strings.TrimSpace(input))
	if input == "" {
		return ""
	}

	best := ""
	bestDistance := 3
	for _, command := range suggestCommands {
		d := levenshtein(input, command)
		if d < bestDistance {
			bestDistance = d
			best = command
		}
	}
	if best == "" {
		return ""
	}
	return best
}

func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}

	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := 0; j <= len(b); j++ {
		prev[j] = j
	}

	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = minInt(
				curr[j-1]+1,
				prev[j]+1,
				prev[j-1]+cost,
			)
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

func minInt(values ...int) int {
	if len(values) == 0 {
		return 0
	}
	min := values[0]
	for _, value := range values[1:] {
		if value < min {
			min = value
		}
	}
	return min
}
