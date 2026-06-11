//go:build !windows

package cli

func setConsoleTitleNative(string) {}

func getConsoleTitleNative() string { return "" }
