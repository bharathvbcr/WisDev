package cli

import (
	"fmt"
	"io"
	"os"
)

// Version is the WisDev CLI release label. Override from cmd/wisdev via -ldflags.
var Version = "dev"

func runVersion(stdout io.Writer) error {
	fmt.Fprintf(stdout, "%s %s\n",
		paint(stdout, ansiBold+scholarlmRedBold, "wisdev"),
		paint(stdout, ansiDim, Version),
	)
	if exe, err := os.Executable(); err == nil && exe != "" {
		fmt.Fprintf(stdout, "%s %s\n",
			paint(stdout, ansiDim, "binary:"),
			exe,
		)
	}
	printScholarLMBrandingProminent(stdout)
	return nil
}
