#!/usr/bin/env bash
# Build the AGENT-TOOLCHAIN generic dev images and push them to the registry
# under the `agent-toolchain` namespace. No agent-worker, no CA — plain dev
# images (sandboxes run a sandbox-<lang> image built FROM these by
# sandbox-images/build.sh).
#
#   DISTRO=debian-trixie ./build-toolchain.sh node
#   ./build-toolchain.sh            # base + every WORKSPACE_LANGS entry
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=config.sh
. "${HERE}/config.sh"

DIR="${HERE}/toolchain/${DISTRO}"
[ -d "$DIR" ] || { echo "no toolchain dir for DISTRO=${DISTRO}: ${DIR}"; exit 1; }

LANGS="${*:-base ${WORKSPACE_LANGS}}"
TOOLCHAIN_BASE_REF="${REGISTRY}/${NAMESPACE}/${TOOLCHAIN_REPO}-base:${TOOLCHAIN_TAG}"

build() { # proto
  local proto="$1" name df
  if [ "$proto" = "base" ]; then
    name="${TOOLCHAIN_REPO}-base"
    df="${DIR}/base.Dockerfile"
    build_image "$name" "$df" "$TOOLCHAIN_TAG" "$DISTRO_BASE"
    return
  fi
  local dfproto="$proto"; [ "$proto" = "go" ] && dfproto="golang"
  name="${TOOLCHAIN_REPO}-${proto}"
  df="${DIR}/Dockerfile.${dfproto}"
  [ -f "$df" ] || { echo "no Dockerfile for ${proto} in ${DISTRO}: ${df}"; exit 1; }
  # A `<lang>.base` overrides the parent image:
  #   * `@distro`            -> the raw distro base (e.g. debian:trixie-slim)
  #   * contains ':' or '/'  -> an explicit image ref, used verbatim
  #   * otherwise            -> a sibling toolchain (kotlin/scala -> java25)
  local parent="$TOOLCHAIN_BASE_REF"
  if [ -f "${DIR}/${dfproto}.base" ]; then
    local p; p="$(grep -vE '^[[:space:]]*(#|$)' "${DIR}/${dfproto}.base" | head -1)"
    [ -n "$p" ] || { echo "empty parent in ${DIR}/${dfproto}.base"; exit 1; }
    case "$p" in
      @distro)          parent="$DISTRO_BASE" ;;
      *:*|*/*)          parent="$p" ;;
      *)                parent="${REGISTRY}/${NAMESPACE}/${TOOLCHAIN_REPO}-${p}:${TOOLCHAIN_TAG}" ;;
    esac
  fi
  build_image "$name" "$df" "$TOOLCHAIN_TAG" "$parent"
}

# A `<lang>.base` may name a sibling toolchain as parent (kotlin/scala/clojure/
# groovy -> java25, gleam -> elixir, ...). If that parent is not already in the
# registry, build it FIRST by prepending it (iterating, so a parent's own
# parent is handled too). When the parent already exists, the child's build just
# pulls it — rebuilding it would also re-hit upstream pins unnecessarily.
sibling_parent() { # lang -> parent lang, or empty
  local dfproto="$1"; [ "$dfproto" = "go" ] && dfproto="golang"
  local f="${DIR}/${dfproto}.base"
  [ -f "$f" ] || return 0
  local p; p="$(grep -vE '^[[:space:]]*(#|$)' "$f" | head -1)"
  case "$p" in ''|@distro|*:*|*/*) return 0 ;; *) echo "$p" ;; esac
}

registry_has() { # lang -> 0 if <repo>-<lang>:<tag> is already pushed
  local dfproto="$1"; [ "$dfproto" = "go" ] && dfproto="golang"
  local ref="http://${REGISTRY}/v2/${NAMESPACE}/${TOOLCHAIN_REPO}-${dfproto}/manifests/${TOOLCHAIN_TAG}"
  local code
  code="$(curl -s -o /dev/null -w '%{http_code}' \
    -H 'Accept: application/vnd.oci.image.manifest.v1+json,application/vnd.docker.distribution.manifest.v2+json' \
    -u "${FORGEJO_USER:-root}:${FORGEJO_PASS:-devpassword}" "$ref" 2>/dev/null || true)"
  [ "$code" = "200" ]
}

changed=1
while [ "$changed" = 1 ]; do
  changed=0
  for l in ${LANGS}; do
    p="$(sibling_parent "$l")"; [ -n "$p" ] || continue
    registry_has "$p" && continue
    case " ${LANGS} " in
      *" ${p} "*) ;;
      *) LANGS="${p} ${LANGS}"; changed=1 ;;
    esac
  done
done

for l in ${LANGS}; do build "$l"; done
