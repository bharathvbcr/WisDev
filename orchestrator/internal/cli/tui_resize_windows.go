//go:build windows

package cli

// watchTerminalResize is a no-op on Windows: the console has no SIGWINCH, and
// ConPTY does not surface resize as a signal. The TUI event loop instead polls
// the terminal size on each tick and redraws when it changes, which covers
// resize on this platform.
func watchTerminalResize(events chan<- tuiEvent) {}
