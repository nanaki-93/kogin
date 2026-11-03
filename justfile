# Kogin standardized repository entrypoint

set shell := ["sh", "-cu"]

default:
    @just --list

# Install Go dependencies and configure local git hooks
setup:
    go mod download
    @if command -v lefthook >/dev/null 2>&1; then lefthook install; fi

# Run the local development daemon on :9090
dev:
    make dev

# Open the Web UI served by the running daemon
ui:
    #!/usr/bin/env sh
    url="http://127.0.0.1:9090/ui"
    if command -v open >/dev/null 2>&1; then open "$url"; elif command -v xdg-open >/dev/null 2>&1; then xdg-open "$url"; else echo "$url"; fi

# Serve the Web UI with hot reload on :5173 (proxying the daemon on :9090)
ui-dev:
    make webui-dev

# Run automated unit and race tests
test:
    go test -race ./...

# Run a single Go test by regex pattern (e.g. just test-one TestChangeWorkflow)
test-one pattern:
    go test ./... -run "{{pattern}}" -v

# Run formatting, static analysis, complexity, clone, and test gates
check:
    make fmt-check
    make vet
    ./scripts/quality.sh --go-only
    make check

# Build kogin and kogin-daemon and install them (see make install); then run kogin in a project folder
build:
    make install

# Build kogin and kogin-daemon into ~/DEV/lab/kogin and KOGIN_HOME (or ~/DEV/programs/kogin)
install:
    make install

# Build Kogin.app (macOS) and copy it into /Applications (or ~/Applications)
desktop:
    make install-desktop
