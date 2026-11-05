package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/textutil"
)

// While a task's agents run, each tool call they make through Kogin's tools
// is kept in a short log in memory and told to listeners, so the task page
// can show the agent at work (what it reads, edits and runs) and the human
// can stop a run that is going the wrong way. The log is not stored: it
// covers the latest workflow of each task since the daemon started.

// AgentActivity is one tool call: when it started, the agent role that made
// it, the tool, what it named (a path, a pattern or a command), and how it
// ended ("running" until it returns, then "done" or "failed").
type AgentActivity struct {
	Seq    int       `json:"seq"`
	At     time.Time `json:"at"`
	Role   string    `json:"role"`
	Tool   string    `json:"tool"`
	Target string    `json:"target,omitempty"`
	Status string    `json:"status"`
	Detail string    `json:"detail,omitempty"`
}

// ChangeActivity is a task's log for its latest workflow, oldest first.
type ChangeActivity struct {
	WorkflowID string          `json:"workflow_id,omitempty"`
	Entries    []AgentActivity `json:"entries"`
}

const (
	maxActivityEntries = 80
	maxActivityTarget  = 160
	maxActivityDetail  = 200
)

type activityLog struct {
	mu   sync.Mutex
	logs map[string]*ChangeActivity
	seq  int
}

// activityTargetKeys are the argument fields that say what a call is about,
// in the order they are looked for.
var activityTargetKeys = []string{"path", "file", "pattern", "query", "command", "name"}

// activityTarget is what a call names, from its arguments.
func activityTarget(arguments json.RawMessage) string {
	var fields map[string]any
	if json.Unmarshal(arguments, &fields) != nil {
		return ""
	}
	for _, key := range activityTargetKeys {
		switch value := fields[key].(type) {
		case string:
			return textutil.Truncate(strings.TrimSpace(value), maxActivityTarget)
		case []any:
			parts := make([]string, 0, len(value))
			for _, part := range value {
				if text, ok := part.(string); ok {
					parts = append(parts, text)
				}
			}
			return textutil.Truncate(strings.Join(parts, " "), maxActivityTarget)
		}
	}
	return ""
}

// startActivity records a call that begins and returns its sequence number;
// a new workflow on the task starts a new log.
func (s *Service) startActivity(session *ChangeSession, role toolRole, tool string, arguments json.RawMessage) int {
	if session == nil || role == toolReader {
		return 0
	}
	workflow := ""
	if session.Workflow != nil {
		workflow = session.Workflow.ID
	}
	entry := AgentActivity{At: time.Now().UTC(), Role: string(role), Tool: tool, Target: activityTarget(arguments), Status: "running"}
	s.activity.mu.Lock()
	if s.activity.logs == nil {
		s.activity.logs = map[string]*ChangeActivity{}
	}
	log := s.activity.logs[session.ID]
	if log == nil || log.WorkflowID != workflow {
		log = &ChangeActivity{WorkflowID: workflow}
		s.activity.logs[session.ID] = log
	}
	s.activity.seq++
	entry.Seq = s.activity.seq
	log.Entries = append(log.Entries, entry)
	if len(log.Entries) > maxActivityEntries {
		log.Entries = append([]AgentActivity(nil), log.Entries[len(log.Entries)-maxActivityEntries:]...)
	}
	s.activity.mu.Unlock()
	s.publishEvent(Event{Kind: "activity", SessionID: session.ID, WorkflowID: workflow, Activity: &entry})
	return entry.Seq
}

// endActivity records how the call ended.
func (s *Service) endActivity(session *ChangeSession, seq int, err error) {
	if session == nil || seq == 0 {
		return
	}
	s.activity.mu.Lock()
	var ended *AgentActivity
	if log := s.activity.logs[session.ID]; log != nil {
		for i := len(log.Entries) - 1; i >= 0; i-- {
			if log.Entries[i].Seq == seq {
				log.Entries[i].Status = "done"
				if err != nil {
					log.Entries[i].Status = "failed"
					log.Entries[i].Detail = textutil.Truncate(firstLine(err.Error()), maxActivityDetail)
				}
				copied := log.Entries[i]
				ended = &copied
				break
			}
		}
	}
	s.activity.mu.Unlock()
	if ended != nil {
		s.publishEvent(Event{Kind: "activity", SessionID: session.ID, Activity: ended})
	}
}

// ChangeActivity is what the task's agents did with their tools in its
// latest workflow; empty when nothing ran since the daemon started.
func (s *Service) ChangeActivity(id string) (*ChangeActivity, error) {
	if !changeIDPattern.MatchString(id) {
		return nil, fmt.Errorf("invalid change session id")
	}
	s.activity.mu.Lock()
	defer s.activity.mu.Unlock()
	log := s.activity.logs[id]
	if log == nil {
		return &ChangeActivity{Entries: []AgentActivity{}}, nil
	}
	return &ChangeActivity{WorkflowID: log.WorkflowID, Entries: append([]AgentActivity{}, log.Entries...)}, nil
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return line
}
