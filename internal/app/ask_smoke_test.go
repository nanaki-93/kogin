package app

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/nanaki-93/mini-orca/v2/internal/config"
)

// TestRealAskAnswersWithCheckedCitations asks a CLI installed on this
// machine, on its signed-in plan, one question about a small project. It
// uses a little of the plan, so it runs only when given the Review role's
// model as Mini-Orca names it:
//
//	MINI_ORCA_ASK_SMOKE=claude:sonnet go test ./internal/app -run TestRealAsk -v
//	MINI_ORCA_ASK_SMOKE=agy:gemini-model-slug go test ./internal/app -run TestRealAsk -v
//	MINI_ORCA_ASK_SMOKE=pi:openai-codex/gpt-5.5 go test ./internal/app -run TestRealAsk -v
func TestRealAskAnswersWithCheckedCitations(t *testing.T) {
	selection := os.Getenv("MINI_ORCA_ASK_SMOKE")
	if selection == "" {
		t.Skip("set MINI_ORCA_ASK_SMOKE to the Review model (claude:…, agy:… or pi:…) to ask a real CLI")
	}
	if _, _, ok := cliModelSelection(selection); !ok {
		t.Fatalf("MINI_ORCA_ASK_SMOKE=%q is not a CLI model Mini-Orca knows", selection)
	}
	fixture, root := newSemanticAnalysisService(t, "http://127.0.0.1:1", 0)
	writeDocsFixture(t, root)
	cfg := scopedTestConfig("http://127.0.0.1:1")
	cfg.AgentRuntime.Roles.Review = config.ModelScope(selection)
	service, err := New(cfg, fixture.manager)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.manager.Reindex(); err != nil {
		t.Fatal(err)
	}
	answer, err := service.AskCodebase(context.Background(), askRequest(t, service, "Which package defines Item, and which package keeps the list of items?"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s: %d turns, %d tool calls\n%s", answer.Model, answer.Turns, answer.ToolCalls, answer.Answer)
	cited := map[string]bool{}
	for _, citation := range answer.Citations {
		t.Logf("cited %s:%d-%d %q", citation.Path, citation.StartLine, citation.EndLine, citation.Quote)
		cited[citation.Path] = true
	}
	if !cited["store/store.go"] && !cited["model/model.go"] || !strings.Contains(strings.ToLower(answer.Answer), "store") {
		t.Fatalf("the answer does not rest on the code: %+v", answer)
	}
}
