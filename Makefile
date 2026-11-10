VERSION ?= dev

.PHONY: test vet build itest

test:
	go test ./...

vet:
	go vet ./...

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/tern ./cmd/tern

# Integration tests: real sshd, restic, rclone and rsync in a compose
# environment. ./internal/... is included so Linux-only tests run too.
itest:
	docker compose -f test/integration/compose.yaml run --rm --build runner \
	  go test -tags integration -count=1 ./internal/... ./test/integration/...
