package project

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestWalkthroughsAreBoundedAndKeepOnlyUsableSteps(t *testing.T) {
	long := strings.Repeat("x", 500)
	raw := []ProjectWalkthrough{
		{Title: "  Add an item ", Summary: long, Steps: []ProjectWalkthroughStep{
			{Text: " Decode it. ", Path: "./cmd/add.go", Symbol: " execAdd "},
			{Text: "Absolute.", Path: "/etc/passwd"},
			{Text: "Escapes.", Path: "../other/x.go"},
			{Text: "", Path: "cmd/add.go"},
			{Text: "Windows.", Path: `cmd\\add.go`},
		}},
		{Title: "", Summary: "No title.", Steps: []ProjectWalkthroughStep{{Text: "x", Path: "a.go"}}},
		{Title: "No steps", Summary: "Nothing usable.", Steps: []ProjectWalkthroughStep{{Text: "x", Path: ".."}}},
	}
	for i := 0; i < 8; i++ {
		raw = append(raw, ProjectWalkthrough{Title: "More", Summary: "s", Steps: []ProjectWalkthroughStep{{Text: "t", Path: "a.go"}}})
	}
	got := boundedWalkthroughs(raw)
	if len(got) != maxWalkthroughs {
		t.Fatalf("walkthroughs = %d, want %d", len(got), maxWalkthroughs)
	}
	first := got[0]
	if first.Title != "Add an item" || utf8.RuneCountInString(first.Summary) != maxWalkthroughSummary || !strings.HasSuffix(first.Summary, "…") || !reflect.DeepEqual(first.Steps, []ProjectWalkthroughStep{{Text: "Decode it.", Path: "cmd/add.go", Symbol: "execAdd"}}) {
		t.Fatalf("first = %+v", first)
	}
}

func TestWalkthroughStepsPointAtDeclarationsTheFilesHave(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"store/store.go": "package store\n\ntype Store struct{}\n\nfunc (s *Store) Save() error { return nil }\n\nfunc Open() *Store { return &Store{} }\n",
		"web/app.ts":     "export class Cart {\n  add(item: string) {\n    return item;\n  }\n}\n",
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	walkthroughs := []ProjectWalkthrough{{Title: "Save", Summary: "s", Steps: []ProjectWalkthroughStep{
		{Text: "Open the store.", Path: "store/store.go", Symbol: "store.Open"},
		{Text: "Save it.", Path: "store/store.go", Symbol: "(*Store).Save"},
		{Text: "Add to the cart.", Path: "web/app.ts", Symbol: "add"},
		{Text: "A guess.", Path: "web/app.ts", Symbol: "Cart.remove"},
		{Text: "Not indexed.", Path: "web/other.ts", Symbol: "x"},
	}}, {Title: "Gone", Summary: "s", Steps: []ProjectWalkthroughStep{{Text: "x", Path: "gone.go"}}}}
	got := checkWalkthroughs(root, []string{"store/store.go", "web/app.ts"}, walkthroughs)
	want := []ProjectWalkthrough{{Title: "Save", Summary: "s", Steps: []ProjectWalkthroughStep{
		{Text: "Open the store.", Path: "store/store.go", Symbol: "Open", Line: 7},
		{Text: "Save it.", Path: "store/store.go", Symbol: "Store.Save", Line: 5},
		{Text: "Add to the cart.", Path: "web/app.ts", Symbol: "Cart.add", Line: 2},
		{Text: "A guess.", Path: "web/app.ts"},
	}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("walkthroughs = %+v", got)
	}
}

func TestOverviewProseIsCutToItsShape(t *testing.T) {
	purpose := leadSentences("Kogin is a coding assistant. It runs locally, e.g. on a laptop. It has a web UI. And more.", 2, maxOverviewPurpose)
	if purpose != "Kogin is a coding assistant. It runs locally, e.g. on a laptop." {
		t.Fatalf("purpose = %q", purpose)
	}
	if got := leadSentences(strings.Repeat("word ", 100), 2, 40); utf8.RuneCountInString(got) > 40 || !strings.HasSuffix(got, "…") {
		t.Fatalf("one long sentence = %q", got)
	}
	architecture := architectureLines("- cmd: starts it\n\n* internal/api: decodes requests\ninternal/app: runs tasks\na\nb\nc\nd")
	if architecture != "cmd: starts it\ninternal/api: decodes requests\ninternal/app: runs tasks\na\nb" {
		t.Fatalf("architecture = %q", architecture)
	}
	if paragraph := architectureLines("One paragraph from an older prompt."); paragraph != "One paragraph from an older prompt." {
		t.Fatalf("paragraph = %q", paragraph)
	}
}

func TestChangeGuideIsParsedLikeWalkthroughs(t *testing.T) {
	output := `{"purpose":"A tool. It helps. It does more.","architecture":"cmd: entry\ninternal/store: saves","components":[],"walkthroughs":[],` +
		`"change_guide":[{"title":"Add a subcommand","summary":"Register it on the root command.","steps":[{"text":"Add the command file.","path":"./cmd/new.go","symbol":""},{"text":"","path":"cmd/root.go","symbol":"init"}]},{"title":"","summary":"x","steps":[{"text":"x","path":"a.go","symbol":""}]}],` +
		`"risks":[],"engineering_insight":null}`
	parsed, err := parseProjectAnalysisResponse(output)
	if err != nil {
		t.Fatal(err)
	}
	want := []ProjectWalkthrough{{Title: "Add a subcommand", Summary: "Register it on the root command.", Steps: []ProjectWalkthroughStep{{Text: "Add the command file.", Path: "cmd/new.go"}}}}
	if !reflect.DeepEqual(parsed.ChangeGuide, want) || parsed.Purpose != "A tool. It helps." || parsed.Architecture != "cmd: entry\ninternal/store: saves" {
		t.Fatalf("parsed = %+v", parsed)
	}
	if !strings.Contains(string(projectAnalysisResponseSchema().Schema), `"change_guide"`) {
		t.Fatal("the schema does not ask for the change guide")
	}
}
