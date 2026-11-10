# Test runner: Go toolchain plus the exact tools tern drives.
ARG BASE_REGISTRY=docker.io/library
FROM ${BASE_REGISTRY}/golang:1.27-alpine

ARG TARGETARCH
RUN apk add --no-cache rsync openssh-client-default iproute2

COPY deploy/fetch-tools.sh /tmp/fetch-tools.sh
RUN TARGETARCH=$TARGETARCH sh /tmp/fetch-tools.sh /usr/local/bin && rm /tmp/fetch-tools.sh

ENV GOFLAGS=-buildvcs=false
WORKDIR /src
