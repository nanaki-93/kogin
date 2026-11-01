package llm

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nanaki-93/mini-orca/v2/internal/config"
)

func TestCLIAgyStructuredRequestDoesNotDependOnFinishTool(t *testing.T) {
	for _, mode := range []string{"success", "fenced-json"} {
		t.Run(mode, func(t *testing.T) {
			profile, capture := cliFixture(t, config.AgyProvider, mode)
			schema := JSONSchema{Name: "answer", Schema: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`)}
			response, err := NewClient(profile).ChatWithJSONSchema(context.Background(), []ChatMessage{{Role: "user", Content: "Explain the supplied source."}}, schema)
			if err != nil || response.Choices[0].Message.Content != cliFixtureContent {
				t.Fatalf("schema-constrained final response = %+v, %v", response, err)
			}
			var request cliCapture
			readTestJSON(t, capture, &request)
			if strings.Contains(strings.Join(request.Args, " "), "--json-schema") || !strings.Contains(request.System, "tools: []") || !strings.Contains(request.System, string(schema.Schema)) {
				t.Fatal("schema must be supplied as context and validated locally with all tools disabled")
			}
		})
	}
}

func TestCLIAgyRequiresValidFinalJSON(t *testing.T) {
	for _, mode := range []string{"invalid-json", "invalid-schema", "null-json", "extra-property", "native-only"} {
		t.Run(mode, func(t *testing.T) {
			profile, _ := cliFixture(t, config.AgyProvider, mode)
			schema := JSONSchema{Name: "answer", Schema: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`)}
			_, err := NewClient(profile).ChatWithJSONSchema(context.Background(), []ChatMessage{{Role: "user", Content: "Explain the supplied source."}}, schema)
			if !errors.Is(err, ErrUnusableResponse) {
				t.Fatalf("invalid final JSON was accepted: %v", err)
			}
		})
	}
}

func TestCLIAgyRejectsAllToolEventsIncludingFinish(t *testing.T) {
	profile := config.ModelProfile{Provider: config.AgyProvider, Model: "fixture-model"}
	valid := cliFixtureOutput(config.AgyProvider, "finish")
	completion := cliFixtureOutput(config.AgyProvider, "success")
	for name, output := range map[string]string{
		"finish":           valid,
		"command":          strings.ReplaceAll(valid, `"tool_name":"finish"`, `"tool_name":"run_command"`),
		"missing name":     strings.ReplaceAll(valid, `"tool_name":"finish"`, `"tool_name":""`),
		"conflicting name": strings.ReplaceAll(valid, `"name":"finish"`, `"name":"view_file"`),
		"subagent":         strings.ReplaceAll(valid, `"step_type":"tool"`, `"step_type":"subagent"`),
		"before init":      agyFinishEvents + completion,
		"after result":     completion + agyFinishEvents,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeCLIResponse(profile, []byte(output)); !errors.Is(err, ErrUnusableResponse) {
				t.Fatalf("unauthorized finish = %v", err)
			}
		})
	}
}

func TestCLIAgyRejectsIncorrectAgentsAndToolUse(t *testing.T) {
	profile := config.ModelProfile{Provider: config.AgyProvider, Model: "fixture-model"}
	valid := cliFixtureOutput(config.AgyProvider, "success")
	for name, output := range map[string]string{
		"missing agent": strings.Replace(valid, `"agent":"kasuri"`, `"agent":""`, 1),
		"wrong agent":   strings.Replace(valid, `"agent":"kasuri"`, `"agent":"default"`, 1),
		"tool":          cliFixtureOutput(config.AgyProvider, "tool"),
		"subagent":      strings.Replace(cliFixtureOutput(config.AgyProvider, "tool"), `"step_type":"tool"`, `"step_type":"subagent"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeCLIResponse(profile, []byte(output)); !errors.Is(err, ErrUnusableResponse) {
				t.Fatalf("unexpected agent or tool use = %v", err)
			}
		})
	}
}

func TestCLIAgyPlainResponsesKeepToolsDisabled(t *testing.T) {
	profile, capture := cliFixture(t, config.AgyProvider, "success")
	response, err := NewClient(profile).Chat(context.Background(), []ChatMessage{{Role: "user", Content: "Explain the supplied source."}})
	if err != nil || response.Choices[0].Message.Content != cliFixtureContent {
		t.Fatalf("plain response = %+v, %v", response, err)
	}
	var request cliCapture
	readTestJSON(t, capture, &request)
	if !strings.Contains(request.System, "tools: []") || strings.Contains(strings.Join(request.Args, " "), "--json-schema") {
		t.Fatal("plain responses must not enable structured completion")
	}
}
