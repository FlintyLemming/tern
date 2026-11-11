# The tern image: web interface → Go binary → Alpine runtime. The first two
# stages run on the build machine and cross-compile; only the last one is
# per target architecture. Build it with `make image VERSION=<v>`.
#
# Base images are pinned by digest. They come from Docker Hub unless
# BASE_REGISTRY names a mirror of its official images (same digests), e.g.
# public.ecr.aws/docker/library.
ARG BASE_REGISTRY=docker.io/library

FROM --platform=$BUILDPLATFORM ${BASE_REGISTRY}/node:26-alpine@sha256:8996bb5821d62a8fd9bb27c1cb07462f3fdce6e47d640c6a881ead0b064ce216 AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM ${BASE_REGISTRY}/golang:1.27-alpine@sha256:eb61c3873c97416fff0116553fff65d67a3549f72e11d9758849a689cdd8b523 AS build
# GOPROXY can name a closer module mirror; go.sum still pins every module.
ARG GOPROXY=https://proxy.golang.org,direct
WORKDIR /src
COPY go.mod go.sum ./
# Shared by both target architectures: declared before TARGETARCH.
RUN --mount=type=cache,target=/go/pkg/mod GOPROXY=$GOPROXY go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY web/*.go ./web/
COPY --from=web /src/web/dist ./web/dist
ARG TARGETOS TARGETARCH
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOPROXY=off \
    go build -tags embedweb -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /out/tern ./cmd/tern

FROM ${BASE_REGISTRY}/alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
ARG TARGETARCH
RUN apk add --no-cache rsync openssh-client-default su-exec tzdata ca-certificates
COPY deploy/fetch-tools.sh /tmp/fetch-tools.sh
RUN TARGETARCH=$TARGETARCH sh /tmp/fetch-tools.sh /usr/local/bin && rm /tmp/fetch-tools.sh
COPY --from=build /out/tern /usr/local/bin/tern
COPY --chmod=0755 deploy/entrypoint.sh /entrypoint.sh

ENV TERN_DATA_DIR=/data TERN_SECRETS_DIR=/secrets TERN_RUNTIME_DIR=/dev/shm/tern
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --start-interval=2s CMD ["tern", "--healthcheck"]
ENTRYPOINT ["/entrypoint.sh"]
