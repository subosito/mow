default:
    @just verify

verify: test vet

test:
    go test ./...

vet:
    go vet ./...

build:
    mkdir -p bin
    go build -o bin/mow ./cmd/mow
    @echo "→ bin/mow"