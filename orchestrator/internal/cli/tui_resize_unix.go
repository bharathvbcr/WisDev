//go:build !windows

package cli

import (
	"os"
	"os/signal"
	"syscall"
)

// watchTerminalResize redraws the TUI as soon as the terminal window is
// resized. On unix-like systems the kernel delivers SIGWINCH on every resize;
// we forward it to the event loop as an eventResize. Without this, a hand-rolled
// full-screen TUI keeps painting at the old dimensions until the next keypress,
// which on terminals like macOS Terminal.app leaves the frame clipped or
// garbled after the window changes size.
func watchTerminalResize(events chan<- tuiEvent) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	go func() {
		for range ch {
			// Drop the resize if the loop is mid-render; the tick-based size
			// check is a backstop, so a coalesced signal is harmless.
			select {
			case events <- tuiEvent{eventType: eventResize}:
			default:
			}
		}
	}()
}
