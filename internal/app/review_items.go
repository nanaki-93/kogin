package app

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/nanaki-93/mini-orca/v2/internal/textutil"
)

// ReviewItem is one blocking finding or advisory note of a review: a few
// words, the file and line it is about when the reviewer quoted the code, and
// the detail behind it.
type ReviewItem struct {
	Title  string `json:"title"`
	Path   string `json:"path,omitempty"`
	Line   int    `json:"line,omitempty"`
	Detail string `json:"detail,omitempty"`
}

const (
	// maxReviewTitle is in characters: a few words that fit one line.
	maxReviewTitle  = 100
	maxReviewDetail = 1024
	maxReviewPath   = 512
)

// UnmarshalJSON also reads the text findings of reviews stored before items
// had a title: the first sentence is the title and the text the detail.
func (item *ReviewItem) UnmarshalJSON(data []byte) error {
	var text string
	if json.Unmarshal(data, &text) == nil {
		*item = reviewItemFromText(text)
		return nil
	}
	type plain ReviewItem
	var value plain
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*item = ReviewItem(value)
	return nil
}

func reviewItemFromText(text string) ReviewItem {
	text = strings.TrimSpace(text)
	item := ReviewItem{Title: textutil.FirstSentence(text, maxReviewTitle)}
	if item.Title != strings.Join(strings.Fields(text), " ") {
		item.Detail = textutil.Truncate(text, maxReviewDetail)
	}
	return item
}

// reviewItemWire is a finding or note as a reviewer writes it: the code it is
// about as a quote, which the daemon turns into a line.
type reviewItemWire struct {
	Title  string `json:"title"`
	Path   string `json:"path"`
	Quote  string `json:"quote"`
	Detail string `json:"detail"`
}

// reviewItemSchema is loose on purpose: CLI runners reject a whole reply that
// breaks the schema, so lengths are trimmed here instead.
const reviewItemSchema = `{"type":"object","required":["title"],"properties":{"title":{"type":"string","minLength":1,"maxLength":300},"path":{"type":"string","maxLength":512},"quote":{"type":"string","maxLength":1024},"detail":{"type":"string","maxLength":2048}}}`

// decodeReviewItems reads a reviewer's findings or notes, as objects or as
// plain text, and places each in the task's files when it can.
func decodeReviewItems(raw []json.RawMessage, files map[string]string) ([]ReviewItem, error) {
	items := make([]ReviewItem, 0, len(raw))
	for _, data := range raw {
		var text string
		if json.Unmarshal(data, &text) == nil {
			items = append(items, reviewItemFromText(text))
			continue
		}
		var wire reviewItemWire
		if err := json.Unmarshal(data, &wire); err != nil {
			return nil, fmt.Errorf("invalid review item: %w", err)
		}
		items = append(items, wire.item(files))
	}
	return items, nil
}

func (wire reviewItemWire) item(files map[string]string) ReviewItem {
	detail := strings.TrimSpace(wire.Detail)
	title := strings.Join(strings.Fields(wire.Title), " ")
	if title == "" {
		title = detail
	}
	item := ReviewItem{Title: textutil.FirstSentence(title, maxReviewTitle), Detail: textutil.Truncate(detail, maxReviewDetail)}
	item.Path, item.Line = placeQuote(cleanReviewPath(wire.Path), wire.Quote, files)
	return item
}

// cleanReviewPath is a reviewer's path as a project-relative one, or empty.
func cleanReviewPath(raw string) string {
	cleaned := path.Clean(strings.TrimPrefix(strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/"), "./"))
	if raw == "" || cleaned == "." || path.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, "../") || len(cleaned) > maxReviewPath {
		return ""
	}
	return cleaned
}

// placeQuote finds the line a quote starts on: in the named file, or in the
// one file that holds it when the name is missing or wrong. A path the task
// does not have is dropped; a quote that is not found leaves the line out.
func placeQuote(name, quote string, files map[string]string) (string, int) {
	if _, ok := files[name]; !ok {
		name = ""
	}
	needle := firstQuoteLine(quote)
	if needle == "" {
		return name, 0
	}
	if name != "" {
		return name, lineOf(files[name], needle)
	}
	found, line := "", 0
	for candidate, content := range files {
		if at := lineOf(content, needle); at > 0 {
			if found != "" {
				return "", 0 // In more than one file: no place is sure.
			}
			found, line = candidate, at
		}
	}
	return found, line
}

// firstQuoteLine is a quote's first line with code on it, when it is long
// enough to find its place.
func firstQuoteLine(quote string) string {
	for _, line := range strings.Split(quote, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			if len(trimmed) < 4 {
				return ""
			}
			return trimmed
		}
	}
	return ""
}

// lineOf is the 1-based line of content that holds needle, or 0.
func lineOf(content, needle string) int {
	for i, line := range strings.Split(content, "\n") {
		if strings.Contains(strings.TrimSpace(line), needle) {
			return i + 1
		}
	}
	return 0
}

// reviewFiles are the files a review can point at: each target as the
// proposal leaves it, and the plan's read-only context.
func reviewFiles(session *ChangeSession) map[string]string {
	files := map[string]string{}
	if session == nil {
		return files
	}
	for _, target := range session.Targets {
		files[target.Path] = target.Content
	}
	if session.Plan != nil {
		for _, target := range session.Plan.ReadTargets {
			files[target.Path] = target.Content
		}
	}
	for _, edit := range session.Changes {
		if edit.Deleted() {
			delete(files, edit.Path)
		} else {
			files[edit.Path] = edit.Content
		}
	}
	return files
}

// validReviewItem bounds what a stored review item holds.
func validReviewItem(item ReviewItem) bool {
	return boundedWorkflowText(item.Title, 4*maxReviewTitle) &&
		(item.Detail == "" || boundedWorkflowText(item.Detail, maxReviewDetail)) &&
		len(item.Path) <= maxReviewPath && item.Line >= 0 && (item.Line == 0 || item.Path != "")
}

// String is how an item reads in a prompt: its words, place and detail.
func (item ReviewItem) String() string {
	text := item.Title
	if item.Path != "" {
		place := item.Path
		if item.Line > 0 {
			place = fmt.Sprintf("%s:%d", item.Path, item.Line)
		}
		text += " (" + place + ")"
	}
	if item.Detail != "" {
		text += ": " + item.Detail
	}
	return text
}
