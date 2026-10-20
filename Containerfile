# syntax=docker/dockerfile:1
# easyworker: single static Go binary, Connect RPC (worker.v1.WorkerService).
#
# Three stages:
#   1. webui — build the control-panel SPA from the easyworker-webui repo
#      (dist is NOT committed in this repo anymore).
#   2. build — compile the Go binary, embedding the SPA from stage 1.
#   3. runtime — a minimal alpine (ca-certificates + curl only).
# Official images by default; a caller can point REGISTRY at a mirror.
ARG REGISTRY=docker.io/library

# ---- webui (node) ----
FROM ${REGISTRY}/node:22-alpine AS webui
ARG EASYWORKER_WEBUI_REPO=https://github.com/easydev-org/easyworker-webui.git
ARG EASYWORKER_WEBUI_REF=main
WORKDIR /webui
RUN apk add --no-cache git ca-certificates \
    && git clone --depth 1 --branch "${EASYWORKER_WEBUI_REF}" "${EASYWORKER_WEBUI_REPO}" . \
    && npm ci --no-audit --no-fund \
    && OUT_DIR=dist npm run build

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
# The SPA is built in the webui stage; place it at the go:embed path AFTER
# copying the tree (the repo's internal/webui/dist/ is just a placeholder), so
# //go:embed finds the real build.
COPY --from=webui /webui/dist/ internal/webui/dist/
RUN go build -trimpath -ldflags "-s -w" -o /out/easyworker ./cmd/easyworker

# ---- runtime ----
FROM ${REGISTRY}/alpine:3.24
RUN apk add --no-cache ca-certificates curl
COPY --from=build /out/easyworker /usr/local/bin/easyworker
RUN mkdir -p /root/workspace /data
ENV WORKER_PORT=8080 \
    WORKER_DB=/data/jobs.db
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/easyworker"]
