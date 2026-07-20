# syntax=docker/dockerfile:1
#
# ios-backup-parser is a pure Go library — no production image ships. This Dockerfile
# exists only to define the pinned containers the gates run inside, so dev and CI
# compile with identical toolchains (mirrors ios-backup-crypt / quince, Go-only).
# All pins arrive as build-args from the Makefile, defaulting to versions.env.

ARG GO_IMAGE=golang:1.26.5-alpine3.24
ARG PYTHON_IMAGE=python:3.12-slim

# --- toolchain-go: the main gate container (gofmt + vet + golangci-lint + go test -race) ---
FROM ${GO_IMAGE} AS toolchain-go
ARG GOLANGCI_LINT_VERSION
# git: module fetches. gcc + musl-dev: the race detector needs cgo (CGO_ENABLED=1).
RUN apk add --no-cache git make bash gcc musl-dev
# golangci-lint built WITH this exact Go, so no analyzer/toolchain skew. The v2 module
# path carries the /v2 suffix.
RUN go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v${GOLANGCI_LINT_VERSION}
WORKDIR /src

# --- toolchain-ileapp: the differential oracle (testing ladder rung 3). iLEAPP is
#     MIT (attributed in NOTICE) and not on PyPI: clone the pinned release tag and
#     install its requirements. It runs only operator-local, against study data. ---
FROM ${PYTHON_IMAGE} AS toolchain-ileapp
ARG ILEAPP_VERSION
# build-essential: iLEAPP's requirements include source-only C extensions
# (astc_decomp_faster, pyliblzfse) with no manylinux wheels.
RUN apt-get update && apt-get install -y --no-install-recommends git build-essential \
    && rm -rf /var/lib/apt/lists/*
RUN git clone --depth 1 --branch ${ILEAPP_VERSION} https://github.com/abrignoni/iLEAPP /opt/iLEAPP \
    && pip install --no-cache-dir -r /opt/iLEAPP/requirements.txt
WORKDIR /src
