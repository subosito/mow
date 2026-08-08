default:
    @just verify

# Commit gate — mirrors .github/workflows/ci.yml (vet + race test + build).
# Keep these steps in sync with CI so failures surface locally, not on push.
verify: vet test-race build

# Fast inner-loop tests (no race detector). Covers the root module and the
# packs/ + packs/otel + packs/mowi submodules (go.work wires them together).
test:
    go test ./...
    cd packs && go test ./...
    cd packs/otel && go test ./...
    cd packs/mowi && go test ./...

# What CI actually runs. The race detector catches unsynchronized test
# helpers that plain `go test` happily lets through.
test-race:
    go test -race ./...
    cd packs && go test -race ./...
    cd packs/otel && go test -race ./...
    cd packs/mowi && go test -race ./...

vet:
    go vet ./...
    cd packs && go vet ./...
    cd packs/otel && go vet ./...
    cd packs/mowi && go vet ./...

build: build-mow build-mowi

build-mow:
    mkdir -p bin
    go build -o bin/mow ./cmd/mow

# mowi lives in a nested module. It depends on the root module, so a root
# change can silently leave bin/mowi stale — `just build` builds both to keep
# the two binaries from drifting apart. CI builds mowi too (ci.yml), so this
# keeps `verify` mirroring CI step for step.
build-mowi:
    mkdir -p bin
    cd packs/mowi && go build -o ../../bin/mowi ./cmd/mowi

# Drive the real mowi binary in a PTY and assert on the painted grid.
#
# Deliberately NOT part of `verify`: it needs a model endpoint and a network
# round trip, and CI has neither. It catches what string-level tests cannot —
# column geometry and rendered colour. Requires bin/mowi and the devenv shell
# (shell-use is pinned in devenv.nix).
smoke-tui: build-mowi
    ./scripts/smoke-tui.sh

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
    env -u MOW_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY \
        -u MOW_MODEL -u OPENAI_MODEL -u ANTHROPIC_MODEL \
        MOW_HOME="$tmp/.mow" HOME="$tmp" \
        bash -c 'cd packs/mowi && go vet ./... && go test -race -count=1 ./...'
    go build -o bin/mow ./cmd/mow
    echo "→ verify-ci ok"
