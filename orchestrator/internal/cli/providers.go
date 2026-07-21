package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
)

func runProviders(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("providers", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	jsonOut := fs.Bool("json", false, "emit JSON array")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}

	registry := search.BuildRegistry()
	names := make([]string, 0, len(registry.All()))
	for _, provider := range registry.All() {
		names = append(names, provider.Name())
	}
	sort.Strings(names)

	if *jsonOut {
		encoded, err := json.Marshal(names)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, string(encoded))
		return nil
	}

	printSection(stdout, fmt.Sprintf("Built-in search providers (%d)", len(names)))
	columns := 3
	for i, name := range names {
		if i%columns == 0 {
			fmt.Fprint(stdout, "  ")
		}
		fmt.Fprintf(stdout, "%-22s", name)
		if (i+1)%columns == 0 || i == len(names)-1 {
			fmt.Fprintln(stdout)
		}
	}
	return nil
}
