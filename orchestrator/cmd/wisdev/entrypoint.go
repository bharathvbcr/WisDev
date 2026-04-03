package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		printHelp()
		os.Exit(0)
	}

	switch os.Args[1] {
	case "version":
		fmt.Println(styleTitle.Render("⚡ WisDev Research Agent"))
		fmt.Printf("Version: %s\n", version)
	case "run":
		handleRun(os.Args[2:])
	case "server":
		handleServer(os.Args[2:])
	case "session":
		handleSession(os.Args[2:])
	case "config":
		handleConfig(os.Args[2:])
	case "help", "--help", "-h":
		printHelp()
	default:
		fmt.Fprintln(os.Stderr, styleError.Render("unknown command: %s\n\n", os.Args[1]))
		printHelp()
		os.Exit(1)
	}
}
