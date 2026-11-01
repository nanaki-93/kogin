package launcher

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Event is one live update from the daemon's events stream.
type Event struct {
	Kind         string `json:"kind"`
	SessionID    string `json:"session_id"`
	WorkflowID   string `json:"workflow_id"`
	Title        string `json:"title"`
	Stage        string `json:"stage"`
	Status       string `json:"status"`
	WorktreePath string `json:"worktree_path"`
}

const (
	watchRetryFirst = time.Second
	watchRetryMax   = 15 * time.Second
	// A frame is one small JSON event; anything bigger is not one.
	maxEventLine = 1 << 20
)

// Watch follows the daemon's events until ctx ends, reconnecting after a
// dropped stream, and hands each update to handle, one at a time.
func (c *Client) Watch(ctx context.Context, handle func(Event)) {
	// The stream stays open; only ctx ends it.
	stream := &http.Client{}
	retry := watchRetryFirst
	for ctx.Err() == nil {
		if c.follow(ctx, stream, handle) {
			retry = watchRetryFirst
		}
		select {
		case <-ctx.Done():
		case <-time.After(retry):
		}
		retry = min(2*retry, watchRetryMax)
	}
}

// follow reads one connection of the stream; it reports whether it connected.
func (c *Client) follow(ctx context.Context, stream *http.Client, handle func(Event)) bool {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/api/projects/current/events", nil)
	if err != nil {
		return false
	}
	request.Header.Set("Accept", "text/event-stream")
	response, err := stream.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxEventLine)
	name, data := "", ""
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if name == "update" && data != "" {
				var event Event
				if json.Unmarshal([]byte(data), &event) == nil {
					handle(event)
				}
			}
			name, data = "", ""
		case strings.HasPrefix(line, "event:"):
			name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data += strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
	}
	return true
}

// Notice is an outcome worth telling someone who is not looking at the app.
type Notice struct {
	// ID names the outcome: the same outcome has the same ID.
	ID    string
	Title string
	Body  string
	// Route is the Web UI page that shows it, as its hash route.
	Route string
}

// The ends of a task's agent run that ask something of the user, worded as
// the Web UI announces them.
var taskNotices = map[string]struct{ title, body string }{
	"awaiting_human_review": {"Ready for your review", "Agents finished “%s”. It needs your review."},
	"changes_requested":     {"Changes requested", "Reviewers requested changes on “%s”."},
	"failed":                {"Agent run failed", "The agent run on “%s” failed."},
	"stale":                 {"Task out of date", "“%s” is out of date; its run stopped."},
	"paused":                {"Agents paused", "Agents paused on “%s”: a subscription plan reached its usage limit."},
}

// The ends of an analysis run worth telling, after it was seen running.
var analysisNotices = map[string]struct{ title, body string }{
	"completed":       {"Analysis finished", "Open Analysis to see what it found."},
	"completed_empty": {"Analysis finished", "It reported no issues."},
	"partial":         {"Analysis finished with failures", "Some stages failed; Retry failed runs only those again."},
	"failed":          {"Analysis failed", "Open Analysis to see why."},
	"paused":          {"Analysis paused", "Its time window ended; resume it on the Analysis page."},
}

var analysisActive = map[string]bool{"queued": true, "running": true, "pausing": true}

// Notices turns daemon events into notices, each outcome once. It keeps what
// it has seen, so one Notices follows one events stream.
type Notices struct {
	// told holds the task outcomes already told.
	told map[string]bool
	// running holds the analysis runs seen running and not ended since.
	running map[string]bool
}

func NewNotices() *Notices {
	return &Notices{told: map[string]bool{}, running: map[string]bool{}}
}

// Notice is the notice an event calls for, if any.
func (n *Notices) Notice(event Event) (Notice, bool) {
	switch event.Kind {
	case "workflow":
		notice, ok := taskNotice(event)
		if !ok || n.told[notice.ID] {
			return Notice{}, false
		}
		n.told[notice.ID] = true
		return notice, true
	case "analysis":
		return n.analysisNotice(event)
	default:
		return Notice{}, false
	}
}

// taskNotice tells the end of a task's run (an event without a stage).
func taskNotice(event Event) (Notice, bool) {
	words, ok := taskNotices[event.Status]
	if !ok || event.Stage != "" || event.SessionID == "" {
		return Notice{}, false
	}
	title := event.Title
	if title == "" {
		title = "a task"
	}
	route := "/tasks/" + url.PathEscape(event.SessionID)
	if event.WorktreePath != "" {
		route += "?" + url.Values{"wt": {event.WorktreePath}}.Encode()
	}
	return Notice{
		ID:    "task-" + event.SessionID + "-" + event.WorkflowID + "-" + event.Status,
		Title: words.title,
		Body:  strings.ReplaceAll(words.body, "%s", title),
		Route: route,
	}, true
}

// analysisNotice tells the end of an analysis run seen running, so a run that
// had already ended when the stream began is not told, and a run paused and
// resumed is told each time it stops.
func (n *Notices) analysisNotice(event Event) (Notice, bool) {
	if event.WorkflowID == "" {
		return Notice{}, false
	}
	if analysisActive[event.Status] {
		n.running[event.WorkflowID] = true
		return Notice{}, false
	}
	if !n.running[event.WorkflowID] {
		return Notice{}, false
	}
	delete(n.running, event.WorkflowID)
	words, ok := analysisNotices[event.Status]
	if !ok {
		return Notice{}, false // Canceled, interrupted or stale: nothing to tell.
	}
	return Notice{
		ID:    "analysis-" + event.WorkflowID + "-" + event.Status,
		Title: words.title,
		Body:  words.body,
		Route: "/analysis",
	}, true
}
