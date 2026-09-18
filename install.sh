#!/bin/bash
# Install notesd as a LaunchAgent on this Mac.
#
# This grants the daemon two macOS privileges and therefore requires SIP to be
# disabled. Read what it does before running it; uninstall.sh reverses all of it.
set -euo pipefail

# Deliberately not /usr/local/bin. The TCC grants below are keyed to this exact
# path with no code-signing requirement, so anything that can write to the
# directory inherits Full Disk Access and Automation over Notes without a
# prompt -- and on a Homebrew Mac /usr/local/bin is group-writable by admin.
# libexec is root-owned and not on anyone's PATH.
PREFIX="${PREFIX:-/usr/local/libexec/applenotes}"
LABEL="dev.applenotes.notesd"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"
STATE="$HOME/.config/applenotes/installed"
TOKEN="$HOME/.config/applenotes/token"
# Empty by default: notesd resolves the tailnet interface itself, and falls
# back to loopback. Set ADDR to override.
ADDR="${ADDR:-}"
BIN="$PREFIX/notesd"

die() { echo "install: $*" >&2; exit 1; }

[[ "$(uname)" == "Darwin" ]] || die "this only runs on macOS"
[[ $EUID -ne 0 ]] || die "run this as yourself, not root; it will ask for sudo where it needs it"

# --- what this needs, and why ------------------------------------------------
#
# Reading NoteStore.sqlite needs Full Disk Access. Writing notes needs Automation
# access to Notes.app. Both live in TCC, whose databases are protected by SIP --
# so with SIP on, the only way to grant them is by clicking consent dialogs,
# which a background agent cannot do. That is the trade this project asks for.
#
# Checked fail-closed: anything other than an explicit "disabled" stops here,
# including csrutil being absent or printing a partial configuration.
sip="$(csrutil status 2>/dev/null || true)"
if [[ "$sip" != *"status: disabled"* ]]; then
	cat >&2 <<MSG
install: System Integrity Protection is not confirmed disabled.

  csrutil says: ${sip:-(csrutil produced no output)}

notesd needs Full Disk Access (to read the notes database) and Automation
access to Notes.app (to write notes). With SIP on, those can only be granted
by clicking consent dialogs, which a background agent cannot do.

You can either:
  - disable SIP (boot to Recovery, \`csrutil disable\`) and run this again, or
  - run notesd by hand from a Terminal window and click the prompts as they
    appear. It will work; it just will not survive a reboot unattended.

This script will not continue.
MSG
	exit 1
fi

# --- the binary --------------------------------------------------------------
tmp="$(mktemp -t notesd)"          # not a predictable /tmp path
trap 'rm -f "$tmp"' EXIT

if [[ -x "./notesd" ]]; then
	echo "install: using the prebuilt ./notesd"
	cp ./notesd "$tmp"
elif command -v go >/dev/null; then
	echo "install: building from source"
	go build -o "$tmp" ./cmd/notesd
else
	die "no ./notesd here and no go toolchain to build one.
     Download a release binary next to this script, or build it elsewhere with
       GOOS=darwin GOARCH=$(uname -m | sed 's/x86_64/amd64/') go build -o notesd ./cmd/notesd"
fi

echo "install: installing to $BIN"
sudo install -d -o root -g wheel -m 0755 "$PREFIX"
sudo install -o root -g wheel -m 0755 "$tmp" "$BIN"

# --- permissions -------------------------------------------------------------
USER_TCC="$HOME/Library/Application Support/com.apple.TCC/TCC.db"
SYS_TCC="/Library/Application Support/com.apple.TCC/TCC.db"

# Values are passed as bound parameters. Interpolating them would break on a
# path containing an apostrophe, and in the worst case would be SQL injection
# executed by root against TCC.
grant() { # db sudo service client [indirect]
	local db=$1 as=$2 service=$3 client=$4 indirect=${5:-}
	local sql
	if [[ -n "$indirect" ]]; then
		sql="INSERT OR REPLACE INTO access
		     (service, client, client_type, auth_value, auth_reason, auth_version,
		      indirect_object_identifier_type, indirect_object_identifier, flags, last_modified)
		     VALUES (?1, ?2, 1, 2, 3, 1, 0, ?3, 0, strftime('%s','now'));"
		$as sqlite3 "$db" -cmd ".param set ?1 $service" -cmd ".param set ?2 $client" \
			-cmd ".param set ?3 $indirect" "$sql"
	else
		sql="INSERT OR REPLACE INTO access
		     (service, client, client_type, auth_value, auth_reason, auth_version, flags, last_modified)
		     VALUES (?1, ?2, 1, 2, 3, 1, 0, strftime('%s','now'));"
		$as sqlite3 "$db" -cmd ".param set ?1 $service" -cmd ".param set ?2 $client" "$sql"
	fi
}

echo "install: granting Automation over Notes and Full Disk Access to $BIN"
# The user database is the user's: writing it as root leaves root-owned -wal and
# -shm files behind, after which the account's own tccd may be unable to write
# its database at all.
grant "$USER_TCC" ""     kTCCServiceAppleEvents           "$BIN" com.apple.Notes
grant "$SYS_TCC"  "sudo" kTCCServiceSystemPolicyAllFiles  "$BIN"
sudo killall tccd 2>/dev/null || true   # tccd caches; it must re-read

# --- the agent ---------------------------------------------------------------
mkdir -p "$(dirname "$PLIST")"
# -m with -p only applies to the deepest directory, so set the mode explicitly.
mkdir -p "$(dirname "$TOKEN")"
chmod 0700 "$(dirname "$TOKEN")"

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
${ADDR:+    <string>-addr</string><string>$ADDR</string>}
    <string>-token</string><string>$TOKEN</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>10</integer>
  <key>StandardOutPath</key><string>$HOME/Library/Logs/notesd.log</string>
  <key>StandardErrorPath</key><string>$HOME/Library/Logs/notesd.log</string>
</dict>
</plist>
PLISTEOF

# Record what was installed, so uninstall.sh reverses this install rather than
# the default one. A stale TCC grant pointing at a path is a capability.
printf 'bin=%s\nplist=%s\nlabel=%s\n' "$BIN" "$PLIST" "$LABEL" > "$STATE"

launchctl unload "$PLIST" 2>/dev/null || true
launchctl load "$PLIST"

# launchctl list reports a crash-looping job as present, so ask the daemon
# itself rather than trusting that it is there. notesd generates the token on
# its first run, so this waits for the file to appear before using it -- reading
# it too early would send an empty bearer and report a false failure.
ok=""
for _ in $(seq 1 20); do
	sleep 0.5
	[[ -s "$TOKEN" ]] || continue
	# The daemon logs the address it chose; ask it where it landed.
	listening="$(sed -n -e 's/.*binding the tailnet address //p' -e 's/.*binding loopback.*/127.0.0.1/p' \
		"$HOME/Library/Logs/notesd.log" 2>/dev/null | tail -1)"
	[[ -n "$listening" ]] || listening="127.0.0.1"
	if curl -fsS -m 3 -H "Authorization: Bearer $(cat "$TOKEN")" \
		"http://${listening}:8437/v1/healthz" >/dev/null 2>&1; then
		ok=yes
		break
	fi
done
if [[ -z "$ok" ]]; then
	echo "install: the agent did not answer within 10s" >&2
	echo "install: see $HOME/Library/Logs/notesd.log" >&2
	echo "install: run ./uninstall.sh to undo this" >&2
	exit 1
fi

ts="$(tailscale ip -4 2>/dev/null || /Applications/Tailscale.app/Contents/MacOS/tailscale ip -4 2>/dev/null || true)"

cat <<MSG

install: notesd is running on ${listening}:8437
install: token is in $TOKEN

MSG

if [[ -n "$ts" ]]; then
	cat <<MSG
This Mac is on a tailnet at $ts, which is where notesd bound itself. From the
Linux side:

  export NOTESD_URL=http://$ts:8437
  export NOTESD_TOKEN=\$(ssh $(hostname -s | tr "[:upper:]" "[:lower:]") cat $TOKEN)
  notes list

MSG
else
	cat <<MSG
notesd is on loopback, so nothing else can reach it yet. Put it on a tailnet
and re-run with ADDR set to this Mac's tailnet address.

MSG
fi

cat <<'MSG'
Keep Notes.app running: it persists changes on its own schedule, and a write
is not visible to reads until it does.
MSG
