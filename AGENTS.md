# AGENTS.md — abcp-sdk/worker

Working notes for agents editing this repo. This repo is the **single source of
truth** for the `agent-worker` binary and every sandbox image a sandbox runs.
See `README.md` for the fork's RPC/behavior deltas; this file covers the build.

Deployment manifests are NOT here: the standalone `agent-worker` Deployments
(`worker-k8s/`) and the platform charts live in **`abc-protocol/deploy`**. This
repo still owns the images (`Dockerfile`, `build-image.sh`, `agent-toolchain/`,
`sandbox-images/`).

## Two image trees (do not confuse them)

| tree | script | produces | used by |
|---|---|---|---|
| `agent-toolchain/` | `build-toolchain.sh <lang>` | `<reg>/agent-toolchain/toolchain-<lang>:debian-trixie` | generic dev images; sandbox **bases**; `list-oci-images` default |
| `sandbox-images/` | `build.sh [langs]` | `<reg>/sandbox/sandbox-<lang>:debian-trixie` | the ONLY images a sandbox may run |

`agent-toolchain/` = plain distro + one toolchain, **no worker, no CA**.
`sandbox-images/` = a toolchain base + `cmd/agent-worker` baked in. The gateway
no longer injects the worker at launch, so **any worker change requires
rebuilding the `sandbox-*` images** (not just the toolchain ones).

Non-linux images live under `agent-toolchain/{vm,android,desktop}/` and are
run-only (not sandbox bases).

## Image naming (unified)

Every image is `<org>/<role>-<subject>:<tag>`. **All images a sandbox may run
live in the `sandbox` org** — the gateway's `CreateSandbox` refuses any other
org.

| org | role | subject | tag | example |
|---|---|---|---|---|
| `agent-toolchain` | `toolchain` | language | `debian-trixie` | `agent-toolchain/toolchain-node:debian-trixie` |
| `sandbox` | `sandbox` | base | `debian-trixie` | `sandbox/sandbox-base:debian-trixie` |
| `sandbox` | `sandbox` | OS | variant | `sandbox/sandbox-windows:base` |

LANGUAGE sandboxes were removed: their toolchains are installed on demand
(`WORKSPACE_TOOLCHAINS` → `/opt/toolchains`), so the only language-ish sandbox
left is `sandbox-base`. OS sandboxes: `sandbox-{macos,windows,android,desktop}`
with bare variant tags (`base`/`aosp`/`gms`/`openbox`/`labwc`). The macOS/Windows
VM repos keep a SINGLE `base` image — the variant (`xcode`/`devtools`) is chosen
at run time by `GOLDEN_DISK_URL`, not by the tag. `sandbox-cuda` stays for the
GPU toolchain. The repo's git tag (`v0.1.0`) carries the release.

### ML / GPU dev images

`cuda torch vllm vllm-omni llamacpp comfyui` are **dev images** (packages
installed so a job can build/run ML code; they are not meant to serve). They
chain off each other via `.base` files:

```
toolchain-cuda            (CUDA 13.4 toolkit + cuDNN 9 + CPython 3.13 + uv)
├── toolchain-torch       (torch 2.13.0 / vision 0.28.0 / audio 2.11.0 + HF stack)
│   ├── toolchain-vllm        (vllm 0.28.0)
│   │   └── toolchain-vllm-omni   (vllm-omni 0.28.0)
│   └── toolchain-comfyui     (ComfyUI v0.37.2 from git + its requirements)
└── toolchain-llamacpp    (llama.cpp b11182 CUDA build + llama-cpp-python 0.3.35)
```

Three things to know:

- **Python is 3.13, not the distro's 3.14.** vLLM-Omni pins `requires-python
  <3.14`; the whole ML stack lags the bleeding edge. `toolchain-python` (the
  separate language image) stays on 3.14.
- **CUDA runtime comes from pip, not the image's apt toolkit.** `pip install
  torch` pulls the `nvidia-*-cu13` wheels (~1.3 GiB); the apt toolkit only
  provides `nvcc`/headers for compiling. The **host driver** (`/dev/nvidia*`,
  `libcuda.so.1`) is never baked in — it is injected at runtime by the device
  plugin + `nvidia` RuntimeClass.
- **CUDA stubs are a lowest-priority ld path.** CUDA ships stub driver libs
  (SONAME `libcuda.so.1`) so a driverless box can still link/dlopen GPU code;
  `/etc/ld.so.conf.d/99-cuda-stubs.conf` registers them *after* the NVIDIA
  runtime's `00-nvcr-*.conf`, so a real driver wins when one is present. That is
  why `import torch` / `import llama_cpp` succeed with no GPU. `llama.cpp`
  prints "CUDA driver is a stub library" on such a box — expected.

To run an ML sandbox on a GPU box, call `CreateSandbox` with `gpu_count>=1`
(the gateway then sets the `nvidia` RuntimeClass + `nvidia.com/gpu` limit).


### OS sandbox call parameters (CreateSandbox)

The gateway exposes `kvm` and `cpu`/`memory`; the OS images need them:

| image | kvm | cpu/memory | computer-use | notes |
|---|---|---|---|---|
| `sandbox/sandbox-x11` | no | default fine | **yes** (a11y + AT-SPI2, X11) | Xvfb + openbox; screen streamed by the worker; worker starts first |
| `sandbox/sandbox-wayland` | no | default fine | **yes** (a11y + AT-SPI2 over D-Bus) | labwc (headless); screen streamed by the worker; input via `wtype`, screenshots via `grim` |
| `sandbox/sandbox-android:{aosp,gms}` | **yes** | ≥4 / ≥8Gi | adb/uiautomator | worker starts first; emulator boots in the background, so jobs must `adb wait-for-device` |
| `sandbox/sandbox-windows:base` | **yes** | 8 / 32Gi | **yes** (UIA, boot-fetched) | single `base` image; variant via `GOLDEN_DISK_URL` (`data.qcow2` / `data-devtools.qcow2`) |
| `sandbox/sandbox-macos:base` | **yes** | 4 / 16Gi | **yes** (AXUIElement, boot-fetched) | single `base` image; variant via `GOLDEN_DISK_URL` (`data.qcow2` / `data-xcode.qcow2`) |

Both GUI sandboxes carry a11y on Linux (`sandbox-x11` via X11/AT-SPI, `sandbox-wayland`
via D-Bus/AT-SPI). The `sandbox-base` image does not. Windows/macOS boot-fetch
`xa11y` from the image's nginx `:8090`, so every tag carries computer-use.

The gateway's `CreateSandbox` waits only **60s** for `:48080`. The linux/desktop
images are ready in seconds; the VM/Android sandboxes boot a guest first, so the
call may return `DeadlineExceeded` while the sandbox is still coming up (it is
left in place and becomes ready on its own).

## Prerequisites on the host

- `buildctl` (moby/buildkit 0.32.x), `skopeo`, `curl`, go 1.26+, `qemu-img`.
- In-cluster services reachable: `buildkitd.agent.svc.cluster.local:1234`,
  `git.agent.svc.cluster.local` (registry, `root:devpassword`),
  `mihomo.develop.svc.cluster.local:7890` (HTTP proxy for upstream fetches).
- Registry + buildkitd MUST be in `NO_PROXY` (config.sh forces this).

## Build order

```sh
scripts/build-all.sh                       # 1. dist/ cross-compiled binaries (18 files)
scripts/build-xa11y.sh fetch               #    dist/xa11y-* (computer-use CLI)
./agent-toolchain/fetch-artifacts.sh       # 2. cache/<distro>/* toolchain tarballs
./agent-toolchain/build-toolchain.sh base   # 3. toolchain images (base first!)
./agent-toolchain/build-toolchain.sh node python ...   #    then languages
./sandbox-images/build.sh base node python ...         # 4. bake worker into them
./agent-toolchain/mirror-images.sh          # (optional) middleware images
```

`dist/` and `agent-toolchain/cache/` are **gitignored and NOT committed** — a
fresh checkout has neither. A build without the needed cache artifact fails
loudly with the missing filename (that is by design).

`dist/` binaries are published as **GitHub Releases** (never committed to git):
`v0.1.0` holds the six `agent-worker-*` binaries; `xa11y-v0.15.0` holds the
three `xa11y-*` CLI binaries. A fresh checkout repopulates the worker binaries
via `scripts/build-all.sh` and the xa11y ones via
`scripts/build-xa11y.sh fetch` (or builds them from source — see that script).

VM / Android / Desktop (separate, need `dist/` staged first). All four push to
the `sandbox` org (`NAMESPACE=sandbox` default):

```sh
./agent-toolchain/vm/macos/build.sh   base|xcode
./agent-toolchain/vm/windows/build.sh base|devtools
./agent-toolchain/android/build.sh    aosp|gms
./agent-toolchain/desktop/build.sh    openbox|labwc
```

The VM goldens (`<variant>/data.qcow2` + `disk-support/`) are git-ignored and
staged outside the repo; without them only a retag of the published image is
possible, not a rebuild.

## Runtime package mirrors are deliberately NOT baked in

The toolchain/sandbox images ship **no runtime package-manager mirror config**:
no `PUB_HOSTED_URL`, no `~/.swiftpm/configuration/mirrors.json`, no
`~/.gradle/init.gradle`, no `git url.*.insteadOf`, no npm/pip/cargo index
override. Two reasons:

- **Portability.** These images are generic dev images and are also mirrored to
  ghcr (`scripts/distribute-ghcr.sh`) and run outside this cluster; baking a
  cluster-internal host (`*.svc.cluster.local`) into them would leak into every
  consumer and break off-cluster use.
- **Environment coupling.** A mirror endpoint is a per-deployment choice, not a
  property of the language toolchain. `fetch-artifacts.sh` /
  `build-toolchain.sh` use mihomo only at **build** time; runtime downloads are
  the job's business.

If a deployment wants runtime downloads to go through a shared artifact mirror
(e.g. `artifact.worker.svc.cluster.local/artifacts/{git,maven,pub}`), inject it
at the **job/agent level**, not into the image. The worker inherits its own
process environment into every job, so env-based knobs (`PUB_HOSTED_URL`, a
`GIT_CONFIG_GLOBAL`/`GRADLE_USER_HOME` pointing at a mounted config, …) set on
the sandbox/service or the agent's skill layer reach the job. The mirror
endpoints and the per-language config snippets live in the `easy-vcs/deploy`
artifact service docs — do not copy them into this repo.

## Packages via artifact (not GitHub / public registries)

The platform's private package registry is **`artifact`** (Service
`artifact.worker.svc.cluster.local`, `worker` namespace; plain HTTP, anonymous
**pull**, write needs a token). Internal modules are not on any public
registry. The authoritative guide — publish + consume for every ecosystem,
endpoint, auth — is **`easy-vcs/deploy:PUBLISHING.md`**. This repo is Go, so
the commands that matter here:

```sh
A=http://artifact.worker.svc.cluster.local

# consume (Go module deps) — a sandbox with no proxy env otherwise times out
# against proxy.golang.org (dial tcp ...: i/o timeout) when running `go build`.
export GOPROXY=$A/artifacts/go GOSUMDB=off

# publish a Go module
curl -X PUT --data-binary @<zip> "$A/artifacts/go/upload?name=<module>&version=vX.Y.Z"
```

**Build-time** fetches (this repo's `agent-toolchain/` images) reach upstream
through buildkitd + mihomo today (`fetch-artifacts.sh`, `config.sh`); to route
them through artifact instead, point the tools at the artifact mounts (npm
`registry=$A/artifacts/npm/`, pip `--index-url $A/artifacts/pypi/simple/`, apt
`$A/artifacts/debian/...`). Not required for the current builds. This is a
**build-time** source choice — distinct from the runtime-mirror decision above
(which is still: not baked into the images).

## Registry / naming

- `REGISTRY=git.agent.svc.cluster.local`. Every sandbox-runnable image is under
  `NAMESPACE=sandbox`; generic toolchain bases are under `agent-toolchain`.
- Tags: lang sandbox images use `debian-trixie`; OS sandboxes use the bare
  variant. All names are `<role>-<subject>` — see the naming table above.
- `scripts/retag.sh <src-repo> <src-tag> <dst-repo> <dst-tag>` re-tags within
  the registry via cross-repo blob mount (no bytes re-uploaded) — used to move
  the catalog to the unified names without rebuilding the multi-GB VM disks.
- ghcr mirror (optional, user-owned): `ghcr.io/silvermelon233`, mirrored by
  `scripts/distribute-ghcr.sh` (`GH_PASS=<pat> ./scripts/distribute-ghcr.sh`).
  Every image is tagged `v0.1.0`; multi-variant repos keep the canonical variant
  under the plain name and others under a suffixed name (`sandbox-android-gms`).
  The GUI desktops are separate images (`sandbox-x11`, `sandbox-wayland`), and
  the macOS/Windows VM repos keep a SINGLE `base` image (the variant is chosen at
  run time by `GOLDEN_DISK_URL`).

## On-demand toolchains (`internal/toolchains`)

Instead of one image per language, a sandbox can declare toolchains at runtime
and the worker installs them into `$WORKER_TOOLCHAIN_ROOT` (default
`/opt/toolchains`):

- `WORKSPACE_TOOLCHAINS="go=1.27.1,node=26.9.0"` (env), and/or a `.toolchains`
  file in the workspace root (same `name=version` lines, `#` comments allowed);
- `agent-worker toolchain-install go=1.27.1,node=26.9.0` (the CLI half);
- an implicit ensure before every `Execute` (a failure FAILS the job loudly —
  never a silent fallback to the bare base image).

The index is fetched from `$WORKER_TOOLCHAIN_INDEX` (default: the artifact
generic mount). **The installer hard-codes no mirror and the image hard-codes no
index address** — the index's `url` fields are data. The schema (artifacts[],
sha256-required, format, strip/bin/rename, install[], requires[]) is the contract
in `abc-protocol/deploy/DEVELOP.md` → "Toolchain index".

The tool list + per-language unpack metadata live in **one place**,
`agent-toolchain/toolchain-meta.sh` (shared by the index generator and the
publisher). `PUBLISHED_LANGS` is the installable set; a language not in it is
absent from the index (declaring it fails loudly). strip/bin/rename mirror each
`Dockerfile.<lang>`'s unpack.

`agent-toolchain/build-index.sh` GENERATES the index (url → the artifact generic
path, sha256 from `cache/<distro>/`), `--publish` PUTs it with `ARTIFACT_TOKEN`.
`agent-toolchain/publish-artifacts.sh` uploads the artifacts first (as-is
verbatim; `--build` for install.sh/compile kinds, phase 2). Both are build-time
data generation, NOT a runtime mirror — consistent with the "runtime mirrors are
not baked in" rule above.

Install semantics: idempotent + atomic (`.tmp` → sha256 verify → `rename`, with
an `.installed` marker), `flock` on the root for concurrent ensures, and a
two-layer PATH (`$ROOT/<lang>/<ver>/bin` merged into the runner's job env AND
the worker's own process env). `requires[]` (e.g. kotlin → java25) is pulled in
automatically.

### Publishing the toolchains (phase 1)

The artifacts are published to the shared `artifact` generic store (owner
decision: publish the FULL set, incl. restrictive-license tools — internal
container-release-style distribution, not commercial redistribution):

```sh
cd agent-toolchain
./fetch-artifacts.sh                    # populate cache/<distro>/ (sha256 source)
ARTIFACT_TOKEN=<token> ./publish-artifacts.sh        # upload + publish index
```

**Layout gotcha**: artifact's generic store addresses content as
`/artifacts/generic/<name>/<version>/<filename>` — EXACTLY three segments (see
`easy-vcs/artifact` `generic/lib.go`). A nested
`toolchains/<lang>/<version>/<file>` is four segments and **404s**. The published
layout is therefore **flat**: toolchain `<lang>@<ver>` → name `toolchains-<lang>`,
version `<ver>`, filename `<file>`; the index → name `toolchains`, version
`index`, filename `index.json`.

**Phase 1** = pure-unpack: `go node python java25 dotnet php dart kotlin zig bun
pixi`. **Phase 2** = the rest, still "download → verify → unpack (+ optional
`install[]` step / runtime `env[]`)" — no publisher-side build:
`scala groovy deno julia crystal ocaml haskell ruby rust`. Together they cover
`tar.gz`/`tar.xz`/`zip`/`gz`/`phar`/`raw`, `strip 0|1`, `rename`, multi-file,
`install[]` (+`unpack_dir`), `env[]`, and `requires` (kotlin/clojure/scala →
java25).

**Phase 2b** = installers / build-time packaging, now PUBLISHED too:
`java swift gleam godot erlang elixir clojure conda perl lua r`. Shapes used:
`install[]` (`erlang` OTP `./Install`, `clojure`, `perl`), `unpack_dir`
(rust/clojure/perl), `install_prefix` (conda — INSTALLER-STYLE, installs in
place), build-time relocatable archives (`lua`, `r` — `publish-artifacts.sh
--build`). `install[]` is a SHELL command (`["sh","-c",cmd]`); do NOT bake
`{root}` absolute paths into it (it runs in `.tmp`, later renamed — use
`$(dirname "$0")/…`). GPU/pip chains and VM/desktop images stay images (never
runtime-installed).

**`clang` / `cmake` / `ninja` are installable too** (not images): clang uses the
official relocatable LLVM prebuilt (`LLVM-<ver>-Linux-X64.tar.xz` → `bin`),
cmake/ninja their official prebuilts. The old `Dockerfile.clang` was
deliberately gcc-free; the installable clang does not try to reproduce that —
it lands on the shared base (which has build-essential). This is a deliberate
semantic change (owner decision).

**`sandbox-submit-mr` diffs the FILE SYSTEM, not git** (it does not honor
`.gitignore`): after building/publishing, `agent-toolchain/cache/` and
`agent-toolchain/.publish/` hold multi-GB artifacts and can make a submit time
out (or hang). Always `rm -rf agent-toolchain/cache agent-toolchain/.publish`
before submitting.

### Platform-aware index (`os`/`arch`) + the VM toolchain bridge

A toolchain may carry `os`/`arch` on its artifacts (empty = wildcard). A single
index then serves linux AND windows/macos: the installer picks the artifact
matching its own `GOOS`/`GOARCH` (`platformArtifacts`). `flutter` is the first
user — four relocatable SDKs (`linux/amd64`, `windows/amd64`, `darwin/amd64`,
`darwin/arm64`), all plain unpack. `Toolchain.os` additionally GATES install:
e.g. `os: ["windows","darwin"]` makes a linux sandbox refuse it with a clear
error instead of installing something useless.

**VM guest → artifact**: a VM guest can only reach the container at
`host.lan:8090` (the token bridge), NOT the cluster. The VM images' nginx
(`vm/{windows,macos}/00-token.conf`) therefore also proxies `/artifacts/` to
`artifact.worker.svc.cluster.local`, and the guest launcher sets
`WORKER_TOOLCHAIN_INDEX=http://host.lan:8090/artifacts/generic/toolchains/index/index.json`.
So a VM sandbox installs toolchains through that bridge (slow for big ones —
rust ~1.5G / julia ~800M / flutter ~1.5–2.2G — but works).

### Published = runtime-installable; sandbox images are slimmed

**Phase 1 + phase 2 are PUBLISHED** (2026-10-02) to the artifact store
(`…/generic/toolchains/index/index.json` + `…/generic/toolchains-<lang>/…`):
`go node python java25 dotnet php dart kotlin zig bun pixi` **+** `scala groovy
deno julia crystal ocaml haskell ruby rust` (21 total). These are therefore
**installable at runtime** and are **no longer baked into sandbox images** —
`sandbox-images/build.sh`'s default `LANGS` drops them. `java25` is the ONE
exception kept preinstalled: it is also the parent image of
`kotlin/scala/clojure/groovy` (`<lang>.base` → `toolchain-java25`), so removing
it would break those builds (option (a)).

**Phase-2b languages stay preinstalled** (declaring one has nothing to install
from yet): `java clojure elixir gleam swift clang lua perl r conda godot`, the
ML chain, and the OS images. Do not trim those from `LANGS` until their publish
lands.

The catalog (`agent-toolchain/WORKSPACE_LANGS`, i.e. the `toolchain-<lang>`
images) is unchanged — only the SANDBOX default set is slimmed, because a
published language no longer needs a prebuilt sandbox image.

## Known gotcha: `fetch-artifacts.sh` provenance

`agent-toolchain/` was moved here from `workspace-gateway` (commit `91e6cc4`);
the move to drop the old easylab tree (`5a8b2f5`) **lost
`fetch-artifacts.sh`** while `.gitignore` and `config.sh`'s `CACHE_ROOT`
contract still require `cache/<distro>/`. It has been restored here; keep it in
sync with `CACHE_ROOT` and with the `COPY cache/<file>` lines in the
Dockerfiles. `HEXKEY_URL` is the one filename that does **not** match its URL
basename (`registry-public-key.pem`, not `hex-registry-public-key.pem`).

`conan-wheels-<ver>.tar.gz` is the one cache file with no single upstream
tarball; `fetch-artifacts.sh` assembles it from PyPI via `pip download` (conan
is public), pinned to CPython 3.14 / manylinux_2_28 so it works regardless of
the host's Python. Set `FETCH_CONAN=0` to skip it when not building `clang`.

**Hex (elixir) is mirrored locally.** `repo.hex.pm/installs/{elixir}/hex-*.ez`
(and the registry public key) 301-redirects to a path that 404s, so the elixir
toolchain cannot fetch them upstream. Put the working files in
`toolchain/urls.local.env` (git-ignored) to point `fetch-artifacts.sh` at a
local store; do not "fix" the pinned URL in `urls.env` — it is correct, the
public path is just unstable.

## Known gotcha: buildkitd injects NO proxy into RUN steps

`buildkitd` runs without any HTTP proxy env of its own, so a `RUN` that talks
to the network (the apt layers in `Dockerfile.r`, `Dockerfile.godot`, the
distro/base setup) reaches `deb.debian.org` **directly at ~25 KB/s** instead of
~13.5 MB/s through `mihomo` — an apt layer that should take seconds crawls for
30+ minutes. Fixing it needs both halves:

1. `config.sh:build_image` passes `HTTP(S)_PROXY` + `NO_PROXY` as build-args;
   Dockerfile RUN steps inherit them as env automatically.
2. A Dockerfile whose RUN text never **references** the proxy still caches
   identically, so BuildKit may attach the new build to an **orphaned exec**
   left by an earlier killed client (BuildKit does not cancel an exec when its
   client dies). Declaring/referencing the arg (`echo "apt via ${HTTP_PROXY:-direct}"`)
   changes the step digest and forces a fresh, proxied exec.

Toolchain images that only `COPY cache/` (no network) are unaffected. The
predecessor tree passed the same build-args for its android/VM images
(`easyworker-new/images/*/build.sh`).

## Known gotcha: the sandbox→MR channel does NOT carry file MODE

`sandbox-submit-mr` diffs file **contents** only. A `chmod +x` done in the
sandbox is **dropped** — the `mr/...` head branch keeps `100644`, so a script
you marked executable lands **non-executable** and `./script.sh` fails with
`permission denied`. (Observed: `vm/publish-golden.sh` was committed 0644 while
the commit/comment claimed +x.)

- **Do not rely on +x through this channel.** Either invoke such scripts as
  `bash <script>.sh …`, or have someone with a real checkout `chmod +x` + push on
  the target branch.
- Existing in-tree scripts (`vm/windows/golden-disk.sh`, `offline-rebake.sh`,
  `repack-disk.sh`, …) are all `100644` for this reason — run them with `bash`.

## Effort (rough, single builder, cold cache)

- `scripts/build-all.sh`: ~1 min.
- `fetch-artifacts.sh` (all): several GB through the proxy, minutes-to-tens.
- `build-toolchain.sh` per language: 1–5 min (clang/rust/swift/dotnet slower).
- `sandbox-images/build.sh` per image: seconds (just COPYs the binary).
- VM images: dominated by disk upload; defrag first (`repack-disk.sh`) or the
  layer will not compress.

## Verification

- Worker: `go build ./... && go vet ./... && go test ./...`.
- Toolchain image: `skopeo inspect` and, for a language, `docker run … <lang> --version`.
- Sandbox image: deploy `abc-protocol/deploy`'s `worker-k8s/agent-worker-*.yaml`
  and hit the worker's health endpoint on `:48080`; run `dist/awtest-<os>-amd64`
  for the full Connect surface.
- `scripts/multi-test.sh` runs awtest on linux/windows/macos in one report.

## Standing rules

- Never commit to `jj-lab`; report its bugs instead.
- Run lint/typecheck/tests before declaring done. Do not commit unless asked.
- The sandbox→MR channel carries **contents only, never file mode** — call helper
  scripts with `bash <script>.sh` (see the gotcha above).
