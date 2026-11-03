package toolchain

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// GoPackageTest is the go test command for only the packages of a Go module
// that hold the given project-relative files: the module is the subproject
// at dir under root, and a package is a directory with Go files that still
// exists. It returns the command and its packages, or nil when dir is not a
// Go module or none of the files is Go code of it. The command leaves out
// the project's own test command and its flags (such as -race): it is a
// quick check, not the project's test suite.
func GoPackageTest(root, dir string, files []string) ([]string, []string) {
	module := filepath.Join(root, filepath.FromSlash(dir))
	if info, err := os.Stat(filepath.Join(module, "go.mod")); err != nil || !info.Mode().IsRegular() {
		return nil, nil
	}
	seen := map[string]bool{}
	for _, file := range files {
		pkg, ok := goPackageOf(module, dir, file)
		if ok {
			seen[pkg] = true
		}
	}
	if len(seen) == 0 {
		return nil, nil
	}
	packages := make([]string, 0, len(seen))
	for pkg := range seen {
		packages = append(packages, pkg)
	}
	sort.Strings(packages)
	return append([]string{"go", "test"}, packages...), packages
}

// goPackageOf is the package ("./cmd", ".") of a Go file of the module at
// dir, when its directory exists and is not inside a nested module.
func goPackageOf(module, dir, file string) (string, bool) {
	file = strings.TrimPrefix(filepath.ToSlash(file), "./")
	if !strings.HasSuffix(file, ".go") {
		return "", false
	}
	rel := file
	if dir != "" {
		if !strings.HasPrefix(file, dir+"/") {
			return "", false
		}
		rel = strings.TrimPrefix(file, dir+"/")
	}
	pkg := path.Dir(rel)
	for walk := pkg; walk != "." && walk != "/"; walk = path.Dir(walk) {
		if _, err := os.Stat(filepath.Join(module, filepath.FromSlash(walk), "go.mod")); err == nil {
			return "", false
		}
	}
	if info, err := os.Stat(filepath.Join(module, filepath.FromSlash(pkg))); err != nil || !info.IsDir() {
		return "", false
	}
	if pkg == "." {
		return ".", true
	}
	return "./" + pkg, true
}
