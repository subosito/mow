default:
    @just verify

# Commit gate — mirrors CI's Linux/macOS jobs on the current platform.
# Keep these steps in sync with CI so failures surface locally, not on push.
verify: vet test-race build version-check vulncheck

# Fast inner-loop tests (no race detector).
test:
    go test ./...

# What CI actually runs. The race detector catches unsynchronized test
# helpers that plain `go test` happily lets through.
test-race:
    go test -race ./...

vet:
    go vet ./...

# Known-vulnerable stdlib or module code reachable from mow (needs network
# for the vuln DB). A hit usually means bump the go directive or a module.
vulncheck:
    go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...

build: build-mow

build-mow:
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p bin
    ver="$(tr -d ' \n' < VERSION)"
    go build -buildvcs=false -trimpath \
      -ldflags "-s -w -X github.com/subosito/mow/internal/engine.Version=${ver}" \
      -o bin/mow ./cmd/mow

version-check:
    #!/usr/bin/env bash
    set -euo pipefail
    ver="$(tr -d ' \n' < VERSION)"
    ./bin/mow version | grep -F "mow $ver"


# Closest local approximation of a CI run: no developer credentials and an
# empty MOW_HOME. CI has no API key, so tests that build an Engine fail there
# while passing on a box where ~/.mow supplies one. Run this before pushing.
#
# Note this does NOT isolate the network: a test that dials a service you
# happen to run locally (e.g. an OTLP collector on :4318) still passes here
# and fails on CI. Tests must stand up their own httptest server instead of
# assuming a listener.
verify-ci:
    #!/usr/bin/env bash
    set -euo pipefail
    tmp="$(mktemp -d)"
    trap 'rm -rf "$tmp"' EXIT
    env -u MOW_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY \
        -u MOW_MODEL -u OPENAI_MODEL -u ANTHROPIC_MODEL \
        MOW_HOME="$tmp/.mow" HOME="$tmp" \
        go vet ./...
    env -u MOW_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY \
        -u MOW_MODEL -u OPENAI_MODEL -u ANTHROPIC_MODEL \
        MOW_HOME="$tmp/.mow" HOME="$tmp" \
        go test -race -count=1 ./...
    just build-mow
    just version-check
    echo "→ verify-ci ok"
