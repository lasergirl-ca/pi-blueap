#!/usr/bin/env bash
# palm-gw developer installer (PLAN.md section 8.11, Path C). Idempotent: every step says what
# it did or that it was already done. Run as root on the Pi from a checkout that has the arm64
# binaries in dist/linux-arm64/ (from `make build-arm64` on the development machine).
#
#   sudo make install [ADAPTER=onboard|dongle] [UPLINK=eth0|wlan0] [CONFIG=path/to/config.toml]
#
# Enables the bridge, dnsmasq, nftables and palm-gwd units (ENABLE_PALM_GWD=0 to leave the daemon off).
set -euo pipefail

ADAPTER=${ADAPTER:-onboard}
UPLINK=${UPLINK:-}
CONFIG=${CONFIG:-}
ENABLE_PALM_GWD=${ENABLE_PALM_GWD:-1}
VERSION=${VERSION:-dev}

REPO=$(cd "$(dirname "$0")/.." && pwd)
BIN_DIR=/usr/local/bin
LIB_DIR=/usr/local/lib/palm-gw
ETC=/etc/palm-gw
STATE=/var/lib/palm-gw
TRACK=$STATE/installed-files.txt
BOOT_CONFIG=/boot/firmware/config.txt
BOOT_CMDLINE=/boot/firmware/cmdline.txt
REBOOT_REQUIRED=0
ADDED_DISABLE_BT=0

say()  { printf '==> %s\n' "$*"; }
note() { printf '    %s\n' "$*"; }
warn() { printf 'WARNING: %s\n' "$*" >&2; }
die()  { printf 'install.sh: %s\n' "$*" >&2; exit 1; }

track() { grep -qxF -- "$1" "$TRACK" 2>/dev/null || echo "$1" >> "$TRACK"; }

# place SRC DST MODE: copy a file, tracking it. Says whether it changed.
place() {
  local src=$1 dst=$2 mode=$3
  install -d "$(dirname "$dst")"
  if [ -f "$dst" ] && cmp -s "$src" "$dst"; then
    note "$dst unchanged"
  else
    install -m "$mode" "$src" "$dst"
    note "$dst written"
  fi
  track "$dst"
}

# render SRC DST MODE: render a deploy/ template through palm-gwd using the live config.
render() {
  local src=$1 dst=$2 mode=$3 tmp
  tmp=$(mktemp)
  "$BIN_DIR/palm-gwd" -config "$ETC/config.toml" -render "$src" > "$tmp"
  place "$tmp" "$dst" "$mode"
  rm -f "$tmp"
}

# ---------------------------------------------------------------- 1. preconditions
[ "$(id -u)" -eq 0 ] || die "run as root (sudo make install)"
[ "$(uname -s)" = Linux ] || die "Linux only; never run make install on the development machine"
arch=$(uname -m)
[ "$arch" = aarch64 ] || die "64-bit OS required (uname -m is $arch; Raspberry Pi OS Lite 64-bit)"
BOARD=$(ALLOW_UNSUPPORTED=1 "$REPO/scripts/detect-board.sh")
[ "$BOARD" = other ] && warn "unsupported board; continuing anyway"
say "Board: $BOARD, kernel $(uname -r)"
[ -f "$BOOT_CONFIG" ] || die "$BOOT_CONFIG not found: Raspberry Pi OS Bookworm or newer is required"
command -v systemctl >/dev/null || die "systemd is required"
command -v apt-get   >/dev/null || die "apt is required"

# ---------------------------------------------------------------- 2. packages
say "Installing packages"
export DEBIAN_FRONTEND=noninteractive
pkgs=(bluez ppp dnsmasq-base nftables)
if ! command -v nmcli >/dev/null; then
  warn "NetworkManager is not installed; installing it changes how this Pi manages its network"
  pkgs+=(network-manager)
fi
missing=()
for p in "${pkgs[@]}"; do dpkg -s "$p" >/dev/null 2>&1 || missing+=("$p"); done
if [ ${#missing[@]} -gt 0 ]; then
  apt-get update -qq
  apt-get install -y -qq "${missing[@]}"
  note "installed: ${missing[*]}"
else
  note "all present: ${pkgs[*]}"
fi

# ---------------------------------------------------------------- 3. group
say "Operator group"
if getent group palm-gw >/dev/null; then note "group palm-gw exists"; else groupadd --system palm-gw; note "group palm-gw created"; fi
if [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != root ]; then
  if id -nG "$SUDO_USER" | tr ' ' '\n' | grep -qx palm-gw; then
    note "$SUDO_USER already in palm-gw"
  else
    usermod -aG palm-gw "$SUDO_USER"; note "$SUDO_USER added to palm-gw (takes effect at next login)"
  fi
fi
install -d -m 0750 "$STATE"
touch "$TRACK"

# ---------------------------------------------------------------- 4. binaries
say "Binaries"
src_dir=""
for d in "$REPO/dist/linux-arm64" "$REPO/bin"; do
  if [ -x "$d/palm-gwd" ] && file "$d/palm-gwd" 2>/dev/null | grep -q 'ARM aarch64\|aarch64'; then src_dir=$d; break; fi
  if [ -x "$d/palm-gwd" ] && ! command -v file >/dev/null; then src_dir=$d; break; fi
done
if [ -z "$src_dir" ]; then
  if command -v go >/dev/null; then
    note "no prebuilt binaries; building with $(go version | cut -d' ' -f3)"
    (cd "$REPO" && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$REPO/bin/" ./cmd/...)
    src_dir=$REPO/bin
  else
    die "no arm64 binaries in dist/linux-arm64/ and no Go toolchain; run 'make build-arm64' on the development machine and copy dist/"
  fi
fi
for b in palm-gwd palm-gwctl palm-gw fake-palm; do place "$src_dir/$b" "$BIN_DIR/$b" 0755; done
install -d "$LIB_DIR"
place "$REPO/scripts/nm-bridge.sh" "$LIB_DIR/nm-bridge.sh" 0755
place "$REPO/scripts/detect-board.sh" "$LIB_DIR/detect-board.sh" 0755

# ---------------------------------------------------------------- 5. config
say "Configuration"
install -d -m 0750 "$ETC"
if [ -f "$ETC/config.toml" ]; then
  note "$ETC/config.toml exists, keeping it"
  [ -n "$UPLINK" ] && warn "UPLINK=$UPLINK ignored because a config already exists; edit $ETC/config.toml"
else
  if [ -z "$UPLINK" ]; then
    UPLINK=$(ip -o route show default 2>/dev/null | awk '{print $5}' | head -1)
    UPLINK=${UPLINK:-eth0}
    note "uplink not given; using the current default-route interface: $UPLINK"
  fi
  src=${CONFIG:-$REPO/config.example.toml}
  [ -f "$src" ] || die "config source $src not found"
  sed -E "s|^(uplink[[:space:]]*=[[:space:]]*)\"[^\"]*\"|\1\"$UPLINK\"|" "$src" > "$ETC/config.toml"
  chmod 0640 "$ETC/config.toml"
  note "$ETC/config.toml written from $(basename "$src") with uplink = \"$UPLINK\""
fi
"$BIN_DIR/palm-gwd" -config "$ETC/config.toml" -check
UPLINK=$("$BIN_DIR/palm-gwd" -config "$ETC/config.toml" -render <(printf '{{UPLINK}}'))

# ---------------------------------------------------------------- 6. system files
say "System files"
render "$REPO/deploy/systemd/palm-gwd.service"            /etc/systemd/system/palm-gwd.service 0644
render "$REPO/deploy/systemd/palm-gwd-nm-bridge.service"  /etc/systemd/system/palm-gwd-nm-bridge.service 0644
render "$REPO/deploy/systemd/palm-gw-dnsmasq.service"     /etc/systemd/system/palm-gw-dnsmasq.service 0644
render "$REPO/deploy/NetworkManager/conf.d/palm-gw.conf"  /etc/NetworkManager/conf.d/palm-gw.conf 0644
render "$REPO/deploy/dnsmasq/palm-gw.conf.tmpl"           "$ETC/dnsmasq.conf" 0644
render "$REPO/deploy/sysctl.d/90-palm-gw.conf"            /etc/sysctl.d/90-palm-gw.conf 0644
render "$REPO/deploy/modules-load.d/palm-gw.conf"         /etc/modules-load.d/palm-gw.conf 0644
render "$REPO/deploy/ppp/options.palm"                    /etc/ppp/options.palm 0644

# ---------------------------------------------------------------- 6b. bluetooth main.conf
say "BlueZ main.conf managed block"
MAINCONF=/etc/bluetooth/main.conf
if [ -f "$MAINCONF" ] && [ ! -f "$STATE/main.conf.orig" ]; then
  cp -p "$MAINCONF" "$STATE/main.conf.orig"   # belt and braces; the block is reversible on its own
fi
track "$MAINCONF"   # listed so uninstall knows to strip the block (it never deletes this file)
mc=$("$BIN_DIR/palm-gwd" -config "$ETC/config.toml" -state "$STATE" -main-conf "$MAINCONF")
BLUETOOTH_RESTART=0
if [ "$mc" = changed ]; then note "$MAINCONF: managed block written (Name, Class, timeouts, JustWorksRepairing, AutoEnable)"; BLUETOOTH_RESTART=1; else note "$MAINCONF: managed block unchanged"; fi

for m in bnep rfcomm ppp_generic ppp_async; do modprobe "$m" 2>/dev/null || warn "modprobe $m failed"; done
# The bridge key is applied again after the units start (the bridge may not exist yet here).
sysctl -q -p /etc/sysctl.d/90-palm-gw.conf 2>/dev/null || true

# ---------------------------------------------------------------- 7. nftables
say "nftables"
install -d /etc/nftables.d
render "$REPO/deploy/nftables/palm-gw.nft.tmpl" /etc/nftables.d/palm-gw.nft 0644
if [ ! -f /etc/nftables.conf ]; then
  printf '#!/usr/sbin/nft -f\nflush ruleset\n' > /etc/nftables.conf
  chmod 0755 /etc/nftables.conf
  note "/etc/nftables.conf created"
fi
include_line='include "/etc/nftables.d/palm-gw.nft"'
if grep -qxF "$include_line" /etc/nftables.conf; then
  note "include line already in /etc/nftables.conf"
else
  printf '\n# palm-gw\n%s\n' "$include_line" >> /etc/nftables.conf
  note "include line appended to /etc/nftables.conf"
fi
nft -c -f /etc/nftables.conf

# ---------------------------------------------------------------- 8. adapter mode
say "Bluetooth adapter: $ADAPTER"
case "$ADAPTER" in
  onboard) note "onboard radio stays enabled" ;;
  dongle)
    if grep -qE '^\s*dtoverlay=disable-bt\s*$' "$BOOT_CONFIG"; then
      note "dtoverlay=disable-bt already in $BOOT_CONFIG"
    else
      printf '\n# palm-gw: onboard Bluetooth disabled so the USB dongle is hci0\ndtoverlay=disable-bt\n' >> "$BOOT_CONFIG"
      ADDED_DISABLE_BT=1; REBOOT_REQUIRED=1
      note "dtoverlay=disable-bt added to $BOOT_CONFIG"
    fi
    systemctl disable --now hciuart 2>/dev/null || true
    ;;
  *) die "ADAPTER must be onboard or dongle" ;;
esac
if [ "$BOARD" = pi4 ] && [ -f "$BOOT_CMDLINE" ] && grep -qE 'console=(serial0|ttyAMA0)' "$BOOT_CMDLINE"; then
  warn "Pi 4: a serial console on serial0/ttyAMA0 in $BOOT_CMDLINE can conflict with the onboard Bluetooth UART (PLAN.md section 3)"
fi
case "$UPLINK" in
  wlan*)
    if command -v iw >/dev/null; then
      freq=$(iw dev "$UPLINK" link 2>/dev/null | sed -n 's/.*freq: *\([0-9]*\).*/\1/p' | head -1)
      if [ -n "$freq" ] && [ "$freq" -lt 3000 ] && [ "$ADAPTER" = onboard ]; then
        warn "uplink $UPLINK is on 2.4 GHz ($freq MHz) with the onboard adapter; Bluetooth 1.1 Palms will suffer. Prefer Ethernet, 5 GHz, or a USB dongle."
      fi
    fi ;;
esac

# ---------------------------------------------------------------- 9. units
say "Units"
systemctl daemon-reload
if [ "$BLUETOOTH_RESTART" = 1 ] || ! systemctl is-active --quiet bluetooth; then
  systemctl restart bluetooth; note "bluetooth restarted"
else
  note "bluetooth left running (main.conf unchanged)"
fi
systemctl enable --now nftables >/dev/null 2>&1 && note "nftables enabled"
systemctl reload-or-restart nftables
systemctl enable --now palm-gwd-nm-bridge >/dev/null 2>&1 && note "palm-gwd-nm-bridge enabled and started"
systemctl enable --now palm-gw-dnsmasq   >/dev/null 2>&1 && note "palm-gw-dnsmasq enabled and started"
sysctl -q -p /etc/sysctl.d/90-palm-gw.conf 2>/dev/null || true
note "sysctl applied: ip_forward=$(sysctl -n net.ipv4.ip_forward) rp_filter=$(sysctl -n net.ipv4.conf.all.rp_filter)"
if [ "$ENABLE_PALM_GWD" = 1 ]; then
  systemctl enable palm-gwd >/dev/null 2>&1
  systemctl restart palm-gwd && note "palm-gwd enabled and (re)started"
else
  systemctl disable --now palm-gwd >/dev/null 2>&1 || true
  note "palm-gwd installed but not enabled (ENABLE_PALM_GWD=0)"
fi

# ---------------------------------------------------------------- 10. install-info
say "Recording install-info.json"
adapters=$(timeout 5 bluetoothctl list 2>/dev/null | sed 's/"/\\"/g' | paste -sd'|' - || true)
bluez=$(bluetoothctl --version 2>/dev/null | awk '{print $NF}' || true)
pppd_v=$(pppd --version 2>&1 | awk '{print $NF}' | head -1 || true)
nm_v=$(nmcli --version 2>/dev/null | awk '{print $NF}' || true)
prev_added=0
[ -f "$STATE/install-info.json" ] && grep -q '"added_disable_bt": *true' "$STATE/install-info.json" && prev_added=1
[ "$prev_added" = 1 ] && ADDED_DISABLE_BT=1
cat > "$STATE/install-info.json" <<JSON
{
  "installed_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "version": "$VERSION",
  "install_path": "source",
  "board": "$BOARD",
  "model": "$(tr -d '\0' < /proc/device-tree/model 2>/dev/null || echo unknown)",
  "kernel": "$(uname -r)",
  "os": "$(. /etc/os-release 2>/dev/null && echo "$PRETTY_NAME")",
  "bluez": "$bluez",
  "pppd": "$pppd_v",
  "networkmanager": "$nm_v",
  "nftables": "$(nft --version 2>/dev/null | awk '{print $2}')",
  "dnsmasq": "$(dnsmasq --version 2>/dev/null | head -1 | awk '{print $3}')",
  "adapter_mode": "$ADAPTER",
  "adapters": "$adapters",
  "uplink": "$UPLINK",
  "added_disable_bt": $([ "$ADDED_DISABLE_BT" = 1 ] && echo true || echo false)
}
JSON
track "$STATE/install-info.json"

# ---------------------------------------------------------------- done
say "Done"
cat <<TXT

palm-gw $VERSION is installed.
  Config:    $ETC/config.toml
  Status:    palm-gwctl status      (as a member of the palm-gw group, or with sudo)
  Pair:      palm-gwctl pair        prints the PIN to enter on the Palm
  Units:     systemctl status palm-gwd nftables palm-gw-dnsmasq palm-gwd-nm-bridge
  Logs:      journalctl -fu palm-gwd
  Uninstall: sudo make uninstall  (PURGE=1 also removes config and state)
TXT
[ "$REBOOT_REQUIRED" = 1 ] && echo "Reboot required: YES (onboard Bluetooth disabled for dongle mode)" || echo "Reboot required: no"
