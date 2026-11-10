VERSION ?= dev

.PHONY: test vet build

test:
	go test ./...

vet:
	go vet ./...

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/tern ./cmd/tern
