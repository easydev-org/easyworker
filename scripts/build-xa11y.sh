#!/usr/bin/env bash
# Fetch or build the `xa11y` computer-use CLI (github.com/xa11y/xa11y) into dist/.
#
# The sandbox images ship `xa11y` so a job can read and drive native apps'
# accessibility trees. Upstream publishes NO prebuilt CLI binary (only a Python
# wheel and a Node addon), so we produce our own:
#
#   scripts/build-xa11y.sh fetch    # download the prebuilt release assets (fast)
#   scripts/build-xa11y.sh          # build all three from source
#   scripts/build-xa11y.sh linux
#   scripts/build-xa11y.sh windows
#   scripts/build-xa11y.sh macos
#
# `fetch` pulls the binaries from this repo's `xa11y-v<ver>` GitHub release
# (the same pattern as the agent-worker binaries in v0.1.0), so a fresh
# checkout can repopulate dist/ without a compiler.
#
# Linux runtime deps (verified with `ldd`): glibc + libgcc +
# libxkbcommon.so.0. The desktop image installs libxkbcommon0; D-Bus/AT-SPI/X11
# are dlopen'd, so no link-time packages are needed.
#
# Windows is cross-built with an llvm-mingw toolchain (downloaded to a cache dir
# on first use) against the `x86_64-pc-windows-gnullvm` target with
# `+crt-static`, producing a self-contained .exe that needs only Windows system
# DLLs (no libunwind/libc++ redistributable).
#
# macOS is NOT cross-compiled (Apple frameworks + Objective-C need the Apple
# SDK). It is built inside a throwaway macOS guest; the `macos` target only
# checks the extracted binary is present (see agent-toolchain/vm/macos/README.md).
#
# Env:
#   XA11Y_REPO    git URL          (default https://github.com/xa11y/xa11y)
#   XA11Y_TAG     git tag/ref      (default v0.15.0)
#   XA11Y_OUT     linux output     (default dist/xa11y-linux-amd64)
#   XA11Y_WIN_OUT windows output   (default dist/xa11y-windows-amd64.exe)
#   XA11Y_MAC_OUT macOS binary     (default dist/xa11y-darwin-amd64)
#   XA11Y_RELEASE release tag      (default xa11y-v0.15.0, for `fetch`)
#   XA11Y_BASEURL download base    (default the GitHub release URL)
#   LLVM_MINGW_DIR  toolchain dir  (default /tmp/opencode/llvm-mingw)
#   BUILD_PROXY   HTTP proxy       (default mihomo)
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

XA11Y_REPO="${XA11Y_REPO:-https://github.com/xa11y/xa11y}"
XA11Y_TAG="${XA11Y_TAG:-v0.15.0}"
XA11Y_OUT="${XA11Y_OUT:-dist/xa11y-linux-amd64}"
XA11Y_WIN_OUT="${XA11Y_WIN_OUT:-dist/xa11y-windows-amd64.exe}"
XA11Y_MAC_OUT="${XA11Y_MAC_OUT:-dist/xa11y-darwin-amd64}"
XA11Y_RELEASE="${XA11Y_RELEASE:-xa11y-v0.15.0}"
XA11Y_BASEURL="${XA11Y_BASEURL:-https://github.com/abcp-sdk/worker/releases/download/${XA11Y_RELEASE}}"
BUILD_PROXY="${BUILD_PROXY:-http://mihomo.develop.svc.cluster.local:7890}"
LLVM_MINGW_DIR="${LLVM_MINGW_DIR:-/tmp/opencode/llvm-mingw}"
LLVM_MINGW_VERSION="${LLVM_MINGW_VERSION:-20260922}"

WANT="${*:-linux windows macos}"

fetch_one() { # <asset-name> <dest> <mode>
  local name="$1" dest="$2" mode="$3"
  mkdir -p "$(dirname "$dest")"
  echo "==> fetch ${name}"
  HTTPS_PROXY="${BUILD_PROXY}" HTTP_PROXY="${BUILD_PROXY}" \
    curl -fSL --retry 3 -o "${dest}.part" "${XA11Y_BASEURL}/${name}"
  chmod "$mode" "${dest}.part"
  mv "${dest}.part" "$dest"
  echo "==> ${dest} ($(du -h "$dest" | cut -f1))"
}

build_fetch() {
  fetch_one xa11y-linux-amd64       "$XA11Y_OUT"     0755
  fetch_one xa11y-windows-amd64.exe "$XA11Y_WIN_OUT" 0644
  fetch_one xa11y-darwin-amd64      "$XA11Y_MAC_OUT" 0755
}

CARGO="$(command -v cargo || true)"
[ -n "$CARGO" ] || { echo "cargo not found (need rust >= 1.88)" >&2; exit 1; }
# Resolve the REAL cargo (not a mise shim): the Windows build runs with a
# custom PATH that excludes the shims directory.
if command -v readlink >/dev/null && [ -L "$CARGO" ]; then
  CARGO="$(readlink -f "$CARGO")"
fi
RUSTBIN="$(dirname "$(command -v rustc)")"

rust_minor="$(rustc --version | grep -oE '[0-9]+\.[0-9]+' | head -1 | cut -d. -f2)"
if [ "${rust_minor:-0}" -lt 88 ]; then
  echo "rustc >= 1.88 required (xa11y rust-version); have $(rustc --version)" >&2
  exit 1
fi

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

echo "==> clone ${XA11Y_REPO} @ ${XA11Y_TAG}"
HTTPS_PROXY="${BUILD_PROXY}" HTTP_PROXY="${BUILD_PROXY}" \
  git clone --depth 1 --branch "${XA11Y_TAG}" "${XA11Y_REPO}" "${WORK}/xa11y"

build_linux() {
  echo "==> cargo build --release (linux) -p xa11y"
  HTTPS_PROXY="${BUILD_PROXY}" HTTP_PROXY="${BUILD_PROXY}" \
    "$CARGO" build --release -p xa11y --bin xa11y --manifest-path "${WORK}/xa11y/Cargo.toml"
  mkdir -p "$(dirname "${XA11Y_OUT}")"
  install -m 0755 "${WORK}/xa11y/target/release/xa11y" "${XA11Y_OUT}"
  echo "==> ${XA11Y_OUT} ($(du -h "${XA11Y_OUT}" | cut -f1))"
  ldd "${XA11Y_OUT}" | grep -vE 'linux-vdso|ld-linux|libc\.so|libm\.so|libgcc_s' || true
}

ensure_llvm_mingw() {
  local bin="${LLVM_MINGW_DIR}/bin"
  [ -x "${bin}/x86_64-w64-mingw32-clang" ] && return 0
  local asset="llvm-mingw-${LLVM_MINGW_VERSION}-ucrt-ubuntu-22.04-x86_64.tar.xz"
  local url="https://github.com/mstorsjo/llvm-mingw/releases/download/${LLVM_MINGW_VERSION}/${asset}"
  echo "==> fetching llvm-mingw ${LLVM_MINGW_VERSION}"
  mkdir -p "${LLVM_MINGW_DIR}"
  HTTPS_PROXY="${BUILD_PROXY}" HTTP_PROXY="${BUILD_PROXY}" \
    curl -fSL --retry 3 -o "${LLVM_MINGW_DIR}/${asset}" "${url}"
  tar -xf "${LLVM_MINGW_DIR}/${asset}" -C "${LLVM_MINGW_DIR}" --strip-components=1
  rm -f "${LLVM_MINGW_DIR}/${asset}"
}

build_windows() {
  ensure_llvm_mingw
  local bin="${LLVM_MINGW_DIR}/bin"
  echo "==> rustup target add x86_64-pc-windows-gnullvm"
  rustup target add x86_64-pc-windows-gnullvm >/dev/null 2>&1 || true
  echo "==> cargo build --release (windows, static) -p xa11y"
  # The custom PATH keeps the mingw tools but drops the mise shims (which break
  # under a rewritten PATH); cargo/rustc come from the absolute RUSTBIN.
  PATH="${bin}:${RUSTBIN}:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin" \
  HTTPS_PROXY="${BUILD_PROXY}" HTTP_PROXY="${BUILD_PROXY}" \
  CARGO_TARGET_X86_64_PC_WINDOWS_GNULLVM_LINKER="${bin}/x86_64-w64-mingw32-clang" \
  RUSTFLAGS="-C target-feature=+crt-static" \
    "$CARGO" build --release -p xa11y --bin xa11y \
      --target x86_64-pc-windows-gnullvm \
      --manifest-path "${WORK}/xa11y/Cargo.toml"
  mkdir -p "$(dirname "${XA11Y_WIN_OUT}")"
  install -m 0644 "${WORK}/xa11y/target/x86_64-pc-windows-gnullvm/release/xa11y.exe" "${XA11Y_WIN_OUT}"
  echo "==> ${XA11Y_WIN_OUT} ($(du -h "${XA11Y_WIN_OUT}" | cut -f1))"
}

build_macos() {
  # macOS cannot be cross-compiled: xa11y-macos links Apple frameworks
  # (ApplicationServices/CoreGraphics/Foundation/ScreenCaptureKit) and compiles
  # Objective-C (exception_safe.m via the `cc` crate), so it needs the Apple
  # SDK. There is no prebuilt CLI binary upstream (only a Python wheel / a Node
  # addon). So we build it INSIDE a throwaway macOS guest and extract the
  # resulting self-contained Mach-O, which links only system frameworks — the
  # sandbox then needs no Command Line Tools and no Python.
  #
  # This is a documented manual step (it needs a running macOS VM), not a
  # host-side build. Recipe (see agent-toolchain/vm/macos/README.md):
  #   * boot a scratch copy of the sandbox-macos:base disk,
  #   * `softwareupdate -i "Command Line Tools for Xcode-16.4"`,
  #   * install Rust: curl the dist tarball
  #     https://static.rust-lang.org/dist/<date>/rust-<ver>-x86_64-apple-darwin.tar.xz
  #     and run its install.sh --prefix=<writable dir>,
  #   * curl the source (codeload .../tar.gz/refs/tags/v0.15.0),
  #   * RUSTC=<prefix>/bin/rustc <prefix>/bin/cargo build --release -p xa11y --bin xa11y,
  #   * copy target/release/xa11y out to ${XA11Y_MAC_OUT}.
  # Verified: ~13 min build, 2.1 MB Mach-O, deps are only /System/* + /usr/lib/*.
  echo "build_macos is a manual in-guest step; see agent-toolchain/vm/macos/README.md" >&2
  echo "expected output: ${XA11Y_MAC_OUT}" >&2
  [ -x "${XA11Y_MAC_OUT}" ] || { echo "missing ${XA11Y_MAC_OUT}" >&2; exit 1; }
  echo "==> ${XA11Y_MAC_OUT} ($(du -h "${XA11Y_MAC_OUT}" | cut -f1))"
}

for t in ${WANT}; do
  case "$t" in
    fetch)       build_fetch ;;
    linux)       build_linux ;;
    windows)     build_windows ;;
    macos)       build_macos ;;
    *) echo "unknown target '$t' (want fetch|linux|windows|macos)" >&2; exit 2 ;;
  esac
done
