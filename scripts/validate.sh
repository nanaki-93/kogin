#!/usr/bin/env sh

# Runs every supported local gate and reports every failure before returning.
set -u

script_dir=$(CDPATH= cd "$(dirname "$0")" && pwd)
repo_root=$(CDPATH= cd "$script_dir/.." && pwd)
cd "$repo_root"

forced_failure=${KOGIN_VALIDATE_FAIL_STAGE:-}
failed_stages=""

case "${1:-}" in
  "")
    ;;
  --help|-h)
    printf '%s\n' "Usage: $0"
    printf '%s\n' "Set KOGIN_VALIDATE_FAIL_STAGE to a stage name to exercise aggregate failure reporting."
    exit 0
    ;;
  *)
    printf 'Unknown validation argument: %s\n' "$1" >&2
    exit 2
    ;;
esac

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

run_stage go-format make fmt-check
run_stage go-test make test
run_stage go-race make test-race
run_stage go-vet make vet
run_stage desktop-build make desktop-check
run_stage daemon-contracts go test ./cmd/daemon -run 'Test(ReleaseDocumentationUsesCanonicalVersion|DocumentedRoutesAreHandledByDaemon)$'
run_stage agent-dispatcher python3 -m unittest discover -s scripts/tests -p 'test_*.py'
run_stage go-quality ./scripts/quality.sh --go-only
if command -v npm >/dev/null 2>&1; then
  run_stage webui-test make webui-test
  run_stage webui-e2e make webui-e2e
  run_stage webui-dist make webui-dist-check
else
  printf '\n==> webui-test\nSKIP webui-test, webui-e2e and webui-dist (npm is not installed)\n'
fi

if [ -n "$failed_stages" ]; then
  printf '\nValidation failed: %s\n' "$failed_stages" >&2
  exit 1
fi

printf '\nValidation passed.\n'
