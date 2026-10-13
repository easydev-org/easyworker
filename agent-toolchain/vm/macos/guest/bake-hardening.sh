#!/bin/bash
# Guest-side hardening for the macOS golden disk (run as root inside the guest).
# Removes the GUI prompts that would otherwise require a human click:
#   * Gatekeeper / quarantine ("unidentified developer", "are you sure?")
#   * TCC consent dialogs (Accessibility / ScreenCapture / Files / Automation)
#   * first-login Setup Assistant panes (keyboard, cloud, Siri, privacy, ...)
#   * stray login items
# Safe to re-run.
set -uo pipefail

WROOT=/Users/docker
TCC="/Library/Application Support/com.apple.TCC/TCC.db"

echo '== 1. Gatekeeper + quarantine off =='
spctl --master-disable 2>/dev/null || true
defaults write /Library/Preferences/com.apple.security GKAutoRearm -bool false 2>/dev/null || true
defaults write com.apple.LaunchServices LSQuarantine -bool false 2>/dev/null || true

echo '== 2. pre-grant TCC (no consent dialogs) =='
# Columns mirror the launcher's grant (auth_value=2 allow, client_type=1 path).
grant() {
  local client="$1" svc="$2"
  sqlite3 "$TCC" "INSERT OR REPLACE INTO access(service,client,client_type,auth_value,auth_reason,auth_version,csreq,policy_id,indirect_object_identifier_type,indirect_object_identifier,indirect_object_code_identity,flags,last_modified) VALUES('$svc','$client',1,2,4,1,NULL,NULL,0,'UNUSED',NULL,0,strftime('%s','now'));" 2>/dev/null || true
}
for client in \
  /usr/local/bin/xa11y \
  "$WROOT/agent-worker-darwin" \
  "$WROOT/agent-worker" \
  "$WROOT/native-a11y" \
  /bin/bash /bin/zsh /usr/bin/curl /usr/bin/python3 \
  /System/Applications/Utilities/Terminal.app \
  /Applications/Xcode.app ; do
  for svc in \
    kTCCServiceAccessibility \
    kTCCServiceScreenCapture \
    kTCCServiceSystemPolicyAllFiles \
    kTCCServiceAppleEvents \
    kTCCServiceListenEvent \
    kTCCServicePostEvent \
    kTCCServiceScreenRecording ; do
    grant "$client" "$svc"
  done
done

echo '== 3. Setup Assistant: mark everything seen =='
for k in DidSeeKeyboardSetupAssistant DidSeeCloudSetup DidSeeSiriSetup \
         DidSeePrivacy DidSeeAccessibility DidSeeAppearanceSetup \
         DidSeeScreenTime DidSeeTrueTone DidSeeTouchIDSetup ; do
  defaults write com.apple.SetupAssistant "$k" -bool true 2>/dev/null || true
done
# Pre-set the keyboard type so the Keyboard Setup Assistant never appears.
# The assistant is triggered by the USER-domain value being absent (a system
# /Library/Preferences write alone is NOT enough — the KSA reads the user's
# domain). Set it for docker in BOTH the user domain and ByHost.
defaults write /Library/Preferences/com.apple.keyboardtype keyboardtype -int 40 2>/dev/null || true
if id docker >/dev/null 2>&1; then
  sudo -u docker defaults write com.apple.keyboardtype keyboardtype -int 40 2>/dev/null || true
  sudo -u docker defaults -currentHost write com.apple.keyboardtype keyboardtype -int 40 2>/dev/null || true
fi

echo '== 4. no window restore / no reopen =='
defaults write NSGlobalDomain NSQuitAlwaysKeepsWindows -bool false 2>/dev/null || true
defaults write com.apple.loginwindow TALLogoutSucceeded -bool false 2>/dev/null || true
rm -rf "$WROOT/Library/Saved Application State/"* 2>/dev/null || true

echo '== 5. clear stray login items =='
osascript -e 'tell application "System Events" to delete every login item' 2>/dev/null || true

echo '== 6. restart tccd so grants take effect =='
launchctl stop com.apple.tccd 2>/dev/null || true
pkill -9 tccd 2>/dev/null || true

echo 'MAC_HARDENING_OK'
