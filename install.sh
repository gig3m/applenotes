#!/bin/bash
# Install notesd as a LaunchAgent on this Mac.
#
# This grants the daemon two macOS privileges and therefore requires SIP to be
# disabled. Read what it does before running it; uninstall.sh reverses all of it.
set -euo pipefail

PREFIX="${PREFIX:-/usr/local}"
LABEL="io.nrsil.notesd"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"
TOKEN="$HOME/.config/applenotes/token"
ADDR="${ADDR:-127.0.0.1:8437}"

die() { echo "install: $*" >&2; exit 1; }

[[ "$(uname)" == "Darwin" ]] || die "this only runs on macOS"
[[ $EUID -ne 0 ]] || die "run this as yourself, not root; it will ask for sudo where it needs it"

# --- what this needs, and why ------------------------------------------------
#
# Reading NoteStore.sqlite needs Full Disk Access. Writing notes needs Automation
# access to Notes.app. Both live in TCC, whose databases are protected by SIP --
# so with SIP on, the only way to grant them is by clicking consent dialogs,
# which a background agent cannot do. That is the trade this project asks for.
if csrutil status | grep -qi enabled; then
	cat >&2 <<'MSG'
install: System Integrity Protection is enabled.

notesd needs Full Disk Access (to read the notes database) and Automation
access to Notes.app (to write notes). With SIP on, those can only be granted
by clicking consent dialogs, which a background agent cannot do.

You can either:
  - disable SIP (boot to Recovery, `csrutil disable`) and run this again, or
  - run notesd by hand from a Terminal window and click the prompts as they
    appear. It will work; it just will not survive a reboot unattended.

This script will not continue.
MSG
	exit 1
fi

# A prebuilt binary beside this script is used in preference to building, since
# the Mac this runs on is usually an old machine kept for the purpose and is
# unlikely to have a Go toolchain.
SRC=""
if [[ -x "./notesd" ]]; then
	SRC="./notesd"
	echo "install: using the prebuilt ./notesd"
elif command -v go >/dev/null; then
	echo "install: building from source"
	go build -o "/tmp/notesd.$$" ./cmd/notesd
	SRC="/tmp/notesd.$$"
else
	die "no ./notesd here and no go toolchain to build one.
     Download a release binary next to this script, or build it elsewhere with
       GOOS=darwin GOARCH=$(uname -m | sed 's/x86_64/amd64/') go build -o notesd ./cmd/notesd"
fi

sudo install -m 0755 "$SRC" "$PREFIX/bin/notesd"
[[ "$SRC" == /tmp/* ]] && rm -f "$SRC"
BIN="$PREFIX/bin/notesd"

# The grants below are keyed to this exact path, so replacing the binary later
# keeps them; moving it does not.
"$BIN" -h >/dev/null 2>&1 || true

echo "install: granting TCC permissions to $BIN"
# These are the two grants, written directly because SIP is off. The user TCC
# database holds Automation; the system one holds Full Disk Access.
USER_TCC="$HOME/Library/Application Support/com.apple.TCC/TCC.db"
SYS_TCC="/Library/Application Support/com.apple.TCC/TCC.db"

grant() { # db service client indirect
	local db=$1 service=$2 client=$3 indirect=${4:-}
	local sql
	if [[ -n "$indirect" ]]; then
		sql="INSERT OR REPLACE INTO access
		     (service, client, client_type, auth_value, auth_reason, auth_version,
		      indirect_object_identifier_type, indirect_object_identifier, flags, last_modified)
		     VALUES ('$service', '$client', 1, 2, 4, 1, 0, '$indirect', 0, strftime('%s','now'));"
	else
		sql="INSERT OR REPLACE INTO access
		     (service, client, client_type, auth_value, auth_reason, auth_version, flags, last_modified)
		     VALUES ('$service', '$client', 1, 2, 4, 1, 0, strftime('%s','now'));"
	fi
	sudo sqlite3 "$db" "$sql"
}

grant "$USER_TCC" kTCCServiceAppleEvents "$BIN" com.apple.Notes
grant "$SYS_TCC"  kTCCServiceSystemPolicyAllFiles "$BIN"
sudo killall tccd 2>/dev/null || true   # tccd caches; it must re-read

echo "install: writing $PLIST"
mkdir -p "$(dirname "$PLIST")" "$(dirname "$TOKEN")"
# A LaunchAgent, not a LaunchDaemon: Apple Events must be sent from within a
# logged-in GUI session, so this runs as the user and only while logged in.
cat > "$PLIST" <<PLISTEOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>$LABEL</string>
  <key>ProgramArguments</key>
  <array>
    <string>$BIN</string>
    <string>-addr</string><string>$ADDR</string>
    <string>-token</string><string>$TOKEN</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>$HOME/Library/Logs/notesd.log</string>
  <key>StandardErrorPath</key><string>$HOME/Library/Logs/notesd.log</string>
</dict>
</plist>
PLISTEOF

launchctl unload "$PLIST" 2>/dev/null || true
launchctl load "$PLIST"
sleep 2

if ! launchctl list | grep -q "$LABEL"; then
	die "the agent did not start; see ~/Library/Logs/notesd.log"
fi

echo
echo "install: notesd is running on $ADDR"
echo "install: token is in $TOKEN"
echo
echo "Reach it over Tailscale from another machine:"
echo "  curl -H \"Authorization: Bearer \$(ssh $(hostname -s) cat $TOKEN)\" \\"
echo "       http://$(hostname -s):${ADDR##*:}/v1/notes"
echo
echo "Keep Notes.app running: it persists changes on its own schedule, and a"
echo "write is not visible to reads until it does."
