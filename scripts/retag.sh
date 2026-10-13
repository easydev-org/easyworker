#!/usr/bin/env bash
# Retag an image in the in-cluster registry WITHOUT re-uploading layers.
#
#   scripts/retag.sh <src-repo> <src-tag> <dst-repo> <dst-tag>
#
#   scripts/retag.sh agent-toolchain/sandbox-windows base \
#                    sandbox/sandbox-windows base
#
# Uses the Registry v2 API directly: every config/layer blob is mounted from the
# source repo into the destination repo (cross-repo blob mount — no bytes are
# transferred) and the same manifest is PUT under the new tag. This is how the
# image catalog was renamed to the unified `<role>-<subject>` scheme without
# rebuilding the multi-GB VM disks. Safe to re-run (idempotent).
#
# Env: REGISTRY (default git.agent.svc.cluster.local), FORGEJO_USER/FORGEJO_PASS.
# Requires: curl, python3.
set -Eeuo pipefail

REG="${REGISTRY:-git.agent.svc.cluster.local}"
USER="${FORGEJO_USER:-root}"
PASS="${FORGEJO_PASS:-devpassword}"
AUTH="$USER:$PASS"

[ "$#" -eq 4 ] || { echo "usage: $0 <src-repo> <src-tag> <dst-repo> <dst-tag>" >&2; exit 2; }
SRC_REPO="$1"; SRC_TAG="$2"; DST_REPO="$3"; DST_TAG="$4"

ACCEPT='application/vnd.docker.distribution.manifest.v2+json,application/vnd.docker.distribution.manifest.list.v2+json,application/vnd.oci.image.manifest.v1+json,application/vnd.oci.image.index.v1+json'
base="http://${REG}/v2"

# Read the source manifest (and its media type) under a broad Accept.
ct=$(curl -sI -H "Accept: ${ACCEPT}" -u "$AUTH" "${base}/${SRC_REPO}/manifests/${SRC_TAG}" \
     | awk -F': ' 'tolower($1)=="content-type"{print $2}' | tr -d '\r')
[ -n "$ct" ] || { echo "cannot read ${SRC_REPO}:${SRC_TAG}" >&2; exit 1; }
manifest=$(curl -s -H "Accept: ${ACCEPT}" -u "$AUTH" "${base}/${SRC_REPO}/manifests/${SRC_TAG}")
echo "== ${SRC_REPO}:${SRC_TAG} (${ct}) -> ${DST_REPO}:${DST_TAG} =="

# Blobs to carry over: an image manifest has config+layers; an index/list has
# child manifests instead.
digests=$(printf '%s' "$manifest" | python3 -c '
import sys, json
m = json.load(sys.stdin)
out = []
if "manifests" in m:                      # image index / manifest list
    for c in m["manifests"]:
        out.append(("child", c["digest"]))
else:
    if "config" in m: out.append(("blob", m["config"]["digest"]))
    for l in m.get("layers", []): out.append(("blob", l["digest"]))
for kind, d in out:
    print(kind, d)
')

printf '%s\n' "$digests" | while read -r kind d; do
  [ -n "$d" ] || continue
  [ "$kind" = "blob" ] || continue
  code=$(curl -s -o /dev/null -w '%{http_code}' -X POST -u "$AUTH" \
    -H 'Content-Type: application/json' \
    "${base}/${DST_REPO}/blobs/uploads/?mount=${d}&from=${SRC_REPO}")
  if [ "$code" = "201" ]; then
    echo "  mounted ${d:0:19}"
  else
    # Mount refused: fall back to copying the blob through this host.
    echo "  mount miss (${code}) for ${d:0:19}; copying"
    loc=$(curl -sI -X POST -u "$AUTH" "${base}/${DST_REPO}/blobs/uploads/" \
          | awk -F': ' 'tolower($1)=="location"{print $2}' | tr -d '\r')
    curl -s -o /dev/null -u "$AUTH" -X PUT -H 'Content-Type: application/octet-stream' \
      -T <(curl -s -u "$AUTH" "${base}/${SRC_REPO}/blobs/${d}") "http://${REG}${loc}?digest=${d}"
  fi
done

# Re-PUT any child manifests (same digest) before the parent index.
if printf '%s' "$manifest" | grep -q '"manifests"'; then
  printf '%s\n' "$digests" | while read -r kind d; do
    [ "$kind" = "child" ] || continue
    child=$(curl -s -H "Accept: ${ACCEPT}" -u "$AUTH" "${base}/${SRC_REPO}/manifests/${d}")
    cct=$(curl -sI -H "Accept: ${ACCEPT}" -u "$AUTH" "${base}/${SRC_REPO}/manifests/${d}" \
          | awk -F': ' 'tolower($1)=="content-type"{print $2}' | tr -d '\r')
    curl -s -o /dev/null -w '  child %{http_code}\n' -X PUT -u "$AUTH" -H "Content-Type: ${cct}" \
      --data-binary "$child" "${base}/${DST_REPO}/manifests/${d}"
  done
fi

printf '%s' "$manifest" | curl -s -o /dev/null -w '  manifest %{http_code}\n' -X PUT -u "$AUTH" \
  -H "Content-Type: ${ct}" --data-binary @- "${base}/${DST_REPO}/manifests/${DST_TAG}"

echo "OK ${DST_REPO}:${DST_TAG}"
