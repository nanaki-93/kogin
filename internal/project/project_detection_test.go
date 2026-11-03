package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectProjectRecognizesSupportedBuildFiles(t *testing.T) {
	for _, test := range []struct {
		name      string
		files     map[string]string
		wantType  string
		wantBuild string
	}{
		{name: "Go", files: map[string]string{"go.mod": "module fixture\n"}, wantType: "go", wantBuild: "go.mod"},
		{name: "Kotlin Gradle", files: map[string]string{"build.gradle.kts": "plugins { kotlin(\"jvm\") }\n"}, wantType: "kotlin", wantBuild: "build.gradle.kts"},
		{name: "Java Gradle", files: map[string]string{"build.gradle": "plugins { id 'java' }\n"}, wantType: "java", wantBuild: "build.gradle"},
		{name: "Rust", files: map[string]string{"Cargo.toml": "[package]\n"}, wantType: "rust", wantBuild: "Cargo.toml"},
		{name: "TypeScript", files: map[string]string{"package.json": "{}\n"}, wantType: "typescript", wantBuild: "package.json"},
		{name: "Python PyProject", files: map[string]string{"pyproject.toml": "[project]\n"}, wantType: "python", wantBuild: "pyproject.toml"},
		{name: "Python Requirements", files: map[string]string{"requirements.txt": "pytest\n"}, wantType: "python", wantBuild: "requirements.txt"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			for path, content := range test.files {
				writeDetectionFixture(t, root, path, content)
			}
			got := detectProject(root)
			if got.Type != test.wantType || got.BuildFile != test.wantBuild {
				t.Fatalf("detectProject() = %+v, want type=%q build=%q", got, test.wantType, test.wantBuild)
			}
		})
	}
}

func TestDetectProjectUsesDeterministicRootPrecedence(t *testing.T) {
	root := t.TempDir()
	writeDetectionFixture(t, root, "go.mod", "module fixture\n")
	writeDetectionFixture(t, root, "build.gradle", "plugins { id 'java' }\n")
	writeDetectionFixture(t, root, "build.gradle.kts", "plugins { kotlin(\"jvm\") }\n")
	writeDetectionFixture(t, root, "pyproject.toml", "[project]\n")
	if got := detectProject(root); got.Type != "go" || got.BuildFile != "go.mod" {
		t.Fatalf("detectProject() = %+v, want Go precedence", got)
	}

	if err := os.Remove(filepath.Join(root, "go.mod")); err != nil {
		t.Fatal(err)
	}
	if got := detectProject(root); got.Type != "kotlin" || got.BuildFile != "build.gradle.kts" {
		t.Fatalf("detectProject() = %+v, want Kotlin Gradle precedence", got)
	}
}

func TestDetectProjectIgnoresNestedBuildFilesAndUnknownRoots(t *testing.T) {
	root := t.TempDir()
	writeDetectionFixture(t, root, filepath.Join("node_modules", "dependency", "go.mod"), "module dependency\n")
	writeDetectionFixture(t, root, filepath.Join(".kogin", "Cargo.toml"), "[package]\n")
	if got := detectProject(root); got.Type != "unknown" || got.BuildFile != "" {
		t.Fatalf("detectProject() = %+v, want unknown root", got)
	}
}

func writeDetectionFixture(t *testing.T, root, path, content string) {
	t.Helper()
	fullPath := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
