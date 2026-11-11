#!/usr/bin/env bash
# palm-gw uninstaller for source installs (PLAN.md section 8.11, `make uninstall`). The work is
# `palm-gw setup --remove`: it stops the units, deletes the bridge connection, strips the
# main.conf block and the nftables include, takes out dtoverlay=disable-bt only if palm-gw added
# it, and deletes every file in /var/lib/palm-gw/installed-files.txt, binaries included.
# Config and paired devices are kept unless PURGE=1.
set -euo pipefail
[ "$(id -u)" -eq 0 ] || { echo "run as root" >&2; exit 1; }
BIN=/usr/local/bin/palm-gw
if [ ! -x "$BIN" ]; then
  echo "uninstall.sh: $BIN not found; nothing to remove (or the .deb is installed: use apt remove)" >&2
  exit 1
fi
args=(setup --remove)
[ "${PURGE:-0}" = 1 ] && args+=(--purge)
"$BIN" "${args[@]}"
echo "==> Done"
