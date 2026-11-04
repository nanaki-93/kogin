package reach

import (
	"path"
	"strings"
)

// moduleDirs are the directories that hold a go.mod among the files; the
// project root counts as one when none does.
func moduleDirs(files []File) []string {
	var dirs []string
	for _, file := range files {
		if path.Base(file.Path) == "go.mod" {
			dirs = append(dirs, cleanDir(path.Dir(file.Path)))
		}
	}
	if len(dirs) == 0 {
		dirs = []string{""}
	}
	return dirs
}

// nearestModule is the innermost module directory that holds a file.
func nearestModule(file string, modules []string) string {
	best, found := "", false
	dir := cleanDir(path.Dir(file))
	for _, module := range modules {
		inside := module == "" || dir == module || strings.HasPrefix(dir, module+"/")
		if inside && (!found || len(module) > len(best)) {
			best, found = module, true
		}
	}
	return best
}

func cleanDir(dir string) string {
	if dir == "." || dir == "/" {
		return ""
	}
	return strings.TrimPrefix(dir, "./")
}
