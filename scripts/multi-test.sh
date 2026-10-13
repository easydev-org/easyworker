#!/usr/bin/env bash
# Multi-platform awtest run: all three platforms, one report.
#
#   linux   -> against the in-cluster agent-worker service (temp ns)
#   windows -> upload+start inside the dockur-windows VM, run awtest there
#   macos   -> upload+start inside the dockur-macos VM, run awtest there
#
# Prereqs: scripts/build-all.sh; kubectl access; SSH reachability of
# ssh-windows/ssh-macos svc (port 80, docker/admin); python3 + paramiko.
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

rc=0

echo "========== linux (k8s svc) =========="
./dist/awtest-linux-amd64 -addr http://agent-worker.temp.svc.cluster.local || rc=1

echo "========== windows (dockur VM) =========="
python3 scripts/vmtest.py windows || rc=1

echo "========== macos (dockur VM) =========="
python3 scripts/vmtest.py macos || rc=1

echo "====================================="
if [ $rc -eq 0 ]; then
  echo "MULTI-PLATFORM: ALL PASS"
else
  echo "MULTI-PLATFORM: FAILURES PRESENT"
fi
exit $rc
