default:
    @just verify

# Commit gate — mirrors .github/workflows/ci.yml (vet + race test + build).
# Keep these steps in sync with CI so failures surface locally, not on push.
verify: vet test-race build

# Fast inner-loop tests (no race detector). Covers the root module and the
# packs/ + packs/otel submodules.
test:
    go test ./...
    cd packs && go test ./...
    cd packs/otel && go test ./...

# What CI actually runs. The race detector catches unsynchronized test
# helpers that plain `go test` happily lets through.
test-race:
    go test -race ./...
    cd packs && go test -race ./...
    cd packs/otel && go test -race ./...

vet:
    go vet ./...
    cd packs && go vet ./...
    cd packs/otel && go vet ./...

build: build-mow

build-mow:
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p bin
    ver="$(tr -d ' \n' < VERSION)"
    go build -trimpath \
      -ldflags "-s -w -X github.com/subosito/mow/internal/engine.Version=${ver}" \
      -o bin/mow ./cmd/mow


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
    env -u MOW_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY \
        -u MOW_MODEL -u OPENAI_MODEL -u ANTHROPIC_MODEL \
        MOW_HOME="$tmp/.mow" HOME="$tmp" \
        bash -c 'cd packs && go vet ./... && go test -race -count=1 ./...'
    env -u MOW_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY \
        -u MOW_MODEL -u OPENAI_MODEL -u ANTHROPIC_MODEL \
        MOW_HOME="$tmp/.mow" HOME="$tmp" \
        bash -c 'cd packs/otel && go vet ./... && go test -race -count=1 ./...'
    just build-mow
    echo "→ verify-ci ok"
