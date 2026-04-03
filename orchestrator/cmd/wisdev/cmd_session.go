package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/config"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/storage"
)

func handleSession(args []string) {
	if len(args) == 0 {
		fmt.Println(styleError.Render("error: session subcommand required"))
		fmt.Println("Usage: wisdev session <list|view|delete> [flags]")
		os.Exit(1)
	}

	switch args[0] {
	case "list":
		handleSessionList(args[1:])
	case "view":
		handleSessionView(args[1:])
	case "delete":
		handleSessionDelete(args[1:])
	default:
		fmt.Fprintln(os.Stderr, styleError.Render("unknown session command: %s\n"), args[0])
		os.Exit(1)
	}
}

func handleSessionList(args []string) {
	listCmd := flag.NewFlagSet("list", flag.ExitOnError)
	listConfig := listCmd.String("config", "wisdev.yaml", "path to config file")
	listUser := listCmd.String("user", "", "filter by user ID")

	if err := listCmd.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	cfg, err := loadConfig(*listConfig)
	if err != nil {
		cfg = &config.Config{}
		cfg.SetDefaults()
	}

	store, err := storage.NewProvider(cfg.Storage.Type, cfg.Storage.DSN)
	if err != nil {
		fmt.Fprintln(os.Stderr, styleError.Render(fmt.Sprintf("storage error: %v\n", err)))
		os.Exit(1)
	}
	defer store.Close()

	sessions, err := store.ListSessions(nil, *listUser)
	if err != nil {
		fmt.Fprintln(os.Stderr, styleError.Render(fmt.Sprintf("list error: %v\n", err)))
		os.Exit(1)
	}

	fmt.Println(styleTitle.Render("Research Sessions"))
	fmt.Println()
	printSessions(sessions)
	fmt.Println()
	fmt.Println(styleDim.Render(fmt.Sprintf("%d session(s) found", len(sessions))))
}

func handleSessionView(args []string) {
	viewCmd := flag.NewFlagSet("view", flag.ExitOnError)
	viewConfig := viewCmd.String("config", "wisdev.yaml", "path to config file")

	if err := viewCmd.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if viewCmd.NArg() == 0 {
		fmt.Fprintln(os.Stderr, styleError.Render("error: session ID required\n"))
		fmt.Println("Usage: wisdev session view <session-id>")
		os.Exit(1)
	}

	sessionID := viewCmd.Arg(0)

	cfg, err := loadConfig(*viewConfig)
	if err != nil {
		cfg = &config.Config{}
		cfg.SetDefaults()
	}

	store, err := storage.NewProvider(cfg.Storage.Type, cfg.Storage.DSN)
	if err != nil {
		fmt.Fprintln(os.Stderr, styleError.Render(fmt.Sprintf("storage error: %v\n", err)))
		os.Exit(1)
	}
	defer store.Close()

	session, err := store.GetSession(nil, sessionID)
	if err != nil {
		fmt.Fprintln(os.Stderr, styleError.Render("not found: %s\n", sessionID))
		os.Exit(1)
	}

	fmt.Println(styleTitle.Render("Session Details"))
	fmt.Println()
	box := styleBox.Render(
		styleLabel.Render("Session ID: ") + styleValue.Render(session.SessionID) + "\n" +
			styleLabel.Render("User ID: ") + styleValue.Render(session.UserID) + "\n" +
			styleLabel.Render("Status: ") + styleValue.Render(session.Status) + "\n" +
			styleLabel.Render("Created: ") + styleValue.Render(session.CreatedAt.Format("2006-01-02 15:04:05")) + "\n" +
			styleLabel.Render("Updated: ") + styleValue.Render(session.UpdatedAt.Format("2006-01-02 15:04:05")),
	)
	fmt.Println(box)
}

func handleSessionDelete(args []string) {
	delCmd := flag.NewFlagSet("delete", flag.ExitOnError)
	delConfig := delCmd.String("config", "wisdev.yaml", "path to config file")

	if err := delCmd.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if delCmd.NArg() == 0 {
		fmt.Fprintln(os.Stderr, styleError.Render("error: session ID required\n"))
		fmt.Println("Usage: wisdev session delete <session-id>")
		os.Exit(1)
	}

	sessionID := delCmd.Arg(0)

	cfg, err := loadConfig(*delConfig)
	if err != nil {
		cfg = &config.Config{}
		cfg.SetDefaults()
	}

	store, err := storage.NewProvider(cfg.Storage.Type, cfg.Storage.DSN)
	if err != nil {
		fmt.Fprintln(os.Stderr, styleError.Render(fmt.Sprintf("storage error: %v\n", err)))
		os.Exit(1)
	}
	defer store.Close()

	if err := store.DeleteSession(nil, sessionID); err != nil {
		fmt.Fprintln(os.Stderr, styleError.Render(fmt.Sprintf("delete error: %v\n", err)))
		os.Exit(1)
	}

	fmt.Println(styleSuccess.Render(fmt.Sprintf("Session %s deleted", sessionID)))
}
