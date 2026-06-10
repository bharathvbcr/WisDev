package main

import (
	"fmt"
	"os"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/cli"
)

var version = "dev"

func main() {
	cli.Version = version
	if err := cli.Run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "wisdev:", err)
		os.Exit(1)
	}
}
