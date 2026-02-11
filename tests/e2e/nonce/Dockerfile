# Multi-stage build for x/nonce E2E tests and benchmarks.
# Stage 1: build gaiad + spammer binary
# Stage 2: minimal runtime with both binaries

# ---------- builder ----------
FROM golang:1.24-alpine AS builder
WORKDIR /src

ENV PACKAGES="curl build-base git bash file linux-headers eudev-dev"
RUN apk add --no-cache $PACKAGES

ARG CGO_CFLAGS="-D__BLST_PORTABLE__"
ENV CGO_CFLAGS=$CGO_CFLAGS

# wasmvm (same version as root Dockerfile)
ARG WASMVM_VERSION=v2.2.4
ADD https://github.com/CosmWasm/wasmvm/releases/download/${WASMVM_VERSION}/libwasmvm_muslc.aarch64.a /lib/libwasmvm_muslc.aarch64.a
ADD https://github.com/CosmWasm/wasmvm/releases/download/${WASMVM_VERSION}/libwasmvm_muslc.x86_64.a /lib/libwasmvm_muslc.x86_64.a
RUN cp "/lib/libwasmvm_muslc.$(uname -m).a" /lib/libwasmvm_muslc.a

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build gaiad
RUN LEDGER_ENABLED=false LINK_STATICALLY=true BUILD_TAGS=muslc make build
RUN file /src/build/gaiad | grep "statically linked"

# Build spammer
RUN CGO_ENABLED=0 go build -o /src/build/spammer ./tests/e2e/nonce/benchmark/spammer.go

# ---------- runtime ----------
FROM alpine:latest

RUN apk add --no-cache bash jq curl

COPY --from=builder /src/build/gaiad /usr/local/bin/gaiad
COPY --from=builder /src/build/spammer /usr/local/bin/spammer

# No fixed entrypoint; compose services specify their own command.
