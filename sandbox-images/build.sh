#!/usr/bin/env bash
# Build SANDBOX images by baking the agent-worker binary (compiled from THIS
# repo) into an existing GENERIC toolchain image
# (agent-toolchain/toolchain-<lang>:debian-trixie). Pushes them to
# <DEST_REGISTRY>/<SANDBOX_ORG>/sandbox-<lang>:debian-trixie.
#
# IMPORTANT: the gateway PULLS sandbox images from the ARTIFACT registry
# (`artifact.worker.svc.cluster.local`), while the generic TOOLCHAIN base images
# live in the Forgejo registry (`git.agent.svc.cluster.local`). So the SOURCE and
# DESTINATION registries differ: REGISTRY (source) defaults to git.agent;
# DEST_REGISTRY (destination) defaults to artifact.
#
# The gateway does NOT inject the worker at launch; a sandbox may only run one
# of these dedicated images. Re-run this whenever the worker changes.
#
#   ./build.sh base node python      # build sandbox-base/-node/-python
#   ./build.sh                       # build the default set (base + a few langs)
#
# Env overrides:
#   REGISTRY     registry host            (default git.agent.svc.cluster.local)
#   DEST_REGISTRY destination registry    (default artifact.worker.svc.cluster.local)
#   SANDBOX_ORG  destination org          (default sandbox)
#   TOOLCHAIN_ORG source toolchain org    (default agent-toolchain)
#   DISTRO_TAG   toolchain tag            (default debian-trixie)
#   BUILDKIT_ADDR buildkitd               (default tcp://buildkitd.agent.svc.cluster.local:1234)
#   FORGEJO_USER/FORGEJO_PASS             source (git.agent) creds
#   ARTIFACT_TOKEN                        destination (artifact) write token
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# The worker source is THIS repo's root (sandbox-images/ lives inside it).
ROOT="$(cd "${HERE}/.." && pwd)"

REGISTRY="${REGISTRY:-git.agent.svc.cluster.local}"
# The registry the GATEWAY pulls sandbox images from. Keep this in sync with the
# gateway's IMAGE_REGISTRY_HOST / DEFAULT_SANDBOX_IMAGE.
DEST_REGISTRY="${DEST_REGISTRY:-artifact.worker.svc.cluster.local}"
SANDBOX_ORG="${SANDBOX_ORG:-sandbox}"
TOOLCHAIN_ORG="${TOOLCHAIN_ORG:-agent-toolchain}"
DISTRO_TAG="${DISTRO_TAG:-debian-trixie}"
BUILDKIT_ADDR="${BUILDKIT_ADDR:-tcp://buildkitd.agent.svc.cluster.local:1234}"
FORGEJO_USER="${FORGEJO_USER:-root}"
FORGEJO_PASS="${FORGEJO_PASS:-devpassword}"
ARTIFACT_TOKEN="${ARTIFACT_TOKEN:-dev-artifact-token}"

# The registry + buildkitd MUST bypass any HTTP proxy.
for _h in .svc.cluster.local .svc 10.199.64.20 "${REGISTRY%%:*}" "${DEST_REGISTRY%%:*}"; do
  case ",${NO_PROXY:-}," in *",${_h},"*) ;; *) NO_PROXY="${NO_PROXY:+${NO_PROXY},}${_h}" ;; esac
done
no_proxy="${NO_PROXY}"
export NO_PROXY no_proxy

# Default set: the toolchains that are NOT yet installable at runtime.
#
# Phase 1 (go/node/python/java25/dotnet/php/dart/kotlin/zig/bun/pixi) AND
# phase 2 (scala/groovy/deno/julia/crystal/ocaml/haskell/ruby/rust) are
# PUBLISHED to the artifact store and installed on demand by the worker
# (WORKSPACE_TOOLCHAINS → internal/toolchains), so they are NOT baked into
# sandbox images anymore.
#
# java25 stays: it is a published toolchain AND the parent of the
# kotlin/scala/clojure/groovy toolchain images (`<lang>.base` → toolchain-java25).
# Keeping it preinstalled is the deliberate option (a) — the `.base` files only
# affect the TOOLCHAIN-image builds, so this is a belt-and-braces choice, not a
# sandbox-image requirement.
#
# What remains is the not-yet-published set (phase 2b: java/clojure/elixir/gleam/
# swift/clang/lua/perl/r/conda/godot), the ML chain, and the OS images.
#
# Pass explicit names to build a subset.
LANGS="${*:-base java java25 clojure elixir gleam swift clang lua perl r conda godot cuda torch vllm vllm-omni llamacpp comfyui}"

echo "==> cross-compiling agent-worker (linux/amd64) from ${ROOT}"
# Embed the `xa11y` CLI (internal/xa11ybin): fetch the linux asset first (the
# assets are fetched from artifact, not committed — see .gitignore).
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -C "${ROOT}" -trimpath -ldflags "-s -w" \
  -o "${HERE}/agent-worker" ./cmd/agent-worker
ls -l "${HERE}/agent-worker"

build_one() {
  local proto="$1" src name dest work
  if [ "$proto" = "base" ]; then
    name="sandbox-base"
  else
    name="sandbox-${proto}"
  fi
  src="${REGISTRY}/${TOOLCHAIN_ORG}/toolchain-${proto}:${DISTRO_TAG}"
    dest="${DEST_REGISTRY}/${SANDBOX_ORG}/${name}:${DISTRO_TAG}"
  work="$(mktemp -d)"
  cp "${HERE}/Dockerfile" "${HERE}/agent-worker" "${work}/"
  echo "== build ${name} (FROM ${src}) =="
  buildctl --addr "${BUILDKIT_ADDR}" build \
    --frontend dockerfile.v0 \
    --local "context=${work}" \
    --local "dockerfile=${work}" \
    --opt "filename=Dockerfile" \
    --opt "build-arg:BASE_IMAGE=${src}" \
    --output "type=docker,name=${name}:${DISTRO_TAG},dest=${work}/image.tar" \
    --progress plain
  echo "== push ${dest} =="
    # Creds depend on the destination registry: artifact uses a bearer token.
    local destcreds
    case "${DEST_REGISTRY}" in
      *artifact*) destcreds="root:${ARTIFACT_TOKEN}" ;;
      *)          destcreds="${FORGEJO_USER}:${FORGEJO_PASS}" ;;
    esac
    skopeo copy --dest-creds "${destcreds}" --dest-tls-verify=false \
      "docker-archive:${work}/image.tar:${name}:${DISTRO_TAG}" "docker://${dest}"
  rm -rf "${work}"
  echo "OK ${dest}"
}

for l in ${LANGS}; do
  build_one "$l"
done
