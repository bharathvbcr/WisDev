package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

func handleRun(args []string) {
	runCmd := flag.NewFlagSet("run", flag.ExitOnError)
	runConfig := runCmd.String("config", "wisdev.yaml", "path to wisdev.yaml config file")
	runQuery := runCmd.String("q", "", "research query")
	runMode := runCmd.String("mode", "", "agent mode: autonomous or guided")
	runPort := runCmd.Int("port", 8081, "HTTP listen port")
	runGRPCPort := runCmd.Int("grpc-port", 50052, "gRPC listen port")
	runJSON := runCmd.Bool("json", false, "output results as JSON")
	runNoTUI := runCmd.Bool("no-tui", false, "disable TUI, use plain text output")

	if err := runCmd.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "error parsing flags: %v\n", err)
		os.Exit(1)
	}

	if *runQuery == "" {
		fmt.Fprintln(os.Stderr, styleError.Render("error: -q flag is required"))
		fmt.Fprintf(os.Stderr, "\nUsage: wisdev run -q \"your research query\"\n")
		os.Exit(1)
	}

	cfg, err := loadConfig(*runConfig)
	if err != nil {
		fmt.Fprintln(os.Stderr, styleError.Render(fmt.Sprintf("config error: %v\n", err)))
		os.Exit(1)
	}

	if *runMode != "" {
		cfg.Agent.Mode = *runMode
	}
	if *runPort != 8081 {
		cfg.Server.HTTPPort = *runPort
	}
	if *runGRPCPort != 50052 {
		cfg.Server.GRPCPort = *runGRPCPort
	}

	if *runNoTUI {
		if !*runJSON {
			printBanner()
			printConfig(cfg)
			fmt.Println()
		}
		if err := runResearchNoTUI(*runQuery, cfg); err != nil {
			fmt.Fprintln(os.Stderr, styleError.Render(fmt.Sprintf("research failed: %v\n", err)))
			os.Exit(1)
		}
		return
	}

	if !*runJSON {
		printBanner()
		printConfig(cfg)
		fmt.Println()
	}

	if err := runResearch(*runQuery, cfg); err != nil {
		fmt.Fprintln(os.Stderr, styleError.Render(fmt.Sprintf("research failed: %v\n", err)))
		os.Exit(1)
	}
}

func printJSONResult(result interface{}) {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, styleError.Render(fmt.Sprintf("failed to marshal JSON: %v\n", err)))
		os.Exit(1)
	}
	fmt.Println(string(data))
}
