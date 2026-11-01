// Command agent-inspect reads local journal records without dispatching work.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/nanaki-93/kasuri/v2/internal/app"
)

func main() { os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)) }

func run(ctx context.Context, args []string, output, diagnostics io.Writer) int {
	var options app.AgentInspectionOptions
	flags := flag.NewFlagSet("agent-inspect", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	flags.StringVar(&options.Root, "root", "", "absolute checkout root containing the local agent journal")
	flags.StringVar(&options.RunID, "run", "", "one recorded invocation ID")
	flags.StringVar(&options.ArtifactID, "artifact", "", "one artifact ID from metadata inspection")
	flags.BoolVar(&options.Private, "private", false, "include private content for the exact selected run or artifact")
	flags.IntVar(&options.Limit, "limit", 100, "per-section record limit, 1–500; output reports truncation")
	err := flags.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil || flags.NArg() != 0 || options.Root == "" {
		fmt.Fprintln(diagnostics, "require --root and supported flags, without positional arguments")
		return 2
	}
	result, err := app.InspectAgentState(ctx, options)
	if err != nil {
		fmt.Fprintf(diagnostics, "inspection failed: %v\n", err)
		return 1
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil || len(data) > 8<<20 {
		fmt.Fprintln(diagnostics, "inspection exceeds the 8 MiB output limit; narrow the selection")
		return 1
	}
	n, err := output.Write(append(data, '\n'))
	if err != nil || n != len(data)+1 {
		fmt.Fprintln(diagnostics, "cannot write inspection output")
		return 1
	}
	return 0
}
