package project

import (
	"path"
	"strings"
	"unicode/utf8"

	"github.com/nanaki-93/kogin/v2/internal/project/syntax"
	"github.com/nanaki-93/kogin/v2/internal/textutil"
)

// boundedWalkthroughs trims the model's walkthroughs to their limits and
// drops the ones without a title or a usable step, so one bad walkthrough
// does not cost the whole overview.
func boundedWalkthroughs(raw []ProjectWalkthrough) []ProjectWalkthrough {
	walkthroughs := []ProjectWalkthrough{}
	for _, item := range raw {
		if len(walkthroughs) == maxWalkthroughs {
			break
		}
		walkthrough := ProjectWalkthrough{
			Title:   textutil.Clip(strings.TrimSpace(item.Title), maxWalkthroughTitle),
			Summary: textutil.Clip(strings.TrimSpace(item.Summary), maxWalkthroughSummary),
			Steps:   []ProjectWalkthroughStep{},
		}
		for _, raw := range item.Steps {
			if len(walkthrough.Steps) == maxWalkthroughSteps {
				break
			}
			step := ProjectWalkthroughStep{
				Text:   textutil.Clip(strings.TrimSpace(raw.Text), maxWalkthroughStepText),
				Path:   walkthroughPath(raw.Path),
				Symbol: textutil.Clip(strings.TrimSpace(raw.Symbol), maxWalkthroughSymbol),
			}
			if step.Text != "" && step.Path != "" {
				walkthrough.Steps = append(walkthrough.Steps, step)
			}
		}
		if walkthrough.Title != "" && len(walkthrough.Steps) > 0 {
			walkthroughs = append(walkthroughs, walkthrough)
		}
	}
	return walkthroughs
}

// walkthroughPath is a step's file as a clean project-relative path, or
// empty when the model named something else.
func walkthroughPath(raw string) string {
	cleaned := strings.Trim(strings.TrimSpace(raw), "`")
	if cleaned == "" || utf8.RuneCountInString(cleaned) > maxWalkthroughPath || strings.Contains(cleaned, "\\") {
		return ""
	}
	cleaned = path.Clean(strings.TrimPrefix(cleaned, "./"))
	if path.IsAbs(cleaned) || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return ""
	}
	return cleaned
}

// checkWalkthroughs keeps the steps whose files the project has and finds
// the line of each step's symbol in its file. A symbol the file does not
// declare is dropped: the step still points at the file, not at a guess.
func checkWalkthroughs(root string, files []string, walkthroughs []ProjectWalkthrough) []ProjectWalkthrough {
	known := make(map[string]bool, len(files))
	for _, file := range files {
		known[file] = true
	}
	outlines := map[string][]syntax.Symbol{}
	outline := func(file string) []syntax.Symbol {
		if symbols, ok := outlines[file]; ok {
			return symbols
		}
		var symbols []syntax.Symbol
		if resolved, err := ResolveFile(root, file); err == nil {
			if data, err := readLimited(resolved, maxFileViewBytes); err == nil && len(data) <= maxFileViewBytes && !isBinary(data) {
				symbols = syntax.Outline(file, data)
			}
		}
		outlines[file] = symbols
		return symbols
	}
	checked := []ProjectWalkthrough{}
	for _, walkthrough := range walkthroughs {
		steps := []ProjectWalkthroughStep{}
		for _, step := range walkthrough.Steps {
			if !known[step.Path] {
				continue
			}
			step.Symbol, step.Line = declaredSymbol(outline(step.Path), step.Symbol)
			steps = append(steps, step)
		}
		if len(steps) > 0 {
			walkthrough.Steps = steps
			checked = append(checked, walkthrough)
		}
	}
	return checked
}

// declaredSymbol finds a symbol in a file's outline: the same name, else a
// declaration whose name ends with it ("Run" or "Service.Run" for
// "Service.Run"). It returns the declared name and its line, or nothing.
func declaredSymbol(outline []syntax.Symbol, symbol string) (string, int) {
	wanted := strings.NewReplacer("(", "", ")", "", "*", "").Replace(strings.TrimSpace(symbol))
	if wanted == "" {
		return "", 0
	}
	for _, declared := range outline {
		if declared.Name == wanted {
			return declared.Name, declared.Line
		}
	}
	for _, declared := range outline {
		if strings.HasSuffix(declared.Name, "."+wanted) || strings.HasSuffix(wanted, "."+declared.Name) && !strings.Contains(declared.Name, ".") {
			return declared.Name, declared.Line
		}
	}
	return "", 0
}
