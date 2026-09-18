#!/bin/bash
# Remove notesd and every permission install.sh granted it.
set -euo pipefail

LABEL="dev.applenotes.notesd"
STATE="$HOME/.config/applenotes/installed"

# Read back what was actually installed. Recomputing it from a default PREFIX
# would leave a real installation in place -- and worse, leave its TCC grants
# behind pointing at a path, which is a dormant capability for whatever is
# written there next.
if [[ -r "$STATE" ]]; then
	# shellcheck disable=SC1090
	. "$STATE"
else
	bin="${PREFIX:-/usr/local/libexec/applenotes}/notesd"
	plist="$HOME/Library/LaunchAgents/$LABEL.plist"
	label="$LABEL"
	echo "uninstall: no install record at $STATE; assuming the defaults" >&2
fi

echo "uninstall: stopping $label"
launchctl bootout "gui/$UID/$label" 2>/dev/null ||
	launchctl unload "$plist" 2>/dev/null ||
	echo "uninstall: the agent was not loaded" >&2
rm -f "$plist"

echo "uninstall: revoking TCC permissions for $bin"
USER_TCC="$HOME/Library/Application Support/com.apple.TCC/TCC.db"
SYS_TCC="/Library/Application Support/com.apple.TCC/TCC.db"
sqlite3 "$USER_TCC" -cmd ".param set ?1 $bin" "DELETE FROM access WHERE client = ?1;" 2>/dev/null || true
sudo sqlite3 "$SYS_TCC" -cmd ".param set ?1 $bin" "DELETE FROM access WHERE client = ?1;" 2>/dev/null || true
sudo killall tccd 2>/dev/null || true

echo "uninstall: removing $bin"
sudo rm -f "$bin"
sudo rmdir "$(dirname "$bin")" 2>/dev/null || true
rm -f "$STATE"

cat <<MSG

uninstall: done.

Left in place, delete them yourself if you want them gone:
  $HOME/.config/applenotes/token   (the only thing that was secret)
  $HOME/Library/Logs/notesd.log

This does not re-enable SIP. If you turned it off for this, turn it back on
from Recovery with 'csrutil enable'.
MSG
