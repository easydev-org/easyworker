#!/usr/bin/env bash
# Generate the on-demand toolchain index (schema 1) consumed by the worker's
# toolchain installer (internal/toolchains; contract:
# abc-protocol/deploy/DEVELOP.md -> "Toolchain index").
#
#   ./build-index.sh                 # print index.json to stdout
#   ./build-index.sh -o index.json   # write to a file
#   ./build-index.sh --publish       # PUT to the artifact generic mount
#
# The `url` of every artifact points at the artifact generic store
# (`$ARTIFACT/artifacts/generic/toolchains/<lang>/<version>/<file>`) — i.e. the
# files publish-artifacts.sh uploads. sha256 is computed from the LOCAL cache
# (`fetch-artifacts.sh` populates it) for as-is toolchains, and from the built
# relocatable tarball for build toolchains (run publish-artifacts.sh first).
#
# The tool list + per-language metadata live in toolchain-meta.sh (one place,
# shared with publish-artifacts.sh). Override ARTIFACT for a different base.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# config.sh reads NO_PROXY under `set -u`; default it so a bare environment works.
: "${NO_PROXY:=}"
# shellcheck source=config.sh
. "${HERE}/config.sh"
# shellcheck source=toolchain-meta.sh
. "${HERE}/toolchain-meta.sh"

DIR="${HERE}/toolchain/${DISTRO}"
CACHE="${CACHE_ROOT}/${DISTRO}"
ARTIFACT="${ARTIFACT:-http://artifact.worker.svc.cluster.local}"
BUILT_DIR="${HERE}/.publish"          # publish-artifacts.sh stages built tarballs here
OUT=""
PUBLISH=0
while [ $# -gt 0 ]; do
  case "$1" in
    -o) OUT="$2"; shift 2 ;;
    --publish) PUBLISH=1; shift ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

set -a
# shellcheck source=/dev/null
. "${DIR}/urls.env"
[ -f "${HERE}/toolchain/urls.local.env" ] && . "${HERE}/toolchain/urls.local.env"
set +a

# json_str: minimal JSON string escape (URLs/paths are plain ASCII here).
json_str() { printf '"%s"' "$(printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g')"; }

# artifact_url <lang> <version> <file>
#
# NOTE the FLAT shape: artifact's generic store addresses content as
# `/artifacts/generic/<name>/<version>/<filename>` — EXACTLY three segments (see
# easy-vcs/artifact generic/lib.go). A nested `toolchains/<lang>/<version>/<file>`
# is four segments and 404s. So the toolchain name IS `toolchains-<lang>`.
artifact_url() { printf '%s/artifacts/generic/toolchains-%s/%s/%s' "${ARTIFACT%/}" "$1" "$2" "$3"; }

# index_url -> the published index (name `toolchains`, version `index`).
index_url() { printf '%s/artifacts/generic/toolchains/index/index.json' "${ARTIFACT%/}"; }

# built_tarball_name <lang> -> the single file publish-artifacts.sh uploads.
built_tarball_name() { echo "toolchain.tar.gz"; }

# artifacts_json <lang> <version> <kind> -> a JSON array of artifact objects.
artifacts_json() {
  local lang="$1" ver="$2" kind="$3" out="" first=1 spec
  if [ "$kind" = "build" ]; then
    local file path sha
    file="$(built_tarball_name "$lang")"
    path="${BUILT_DIR}/${lang}/${ver}/${file}"
    if [ ! -s "$path" ]; then echo "missing built tarball $path (run ./publish-artifacts.sh $lang)" >&2; exit 1; fi
    sha="$(sha256sum "$path" | awk '{print $1}')"
    out="{\"url\":$(json_str "$(artifact_url "$lang" "$ver" "$file")"),\"sha256\":\"$sha\",\"format\":\"tar.gz\",\"strip\":0,\"bin\":$(json_str "$(toolchain_build_bin "$lang")")}"
  else
    local IFS=';'
    for spec in $(toolchain_specs "$lang"); do
      unset IFS
      local var="${spec%%|*}" rest="${spec#*|}"
      local format="${rest%%|*}" rest2="${rest#*|}"
      local strip="${rest2%%|*}" rest3="${rest2#*|}"
      local bin="${rest3%%|*}" rest4="${rest3#*|}"
      local rename="${rest4%%|*}" rest5="${rest4#*|}"
      [ "$rename" = "$rest4" ] && rename=""   # no rename field
      local os="${rest5%%|*}" arch="${rest5#*|}"
      [ "$os" = "$rest5" ] && os=""            # no os field
      [ "$arch" = "$rest5" ] && arch=""        # no arch field
      local url="${!var-}"
      [ -n "$url" ] || { echo "missing url var $var for $lang" >&2; exit 1; }
      local file; file="$(toolchain_cache_name "$var")"
      local path="${CACHE}/${file}"
      if [ ! -s "$path" ]; then echo "missing cache artifact $file for $lang (run ./fetch-artifacts.sh $lang)" >&2; exit 1; fi
      local sha; sha="$(sha256sum "$path" | awk '{print $1}')"
      local ren=""
      [ -n "$rename" ] && ren+=",\"rename\":$(json_str "$rename")"
      [ -n "$os" ] && ren+=",\"os\":$(json_str "$os")"
      [ -n "$arch" ] && ren+=",\"arch\":$(json_str "$arch")"
      [ "$first" -eq 1 ] || out+=","
      first=0
      out+="{\"url\":$(json_str "$(artifact_url "$lang" "$ver" "$file")"),\"sha256\":\"$sha\",\"format\":\"$format\",\"strip\":$strip,\"bin\":$(json_str "$bin")$ren}"
      IFS=';'
    done
    unset IFS
  fi
  printf '[%s]' "$out"
}

# json_argv <space-separated> -> ["a","b"] (empty -> "[]").
json_argv() {
  local s="$1"
  [ -n "$s" ] || { printf '[]'; return; }
  printf '[%s]' "$(printf '%s' "$s" | awk '{for(i=1;i<=NF;i++){printf "%s\"%s\"",(i>1?",":""),$i}}')"
}

# json_env <semicolon-separated NAME=VALUE> -> {"NAME":"VALUE",...}
json_env() {
  local s="$1" out="" first=1 kv
  [ -n "$s" ] || { printf '{}'; return; }
  local IFS=';'
  for kv in $s; do
    unset IFS
    [ -n "$kv" ] || { IFS=';'; continue; }
    local name="${kv%%=*}" val="${kv#*=}"
    [ "$first" -eq 1 ] || out+=","
    first=0
    out+="$(json_str "$name"):$(json_str "$val")"
    IFS=';'
  done
  unset IFS
  printf '{%s}' "$out"
}

# json_array <comma-separated> -> ["a","b"]
json_array() {
  local s="$1"
  [ -n "$s" ] || { printf '[]'; return; }
  printf '[%s]' "$(printf '%s' "$s" | awk -F, '{for(i=1;i<=NF;i++){printf "%s\"%s\"",(i>1?",":""),$i}}')"
}

emit() {
  local lang="$1" ver kind req arts inst env ud ip extra=""
  ver="$(toolchain_version "$lang")"
  kind="$(toolchain_kind "$lang")"
  req="$(toolchain_requires "$lang")"
  # artifacts_json exits non-zero (hard stop) when a required cache artifact is
  # missing; under `set -e` a failing assignment aborts the whole generation
  # rather than emitting a partial/invalid index.
  arts="$(artifacts_json "$lang" "$ver" "$kind")"
  inst="$(toolchain_install "$lang")"
  env="$(toolchain_env "$lang")"
  ud="$(toolchain_unpack_dir "$lang")"
  ip="$(toolchain_install_prefix "$lang")"
  # install is a SHELL command -> argv ["sh","-c",<cmd>] (so &&/pipes/quoting work).
  [ -n "$inst" ] && extra+=", \"install\": [\"sh\", \"-c\", $(json_str "$inst")]"
  [ -n "$env" ] && extra+=", \"env\": $(json_env "$env")"
  [ -n "$ud" ] && extra+=", \"unpack_dir\": $(json_str "$ud")"
  [ -n "$ip" ] && extra+=", \"install_prefix\": $(json_str "$ip")"
  printf '    %s: {"requires": %s, "versions": {"%s": {"artifacts": %s%s}}}\n' \
    "$(json_str "$lang")" "$(json_array "$req")" "$ver" "$arts" "$extra"
}

# specs_ok <lang>: known to the shared metadata (has a version).
specs_ok() { [ "$(toolchain_version "$1")" != "unknown" ]; }

generate() {
  echo "{"
  echo '  "schema": 1,'
  echo '  "toolchains": {'
  first=1
  # LANGS: an explicit subset (env LANGS=… or positional args) overrides the
  # published set, so a single toolchain can be regenerated.
  for lang in ${LANGS:-${PUBLISHED_LANGS}}; do
    specs_ok "$lang" || continue
    [ "$first" -eq 1 ] || echo ","
    first=0
    emit "$lang"
  done
  echo ""
  echo "  }"
  echo "}"
}

if [ "$PUBLISH" -eq 1 ]; then
  : "${ARTIFACT_TOKEN:?set ARTIFACT_TOKEN to publish}"
  tmp="$(mktemp)"
  generate > "$tmp"
  jq -e . "$tmp" >/dev/null || { echo "generated index is not valid JSON" >&2; exit 1; }
  curl -fsS -X PUT -H "Authorization: Bearer ${ARTIFACT_TOKEN}" \
    --data-binary "@${tmp}" "$(index_url)"
  rm -f "$tmp"
  echo "published $(index_url)"
elif [ -n "$OUT" ]; then
  generate > "$OUT"
else
  generate
fi
