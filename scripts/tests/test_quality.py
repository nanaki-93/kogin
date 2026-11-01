import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path


QUALITY_SCRIPT = Path(__file__).resolve().parents[1] / "quality.sh"

GO_STUB = """#!/bin/sh
printf '%s\\n' "go $*" >> "$QUALITY_CALLS"
if [ "$1" = install ] && [ "$2" = honnef.co/go/tools/cmd/staticcheck@v0.8.2 ]; then
  # The desktop module's staticcheck runs from GOBIN for another GOOS.
  printf '%s\\n' '#!/bin/sh' 'printf "%s\\n" "$GOOS staticcheck $*" >> "$QUALITY_CALLS"' > "$GOBIN/staticcheck"
  chmod +x "$GOBIN/staticcheck"
  exit 0
fi
case "$2" in
  honnef.co/go/tools/cmd/staticcheck@v0.8.2|github.com/fzipp/gocyclo/cmd/gocyclo@v0.6.0)
    exit 0 ;;
  golang.org/x/tools/cmd/deadcode@v0.51.0)
    printf '%s' "$DEADCODE_STDOUT"
    printf '%s' "$DEADCODE_STDERR" >&2
    exit "$DEADCODE_STATUS" ;;
  *) exit 97 ;;
esac
"""


class QualityTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.calls = self.root / "calls.txt"
        self.write_executable(self.bin / "go", GO_STUB)
        self.write_executable(
            self.bin / "npx",
            '#!/bin/sh\nprintf "%s\\n" "npx $*" >> "$QUALITY_CALLS"\n',
        )
        # Only stubbed package runners are on PATH, so tests cannot download tools.
        for tool in ("find", "mktemp", "rm", "chmod"):
            (self.bin / tool).symlink_to(shutil.which(tool))
        for directory in ("cmd", "internal", "desktop", "scripts"):
            (self.root / directory).mkdir()
        (self.root / "cmd" / "main.go").write_text("package main\n")

    def write_executable(self, path, content):
        path.write_text(content)
        path.chmod(0o755)

    def run_quality(self, lane, stdout="", stderr="", status=0, forced_failure=""):
        self.calls.write_text("")
        result = subprocess.run(
            ["/bin/sh", str(QUALITY_SCRIPT), lane],
            cwd=self.root,
            env={
                **os.environ,
                "PATH": str(self.bin),
                "QUALITY_CALLS": str(self.calls),
                "DEADCODE_STDOUT": stdout,
                "DEADCODE_STDERR": stderr,
                "DEADCODE_STATUS": str(status),
                "KASURI_QUALITY_FAIL_STAGE": forced_failure,
            },
            capture_output=True,
            text=True,
            timeout=10,
        )
        calls = self.calls.read_text().splitlines()
        self.assertIn(
            "go run golang.org/x/tools/cmd/deadcode@v0.51.0 "
            "./cmd/daemon ./cmd/engineering-insight-eval ./cmd/agent-eval ./cmd/agent-inspect",
            calls,
        )
        self.assertIn("darwin staticcheck -tags purego,production ./...", calls)
        self.assertIn("PASS Desktop static analysis", result.stdout)
        self.assertTrue(any("gocyclo@v0.6.0" in call for call in calls))
        self.assertTrue(any("jscpd@4.0.5" in call for call in calls))
        self.assertIn("PASS Go complexity", result.stdout)
        self.assertIn("PASS Go clone detection", result.stdout)
        return result

    def test_clean_output_passes(self):
        for lane in ("--go-only", "all"):
            with self.subTest(lane=lane):
                result = self.run_quality(lane)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn("PASS Go reachability", result.stdout)
                self.assertIn("Quality passed.", result.stdout)

    def test_findings_with_successful_exit_fail(self):
        finding = "internal/example.go:12:1: unreachable func: unused\n"
        for lane in ("--go-only", "all"):
            with self.subTest(lane=lane):
                result = self.run_quality(lane, stdout=finding)
                self.assertEqual(result.returncode, 1)
                self.assertIn(finding, result.stdout)
                self.assertIn("Quality failed: Go reachability", result.stderr)
                self.assertNotIn("Reachability tool failed", result.stderr)
                self.assertNotIn("PASS Go reachability", result.stdout)

    def test_tool_failure_preserves_diagnostics(self):
        for lane in ("--go-only", "all"):
            for output in ("", "partial tool output\n"):
                with self.subTest(lane=lane, output=output):
                    result = self.run_quality(
                        lane, stdout=output, stderr="cannot load packages\n", status=2
                    )
                    self.assertEqual(result.returncode, 1)
                    self.assertIn(output, result.stdout)
                    self.assertIn("cannot load packages", result.stderr)
                    self.assertIn("Reachability tool failed (exit 2).", result.stderr)
                    self.assertIn("Quality failed: Go reachability", result.stderr)

    def test_failure_summary_keeps_other_failed_stages(self):
        result = self.run_quality(
            "--go-only",
            stdout="internal/example.go:12:1: unreachable func: unused\n",
            forced_failure="Go static analysis",
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn(
            "Quality failed: Go static analysis, Go reachability", result.stderr
        )


class ValidationTests(unittest.TestCase):
    def test_all_supported_stages_run_and_failures_are_aggregated(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            scripts = root / "scripts"
            scripts.mkdir()
            shutil.copy(QUALITY_SCRIPT.with_name("validate.sh"), scripts)
            bin_dir = root / "bin"
            bin_dir.mkdir()
            calls = root / "calls"
            stub = '#!/bin/sh\nprintf "%s %s\\n" "$0" "$*" >> "$VALIDATION_CALLS"\n'
            for path in [
                *(bin_dir / name for name in ("make", "go", "python3")),
                scripts / "quality.sh",
            ]:
                path.write_text(stub)
                path.chmod(0o755)
            (bin_dir / "dirname").symlink_to(shutil.which("dirname"))
            for failure in ("", "go-test"):
                with self.subTest(failure=failure):
                    calls.write_text("")
                    result = subprocess.run(
                        ["/bin/sh", str(scripts / "validate.sh")],
                        env={
                            **os.environ,
                            "PATH": str(bin_dir),
                            "VALIDATION_CALLS": str(calls),
                            "KASURI_VALIDATE_FAIL_STAGE": failure,
                        },
                        capture_output=True, text=True, timeout=10,
                    )
                    self.assertEqual(result.returncode, 1 if failure else 0)
                    self.assertIn("PASS go-quality", result.stdout)
                    self.assertIn("PASS daemon-contracts", result.stdout)
                    self.assertIn("PASS desktop-build", result.stdout)
                    self.assertEqual(len(calls.read_text().splitlines()), 7 if failure else 8)
                    if failure:
                        self.assertIn("Validation failed: go-test", result.stderr)
                    else:
                        self.assertIn("Validation passed.", result.stdout)


if __name__ == "__main__":
    unittest.main()
