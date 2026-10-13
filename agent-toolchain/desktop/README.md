# agent-worker GUI sandboxes (X11 / Wayland)

Self-contained, run-only graphical sandboxes: a screen stack (virtual display +
the worker's live H.264 screen stream (WebCodecs) plus the agent-worker binary, all in one image, viewable in a
browser. Built `FROM agent-toolchain/toolchain-base`, so the base build tools
come along (no extra toolchain is installed).

Two flavors, built as **two separate images**:

| flavor | image | stack | use when |
|---|---|---|---|
| `openbox` | `sandbox-x11` | **pure X11**: `Xvfb :99 → D-Bus+AT-SPI2 → openbox` (screen streamed by the worker) | jobs must **drive X11 apps** (computer-use / a11y, xdotool/xclip/xsel are X11-only) |
| `labwc`   | `sandbox-wayland` | **pure Wayland**: `labwc (headless)` (screen streamed by the worker) | native Wayland clients |

> X11-only automation (`xdotool`, `xclip`, `xsel`, and the `xa11y` a11y CLI)
> **cannot** target Wayland clients, and labwc does not provide an X server. If
> you need to drive an X11 app, use the `openbox` flavor.

> **Both flavors now support computer-use.** `xa11y` reads the AT-SPI2 tree,
> which lives on **D-Bus** — so it works on Wayland too (no X server needed). The
> `labwc` image ships `xa11y` + the AT-SPI2 stack, and its entrypoint brings up a
> D-Bus session bus + the AT-SPI2 bus/registry before the worker. Wayland INPUT
> uses **`wtype`** (virtual-keyboard protocol) and screenshots use **`grim`**;
> X11-only tools (`xdotool`/`xclip`) remain openbox-only.

## Computer-use / accessibility (both flavors)

The `openbox` image bakes in the **`xa11y`** CLI (github.com/xa11y/xa11y) plus
the **AT-SPI2** stack (`at-spi2-core`, `dbus`), so a job can read and drive
native apps through their accessibility trees instead of guessing from pixels:

```sh
xa11y apps                                   # running apps
xa11y tree  --app zenity                      # accessibility tree
xa11y find  'button[name="OK"]' --app zenity -o center
xa11y action press 'button[name="OK"]' --app zenity
xa11y click --at X,Y ; xa11y type "hello" ; xa11y screenshot --out /tmp/s.png
```

The entrypoint brings up Xvfb, then a **D-Bus session bus + AT-SPI2 bus
launcher/registry**, then agent-worker; the screen stack (openbox)
follows in the background. Order matters: `at-spi2-registryd` opens the X
display, and jobs inherit the worker's environment, so the a11y bus must be live
before the worker starts. The session bus uses a **fixed** address
(`DBUS_SESSION_BUS_ADDRESS=unix:path=/tmp/agent-dbus`, set in the image ENV) so
the address jobs see is known ahead of the worker's boot.

`xa11y` is not on any registry as a binary (upstream ships only Python/JS
wheels + crates), so `scripts/build-xa11y.sh` builds it from source into
`dist/xa11y-linux-amd64`; `build.sh` stages it into the image. Its only
non-glibc runtime dependency is `libxkbcommon0` (installed by the image);
D-Bus/AT-SPI/X11 are loaded at runtime.

The **labwc** flavor also ships the a11y CLI: `xa11y` reads/drives the AT-SPI2
tree over D-Bus (which Wayland apps publish just like X11 ones), so `xa11y
apps`/`tree`/`find`/`action` work headless on Wayland. For raw input the image
ships **`wtype`** (Wayland virtual-keyboard) and **`grim`** (screenshots); a
future option is `xa11y-linux`'s `/dev/uinput` path if a device is exposed.

## Gateway sandbox

Unlike `sandbox-<lang>` images (whose sole `ENTRYPOINT` is `agent-worker`), this
image ships its **own** entrypoint that starts the screen stack *and*
agent-worker. The gateway does not override the entrypoint, so it is a normal
`CreateSandbox` target like any `sandbox/` image (no `kvm` needed). Xvfb and the
a11y bus come up first (readiness still a few seconds); the visible desktop
(openbox) starts in the background; the worker streams the screen as H.264.

You can also run it as a plain Deployment — exactly like
`abc-protocol/deploy`'s `worker-k8s/agent-worker-android.yaml`:

```sh
kubectl apply -f worker-k8s/agent-worker-desktop.yaml   # from abc-protocol/deploy
```

Endpoint (in-pod): `:48080` worker API (which also serves the live screen at /stream/ws).

## Layout

```
openbox/Containerfile    pure-X11 image (Xvfb/openbox + a11y + worker)
openbox/entrypoint.sh    starts Xvfb -> D-Bus/AT-SPI -> agent-worker, then openbox
labwc/Containerfile      pure-Wayland image (labwc + worker)
labwc/entrypoint.sh      starts agent-worker, then the Wayland stack (background)
build.sh                 build+push one flavor (buildctl → skopeo → registry)
```

## Build

```sh
scripts/build-all.sh                          # dist/agent-worker-linux-amd64
scripts/build-xa11y.sh                        # dist/xa11y-linux-amd64 (openbox)
./agent-toolchain/desktop/build.sh openbox    # -> sandbox/sandbox-x11:debian-trixie
./agent-toolchain/desktop/build.sh labwc      # -> sandbox/sandbox-wayland:debian-trixie
```

## Display env reaches jobs

The worker's job environment **inherits** the worker's own environment (see the
repo README), so `DISPLAY` (openbox) / `WAYLAND_DISPLAY` + `XDG_RUNTIME_DIR`
(labwc) are visible to jobs — a GUI program run from a job appears on the
desktop with no extra plumbing.
