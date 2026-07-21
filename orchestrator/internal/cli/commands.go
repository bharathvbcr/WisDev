package cli

import "strings"

var commandAliases = map[string]string{
	"ask":     "search",
	"check":   "doctor",
	"sources": "providers",
	"setup":   "mcp-config",
	"ui":       "tui",
	"upgrade":  "update",
	"commands": "guide",
	"docugen":  "docgen",
}

func isKnownCommand(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if _, ok := commandAliases[name]; ok {
		return true
	}
	switch name {
	case "search", "run", "yolo", "max", "docgen", "mcp", "mcp-config",
		"doctor", "providers", "version", "serve", "stack", "help", "tui", "update", "guide":
		return true
	default:
		return false
	}
}

func looksLikeFlag(arg string) bool {
	return strings.HasPrefix(arg, "-")
}

// normalizeInvocation applies aliases and treats a bare question as `search`.
func normalizeInvocation(args []string) []string {
	if len(args) == 0 {
		return args
	}

	first := strings.ToLower(strings.TrimSpace(args[0]))
	if command, ok := commandAliases[first]; ok {
		args[0] = command
		return args
	}
	if isKnownCommand(first) {
		return args
	}
	if looksLikeFlag(args[0]) {
		return append([]string{"search"}, args...)
	}
	if len(args) == 1 && suggestCommand(first) != "" {
		return args
	}

	return append([]string{"search"}, args...)
}
