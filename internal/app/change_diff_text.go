package app

import (
	"fmt"
	"strings"

	"github.com/nanaki-93/kogin/v2/internal/project/diff"
)

const diffContextLines = 3

// proposalDiffText renders a proposal as unified diffs with three lines of
// context, within limit bytes; what does not fit is named, not cut mid-file.
func proposalDiffText(edits []ChangeEdit, limit int) string {
	var out strings.Builder
	var left []string
	for _, edit := range edits {
		text := editDiffText(edit)
		if out.Len()+len(text) > limit {
			left = append(left, edit.Path)
			continue
		}
		out.WriteString(text)
	}
	if len(left) > 0 {
		fmt.Fprintf(&out, "(Diffs left out for space: %s. Read those files.)\n", strings.Join(left, ", "))
	}
	return out.String()
}

func editDiffText(edit ChangeEdit) string {
	var out strings.Builder
	oldPath, newPath := "a/"+edit.Path, "b/"+edit.Path
	switch {
	case edit.Status == EditAdded:
		oldPath = "/dev/null"
	case edit.Deleted():
		newPath = "/dev/null"
	case edit.OldPath != "":
		oldPath = "a/" + edit.OldPath
	}
	fmt.Fprintf(&out, "--- %s\n+++ %s\n", oldPath, newPath)
	for _, hunk := range diffHunks(edit.Diff.Lines) {
		out.WriteString(hunk)
	}
	return out.String()
}

// diffHunks groups changed lines with their context into unified hunks.
func diffHunks(lines []diff.DiffLine) []string {
	var hunks []string
	for start := 0; start < len(lines); {
		first := nextChange(lines, start)
		if first < 0 {
			break
		}
		from, to := max(first-diffContextLines, 0), first
		for next := first; next >= 0 && next <= to+2*diffContextLines; next = nextChange(lines, to+1) {
			to = next
		}
		to = min(to+diffContextLines, len(lines)-1)
		hunks = append(hunks, hunkText(lines[from:to+1]))
		start = to + 1
	}
	return hunks
}

func nextChange(lines []diff.DiffLine, from int) int {
	for index := from; index < len(lines); index++ {
		if lines[index].Kind == "added" || lines[index].Kind == "removed" {
			return index
		}
	}
	return -1
}

func hunkText(lines []diff.DiffLine) string {
	var body strings.Builder
	oldStart, newStart, oldCount, newCount := 0, 0, 0, 0
	for _, line := range lines {
		prefix := " "
		switch line.Kind {
		case "added":
			prefix = "+"
			newCount++
		case "removed":
			prefix = "-"
			oldCount++
		default:
			oldCount++
			newCount++
		}
		if oldStart == 0 && line.OldLine > 0 {
			oldStart = line.OldLine
		}
		if newStart == 0 && line.NewLine > 0 {
			newStart = line.NewLine
		}
		body.WriteString(prefix + line.Text + "\n")
	}
	return fmt.Sprintf("@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount) + body.String()
}
