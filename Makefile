VERSION ?= dev
# Base images come from Docker Hub unless TERN_BASE_REGISTRY names a mirror
# of its official images, e.g. public.ecr.aws/docker/library.
BASE_REGISTRY ?= $(or $(TERN_BASE_REGISTRY),docker.io/library)
# Go modules come from proxy.golang.org unless TERN_GOPROXY names another
# module proxy; go.sum pins every module either way.
GOPROXY_IMAGE ?= $(or $(TERN_GOPROXY),https://proxy.golang.org$(comma)direct)
comma := ,
PLATFORMS ?= linux/amd64,linux/arm64

.PHONY: test vet web build itest acceptance image image-test

test:
	go test ./...

vet:
	go vet ./...

# The web interface; build embeds it into the binary.
web:
	npm --prefix web ci
	npm --prefix web run build

build: web
	CGO_ENABLED=0 go build -tags embedweb -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/tern ./cmd/tern

# Integration tests: real sshd, restic, rclone and rsync in a compose
# environment. ./internal/... is included so Linux-only tests run too.
itest:
	docker compose -f test/integration/compose.yaml run --rm --build runner \
	  go test -tags integration -count=1 ./internal/... ./test/integration/...

# Acceptance tests on a bad network (packet loss, outages), in the same
# compose environment. They take about an hour.
acceptance:
	docker compose -f test/integration/compose.yaml run --rm --build runner \
	  go test -tags acceptance -count=1 -timeout 60m ./test/acceptance/...

# The production image for both architectures, loaded locally as tern:$(VERSION).
# Nothing is pushed anywhere.
image:
	docker buildx build --platform $(PLATFORMS) -f deploy/Dockerfile \
	  --build-arg VERSION=$(VERSION) --build-arg BASE_REGISTRY=$(BASE_REGISTRY) \
	  --build-arg GOPROXY=$(GOPROXY_IMAGE) \
	  -t tern:$(VERSION) .

# Tests the image from the outside with the docker command line. Both
# architectures are built: one test runs the tools of each.
image-test:
	$(MAKE) image VERSION=image-test
	TERN_IMAGE=tern:image-test go test -tags image -count=1 -timeout 30m ./test/image/...

