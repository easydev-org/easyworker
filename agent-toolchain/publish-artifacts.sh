#!/usr/bin/env bash
# Publish the on-demand toolchain artifacts to the shared `artifact` generic
# store, then publish the index that points at them.
#
#   ./publish-artifacts.sh                 # as-is toolchains of PUBLISHED_LANGS
#   ./publish-artifacts.sh go node rust    # a subset
#   ./publish-artifacts.sh --build         # also build the source/install ones
#   ./publish-artifacts.sh --dry-run       # print actions, upload nothing
#
# Two kinds of toolchain (see toolchain-meta.sh):
#   as-is  the upstream cache artifact is a relocatable prebuilt -> uploaded
#          verbatim to .../toolchains/<lang>/<version>/<cache-filename>.
#   build  needs install.sh / a compile -> built ONCE here in a container with
#          the FINAL prefix (/opt/toolchains/<lang>/<version>) and shipped as
#          .../toolchains/<lang>/<version>/toolchain.tar.gz (root = version root).
#
# The index (`build-index.sh`) then points every url at these generic paths and
# records sha256. Uploads are idempotent (the store replaces the same filename).
#
# Reads are anonymous; WRITE needs ARTIFACT_TOKEN (env/secret, never committed).
#
# Owner decision: publish the FULL set, including restrictive-license tools
# (e.g. CUDA). Internal container-release-style distribution, not commercial
# redistribution — do not drop tools on license grounds.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
: "${NO_PROXY:=}"
# shellcheck source=config.sh
. "${HERE}/config.sh"
# shellcheck source=toolchain-meta.sh
. "${HERE}/toolchain-meta.sh"

DIR="${HERE}/toolchain/${DISTRO}"
CACHE="${CACHE_ROOT}/${DISTRO}"
ARTIFACT="${ARTIFACT:-http://artifact.worker.svc.cluster.local}"
STAGE="${HERE}/.publish"
ENGINE="${CONTAINER_ENGINE:-docker}"

DO_BUILD=0
DRY=0
FORCE=0
LANGS=""
while [ $# -gt 0 ]; do
  case "$1" in
    --build) DO_BUILD=1; shift ;;
    --dry-run) DRY=1; shift ;;
    --force) FORCE=1; shift ;;
    --) shift; LANGS="$LANGS $*"; break ;;
    -*) echo "unknown arg: $1" >&2; exit 2 ;;
    *) LANGS="$LANGS $1"; shift ;;
  esac
done
LANGS="${LANGS:-${PUBLISHED_LANGS}}"

set -a
# shellcheck source=/dev/null
. "${DIR}/urls.env"
[ -f "${HERE}/toolchain/urls.local.env" ] && . "${HERE}/toolchain/urls.local.env"
set +a

put() { # local-file remote-relative-path
  local file="$1" name="$2" ver="$3" fname="$4"
  # Flat generic shape: /artifacts/generic/<name>/<version>/<filename> (EXACTLY
  # 3 segments; nested toolchains/<lang>/<version>/<file> 404s — see
  # easy-vcs/artifact generic/lib.go). So name = toolchains-<lang>.
  local url="${ARTIFACT%/}/artifacts/generic/${name}/${ver}/${fname}"
  if [ "$DRY" -eq 1 ]; then echo "  [dry-run] PUT ${url} <- ${file}"; return 0; fi
  # Stream the body (`-T`), never `--data-binary @file`: the latter buffers the
  # WHOLE file in memory, which OOM-killed the publisher on the ~2 GB flutter
  # SDKs. `-T` streams from disk with a fixed memory footprint.
  curl -fsS -X PUT -H "Authorization: Bearer ${ARTIFACT_TOKEN}" \
    -T "${file}" "${url}" >/dev/null
  echo "  PUT ${name}/${ver}/${fname}"
}

require_token() {
  [ "$DRY" -eq 1 ] && return 0
  : "${ARTIFACT_TOKEN:?set ARTIFACT_TOKEN (write token) to publish}"
}

# ---- as-is toolchains --------------------------------------------------------
# Some as-is upstreams need shared libs their Dockerfile apt-installs (swift).
# The publisher apt-installs `toolchain_deps` in its OWN build container first.
install_deps() { # lang
  local pkgs; pkgs="$(toolchain_deps "$1")"
  [ -n "$pkgs" ] || return 0
  if [ "$DRY" -eq 1 ]; then echo "  [dry-run] apt-get install $pkgs"; return 0; fi
  echo "  deps: apt-get install $pkgs"
  export DEBIAN_FRONTEND=noninteractive
  apt-get -o Acquire::Retries=5 update >/dev/null 2>&1
  apt-get -o Acquire::Retries=5 install -y --no-install-recommends $pkgs >/dev/null 2>&1
}

publish_as_is() { # lang
  local lang="$1" ver spec
  install_deps "$lang"
  ver="$(toolchain_version "$lang")"
  local IFS=';'
  for spec in $(toolchain_specs "$lang"); do
    unset IFS
    local var="${spec%%|*}"
    local file; file="$(toolchain_cache_name "$var")"
    local path="${CACHE}/${file}"
    if [ ! -s "$path" ]; then
      echo "  WARN ${lang}: missing cache artifact ${file} — run ./fetch-artifacts.sh ${lang} (index build will fail without it)" >&2
      IFS=';'; continue
    fi
    put "$path" "toolchains-${lang}" "$ver" "$file"
    IFS=';'
  done
  unset IFS
}

# ---- build toolchains (PHASE 2) ----------------------------------------------
# Recipe: run inside a container FROM toolchain-base, with the version root bind-
# mounted at its FINAL path ($PREFIX), the artifact cache read-only at /cache,
# and the build proxy exported. It must populate $PREFIX (bin/ etc.).
#
# These recipes mirror the corresponding Dockerfiles' install logic. They are
# only exercised with --build; validate a new/changed recipe against its
# Dockerfile.<lang> (same apt deps, same flags) before trusting it.
build_recipe() { # lang -> container bash script on stdout
  case "$1" in
    rust) cat <<'EOF'
set -euxo pipefail
mkdir -p /tmp/d
tar -xzf /cache/rust-*-x86_64-unknown-linux-gnu.tar.gz -C /tmp/d --strip-components=1
/tmp/d/install.sh --prefix="$PREFIX" --without=rust-docs --disable-ldconfig
EOF
      ;;
    lua) cat <<'EOF'
set -euxo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update && apt-get install -y --no-install-recommends libreadline-dev
mkdir -p /tmp/lua && tar -xzf /cache/lua-*.tar.gz -C /tmp/lua --strip-components=1
(cd /tmp/lua && make linux MYCFLAGS=-fPIC && make install INSTALL_TOP="$PREFIX")
mkdir -p /tmp/lr && tar -xzf /cache/luarocks-*.tar.gz -C /tmp/lr --strip-components=1
(cd /tmp/lr && ./configure --prefix="$PREFIX" --with-lua="$PREFIX" --lua-version=5.5 && make && make install)
EOF
      ;;
    r) cat <<'EOF'
set -euxo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends gfortran libreadline-dev libblas-dev liblapack-dev \
  libpcre2-dev libcurl4-openssl-dev libdeflate-dev libbz2-dev liblzma-dev zlib1g-dev libpng-dev \
  libjpeg-dev libtiff-dev libicu-dev libx11-dev libxt-dev libcairo2-dev texinfo
mkdir -p /tmp/R && tar -xzf /cache/R-*.tar.gz -C /tmp/R --strip-components=1
(cd /tmp/R && ./configure --prefix="$PREFIX" --enable-R-shlib --with-blas --with-lapack --with-x=no \
  && make -j"$(nproc)" && make install)
EOF
      ;;
    clojure) cat <<'EOF'
set -euxo pipefail
mkdir -p /tmp/c && tar -xzf /cache/clojure-tools-*.tar.gz -C /tmp/c --strip-components=1
cd /tmp/c
install -d "$PREFIX/libexec" "$PREFIX/bin"
cp ./*.jar "$PREFIX/libexec/"
cp deps.edn example-deps.edn tools.edn "$PREFIX"/
sed -e "s#PREFIX#$PREFIX#g" clojure > "$PREFIX/bin/clojure"
sed -e "s#BINDIR#$PREFIX/bin#g" clj > "$PREFIX/bin/clj"
chmod 755 "$PREFIX/bin/clojure" "$PREFIX/bin/clj"
EOF
      ;;
    elixir) cat <<'EOF'
set -euxo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends build-essential autoconf m4 libncurses-dev libssl-dev unzip
mkdir -p /tmp/otp && tar -xzf /cache/OTP-*.tar.gz -C /tmp/otp --strip-components=1
(cd /tmp/otp && ./configure --prefix="$PREFIX" --without-javac --without-odbc --without-wx --without-debugger \
  && make -j"$(nproc)" && make install)
unzip -q /cache/v*-otp-*.zip -d "$PREFIX"
install -d "$PREFIX/lib/elixir/lib/hex"
unzip -q /cache/hex-*.ez -d "$PREFIX/lib/elixir/lib/hex"
cp /cache/registry-public-key.pem "$PREFIX/lib/elixir/lib/hex/" 2>/dev/null || true
EOF
      ;;
    *) return 1 ;;
  esac
}

publish_build() { # lang
  local lang="$1" ver recipe
  ver="$(toolchain_version "$lang")"
  local out="${STAGE}/${lang}/${ver}"
  local root="${out}/root"
  local tarball="${out}/toolchain.tar.gz"
  local prefix="/opt/toolchains/${lang}/${ver}"
  if [ -s "$tarball" ] && [ "$FORCE" -ne 1 ]; then
    echo "  built tarball exists: ${tarball} (use --force to rebuild)"
  else
    recipe="$(build_recipe "$lang")" || { echo "  no build recipe for ${lang}" >&2; return 1; }
    if [ "$DRY" -eq 1 ]; then echo "  [dry-run] build ${lang}@${ver} in ${ENGINE} (prefix ${prefix})"; else
      command -v "$ENGINE" >/dev/null || { echo "  ${ENGINE} not found (set CONTAINER_ENGINE)" >&2; return 1; }
      local base="${REGISTRY}/${NAMESPACE}/${TOOLCHAIN_REPO}-base:${TOOLCHAIN_TAG}"
      rm -rf "$out"; mkdir -p "$root"
      echo "  build ${lang}@${ver} from ${base} (prefix ${prefix})"
      "$ENGINE" run --rm \
        -e PREFIX="$prefix" \
        -e HTTP_PROXY="${BUILD_PROXY}" -e HTTPS_PROXY="${BUILD_PROXY}" \
        -e http_proxy="${BUILD_PROXY}" -e https_proxy="${BUILD_PROXY}" \
        -e NO_PROXY="${NO_PROXY}" -e no_proxy="${NO_PROXY}" \
        -v "${CACHE}":/cache:ro \
        -v "${root}:${prefix}" \
        -w / \
        --entrypoint /bin/bash \
        "$base" -c "$recipe"
      tar -C "$root" -czf "$tarball" .
      rm -rf "$root"
      echo "  staged ${tarball} ($(wc -c <"$tarball") bytes)"
    fi
  fi
  put "$tarball" "toolchains-${lang}" "$ver" "toolchain.tar.gz"
}

# ---- main --------------------------------------------------------------------
require_token
echo "Publishing toolchains to ${ARTIFACT%/}/artifacts/generic/toolchains/"
for lang in ${LANGS}; do
  ver="$(toolchain_version "$lang")"
  if [ "$ver" = "unknown" ]; then echo "skip ${lang}: not in toolchain-meta"; continue; fi
  kind="$(toolchain_kind "$lang")"
  echo "== ${lang}@${ver} (${kind}) =="
  if [ "$kind" = "build" ]; then
    if [ "$DO_BUILD" -eq 1 ]; then publish_build "$lang"; else echo "  build kind: pass --build to build+publish"; fi
  else
    publish_as_is "$lang"
  fi
done

echo "Publishing index…"
if [ "$DRY" -eq 1 ]; then
  echo "  [dry-run] build-index.sh --publish"
else
  export ARTIFACT
  "${HERE}/build-index.sh" --publish
fi
echo "Done."
