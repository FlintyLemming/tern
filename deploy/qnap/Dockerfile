# The QNAP package of tern: one .qpkg per architecture, written out with
# `--output`. Build it with `make qpkg VERSION=<v>`.
#
# Stages: the web interface and the Go binary (as in deploy/Dockerfile, plus
# embedded time zone data and the package icons), the tools for the target
# architecture (restic and rclone as released; rsync and the OpenSSH client
# built from source and linked statically, see build-tools.sh), and QNAP's
# QDK packing it all. Only the tools stage runs per target architecture.
#
# Base images are pinned by digest and come from Docker Hub unless
# BASE_REGISTRY names a mirror of its official images (same digests).
ARG BASE_REGISTRY=docker.io/library

FROM --platform=$BUILDPLATFORM ${BASE_REGISTRY}/node:26-alpine@sha256:8996bb5821d62a8fd9bb27c1cb07462f3fdce6e47d640c6a881ead0b064ce216 AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM ${BASE_REGISTRY}/golang:1.27-alpine@sha256:eb61c3873c97416fff0116553fff65d67a3549f72e11d9758849a689cdd8b523 AS build
ARG GOPROXY=https://proxy.golang.org,direct
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod GOPROXY=$GOPROXY go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY web/*.go ./web/
COPY --from=web /src/web/dist ./web/dist
COPY deploy/qnap/genicons.go ./deploy/qnap/
RUN --mount=type=cache,target=/root/.cache/go-build GOPROXY=off go run deploy/qnap/genicons.go /out/icons
ARG TARGETOS TARGETARCH
ARG VERSION=dev
# timetzdata: QTS's time zone name (TZ) resolves even without zoneinfo files.
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOPROXY=off \
    go build -tags "embedweb timetzdata" -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /out/bin/tern ./cmd/tern

FROM ${BASE_REGISTRY}/alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf AS toolchain
RUN apk add --no-cache build-base linux-headers perl openssl-dev openssl-libs-static

FROM toolchain AS tools
ARG TARGETARCH
COPY deploy/fetch-tools.sh deploy/qnap/build-tools.sh /tmp/
RUN TARGETARCH=$TARGETARCH sh /tmp/fetch-tools.sh /out/bin && sh /tmp/build-tools.sh /out/bin

# QDK runs on the build machine whatever the target.
FROM --platform=$BUILDPLATFORM ${BASE_REGISTRY}/ubuntu:24.04@sha256:73b999393d07f2bf65a40373778a44dc9a6d54ad7718d29247d8ff9413c4d6a8 AS qdk
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
 && apt-get install -y --no-install-recommends build-essential bsdmainutils rsync openssl python3 git ca-certificates xz-utils \
 && rm -rf /var/lib/apt/lists/*
ARG QDK_COMMIT=b7b5f4c86ebe95b10a62d64725515e7cdcf4bb35
RUN git clone https://github.com/qnap-dev/QDK.git /opt/QDK \
 && cd /opt/QDK && git checkout "$QDK_COMMIT" \
 && make -C src && cp src/bin/qpkg_encrypt /bin/ \
 && cp -rf shared /usr/share/QDK \
 && mkdir -p /etc/config \
 && sed -e '2d' shared/qdk.conf > /etc/config/qdk.conf \
 && sed -i '2iQDK_PATH_P=/usr/share' /etc/config/qdk.conf
ENV PATH=/usr/share/QDK/bin:$PATH

FROM qdk AS package
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /work
COPY deploy/qnap/qpkg/ ./
COPY --from=build /out/icons ./icons
COPY --from=build /out/bin /stage/bin
COPY --from=tools /out/bin /stage/bin
# QDK's names for the architectures; the package version drops a leading v
# ("dev" builds become 0.0.0).
RUN set -eu; \
    case "$TARGETARCH" in amd64) qarch=x86_64 ;; arm64) qarch=arm_64 ;; *) echo "unsupported: $TARGETARCH" >&2; exit 1 ;; esac; \
    ver=${VERSION#v}; [ "$ver" = dev ] && ver=0.0.0; \
    sed -i "s/@VERSION@/$ver/" qpkg.cfg; \
    mkdir -p "$qarch"; mv /stage/bin "$qarch/bin"; \
    chmod 0755 shared/tern.sh "$qarch"/bin/*; \
    qbuild --build-arch "$qarch" --build-dir /out; \
    ls -l /out

FROM scratch
COPY --from=package /out/*.qpkg /
