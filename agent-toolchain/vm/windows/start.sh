#!/usr/bin/env bash
set -Eeuo pipefail

# AgentWorker: publish the pod-provided per-pod env the guest launcher fetches
# over nginx at http://host.lan:8090/env. The guest does NOT inherit the pod's
# env, so WORKER_TOKEN (pre-auth) + SANDBOX_PROXY (deployment HTTP proxy for the
# guest's jobs) travel here.
{
  printf 'WORKER_TOKEN=%s\n' "${WORKER_TOKEN:-}"
  printf 'SANDBOX_PROXY=%s\n' "${SANDBOX_PROXY:-${HTTP_PROXY:-}}"
} > /run/shm/env
chmod 644 /run/shm/env 2>/dev/null || true

# Boot support files (windows.rom/vars/...) live in /storage alongside the disk
# in the image. When a PVC is mounted at /storage (to persist ONLY the disk) it
# shadows the image copies, so restore them from the image's /opt/winboot copy.
# Boot state is image-owned, not disk state — it must not live on the PVC.
if [ -d /opt/winboot ]; then
  for f in windows.base windows.boot windows.mac windows.rom windows.vars windows.ver; do
    [ -s "/storage/$f" ] || cp -f "/opt/winboot/$f" "/storage/$f" 2>/dev/null || true
  done
fi

# Materialize the guest disk from the artifact-published golden base (overlay);
# a no-op when GOLDEN_DISK_URL is unset (image-embedded disk is used).
if [ -f /run/golden-disk.sh ]; then . /run/golden-disk.sh; fi

return 0
