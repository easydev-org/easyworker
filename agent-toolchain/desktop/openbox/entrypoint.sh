#!/usr/bin/env bash
# agent-worker desktop entrypoint — PURE X11 flavor (Xvfb + openbox).
# The screen is served over WebRTC-free H.264 (WebCodecs) by the worker; no VNC.
#
#   Xvfb :99  ->  D-Bus + AT-SPI2  ->  agent-worker :48080
#                  openbox  (screen streamed by the worker as H.264)
#
# Everything runs as root. tini is PID 1, so this script only needs to start the
# stack and wait on the worker; on TERM it tears the children down.
#
# ORDER MATTERS. Xvfb comes first because at-spi2-registryd opens the X display.
# The D-Bus/AT-SPI stack then comes up before agent-worker, so every job (which
# inherits the worker's environment) finds a live accessibility bus. The gateway
# waits only 60s for :48080; this bring-up is ~3s, so readiness is unaffected.
set -Eeuo pipefail

DISPLAY_NUM="${DISPLAY_NUM:-99}"
export DISPLAY=":${DISPLAY_NUM}"
GEOMETRY="${SCREEN_GEOMETRY:-1920x1080x24}"
SCREEN_PORT="${SCREEN_PORT:-6080}"
VNC_PORT="${VNC_PORT:-5900}"

mkdir -p "$HOME/workspace" /data

# Screen-stack PIDs live in pid files: the stack comes up in a background
# subshell, so its variables would not reach the signal handler.
cleanup() {
  kill "${WORKER_PID:-}" \
       "$(cat /tmp/at-spi-registry.pid 2>/dev/null)" \
       "$(cat /tmp/at-spi-bus.pid 2>/dev/null)" \
       "$(cat /tmp/dbus.pid 2>/dev/null)" \
              "$(cat /tmp/xterm.pid 2>/dev/null)" \
       "$(cat /tmp/openbox.pid 2>/dev/null)" \
       "$(cat /tmp/xvfb.pid 2>/dev/null)" 2>/dev/null || true
}
trap cleanup TERM INT

# --- accessibility stack ---------------------------------------------------
# D-Bus session bus + the AT-SPI2 bus launcher/registry MUST be up before the
# worker (and thus any job) starts, so an app a job launches can publish its
# accessibility tree. `at-spi2-registryd` opens the X display, so Xvfb has to be
# up first. The whole bring-up is ~2-3s — well inside the gateway's 60s :48080
# readiness wait.
#
# The session bus address is FIXED (DBUS_SESSION_BUS_ADDRESS in the image ENV,
# `unix:path=/tmp/agent-dbus`) rather than the random one `dbus-launch` picks:
# the worker's jobs inherit its environment verbatim, so the address must be
# known before the worker boots.
bring_up_display() {
  echo "desktop[x11]: starting Xvfb on ${DISPLAY} (${GEOMETRY})"
  Xvfb "${DISPLAY}" -screen 0 "${GEOMETRY}" -ac -nolisten tcp >/tmp/xvfb.log 2>&1 &
  echo $! > /tmp/xvfb.pid
  for _ in $(seq 1 100); do
    [ -S "/tmp/.X11-unix/X${DISPLAY_NUM}" ] && break
    sleep 0.1
  done
}

bring_up_a11y() {
  echo "desktop[x11]: starting D-Bus session bus at ${DBUS_SESSION_BUS_ADDRESS:-?}"
  rm -f /tmp/agent-dbus
  # dbus-daemon wants `--print-pid` to be an OPEN FD, not a path: hand it fd 4.
  dbus-daemon --session --address="${DBUS_SESSION_BUS_ADDRESS}" \
    --fork --print-pid=4 4>/tmp/dbus.pid >/tmp/dbus.log 2>&1
  for _ in $(seq 1 50); do
    [ -S /tmp/agent-dbus ] && break
    sleep 0.1
  done

  # AT-SPI2: the bus launcher proxies org.a11y.Bus (apps/clients ask it for the
  # private a11y bus); the registry aggregates the per-app accessible trees.
  # Then flip the Status flags on so clients (xa11y) see the tree. Ordering and
  # the flag writes mirror xa11y's own setup-a11y action.
  echo "desktop[x11]: starting at-spi2 bus launcher + registry daemon"
  /usr/libexec/at-spi-bus-launcher --launch-immediately >/tmp/at-spi-bus.log 2>&1 &
  echo $! > /tmp/at-spi-bus.pid
  sleep 1
  /usr/libexec/at-spi2-registryd >/tmp/at-spi-registry.log 2>&1 &
  echo $! > /tmp/at-spi-registry.pid
  sleep 1
  for prop in IsEnabled ScreenReaderEnabled; do
    dbus-send --session --print-reply --dest=org.a11y.Bus /org/a11y/bus \
      org.freedesktop.DBus.Properties.Set \
      string:org.a11y.Status string:"$prop" variant:boolean:true \
      >/dev/null 2>&1 || true
  done
  return 0
}

bring_up_display
bring_up_a11y

# --- agent-worker (foreground process; owns the container lifetime) -------
# Started after the a11y stack so jobs inherit a live AT-SPI session.
echo "desktop[x11]: starting agent-worker on :${WORKER_PORT:-48080}"
agent-worker &
WORKER_PID=$!

# --- window manager + a terminal + native-a11y (background) ---------------
bring_up_desktop() {
  # --- window manager + a terminal so the desktop is not empty ----------
  echo "desktop[x11]: starting openbox"
  openbox >/tmp/openbox.log 2>&1 &
  echo $! > /tmp/openbox.pid
  xterm -geometry 100x28+20+20 -title "agent-worker" >/tmp/xterm.log 2>&1 &
  echo $! > /tmp/xterm.pid

  # native-a11y daemon (abc-agent/worker-native): owns a11y/input/capture/H.264
  # on 127.0.0.1:48079. Started after X so its X11 backend can connect. The
  # worker uses it when WORKER_A11Y=native.
  if command -v native-a11y >/dev/null 2>&1; then
    echo "desktop[x11]: starting native-a11y on :48079"
    native-a11y serve >/tmp/native-a11y.log 2>&1 &
    echo $! > /tmp/native-a11y.pid
  fi
}
bring_up_desktop &

# The worker is PID-of-record: when it exits, tear the desktop down.
wait "$WORKER_PID"
