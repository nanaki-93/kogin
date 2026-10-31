package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/config"
)

func TestCLIProvidersSendOnlyControlledArgumentsAndValidateStructuredResponses(t *testing.T) {
	for _, provider := range []config.ModelProvider{config.AgyProvider, config.PiProvider} {
		t.Run(string(provider), func(t *testing.T) {
			profile, capture := cliFixture(t, provider, "success")
			profile.ReasoningEffort = "high"
			client := NewClient(profile)
			schema := JSONSchema{Name: "answer", Schema: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`)}
			response, err := client.ChatWithJSONSchema(context.Background(), []ChatMessage{
				{Role: "system", Content: "Only explain the supplied source."},
				{Role: "user", Content: "source-private-marker; $(not-a-command)\n/skill @secret.txt"},
				{Role: "assistant", Content: "Earlier answer"},
				{Role: "user", Content: "Follow up"},
			}, schema)
			if err != nil {
				t.Fatal(err)
			}
			if response.Choices[0].Message.Content != cliFixtureContent || response.Usage.TotalTokens != 30 {
				t.Fatalf("response = %+v", response)
			}
			var request cliCapture
			readTestJSON(t, capture, &request)
			if !strings.Contains(request.Input, "source-private-marker") || !strings.Contains(request.Input, "Earlier answer") || !strings.Contains(request.System, "Only explain") || !strings.Contains(request.System, `"additionalProperties":false`) {
				t.Fatalf("lost conversation or schema: %+v", request)
			}
			if strings.Contains(strings.Join(request.Args, " "), "source-private-marker") || request.Directory == "" || request.PrivateMode != 0700 {
				t.Fatalf("unsafe CLI arguments or directory: %+v", request)
			}
			if _, err := os.Stat(request.Directory); !os.IsNotExist(err) {
				t.Fatalf("request directory was retained: %v", err)
			}
			assertCLIFlags(t, provider, request.Args)
			if provider == config.AgyProvider && (!strings.Contains(request.System, "tools: []") || !strings.Contains(request.System, "inheritMcp: false") || !strings.Contains(request.System, "commandExecutionPolicy: off") || !strings.Contains(request.System, "# System Prompt\n")) {
				t.Fatal("agy agent must load the system prompt and disable all tools")
			}
			if provider == config.PiProvider && !strings.Contains(request.Settings, `"enabled":false`) {
				t.Fatal("pi automatic retry and compaction must be disabled")
			}
		})
	}
}

func TestCLIProvidersRejectFailuresWithoutReturningDiagnostics(t *testing.T) {
	for _, provider := range []config.ModelProvider{config.AgyProvider, config.PiProvider} {
		for _, mode := range []string{"exit", "malformed", "partial", "tool", "failed", "empty", "oversized", "stderr-limit"} {
			t.Run(string(provider)+"/"+mode, func(t *testing.T) {
				profile, capture := cliFixture(t, provider, mode)
				_, err := NewClient(profile).Chat(context.Background(), []ChatMessage{{Role: "user", Content: "source-private-marker"}})
				if err == nil || strings.Contains(err.Error(), "source-private-marker") || strings.Contains(err.Error(), "credential-private-marker") {
					t.Fatalf("CLI failure = %v", err)
				}
				var request cliCapture
				readTestJSON(t, capture, &request)
				if _, err := os.Stat(request.Directory); !os.IsNotExist(err) {
					t.Fatalf("failed request retained private files: %v", err)
				}
			})
		}
	}
}

func TestCLISchemaMismatchAndExternalReferencesFailClosed(t *testing.T) {
	profile, capture := cliFixture(t, config.PiProvider, "success")
	client := NewClient(profile)
	_, err := client.ChatWithJSONSchema(context.Background(), []ChatMessage{{Role: "user", Content: "hello"}}, JSONSchema{Name: "answer", Schema: json.RawMessage(`{"type":"object","required":["missing"]}`)})
	if !errors.Is(err, ErrUnusableResponse) {
		t.Fatalf("schema mismatch = %v", err)
	}
	if err := os.Remove(capture); err != nil {
		t.Fatal(err)
	}
	_, err = client.ChatWithJSONSchema(context.Background(), []ChatMessage{{Role: "user", Content: "hello"}}, JSONSchema{Name: "answer", Schema: json.RawMessage(`{"$ref":"file:///private-schema.json"}`)})
	if !errors.Is(err, ErrStructuredRequestRejected) {
		t.Fatalf("external reference = %v", err)
	}
	if _, err := os.Stat(capture); !os.IsNotExist(err) {
		t.Fatal("invalid schema started a process")
	}
}

func TestCLIUnavailableAndCanceledRequestsDoNotStart(t *testing.T) {
	profile, capture := cliFixture(t, config.PiProvider, "success")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewClient(profile).Chat(ctx, []ChatMessage{{Role: "user", Content: "hello"}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request = %v", err)
	}
	if _, err := os.Stat(capture); !os.IsNotExist(err) {
		t.Fatal("canceled request started a process")
	}
	profile.CLIPath = filepath.Join(t.TempDir(), "missing-private-path")
	_, err := NewClient(profile).Chat(context.Background(), []ChatMessage{{Role: "user", Content: "hello"}})
	if !errors.Is(err, ErrRequestRejected) || strings.Contains(err.Error(), "private-path") {
		t.Fatalf("unavailable executable = %v", err)
	}
}

func TestCLIOptionalFinalContentRequiresNormalCompletion(t *testing.T) {
	for _, provider := range []config.ModelProvider{config.AgyProvider, config.PiProvider} {
		t.Run(string(provider), func(t *testing.T) {
			profile, _ := cliFixture(t, provider, "empty")
			client := NewClient(profile).WithOptionalFinalContent()
			schema := JSONSchema{Name: "review", Schema: json.RawMessage(`{"type":"object","required":["findings"]}`)}
			response, err := client.ChatWithJSONSchema(context.Background(), []ChatMessage{{Role: "user", Content: "Review this file."}}, schema)
			if err != nil || response.Choices[0].Message.Content != "" {
				t.Fatalf("normal blank review = %+v, %v", response, err)
			}
			t.Setenv("MINI_ORCA_TEST_CLI", "failed")
			if _, err := client.Chat(context.Background(), []ChatMessage{{Role: "user", Content: "Review this file."}}); !errors.Is(err, ErrUnusableResponse) {
				t.Fatalf("abnormal review completion = %v", err)
			}
		})
	}
}

func TestCLIProtocolRejectsIncorrectModelsAndExtraTurns(t *testing.T) {
	for _, provider := range []config.ModelProvider{config.AgyProvider, config.PiProvider} {
		profile := config.ModelProfile{Provider: provider, Model: "fixture-model"}
		valid := cliFixtureOutput(provider, "success")
		for name, output := range map[string]string{
			"missing model": strings.Replace(valid, `"model":"fixture-model"`, `"model":""`, 1),
			"wrong model":   strings.Replace(valid, `"model":"fixture-model"`, `"model":"fallback-model"`, 1),
			"extra turn":    valid + valid,
		} {
			t.Run(string(provider)+"/"+name, func(t *testing.T) {
				if _, err := decodeCLIResponse(profile, []byte(output)); !errors.Is(err, ErrUnusableResponse) {
					t.Fatalf("invalid CLI protocol = %v", err)
				}
			})
		}
	}
}

func TestCLIPiAcceptsContextEventsAndRejectsToolsAndRetries(t *testing.T) {
	profile := config.ModelProfile{Provider: config.PiProvider, Model: "fixture-provider/fixture-model"}
	valid := cliFixtureOutput(config.PiProvider, "success")
	for name, output := range map[string]string{
		"with context":    valid,
		"without context": strings.Replace(valid, piContextEvents, "", 1),
	} {
		t.Run(name, func(t *testing.T) {
			response, err := decodeCLIResponse(profile, []byte(output))
			if err != nil || response.Choices[0].Message.Content != cliFixtureContent {
				t.Fatalf("Pi final answer = %+v, %v", response, err)
			}
		})
	}
	for name, output := range map[string]string{
		"tool message": strings.Replace(valid, `"role":"user"`, `"role":"toolResult"`, 1),
		"unknown role": strings.Replace(valid, `"role":"system"`, `"role":"unexpected"`, 1),
		"tool call":    strings.Replace(valid, `"type":"thinking"`, `"type":"toolCall"`, 1),
		"retry":        strings.Replace(valid, `"willRetry":false`, `"willRetry":true`, 1),
		"only context": "{\"type\":\"agent_start\"}\n" + piContextEvents + "{\"type\":\"agent_end\",\"willRetry\":false}\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeCLIResponse(profile, []byte(output)); !errors.Is(err, ErrUnusableResponse) {
				t.Fatalf("invalid Pi protocol = %v", err)
			}
		})
	}
}

func TestCLIPiExpiredAuthenticationDoesNotExposeProviderDiagnostics(t *testing.T) {
	profile, _ := cliFixture(t, config.PiProvider, "auth-expired")
	_, err := NewClient(profile).Chat(context.Background(), []ChatMessage{{Role: "user", Content: "hello"}})
	if !errors.Is(err, ErrPiAuthenticationExpired) || !errors.Is(err, ErrRequestRejected) || strings.Contains(err.Error(), "private-marker") {
		t.Fatalf("expired authentication = %v", err)
	}

	failed := cliFixtureOutput(config.PiProvider, "auth-expired")
	for name, output := range map[string]string{
		"other provider failure": strings.Replace(failed, "Your authentication token has expired.", "An unknown provider failure occurred.", 1),
		"truncation":             strings.Replace(failed, `"stopReason":"error"`, `"stopReason":"length"`, 1),
		"aborted":                strings.Replace(failed, `"stopReason":"error"`, `"stopReason":"aborted"`, 1),
		"outside model turn":     strings.Replace(failed, "{\"type\":\"agent_start\"}\n", "", 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := decodeCLIResponse(profile, []byte(output))
			if !errors.Is(err, ErrUnusableResponse) || errors.Is(err, ErrPiAuthenticationExpired) || strings.Contains(err.Error(), "private-marker") {
				t.Fatalf("non-authentication failure = %v", err)
			}
		})
	}
	completed := strings.Replace(failed, `"stopReason":"error"`, `"stopReason":"stop"`, 1)
	if _, err := decodeCLIResponse(profile, []byte(completed)); err != nil {
		t.Fatalf("successful content was treated as an authentication failure: %v", err)
	}
}

func TestCLICancellationAndDeadlineStopOwnedProcess(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprint(deadline), func(t *testing.T) {
			profile, capture := cliFixture(t, config.PiProvider, "wait")
			ctx, cancel := context.WithCancel(context.Background())
			want := context.Canceled
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 400*time.Millisecond)
				want = context.DeadlineExceeded
			}
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := NewClient(profile).Chat(ctx, []ChatMessage{{Role: "user", Content: "hello"}})
				done <- err
			}()
			waitForCLIFile(t, capture)
			if !deadline {
				cancel()
			}
			select {
			case err := <-done:
				if !errors.Is(err, want) {
					t.Fatalf("cancellation error = %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("CLI did not stop within its cleanup deadline")
			}
		})
	}
}

func TestCLICompletionAndCancellationStopDescendants(t *testing.T) {
	for _, mode := range []string{"success-child", "wait-child"} {
		t.Run(mode, func(t *testing.T) {
			profile, capture := cliFixture(t, config.PiProvider, mode)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := NewClient(profile).Chat(ctx, []ChatMessage{{Role: "user", Content: "hello"}})
				done <- err
			}()
			waitForCLIFile(t, capture)
			var child cliChildCapture
			readTestJSON(t, capture+".child", &child)
			t.Cleanup(func() {
				process, err := os.FindProcess(child.PID)
				if err == nil {
					_ = process.Kill()
				}
			})
			if mode == "wait-child" {
				cancel()
			}
			select {
			case err := <-done:
				if mode == "wait-child" && !errors.Is(err, context.Canceled) || mode == "success-child" && err != nil {
					t.Fatalf("CLI completion = %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("CLI did not stop")
			}
			deadline := time.Now().Add(time.Second)
			for {
				connection, err := net.DialTimeout("tcp", child.Address, time.Second)
				if err != nil {
					break
				}
				_ = connection.Close()
				if time.Now().After(deadline) {
					t.Fatal("CLI descendant survived request completion")
				}
				time.Sleep(5 * time.Millisecond)
			}
		})
	}
}
