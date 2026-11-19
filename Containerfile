# syntax=docker/dockerfile:1
# easyworker: single static Go binary, Connect RPC (worker.v1.WorkerService).
#
# TWO STAGES, AND THERE USED TO BE THREE. The one that went built a Svelte control panel from a
# separate repository and embedded it so this service could serve a UI. That UI is gone: the browser
# belongs to easyvm, and this binary is the Connect RPC and nothing else. Removing the stage removed
# a node toolchain, an `npm ci` and a cross-repository build dependency from every image build.
# Official images by default; a caller can point REGISTRY at a mirror.
ARG REGISTRY=docker.io/library

# ---- build (go) ----
FROM ${REGISTRY}/golang:1.26-alpine AS build
# CGO_ENABLED=0 is the whole point: a static binary runs on glibc, musl, macOS,
# Windows and Android's bionic alike. A dynamically linked build silently works
# on the builder and fails with ENOENT (missing ELF interpreter) everywhere else.
ENV GOPROXY=https://proxy.golang.org \
    CGO_ENABLED=0 \
    GOWORK=off
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
RUN go build -trimpath -ldflags "-s -w" -o /out/easyworker ./cmd/easyworker

# ---- runtime ----
FROM ${REGISTRY}/alpine:3.24
RUN apk add --no-cache ca-certificates curl
COPY --from=build /out/easyworker /usr/local/bin/easyworker
RUN mkdir -p /root/workspace /data
# 48080 SITS BESIDE easystream's 48079, and the pair is the contract between the two daemons and
# easyvm. 8080 is what a container usually gets and it collides with easyvm's own daemon.
ENV WORKER_PORT=48080 \
    WORKER_DB=/data/jobs.db
EXPOSE 48080
ENTRYPOINT ["/usr/local/bin/easyworker"]
