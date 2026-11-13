#!/bin/sh
# palm-gw: make sure the Palm bridge exists in NetworkManager with the gateway address.
# Idempotent; run by palm-gwd-nm-bridge.service before palm-gwd and dnsmasq.
# Usage: nm-bridge.sh BRIDGE GATEWAY PREFIX
# WG_RULES and WG_ROUTES (from bridge.env, empty unless WireGuard is on) are the
# Palm network's policy routing into the tunnel (D-069); empty clears them.
set -eu
BRIDGE=$1
GATEWAY=$2
PREFIX=$3
RULES=${WG_RULES:-}
ROUTES=${WG_ROUTES:-}

if ! nmcli -t -f NAME con show | grep -qx "$BRIDGE"; then
  echo "nm-bridge: creating $BRIDGE ($GATEWAY/$PREFIX)"
  nmcli con add type bridge ifname "$BRIDGE" con-name "$BRIDGE" \
      ipv4.method manual ipv4.addresses "$GATEWAY/$PREFIX" \
      ipv4.routing-rules "$RULES" ipv4.routes "$ROUTES" \
      ipv6.method disabled bridge.stp no connection.autoconnect yes
else
  # Keep the address in step with config.toml.
  nmcli con modify "$BRIDGE" ipv4.method manual ipv4.addresses "$GATEWAY/$PREFIX" \
      ipv4.routing-rules "$RULES" ipv4.routes "$ROUTES" \
      ipv6.method disabled bridge.stp no connection.autoconnect yes
fi
nmcli con up "$BRIDGE" >/dev/null
echo "nm-bridge: $BRIDGE is up"
