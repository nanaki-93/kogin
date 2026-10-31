package app

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

func TestIssueTaskTitlesFitTheTaskTitleLimit(t *testing.T) {
	long := "savePassword in cmd/master.go is a multi-file update that is not atomic. generateKey removes the legacy key file, writes .pwd-salt and switches the in-memory master key, and only then persists the re-encrypted vault, so a failed save leaves the vault undecryptable."
	runOn := strings.Repeat("the vault and its key file can disagree after a crash ", 6)
	accents := strings.Repeat("é", 150)
	for _, test := range []struct {
		name, title, want string
	}{
		{"short", "Total rounds down", "Fix: Total rounds down"},
		{"first sentence", long, "Fix: savePassword in cmd/master.go is a multi-file update that is not atomic"},
		{"cut at a word", runOn, ""},
		{"cut on a rune", accents, ""},
		{"longer than a criterion", strings.Repeat(runOn, 5), ""},
	} {
		task := issueTask(t.TempDir(), project.UnifiedFinding{Title: test.title, Message: test.title, Category: project.FindingCategoryBugs})
		if len(task.Title) > maxTaskTitleBytes || !utf8.ValidString(task.Title) || !strings.HasPrefix(task.Title, "Fix: ") {
			t.Fatalf("%s: title %q (%d bytes) does not fit", test.name, task.Title, len(task.Title))
		}
		if test.want != "" && task.Title != test.want {
			t.Fatalf("%s: title = %q, want %q", test.name, task.Title, test.want)
		}
		if test.want == "" && (!strings.HasSuffix(task.Title, "…") || strings.Contains(task.Title, " …")) {
			t.Fatalf("%s: title = %q, want it cut at a word with an ellipsis", test.name, task.Title)
		}
		if criterion := task.AcceptanceCriteria[0]; !strings.HasPrefix(criterion, "Resolve “"+strings.TrimPrefix(strings.TrimSuffix(task.Title, "…"), "Fix: ")) {
			t.Fatalf("%s: criterion = %q, want it to quote the title", test.name, criterion)
		}
		if !strings.Contains(task.Description, strings.TrimSpace(test.title)) {
			t.Fatalf("%s: description lost the full message: %q", test.name, task.Description)
		}
		request := ChangeCreateRequest{Title: task.Title, Kind: task.Kind, Paths: []string{"cmd/master.go"}, AcceptanceCriteria: task.AcceptanceCriteria}
		if err := validateChangeCreateRequest(request); err != nil {
			t.Fatalf("%s: the drafted task is refused: %v", test.name, err)
		}
	}
}

func TestMaintainabilityIssueTitlesFitTheTaskTitleLimit(t *testing.T) {
	title := strings.Repeat("split the oversized handler ", 10)
	task := issueTask(t.TempDir(), project.UnifiedFinding{Title: title, Category: project.FindingCategoryMaintainability, Location: project.FindingLocation{Path: "cmd/main.go"}})
	if len(task.Title) > maxTaskTitleBytes || strings.HasPrefix(task.Title, "Fix: ") {
		t.Fatalf("title = %q (%d bytes)", task.Title, len(task.Title))
	}
}
