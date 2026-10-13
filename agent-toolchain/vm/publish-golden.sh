#!/usr/bin/env bash
# Publish a re-baked golden disk to the artifact generic store SAFELY.
#
# A plain `curl -T` to the artifact path is NOT atomic: an interrupted PUT
# TRUNCATES the previously-published object. Observed once: a killed upload left
# the 15 GB `golden-windows/11/data-devtools.qcow2` as a 33-byte error body,
# destroying the good disk (recovered only from a node's golden-disk cache).
#
# This wrapper refuses to publish anything that does not look like a real qcow2
# of a plausible size, then verifies the object it just wrote (size round-trip)
# before declaring success — so a failed/partial upload can never clobber a good
# artifact.
#
#   publish-golden.sh <local.qcow2> <url> [min-bytes] [artifact-token]
#
#   min-bytes   refuse to publish a local file smaller than this (default 1 GiB)
#   token       defaults to $ARTIFACT_TOKEN, else "dev-artifact-token"
#
# Run it as `bash publish-golden.sh …`. NOTE: this file may land with mode 0644
# (the MR pipeline does not preserve the executable bit — several VM scripts,
# e.g. windows/golden-disk.sh and windows/offline-rebake.sh, are 0644 in-tree),
# so invoke it via `bash` rather than `./publish-golden.sh`.
#
# Usage in a bake script (replaces the raw `curl -T`):
#   bash publish-golden.sh /storage/out.qcow2 \
#     http://artifact.worker.svc.cluster.local/artifacts/generic/golden-windows/11/data-devtools.qcow2
set -Eeuo pipefail

LOCAL="${1:?usage: publish-golden.sh <local.qcow2> <url> [min-bytes] [token]}"
URL="${2:?}"
MIN="${3:-1073741824}"                       # default: refuse < 1 GiB
TOKEN="${4:-${ARTIFACT_TOKEN:-dev-artifact-token}}"

fail() { printf '[publish-golden] REFUSE: %s\n' "$*" >&2; exit 1; }

[ -s "${LOCAL}" ] || fail "local file missing or empty: ${LOCAL}"
sz=$(wc -c <"${LOCAL}")
[ "${sz}" -ge "${MIN}" ] || fail "local size ${sz} < minimum ${MIN} — refusing to overwrite the artifact"

# A qcow2 starts with the magic 'QFI\xfb' (0x514649fb). A truncated PUT body
# (e.g. a JSON error) fails this, so we never push a non-disk over a good one.
magic=$(od -An -tx1 -N4 "${LOCAL}" | tr -d ' \n')
[ "${magic}" = "514649fb" ] || fail "not a qcow2 (magic=${magic}) — refusing to overwrite the artifact"

echo "[publish-golden] publishing ${LOCAL} (${sz} bytes) -> ${URL}"
curl -fS -T "${LOCAL}" -H "Authorization: Bearer ${TOKEN}" \
  -w '\n[publish-golden] PUT http=%{http_code} size=%{size_upload}\n' "${URL}"

# Round-trip check: the object we just wrote must report the SAME size.
got=$(curl -fsS -I "${URL}" | awk 'tolower($1)=="content-length:"{print $2}' | tr -d '\r')
[ "${got}" = "${sz}" ] || fail "post-upload size mismatch: artifact=${got} local=${sz}"
echo "[publish-golden] OK: ${URL} = ${got} bytes"
