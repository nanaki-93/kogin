// Command agent-eval defaults to local synthetic replay. Live provider work
// requires an explicit mode, destination configuration, confirmation and budget.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/nanaki-93/kogin/v2/internal/agenteval"
	"github.com/nanaki-93/kogin/v2/internal/config"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

type commandOptions struct {
	mode, configPath, caseID string
	suite, coders            string
	confirm                  bool
	requests                 int
	input, output            int64
}

func parseOptions(args []string, diagnostics io.Writer) (commandOptions, error) {
	var options commandOptions
	flags := flag.NewFlagSet("agent-eval", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	flags.StringVar(&options.mode, "mode", "replay", "replay or live; replay sends only to its owned loopback fixture")
	flags.StringVar(&options.suite, "suite", "agent", "agent (the investigator corpus) or coder (the tool-using and one-shot coders on small tasks in several languages; live only)")
	flags.StringVar(&options.coders, "coders", agenteval.CoderTools+","+agenteval.CoderOneShot, "coder suite: the coders to run and compare")
	flags.StringVar(&options.configPath, "config", "", "live model configuration; uses the function scope")
	flags.StringVar(&options.caseID, "case", "", "one fixed corpus case; empty selects all eligible cases")
	flags.BoolVar(&options.confirm, "confirm-live", false, "confirm sending only the embedded synthetic corpus to the configured provider")
	flags.IntVar(&options.requests, "requests", 0, "campaign model-call limit; required for live")
	flags.Int64Var(&options.input, "input-tokens", 0, "campaign input-token charge limit; required for live")
	flags.Int64Var(&options.output, "output-tokens", 0, "campaign output-token charge limit; required for live")
	if err := flags.Parse(args); err != nil {
		return options, err
	}
	if flags.NArg() != 0 {
		return options, fmt.Errorf("unexpected positional arguments")
	}
	if options.mode != "live" && options.mode != "replay" {
		return options, fmt.Errorf("mode must be replay or live")
	}
	if options.suite != "agent" && options.suite != "coder" {
		return options, fmt.Errorf("suite must be agent or coder")
	}
	if options.suite == "coder" && options.mode != "live" {
		return options, fmt.Errorf("the coder suite runs live models: add --mode live")
	}
	if options.mode == "replay" && (options.configPath != "" || options.confirm) {
		return options, fmt.Errorf("replay does not accept live configuration or confirmation")
	}
	if options.requests < 0 || options.input < 0 || options.output < 0 {
		return options, fmt.Errorf("budgets must be positive")
	}
	return options, nil
}

func run(ctx context.Context, args []string, output, diagnostics io.Writer) int {
	options, err := parseOptions(args, diagnostics)
	if err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		fmt.Fprintln(diagnostics, err)
		return 2
	}
	if options.suite == "coder" {
		return runCoderSuite(ctx, options, output, diagnostics)
	}
	input, err := evaluationOptions(options, diagnostics)
	if err != nil {
		fmt.Fprintln(diagnostics, err)
		return 2
	}
	receipt, err := agenteval.Run(ctx, input)
	if err != nil {
		fmt.Fprintln(diagnostics, err)
		return 2
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(receipt); err != nil {
		fmt.Fprintln(diagnostics, "write evaluation receipt failed")
		return 2
	}
	if receipt.State != "passed" {
		return 1
	}
	return 0
}

func evaluationOptions(options commandOptions, diagnostics io.Writer) (agenteval.Options, error) {
	result := agenteval.Options{Mode: options.mode, CaseID: options.caseID, Limits: agenteval.DefaultLimits()}
	if options.requests > 0 {
		result.Limits.ModelCalls = options.requests
	}
	if options.input > 0 {
		result.Limits.InputTokens = options.input
	}
	if options.output > 0 {
		result.Limits.OutputTokens = options.output
	}
	version, digest := agenteval.CorpusIdentity()
	if options.mode == "replay" {
		fmt.Fprintf(diagnostics, "Replay: embedded synthetic corpus %s (%s); owned loopback fixture only. Usage is simulated; timing measures the local harness.\n", version, digest)
		return result, nil
	}
	if options.configPath == "" || options.requests < 1 || options.input < 1 || options.output < 1 {
		return result, fmt.Errorf("live requires --config, --requests, --input-tokens and --output-tokens")
	}
	cfg, err := config.LoadFromYAML(options.configPath)
	if err != nil {
		return result, fmt.Errorf("load evaluation configuration failed")
	}
	profiles, err := config.ResolveModelProfiles(cfg)
	if err != nil {
		return result, fmt.Errorf("resolve evaluation model failed")
	}
	result.Profile = profiles.Code
	fmt.Fprintf(diagnostics, "Live source: embedded synthetic corpus %s (%s), no workspace files. Destination: %q; model: %q. Campaign limits: %d requests, %d input-token charges, %d output-token charges.\n", version, digest, result.Profile.APIBaseURL, result.Profile.Model, result.Limits.ModelCalls, result.Limits.InputTokens, result.Limits.OutputTokens)
	if !options.confirm {
		return result, fmt.Errorf("inspect this disclosure and add --confirm-live to dispatch")
	}
	result.Confirmed = true
	return result, nil
}

// runCoderSuite compares the coders on the embedded coder corpus. Each task
// runs in a scratch Git repository; the coders' code and the hidden tests
// run on this machine, so the disclosure precedes any model request.
func runCoderSuite(ctx context.Context, options commandOptions, output, diagnostics io.Writer) int {
	if options.configPath == "" {
		fmt.Fprintln(diagnostics, "the coder suite requires --config")
		return 2
	}
	cfg, err := config.LoadFromYAML(options.configPath)
	if err != nil {
		fmt.Fprintln(diagnostics, "load evaluation configuration failed")
		return 2
	}
	version, digest := agenteval.CoderCorpusIdentity()
	coders := strings.Split(options.coders, ",")
	fmt.Fprintf(diagnostics, "Coder suite: embedded corpus %s (%s). Coders: %s, on the bug profile's model %q (%s). Each task runs in a scratch Git repository; the coders' changes and the tasks' hidden tests run on this machine (go, python3 and node as the tasks need).\n",
		version, digest, strings.Join(coders, ", "), cfg.ModelScopes.Bug.Model, cfg.ModelScopes.Bug.Provider)
	if !options.confirm {
		fmt.Fprintln(diagnostics, "inspect this disclosure and add --confirm-live to dispatch")
		return 2
	}
	trust, err := os.MkdirTemp("", "kogin-coder-eval-trust-")
	if err != nil {
		fmt.Fprintln(diagnostics, err)
		return 2
	}
	defer os.RemoveAll(trust)
	// The scratch projects' execution trust never reaches the user's own store.
	if err := os.Setenv("KOGIN_TRUST_DIR", trust); err != nil {
		fmt.Fprintln(diagnostics, err)
		return 2
	}
	receipt, err := agenteval.RunCoders(ctx, agenteval.CoderOptions{Config: cfg, CaseID: options.caseID, Coders: coders, Confirmed: true})
	if err != nil {
		fmt.Fprintln(diagnostics, err)
		return 2
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(receipt); err != nil {
		fmt.Fprintln(diagnostics, "write evaluation receipt failed")
		return 2
	}
	if receipt.ToolsAtLeastAsGood != nil && !*receipt.ToolsAtLeastAsGood {
		return 1
	}
	return 0
}
