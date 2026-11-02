package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/config"
	"github.com/nanaki-93/kasuri/v2/internal/llm"
)

const (
	toolCalcSource = "package main\n\n// Add adds.\nfunc Add(a, b int) int { return a + b }\n"
	toolCalcTest   = "package main\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 2) != 4 {\n\t\tt.Fatal(\"Add\")\n\t}\n}\n"
	toolSubTest    = "package main\n\nimport \"testing\"\n\nfunc TestSub(t *testing.T) {\n\tif Sub(5, 3) != 2 || Sub(0, 1) != -1 {\n\t\tt.Fatal(\"Sub\")\n\t}\n}\n"
)

// reviewRecorder approves every proposal and keeps what the reviewers read.
type reviewRecorder struct {
	mu     sync.Mutex
	bodies []string
}

func (r *reviewRecorder) serve(t *testing.T) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		r.mu.Lock()
		r.bodies = append(r.bodies, string(body))
		r.mu.Unlock()
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: `{"verdict":"approve","summary":"Sub is correct and tested.","findings":[]}`}}}})
	}))
	t.Cleanup(server.Close)
	return server
}

func piScope(model string) config.ModelProfileConfig {
	return config.ModelProfileConfig{Provider: config.PiProvider, Model: model, CLIPath: os.Args[0]}
}

// toolFlow is a workflow in a task with its own worktree whose creation and
// testing stages are CLI agents played by the test binary.
type toolFlow struct {
	service  *Service
	id       string
	dir      string
	reviews  *reviewRecorder
	worktree string
	request  ChangeWorkflowRequest
}

// startToolFlow starts the workflow on pi agents and an HTTP reviewer;
// configure may give the stages other models.
func startToolFlow(t *testing.T, coder, tester []fakePiStep, configure func(*config.Config)) *toolFlow {
	t.Helper()
	dir := t.TempDir()
	for role, steps := range map[string][]fakePiStep{"coder": coder, "tester": tester} {
		data, _ := json.Marshal(steps)
		if err := os.WriteFile(filepath.Join(dir, role+".json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(fakeCLIEnv, "1")
	t.Setenv(fakeCLIDirEnv, dir)
	reviews := &reviewRecorder{}
	server := reviews.serve(t)
	service, root := newSemanticAnalysisService(t, server.URL, 0)
	cfg := scopedTestConfig(server.URL)
	cfg.ModelScopes.Bug, cfg.ModelScopes.Analyze = piScope("fake/coder"), piScope("fake/tester")
	cfg.ModelScopes.Code.Model = "reviewer"
	if configure != nil {
		configure(cfg)
	}
	configured, err := New(cfg, service.manager)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(configured.CancelChangeWorkflows)
	for name, content := range map[string]string{"go.mod": "module example.test/toolflow\n\ngo 1.22\n", "calc.go": toolCalcSource, "calc_test.go": toolCalcTest, "AGENTS.md": "# Toolflow\n\nKeep functions small.\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	initializeChangeGit(t, configured, root)
	trustedGitRequest(t, configured)
	request := worktreeTaskRequest(t, configured, "main.go", "calc.go")
	request.Kind, request.Title, request.AcceptanceCriteria = "feature", "Add Sub", []string{"Sub returns a minus b.", "Sub is tested."}
	opened, err := configured.OpenChangeInWorktree(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	child, _, err := configured.WorktreeService(context.Background(), opened.WorktreePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(child.CancelChangeWorkflows)
	workflow := ChangeWorkflowRequest{ChangeIdentity: changeIdentity(opened.ChangeSession), Message: "Add Sub(a, b) next to Add.", Models: ChangeWorkflowModels{Create: "bug", Test: "analyze", Review: "code"}, ConfirmedProfiles: []string{"bug", "analyze", "code"}}
	if _, err := child.StartChangeWorkflow(context.Background(), opened.ID, workflow); err != nil {
		t.Fatal(err)
	}
	return &toolFlow{service: child, id: opened.ID, dir: dir, reviews: reviews, worktree: opened.WorktreePath, request: workflow}
}

func (f *toolFlow) finished(t *testing.T) *ChangeSession {
	t.Helper()
	return finishedWorkflow(t, f.service, f.id)
}

// resume runs the workflow again from its pause.
func (f *toolFlow) resume(t *testing.T, paused *ChangeSession) {
	t.Helper()
	request := f.request
	request.ChangeIdentity, request.ResumeFrom = changeIdentity(paused), paused.Workflow.Pause.ResumeFrom
	if _, err := f.service.StartChangeWorkflow(context.Background(), f.id, request); err != nil {
		t.Fatal(err)
	}
}

// toolWorkflow runs a workflow whose creation and testing stages are pi
// agents played by the test binary, in a task with its own worktree.
func toolWorkflow(t *testing.T, coder, tester []fakePiStep) (*ChangeSession, string, *reviewRecorder, string) {
	t.Helper()
	flow := startToolFlow(t, coder, tester, nil)
	return flow.finished(t), flow.dir, flow.reviews, flow.worktree
}

func fakePiLog(t *testing.T, dir, role string) fakePiRecord {
	t.Helper()
	var record fakePiRecord
	data, err := os.ReadFile(filepath.Join(dir, role+".log.json"))
	if err != nil || json.Unmarshal(data, &record) != nil {
		t.Fatalf("no %s run was recorded: %v", role, err)
	}
	return record
}

func TestAWorktreeTaskOnPiIsWrittenAndTestedByToolUsingAgents(t *testing.T) {
	coder := []fakePiStep{
		{Tool: "read_file", Arguments: map[string]any{"path": "calc.go"}},
		{Tool: "edit_file", Arguments: map[string]any{"path": "calc.go", "old_text": "func Add(a, b int) int { return a + b }\n", "new_text": "func Add(a, b int) int { return a + b }\n\n// Sub subtracts.\nfunc Sub(a, b int) int { return a - b }\n"}},
		{Tool: "run_checks", Arguments: map[string]any{}},
		{Tool: "finish", Arguments: map[string]any{"summary": "Added Sub next to Add.", "criteria": []map[string]any{{"criterion": 1, "how": "Sub returns a - b."}}}},
		{Text: "Done."},
	}
	tester := []fakePiStep{
		{Tool: "create_file", Arguments: map[string]any{"path": "calc_sub_test.go", "content": toolSubTest}},
		{Tool: "run_checks", Arguments: map[string]any{}},
		{Tool: "finish", Arguments: map[string]any{"summary": "Tested Sub, including a negative result.", "criteria": []map[string]any{{"criterion": 2, "how": "TestSub."}}}},
		{Text: "Done."},
	}
	finished, dir, reviews, _ := toolWorkflow(t, coder, tester)
	if finished.Workflow.Status != "awaiting_human_review" || !requiredChecksPassed(finished.Checks) {
		t.Fatalf("workflow = %+v (%s), checks %+v", finished.Workflow.Status, finished.Workflow.Reason, finished.Checks)
	}
	if calc, test := editByPath(finished.Changes, "calc.go"), editByPath(finished.Changes, "calc_sub_test.go"); calc == nil || !strings.Contains(calc.Content, "func Sub(") || test == nil || test.Status != EditAdded {
		t.Fatalf("changes = %+v", finished.Changes)
	}
	var coderReport, testerReport bool
	for _, message := range finished.Messages {
		coderReport = coderReport || strings.Contains(message.Content, "Added Sub next to Add.\n- Criterion 1: Sub returns a - b.\n\nTools: 4 calls (edit_file 1, finish 1, read_file 1, run_checks 1) in 5 model turns.")
		testerReport = testerReport || strings.Contains(message.Content, "Tested Sub, including a negative result.")
	}
	if !coderReport || !testerReport {
		t.Fatalf("messages = %+v", finished.Messages)
	}

	coderRun := fakePiLog(t, dir, "coder")
	if !strings.Contains(coderRun.Prompt, "Project guidance (AGENTS.md) for these files; follow it:\n--- AGENTS.md ---\n# Toolflow") || !strings.Contains(coderRun.Prompt, "Task (feature): Add Sub") || !strings.Contains(coderRun.Prompt, "1. Sub returns a minus b.") || !strings.Contains(coderRun.Results[2], "All required checks pass.") {
		t.Fatalf("coder run = %+v", coderRun)
	}
	testerRun := fakePiLog(t, dir, "tester")
	if !strings.Contains(testerRun.Prompt, "--- a/calc.go\n+++ b/calc.go\n") || !strings.Contains(testerRun.Prompt, "+func Sub(a, b int) int { return a - b }") || !strings.Contains(testerRun.Results[1], "- tests: PASSED") {
		t.Fatalf("tester run = %+v", testerRun)
	}

	if tools := finished.Workflow.Stages[0].Tools; tools == nil || tools.Calls != 4 || tools.Turns != 5 || !tools.Finished || tools.Summary != "edit_file 1, finish 1, read_file 1, run_checks 1" {
		t.Fatalf("creation tools = %+v", tools)
	}
	if tools := finished.Workflow.Stages[1].Tools; tools == nil || tools.Calls != 3 {
		t.Fatalf("testing tools = %+v", tools)
	}
	creation := finished.Workflow.Stages[0].Usage
	if creation == nil || creation.ModelCalls != 5 || creation.InputTokens != 5000 || creation.OutputTokens != 250 {
		t.Fatalf("creation usage = %+v", creation)
	}
	reviews.mu.Lock()
	defer reviews.mu.Unlock()
	if len(reviews.bodies) == 0 || !strings.Contains(reviews.bodies[0], `+++ b/calc_sub_test.go`) {
		t.Fatalf("the reviewers did not get the diff: %d requests", len(reviews.bodies))
	}
}

func TestATesterWithNothingToAddPassesTheCodersTests(t *testing.T) {
	coder := []fakePiStep{
		{Tool: "edit_file", Arguments: map[string]any{"path": "calc.go", "old_text": "func Add(a, b int) int { return a + b }\n", "new_text": "func Add(a, b int) int { return a + b }\n\n// Sub subtracts.\nfunc Sub(a, b int) int { return a - b }\n"}},
		{Tool: "create_file", Arguments: map[string]any{"path": "calc_sub_test.go", "content": toolSubTest}},
		{Tool: "finish", Arguments: map[string]any{"summary": "Added Sub and its test."}},
		{Text: "Done."},
	}
	tester := []fakePiStep{
		{Tool: "read_file", Arguments: map[string]any{"path": "calc_sub_test.go"}},
		{Tool: "finish", Arguments: map[string]any{"summary": "TestSub already covers both criteria; nothing to add."}},
		{Text: "Done."},
	}
	finished, _, _, _ := toolWorkflow(t, coder, tester)
	if finished.Workflow.Status != "awaiting_human_review" || editByPath(finished.Changes, "calc_sub_test.go") == nil {
		t.Fatalf("workflow = %s (%s), changes %+v", finished.Workflow.Status, finished.Workflow.Reason, finished.Changes)
	}
}

func TestAFailedAgentRunLeavesTheWorktreeAsTheProposalWas(t *testing.T) {
	coder := []fakePiStep{
		{Tool: "edit_file", Arguments: map[string]any{"path": "calc.go", "old_text": "a + b", "new_text": "a + b + 0"}},
		{Tool: "create_file", Arguments: map[string]any{"path": "scratch.go", "content": "package main\n"}},
		{Exit: 7},
	}
	finished, _, _, worktree := toolWorkflow(t, coder, nil)
	if finished.Workflow.Status != "failed" || !strings.Contains(finished.Workflow.Reason, "exited with status 7") || len(finished.Changes) != 0 {
		t.Fatalf("workflow = %s (%s), changes %+v", finished.Workflow.Status, finished.Workflow.Reason, finished.Changes)
	}
	if data, _ := os.ReadFile(filepath.Join(worktree, "calc.go")); string(data) != toolCalcSource {
		t.Fatalf("calc.go was left as %q", data)
	}
	if _, err := os.Stat(filepath.Join(worktree, "scratch.go")); !os.IsNotExist(err) {
		t.Fatalf("scratch.go was left behind: %v", err)
	}
}
