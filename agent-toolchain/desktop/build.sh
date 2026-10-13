#!/usr/bin/env bash
# Build and push an agent-worker GUI sandbox image.
#
#   ./agent-toolchain/desktop/build.sh openbox      # -> sandbox-x11     (Xvfb+openbox)
#   ./agent-toolchain/desktop/build.sh labwc        # -> sandbox-wayland (labwc)
#
# The two flavors are separate IMAGES (not two tags of one image): `openbox` is
# the X11 desktop (`sandbox-x11`) and `labwc` the Wayland desktop
# (`sandbox-wayland`), both tagged `debian-trixie` like the rest of the catalog.
#
# SELF-CONTAINED / RUN-ONLY: the image bakes the screen stack AND the
# agent-worker binary, and its entrypoint starts both. The gateway runs only
# `sandbox-<lang>` images (agent-worker as the sole ENTRYPOINT), so this image
# is NOT used as a sandbox base; deploy it as a plain Deployment
# (abc-protocol/deploy: worker-k8s/agent-worker-desktop.yaml).
#
# Built FROM agent-toolchain/toolchain-base, so the base build tools come along.
#
# Prerequisites:
#   * dist/agent-worker-linux-amd64    (scripts/build-all.sh)
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${DIR}/../.." && pwd)"

FLAVOR="${1:-${FLAVOR:-openbox}}"
# flavor -> image name. openbox = X11 desktop, labwc = Wayland desktop.
case "$FLAVOR" in
  openbox) DEFAULT_NAME=sandbox-x11 ;;
  labwc)   DEFAULT_NAME=sandbox-wayland ;;
  *) echo "unknown flavor '$FLAVOR' (want openbox|labwc)" >&2; exit 2 ;;
esac

REGISTRY="${REGISTRY:-artifact.worker.svc.cluster.local}"
NAMESPACE="${NAMESPACE:-sandbox}"
NAME="${NAME:-$DEFAULT_NAME}"
TAG="${TAG:-debian-trixie}"
DEST="${REGISTRY}/${NAMESPACE}/${NAME}:${TAG}"
BUILDKIT="${BUILDKIT_ADDR:-tcp://buildkitd.agent.svc.cluster.local:1234}"
PROXY="${PROXY:-http://mihomo.develop.svc.cluster.local:7890}"
# Keep in-cluster traffic OFF the proxy (buildctl gRPC to buildkitd, the
# artifact registry). Both spellings: some tools read no_proxy, others NO_PROXY.
NO_PROXY="${NO_PROXY:-localhost,127.0.0.1,.svc,.svc.cluster.local,.cluster.local,artifact.worker.svc.cluster.local,buildkitd.worker.svc.cluster.local,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16}"
no_proxy="${no_proxy:-$NO_PROXY}"
export NO_PROXY no_proxy
WORKER_BIN="${WORKER_BIN:-${ROOT}/dist/agent-worker-linux-amd64}"
# native-a11y daemon (abc-agent/worker-native, Zig). Optional: when present it is
# baked into the image and the entrypoint starts it on 127.0.0.1:48079.
NATIVE_BIN="${NATIVE_BIN:-${ROOT}/dist/native-a11y-linux-amd64}"
# The image is built FROM the generic toolchain base (build-essential etc.).
BASE_IMAGE="${BASE_IMAGE:-artifact.worker.svc.cluster.local/library/debian:trixie-slim}"

FDIR="${DIR}/${FLAVOR}"
for f in "${FDIR}/Containerfile" "${FDIR}/entrypoint.sh" "${WORKER_BIN}"; do
  [ -e "$f" ] || { echo "missing $f" >&2; exit 1; }
done
BUILDCTL="${BUILDCTL:-$(command -v buildctl || echo /opt/tools/mise/installs/aqua-moby-buildkit/0.32.2/bin/buildctl)}"

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT
cp "${FDIR}/Containerfile" "${WORK}/Dockerfile"
cp "${FDIR}/entrypoint.sh" "${WORK}/"
cp "${WORKER_BIN}" "${WORK}/agent-worker"
if [ -e "${NATIVE_BIN}" ]; then
  cp "${NATIVE_BIN}" "${WORK}/native-a11y"
fi

echo "Building ${NAME}:${TAG} (flavor=${FLAVOR}, buildkitd=${BUILDKIT})"
"${BUILDCTL}" --addr "${BUILDKIT}" build \
  --frontend dockerfile.v0 \
  --local "context=${WORK}" \
  --local "dockerfile=${WORK}" \
  --opt "filename=Dockerfile" \
  --opt "build-arg:BASE_IMAGE=${BASE_IMAGE}" \
  --opt "build-arg:HTTP_PROXY=${PROXY}" \
  --opt "build-arg:HTTPS_PROXY=${PROXY}" \
  --opt "build-arg:NO_PROXY=localhost,127.0.0.1,.svc.cluster.local,.svc,.nip.io,10.199.64.20" \
  --output "type=oci,dest=${WORK}/image.oci,compression=zstd" \
  --progress plain

echo "Pushing to ${DEST}"
mkdir -p "${WORK}/oci" && tar -xf "${WORK}/image.oci" -C "${WORK}/oci"
skopeo copy --src-tls-verify=false --dest-tls-verify=false \
  --dest-creds "${FORGEJO_USER:-root}:${FORGEJO_PASS:-devpassword}" \
  "oci:${WORK}/oci" "docker://${DEST}"

echo "Verifying push:"
skopeo inspect --creds "${FORGEJO_USER:-root}:${FORGEJO_PASS:-devpassword}" \
  --tls-verify=false "docker://${DEST}" >/dev/null 2>&1 \
  && echo "OK ${DEST}" \
  || echo "inspect failed for ${DEST} (image may still be present)"
