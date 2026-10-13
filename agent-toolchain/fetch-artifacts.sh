#!/usr/bin/env bash
# Pre-download every debian-trixie toolchain artifact into a local cache, so
# image builds COPY them from the build context instead of curling upstream from
# inside the (slow, proxied) build container.
#
#   ./fetch-artifacts.sh              # every artifact of $DISTRO
#   ./fetch-artifacts.sh node python  # only the files those Dockerfiles need
#
# Cache layout: cache/<distro>/<filename>, matching CACHE_ROOT in config.sh
# (gitignored). Filenames come from the URL basename (%2B decoded) and must
# match the `COPY cache/<file>` lines in the Dockerfiles; this script verifies
# that and fails loudly on a missing artifact.
#
# Downloads go through BUILD_PROXY on the host. The one artifact with no single
# upstream tarball — the offline conan wheel set (cache/conan-wheels-*.tar.gz) —
# is assembled here with `pip download` from PyPI (conan is public), so it needs
# no urls.local.env override.
#
# NOTE: this helper was dropped from this tree when agent-toolchain/ was moved
# here from workspace-gateway (commit 5a8b2f5); restored so build_image's
# cache/<distro> contract holds. Keep it in sync with config.sh's CACHE_ROOT.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=config.sh
. "${HERE}/config.sh"
# Shared metadata: some toolchains (e.g. flutter) are index/publisher-only (no
# Dockerfile.<lang>), so `wanted` also consults toolchain_specs below.
# shellcheck source=toolchain-meta.sh
. "${HERE}/toolchain-meta.sh"

LANGS="${*:-}"
DIR="${HERE}/toolchain/${DISTRO}"
CACHE="${CACHE_ROOT}/${DISTRO}"

# Load URL knobs (canonical, then local overrides) into the environment.
set -a
# shellcheck source=/dev/null
. "${DIR}/urls.env"
[ -f "${HERE}/toolchain/urls.local.env" ] && . "${HERE}/toolchain/urls.local.env"
set +a

URL_VARS="NODE_URL GO_URL PYTHON_URL PYTHON313_URL UV_URL RUST_URL JDK_URL JDK25_URL GRADLE_URL \
DOTNET_URL RUBY_URL PHP_URL COMPOSER_URL OTP_URL ELIXIR_URL HEX_URL HEXKEY_URL \
DART_URL SWIFT_URL CONDA_URL PIXI_URL CONAN_URL \
BUN_URL DENO_URL GLEAM_URL KOTLIN_URL GROOVY_URL CLOJURE_URL SCALA_CLI_URL SBT_URL \
FLUTTER_LINUX_URL FLUTTER_WINDOWS_URL FLUTTER_MACOS_URL FLUTTER_MACOS_ARM64_URL \
LLVM_URL \
JULIA_URL CRYSTAL_URL OPAM_URL GHCUP_URL ZIG_URL CPANM_URL LUA_URL LUAROCKS_URL \
R_URL CMAKE_URL NINJA_URL GODOT_URL"

# local_name <url> -> filename used in the cache and in COPY lines.
local_name() {
  local u="${1%%\?*}"
  u="${u##*/}"
  echo "${u//%2B/+}"
}

# cache_name <url-var>: the on-disk name a Dockerfile expects. Almost always the
# URL basename; the hex registry key is the one exception (upstream serves it as
# hex-registry-public-key.pem, Dockerfile.elixir COPYs registry-public-key.pem).
cache_name() {
  local var="$1"
  case "$var" in
    HEXKEY_URL) echo "registry-public-key.pem" ;;
    *)          local_name "${!var}" ;;
  esac
}

# wanted <file>: with explicit languages, only files referenced by those
# Dockerfiles are downloaded.
wanted() {
  [ -z "$LANGS" ] && return 0
  local l dfproto
  for l in $LANGS; do
    dfproto="$l"; [ "$l" = "go" ] && dfproto="golang"
    [ "$l" = "base" ] && continue
    [ -f "${DIR}/Dockerfile.${dfproto}" ] || continue
    if grep -q "COPY cache/$1\b" "${DIR}/Dockerfile.${dfproto}" 2>/dev/null; then return 0; fi
  done
  # Index/publisher-only toolchains (no Dockerfile): match against their
  # toolchain_specs cache filenames instead.
  local spec var
  for l in $LANGS; do
    local IFS=';'
    for spec in $(toolchain_specs "$l" 2>/dev/null); do
      unset IFS
      var="${spec%%|*}"
      [ "$(toolchain_cache_name "$var")" = "$1" ] && return 0
      IFS=';'
    done
    unset IFS
  done
  return 1
}

# The conan wheel set is not a single upstream file: the Dockerfile expects
# cache/conan-wheels-<ver>.tar.gz, which we assemble from PyPI with `pip
# download` (pinned to the container's CPython 3.14 / manylinux). FETCH_CONAN=0
# skips it (e.g. when only building non-clang images).
CONAN_VERSION="${CONAN_VERSION:-2.32.0}"
CONAN_WHEELS="conan-wheels-${CONAN_VERSION}.tar.gz"
fetch_conan_wheels() {
  local dest="${CACHE}/${CONAN_WHEELS}" tmp
  [ -s "$dest" ] && { echo "skip  ${CONAN_WHEELS} ($(wc -c <"$dest") bytes)"; return 0; }
  echo "fetch ${CONAN_WHEELS} (pip download conan==${CONAN_VERSION})"
  tmp="$(mktemp -d)"
  if HTTPS_PROXY="${BUILD_PROXY}" HTTP_PROXY="${BUILD_PROXY}" \
       "${PYTHON:-python3}" -m pip download --only-binary=:all: --no-cache-dir \
         --python-version 3.14 --implementation cp --platform manylinux_2_28_x86_64 \
         --dest "${tmp}" "conan==${CONAN_VERSION}" >/dev/null; then
    tar -czf "${dest}.part" -C "${tmp}" .
    mv "${dest}.part" "$dest"
    echo "  -> $(wc -c <"$dest") bytes"
  else
    echo "  -> FAILED (pip download)"; rm -rf "${tmp}" "${dest}.part"; return 1
  fi
  rm -rf "${tmp}"
  return 0
}

mkdir -p "${CACHE}"
ok=0; skip=0; fail=0
# The conan wheel set is referenced by Dockerfile.clang; assemble it whenever
# clang is in scope (all languages, or an explicit `clang` argument).
if [ "${FETCH_CONAN:-auto}" != 0 ] && { [ -z "$LANGS" ] || wanted "${CONAN_WHEELS}"; }; then
  fetch_conan_wheels || fail=$((fail+1))
fi
for var in ${URL_VARS}; do
  url="${!var-}"
  [ -n "$url" ] || continue
  file="$(cache_name "$var")"
  dest="${CACHE}/${file}"
  wanted "$file" || continue
  if [ -s "$dest" ]; then
    echo "skip  ${file} ($(wc -c <"$dest") bytes)"
    skip=$((skip+1)); continue
  fi
  echo "fetch ${file}"
  if HTTPS_PROXY="${BUILD_PROXY}" HTTP_PROXY="${BUILD_PROXY}" \
     curl -fSL --retry 3 --retry-delay 2 -o "${dest}.part" "$url"; then
    mv "${dest}.part" "$dest"
    echo "  -> $(wc -c <"$dest") bytes"
    ok=$((ok+1))
  else
    rm -f "${dest}.part"; echo "  -> FAILED"; fail=$((fail+1))
  fi
done

# Verify every COPY cache/<file> referenced by the requested Dockerfiles exists.
missing=0
for l in ${LANGS:-base ${WORKSPACE_LANGS}}; do
  dfproto="$l"; [ "$l" = "go" ] && dfproto="golang"
  df="${DIR}/Dockerfile.${dfproto}"; [ -f "$df" ] || continue
  while read -r f; do
    [ -n "$f" ] || continue
    if [ ! -s "${CACHE}/${f}" ]; then
      echo "MISSING ${dfproto}: ${f}"; missing=1
    fi
  done < <(sed -n 's#^COPY cache/\([^ ]*\).*#\1#p' "$df")
done

echo "fetch-artifacts(${DISTRO}): $ok downloaded, $skip cached, $fail failed"
[ "$fail" -eq 0 ] && [ "$missing" -eq 0 ]
