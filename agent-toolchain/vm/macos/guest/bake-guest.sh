#!/bin/bash
# Guest-side bake for the macOS image (run as root INSIDE the macOS guest).
#
# Goal: agent-worker runs inside docker's logged-in Aqua session (so GUI apps
# reach WindowServer) and docker may sudo without a password or TTY. The
# launcher boot-fetches the worker from host.lan:8090/worker.
set -Eeuo pipefail

WROOT=/Users/docker
SUDOERS=/etc/sudoers.d/agent-worker
LAUNCHER=$WROOT/worker-launch.sh
DAEMON=/Library/LaunchDaemons/com.agentworker.worker.plist
AGENT=$WROOT/Library/LaunchAgents/com.agentworker.worker.plist

mkdir -p "$WROOT/Library/LaunchAgents" "$WROOT/ws"
chown docker:staff "$WROOT/ws" "$WROOT/Library/LaunchAgents"

############ 1. passwordless sudo
cat > "$SUDOERS" <<'EOF'
Defaults:docker !requiretty
Defaults:docker !authenticate
docker ALL=(ALL) NOPASSWD: ALL
EOF
chown root:wheel "$SUDOERS"; chmod 440 "$SUDOERS"
visudo -c -f "$SUDOERS"

############ 2. launcher (installed from the image build context)
if [ -f /tmp/worker-launch.sh ]; then
  install -m 755 -o root -g wheel /tmp/worker-launch.sh "$LAUNCHER"
fi
chown root:wheel "$LAUNCHER"; chmod 755 "$LAUNCHER"

############ 3. LaunchDaemon: one-shot bootstrap/fallback (not KeepAlive)
cat > "$DAEMON" <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key><string>com.agentworker.worker</string>
    <key>ProgramArguments</key>
    <array>
        <string>/bin/bash</string>
        <string>/Users/docker/worker-launch.sh</string>
    </array>
    <key>RunAtLoad</key><true/>
    <key>KeepAlive</key><false/>
    <key>StandardOutPath</key><string>/Library/Logs/agent-worker.log</string>
    <key>StandardErrorPath</key><string>/Library/Logs/agent-worker.log</string>
</dict>
</plist>
EOF
chown root:wheel "$DAEMON"; chmod 644 "$DAEMON"

############ 4. LaunchAgent: the worker itself, inside docker's Aqua session
cat > "$AGENT" <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key><string>com.agentworker.worker</string>
    <key>ProgramArguments</key>
    <array>
        <string>/bin/bash</string>
        <string>/Users/docker/worker-launch.sh</string>
    </array>
    <key>RunAtLoad</key><true/>
    <key>KeepAlive</key><true/>
    <key>LimitLoadToSessionType</key><string>Aqua</string>
    <key>StandardOutPath</key><string>/Users/docker/ws/agent-worker-agent.log</string>
    <key>StandardErrorPath</key><string>/Users/docker/ws/agent-worker-agent.log</string>
</dict>
</plist>
EOF
chown docker:staff "$AGENT"; chmod 644 "$AGENT"

############ 5. auto-login docker so a real GUI session exists at boot
sysadminctl -autologin set -userName docker -password admin \
  -adminUser docker -adminPassword admin 2>&1 | tail -2 || true
defaults write /Library/Preferences/com.apple.loginwindow autoLoginUser -string docker 2>/dev/null || true

############ 5b. xa11y is boot-fetched by worker-launch.sh, not baked here
# The xa11y CLI is a self-contained Mach-O binary served by the image at nginx
# :8090 /xa11y; worker-launch.sh fetches it, links it into /usr/local/bin, and
# grants TCC on every boot. Nothing is installed into the disk at bake time, so
# the guest needs no Command Line Tools and no Python.

############ 6. verify
echo "== sudoers =="; visudo -c -f "$SUDOERS"
echo "== autologin =="; sysadminctl -autologin status 2>&1 | tail -1
echo "== daemon =="; plutil -lint "$DAEMON"
echo "== agent =="; plutil -lint "$AGENT"
echo "== launcher =="; ls -la "$LAUNCHER"
echo BAKE_GUEST_OK
