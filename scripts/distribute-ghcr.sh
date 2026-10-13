#!/usr/bin/env bash
# Mirror the in-cluster `sandbox/*` catalog to ghcr.io/silvermelon233.
#
#   GH_PASS=<github-pat-with-write:packages> ./scripts/distribute-ghcr.sh
#
# Every image is tagged `v0.1.0` (the project's single release tag). A repo with
# several variants keeps its canonical variant under the plain name and each
# other variant under a suffixed name:
#
#   sandbox-android            -> aosp     ; sandbox-android-gms       -> gms
#
# Only the images that still exist are listed: the LANGUAGE sandboxes were
# removed (their toolchains are installed on demand via WORKSPACE_TOOLCHAINS),
# and the macOS/Windows VM repos keep a SINGLE `base` image (the variant — xcode
# / devtools — is chosen at run time by GOLDEN_DISK_URL, not by the tag).
#
# Source: the in-cluster registry `git.agent.svc.cluster.local` (root:devpassword).
# The push goes through the build host's HTTP proxy (mihomo), so this must run
# where `ghcr.io` is reachable (the dev pod / build host).
set -euo pipefail

REGISTRY="${REGISTRY:-git.agent.svc.cluster.local}"
SRC_CREDS="${SRC_CREDS:-root:devpassword}"
GH_ORG="${GH_ORG:-ghcr.io/silvermelon233}"
GH_USER="${GH_USER:-SilverMelon233}"
: "${GH_PASS:?set GH_PASS to a GitHub PAT with write:packages}"

# src-repo:src-tag:dest-name triples
PAIRS="
sandbox/sandbox-android:aosp:sandbox-android
sandbox/sandbox-android:gms:sandbox-android-gms
sandbox/sandbox-base:debian-trixie:sandbox-base
sandbox/sandbox-cuda:debian-trixie:sandbox-cuda
sandbox/sandbox-x11:debian-trixie:sandbox-x11
sandbox/sandbox-wayland:debian-trixie:sandbox-wayland
sandbox/sandbox-macos:base:sandbox-macos
sandbox/sandbox-windows:base:sandbox-windows
"

ok=0; fail=0
for triple in $PAIRS; do
  repo="${triple%%:*}"; rest="${triple#*:}"; srctag="${rest%%:*}"; name="${rest##*:}"
  dst="$GH_ORG/$name:v0.1.0"
  echo "### $repo:$srctag -> $dst"
  if skopeo copy --all --retry-times 3 \
      --src-tls-verify=false --src-creds "$SRC_CREDS" \
      --dest-creds "$GH_USER:$GH_PASS" \
      "docker://$REGISTRY/$repo:$srctag" "docker://$dst"; then
    echo "OK $dst"; ok=$((ok+1))
  else
    echo "FAIL $dst"; fail=$((fail+1))
  fi
done
echo "=== done ok=$ok fail=$fail ==="
[ "$fail" -eq 0 ]
