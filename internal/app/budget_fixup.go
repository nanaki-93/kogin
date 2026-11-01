package app

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/app/archgate"
	"github.com/nanaki-93/mini-orca/v2/internal/llm"
	"github.com/nanaki-93/mini-orca/v2/internal/project"
	"github.com/nanaki-93/mini-orca/v2/internal/project/syntax"
)

const (
	// maxBudgetFixupRounds bounds the fix-up step after a stage writes code.
	maxBudgetFixupRounds = 2
	// maxSiblingFiles is how many new files one over-budget file may split into.
	maxSiblingFiles = 2
)

const budgetFixupPrompt = "You are the budget fix-up step of a coding workflow. The proposal below breaks the code budgets the project's AGENTS.md files declare. " +
	"Restructure it to meet every budget without changing behavior: move cohesive groups of functions, with the imports they need, into new sibling files, and extract helpers from functions that are too long or too complex. " +
	"Move code verbatim wherever you can, keep every function, and keep each file's public interface: where the language allows, re-export moved names from the original file so files outside this task need no change. " +
	"Return one JSON object: explanation (what moved where) and changes (array of {path, content, summary?}) with the complete new content of every file you change or create. " +
	"Write only the listed files and new files in the same directory with the same extension, at most %d new files per listed file; a test file may only be split into test files. Do not delete files."

// fitChangeToBudgets runs the budget fix-up step: while the proposal makes a
// file or function go over its budget, a model restructures the over-budget
// files, splitting them into new sibling files when needed. A round that
// loses a function or writes outside its files is discarded. What is still
// over after the last round is left to the complexity gate.
func (s *Service) fitChangeToBudgets(ctx context.Context, root string, session *ChangeSession, runtime modelRuntime, testsOnly bool) (*ChangeSession, error) {
	for round := 1; round <= maxBudgetFixupRounds; round++ {
		over := overBudgetFiles(session, archgate.BudgetReports(root, session.Targets, session.Changes), testsOnly)
		if len(over) == 0 {
			return session, nil
		}
		next, err := s.budgetFixupRound(ctx, root, session, runtime, over, round)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			next, err = s.noteBudgetFixup(ctx, root, session, fmt.Sprintf("Budget fix-up round %d was discarded: %s", round, sanitizeWorkflowError(err)))
			if err != nil {
				return nil, err
			}
		}
		session = next
	}
	return session, nil
}

func overBudgetFiles(session *ChangeSession, reports []archgate.BudgetReport, testsOnly bool) []archgate.BudgetReport {
	var over []archgate.BudgetReport
	for _, report := range reports {
		if len(report.Over) > 0 && (!testsOnly || isTestEditPath(session, report.Path)) {
			over = append(over, report)
		}
	}
	return over
}

func (s *Service) budgetFixupRound(ctx context.Context, root string, session *ChangeSession, runtime modelRuntime, over []archgate.BudgetReport, round int) (*ChangeSession, error) {
	findings := budgetFindingsText(over)
	messages := []llm.ChatMessage{
		{Role: "system", Content: fmt.Sprintf(budgetFixupPrompt, maxSiblingFiles)},
		{Role: "user", Content: budgetFixupRequest(session, over, findings)},
	}
	result, err := s.requestWorkflowModel(ctx, root, session, runtime, messages, changeResponseSchema())
	if err != nil {
		return nil, err
	}
	scoped, err := budgetFixupScope(root, session, over, result.Content)
	if err != nil {
		return nil, err
	}
	explanation, edits, err := parseChangeResponse(root, result.Content, scoped)
	if err != nil {
		return nil, err
	}
	moves, err := checkBudgetFixupMoves(session, scoped, over, edits)
	if err != nil {
		return nil, err
	}
	return s.storeBudgetFixup(ctx, root, session, scoped.Targets, edits, fmt.Sprintf("Budget fix-up, round %d. Budgets broken by the proposal:\n%s", round, findings), explanation+"\n\n"+moves)
}

func budgetFindingsText(over []archgate.BudgetReport) string {
	var findings strings.Builder
	for _, report := range over {
		for _, finding := range report.Over {
			findings.WriteString("- " + report.Path + ": " + finding.String() + "\n")
		}
	}
	return findings.String()
}

func budgetFixupRequest(session *ChangeSession, over []archgate.BudgetReport, findings string) string {
	var request strings.Builder
	request.WriteString("Budgets broken by this proposal:\n" + findings)
	for _, report := range over {
		if edit := proposedEdit(session, report.Path); edit != nil {
			fmt.Fprintf(&request, "\nFile %s (budgets: %s):\n```\n%s\n```\n", report.Path, describeBudgets(report.Budgets), edit.Content)
		}
	}
	var others []string
	for _, edit := range session.Changes {
		if !slices.ContainsFunc(over, func(report archgate.BudgetReport) bool { return report.Path == edit.Path }) {
			others = append(others, edit.Path)
		}
	}
	if len(others) > 0 {
		request.WriteString("\nOther files in this proposal, not to be written: " + strings.Join(others, ", ") + "\n")
	}
	return request.String()
}

// budgetFixupScope returns a copy of the session whose targets are what the
// round may write: the over-budget files and the new sibling files it names.
func budgetFixupScope(root string, session *ChangeSession, over []archgate.BudgetReport, output string) (*ChangeSession, error) {
	var response struct {
		Changes []struct {
			Path string `json:"path"`
		} `json:"changes"`
	}
	if err := json.Unmarshal([]byte(llm.UnwrapMarkdownJSON(output)), &response); err != nil {
		return nil, fmt.Errorf("decode budget fix-up: %w", err)
	}
	scoped := *session
	scoped.Targets = nil
	for _, report := range over {
		if target := changeTarget(session, report.Path); target != nil {
			scoped.Targets = append(scoped.Targets, *target)
		}
	}
	siblings, added := map[string]int{}, 0
	for _, change := range response.Changes {
		clean := path.Clean(filepath.ToSlash(change.Path))
		if changeTarget(&scoped, clean) != nil {
			continue
		}
		if changeTarget(session, clean) != nil {
			return nil, fmt.Errorf("the fix-up wrote %s, which is not over budget", boundedProposalPath(clean))
		}
		target, err := captureChangeTarget(root, clean)
		if err != nil {
			return nil, err
		}
		if target.Exists {
			return nil, fmt.Errorf("the fix-up wrote %s, which already exists outside the task", boundedProposalPath(clean))
		}
		origin, err := siblingOrigin(session, over, target)
		if err != nil {
			return nil, err
		}
		siblings[origin]++
		if added++; siblings[origin] > maxSiblingFiles || len(session.Targets)+added > maxStoredTaskPaths {
			return nil, fmt.Errorf("the fix-up created too many files for %s", origin)
		}
		scoped.Targets = append(scoped.Targets, target)
	}
	return &scoped, nil
}

// siblingOrigin returns the over-budget file a new file may split from: one in
// the same directory with the same extension and the same test-ness.
func siblingOrigin(session *ChangeSession, over []archgate.BudgetReport, candidate ChangeTarget) (string, error) {
	for _, report := range over {
		if path.Dir(report.Path) == path.Dir(candidate.Path) && path.Ext(report.Path) == path.Ext(candidate.Path) &&
			isTestEditPath(session, report.Path) == isTestTarget(candidate) {
			return report.Path, nil
		}
	}
	return "", fmt.Errorf("the fix-up wrote %s, which is not a sibling of an over-budget file", boundedProposalPath(candidate.Path))
}

// checkBudgetFixupMoves compares the functions of each over-budget file
// before the round with those of the file and its new siblings after it.
// Losing a function discards the round; a function that changed while it
// moved is named for the reviewer.
func checkBudgetFixupMoves(session, scoped *ChangeSession, over []archgate.BudgetReport, edits []ChangeEdit) (string, error) {
	var moved, changed, lost int
	var changedNames, lostNames []string
	for _, report := range over {
		before := proposedEdit(session, report.Path)
		if before == nil {
			continue
		}
		original := syntax.Analyze(before.Path, []byte(before.Content))
		if !original.Measured {
			continue
		}
		after := functionsAfterFixup(session, scoped, report.Path, edits)
		for _, function := range original.Functions {
			switch {
			case after.hashes[function.Hash]:
				moved++
			case after.names[shortName(function.Name)]:
				changed++
				changedNames = append(changedNames, report.Path+": "+function.Name)
			default:
				lost++
				lostNames = append(lostNames, report.Path+": "+function.Name)
			}
		}
	}
	if lost > 0 {
		return "", fmt.Errorf("the fix-up dropped %d function(s): %s", lost, strings.Join(lostNames, ", "))
	}
	summary := fmt.Sprintf("Kasuri checked the split: %d function(s) kept or moved verbatim", moved)
	if changed > 0 {
		summary += fmt.Sprintf("; %d changed while moving (review them): %s", changed, strings.Join(changedNames, ", "))
	}
	return summary + ".", nil
}

type functionSet struct {
	hashes map[string]bool
	names  map[string]bool
}

// functionsAfterFixup gathers the functions of an over-budget file and the new
// siblings it split into, as the round leaves them.
func functionsAfterFixup(session, scoped *ChangeSession, origin string, edits []ChangeEdit) functionSet {
	set := functionSet{hashes: map[string]bool{}, names: map[string]bool{}}
	add := func(file, content string) {
		for _, function := range syntax.Analyze(file, []byte(content)).Functions {
			set.hashes[function.Hash] = true
			set.names[shortName(function.Name)] = true
		}
	}
	if edit := proposedEdit(session, origin); edit != nil {
		content := edit.Content
		for _, written := range edits {
			if written.Path == origin {
				content = written.Content
			}
		}
		add(origin, content)
	}
	for _, written := range edits {
		if changeTarget(session, written.Path) == nil && changeTarget(scoped, written.Path) != nil && path.Dir(written.Path) == path.Dir(origin) {
			add(written.Path, written.Content)
		}
	}
	return set
}

func shortName(name string) string {
	return name[strings.LastIndex(name, ".")+1:]
}

// storeBudgetFixup merges a round into the stored task: rewritten files
// replace their proposal, new files join the task's targets.
func (s *Service) storeBudgetFixup(ctx context.Context, root string, session *ChangeSession, scopedTargets []ChangeTarget, edits []ChangeEdit, request, explanation string) (*ChangeSession, error) {
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	current, _, err := s.loadChangeForAction(ctx, session.ID, identityForChange(session))
	if err != nil {
		return nil, err
	}
	for _, target := range scopedTargets {
		if changeTarget(current, target.Path) != nil {
			continue
		}
		instructions, err := project.ResolveInstructions(root, target.Path)
		if err != nil {
			return nil, err
		}
		if current.Instructions == nil {
			current.Instructions = map[string]string{}
		}
		current.Targets = append(current.Targets, target)
		current.Instructions[target.Path] = instructions.Fingerprint
	}
	merged, err := setProposal(ctx, root, current, mergeWorkflowTests(append([]ChangeEdit(nil), current.Changes...), edits))
	if err != nil {
		return nil, err
	}
	current.Changes = merged
	current.Hash, current.Revision = changeProposalHash(current.Changes), current.Revision+1
	current.Checks, current.PinnedTests = []CheckResult{}, nil
	revokeApproval(current)
	now := time.Now().UTC()
	current.Messages = append(current.Messages, ChangeMessage{Role: "user", Content: request, CreatedAt: now}, ChangeMessage{Role: "assistant", Content: explanation, CreatedAt: now})
	current.UpdatedAt = now
	return current, storeChangeSession(root, current)
}

// noteBudgetFixup records why a fix-up round was discarded and keeps the
// proposal as it was.
func (s *Service) noteBudgetFixup(ctx context.Context, root string, session *ChangeSession, note string) (*ChangeSession, error) {
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	current, _, err := s.loadChangeForAction(ctx, session.ID, identityForChange(session))
	if err != nil {
		return nil, err
	}
	current.Messages = append(current.Messages, ChangeMessage{Role: "assistant", Content: note, CreatedAt: time.Now().UTC()})
	current.UpdatedAt = time.Now().UTC()
	return current, storeChangeSession(root, current)
}
