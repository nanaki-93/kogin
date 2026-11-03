package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/nanaki-93/kogin/v2/internal/app/archgate"
	"github.com/nanaki-93/kogin/v2/internal/project/syntax"
)

func (run *taskToolRun) writeTools() []toolSpec {
	return []toolSpec{
		{name: "edit_file", call: run.editFile,
			description: "Replace exact text in a file. old_text must appear exactly once unless replace_all is set; include enough surrounding lines to make it unique. Read the file first.",
			schema:      `{"type":"object","properties":{"path":{"type":"string"},"old_text":{"type":"string","minLength":1},"new_text":{"type":"string"},"replace_all":{"type":"boolean"}},"required":["path","old_text","new_text"],"additionalProperties":false}`},
		{name: "create_file", call: run.createFile,
			description: "Create a new file with the given content. Fails if the file exists; edit existing files with edit_file.",
			schema:      `{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"],"additionalProperties":false}`},
		{name: "delete_file", call: run.deleteFile, destructive: true,
			description: "Delete a file that the change no longer needs.",
			schema:      `{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`},
		{name: "rename_file", call: run.renameFile, destructive: true,
			description: "Move or rename a file. The destination must not exist.",
			schema:      `{"type":"object","properties":{"from":{"type":"string"},"to":{"type":"string"}},"required":["from","to"],"additionalProperties":false}`},
	}
}

func (run *taskToolRun) editFile(ctx context.Context, arguments json.RawMessage) (string, error) {
	var input struct {
		Path       string `json:"path"`
		OldText    string `json:"old_text"`
		NewText    string `json:"new_text"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := decodeToolArguments(arguments, &input); err != nil {
		return "", err
	}
	run.writeMu.Lock()
	defer run.writeMu.Unlock()
	clean, text, err := run.readText(input.Path)
	if err != nil {
		return "", err
	}
	count := strings.Count(text, input.OldText)
	switch {
	case input.OldText == "":
		return "", errors.New("old_text is empty; use create_file for a new file")
	case count == 0:
		return "", fmt.Errorf("old_text does not appear in %s; read the file again and copy the text exactly, including whitespace", clean)
	case count > 1 && !input.ReplaceAll:
		return "", fmt.Errorf("old_text appears %d times in %s; include more surrounding lines, or set replace_all", count, clean)
	}
	replaced, updated := 1, strings.Replace(text, input.OldText, input.NewText, 1)
	if input.ReplaceAll {
		replaced, updated = count, strings.ReplaceAll(text, input.OldText, input.NewText)
	}
	if err := run.writeFile(clean, updated, false); err != nil {
		return "", err
	}
	return fmt.Sprintf("Edited %s (%d replaced).%s", clean, replaced, run.feedback(ctx, clean, updated)), nil
}

func (run *taskToolRun) createFile(ctx context.Context, arguments json.RawMessage) (string, error) {
	var input struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := decodeToolArguments(arguments, &input); err != nil {
		return "", err
	}
	run.writeMu.Lock()
	defer run.writeMu.Unlock()
	clean, err := cleanToolPath(input.Path)
	if err != nil {
		return "", err
	}
	if full, err := run.existing(clean); err == nil {
		return "", fmt.Errorf("%s exists; change it with edit_file (%s)", clean, filepath.Base(full))
	}
	if strings.TrimSpace(input.Content) == "" {
		return "", fmt.Errorf("give %s some content", clean)
	}
	if err := run.writeFile(clean, input.Content, true); err != nil {
		return "", err
	}
	return "Created " + clean + "." + run.feedback(ctx, clean, input.Content), nil
}

func (run *taskToolRun) existing(clean string) (string, error) {
	_, full, err := run.readable(clean)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(full); err != nil {
		return "", err
	}
	return full, nil
}

func (run *taskToolRun) deleteFile(_ context.Context, arguments json.RawMessage) (string, error) {
	var input struct {
		Path string `json:"path"`
	}
	if err := decodeToolArguments(arguments, &input); err != nil {
		return "", err
	}
	run.writeMu.Lock()
	defer run.writeMu.Unlock()
	clean, full, err := run.writable(input.Path)
	if err != nil {
		return "", err
	}
	if info, err := os.Lstat(full); err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s does not exist or is not a file", clean)
	}
	if err := os.Remove(full); err != nil {
		return "", err
	}
	return "Deleted " + clean + ".", nil
}

func (run *taskToolRun) renameFile(ctx context.Context, arguments json.RawMessage) (string, error) {
	var input struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := decodeToolArguments(arguments, &input); err != nil {
		return "", err
	}
	run.writeMu.Lock()
	defer run.writeMu.Unlock()
	from, fromFull, err := run.writable(input.From)
	if err != nil {
		return "", err
	}
	to, toFull, err := run.writable(input.To)
	if err != nil {
		return "", err
	}
	if info, err := os.Lstat(fromFull); err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s does not exist or is not a file", from)
	}
	if _, err := os.Lstat(toFull); err == nil {
		return "", fmt.Errorf("%s exists", to)
	}
	if err := os.MkdirAll(filepath.Dir(toFull), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(fromFull, toFull); err != nil {
		return "", err
	}
	data, _ := os.ReadFile(toFull)
	return fmt.Sprintf("Moved %s to %s.", from, to) + run.feedback(ctx, to, string(data)), nil
}

// writeFile writes text through the diff policy, keeping an existing file's mode.
func (run *taskToolRun) writeFile(file, text string, create bool) error {
	if !utf8.ValidString(text) || strings.ContainsRune(text, 0) || len(text) > maxChangeBytes {
		return fmt.Errorf("%s must be UTF-8 text within 256 KiB", file)
	}
	clean, full, err := run.writable(file)
	if err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if info, err := os.Lstat(full); err == nil {
		if create || !info.Mode().IsRegular() {
			return fmt.Errorf("%s exists", clean)
		}
		mode = info.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, []byte(text), mode)
}

// feedback is what the gates say about a file the run just wrote: Go
// formatting, a parse error, and code budgets the file now breaks. Budgets
// are ratcheted against the task's base commit, as the checks do.
func (run *taskToolRun) feedback(ctx context.Context, file, text string) string {
	var notes []string
	if strings.HasSuffix(file, ".go") {
		if formatted, err := format.Source([]byte(text)); err != nil {
			notes = append(notes, "Go does not parse it: "+clip(err.Error(), 300))
		} else if string(formatted) != text {
			notes = append(notes, "It is not gofmt-formatted; the checks require gofmt formatting.")
		}
	} else if analysis := syntax.Analyze(file, []byte(text)); analysis.SyntaxError != "" {
		notes = append(notes, "The parser reports a syntax error at "+clip(analysis.SyntaxError, 300)+".")
	}
	base, _, exists, err := worktreeBaseFile(ctx, run.root, run.session.Worktree.Base, file)
	if err == nil {
		target := ChangeTarget{Path: file, Exists: exists, Content: base, Test: run.resolver.IsTestFile(file)}
		for _, report := range archgate.BudgetReports(run.root, []ChangeTarget{target}, []ChangeEdit{{Path: file, Content: text}}) {
			for _, over := range report.Over {
				notes = append(notes, "Over budget: "+over.String()+".")
			}
		}
	}
	if len(notes) == 0 {
		return ""
	}
	return "\n" + strings.Join(notes, "\n")
}
