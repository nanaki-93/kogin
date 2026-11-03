package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/llm"
	"github.com/nanaki-93/kogin/v2/internal/project"
)

// reanalysisProvider answers every request with reply and keeps the last prompt.
func reanalysisProvider(t *testing.T, calls *atomic.Int32, prompt *atomic.Value, during func(), reply string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		prompt.Store(string(body))
		if during != nil {
			during()
		}
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: reply}}}})
	}))
	t.Cleanup(server.Close)
	return server.URL
}

// openIssueTask captures a fix for a finding in main.go, with a plan and a
// manual proposal, the way "Fix with agents" leaves it after a first edit.
func openIssueTask(t *testing.T, service *Service, targets ...project.FindingTarget) *ChangeSession {
	t.Helper()
	index, err := service.manager.Index()
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.OpenChangeSession(context.Background(), ChangeCreateRequest{
		ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision, Kind: "fix", Title: "Fix: Run ignores errors",
		Paths: []string{"main.go"}, AcceptanceCriteria: []string{"Run reports print errors."},
		Plan:           &ChangePlanInput{Problem: "Run ignores the error from fmt.Println.", Approach: "Return the error to the caller."},
		FindingTargets: targets,
	})
	if err != nil {
		t.Fatal(err)
	}
	proposed, err := service.SendChangeMessage(context.Background(), session.ID, ChangeMessageRequest{ChangeIdentity: changeIdentity(session), Message: "First attempt.", Changes: []ChangeEditInput{{Path: "main.go", Content: "package main\n\nimport \"fmt\"\n\nfunc Run() error { _, err := fmt.Println(\"run\"); return err }\n"}}})
	if err != nil {
		t.Fatal(err)
	}
	return proposed
}

func editMain(t *testing.T, root, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

const editedMain = "package main\n\nimport \"fmt\"\n\n// Run prints a greeting.\nfunc Run() { fmt.Println(\"hello\") }\n"

func TestReanalyzeKeepsAStillValidTaskAtTheCurrentSource(t *testing.T) {
	var calls atomic.Int32
	var prompt atomic.Value
	url := reanalysisProvider(t, &calls, &prompt, nil, `{"still_valid":true,"reason":"Run still drops the error from fmt.Println."}`)
	service, root := newSemanticAnalysisService(t, url, 0)
	service.conf().runtimes.bug.effective.RemoteProvider = true
	index, _ := service.manager.Index()
	target := project.FindingTarget{ProjectID: index.ProjectID, Kind: "semantic", ID: "finding:run", Path: "main.go"}
	session := openIssueTask(t, service, target)
	request := ChangeReanalysisRequest{ChangeIdentity: changeIdentity(session), ConfirmRemoteProvider: true}

	if _, err := service.ReanalyzeChange(context.Background(), session.ID, request); err == nil || !strings.Contains(err.Error(), "matches the current files") || calls.Load() != 0 {
		t.Fatalf("a current task was re-analyzed: %v (calls %d)", err, calls.Load())
	}
	editMain(t, root, editedMain)
	if _, err := service.ReanalyzeChange(context.Background(), session.ID, ChangeReanalysisRequest{ChangeIdentity: changeIdentity(session)}); err == nil || calls.Load() != 0 {
		t.Fatalf("a remote Bug model was called without consent: %v", err)
	}
	old := request
	old.Revision--
	if _, err := service.ReanalyzeChange(context.Background(), session.ID, old); !errors.Is(err, project.ErrRevisionConflict) || calls.Load() != 0 {
		t.Fatalf("another task version was re-analyzed: %v", err)
	}

	result, err := service.ReanalyzeChange(context.Background(), session.ID, request)
	if err != nil || result.Outcome != ReanalysisStillValid || result.Reason != "Run still drops the error from fmt.Println." || calls.Load() != 1 {
		t.Fatalf("re-analysis = %+v, %v (calls %d)", result, err, calls.Load())
	}
	sent := prompt.Load().(string)
	for _, want := range []string{"Fix: Run ignores errors", "Run ignores the error from fmt.Println.", "Run prints a greeting.", "main.go changed since this proposal was made"} {
		if !strings.Contains(sent, want) {
			t.Fatalf("the request lacks %q: %s", want, sent)
		}
	}
	refreshed := result.Session
	if refreshed.ID != session.ID || refreshed.Revision != session.Revision+1 || refreshed.Freshness != "current" || len(refreshed.Changes) != 0 || refreshed.Targets[0].Content != editedMain {
		t.Fatalf("task not captured at the current source: %+v", refreshed)
	}
	if refreshed.Plan == nil || refreshed.Plan.Problem != session.Plan.Problem || refreshed.Plan.ApprovedHash != "" || len(refreshed.FindingTargets) != 1 || refreshed.FindingTargets[0] != target || refreshed.Title != session.Title || refreshed.CreatedAt != session.CreatedAt {
		t.Fatalf("task intent was not kept: %+v", refreshed)
	}
	if len(refreshed.Messages) != 1 || !strings.Contains(refreshed.Messages[0].Content, "still present. Run still drops") {
		t.Fatalf("decision note = %+v", refreshed.Messages)
	}
	stored, err := service.ChangeSession(context.Background(), session.ID)
	if err != nil || stored.Freshness != "current" || stored.Revision != refreshed.Revision || stored.Hash != refreshed.Hash {
		t.Fatalf("stored task = %+v, %v", stored, err)
	}
	if _, err := service.ReanalyzeChange(context.Background(), session.ID, ChangeReanalysisRequest{ChangeIdentity: changeIdentity(stored), ConfirmRemoteProvider: true}); err == nil || calls.Load() != 1 {
		t.Fatalf("the refreshed task was re-analyzed again: %v", err)
	}
}

func TestReanalyzeDeletesATaskThatIsNoLongerValidAndDismissesItsIssues(t *testing.T) {
	var calls atomic.Int32
	var prompt atomic.Value
	url := reanalysisProvider(t, &calls, &prompt, nil, "```json\n{\"still_valid\":false,\"reason\":\"Run no longer calls fmt.Println with an ignored error.\"}\n```")
	service, root := newSemanticAnalysisService(t, url, 0)
	_, _, input, err := service.workspaceState()
	if err != nil {
		t.Fatal(err)
	}
	store, err := project.NewFindingStore(root)
	if err != nil {
		t.Fatal(err)
	}
	reported, err := store.ReconcileSource(input, project.FindingSourceAI, []project.UnifiedFinding{{Source: project.FindingSourceAI, Confidence: project.FindingConfidenceSuggested, Severity: "medium", Title: "Run ignores errors", Message: "Run ignores the error from fmt.Println.", Location: project.FindingLocation{Path: "main.go", StartLine: 5, EndLine: 5}}})
	if err != nil {
		t.Fatal(err)
	}
	own := project.FindingTarget{ProjectID: input.ProjectID, Kind: "semantic", ID: reported[0].ID, Path: "main.go"}
	other := project.FindingTarget{ProjectID: "other-checkout", Kind: "semantic", ID: "finding:elsewhere", Path: "main.go"}
	session := openIssueTask(t, service, own, other)
	editMain(t, root, editedMain)

	result, err := service.ReanalyzeChange(context.Background(), session.ID, ChangeReanalysisRequest{ChangeIdentity: changeIdentity(session)})
	if err != nil || result.Outcome != ReanalysisNotValid || result.Deleted != session.ID || result.DismissedIssues != 1 || len(result.Warnings) != 0 || result.Session != nil || calls.Load() != 1 {
		t.Fatalf("re-analysis = %+v, %v", result, err)
	}
	if _, err := loadChangeSession(root, session.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the task was kept: %v", err)
	}
	findings, err := store.Load(input)
	if err != nil || len(findings) != 1 || findings[0].Status != project.FindingStatusDismissed {
		t.Fatalf("linked issue = %+v, %v", findings, err)
	}
}

func TestReanalyzeLeavesOutReadContextThatNoLongerExists(t *testing.T) {
	var calls atomic.Int32
	var prompt atomic.Value
	service, root := newSemanticAnalysisServiceWithHelper(t, reanalysisProvider(t, &calls, &prompt, nil, `{"still_valid":true,"reason":"Run still drops the error."}`), 0)
	index, _ := service.manager.Index()
	session, err := service.OpenChangeSession(context.Background(), ChangeCreateRequest{
		ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision, Kind: "fix", Title: "Fix: Run ignores errors",
		Paths: []string{"main.go"}, AcceptanceCriteria: []string{"Run reports print errors."},
		Plan: &ChangePlanInput{Problem: "Run ignores errors.", Approach: "Return them.", ReadPaths: []string{"helper.go"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "helper.go")); err != nil {
		t.Fatal(err)
	}
	result, err := service.ReanalyzeChange(context.Background(), session.ID, ChangeReanalysisRequest{ChangeIdentity: changeIdentity(session)})
	if err != nil || result.Outcome != ReanalysisStillValid || len(result.Session.Plan.ReadPaths) != 0 || len(result.Session.Plan.ReadTargets) != 0 || calls.Load() != 1 {
		t.Fatalf("re-analysis = %+v, %v", result, err)
	}
}

func TestReanalyzeSavesNothingWhenTheFilesOrTheAnswerAreNotUsable(t *testing.T) {
	root := ""
	var calls atomic.Int32
	var prompt atomic.Value
	for _, test := range []struct {
		name   string
		reply  string
		during func()
		want   error
	}{
		{name: "file edited again", reply: `{"still_valid":false,"reason":"Fixed."}`, during: func() { editMain(t, root, editedMain+"// again\n") }, want: project.ErrRevisionConflict},
		{name: "no verdict", reply: `{"reason":"Unclear."}`},
		{name: "extra field", reply: `{"still_valid":true,"reason":"Still there.","confidence":"high"}`},
		{name: "empty reason", reply: `{"still_valid":true,"reason":"  "}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls.Store(0)
			service, dir := newSemanticAnalysisService(t, reanalysisProvider(t, &calls, &prompt, test.during, test.reply), 0)
			root = dir
			session := openIssueTask(t, service)
			editMain(t, root, editedMain)
			_, err := service.ReanalyzeChange(context.Background(), session.ID, ChangeReanalysisRequest{ChangeIdentity: changeIdentity(session)})
			if err == nil || test.want != nil && !errors.Is(err, test.want) || calls.Load() == 0 {
				t.Fatalf("error = %v, want %v (calls %d)", err, test.want, calls.Load())
			}
			stored, err := loadChangeSession(root, session.ID)
			if err != nil || stored.Revision != session.Revision || stored.Hash != session.Hash || len(stored.Changes) != 1 {
				t.Fatalf("a failed re-analysis changed the task: %+v, %v", stored, err)
			}
		})
	}
}
