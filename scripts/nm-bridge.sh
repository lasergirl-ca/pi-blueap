#!/bin/sh
# palm-gw: make sure the Palm bridge exists in NetworkManager with the gateway address.
# Idempotent; run by palm-gwd-nm-bridge.service before palm-gwd and dnsmasq.
# Usage: nm-bridge.sh BRIDGE GATEWAY PREFIX
set -eu
BRIDGE=$1
GATEWAY=$2
PREFIX=$3

if ! nmcli -t -f NAME con show | grep -qx "$BRIDGE"; then
  echo "nm-bridge: creating $BRIDGE ($GATEWAY/$PREFIX)"
  nmcli con add type bridge ifname "$BRIDGE" con-name "$BRIDGE" \
      ipv4.method manual ipv4.addresses "$GATEWAY/$PREFIX" \
      ipv6.method disabled bridge.stp no connection.autoconnect yes
else
  # Keep the address in step with config.toml.
  nmcli con modify "$BRIDGE" ipv4.method manual ipv4.addresses "$GATEWAY/$PREFIX" \
      ipv6.method disabled bridge.stp no connection.autoconnect yes
fi
nmcli con up "$BRIDGE" >/dev/null
echo "nm-bridge: $BRIDGE is up"
