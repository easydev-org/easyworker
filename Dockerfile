# syntax=docker/dockerfile:1
# agent-worker: single static Go binary, Connect RPC (worker.v1.WorkerService).
# Built via the cluster buildkitd (./build-image.sh), pushed to forgejo.
#
# The runtime image deliberately ships NO shell: agent-worker brings its own
# (mvdan.cc/sh interp) — alpine is here only for ca-certificates + curl
# (in-cluster verification). The same binary is injected into arbitrary
# sandbox base images, including scratch/distroless.
ARG REGISTRY=git.agent.svc.cluster.local/root

FROM ${REGISTRY}/golang:1.26-alpine AS build
ARG HTTP_PROXY
ARG HTTPS_PROXY
ENV HTTP_PROXY=${HTTP_PROXY} \
    HTTPS_PROXY=${HTTPS_PROXY} \
    NO_PROXY=localhost,127.0.0.1,.svc.cluster.local,.svc,.fenjin.org,10.199.64.20 \
    GOPROXY=https://proxy.golang.org \
    CGO_ENABLED=0 \
    GOWORK=off
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
# xa11y assets are FETCHED from the artifact store (not committed). Fetch the
# target's platform before building, so //go:embed finds it.
ARG ARTIFACT=http://artifact.worker.svc.cluster.local
COPY scripts/fetch-xa11y.sh scripts/fetch-xa11y.sh
RUN ARTIFACT=${ARTIFACT} sh scripts/fetch-xa11y.sh
# internal/webui/dist (the built SPA, embedded via go:embed) is COMMITTED, so a
# plain source build needs no Node toolchain. Regenerate it with
# `npm --prefix webui ci && npm --prefix webui run build`.
COPY . ./
RUN go build -trimpath -ldflags "-s -w" -o /out/agent-worker ./cmd/agent-worker

FROM ${REGISTRY}/alpine:3.24
ARG HTTP_PROXY
ARG HTTPS_PROXY
ENV HTTP_PROXY=${HTTP_PROXY} \
    HTTPS_PROXY=${HTTPS_PROXY} \
    http_proxy=${HTTP_PROXY} \
    https_proxy=${HTTPS_PROXY} \
    NO_PROXY=localhost,127.0.0.1,.svc.cluster.local,.svc,.fenjin.org,10.199.64.20 \
    no_proxy=localhost,127.0.0.1,.svc.cluster.local,.svc,.fenjin.org,10.199.64.20
# Install ca-certificates/curl directly through the build proxy — the official
# Alpine CDN is left untouched (no mirror swap). apk honors the LOWERCASE
# http_proxy/https_proxy variables.
RUN apk add --no-cache ca-certificates curl
COPY --from=build /out/agent-worker /usr/local/bin/agent-worker
# Default workspace is ~/workspace (the binary's own default); create it.
RUN mkdir -p /root/workspace /data
ENV WORKER_PORT=8080 \
    WORKER_DB=/data/jobs.db
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/agent-worker"]
