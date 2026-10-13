#!/usr/bin/env bash
set -Eeuo pipefail

# agent-worker Android sandbox.
#
# FINAL SHAPE: the Go worker AND the Kotlin native-a11y daemon both run INSIDE
# the emulator. This container only boots the emulator, installs the baked APK,
# grants the accessibility + screen-capture (MediaProjection) consents, pushes
# the static Go worker into the guest, starts it, and forwards its port out.
#
#   container                                guest (Android x86_64, bionic)
#   ┌─────────────────────────┐   adb       ┌──────────────────────────────┐
#   │ emulator + entrypoint   │ ─────────▶  │ nativea11y APK  (est :48079) │
#   │  adb forward 48080      │             │ Go worker       (:48080)     │
#   └─────────────────────────┘             └──────────────────────────────┘
#   worker :48080 <--adb forward-- :48080

AVD="${ANDROID_AVD:-sandbox}"
MEM="${ANDROID_MEM:-6144}"
CPUS="${ANDROID_CPUS:-4}"
GPU="${ANDROID_GPU:-swiftshader_indirect}"
WIPE="${ANDROID_WIPE_DATA:-N}"
ADB="adb -s emulator-5554"
GUEST_WORKER=/data/local/tmp/agent-worker
GUEST_WS=/data/local/tmp/workspace

export ANDROID_SDK_ROOT=/opt/android-sdk
export ANDROID_HOME=/opt/android-sdk
export ANDROID_AVD_HOME="${ANDROID_AVD_HOME:-/opt/android-avd}"
export PATH="/opt/android-sdk/platform-tools:/opt/android-sdk/emulator:${PATH}"

if [ "${ANDROID_AVD_HOME}" != "/opt/android-avd" ] && [ ! -f "${ANDROID_AVD_HOME}/${AVD}.avd/config.ini" ]; then
  echo "android: seeding AVD ${AVD} -> ${ANDROID_AVD_HOME}"
  mkdir -p "${ANDROID_AVD_HOME}"
  cp -a /opt/android-avd/. "${ANDROID_AVD_HOME}/" 2>/dev/null || true
fi
mkdir -p /workspace

cleanup() { kill "$(cat /tmp/emu.pid 2>/dev/null)" 2>/dev/null || true; }
trap cleanup TERM INT

# --- bring up the emulator + the in-guest stack ----------------------------
bring_up_guest() {
  WIPE_ARG=""; [ "${WIPE^^}" = "Y" ] && WIPE_ARG="-wipe-data"
  PROXY_ARG=""; P="${SANDBOX_PROXY:-${HTTP_PROXY:-}}"; [ -n "$P" ] && PROXY_ARG="-http-proxy ${P}"
  echo "android: starting emulator (headless, kvm, ${CPUS} vCPU, ${MEM} MB, gpu=${GPU})"
  emulator -avd "$AVD" \
    -no-window -no-audio -no-boot-anim -no-snapshot ${WIPE_ARG} ${PROXY_ARG} \
    -gpu "$GPU" -accel on -memory "$MEM" -cores "$CPUS" -port 5554 \
    >/tmp/emulator.log 2>&1 &
  echo $! > /tmp/emu.pid

  adb start-server >/dev/null 2>&1 || true
  booted=""
  for _ in $(seq 1 180); do
    bc="$($ADB shell getprop sys.boot_completed 2>/dev/null | tr -d '\r' || true)"
    [ "$bc" = "1" ] && { booted=1; break; }
    sleep 5
  done
  [ -z "$booted" ] && { echo "android: guest did not boot" >&2; tail -40 /tmp/emulator.log >&2; return 0; }
  echo "android: guest is up"
  $ADB shell "svc power stayon true" >/dev/null 2>&1 || true
  $ADB shell "wm dismiss-keyguard" >/dev/null 2>&1 || true

  # --- install + authorize the native-a11y daemon --------------------------
  $ADB install -r /opt/nativea11y.apk >/dev/null 2>&1 || echo "android: apk install failed" >&2
  $ADB shell settings put secure enabled_accessibility_services dev.nativea11y/dev.nativea11y.A11yService >/dev/null 2>&1 || true
  $ADB shell settings put secure accessibility_enabled 1 >/dev/null 2>&1 || true
  # Pre-grant screen capture so the MediaProjection consent dialog never appears
  # (this is what makes the est daemon start headless).
  $ADB shell appops set dev.nativea11y PROJECT_MEDIA allow >/dev/null 2>&1 || true
  $ADB shell am start -n dev.nativea11y/.MainActivity --ez autoGrant true >/dev/null 2>&1 || true
  sleep 5
  # If a consent dialog did appear, tap its START button.
  $ADB shell input tap 1221 793 >/dev/null 2>&1 || true
  sleep 2

  # --- the Go worker (static) runs INSIDE the guest ------------------------
  $ADB push /opt/agent-worker-android "$GUEST_WORKER" >/dev/null 2>&1 || echo "android: worker push failed" >&2
  $ADB shell chmod 755 "$GUEST_WORKER" >/dev/null 2>&1 || true
  $ADB shell mkdir -p "$GUEST_WS" >/dev/null 2>&1 || true
  $ADB shell "pkill -f $GUEST_WORKER" >/dev/null 2>&1 || true
  $ADB shell "cd /data/local/tmp && WORKER_OS=android WORKER_TOKEN='${WORKER_TOKEN:-}' WORKER_WORKSPACE=$GUEST_WS setsid $GUEST_WORKER -addr 127.0.0.1:48080 -workspace $GUEST_WS -db $GUEST_WS/jobs.db >/data/local/tmp/agent-worker.log 2>&1 </dev/null &" >/dev/null 2>&1 || true
  sleep 3
  $ADB shell "cat /data/local/tmp/agent-worker.log" 2>/dev/null | tail -5 >&2 || true
}
bring_up_guest &

# --- publish the in-guest worker's port ------------------------------------
# adb forward binds 127.0.0.1 ONLY, but the k8s Service dials the pod IP, so a
# front proxy must listen on 0.0.0.0 and relay to the adb-forwarded loopback.
echo "android: forwarding :48080 -> guest worker"
for _ in $(seq 1 240); do
  adb -s emulator-5554 forward tcp:14880 tcp:48080 >/dev/null 2>&1 || true
  if curl -s -m 2 -o /dev/null "http://127.0.0.1:14880/"; then break; fi
  sleep 3
done
echo "android: worker reachable on 127.0.0.1:14880; fronting on 0.0.0.0:48080"

# Front proxy: socat binds 0.0.0.0:48080 (the k8s Service targetPort) and relays
# to the adb-forwarded loopback. Run it in the foreground of its own subshell so
# it is restarted if it ever exits.
(
  while true; do
    socat TCP-LISTEN:48080,fork,reuseaddr TCP:127.0.0.1:14880
    echo "android: socat exited ($?); restarting" >&2
    sleep 2
  done
) &

# Keep the container (and the emulator) alive; re-assert the adb forward if it
# drops.
while true; do
  adb -s emulator-5554 forward tcp:14880 tcp:48080 >/dev/null 2>&1 || true
  sleep 10
done
