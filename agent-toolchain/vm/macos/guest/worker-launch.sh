#!/bin/bash
# AgentWorker macOS guest launcher.
#
# Runs in two places (same script):
#   * as `docker` via the LaunchAgent in the Aqua session (normal path), and
#   * as root via the one-shot LaunchDaemon (bootstrap/fallback).
#
# Responsibilities:
#   1. fetch the pod-provided per-pod env (WORKER_TOKEN + SANDBOX_PROXY) from
#      http://host.lan:8090/env
#   2. boot-fetch the current agent-worker binary from
#      http://host.lan:8090/worker (so a worker upgrade is an image change,
#      not a golden-disk change), falling back to the baked copy
#   3. boot-fetch the xa11y computer-use CLI from http://host.lan:8090/xa11y
#      and grant it the Accessibility (TCC) permission (SIP is off in this
#      image, so the TCC db is writable) — done as root before the worker
#   4. start agent-worker inside docker's GUI session
set -Eeuo pipefail

WROOT=/Users/docker
BIN="$WROOT/agent-worker-darwin"
DL="$WROOT/agent-worker-darwin.dl"
WS="$WROOT/workspace"
XA="$WROOT/xa11y"
XA_BIN=/usr/local/bin/xa11y
DP="$WROOT/displayplacer"
SCREEN_RESOLUTION="${SCREEN_RESOLUTION:-1920x1080}"
TCC="/Library/Application Support/com.apple.TCC/TCC.db"

# --- pod env (WORKER_TOKEN + SANDBOX_PROXY) -------------------------------
# The guest does not inherit the pod's env; the container publishes the few
# per-pod values it needs at host.lan:8090/env as KEY=VALUE lines.
PODENV=""
for _ in $(seq 1 60); do
  if PODENV=$(curl -s -m 2 http://host.lan:8090/env 2>/dev/null); then break; fi
  sleep 1
done
WORKER_TOKEN=$(printf '%s\n' "$PODENV" | sed -n 's/^WORKER_TOKEN=//p' | tr -d '\r')
SANDBOX_PROXY=$(printf '%s\n' "$PODENV" | sed -n 's/^SANDBOX_PROXY=//p' | tr -d '\r')
[ -n "$WORKER_TOKEN" ] && export WORKER_TOKEN
# The deployment HTTP proxy: jobs (git/npm/go) reach the blocked internet only
# through it. Jobs inherit the worker's env, so export it here.
if [ -n "$SANDBOX_PROXY" ]; then
  export HTTP_PROXY="$SANDBOX_PROXY" HTTPS_PROXY="$SANDBOX_PROXY" \
         http_proxy="$SANDBOX_PROXY" https_proxy="$SANDBOX_PROXY"
  export NO_PROXY="localhost,127.0.0.1,.svc,.svc.cluster.local,host.lan,artifact.worker.svc.cluster.local"
  export no_proxy="$NO_PROXY"
  # Also set the SYSTEM proxy so GUI/CFNetwork consumers (Xcode downloads,
  # Safari, curl without env) use it, not just env-var-aware tools. Best-effort.
  if [ "$(id -un)" = "root" ] || sudo -n true 2>/dev/null; then
    PROXY_HOST=$(printf '%s' "$SANDBOX_PROXY" | sed -E 's#^https?://##; s#/.*$##')
    PROXY_PORT=$(printf '%s' "$PROXY_HOST" | sed -E 's/.*://'); PROXY_HOST=${PROXY_HOST%%:*}
    for svc in "Wi-Fi" "Ethernet"; do
      sudo -n networksetup -setwebproxy "$svc" "$PROXY_HOST" "$PROXY_PORT" >/dev/null 2>&1 || true
      sudo -n networksetup -setsecurewebproxy "$svc" "$PROXY_HOST" "$PROXY_PORT" >/dev/null 2>&1 || true
      sudo -n networksetup -setproxybypassdomains "$svc" "*.local" "169.254/16" "*.svc" "*.svc.cluster.local" "host.lan" "localhost" "127.0.0.1" >/dev/null 2>&1 || true
    done
  fi
fi

# WORKER_OS (the sandbox OS) is baked into the golden disk; the macOS image has
# no ~/worker-os file (empty -> macos driver).
if [ -f "$WROOT/worker-os" ]; then
  WORKER_OS=$(tr -d '\r\n' < "$WROOT/worker-os")
  [ -n "$WORKER_OS" ] && export WORKER_OS
fi

mkdir -p "$WS"
cd "$WROOT"

# --- xa11y computer-use CLI (STATIC: baked into the golden disk) ------------
# xa11y is a stable CLI, so it takes the STATIC route: it is BAKED into the
# golden disk at /usr/local/bin/xa11y (the STABLE path macOS TCC attributes the
# grant to). The launcher does NOT boot-fetch it; it only makes sure the path
# exists. (The worker binary, by contrast, IS boot-fetched — see below.)
mkdir -p /usr/local/bin 2>/dev/null || true

# --- native-a11y (est://) daemon: boot-fetch + start ------------------------
# The Go worker talks to native-a11y over est:// on 127.0.0.1:48079. Fetch it
# every boot (image change, never a golden rebake) and start it BEFORE the
# worker so the first /stream/ws connection finds it listening.
NA_BIN="$WROOT/native-a11y"
if curl -s -m 60 -o "$NA_BIN.dl" http://host.lan:8090/native-a11y 2>/dev/null; then
  if [ "$(stat -f%z "$NA_BIN.dl" 2>/dev/null || echo 0)" -gt 10000 ]; then
    chmod 755 "$NA_BIN.dl"
    mv -f "$NA_BIN.dl" "$NA_BIN" 2>/dev/null || true
  fi
fi
if [ -x "$NA_BIN" ]; then
  pkill -f 'native-a11y serve' 2>/dev/null || true
  ( "$NA_BIN" serve >"$WROOT/native-a11y.log" 2>&1 & ) 2>/dev/null || true
fi

# --- display resolution -----------------------------------------------------
# NOTE: displayplacer was removed. It was a best-effort resolution setter, but
# the artifact path (displayplacer/1.4.0/displayplacer-darwin-amd64) 404s, so
# the launcher downloaded a 33-byte JSON error body and `displayplacer list`
# aborted the whole script under `set -e`+`pipefail` (exit 127) — the worker
# never started. Resolution is not critical; do NOT reintroduce a hard failure
# here. If a resolution must be forced, bake it into the golden disk's
# WindowServer prefs instead.

# --- boot-fetch the worker binary (dynamic ONLY, no disk fallback) ----------
# The container always serves the worker at host.lan:8090/worker. Fetch it on
# every boot and run THAT binary; a worker upgrade must never require a golden
# rebake. No disk fallback — if the fetch fails, retry until it succeeds.
for _ in $(seq 1 30); do
  if curl -s -m 30 -o "$DL" http://host.lan:8090/worker 2>/dev/null \
     && [ "$(stat -f%z "$DL" 2>/dev/null || echo 0)" -gt 1000000 ]; then
    chmod 755 "$DL"
    mv -f "$DL" "$BIN" 2>/dev/null || BIN="$DL"
    break
  fi
  rm -f "$DL" 2>/dev/null || true
  sleep 2
done

# --- TCC --------------------------------------------------------------------
# STATIC: the Accessibility / ScreenCapture grants are BAKED into the golden
# disk's TCC.db by guest/bake-hardening.sh (SIP is off, so the db is writable).
# TCC grants are keyed by the STABLE paths (/usr/local/bin/xa11y and
# ~/agent-worker-darwin), so they persist across boots and survive worker
# upgrades — the launcher does NOT re-grant every boot. Persistent state takes
# the static route.

# The daemon path runs as root; hand the workspace + binaries to docker so the
# worker (which runs as docker) can write its sqlite DB.
if [ "$(id -un)" = "root" ]; then
  chown -R docker:staff "$WS" 2>/dev/null || true
  chown docker:staff "$BIN" "$XA" 2>/dev/null || true
fi

# Jobs inherit the worker's environment, so put xa11y on PATH here — a
# launchd-spawned process gets only the bare system PATH.
export PATH="/usr/local/bin:$PATH"

# On-demand toolchains: the guest can only reach the container (host.lan:8090),
# whose nginx bridges /artifacts/ to the in-cluster artifact registry — so the
# worker's installer fetches toolchains through that bridge.
# The guest reaches the in-cluster artifact registry DIRECTLY (qemu NAT inherits
# the pod's cluster network) — no container bridge needed.
export WORKER_TOOLCHAIN_INDEX="http://artifact.worker.svc.cluster.local/artifacts/generic/toolchains/index/index.json"

# Agent path: already inside docker's Aqua session.
if [ "$(id -un)" = "docker" ]; then
  exec "$BIN" -addr 0.0.0.0:48080 -workspace "$WS" -db "$WS/jobs.db"
fi

# Daemon path: wait for the logged-in session, then hand off to the agent.
for _ in $(seq 1 150); do
  if launchctl print gui/501 >/dev/null 2>&1; then
    if launchctl asuser 501 launchctl kickstart -k gui/501/com.agentworker.worker 2>/dev/null; then
      exit 0
    fi
  fi
  sleep 2
done

# No GUI session ever appeared: run headless as docker (old behaviour).
# WORKER_OS (computer-use driver) and the streamed screen (/stream/ws) -> the
# control panel's desktop pane) must survive the sudo.
exec sudo -u docker --preserve-env=WORKER_TOKEN,WORKER_OS,,PATH "$BIN" \
  -addr 0.0.0.0:48080 -workspace "$WS" -db "$WS/jobs.db"
