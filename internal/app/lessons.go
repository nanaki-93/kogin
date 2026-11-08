package app

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/app/history"
	"github.com/nanaki-93/kogin/v2/internal/textutil"
)

// The learning log: what each accepted task taught — the problem it removed,
// the approach that removed it and the concepts its change used — and the
// Ask the code answers worth keeping. It accumulates per project in the task
// history, so meeting a concept again links back to where it was met before.

const (
	maxLessonTitle        = 300
	maxLessonText         = 2000
	maxLessonConcepts     = 5
	maxLessonPaths        = 8
	defaultLessonsListing = 200
)

// Lesson is one learning-log entry on the wire.
type Lesson struct {
	ID       int64     `json:"id"`
	Kind     string    `json:"kind"`
	TaskID   string    `json:"task_id,omitempty"`
	Title    string    `json:"title"`
	Problem  string    `json:"problem,omitempty"`
	Insight  string    `json:"insight,omitempty"`
	Concepts []string  `json:"concepts,omitempty"`
	Paths    []string  `json:"paths,omitempty"`
	At       time.Time `json:"at"`
}

// LessonsReport is the learning log, newest first, with each concept's count.
type LessonsReport struct {
	ProjectID       string        `json:"project_id"`
	ProjectRevision string        `json:"project_revision"`
	Lessons         []Lesson      `json:"lessons"`
	Concepts        []ConceptSeen `json:"concepts"`
}

// ConceptSeen is how often one concept appears in the log.
type ConceptSeen struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// AskLessonRequest saves an Ask the code answer to the learning log.
type AskLessonRequest struct {
	ProjectID       string   `json:"project_id"`
	ProjectRevision string   `json:"project_revision"`
	Question        string   `json:"question"`
	Answer          string   `json:"answer"`
	Paths           []string `json:"paths,omitempty"`
}

// Lessons is the project's learning log.
func (s *Service) Lessons(ctx context.Context) (*LessonsReport, error) {
	index, err := s.manager.Index()
	if err != nil {
		return nil, err
	}
	store, err := history.Open(historyDir(s.manager.Root()))
	if err != nil {
		return nil, err
	}
	defer store.Close()
	entries, err := store.Lessons(ctx, defaultLessonsListing)
	if err != nil {
		return nil, err
	}
	report := &LessonsReport{ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision, Lessons: []Lesson{}, Concepts: []ConceptSeen{}}
	counts := map[string]int{}
	for _, entry := range entries {
		report.Lessons = append(report.Lessons, Lesson{ID: entry.ID, Kind: entry.Kind, TaskID: entry.TaskID, Title: entry.Title, Problem: entry.Problem,
			Insight: entry.Insight, Concepts: entry.Concepts, Paths: entry.Paths, At: entry.CreatedAt})
		for _, concept := range entry.Concepts {
			counts[concept]++
		}
	}
	for name, count := range counts {
		report.Concepts = append(report.Concepts, ConceptSeen{Name: name, Count: count})
	}
	slices.SortFunc(report.Concepts, func(a, b ConceptSeen) int {
		if a.Count != b.Count {
			return b.Count - a.Count
		}
		return strings.Compare(a.Name, b.Name)
	})
	return report, nil
}

// AddAskLesson saves an Ask the code answer to the learning log.
func (s *Service) AddAskLesson(ctx context.Context, request AskLessonRequest) (*LessonsReport, error) {
	if err := s.requireActiveRevision(request.ProjectID, request.ProjectRevision); err != nil {
		return nil, err
	}
	question := strings.TrimSpace(request.Question)
	answer := strings.TrimSpace(request.Answer)
	if question == "" || answer == "" || len(question) > maxAskQuestion*4 || len(answer) > maxAskAnswer {
		return nil, fmt.Errorf("save a question and its answer, within their limits")
	}
	if len(request.Paths) > maxLessonPaths*2 {
		return nil, fmt.Errorf("a saved answer cites at most %d files", maxLessonPaths*2)
	}
	var paths []string
	for _, path := range request.Paths {
		clean, err := cleanToolPath(path)
		if err != nil || clean != path {
			return nil, fmt.Errorf("a saved answer's paths name project files")
		}
		if !slices.Contains(paths, clean) && len(paths) < maxLessonPaths {
			paths = append(paths, clean)
		}
	}
	lesson := history.Lesson{Kind: history.LessonAsk, Title: textutil.Truncate(question, maxLessonTitle),
		Insight: textutil.Truncate(answer, maxLessonText), Paths: paths, CreatedAt: time.Now().UTC()}
	store, err := history.Open(historyDir(s.manager.Root()))
	if err != nil {
		return nil, err
	}
	err = store.AddLesson(ctx, lesson)
	_ = store.Close()
	if err != nil {
		return nil, err
	}
	return s.Lessons(ctx)
}

// recordTaskLesson adds an accepted task to the learning log, best effort:
// its problem and approach from the approved plan, and the concepts its
// walkthrough tagged. A task never fails because its lesson was not kept.
func recordTaskLesson(root string, session *ChangeSession) {
	lesson := history.Lesson{Kind: history.LessonTask, TaskID: session.ID, Title: textutil.Truncate(session.Title, maxLessonTitle), CreatedAt: time.Now().UTC()}
	if plan := session.Plan; plan != nil {
		lesson.Problem = textutil.Truncate(plan.Problem, maxLessonText)
		lesson.Insight = textutil.Truncate(plan.Approach, maxLessonText)
	}
	if lesson.Problem == "" {
		lesson.Problem = textutil.Truncate(walkthroughProblem(session), maxLessonText)
	}
	lesson.Concepts = lessonConcepts(session)
	for _, edit := range session.Changes {
		if len(lesson.Paths) == maxLessonPaths {
			break
		}
		lesson.Paths = append(lesson.Paths, edit.Path)
	}
	withHistory(root, func(store *history.Store) error {
		return store.AddLesson(context.Background(), lesson)
	})
}

// walkthroughProblem is the first problem the walkthrough names, when the
// task has no plan to name one.
func walkthroughProblem(session *ChangeSession) string {
	walkthrough := session.Walkthrough
	if walkthrough == nil || walkthrough.Status != WalkthroughReady || walkthrough.ProposalHash != session.Hash {
		return ""
	}
	for _, file := range walkthrough.Files {
		for _, note := range file.Notes {
			if note.Problem != "" {
				return note.Problem
			}
		}
	}
	return ""
}

// lessonConcepts are the concepts the task's walkthrough tagged, most used
// first, at most five.
func lessonConcepts(session *ChangeSession) []string {
	walkthrough := session.Walkthrough
	if walkthrough == nil || walkthrough.Status != WalkthroughReady || walkthrough.ProposalHash != session.Hash {
		return nil
	}
	counts := map[string]int{}
	var order []string
	for _, file := range walkthrough.Files {
		for _, note := range file.Notes {
			for _, concept := range note.Concepts {
				if counts[concept] == 0 {
					order = append(order, concept)
				}
				counts[concept]++
			}
		}
	}
	slices.SortStableFunc(order, func(a, b string) int { return counts[b] - counts[a] })
	if len(order) > maxLessonConcepts {
		order = order[:maxLessonConcepts]
	}
	return order
}
