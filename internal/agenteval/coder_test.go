package agenteval

import (
	"context"
	_ "embed"
	"encoding/json"
	"os/exec"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/app"
	"github.com/nanaki-93/kogin/v2/internal/app/agentstate"
	"github.com/nanaki-93/kogin/v2/internal/config"
)

// The reference solutions show that every task can be done and that its
// hidden tests tell a solution from the starting code.
//
//go:embed testdata/coder_solutions.json
var coderSolutionsData []byte

type coderSolution struct {
	Write  map[string]string `json:"write"`
	Delete []string          `json:"delete"`
}

func coderSolutions(t *testing.T) map[string]coderSolution {
	t.Helper()
	var solutions map[string]coderSolution
	if err := json.Unmarshal(coderSolutionsData, &solutions); err != nil {
		t.Fatal(err)
	}
	return solutions
}

func (s coderSolution) changes() []app.CoderFile {
	var changes []app.CoderFile
	for path, content := range s.Write {
		changes = append(changes, app.CoderFile{Path: path, Status: "modified", Content: content})
	}
	for _, path := range s.Delete {
		changes = append(changes, app.CoderFile{Path: path, Status: "deleted"})
	}
	return changes
}

func TestEveryCoderTaskIsSolvableAndItsHiddenTestsFailOnTheStartingCode(t *testing.T) {
	corpus, err := decodeCoderCorpus(coderCorpusData)
	if err != nil {
		t.Fatal(err)
	}
	solutions := coderSolutions(t)
	for _, item := range corpus.Cases {
		t.Run(item.ID, func(t *testing.T) {
			if _, err := exec.LookPath(item.Test[0]); err != nil {
				t.Skipf("%s is not installed here", item.Test[0])
			}
			if state, reason, _ := scoreCoderAttempt(context.Background(), item, nil); state != "failed" {
				t.Fatalf("the starting code scored %s (%s); the hidden tests must fail on it", state, reason)
			}
			solution, ok := solutions[item.ID]
			if !ok {
				t.Fatal("no reference solution")
			}
			if state, reason, failing := scoreCoderAttempt(context.Background(), item, solution.changes()); state != "passed" {
				t.Fatalf("the reference solution scored %s: %s %v", state, reason, failing)
			}
		})
	}
}

func TestACoderCampaignScoresEachCoderAndComparesThem(t *testing.T) {
	solutions := coderSolutions(t)
	corpus, _ := decodeCoderCorpus(coderCorpusData)
	run := func(_ context.Context, _ *config.Config, task app.CoderTask, tools bool) (*app.CoderAttempt, error) {
		attempt := &app.CoderAttempt{Tools: tools, Usage: agentstate.BudgetUsage{ModelCalls: 1, InputTokens: 900, OutputTokens: 100}}
		if !tools {
			return attempt, nil // The one-shot coder changes nothing here.
		}
		attempt.Usage = agentstate.BudgetUsage{ModelCalls: 6, InputTokens: 12000, OutputTokens: 800}
		attempt.Stage = &app.ChangeStageTools{Calls: 9, Turns: 6, Finished: true}
		for _, item := range corpus.Cases {
			if item.Title == task.Title {
				attempt.Changes = solutions[item.ID].changes()
			}
		}
		return attempt, nil
	}
	cfg := config.Default()
	receipt, err := RunCoders(context.Background(), CoderOptions{Config: cfg, CaseID: "go-sum-evens", Coders: []string{CoderTools, CoderOneShot}, Confirmed: true, run: run})
	if err != nil || len(receipt.Cases) != 2 || receipt.ToolsAtLeastAsGood == nil || !*receipt.ToolsAtLeastAsGood {
		t.Fatalf("receipt = %+v, %v", receipt, err)
	}
	tools, oneShot := receipt.Summary[CoderTools], receipt.Summary[CoderOneShot]
	if tools.Passed != 1 || oneShot.Passed != 0 || tools.ModelCalls != 6 || tools.InputTokens != 12000 || oneShot.OutputTokens != 100 {
		t.Fatalf("summary = %+v", receipt.Summary)
	}
	if failed := receipt.Cases[1]; failed.State != "failed" || len(failed.Failing) == 0 || failed.Tools != nil {
		t.Fatalf("one-shot case = %+v", failed)
	}
	if receipt.Cases[0].Tools == nil || receipt.Cases[0].Tools.Calls != 9 {
		t.Fatalf("tools case = %+v", receipt.Cases[0])
	}

	for _, options := range []CoderOptions{{Config: cfg, Coders: []string{CoderTools}}, {Config: cfg, Confirmed: true}, {Config: cfg, Confirmed: true, Coders: []string{"magic"}}, {Confirmed: true, Coders: []string{CoderTools}}, {Config: cfg, Confirmed: true, Coders: []string{CoderTools}, CaseID: "missing"}} {
		if _, err := RunCoders(context.Background(), options); err == nil {
			t.Fatalf("options %+v were accepted", options)
		}
	}
}
