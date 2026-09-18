#!/bin/bash
# Remove notesd and every permission install.sh granted it.
set -euo pipefail

PREFIX="${PREFIX:-/usr/local}"
LABEL="io.nrsil.notesd"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"
BIN="$PREFIX/bin/notesd"

echo "uninstall: stopping the agent"
launchctl unload "$PLIST" 2>/dev/null || true
rm -f "$PLIST"

echo "uninstall: revoking TCC permissions"
USER_TCC="$HOME/Library/Application Support/com.apple.TCC/TCC.db"
SYS_TCC="/Library/Application Support/com.apple.TCC/TCC.db"
sudo sqlite3 "$USER_TCC" "DELETE FROM access WHERE client='$BIN';" 2>/dev/null || true
sudo sqlite3 "$SYS_TCC"  "DELETE FROM access WHERE client='$BIN';" 2>/dev/null || true
sudo killall tccd 2>/dev/null || true

echo "uninstall: removing $BIN"
sudo rm -f "$BIN"

echo
echo "uninstall: done. The token file is left in place at"
echo "  $HOME/.config/applenotes/token"
echo "Delete it yourself if you want it gone; it is the only thing that was secret."
echo
echo "Note: this does not re-enable SIP. If you turned it off for this, turn it"
echo "back on from Recovery with 'csrutil enable'."
