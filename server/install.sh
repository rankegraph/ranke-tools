#!/usr/bin/env bash
# Install (or update) the ranke-db server binary to the version server/.rankedb-version
# pins — never built from source, since the release binary is what a client of the
# server is meant to run. The pin is what `make upgrade` moves; this script only ever
# catches the binary up to it. No network call when the two already agree.
#
# Usage: server/install.sh
set -euo pipefail

REPO="rankegraph/ranke-db"
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN="$DIR/rankedb"
VERSION_FILE="$DIR/.rankedb-version"
INSTALLED_FILE="$DIR/.rankedb-installed"

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"
case "$arch" in
x86_64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*)
	echo "install.sh: unsupported architecture '$arch' — ranke-db ships linux/darwin, amd64/arm64 only" >&2
	exit 1
	;;
esac
asset="ranke-db-${os}-${arch}"

pinned="$(cat "$VERSION_FILE" 2>/dev/null || true)"
[ -n "$pinned" ] || { echo "install.sh: $VERSION_FILE is missing or empty — run 'make upgrade' first" >&2; exit 1; }

installed="$(cat "$INSTALLED_FILE" 2>/dev/null || true)"
if [ -x "$BIN" ] && [ "$installed" = "$pinned" ]; then
	echo ">> ranke-db $pinned already installed"
	exit 0
fi

command -v curl >/dev/null 2>&1 || { echo "install.sh needs curl" >&2; exit 1; }

[ -n "$installed" ] && echo ">> installing ranke-db $installed -> $pinned" || echo ">> installing ranke-db $pinned"
url="https://github.com/$REPO/releases/download/$pinned/$asset"
tmp="$BIN.new"
curl -fsSL "$url" -o "$tmp" || { rm -f "$tmp"; echo "install.sh: download failed: $url" >&2; exit 1; }
chmod +x "$tmp"
mv -f "$tmp" "$BIN" # atomic within DIR
echo "$pinned" >"$INSTALLED_FILE"
