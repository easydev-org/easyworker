#!/usr/bin/env bash
# agent-worker desktop entrypoint — PURE Wayland flavor (labwc).
# The screen is streamed by the worker as H.264 (WebCodecs); no VNC.
#
#   agent-worker :48080                    (started FIRST, see below)
#   labwc (headless)  (screen streamed by the worker as H.264)
#
# labwc is a wlroots compositor; WLR_BACKENDS=headless gives it an output with
# no real display. Everything runs as root
# with tini as PID 1.
#
# ORDER MATTERS: the D-Bus session bus + AT-SPI2 bus MUST be up before the
# worker (and thus any job) starts, so an app a job launches publishes its
# accessibility tree over D-Bus — this is what makes computer-use (xa11y) work
# on Wayland (no X server needed). The compositor is started in the background
# (it never blocks the worker). WAYLAND_DISPLAY is preset to wayland-0 BEFORE
# the worker starts, because jobs inherit the worker's environment.
set -Eeuo pipefail

SCREEN_PORT="${SCREEN_PORT:-6080}"
VNC_PORT="${VNC_PORT:-5900}"

export XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/tmp/xdg}"
export WAYLAND_DISPLAY="${WAYLAND_DISPLAY:-wayland-0}"
# The portal FRONTEND (xdg-desktop-portal) picks a backend by matching the
# portal file's UseIn= list against XDG_CURRENT_DESKTOP. It must be EXPORTED
# (not just set on the -wlr backend's command line), or the frontend sees an
# empty desktop and never loads the wlr backend — so Screenshot/ScreenCast are
# missing (Trash/NetworkMonitor, which are built-in, still appear).
export XDG_CURRENT_DESKTOP="${XDG_CURRENT_DESKTOP:-wlroots}"
mkdir -p "$XDG_RUNTIME_DIR" "$HOME/workspace" /data
chmod 700 "$XDG_RUNTIME_DIR"

# Screen-stack PIDs live in pid files: the stack comes up in a background
# subshell, so its variables would not reach the signal handler.
cleanup() {
  kill "${WORKER_PID:-}" \
       "$(cat /tmp/at-spi-registry.pid 2>/dev/null)" \
       "$(cat /tmp/at-spi-bus.pid 2>/dev/null)" \
       "$(cat /tmp/dbus.pid 2>/dev/null)" \
              "$(cat /tmp/foot.pid 2>/dev/null)" \
       "$(cat /tmp/portal.pid 2>/dev/null)" \
       "$(cat /tmp/portal-wlr.pid 2>/dev/null)" \
       "$(cat /tmp/pipewire.pid 2>/dev/null)" \
       "$(cat /tmp/labwc.pid 2>/dev/null)" 2>/dev/null || true
}
trap cleanup TERM INT

# --- accessibility stack (D-Bus + AT-SPI2) --------------------------------
# MUST be up before the worker, so a job's app publishes its a11y tree over
# D-Bus. Unlike the X11 flavor this needs NO X server — AT-SPI2's tree lives on
# D-Bus, which is why computer-use works on Wayland. The session bus address is
# FIXED (DBUS_SESSION_BUS_ADDRESS in the image ENV, `unix:path=/tmp/agent-dbus`)
# because the worker's jobs inherit its environment verbatim.
bring_up_a11y() {
  echo "desktop[wayland]: starting D-Bus session bus at ${DBUS_SESSION_BUS_ADDRESS:-?}"
  rm -f /tmp/agent-dbus
  # dbus-daemon wants `--print-pid` to be an OPEN FD, not a path: hand it fd 4.
  dbus-daemon --session --address="${DBUS_SESSION_BUS_ADDRESS}" \
    --fork --print-pid=4 4>/tmp/dbus.pid >/tmp/dbus.log 2>&1
  for _ in $(seq 1 50); do
    [ -S /tmp/agent-dbus ] && break
    sleep 0.1
  done

  echo "desktop[wayland]: starting at-spi2 bus launcher + registry daemon"
  /usr/libexec/at-spi-bus-launcher --launch-immediately >/tmp/at-spi-bus.log 2>&1 &
  echo $! > /tmp/at-spi-bus.pid
  sleep 1
  /usr/libexec/at-spi2-registryd >/tmp/at-spi-registry.log 2>&1 &
  echo $! > /tmp/at-spi-registry.pid
  sleep 1
  # Flip the Status flags on so clients (xa11y) see the tree.
  for prop in IsEnabled ScreenReaderEnabled; do
    dbus-send --session --print-reply --dest=org.a11y.Bus /org/a11y/bus \
      org.freedesktop.DBus.Properties.Set \
      string:org.a11y.Status string:"$prop" variant:boolean:true \
      >/dev/null 2>&1 || true
  done
  return 0
}

bring_up_a11y

# --- agent-worker FIRST (foreground process; owns the container lifetime) --
echo "desktop[wayland]: starting agent-worker on :${WORKER_PORT:-48080} (WAYLAND_DISPLAY=${WAYLAND_DISPLAY})"
agent-worker &
WORKER_PID=$!

# --- compositor + sharing in the background (never blocks the worker) -----
bring_up_desktop() {
  echo "desktop[wayland]: starting labwc (headless)"
  # wlroots' headless backend has NO mode env (only WLR_HEADLESS_OUTPUTS count)
  # and defaults to 1280x720. Set the output mode AFTER the compositor is up
  # with wlr-randr --custom-mode (see below).
  # NOTE: do NOT set WLR_LIBINPUT_NO_DEVICES=1 — that makes wlroots SKIP
  # libinput device discovery, so the /dev/uinput device xa11y creates for
  # click/type/key is never picked up. Unset it so the libinput backend scans
  # /dev/input and binds the virtual device.
  WLR_BACKENDS=headless \
    WLR_HEADLESS_OUTPUTS=1 labwc >/tmp/labwc.log 2>&1 &
  echo $! > /tmp/labwc.pid

  # Wait for the wayland socket (labwc names it wayland-<n>; usually wayland-0).
  for _ in $(seq 1 100); do
    ls "$XDG_RUNTIME_DIR" 2>/dev/null | grep -q '^wayland' && break
    sleep 0.2
  done
  WL_SOCK="$(ls "$XDG_RUNTIME_DIR" 2>/dev/null | grep '^wayland' | head -1 || true)"
  if [ -z "$WL_SOCK" ]; then
    echo "desktop[wayland]: compositor failed to start" >&2
    cat /tmp/labwc.log >&2 || true
    return 0
  fi
  # The worker (and thus its jobs) already inherited WAYLAND_DISPLAY. A clean
  # container always yields wayland-0; warn loudly if that assumption breaks.
  if [ "$WL_SOCK" != "$WAYLAND_DISPLAY" ]; then
    echo "desktop[wayland]: WARNING socket is ${WL_SOCK}, jobs inherited ${WAYLAND_DISPLAY}" >&2
  fi

  # --- force the headless output to SCREEN_GEOMETRY (default 1920x1080) -----
  # wlroots headless always starts at 1280x720; wlr-randr --custom-mode is the
  # only way to change it. Retry until the output appears.
  GEOM="${SCREEN_GEOMETRY:-1920x1080}"
  for _ in $(seq 1 25); do
    OUT="$(wlr-randr 2>/dev/null | awk '/^[^ ]/{print $1; exit}')"
    if [ -n "${OUT}" ]; then
      wlr-randr --output "${OUT}" --custom-mode "${GEOM}" >/tmp/wlr-randr.log 2>&1 \
        && { echo "desktop[wayland]: output ${OUT} set to ${GEOM}"; break; }
    fi
    sleep 0.2
  done

  # --- a terminal so the desktop is not empty ---------------------------
  foot >/tmp/foot.log 2>&1 &
  echo $! > /tmp/foot.pid

  # --- screen sharing ---------------------------------------------------
  # --- screenshot portal stack (computer-screenshot / grim) --------------
  # xdg-desktop-portal-wlr provides org.freedesktop.portal.Screenshot +
  # ScreenCast for the wlroots compositor; it needs pipewire up first and a
  # wlroots-desktop hint, or it exits "failed to initialize screencast".
  echo "desktop[wayland]: starting pipewire + xdg-desktop-portal(-wlr)"
  pipewire >/tmp/pipewire.log 2>&1 &
  echo $! > /tmp/pipewire.pid
  sleep 1
  XDG_CURRENT_DESKTOP=wlroots /usr/libexec/xdg-desktop-portal-wlr \
    >/tmp/portal-wlr.log 2>&1 &
  echo $! > /tmp/portal-wlr.pid
  /usr/libexec/xdg-desktop-portal >/tmp/portal.log 2>&1 &
  echo $! > /tmp/portal.pid

  # native-a11y daemon (abc-agent/worker-native): a11y/input/capture/H.264 on
  # 127.0.0.1:48079. Started after the compositor so its Wayland backend
  # (wlr-screencopy / linux-dmabuf) can bind. The worker uses it when
  # WORKER_A11Y=native.
  if command -v native-a11y >/dev/null 2>&1; then
    echo "desktop[wayland]: starting native-a11y on :48079"
    native-a11y serve >/tmp/native-a11y.log 2>&1 &
    echo $! > /tmp/native-a11y.pid
  fi
}
bring_up_desktop &

# The worker is PID-of-record: when it exits, tear the desktop down.
wait "$WORKER_PID"
