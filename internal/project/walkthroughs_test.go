package project

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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
	if first.Title != "Add an item" || len(first.Summary) != maxWalkthroughSummary || !reflect.DeepEqual(first.Steps, []ProjectWalkthroughStep{{Text: "Decode it.", Path: "cmd/add.go", Symbol: "execAdd"}}) {
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
