package app

import (
	"bufio"
	"context"
	"go/build/constraint"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/app/gitops"
	"github.com/nanaki-93/kogin/v2/internal/project"
)

// Why the profile leaves an issue out of the work. Out-of-scope issues stay
// listed, but are not counted, not in the inbox and not fixed together.
const (
	// ScopeTest: an AI finding in a test file; tests are judged by running.
	ScopeTest = "test"
	// ScopePlatform: the file only builds on platforms the project skips.
	ScopePlatform = "platform"
	// ScopeUnchanged: first reported on code that had already been analyzed
	// and has not changed since; a second look, not a new problem.
	ScopeUnchanged = "unchanged"
	// ScopeBelowFinishLine: less severe than the profile's finish line.
	ScopeBelowFinishLine = "below_finish_line"
)

// findingScoper decides, at listing, which issues the profile counts.
type findingScoper struct {
	root    string
	profile ProjectProfile
	// analyzedBefore is when each file was last analyzed at an earlier
	// version, from the issues reported on that version.
	analyzedBefore map[string]time.Time
	lines          *lineAges
}

// newFindingScoper reads when each file was analyzed before from all
// findings ever recorded; current maps each indexed file to its hash.
func newFindingScoper(root string, profile ProjectProfile, all []project.UnifiedFinding, current map[string]string, lines *lineAges) *findingScoper {
	before := map[string]time.Time{}
	for _, finding := range all {
		file := finding.Location.Path
		if file == "" || finding.FileHash == "" || finding.FileHash == current[file] {
			continue
		}
		if finding.DetectedAt.After(before[file]) {
			before[file] = finding.DetectedAt
		}
	}
	return &findingScoper{root: root, profile: profile, analyzedBefore: before, lines: lines}
}

// scope says why the profile leaves finding out of the work; nil when it
// counts. Failing tests always count.
func (s *findingScoper) scope(finding project.UnifiedFinding) *project.FindingScope {
	file := finding.Location.Path
	if finding.Source == project.FindingSourceTest {
		return nil
	}
	if file != "" && !s.platformInScope(file) {
		return &project.FindingScope{Reason: ScopePlatform, Detail: "This file only builds for platforms the project profile leaves out."}
	}
	if finding.Source == project.FindingSourceAI && finding.Test && !s.profile.AnalyzeTests {
		return &project.FindingScope{Reason: ScopeTest, Detail: "Tests are judged by running them; turn on test file analysis in the project profile to count review notes on them."}
	}
	if s.unchangedSinceAnalyzed(finding) {
		return &project.FindingScope{Reason: ScopeUnchanged, Detail: "First reported on code that was analyzed before and has not changed since: a second look at the same code, not a problem a change introduced."}
	}
	if !s.profile.countsAsWork(finding.Severity) {
		return &project.FindingScope{Reason: ScopeBelowFinishLine, Detail: "Less severe than the project's finish line (" + s.profile.FinishLine + ")."}
	}
	return nil
}

// unchangedSinceAnalyzed reports an open AI finding below high severity
// whose lines are all older than the file's previous analysis, which did not
// report it. Every new analysis of unchanged code finds something new to say;
// only problems in changed code are new problems.
func (s *findingScoper) unchangedSinceAnalyzed(finding project.UnifiedFinding) bool {
	file := finding.Location.Path
	before, ok := s.analyzedBefore[file]
	if !ok || finding.Source != project.FindingSourceAI || finding.Status != project.FindingStatusOpen ||
		severityRank(finding.Severity) <= severityRank("high") || finding.Location.StartLine <= 0 || !finding.DetectedAt.After(before) {
		return false
	}
	end := max(finding.Location.EndLine, finding.Location.StartLine)
	ages := s.lines.of(s.root, file, finding.FileHash)
	if len(ages) < end {
		return false
	}
	for line := finding.Location.StartLine; line <= end; line++ {
		if !ages[line-1].Before(before) {
			return false
		}
	}
	return true
}

// platformInScope reports whether a file builds for at least one of the
// profile's platforms. Only Go files say so (file name and build
// constraint); other files are always in scope.
func (s *findingScoper) platformInScope(file string) bool {
	if len(s.profile.Platforms) == 0 || path.Ext(file) != ".go" {
		return true
	}
	expr := goBuildConstraint(filepath.Join(s.root, filepath.FromSlash(file)))
	for _, platform := range s.profile.Platforms {
		goos := map[string]string{"macos": "darwin", "linux": "linux", "windows": "windows"}[platform]
		if goFileBuildsFor(path.Base(file), expr, goos) {
			return true
		}
	}
	return false
}

// goFileBuildsFor reports whether a Go file with this name and build
// constraint builds for goos on a common architecture, with cgo.
func goFileBuildsFor(name string, expr constraint.Expr, goos string) bool {
	stem := strings.TrimSuffix(strings.TrimSuffix(name, ".go"), "_test")
	parts := strings.Split(stem, "_")
	known := map[string]bool{"darwin": true, "linux": true, "windows": true, "freebsd": true, "openbsd": true, "netbsd": true, "android": true, "ios": true, "js": true, "wasip1": true, "plan9": true, "solaris": true, "aix": true, "dragonfly": true, "illumos": true}
	arches := map[string]bool{"amd64": true, "arm64": true, "386": true, "arm": true, "wasm": true, "riscv64": true, "ppc64le": true, "s390x": true, "loong64": true, "mips": true, "mips64": true}
	if n := len(parts); n > 1 {
		last := parts[n-1]
		if arches[last] && n > 2 && known[parts[n-2]] {
			last = parts[n-2]
		}
		if known[last] && last != goos {
			return false
		}
	}
	if expr == nil {
		return true
	}
	return expr.Eval(func(tag string) bool {
		switch tag {
		case goos, "cgo", "gc", "amd64", "arm64":
			return true
		case "unix":
			return goos != "windows"
		}
		return strings.HasPrefix(tag, "go1.")
	})
}

// goBuildConstraint reads the //go:build line of a Go file's header; nil
// when it has none or cannot be read.
func goBuildConstraint(full string) constraint.Expr {
	file, err := os.Open(full)
	if err != nil {
		return nil
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "package ") {
			return nil
		}
		if constraint.IsGoBuild(line) {
			expr, err := constraint.Parse(line)
			if err != nil {
				return nil
			}
			return expr
		}
	}
	return nil
}

// lineAges caches, per file version, when each line was last committed
// (git blame); lines not yet committed count as changed after any analysis.
type lineAges struct {
	mu    sync.Mutex
	files map[string][]time.Time
}

func newLineAges() *lineAges { return &lineAges{files: map[string][]time.Time{}} }

// findingLineAges is shared by every listing; entries are per file version.
var findingLineAges = newLineAges()

func (a *lineAges) of(root, file, hash string) []time.Time {
	key := root + "\x00" + file + "\x00" + hash
	a.mu.Lock()
	defer a.mu.Unlock()
	if ages, ok := a.files[key]; ok {
		return ages
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := gitops.Command(ctx, root, "", nil, "blame", "--porcelain", "--", file)
	var ages []time.Time
	if err == nil {
		ages = parseBlameAges(out)
	}
	if len(a.files) > 4096 {
		clear(a.files)
	}
	a.files[key] = ages
	return ages
}

// uncommitted dates lines Git has not committed: after every analysis.
var uncommitted = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)

// parseBlameAges turns `git blame --porcelain` output into each line's
// commit time; lines of the uncommitted placeholder commit are uncommitted.
func parseBlameAges(out string) []time.Time {
	committed := map[string]time.Time{}
	var ages []time.Time
	commit := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "\t") {
			ages = append(ages, committed[commit])
			continue
		}
		fields := strings.Fields(line)
		switch {
		case len(fields) >= 3 && len(fields[0]) == 40:
			commit = fields[0]
			if strings.Trim(commit, "0") == "" {
				committed[commit] = uncommitted
			}
		case len(fields) == 2 && fields[0] == "committer-time":
			if seconds, err := strconv.ParseInt(fields[1], 10, 64); err == nil && strings.Trim(commit, "0") != "" {
				committed[commit] = time.Unix(seconds, 0)
			}
		}
	}
	return ages
}
