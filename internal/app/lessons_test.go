package app

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAnAcceptedTaskLeavesALessonWithItsPlanAndConcepts(t *testing.T) {
	service, root := newSemanticAnalysisService(t, "http://127.0.0.1:0", 0)
	session := &ChangeSession{ID: "change-1", ProjectID: "p", Title: "Persist the salt", Hash: "h1",
		Plan: &ChangePlan{ChangePlanInput: ChangePlanInput{Problem: "Each run derived a new key, so the vault could not be opened again.", Approach: "Read the saved salt before deriving."}, Source: planSourceInvestigator},
		Walkthrough: &ChangeWalkthrough{ProposalHash: "h1", Status: WalkthroughReady, Files: []WalkthroughFile{
			{Path: "cmd/master.go", Notes: []WalkthroughNote{
				{Problem: "The salt was discarded.", Concepts: []string{"error wrapping", "table-driven test"}},
				{Problem: "The reader is shared.", Concepts: []string{"error wrapping"}},
			}},
		}},
		Changes: []ChangeEdit{{Path: "cmd/master.go", Content: "package cmd\n"}, {Path: "cmd/master_test.go", Content: "package cmd\n"}}}
	recordTaskLesson(root, session)

	report, err := service.Lessons(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Lessons) != 1 {
		t.Fatalf("lessons = %+v", report.Lessons)
	}
	lesson := report.Lessons[0]
	if lesson.Kind != "task" || lesson.TaskID != "change-1" || lesson.Title != "Persist the salt" {
		t.Fatalf("lesson = %+v", lesson)
	}
	if !strings.Contains(lesson.Problem, "vault could not be opened") || !strings.Contains(lesson.Insight, "Read the saved salt") {
		t.Fatalf("lesson text = %+v", lesson)
	}
	// Concepts come most used first and count across the log.
	if len(lesson.Concepts) != 2 || lesson.Concepts[0] != "error wrapping" || len(lesson.Paths) != 2 {
		t.Fatalf("lesson detail = %+v", lesson)
	}
	if len(report.Concepts) != 2 || report.Concepts[0].Name != "error wrapping" || report.Concepts[0].Count != 1 {
		t.Fatalf("concepts = %+v", report.Concepts)
	}
	if lesson.At.IsZero() || time.Since(lesson.At) > time.Minute {
		t.Fatalf("lesson time = %v", lesson.At)
	}
}

func TestAnAskAnswerIsSavedToTheLearningLogWithCheckedPaths(t *testing.T) {
	service, _ := newSemanticAnalysisService(t, "http://127.0.0.1:0", 0)
	index, err := service.manager.Index()
	if err != nil {
		t.Fatal(err)
	}
	request := AskLessonRequest{ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision,
		Question: "How does the daemon find the salt?", Answer: "It reads .pwd-salt beside the vault; generateKey derives from it.", Paths: []string{"cmd/master.go", "cmd/master.go", "cmd/add.go"}}
	report, err := service.AddAskLesson(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Lessons) != 1 || report.Lessons[0].Kind != "ask" || len(report.Lessons[0].Paths) != 2 {
		t.Fatalf("lessons = %+v", report.Lessons)
	}
	// A path outside the project is refused and nothing is saved.
	bad := request
	bad.Paths = []string{"../outside.go"}
	if _, err := service.AddAskLesson(context.Background(), bad); err == nil || !strings.Contains(err.Error(), "name project files") {
		t.Fatalf("outside path = %v", err)
	}
	// An empty answer is refused.
	empty := request
	empty.Answer = " "
	if _, err := service.AddAskLesson(context.Background(), empty); err == nil {
		t.Fatal("an empty answer was saved")
	}
	if report, err := service.Lessons(context.Background()); err != nil || len(report.Lessons) != 1 {
		t.Fatalf("log after refusals = %+v, %v", report, err)
	}
}
