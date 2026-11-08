package history

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Lesson kinds: an accepted task, or an answer saved from Ask the code.
const (
	LessonTask = "task"
	LessonAsk  = "ask"
)

// Lesson is one entry of the project's learning log: what an accepted task
// taught (the problem it removed, the approach, the concepts its change
// used), or an Ask the code answer worth keeping.
type Lesson struct {
	ID        int64
	Kind      string
	TaskID    string
	Title     string
	Problem   string
	Insight   string
	Concepts  []string
	Paths     []string
	CreatedAt time.Time
}

// AddLesson appends one lesson to the learning log.
func (s *Store) AddLesson(ctx context.Context, lesson Lesson) error {
	concepts, err := packStrings(lesson.Concepts)
	if err != nil {
		return fmt.Errorf("record lesson concepts: %w", err)
	}
	paths, err := packStrings(lesson.Paths)
	if err != nil {
		return fmt.Errorf("record lesson paths: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO lessons (kind, task_id, title, problem, insight, concepts, paths, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		lesson.Kind, lesson.TaskID, lesson.Title, lesson.Problem, lesson.Insight, concepts, paths, stamp(lesson.CreatedAt)); err != nil {
		return fmt.Errorf("record lesson: %w", err)
	}
	return nil
}

// Lessons are the learning log's newest entries, newest first.
func (s *Store) Lessons(ctx context.Context, limit int) ([]Lesson, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, task_id, title, problem, insight, concepts, paths, created_at FROM lessons ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var lessons []Lesson
	for rows.Next() {
		var lesson Lesson
		var concepts, paths, created string
		if err := rows.Scan(&lesson.ID, &lesson.Kind, &lesson.TaskID, &lesson.Title, &lesson.Problem, &lesson.Insight, &concepts, &paths, &created); err != nil {
			return nil, err
		}
		lesson.Concepts = unpackStrings(concepts)
		lesson.Paths = unpackStrings(paths)
		if at, err := time.Parse(time.RFC3339Nano, created); err == nil {
			lesson.CreatedAt = at
		}
		lessons = append(lessons, lesson)
	}
	return lessons, rows.Err()
}

// packStrings stores a list as JSON; an empty list is empty text.
func packStrings(values []string) (string, error) {
	if len(values) == 0 {
		return "", nil
	}
	data, err := json.Marshal(values)
	return string(data), err
}

// unpackStrings reads a stored list; unreadable text is an empty list.
func unpackStrings(text string) []string {
	if text == "" {
		return nil
	}
	var values []string
	if json.Unmarshal([]byte(text), &values) != nil {
		return nil
	}
	return values
}
