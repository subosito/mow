default:
    @just verify

# Commit gate — mirrors .github/workflows/ci.yml (vet + race test + build).
# Keep these steps in sync with CI so failures surface locally, not on push.
verify: vet test-race build

# Fast inner-loop tests (no race detector).
test:
    go test ./...

# What CI actually runs. The race detector catches unsynchronized test
# helpers that plain `go test` happily lets through.
test-race:
    go test -race ./...

vet:
    go vet ./...

build:
    mkdir -p bin
    go build -o bin/mow ./cmd/mow
    @echo "→ bin/mow"
