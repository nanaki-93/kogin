// Command kasuri opens Kasuri on the project in the current folder (or
// the folder given as its argument): it starts the daemon beside it when none
// is running, makes that folder the daemon's project, and opens the Web UI in
// the browser. A daemon it started runs until Ctrl+C; one that was already
// running is only switched to the folder and keeps running.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/nanaki-93/mini-orca/v2/internal/brand"
	"github.com/nanaki-93/mini-orca/v2/internal/launcher"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintf(os.Stderr, "kasuri: %v\n", err)
		os.Exit(1)
	}
}

type options struct {
	url     string
	project string
	open    bool
}

func parseOptions(args []string, out io.Writer) (options, error) {
	flags := flag.NewFlagSet("kasuri", flag.ContinueOnError)
	flags.SetOutput(out)
	flags.Usage = func() {
		fmt.Fprintln(out, "Usage: kasuri [flags] [project folder]")
		fmt.Fprintln(out, "Opens Kasuri on the project folder (default: the current folder).")
		flags.PrintDefaults()
	}
	defaultURL := brand.Getenv("KASURI_URL")
	if defaultURL == "" {
		defaultURL = launcher.DefaultURL
	}
	url := flags.String("url", defaultURL, "daemon address (KASURI_URL)")
	noOpen := flags.Bool("no-open", false, "do not open the Web UI in the browser")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if flags.NArg() > 1 {
		return options{}, fmt.Errorf("give at most one project folder")
	}
	project, err := projectFolder(flags.Arg(0))
	if err != nil {
		return options{}, err
	}
	return options{url: *url, project: project, open: !*noOpen}, nil
}

// projectFolder resolves the folder to open: the argument, or the current one.
func projectFolder(argument string) (string, error) {
	if argument != "" {
		return launcher.Folder(argument)
	}
	current, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return launcher.Folder(current)
}

func run(ctx context.Context, args []string, out io.Writer) error {
	opts, err := parseOptions(args, out)
	if err != nil {
		return err
	}
	session, err := launcher.Open(ctx, launcher.Options{URL: opts.url, Project: opts.project, Notes: out, Stdout: os.Stdout, Stderr: os.Stderr})
	if err != nil {
		return err
	}
	defer session.Stop()
	page := session.Page()
	if opts.open {
		if err := openBrowser(page); err != nil {
			fmt.Fprintf(out, "Could not open the browser (%v); open %s yourself.\n", err, page)
		}
	}
	if session.Daemon == nil {
		fmt.Fprintf(out, "Opened %s in the running daemon: %s\n", opts.project, page)
		fmt.Fprintln(out, session.Client.ConfigNote(ctx))
		return nil
	}
	fmt.Fprintf(out, "Kasuri is running for %s at %s — press Ctrl+C to stop it.\n", opts.project, page)
	select {
	case <-ctx.Done():
		fmt.Fprintln(out, "\nStopping the daemon…")
		return nil
	case <-session.Daemon.Done():
		return fmt.Errorf("the daemon stopped: %v", session.Daemon.Err())
	}
}
