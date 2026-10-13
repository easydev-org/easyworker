# agent-toolchain

The **`agent-toolchain`** image catalog: generic, deployment-neutral language
dev images pushed to the in-cluster registry under the `agent-toolchain`
namespace. The workspace gateway's `list-oci-images` default browses exactly
this namespace, and any of these images can be used as a **sandbox base** (the
worker-bundled sandbox images in `../sandbox-images/` are built FROM them) or
referenced from `service-deploy`.

This tree was moved here from `workspace-gateway/images/` so the worker repo
owns the whole execution-image story (worker binary + the images sandboxes run
on). It is a curated subset of agent-worker's stage-1 toolchain build
(the upstream EasyLab worker images tree), with two deliberate differences:

- Registry/namespace default to the shared catalog
  (`git.agent.svc.cluster.local/agent-toolchain`) with NATIVE `<lang>:<distro>`
  tags.
- **No egress MITM CA and no worker binary are baked in** — these are plain dev
  images; `../sandbox-images/build.sh` bakes agent-worker in to form a sandbox.

## Layout

```
config.sh                 shared knobs (registry/namespace/distro/cache) + build_image()
build-toolchain.sh        build one language image (or `base`, or all)
build-all.sh              build base + every WORKSPACE_LANGS entry, one at a time
build-one.sh              detached single-image build with a log file
build-index.sh            generate/publish the on-demand toolchain index (schema 1)
publish-artifacts.sh      upload toolchain artifacts to the artifact generic store
toolchain-meta.sh         shared tool list + per-language unpack metadata (sourced)
mirror-images.sh          mirror pinned middleware images (middleware.env)
middleware.Dockerfile     no-op FROM re-serve used by mirror-images.sh
middleware.env            pinned shared middleware image refs
toolchain/debian-trixie/  per-language Dockerfiles (+ base.Dockerfile, urls.env)
cache/debian-trixie/      pre-downloaded upstream artifacts (GITIGNORED, multi-GB)
vm/macos/                 macOS (Sequoia) VM sandbox image + build/repack + guest/
vm/windows/               Windows 11 VM sandbox image + build/repack + guest/
android/                  Android emulator sandbox image (run-only) + screen bridge
desktop/                  graphical desktop sandbox (openbox=pure X11 / labwc=pure Wayland)
```

## VM / Android / Desktop sandboxes

`vm/`, `android/` and `desktop/` hold the non-linux sandbox images. They push to
the **`sandbox` org** (like `sandbox-images/`), so the gateway's `CreateSandbox`
can run them; `CreateSandbox(kvm=true)` supplies `/dev/kvm` + `/dev/net/tun` +
the cap/unconfined security context that the VM and Android images need.

| dir | image | variants | base |
|---|---|---|---|
| `vm/macos/`   | `sandbox/sandbox-macos`   | `base`, `xcode`    | macOS 15 Sequoia |
| `vm/windows/` | `sandbox/sandbox-windows` | `base`, `devtools` | Windows 11 (devtools = MSVC + Windows SDK + .NET) |
| `android/`    | `sandbox/sandbox-android` | `aosp`, `gms`      | official Android emulator |
| `desktop/`    | `sandbox/sandbox-desktop` | `openbox`, `labwc` | toolchain-base; X11 or Wayland desktop, screen streamed by the worker |

```sh
scripts/build-all.sh                      # dist/agent-worker-* (all platforms)
./agent-toolchain/vm/macos/build.sh base
./agent-toolchain/vm/macos/build.sh xcode
./agent-toolchain/vm/windows/build.sh base
./agent-toolchain/vm/windows/build.sh devtools
./agent-toolchain/android/build.sh aosp
./agent-toolchain/android/build.sh gms
./agent-toolchain/desktop/build.sh openbox
./agent-toolchain/desktop/build.sh labwc
```

The VM runtime is a pre-baked guest disk wrapped in a self-owned runtime
(generic `qemux/qemu:750` + vendored boot scripts). The worker binary is
**boot-fetched** by the guest from the container's nginx
(`http://host.lan:8090/worker`) on every start, so a worker upgrade is an image
rebuild. Guest disks and boot support files are staged (git-ignored) under each
variant's `disk/` (macOS) or `disk/` + `disk-support/` (Windows); see the
per-tree `README.md`. The Android and Desktop entrypoints start agent-worker
**first** (the gateway waits only 60s for `:48080`); the emulator / screen stack
comes up in the background.


## Build

```sh
# one image (base + one language), from the repo root or this dir:
DISTRO=debian-trixie ./build-toolchain.sh node
./build-toolchain.sh clang          # conan + clang + libc++ + llvm

# everything, one at a time, each to /tmp/wsbuild/<name>.log:
setsid bash build-all.sh > /tmp/wsbuild/all.log 2>&1 < /dev/null &
tail -f /tmp/wsbuild/all.log

# a single image, detached:
./build-one.sh toolchain node
```

The language set is `WORKSPACE_LANGS` in `config.sh`:
`node python go rust java java25 kotlin scala clojure groovy dart dotnet elixir
gleam php ruby swift zig clang bun deno julia crystal ocaml haskell lua perl r
conda pixi godot cuda torch vllm vllm-omni llamacpp comfyui`. The last six are
the ML/GPU **dev** images (`.base`-chained: cuda → torch → {vllm, comfyui} →
vllm-omni, and cuda → llamacpp); see `../AGENTS.md` and `toolchain/VERSIONS.md`.

## Middleware

`middleware.env` pins the shared middleware images (postgres / redis / mariadb /
nats) that every tenant sees via `list-oci-images`. Bump a version there, then:

```sh
./mirror-images.sh            # mirror every entry
./mirror-images.sh postgres   # just one
```

## On-demand toolchains: publish + index

The on-demand installer's tool list + per-language unpack metadata live in
**`toolchain-meta.sh`** (one place, sourced by both scripts below). The versions
are pinned by `toolchain/<distro>/urls.env` (the same source the image builds
use). `PUBLISHED_LANGS` is the installable set; a language not in it is absent
from the index.

```sh
./fetch-artifacts.sh                     # populate cache/<distro>/ (sha256 source)
ARTIFACT_TOKEN=<token> ./publish-artifacts.sh   # upload artifacts + publish index
./build-index.sh                         # print index.json to stdout
./build-index.sh -o index.json           # write to a file
ARTIFACT_TOKEN=<token> ./build-index.sh --publish
```

`publish-artifacts.sh` uploads each `(lang, version)` to the shared artifact
generic store (idempotent). As-is toolchains are uploaded verbatim; `--build`
handles the install.sh/compile kinds (phase 2). `build-index.sh` then emits the
index whose `url`s point at those paths, with sha256 from the local cache.
Schema + consumer contract: `abc-protocol/deploy/DEVELOP.md` → "Toolchain
index"; implementation: `internal/toolchains`.

**Layout**: artifact's generic store is EXACTLY three path segments
(`/artifacts/generic/<name>/<version>/<filename>`), so the layout is FLAT —
`toolchains-<lang>/<ver>/<file>` for a toolchain, `toolchains/index/index.json`
for the index. (A nested `toolchains/<lang>/<version>/<file>` is four segments
and 404s.)

strip/bin/rename mirror each `Dockerfile.<lang>`'s unpack — keep them in sync
when a Dockerfile's unpack changes.

## Notes

- `cache/` holds the pre-downloaded upstream tarballs (`fetch-artifacts.sh` is
  the upstream helper; the artifacts are already present here). It is
  gitignored and rebuilt on demand — a build without a needed artifact fails
  loudly with the missing filename.
- **No runtime package mirrors are baked in** (no `PUB_HOSTED_URL`, SwiftPM
  `mirrors.json`, Gradle `init.gradle`, `git insteadOf`, npm/pip index). The
  images are generic/portable (also mirrored to ghcr); a mirror endpoint is a
  per-deployment choice and belongs at the job/agent level, not the image. See
  `../AGENTS.md` → "Runtime package mirrors are deliberately NOT baked in".
- **Packages come from the in-cluster `artifact` registry** (private modules
  are not on any public registry): see `../AGENTS.md` → "Packages via artifact"
  and `easy-vcs/deploy:PUBLISHING.md` for the per-ecosystem commands.
- `clang` builds from the raw distro base (it deliberately ships WITHOUT gcc);
  kotlin/scala extend `toolchain-java25`.
