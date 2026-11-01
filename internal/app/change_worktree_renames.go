package app

import (
	"path"
	"strings"

	"github.com/nanaki-93/kasuri/v2/internal/project/diff"
)

// pairRenames turns a deletion and an addition into one rename: first the
// moves the hints declare (the agent's renamed_from, or the renames of the
// stored proposal), then pairs with the same extension and at least 60% of
// their non-blank lines in common.
func pairRenames(edits []ChangeEdit, hints []ChangeEdit) []ChangeEdit {
	index := map[string]int{}
	for i, edit := range edits {
		index[edit.Path] = i
	}
	declared := map[int]int{}
	for _, hint := range hints {
		from, okFrom := index[hint.OldPath]
		to, okTo := index[hint.Path]
		if hint.OldPath != "" && okFrom && okTo && edits[from].Status == EditDeleted && edits[to].Status == EditAdded {
			declared[from] = to
		}
	}
	var paired []ChangeEdit
	used := map[int]bool{}
	for i, deleted := range edits {
		if deleted.Status != EditDeleted {
			continue
		}
		best := -1
		if to, ok := declared[i]; ok && !used[to] {
			best = to
		} else {
			best = similarAddition(edits, used, deleted)
		}
		if best < 0 {
			continue
		}
		used[i], used[best] = true, true
		renamed := edits[best]
		renamed.Status, renamed.OldPath = EditRenamed, deleted.Path
		renamed.Diff = diff.BuildUnifiedDiff(renamed.Path, removedText(deleted.Diff), renamed.Content)
		renamed.Diff.OldPath = deleted.Path
		paired = append(paired, renamed)
	}
	for i, edit := range edits {
		if !used[i] {
			paired = append(paired, edit)
		}
	}
	return paired
}

// similarAddition finds the unpaired addition most like a deleted file.
func similarAddition(edits []ChangeEdit, used map[int]bool, deleted ChangeEdit) int {
	best, score := -1, 0.6
	for j, added := range edits {
		if added.Status != EditAdded || used[j] || path.Ext(added.Path) != path.Ext(deleted.Path) {
			continue
		}
		if similarity := lineSimilarity(removedText(deleted.Diff), added.Content); similarity >= score {
			best, score = j, similarity
		}
	}
	return best
}

func removedText(unified diff.UnifiedDiff) string {
	var lines []string
	for _, line := range unified.Lines {
		if line.Kind != "added" {
			lines = append(lines, line.Text)
		}
	}
	return strings.Join(lines, "\n")
}

// lineSimilarity is the share of non-blank lines two texts have in common.
func lineSimilarity(a, b string) float64 {
	left, right := nonBlankLines(a), nonBlankLines(b)
	if len(left)+len(right) == 0 {
		return 1
	}
	counts := map[string]int{}
	for _, line := range left {
		counts[line]++
	}
	common := 0
	for _, line := range right {
		if counts[line] > 0 {
			counts[line]--
			common++
		}
	}
	return 2 * float64(common) / float64(len(left)+len(right))
}

func nonBlankLines(text string) []string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, strings.TrimSpace(line))
		}
	}
	return lines
}
