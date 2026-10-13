#!/bin/sh
# Fetch the `xa11y` accessibility CLI from the ARTIFACT generic store into
# internal/xa11ybin/assets/, where //go:embed picks it up at build time. The
# binaries are NEVER committed (see .gitignore): the worker binary embeds them,
# and the sandbox images are published to artifact.
#
# POSIX sh (the build image's /bin/sh is busybox/dash — no bash arrays).
#   scripts/fetch-xa11y.sh                 # all platforms
#   scripts/fetch-xa11y.sh linux darwin    # a subset
#
# Env:
#   ARTIFACT   artifact base URL (default http://artifact.worker.svc.cluster.local)
#   XA11Y_NS   generic namespace (default abcp)
#   XA11Y_NAME generic name      (default agent-worker)
#   XA11Y_TAG  generic tag       (default xa11y-v0.15.0)
set -eu
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEST="${ROOT}/internal/xa11ybin/assets"
ARTIFACT="${ARTIFACT:-http://artifact.worker.svc.cluster.local}"
XA11Y_NS="${XA11Y_NS:-abcp}"
XA11Y_NAME="${XA11Y_NAME:-agent-worker}"
XA11Y_TAG="${XA11Y_TAG:-xa11y-v0.15.0}"

mkdir -p "$DEST"

fetch() { # <asset> <outfile>
  asset="$1"; out="$2"
  url="${ARTIFACT}/artifacts/generic/_hosted/${XA11Y_NS}/${XA11Y_NAME}/${XA11Y_TAG}/${asset}"
  echo "==> ${asset}"
  # The embed expects a gzip-compressed asset (the raw CLI is ~3x larger), so
  # download to a temp file and compress it in place.
  curl -fSL --retry 3 -o "${DEST}/${out}.raw" "$url"
  gzip -9 -c "${DEST}/${out}.raw" > "${DEST}/${out}"
  rm -f "${DEST}/${out}.raw"
  chmod 0644 "${DEST}/${out}"
}

# Default: all platforms; otherwise the names passed on the command line.
if [ "$#" -eq 0 ]; then
  set -- linux darwin windows
fi
for os in "$@"; do
  case "$os" in
    linux)   fetch xa11y-linux-amd64       xa11y-linux-amd64.gz ;;
    darwin)  fetch xa11y-darwin-amd64      xa11y-darwin-amd64.gz ;;
    windows) fetch xa11y-windows-amd64.exe xa11y-windows-amd64.exe.gz ;;
    *) echo "unknown os '$os' (want linux|darwin|windows)" >&2; exit 2 ;;
  esac
done
ls -l "$DEST"
