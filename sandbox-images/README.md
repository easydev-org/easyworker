# Sandbox images

Sandboxes run a **pre-built, worker-bundled** image. The gateway no longer
injects the worker at launch: `CreateSandbox` refuses any image whose registry
org is not `SANDBOX_ORG` (default `sandbox`).

`build.sh` bakes the agent-worker binary (cross-compiled from THIS repo,
`../cmd/agent-worker`) into an existing GENERIC toolchain image and pushes it to
`<DEST_REGISTRY>/<SANDBOX_ORG>/sandbox-<lang>:<distro-tag>`:

**Two registries.** The generic TOOLCHAIN base images live in the Forgejo
registry (`git.agent.svc.cluster.local`, `REGISTRY`), but the **gateway pulls
sandbox images from the ARTIFACT registry** (`artifact.worker.svc.cluster.local`,
`DEST_REGISTRY`, matching the gateway's `IMAGE_REGISTRY_HOST`). `build.sh`
pulls the base from `REGISTRY` and pushes the result to `DEST_REGISTRY`, with the
destination creds chosen by host (artifact → `root:$ARTIFACT_TOKEN`).

    ./build.sh                       # the default set (below)
    ./build.sh base node python      # a subset
    ./build.sh base                  # just the base

    # push to artifact (the gateway's pull source) with a write token:
    ARTIFACT_TOKEN=… ./build.sh base

**The default set no longer includes the PUBLISHED languages** — phase 1 (`go
node python java25 dotnet php dart kotlin zig bun pixi`) and phase 2 (`scala
groovy deno julia crystal ocaml haskell ruby rust`). They are published to the
artifact store and installed **on demand** by the worker
(`WORKSPACE_TOOLCHAINS` → `internal/toolchains`), so baking them is redundant.
`java25` is the exception — it stays because it is also the parent image of
`kotlin/scala/clojure/groovy` (`<lang>.base` → `toolchain-java25`).

The default set is therefore the not-yet-published toolchains: `base java java25
clojure elixir gleam swift clang lua perl r conda godot cuda torch vllm
vllm-omni llamacpp comfyui` → `sandbox-<lang>:debian-trixie`. Phase-2b languages
stay preinstalled (declaring one has nothing to install from yet).

Re-run whenever the worker changes (a worker fix requires rebuilt images).

**Same-tag re-push is served from the node's image cache.** After rebuilding a
tag that nodes already have (e.g. `debian-trixie`), a new pod on that node may
still start the OLD image. To roll out a worker change reliably, publish a NEW
tag (e.g. `debian-trixie-<date>`) and point the gateway/manifests at it, or
otherwise invalidate the node image cache (imagePullPolicy Always is not enough
for a tag the kubelet already cached). Verify by checking the config digest on
the pod's node equals the freshly pushed one.
