#!/usr/bin/env bash
# Run the ranke-db dev server: install.sh catches the binary up to what
# server/.rankedb-version pins, then this runs it against config.json — an
# ephemeral, in-memory instance, the same shape as ranke-db's own `make dev`: a
# throwaway signing key, nothing persisted between runs.
#
# The pin only moves via `make upgrade`, not on every run: a dev server that
# silently followed GitHub's latest on each launch could change under you
# mid-session, and would need the network to start at all. `make upgrade` is the
# deliberate "take the new latest" step — and the point of testing against it is
# that ranke-tools' own tests should fail if they're not compatible with it.
#
# Usage: server/run.sh
#        PORT=8123 server/run.sh   # a second, independent instance alongside the default
#
# Refuses to start a second instance on the same port while one is already running
# (pid file, named per port) — server/stop.sh (with the same PORT, if set) ends it.
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN="$DIR/rankedb"
CONFIG="$DIR/config.json"

command -v openssl >/dev/null 2>&1 || { echo "run.sh needs openssl to mint a throwaway signing key" >&2; exit 1; }

# --- resolve the port ---------------------------------------------------------
addr="$(grep -o '"addr"[[:space:]]*:[[:space:]]*"[^"]*"' "$CONFIG" | head -1 | sed -E 's/.*"([^"]*)"$/\1/')"
addr="${addr:-:8080}"
[ -n "${PORT:-}" ] && addr=":$PORT"
PID_FILE="$DIR/.rankedb${PORT:+.$PORT}.pid"

# --- refuse a second instance on this port -----------------------------------
if [ -f "$PID_FILE" ]; then
	pid="$(cat "$PID_FILE" 2>/dev/null || true)"
	if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
		echo "run.sh: already running on ${addr} (pid $pid) — server/stop.sh to stop it, or remove $PID_FILE if it's stale" >&2
		exit 1
	fi
	rm -f "$PID_FILE"
fi

"$DIR/install.sh"

# --- run ------------------------------------------------------------------
# PORT patches a throwaway copy of config.json rather than the committed
# file itself, so the default `server/run.sh` invocation stays byte-for-byte
# what it always ran.
run_config="$CONFIG"
if [ -n "${PORT:-}" ]; then
	run_config="$(mktemp "${TMPDIR:-/tmp}/rankedb-config.XXXXXX.json")"
	sed -E "s/(\"addr\"[[:space:]]*:[[:space:]]*)\"[^\"]*\"/\1\"$addr\"/" "$CONFIG" >"$run_config"
fi
url="http://localhost${addr}"
echo ">> $CONFIG — ephemeral signing key, in-memory storage, nothing persisted between runs"
echo ">> serving on  $url"
echo ">> explorer at $url/explorer"
echo ">> try:  curl $url/health  ·  curl $url/branches  ·  curl $url/branches/main/head"
echo ">> ctrl-c (or server/stop.sh from another shell) to stop"

# In-memory storage means every launch starts on an empty bookmark list, holding no
# archive until one is founded. The founder's public half is all the server wants; the
# private half goes with the process, since the tools mint their own contributors.
founder_pubkey="$(openssl genpkey -algorithm ed25519 | openssl pkey -pubout)"

RANKE_SIGNER_KEY="$(openssl genpkey -algorithm ed25519)" RANKE_FOUNDER_PUBKEY="$founder_pubkey" "$BIN" run --dev "$run_config" &
child=$!
echo "$child" >"$PID_FILE"
trap 'kill "$child" 2>/dev/null || true; rm -f "$PID_FILE"; [ "$run_config" = "$CONFIG" ] || rm -f "$run_config"' EXIT INT TERM
wait "$child"
