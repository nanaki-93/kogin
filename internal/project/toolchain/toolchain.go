// Package toolchain finds how each subproject of a repository is tested and
// linted. A subproject is a directory with a build manifest, a task runner
// file or an AGENTS.md that declares checks. Its test command comes, in
// order, from its AGENTS.md checks block, from the test target of its
// justfile, Taskfile or Makefile, and from the defaults of its ecosystem
// (Go, npm/pnpm/yarn/bun, Cargo, pytest, Maven, Gradle, .NET), preferring a
// project's own wrapper (mvnw, gradlew) where it has one. Its lint command
// comes from its AGENTS.md checks block or else from its ecosystem (go vet,
// cargo check, a package.json lint or typecheck script), never from a task
// runner: a runner's lint or check target often runs the tests as well, or
// tools that are not installed. Commands are argument lists and never run
// through a shell.
package toolchain

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/nanaki-93/kasuri/v2/internal/project/rules"
)

// Profile is how one subproject is checked.
type Profile struct {
	// Dir is the subproject's project-relative directory; "" is the root.
	Dir string `json:"dir"`
	// Kind names the toolchain: go, node, rust, python, maven, gradle,
	// dotnet, just, task, make or agents.
	Kind string `json:"kind"`
	// Source is the file the commands came from.
	Source string   `json:"source"`
	Test   []string `json:"test,omitempty"`
	Lint   []string `json:"lint,omitempty"`
}

// Label is how checks name the profile: "" for the root, else its directory.
func (p Profile) Label() string {
	return p.Dir
}

// Covers reports whether a project-relative file belongs to this subproject.
func (p Profile) Covers(file string) bool {
	file = strings.TrimPrefix(filepath.ToSlash(file), "./")
	return p.Dir == "" || file == p.Dir || strings.HasPrefix(file, p.Dir+"/")
}

// ExecutesProjectCode reports whether a command runs the project's own code
// (its tests, scripts or build), which needs the user's execution trust.
// go vet only type-checks.
func ExecutesProjectCode(command []string) bool {
	return !(len(command) >= 2 && command[0] == "go" && command[1] == "vet")
}

// manifests mark a subproject directory.
var manifests = map[string]bool{
	"go.mod": true, "package.json": true, "Cargo.toml": true,
	"pyproject.toml": true, "setup.py": true, "setup.cfg": true, "pytest.ini": true,
	"pom.xml": true, "build.gradle": true, "build.gradle.kts": true,
	"justfile": true, "Justfile": true, "Taskfile.yml": true, "Taskfile.yaml": true, "Makefile": true,
	"AGENTS.md": true,
}

// Detect returns one profile per subproject that has a test or lint command,
// ordered by directory. files are the project's project-relative paths; root
// is where their content is read.
func Detect(root string, files []string, resolver *rules.Resolver) []Profile {
	dirs := map[string]map[string]bool{}
	for _, file := range files {
		file = filepath.ToSlash(file)
		base := path.Base(file)
		if !manifests[base] && !strings.HasSuffix(base, ".csproj") && !strings.HasSuffix(base, ".sln") {
			continue
		}
		dir := path.Dir(file)
		if dir == "." {
			dir = ""
		}
		if dirs[dir] == nil {
			dirs[dir] = map[string]bool{}
		}
		dirs[dir][base] = true
	}
	var profiles []Profile
	for dir, names := range dirs {
		if profile, ok := detectDir(root, dir, names, resolver); ok {
			profiles = append(profiles, profile)
		}
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Dir < profiles[j].Dir })
	return profiles
}

// Affected returns, for each changed file, the profile of the deepest
// subproject that covers it, in profile order and without repeats.
func Affected(profiles []Profile, changed []string) []Profile {
	picked := map[string]bool{}
	for _, file := range changed {
		best := -1
		for i, profile := range profiles {
			if profile.Covers(file) && (best < 0 || len(profile.Dir) > len(profiles[best].Dir)) {
				best = i
			}
		}
		if best >= 0 {
			picked[profiles[best].Dir] = true
		}
	}
	var affected []Profile
	for _, profile := range profiles {
		if picked[profile.Dir] {
			affected = append(affected, profile)
		}
	}
	return affected
}

func detectDir(root, dir string, names map[string]bool, resolver *rules.Resolver) (Profile, bool) {
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(dir), name))
		if err != nil || len(data) > 1<<20 {
			return ""
		}
		return string(data)
	}
	profile := ecosystemProfile(dir, names, read)
	if runner, ok := taskRunnerProfile(dir, names, read); ok {
		profile = merge(runner, profile)
	}
	if names["AGENTS.md"] && resolver != nil {
		declared := resolver.ChecksIn(dir)
		if declared.TestDeclared || declared.LintDeclared {
			agents := Profile{Dir: dir, Kind: profile.Kind, Source: path.Join(dir, "AGENTS.md"), Test: profile.Test, Lint: profile.Lint}
			if agents.Kind == "" {
				agents.Kind = "agents"
			}
			if declared.TestDeclared {
				agents.Test = declared.Test
			}
			if declared.LintDeclared {
				agents.Lint = declared.Lint
			}
			profile = agents
		}
	}
	return profile, len(profile.Test) > 0 || len(profile.Lint) > 0
}

// merge keeps the preferred profile's commands and fills a missing one from
// the fallback.
func merge(preferred, fallback Profile) Profile {
	if len(preferred.Test) == 0 {
		preferred.Test = fallback.Test
	}
	if len(preferred.Lint) == 0 {
		preferred.Lint = fallback.Lint
	}
	return preferred
}

var runnerTargetPattern = regexp.MustCompile(`^([A-Za-z0-9_.-]+)(?:\s[^:]*)?:([^=]|$)`)

func taskRunnerProfile(dir string, names map[string]bool, read func(string) string) (Profile, bool) {
	for _, runner := range []struct {
		files []string
		tool  string
	}{
		{[]string{"justfile", "Justfile"}, "just"},
		{[]string{"Taskfile.yml", "Taskfile.yaml"}, "task"},
		{[]string{"Makefile"}, "make"},
	} {
		for _, name := range runner.files {
			if !names[name] {
				continue
			}
			if !hasTestTarget(read(name), runner.tool == "task") {
				continue
			}
			return Profile{Dir: dir, Kind: runner.tool, Source: path.Join(dir, name), Test: []string{runner.tool, "test"}}, true
		}
	}
	return Profile{}, false
}

// hasTestTarget reports whether a justfile, Taskfile or Makefile has a test
// target. Its other targets are not used.
func hasTestTarget(content string, taskfile bool) bool {
	for _, raw := range strings.Split(content, "\n") {
		line := raw
		if taskfile {
			line = strings.TrimSpace(raw)
			if !strings.HasPrefix(raw, "  ") || strings.HasPrefix(raw, "    ") {
				continue
			}
		}
		if match := runnerTargetPattern.FindStringSubmatch(line); match != nil && match[1] == "test" {
			return true
		}
	}
	return false
}

func ecosystemProfile(dir string, names map[string]bool, read func(string) string) Profile {
	at := func(name string) string { return path.Join(dir, name) }
	switch {
	case names["go.mod"]:
		return Profile{Dir: dir, Kind: "go", Source: at("go.mod"), Test: []string{"go", "test", "./..."}, Lint: []string{"go", "vet", "./..."}}
	case names["package.json"]:
		return nodeProfile(dir, read)
	case names["Cargo.toml"]:
		return Profile{Dir: dir, Kind: "rust", Source: at("Cargo.toml"), Test: []string{"cargo", "test"}, Lint: []string{"cargo", "check", "--all-targets"}}
	case names["pom.xml"]:
		return Profile{Dir: dir, Kind: "maven", Source: at("pom.xml"), Test: wrapper(read, "mvnw", "mvn", "test")}
	case names["build.gradle"] || names["build.gradle.kts"]:
		return Profile{Dir: dir, Kind: "gradle", Source: at(firstOf(names, "build.gradle.kts", "build.gradle")), Test: wrapper(read, "gradlew", "gradle", "test")}
	case names["pyproject.toml"] || names["setup.py"] || names["setup.cfg"] || names["pytest.ini"]:
		return pythonProfile(dir, names, read)
	}
	for name := range names {
		if strings.HasSuffix(name, ".sln") || strings.HasSuffix(name, ".csproj") {
			return Profile{Dir: dir, Kind: "dotnet", Source: at(name), Test: []string{"dotnet", "test"}}
		}
	}
	return Profile{Dir: dir}
}

// wrapper prefers the project's own build wrapper script when it has one.
func wrapper(read func(string) string, script, tool, goal string) []string {
	if read(script) != "" {
		return []string{"./" + script, goal}
	}
	return []string{tool, goal}
}

func pythonProfile(dir string, names map[string]bool, read func(string) string) Profile {
	usesPytest := names["pytest.ini"] || strings.Contains(read("pyproject.toml"), "pytest") ||
		strings.Contains(read("setup.cfg"), "pytest") || strings.Contains(read("setup.py"), "pytest")
	if !usesPytest {
		return Profile{Dir: dir}
	}
	return Profile{Dir: dir, Kind: "python", Source: path.Join(dir, firstOf(names, "pytest.ini", "pyproject.toml", "setup.cfg", "setup.py")), Test: []string{"python", "-m", "pytest", "-q"}}
}

func firstOf(names map[string]bool, candidates ...string) string {
	for _, candidate := range candidates {
		if names[candidate] {
			return candidate
		}
	}
	return candidates[0]
}

func nodeProfile(dir string, read func(string) string) Profile {
	var manifest struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal([]byte(read("package.json")), &manifest); err != nil {
		return Profile{Dir: dir}
	}
	manager := "npm"
	for _, lock := range []struct{ file, tool string }{{"pnpm-lock.yaml", "pnpm"}, {"yarn.lock", "yarn"}, {"bun.lockb", "bun"}, {"bun.lock", "bun"}} {
		if read(lock.file) != "" {
			manager = lock.tool
			break
		}
	}
	profile := Profile{Dir: dir, Kind: "node", Source: path.Join(dir, "package.json")}
	if script := manifest.Scripts["test"]; script != "" && !strings.Contains(script, "no test specified") {
		profile.Test = nodeScript(manager, "test")
	}
	// Not a "check" script: like a runner's check target, it often runs the
	// tests as well.
	for _, lint := range []string{"lint", "typecheck"} {
		if manifest.Scripts[lint] != "" {
			profile.Lint = nodeScript(manager, lint)
			break
		}
	}
	return profile
}

func nodeScript(manager, script string) []string {
	if manager == "npm" && script == "test" {
		return []string{"npm", "test"}
	}
	if manager == "npm" || manager == "bun" {
		return []string{manager, "run", script}
	}
	return []string{manager, script}
}
