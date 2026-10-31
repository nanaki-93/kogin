package insighteval

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nanaki-93/mini-orca/v2/internal/app"
	"github.com/nanaki-93/mini-orca/v2/internal/config"
	"github.com/nanaki-93/mini-orca/v2/internal/llm"
)

type fakeEngineeringInsightClient struct {
	calls    atomic.Int32
	reply    string
	response *llm.ChatResponse
	err      error
	prompt   string
	schema   llm.JSONSchema
}

func (c *fakeEngineeringInsightClient) ChatWithJSONSchema(_ context.Context, messages []llm.ChatMessage, schema llm.JSONSchema) (*llm.ChatResponse, error) {
	c.calls.Add(1)
	if len(messages) != 1 || !strings.Contains(messages[0].Content, "TARGET_SOURCE") {
		return nil, errors.New("runner did not use the selected-file production prompt")
	}
	c.prompt = messages[0].Content
	c.schema = schema
	if c.err != nil {
		return nil, c.err
	}
	if c.response != nil {
		return c.response, nil
	}
	return &llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Role: "assistant", Content: c.reply}, FinishReason: "stop"}}, Usage: llm.ChatUsage{CompletionTokens: 7}}, nil
}

func runnerTestConfig(t *testing.T, runID, mode string, count int, client EngineeringInsightRunnerClient) EngineeringInsightRunnerConfig {
	t.Helper()
	cases := make([]EngineeringInsightRunnerCase, 0, count)
	for index := 0; index < count; index++ {
		caseIndex := index % 3
		intent := "substantive"
		if caseIndex == 2 {
			intent = "control"
		}
		cases = append(cases, EngineeringInsightRunnerCase{Expected: EngineeringInsightExpectedAttempt{CaseName: "case-" + string(rune('a'+caseIndex)), Partition: "development", Intent: intent, Repetition: index/3 + 1, Attempt: 1}, Source: "package fixture\n\n// fixture-secret\nfunc Run() {}\n"})
	}
	return EngineeringInsightRunnerConfig{Root: t.TempDir(), RunID: runID, Mode: mode, CandidateID: "candidate", Provider: "configured", Model: "model", PromptVersion: app.EngineeringInsightPromptVersion(), CorpusID: "corpus", CorpusDigest: strings.Repeat("a", 64), BaseRevision: "base", Profile: config.ModelProfile{Scope: config.BugModelScope, APIBaseURL: "http://127.0.0.1:9999", APIKey: "provider-secret", Model: "model", ContextMaxTokens: 32000}, Cases: cases, Client: client}
}

func qualificationRunnerTestConfig(t *testing.T, runID string, client EngineeringInsightRunnerClient) EngineeringInsightRunnerConfig {
	t.Helper()
	cases := make([]EngineeringInsightRunnerCase, 0, 24)
	for repetition := 1; repetition <= 2; repetition++ {
		for index := 0; index < 12; index++ {
			intent := "substantive"
			if index >= 8 {
				intent = "control"
			}
			cases = append(cases, EngineeringInsightRunnerCase{Expected: EngineeringInsightExpectedAttempt{CaseName: "qualification-" + string(rune('a'+index)), Partition: "qualification", Intent: intent, Repetition: repetition, Attempt: 1}, Source: "package fixture\nfunc Run() {}\n"})
		}
	}
	return EngineeringInsightRunnerConfig{Root: t.TempDir(), RunID: runID, Mode: EngineeringInsightQualificationRunMode, CandidateID: "candidate", Provider: "configured", Model: "model", PromptVersion: app.EngineeringInsightPromptVersion(), CorpusID: "corpus", CorpusDigest: strings.Repeat("a", 64), BaseRevision: "base", Profile: config.ModelProfile{Scope: config.BugModelScope, APIBaseURL: "http://127.0.0.1:9999", Model: "model", ContextMaxTokens: 32000}, Cases: cases, Client: client}
}

func runnerValidResponse() string {
	return `{"purpose":"model reply is private","responsibilities":[],"dependencies":[],"side_effects":[],"risks":[],"suggestions":[]}`
}
