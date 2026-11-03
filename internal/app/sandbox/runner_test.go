package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestRunCheckCommandHonorsCancellation(t *testing.T) {
	t.Setenv("KOGIN_CHECK_HELPER", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := RunCommand(ctx, t.TempDir(), checkHelperCommand("cancel-with-output"))
	if err == nil || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("command error = %v, context = %v", err, ctx.Err())
	}
}

func TestRunCheckCommandBoundsOutputAndPreservesExitCode(t *testing.T) {
	t.Setenv("KOGIN_CHECK_HELPER", "1")
	result, err := RunCommand(context.Background(), t.TempDir(), checkHelperCommand("huge-output"))
	if err == nil || result.ExitCode != 17 {
		t.Fatalf("command result = %+v, err = %v", result, err)
	}
	if len(result.Output) > CaptureBytes || len(result.Output) <= checkLimits.maxOutputBytes {
		t.Fatalf("command output was not captured within its bound: %d bytes", len(result.Output))
	}
	output := SanitizeOutput(result.Output, result.Truncated, "/workspace", "/project")
	if !strings.Contains(output, "[output truncated]") {
		t.Fatalf("sanitized output = %q", output)
	}
}

func TestRunCheckCommandCancellationDoesNotWaitForInheritedOutputPipe(t *testing.T) {
	t.Setenv("KOGIN_CHECK_HELPER", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := RunCommand(ctx, t.TempDir(), checkHelperCommand("cancel-with-output"))
	if err == nil || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("command error = %v, context = %v", err, ctx.Err())
	}
	if elapsed := time.Since(started); elapsed > 2*checkLimits.pipeDrainWait {
		t.Fatalf("canceled command waited %s for inherited output pipe", elapsed)
	}
}

func TestRunCheckCommandUsesAllowlistedChildEnvironment(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "openai-must-not-reach-child")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-must-not-reach-child")
	t.Setenv("PATH", os.Getenv("PATH"))
	result, err := RunCommand(context.Background(), t.TempDir(), checkHelperCommand("environment"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Output, "must-not-reach-child") {
		t.Fatalf("child inherited sentinel: %q", result.Output)
	}
	if !strings.Contains(result.Output, "path-present") {
		t.Fatalf("child lost toolchain path: %q", result.Output)
	}
}

func TestSourceOnlyCheckCommandDoesNotRequireDescendantOwnership(t *testing.T) {
	result, err := RunCommand(context.Background(), t.TempDir(), checkHelperCommand("environment"))
	if err != nil || !strings.Contains(result.Output, "path-present") {
		t.Fatalf("source-only command = %+v, %v", result, err)
	}
}

func TestRunCheckCommandCancellationStopsDescendants(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "descendant-marker")
	directory := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := RunCommandWithStart(ctx, directory, checkHelperCommand("descendant-parent", marker), true, nil)
		done <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.ReadFile(marker); err == nil {
			break
		} else if time.Now().After(deadline) {
			cancel()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("descendant command did not stop after cancellation")
			}
			t.Fatalf("descendant did not start: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	var err error
	select {
	case err = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("descendant command did not stop after cancellation")
	}
	if err == nil || !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("command error = %v, context = %v", err, ctx.Err())
	}
	before, readErr := os.ReadFile(marker)
	if readErr != nil {
		t.Fatal(readErr)
	}
	time.Sleep(40 * time.Millisecond)
	after, readErr := os.ReadFile(marker)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != string(before) {
		t.Fatalf("descendant survived cancellation: before=%q after=%q", before, after)
	}
}

func TestRunCheckCommandNormalExitStopsDescendants(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "descendant-exit-marker")
	directory := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := RunCommandWithStart(ctx, directory, checkHelperCommand("descendant-parent-exit", marker), true, nil)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("command result = %+v, err = %v", result, err)
	}
	cancel()
	if err := os.Remove(marker); err != nil {
		t.Fatalf("expected descendant to have created marker before parent exit: %v", err)
	}
	time.Sleep(40 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("background descendant survived successful check completion and wrote marker: %v", err)
	}
}

func TestCheckCommandHelperProcess(t *testing.T) {
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || len(os.Args) <= separator+1 {
		return
	}
	mode := os.Args[separator+1]
	argument := ""
	if len(os.Args) > separator+2 {
		argument = os.Args[separator+2]
	}
	switch mode {
	case "wait-stdin":
		_, _ = io.Copy(io.Discard, os.Stdin)
	case "environment":
		if os.Getenv("PATH") == "" {
			fmt.Fprint(os.Stdout, "path-missing")
		} else {
			fmt.Fprint(os.Stdout, "path-present")
		}
		fmt.Fprint(os.Stdout, os.Getenv("OPENAI_API_KEY"))
		fmt.Fprint(os.Stdout, os.Getenv("ANTHROPIC_API_KEY"))
	case "huge-output":
		chunk := strings.Repeat("x", checkLimits.maxOutputBytes)
		_, _ = fmt.Fprint(os.Stdout, chunk)
		_, _ = fmt.Fprint(os.Stderr, chunk)
		os.Exit(17)
	case "cancel-with-output":
		child := exec.Command(os.Args[0], "-test.run=^TestCheckCommandHelperProcess$", "--", "keep-output")
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(18)
		}
		for {
			_, _ = fmt.Fprintln(os.Stdout, "parent output")
			time.Sleep(time.Millisecond)
		}
	case "descendant-parent":
		child := exec.Command(os.Args[0], "-test.run=^TestCheckCommandHelperProcess$", "--", "descendant-child", argument)
		if err := child.Start(); err != nil {
			os.Exit(18)
		}
		for {
			time.Sleep(time.Millisecond)
		}
	case "descendant-parent-exit":
		child := exec.Command(os.Args[0], "-test.run=^TestCheckCommandHelperProcess$", "--", "descendant-child", argument)
		if err := child.Start(); err != nil {
			os.Exit(18)
		}
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.ReadFile(argument); err == nil {
				os.Exit(0)
			}
			time.Sleep(time.Millisecond)
		}
		os.Exit(19)
	case "descendant-child":
		for counter := 0; ; counter++ {
			_ = os.WriteFile(argument, []byte(fmt.Sprintf("%d", counter)), 0600)
			time.Sleep(time.Millisecond)
		}
	case "keep-output":
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			_, _ = fmt.Fprintln(os.Stdout, "child output")
			time.Sleep(time.Millisecond)
		}
	}
	os.Exit(0)
}

func checkHelperCommand(mode string, arguments ...string) []string {
	return append([]string{os.Args[0], "-test.run=^TestCheckCommandHelperProcess$", "--", mode}, arguments...)
}

func TestSanitizeCheckOutputRedactsAuthorizationValues(t *testing.T) {
	for _, value := range []string{
		"Authorization: Bearer secret.token,with=delimiters",
		"authorization=Basic YWxpY2U6c2VjcmV0",
		"proxy-authorization: Bearer another secret",
	} {
		evidence := SanitizeOutput(value, false, "", "")
		if evidence != "[redacted]" {
			t.Fatalf("authorization evidence = %q", evidence)
		}
	}
}

func TestSanitizeOutputKeepsWholeCharactersWhenItTruncates(t *testing.T) {
	output := SanitizeOutput("x"+strings.Repeat("é", checkLimits.maxOutputBytes), false, "", "")
	if !utf8.ValidString(output) || !strings.Contains(output, "[output truncated]") {
		t.Fatalf("truncated output is not valid UTF-8: %q", output[len(output)-40:])
	}
}
