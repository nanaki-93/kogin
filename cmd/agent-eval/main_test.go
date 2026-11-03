package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/agenteval"
)

func TestAgentEvaluationCommandDefaultsToSourceFreeReplay(t *testing.T) {
	var output, diagnostics bytes.Buffer
	status := run(t.Context(), []string{"--case", "addition"}, &output, &diagnostics)
	var receipt agenteval.Receipt
	if err := json.Unmarshal(output.Bytes(), &receipt); err != nil {
		t.Fatal(err, diagnostics.String())
	}
	if status != 0 || receipt.Mode != "replay" || receipt.State != "passed" || !strings.Contains(diagnostics.String(), "Usage is simulated") {
		t.Fatal(status, receipt, diagnostics.String())
	}
}

func TestAgentEvaluationCommandRejectsImplicitLiveAndReportsIncomplete(t *testing.T) {
	for _, args := range [][]string{{"--mode", "live"}, {"--confirm-live"}, {"--config", "config.yaml"}, {"--mode", "unknown"}, {"--requests", "-1"}, {"extra"}} {
		var output, diagnostics bytes.Buffer
		if status := run(t.Context(), args, &output, &diagnostics); status != 2 || output.Len() != 0 {
			t.Fatal(args, status, output.String())
		}
	}
	var output, diagnostics bytes.Buffer
	status := run(t.Context(), []string{"--requests", "1"}, &output, &diagnostics)
	if status != 1 || !strings.Contains(output.String(), `"state": "incomplete"`) || !strings.Contains(output.String(), `"state": "not_run"`) {
		t.Fatal(status, output.String(), diagnostics.String())
	}
}

func TestAgentEvaluationLiveDisclosurePrecedesAnyRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "config.yaml")
	text := "model_scopes:\n"
	for _, scope := range []string{"analyze", "bug", "code"} {
		text += fmt.Sprintf("  %s: {provider: openai, api_base_url: '%s', model: fixture, api_key: PRIVATE_KEY, max_tokens: 512}\n", scope, server.URL)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	var output, diagnostics bytes.Buffer
	status := run(t.Context(), []string{"--mode", "live", "--config", path, "--requests", "8", "--input-tokens", "65536", "--output-tokens", "8192"}, &output, &diagnostics)
	if status != 2 || calls.Load() != 0 || output.Len() != 0 || !strings.Contains(diagnostics.String(), server.URL) || !strings.Contains(diagnostics.String(), "no workspace files") || !strings.Contains(diagnostics.String(), "--confirm-live") || strings.Contains(diagnostics.String(), "PRIVATE_KEY") {
		t.Fatal(status, diagnostics.String())
	}
}

func TestTheCoderSuiteIsLiveOnlyAndDisclosesBeforeDispatch(t *testing.T) {
	for _, args := range [][]string{{"--suite", "coder"}, {"--suite", "coders", "--mode", "live"}, {"--suite", "coder", "--mode", "live"}} {
		var output, diagnostics bytes.Buffer
		if status := run(t.Context(), args, &output, &diagnostics); status != 2 || output.Len() != 0 {
			t.Fatal(args, status, output.String(), diagnostics.String())
		}
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("model_scopes:\n  analyze: {provider: pi, model: openai-codex/gpt-5.5}\n  bug: {provider: pi, model: openai-codex/gpt-5.5}\n  code: {provider: pi, model: openai-codex/gpt-5.5}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var output, diagnostics bytes.Buffer
	status := run(t.Context(), []string{"--suite", "coder", "--mode", "live", "--config", path}, &output, &diagnostics)
	if status != 2 || output.Len() != 0 || !strings.Contains(diagnostics.String(), `"openai-codex/gpt-5.5" (pi)`) || !strings.Contains(diagnostics.String(), "hidden tests run on this machine") || !strings.Contains(diagnostics.String(), "--confirm-live") {
		t.Fatal(status, diagnostics.String())
	}
}
