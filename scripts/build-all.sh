#!/usr/bin/env bash
# Cross-compile easyworker + awtest for every supported platform/arch into
# dist/: linux, windows, darwin (macOS) × amd64 + arm64. Pure-Go (CGO disabled)
# so all targets build from any host.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

# //go:embed). Its per-platform assets are fetched from artifact, NOT committed
# (.gitignore), so a fresh checkout MUST fetch them before compiling — one per
# GOOS, since build-all cross-compiles linux/windows/darwin.

# GOOS/GOARCH -> filename suffix (windows gets .exe).
suffix() { # os arch
  if [ "$1" = "windows" ]; then echo ".exe"; else echo ""; fi
}

build() { # os arch
  local os="$1" arch="$2" sfx
  sfx="$(suffix "$os" "$arch")"
  echo "-> easyworker-$os-$arch$sfx + awtest-$os-$arch$sfx + trustprobe-$os-$arch$sfx"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "-s -w" -o "dist/easyworker-$os-$arch$sfx" ./cmd/easyworker
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "-s -w" -o "dist/awtest-$os-$arch$sfx" ./cmd/awtest
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "-s -w" -o "dist/trustprobe-$os-$arch$sfx" ./cmd/trustprobe
}

rm -rf dist && mkdir -p dist
for os in linux windows darwin; do
  for arch in amd64 arm64; do
    build "$os" "$arch"
  done
done

ls -lh dist/
