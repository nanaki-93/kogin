package app

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nanaki-93/mini-orca/v2/internal/app/archgate"
	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

const (
	overBudgetApp = "import { helper } from './helper';\nexport function compute(): number {\n  return 42;\n}\nexport function double(x: number): number {\n  return x * 2;\n}\n"
	splitApp      = "import { helper } from './helper';\nexport { double } from './app_more';\nexport function compute(): number {\n  return 42;\n}\n"
	splitSibling  = "export function double(x: number): number {\n  return x * 2;\n}\n"
)

func proposalReply(explanation string, files ...string) string {
	var changes []string
	for i := 0; i+1 < len(files); i += 2 {
		changes = append(changes, `{"path":`+strconv.Quote(files[i])+`,"content":`+strconv.Quote(files[i+1])+`}`)
	}
	return `{"explanation":` + strconv.Quote(explanation) + `,"changes":[` + strings.Join(changes, ",") + `]}`
}

func budgetWorkflowService(t *testing.T, creations []string) (*Service, *ChangeSession, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	create := changeProvider(t, func() string {
		n := int(calls.Add(1)) - 1
		return creations[min(n, len(creations)-1)]
	})
	tests := changeProvider(t, func() string {
		return proposalReply("Test compute.", "app.test.ts", "import { compute } from './app';\nif (compute() !== 42) throw new Error('bad');\n")
	})
	review := changeProvider(t, func() string { return workflowApproval })
	root := t.TempDir()
	for name, content := range map[string]string{
		"package.json": `{"name":"budget-fixture"}`,
		"Makefile":     "test:\n\t@true\n",
		"app.ts":       "import { helper } from './helper';\nexport function compute(): number {\n  return 1;\n}\n",
		"AGENTS.md":    "```kasuri\nbudgets:\n  file_lines: 6\n```\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(root, &project.Analysis{Name: "budget-fixture", Path: root, Type: "typescript"}); err != nil {
		t.Fatal(err)
	}
	cfg := scopedTestConfig(create.URL)
	cfg.ModelScopes.Bug.APIBaseURL, cfg.ModelScopes.Bug.Model = create.URL, "creator"
	cfg.ModelScopes.Analyze.APIBaseURL, cfg.ModelScopes.Analyze.Model = tests.URL, "tester"
	cfg.ModelScopes.Code.APIBaseURL, cfg.ModelScopes.Code.Model = review.URL, "reviewer"
	s, err := New(cfg, manager)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.CancelChangeWorkflows)
	session := openChangeFixture(t, s, "app.ts", "app.test.ts")
	if _, err := s.TrustProjectExecution(context.Background(), session.ProjectRevision, true); err != nil {
		t.Fatal(err)
	}
	return s, session, &calls
}

func TestAProposalOverItsFileBudgetIsSplitIntoASiblingInTheSameTask(t *testing.T) {
	s, session, calls := budgetWorkflowService(t, []string{
		proposalReply("Return 42 and add double.", "app.ts", overBudgetApp),
		proposalReply("Moved double to app_more.ts and re-exported it.", "app.ts", splitApp, "app_more.ts", splitSibling),
	})
	if _, err := s.StartChangeWorkflow(context.Background(), session.ID, workflowRequest(session)); err != nil {
		t.Fatal(err)
	}
	finished := finishedWorkflow(t, s, session.ID)
	if finished.Workflow.Status != "awaiting_human_review" || calls.Load() != 2 {
		t.Fatalf("workflow = %+v, creation calls %d", finished.Workflow, calls.Load())
	}
	sibling := changeTarget(finished, "app_more.ts")
	if sibling == nil || sibling.Exists || sibling.Test || len(finished.Changes) != 3 {
		t.Fatalf("targets = %+v, changes = %d", finished.Targets, len(finished.Changes))
	}
	var checked bool
	for _, message := range finished.Messages {
		checked = checked || strings.Contains(message.Content, "Kasuri checked the split: 2 function(s) kept or moved verbatim.")
	}
	if !checked || !requiredChecksPassed(finished.Checks) {
		t.Fatalf("messages = %+v, checks = %+v", finished.Messages, finished.Checks)
	}
}

func TestAFixupThatDropsAFunctionIsDiscarded(t *testing.T) {
	dropped := proposalReply("Removed double.", "app.ts", splitApp, "app_more.ts", "export const unused = 1;\n")
	s, session, calls := budgetWorkflowService(t, []string{proposalReply("Return 42 and add double.", "app.ts", overBudgetApp), dropped})
	if _, err := s.StartChangeWorkflow(context.Background(), session.ID, workflowRequest(session)); err != nil {
		t.Fatal(err)
	}
	finished := finishedWorkflow(t, s, session.ID)
	var discarded int
	for _, message := range finished.Messages {
		if strings.Contains(message.Content, "was discarded: the fix-up dropped 1 function(s): app.ts: double") {
			discarded++
		}
	}
	if discarded != maxBudgetFixupRounds || calls.Load() != 1+maxBudgetFixupRounds || changeTarget(finished, "app_more.ts") != nil || finished.Workflow.Status == "awaiting_human_review" {
		t.Fatalf("workflow = %+v, discarded %d, calls %d, targets %+v", finished.Workflow, discarded, calls.Load(), finished.Targets)
	}
}

func TestFixupSiblingsStayNextToTheirFile(t *testing.T) {
	session := &ChangeSession{Targets: []ChangeTarget{{Path: "src/app.ts", Exists: true}, {Path: "src/app.test.ts"}}}
	over := []archgate.BudgetReport{{Path: "src/app.ts"}}
	for _, candidate := range []ChangeTarget{{Path: "lib/app_more.ts"}, {Path: "src/app_more.py"}, {Path: "src/app_more.test.ts", Test: true}} {
		if origin, err := siblingOrigin(session, over, candidate); err == nil {
			t.Errorf("%s was accepted as a sibling of %s", candidate.Path, origin)
		}
	}
	if origin, err := siblingOrigin(session, over, ChangeTarget{Path: "src/app_more.ts"}); err != nil || origin != "src/app.ts" {
		t.Fatalf("sibling = %q, %v", origin, err)
	}
}
