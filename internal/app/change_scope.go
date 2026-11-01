package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nanaki-93/mini-orca/v2/internal/app/changes"
	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

// A proposal stays fresh while the files it depends on are unchanged; edits
// elsewhere in the project no longer make it stale. Its freshness scope is
// the captured targets and read context (verified by content with their
// instructions), plus the files recorded here: the other Go files of each
// target package and of the local packages the targets import (their
// declarations reach the prompt), and the build manifests in the target
// directories and their ancestors. Files that .gitignore or .kasuriignore
// exclude are not part of the scope.
//
// The whole-tree fingerprint still validates every check copy and binds check
// evidence to the tree it ran on, so a change anywhere outdates the checks
// (run them again) without making the proposal stale.
const changeScopeVersion = 1

const (
	maxChangeScopeFiles    = 512
	maxChangeScopeFileSize = 64 << 20
)

// ChangeScopeFile is one recorded dependency of a proposal and its content hash.
type ChangeScopeFile struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
}

// buildManifests decide how the code around a target builds and tests.
var buildManifests = []string{
	"go.mod", "go.sum", "go.work", "go.work.sum",
	"package.json", "package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "yarn.lock", "tsconfig.json",
	"Cargo.toml", "Cargo.lock", "pyproject.toml", "setup.py", "setup.cfg", "requirements.txt", "Pipfile", "Pipfile.lock",
	"Makefile", "justfile",
}

// captureFreshnessScope records the scope of the session's current targets
// and read context. Caller has captured both.
func captureFreshnessScope(root string, session *ChangeSession) error {
	policy, err := project.NewContextPolicy(root)
	if err != nil {
		return err
	}
	scope, err := freshnessScopeFiles(root, policy, session)
	if err != nil {
		return err
	}
	session.ScopeVersion, session.Scope = changeScopeVersion, scope
	return nil
}

// verifyFreshnessScope fails with ErrRevisionConflict naming the first scope
// file that changed, appeared or disappeared since capture.
func verifyFreshnessScope(root string, policy *project.ContextPolicy, session *ChangeSession) error {
	current, err := freshnessScopeFiles(root, policy, session)
	if err != nil {
		return err
	}
	if changed := firstScopeDifference(session.Scope, current); changed != "" {
		return fmt.Errorf("%w: %s changed since this proposal was made", project.ErrRevisionConflict, changed)
	}
	return nil
}

func firstScopeDifference(recorded, current []ChangeScopeFile) string {
	for i := 0; i < len(recorded) || i < len(current); i++ {
		switch {
		case i >= len(recorded):
			return current[i].Path
		case i >= len(current):
			return recorded[i].Path
		case recorded[i] != current[i]:
			return min(recorded[i].Path, current[i].Path)
		}
	}
	return ""
}

func freshnessScopeFiles(root string, policy *project.ContextPolicy, session *ChangeSession) ([]ChangeScopeFile, error) {
	paths := freshnessScopePaths(root, policy, session)
	if len(paths) > maxChangeScopeFiles {
		return nil, fmt.Errorf("the files this task depends on exceed %d; narrow its scope", maxChangeScopeFiles)
	}
	scope := make([]ChangeScopeFile, 0, len(paths))
	for _, relative := range paths {
		hash, err := hashScopeFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", relative, err)
		}
		scope = append(scope, ChangeScopeFile{Path: relative, Hash: hash})
	}
	return scope, nil
}

// freshnessScopePaths lists existing, regular, non-ignored scope files in
// sorted order, excluding the captured targets and read context.
func freshnessScopePaths(root string, policy *project.ContextPolicy, session *ChangeSession) []string {
	captured := map[string]bool{}
	targets := capturedSources(session)
	var goSources []string
	dirs := map[string]bool{}
	for _, target := range targets {
		captured[target.Path] = true
		if strings.HasSuffix(target.Path, ".go") && target.Content != "" {
			goSources = append(goSources, target.Content)
		}
		for dir := path.Dir(target.Path); ; dir = path.Dir(dir) {
			dirs[dir] = true
			if dir == "." {
				break
			}
		}
	}
	candidates := map[string]bool{}
	for dir := range dirs {
		for _, name := range buildManifests {
			candidates[path.Join(dir, name)] = true
		}
	}
	for _, dir := range goPackageDirs(targets) {
		addGoFiles(root, dir, true, candidates)
	}
	if module := changes.ReadGoModulePath(root); module != "" {
		for _, dir := range changes.LocalImportedGoDirs(root, module, goSources) {
			addGoFiles(root, dir, false, candidates)
		}
	}
	var paths []string
	for candidate := range candidates {
		if !captured[candidate] && !policy.Ignored(candidate) && isRegularScopeFile(root, candidate) {
			paths = append(paths, candidate)
		}
	}
	slices.Sort(paths)
	return paths
}

func capturedSources(session *ChangeSession) []ChangeTarget {
	targets := slices.Clone(session.Targets)
	if session.Plan != nil {
		targets = append(targets, session.Plan.ReadTargets...)
	}
	return targets
}

func goPackageDirs(targets []ChangeTarget) []string {
	var dirs []string
	for _, target := range targets {
		if dir := path.Dir(target.Path); strings.HasSuffix(target.Path, ".go") && !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

func addGoFiles(root, dir string, tests bool, into map[string]bool) {
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(dir)))
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() && strings.HasSuffix(name, ".go") && (tests || !strings.HasSuffix(name, "_test.go")) {
			into[path.Join(dir, name)] = true
		}
	}
}

func isRegularScopeFile(root, relative string) bool {
	info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(relative)))
	return err == nil && info.Mode().IsRegular()
}

func hashScopeFile(name string) (string, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	copied, err := io.Copy(hash, io.LimitReader(file, maxChangeScopeFileSize+1))
	if err != nil {
		return "", err
	}
	if copied > maxChangeScopeFileSize {
		return "", fmt.Errorf("file exceeds %d bytes", maxChangeScopeFileSize)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func validateStoredScope(session *ChangeSession) error {
	if session.ScopeVersion != 0 && session.ScopeVersion != changeScopeVersion || len(session.Scope) > maxChangeScopeFiles {
		return fmt.Errorf("corrupt freshness scope")
	}
	if session.ScopeVersion == 0 && len(session.Scope) > 0 {
		return fmt.Errorf("corrupt freshness scope")
	}
	for i, file := range session.Scope {
		if file.Path == "" || file.Hash == "" || i > 0 && session.Scope[i-1].Path >= file.Path || !filepath.IsLocal(filepath.FromSlash(file.Path)) {
			return fmt.Errorf("corrupt freshness scope")
		}
	}
	return nil
}

// verifyChecksWorkspace binds check evidence to the tree the checks ran on:
// any project change since then means the checks must run again. Sessions
// captured before path-scoped freshness verify the whole tree on every action.
func verifyChecksWorkspace(ctx context.Context, root string, session *ChangeSession) error {
	if session.ScopeVersion == 0 {
		return nil
	}
	current, err := workspaceFingerprint(ctx, root)
	if err != nil {
		return err
	}
	if session.ChecksWorkspaceHash == "" || current != session.ChecksWorkspaceHash {
		return errChecksOutdated
	}
	return nil
}

var errChecksOutdated = fmt.Errorf("%w: the project changed since checks ran; run checks again", project.ErrRevisionConflict)

// staleReason turns a freshness failure into the sentence shown to the user.
func staleReason(err error) string {
	message, found := strings.CutPrefix(err.Error(), project.ErrRevisionConflict.Error()+": ")
	if !found {
		return ""
	}
	return message
}

// listedFreshness checks each listed draft by its captured sources and scope
// with one context policy; the whole tree is never walked for a listing.
func (s *Service) listedFreshness(root string) func(*ChangeSession) string {
	index, indexErr := s.manager.Index()
	policy, policyErr := project.NewContextPolicy(root)
	return func(session *ChangeSession) string {
		switch {
		case indexErr != nil || policyErr != nil:
			return session.Freshness
		case index.ProjectID != session.ProjectID || policy.Version() != session.PolicyFingerprint:
			return "stale"
		case session.ScopeVersion == 0:
			if index.ProjectRevision != session.ProjectRevision {
				return "stale"
			}
			return session.Freshness
		case verifyCapturedSources(root, policy, session) != nil:
			return "stale"
		default:
			return "current"
		}
	}
}

// staleWorkflowReason explains a workflow ended or shown as stale.
func staleWorkflowReason(detail string) string {
	if detail == "" {
		return "Project source or instructions changed. Re-analyze the task against the current files."
	}
	return "The task is out of date: " + detail + ". Re-analyze it against the current files."
}
