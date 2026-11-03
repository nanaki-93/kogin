package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/config"
)

func cliScope(provider config.ModelProvider, model string) config.ModelProfileConfig {
	return config.ModelProfileConfig{Provider: provider, Model: model, CLIPath: os.Args[0]}
}

// onThreeSubscriptions writes with Claude Code, tests with agy and reviews
// with pi: each stage on its own subscription, none with an API key.
func onThreeSubscriptions(cfg *config.Config) {
	cfg.ModelScopes.Bug = cliScope(config.ClaudeProvider, "sonnet")
	cfg.ModelScopes.Analyze = cliScope(config.AgyProvider, "gemini-fake")
	cfg.ModelScopes.Code = cliScope(config.PiProvider, "fake/reviewer")
}

func subCoderSteps() []fakePiStep {
	return []fakePiStep{
		{Tool: "read_file", Arguments: map[string]any{"path": "calc.go"}},
		{Tool: "edit_file", Arguments: map[string]any{"path": "calc.go", "old_text": "func Add(a, b int) int { return a + b }\n", "new_text": "func Add(a, b int) int { return a + b }\n\n// Sub subtracts.\nfunc Sub(a, b int) int { return a - b }\n"}},
		{Tool: "run_checks", Arguments: map[string]any{}},
		{Tool: "finish", Arguments: map[string]any{"summary": "Added Sub next to Add.", "criteria": []map[string]any{{"criterion": 1, "how": "Sub returns a - b."}}}},
		{Text: "Done."},
	}
}

func subTesterSteps() []fakePiStep {
	return []fakePiStep{
		{Tool: "create_file", Arguments: map[string]any{"path": "calc_sub_test.go", "content": toolSubTest}},
		{Tool: "run_checks", Arguments: map[string]any{}},
		{Tool: "finish", Arguments: map[string]any{"summary": "Tested Sub, including a negative result.", "criteria": []map[string]any{{"criterion": 2, "how": "TestSub."}}}},
		{Text: "Done."},
	}
}

func fakeReviews(t *testing.T, dir string) []string { return fakeLog(t, dir, "reviews.log") }

// fakeRuns lists the agent runs the fake CLIs played, as cli:role.
func fakeRuns(t *testing.T, dir string) []string { return fakeLog(t, dir, "runs.log") }

func fakeLog(t *testing.T, dir, name string) []string {
	t.Helper()
	data, _ := os.ReadFile(filepath.Join(dir, name))
	return strings.Fields(string(data))
}

func TestATaskRunsEndToEndOnClaudeAgyAndPiSubscriptions(t *testing.T) {
	flow := startToolFlow(t, subCoderSteps(), subTesterSteps(), onThreeSubscriptions)
	finished := flow.finished(t)
	if finished.Workflow.Status != "awaiting_human_review" || !requiredChecksPassed(finished.Checks) {
		t.Fatalf("workflow = %s (%s), checks %+v", finished.Workflow.Status, finished.Workflow.Reason, finished.Checks)
	}
	if calc, test := editByPath(finished.Changes, "calc.go"), editByPath(finished.Changes, "calc_sub_test.go"); calc == nil || !strings.Contains(calc.Content, "func Sub(") || test == nil || test.Status != EditAdded {
		t.Fatalf("changes = %+v", finished.Changes)
	}
	if coder, tester := fakePiLog(t, flow.dir, "coder"), fakePiLog(t, flow.dir, "tester"); coder.CLI != "claude" || tester.CLI != "agy" || !strings.Contains(coder.Results[2], "All required checks pass.") || !strings.Contains(tester.Results[1], "- tests: PASSED") {
		t.Fatalf("coder %+v, tester %+v", coder, tester)
	}
	reviews := fakeReviews(t, flow.dir)
	if len(reviews) == 0 || strings.Join(reviews, "") != strings.Repeat("pi", len(reviews)) {
		t.Fatalf("reviews = %v", reviews)
	}
	flow.reviews.mu.Lock()
	defer flow.reviews.mu.Unlock()
	if len(flow.reviews.bodies) != 0 {
		t.Fatalf("a stage used an HTTP provider: %d requests", len(flow.reviews.bodies))
	}
	stages := finished.Workflow.Stages
	if stages[0].Model.ProviderOrigin != "cli://claude" || stages[1].Model.ProviderOrigin != "cli://agy" || stages[2].Model.ProviderOrigin != "cli://pi" {
		t.Fatalf("stage models = %+v / %+v / %+v", stages[0].Model, stages[1].Model, stages[2].Model)
	}
	if tools := stages[0].Tools; tools == nil || tools.Calls != 4 || tools.Turns != 5 || !tools.Finished {
		t.Fatalf("creation tools = %+v", tools)
	}
	if usage := stages[0].Usage; usage == nil || usage.ModelCalls != 5 || usage.InputTokens != 1000 || usage.OutputTokens != 50 {
		t.Fatalf("creation usage = %+v", usage)
	}
	if tools := stages[1].Tools; tools == nil || tools.Calls != 3 || tools.Turns != 4 || !tools.Finished {
		t.Fatalf("testing tools = %+v", tools)
	}
}

func TestAPlanLimitPausesTheTaskAndResumingContinuesIt(t *testing.T) {
	coder := append([]fakePiStep{{Limit: true}}, subCoderSteps()...)
	tester := append([]fakePiStep{subTesterSteps()[0], {Limit: true}}, subTesterSteps()[1:]...)
	flow := startToolFlow(t, coder, tester, onThreeSubscriptions)

	paused := flow.finished(t)
	w := paused.Workflow
	if w.Status != "paused" || w.Pause == nil || w.Pause.Stage != "create" || w.Pause.ResumeFrom != "create" || w.Pause.Provider != "claude" || !w.Pause.ResumesAt.Equal(time.Unix(fakeResetEpoch, 0)) {
		t.Fatalf("workflow = %s (%s), pause %+v", w.Status, w.Reason, w.Pause)
	}
	if !strings.Contains(w.Reason, "Claude plan reached its usage limit during the creation stage") || w.Stages[0].Status != "paused" || w.Stages[1].Status != "skipped" || len(paused.Changes) != 0 {
		t.Fatalf("paused run = %s, stages %s/%s, changes %d", w.Reason, w.Stages[0].Status, w.Stages[1].Status, len(paused.Changes))
	}
	if usage := w.Stages[0].Usage; usage == nil || usage.ModelCalls != 1 {
		t.Fatalf("the paused stage's usage = %+v", usage)
	}

	flow.resume(t, paused)
	paused = flow.finished(t)
	w = paused.Workflow
	if w.Status != "paused" || w.Pause == nil || w.Pause.Stage != "test" || w.Pause.ResumeFrom != "test" || w.Pause.Provider != "agy" || time.Until(w.Pause.ResumesAt) < 59*time.Minute {
		t.Fatalf("workflow = %s (%s), pause %+v", w.Status, w.Reason, w.Pause)
	}
	if w.Stages[0].Status != "completed" || editByPath(paused.Changes, "calc.go") == nil || editByPath(paused.Changes, "calc_sub_test.go") != nil {
		t.Fatalf("the coder's work = %s, changes %+v", w.Stages[0].Status, paused.Changes)
	}

	flow.resume(t, paused)
	finished := flow.finished(t)
	if finished.Workflow.Status != "awaiting_human_review" || finished.Workflow.Pause != nil || editByPath(finished.Changes, "calc_sub_test.go") == nil {
		t.Fatalf("workflow = %s (%s), changes %+v", finished.Workflow.Status, finished.Workflow.Reason, finished.Changes)
	}
	if runs := strings.Join(fakeRuns(t, flow.dir), " "); runs != "claude:coder claude:coder agy:tester agy:tester" {
		t.Fatalf("runs = %s", runs)
	}
}

func TestAPlanLimitInTheReviewPausesBeforeTheReviewAndKeepsTheChecks(t *testing.T) {
	flow := startToolFlow(t, subCoderSteps(), subTesterSteps(), func(cfg *config.Config) {
		onThreeSubscriptions(cfg)
		if err := os.WriteFile(filepath.Join(os.Getenv(fakeCLIDirEnv), "reviewer.limit"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	})
	paused := flow.finished(t)
	w := paused.Workflow
	if w.Status != "paused" || w.Pause == nil || w.Pause.Stage != "review" || w.Pause.ResumeFrom != "review" || w.Pause.Provider != "pi" || !requiredChecksPassed(paused.Checks) {
		t.Fatalf("workflow = %s (%s), pause %+v", w.Status, w.Reason, w.Pause)
	}
	if !strings.Contains(w.Reason, "subscription pi signs in with reached its usage limit during the review stage") || w.Stages[1].Status != "completed" || w.Stages[2].Status != "paused" {
		t.Fatalf("paused run = %s, stages %+v", w.Reason, w.Stages)
	}
	flow.resume(t, paused)
	finished := flow.finished(t)
	if finished.Workflow.Status != "awaiting_human_review" || finished.Workflow.Stages[0].Tools == nil || finished.Workflow.Stages[1].Tools == nil {
		t.Fatalf("workflow = %s (%s)", finished.Workflow.Status, finished.Workflow.Reason)
	}
	if runs := strings.Join(fakeRuns(t, flow.dir), " "); runs != "claude:coder agy:tester" {
		t.Fatalf("resuming the review ran the agents again: %s", runs)
	}
}

func TestAPauseBelongsToAPausedRunOnly(t *testing.T) {
	pause := &ChangeWorkflowPause{Stage: "test", ResumeFrom: "test", Provider: "agy"}
	for name, w := range map[string]*ChangeWorkflow{
		"no pause":      {Status: "paused"},
		"other stage":   {Status: "paused", Pause: &ChangeWorkflowPause{Stage: "human_review", ResumeFrom: "test", Provider: "agy"}},
		"other start":   {Status: "paused", Pause: &ChangeWorkflowPause{Stage: "test", ResumeFrom: "merge", Provider: "agy"}},
		"no provider":   {Status: "paused", Pause: &ChangeWorkflowPause{Stage: "test", ResumeFrom: "test"}},
		"not paused":    {Status: "failed", Pause: pause},
		"long provider": {Status: "paused", Pause: &ChangeWorkflowPause{Stage: "test", ResumeFrom: "test", Provider: strings.Repeat("x", 33)}},
	} {
		if validateWorkflowPause(w) == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	if validateWorkflowPause(&ChangeWorkflow{Status: "paused", Pause: pause}) != nil || validateWorkflowPause(&ChangeWorkflow{Status: "failed"}) != nil {
		t.Fatal("a valid pause was rejected")
	}
	restored := &ChangeWorkflow{Status: "outdated", Pause: pause}
	settleWorkflowPause(restored)
	if restored.Pause != nil {
		t.Fatal("a restored run kept its pause")
	}
}
