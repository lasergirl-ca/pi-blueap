#!/usr/bin/env bash
# palm-gw uninstaller for source installs: stops units, removes every file install.sh placed
# (tracked in /var/lib/palm-gw/installed-files.txt), the nftables include line, the bridge
# connection, and the dtoverlay=disable-bt line only if install-info.json says we added it.
# Config and state are kept unless PURGE=1.
set -euo pipefail
STATE=/var/lib/palm-gw
ETC=/etc/palm-gw
TRACK=$STATE/installed-files.txt
BOOT_CONFIG=/boot/firmware/config.txt
say() { printf '==> %s\n' "$*"; }
[ "$(id -u)" -eq 0 ] || { echo "run as root" >&2; exit 1; }

say "Stopping units"
for u in palm-gwd palm-gw-dnsmasq palm-gwd-nm-bridge; do
  systemctl disable --now "$u" 2>/dev/null || true
done

bridge=$(sed -n 's/^.*bridge *= *"\([^"]*\)".*$/\1/p' "$ETC/config.toml" 2>/dev/null | head -1)
bridge=${bridge:-br-palm}
if command -v nmcli >/dev/null && nmcli -t -f NAME con show | grep -qx "$bridge"; then
  nmcli con delete "$bridge" >/dev/null && say "Removed NetworkManager connection $bridge"
fi

say "Removing firewall table"
nft delete table inet palm_gw 2>/dev/null || true
if [ -f /etc/nftables.conf ]; then
  sed -i '/^# palm-gw$/d; /^include "\/etc\/nftables.d\/palm-gw.nft"$/d' /etc/nftables.conf
fi

say "Removing files"
if [ -f "$TRACK" ]; then
  while IFS= read -r f; do
    case "$f" in "$STATE"/*|"$ETC"/*) continue ;; esac
    [ -e "$f" ] && rm -f "$f" && echo "    $f"
  done < "$TRACK"
fi
rmdir /usr/local/lib/palm-gw 2>/dev/null || true
systemctl daemon-reload
systemctl reload-or-restart nftables 2>/dev/null || true

if [ -f "$STATE/install-info.json" ] && grep -q '"added_disable_bt": *true' "$STATE/install-info.json"; then
  sed -i '/^# palm-gw: onboard Bluetooth disabled/d; /^dtoverlay=disable-bt$/d' "$BOOT_CONFIG"
  say "Removed dtoverlay=disable-bt from $BOOT_CONFIG (reboot to re-enable the onboard radio)"
fi

if [ "${PURGE:-0}" = 1 ]; then
  rm -rf "$ETC" "$STATE"
  say "Purged $ETC and $STATE"
else
  rm -f "$ETC/dnsmasq.conf" "$TRACK"
  say "Kept $ETC/config.toml and $STATE (PURGE=1 removes them)"
fi
say "Done"
