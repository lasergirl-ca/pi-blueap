#!/usr/bin/env bash
# palm-gw developer installer (PLAN.md section 8.11, Path C). Idempotent: every step says what
# it did or that it was already done. Run as root on the Pi from a checkout that has the arm64
# binaries in dist/linux-arm64/ (from `make build-arm64` on the development machine).
#
#   sudo make install [ADAPTER=onboard|dongle] [UPLINK=eth0|wlan0] [CONFIG=path/to/config.toml]
#
# ADAPTER unset keeps the current adapter mode (onboard on a first install).
#
# Enables the bridge, dnsmasq, nftables and palm-gwd units (ENABLE_PALM_GWD=0 to leave the daemon off).
set -euo pipefail

ADAPTER=${ADAPTER:-}
UPLINK=${UPLINK:-}
CONFIG=${CONFIG:-}
ENABLE_PALM_GWD=${ENABLE_PALM_GWD:-1}
VERSION=${VERSION:-dev}

REPO=$(cd "$(dirname "$0")/.." && pwd)
BIN_DIR=/usr/local/bin
ETC=/etc/palm-gw
STATE=/var/lib/palm-gw
TRACK=$STATE/installed-files.txt
REBOOT_REQUIRED=0

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

# ---------------------------------------------------------------- 1. preconditions
[ "$(id -u)" -eq 0 ] || die "run as root (sudo make install)"
[ "$(uname -s)" = Linux ] || die "Linux only; never run make install on the development machine"
arch=$(uname -m)
[ "$arch" = aarch64 ] || die "64-bit OS required (uname -m is $arch; Raspberry Pi OS Lite 64-bit)"
BOARD=$(ALLOW_UNSUPPORTED=1 "$REPO/scripts/detect-board.sh")
[ "$BOARD" = other ] && warn "unsupported board; continuing anyway"
say "Board: $BOARD, kernel $(uname -r)"
[ -f /boot/firmware/config.txt ] || die "/boot/firmware/config.txt not found: Raspberry Pi OS Bookworm or newer is required"
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

# ---------------------------------------------------------------- 5. static files
# Units, the bridge helper, NetworkManager's unmanaged list, modules-load and the pppd options,
# rendered for the source layout (/usr/local, /etc) and recorded in the manifest (D-042).
"$BIN_DIR/palm-gw" setup --install-files --layout source

# ---------------------------------------------------------------- 6. config
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

# ---------------------------------------------------------------- 7. apply
# Everything rendered from config.toml (dnsmasq, nftables, sysctl, bridge.env, the main.conf
# block, the adapter mode in config.txt), then the units in order and install-info.json. The
# same step the wizard and the .deb's postinst run. It ends with detection's warnings (Wi-Fi band,
# Pi 4 console UART, power supply).
apply_args=(--apply)
[ -n "$ADAPTER" ] && apply_args+=(--adapter-mode "$ADAPTER")
[ "$ENABLE_PALM_GWD" = 1 ] || apply_args+=(--no-start)
apply_out=$(mktemp)
"$BIN_DIR/palm-gw" setup "${apply_args[@]}" | tee "$apply_out" | grep -v "^Reboot required:"
grep -q '^Reboot required: YES' "$apply_out" && REBOOT_REQUIRED=1
rm -f "$apply_out"
if [ "$ENABLE_PALM_GWD" != 1 ]; then
  systemctl disable --now palm-gwd >/dev/null 2>&1 || true
  note "palm-gwd installed but not enabled (ENABLE_PALM_GWD=0)"
fi

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
[ "$REBOOT_REQUIRED" = 1 ] && echo "Reboot required: YES (the adapter mode changed)" || echo "Reboot required: no"
