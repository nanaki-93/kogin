#!/usr/bin/env sh

set -u

staticcheck_version=v0.8.2
tools_version=v0.51.0
gocyclo_version=v0.6.0
jscpd_version=4.0.5
forced_failure=${KASURI_QUALITY_FAIL_STAGE:-}
failed_stages=""

run_stage() {
  stage=$1
  shift

  printf '\n==> %s\n' "$stage"
  if [ "$forced_failure" = "$stage" ]; then
    printf 'FAIL %s (forced diagnostic failure)\n' "$stage" >&2
    failed_stages="${failed_stages}${failed_stages:+, }${stage}"
    return
  fi

  if "$@"; then
    printf 'PASS %s\n' "$stage"
    return
  fi

  printf 'FAIL %s\n' "$stage" >&2
  failed_stages="${failed_stages}${failed_stages:+, }${stage}"
}

run_go_reachability() {
  # Keep daemon, evaluation, worker, inspector and terminal UI roots.
  # deadcode reports findings on stdout but exits successfully when it finds any.
  reachability_output=$(go run golang.org/x/tools/cmd/deadcode@"$tools_version" ./cmd/daemon ./cmd/engineering-insight-eval ./cmd/agent-eval ./cmd/agent-inspect)
  reachability_status=$?
  if [ -n "$reachability_output" ]; then
    printf '%s\n' "$reachability_output"
  fi
  if [ "$reachability_status" -ne 0 ]; then
    printf 'Reachability tool failed (exit %s).\n' "$reachability_status" >&2
    return "$reachability_status"
  fi
  [ -z "$reachability_output" ]
}

run_desktop_static_analysis() {
  # The desktop app is its own module (desktop/go.mod). Its macOS build is
  # checked without cgo, so no Xcode or GTK is needed here.
  tool_dir=$(mktemp -d) || return 1
  GOBIN="$tool_dir" go install honnef.co/go/tools/cmd/staticcheck@"$staticcheck_version" || { rm -rf "$tool_dir"; return 1; }
  (cd desktop && CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 "$tool_dir/staticcheck" -tags purego,production ./...)
  desktop_status=$?
  rm -rf "$tool_dir"
  return "$desktop_status"
}

run_go_quality() {
  # go run and npx use their user-level module/package caches. No quality tool is
  # added to go.mod, the daemon image, or this worktree.
  run_stage "Go static analysis" go run honnef.co/go/tools/cmd/staticcheck@"$staticcheck_version" ./...
  run_stage "Desktop static analysis" run_desktop_static_analysis
  run_stage "Go reachability" run_go_reachability
  run_stage "Go complexity" find cmd internal desktop -name '*.go' -type f ! -name '*_test.go' -exec go run github.com/fzipp/gocyclo/cmd/gocyclo@"$gocyclo_version" -over 15 '{}' +
  export npm_config_cache=".npm_local"
  run_stage "Go clone detection" npx --yes jscpd@"$jscpd_version" \
    --min-tokens 70 \
    --min-lines 8 \
    --threshold 1 \
    --format go \
    --ignore '**/*_test.go,**/testdata/**,build/**' \
    internal cmd desktop
  run_stage "Web UI clone detection" npx --yes jscpd@"$jscpd_version" \
    --min-tokens 70 \
    --min-lines 8 \
    --threshold 1 \
    --format typescript,tsx \
    --ignore '**/*.test.ts,**/*.test.tsx,**/node_modules/**' \
    web/src
}

case "${1:-all}" in
  all|--go-only)
    run_go_quality
    ;;
  --help|-h)
    printf '%s\n' "Usage: $0 [--go-only]"
    printf '%s\n' "Set KASURI_QUALITY_FAIL_STAGE to a stage name to exercise aggregate failure reporting."
    exit 0
    ;;
  *)
    printf 'Unknown quality lane: %s\n' "$1" >&2
    exit 2
    ;;
esac

if [ -n "$failed_stages" ]; then
  printf '\nQuality failed: %s\n' "$failed_stages" >&2
  exit 1
fi

printf '\nQuality passed.\n'
