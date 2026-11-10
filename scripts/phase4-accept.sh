#!/usr/bin/env bash
# Phase 4 acceptance on the gateway host (PLAN.md section 10, Phase 4; D-027, D-031, D-032).
#
# fake-palm (second adapter) connects as a PANU to the gateway's NAP, and its bnep interface is
# moved into network namespace $NETNS so the lease and routes live apart from the gateway's. The
# "LAN" is the stand-in host from Phase 3 (namespace $LANNS, $LAN_HOST), so nothing on the real
# LAN is scanned. Needs busybox (udhcpc), nmap and dig. Run as the operator user on the Pi:
#
#   URL=http://example.com/ scripts/phase4-accept.sh
#
# Output is shown and saved to /var/tmp/phase4-accept-<time>.log. Exit status is the number of
# failed checks. The D-032 step stops palm-gwd briefly and restores its state file afterwards.

set -u

CLIENT_HCI=${CLIENT_HCI:-hci1}
NETNS=${NETNS:-fakepalm}
LANNS=${LANNS:-palmlan}
LAN_PI=${LAN_PI:-192.168.77.1}
LAN_HOST=${LAN_HOST:-192.168.77.2}
FAKE_PALM=${FAKE_PALM:-$HOME/fake-palm}
URL=${URL:-http://neverssl.com/}
POOL=${POOL:-10.42.0.100-10.42.0.199}
STATE=/var/lib/palm-gw/state.json
STATE_BACKUP=/var/tmp/phase4-state.json.orig
LOG=/var/tmp/phase4-accept-$(date +%Y%m%d-%H%M%S).log

exec 9>/var/tmp/phase4-accept.lock
if ! flock -n 9; then
	echo "another phase4-accept.sh is running" >&2
	exit 1
fi
exec > >(tee "$LOG") 2>&1

fails=0
started=$(date '+%F %T')
step() { printf '\n==> %s\n' "$*"; }
ok() { printf '    PASS: %s\n' "$*"; }
bad() { printf '    FAIL: %s\n' "$*"; fails=$((fails + 1)); }
run() { printf '$ %s\n' "$*"; "$@"; }
inns() { sudo ip netns exec "$NETNS" "$@"; }
journal() { journalctl -u palm-gwd --since "$1" --no-pager -o cat; }

pan_pid=""
stop_pan() {
	if [ -n "$pan_pid" ]; then
		sudo pkill -f "^(sudo )?$FAKE_PALM pan " 2>/dev/null
		wait "$pan_pid" 2>/dev/null
		pan_pid=""
	fi
}
cleanup() {
	set +e
	stop_pan
	if [ -f "$STATE_BACKUP" ]; then
		sudo systemctl stop palm-gwd
		sudo cp -p "$STATE_BACKUP" "$STATE" && sudo rm -f "$STATE_BACKUP"
		sudo systemctl start palm-gwd
	fi
	sudo ip netns pids "$LANNS" 2>/dev/null | xargs -r sudo kill 2>/dev/null
	sudo ip netns del "$LANNS" 2>/dev/null
	sudo ip link del lan-pi 2>/dev/null
}
trap cleanup EXIT

counter() { sudo nft list chain inet palm_gw "$1" | sed -n "s/.*counter packets \([0-9]*\) .*comment \"$2\".*/\1/p"; }
scan() {
	inns nmap -sT -Pn -n --max-retries 1 --host-timeout 60s -p "$2" "$1" 2>&1 |
		awk '/^[0-9]+\/tcp/ { printf "%s/%s ", $1, $2 } END { print "" }'
}
allowlisted() { palm-gwctl status | grep -qE "^  $client_addr  (dynamic|static) "; }
pan_line() { palm-gwctl status | grep -E "^pan:"; }
nap_up() { pan_line | grep -q "NAP on"; }
wait_nap() {
	for _ in $(seq 1 40); do
		nap_up && return 0
		sleep 0.5
	done
	return 1
}
# start_pan HOLD: fake-palm pan in the background; output to /var/tmp/phase4-pan.txt
start_pan() {
	sudo "$FAKE_PALM" pan "$GW" --adapter "$CLIENT_HCI" --netns "$NETNS" --internet --url "$URL" \
		--pool "$POOL" --hold "$1" > /var/tmp/phase4-pan.txt 2>&1 &
	pan_pid=$!
}

step "Preconditions ($started)"
run palm-gwctl status | grep -E '^(palm-gwd|adapter|pan|firewall)'
gw_hci=$(palm-gwctl status | sed -n 's/^adapter: *\(hci[0-9]*\).*/\1/p')
GW=$(palm-gwctl status | sed -n 's/^adapter: *hci[0-9]* \([0-9A-F:]\{17\}\).*/\1/p')
for t in busybox nmap dig python3; do command -v "$t" > /dev/null || { bad "$t not installed"; exit 1; }; done
if [ -z "$GW" ] || [ "$gw_hci" = "$CLIENT_HCI" ] || [ ! -e "/sys/class/bluetooth/$CLIENT_HCI" ]; then
	bad "need the gateway on one adapter and $CLIENT_HCI present for fake-palm"
	exit 1
fi
rf=$(grep -lx "$CLIENT_HCI" /sys/class/rfkill/rfkill*/name 2>/dev/null | head -1)
if [ -n "$rf" ] && [ "$(cat "$(dirname "$rf")/soft")" = "1" ]; then
	echo "$CLIENT_HCI is soft-blocked by rfkill; unblocking"
	echo 0 | sudo tee "$(dirname "$rf")/soft" > /dev/null
fi
# busctl, not btmgmt: btmgmt waits forever without a terminal.
adapter_prop() { sudo busctl get-property org.bluez "/org/bluez/$CLIENT_HCI" org.bluez.Adapter1 "$1" | cut -d' ' -f2- | tr -d '"'; }
for _ in 1 2 3 4 5; do
	[ "$(adapter_prop Powered)" = true ] && break
	sudo busctl set-property org.bluez "/org/bluez/$CLIENT_HCI" org.bluez.Adapter1 Powered b true 2> /dev/null
	sleep 1
done
[ "$(adapter_prop Powered)" = true ] || { bad "$CLIENT_HCI does not power on"; exit 1; }
client_addr=$(adapter_prop Address)
echo "gateway $gw_hci $GW; fake-palm $CLIENT_HCI $client_addr"
if nap_up; then ok "NAP registered on the bridge"; else bad "NAP not registered"; exit 1; fi

step "fake-palm knows the gateway's NAP and is allowlisted (pair again if not)"
client_dev=/org/bluez/$CLIENT_HCI/dev_${GW//:/_}
if sudo busctl introspect org.bluez "$client_dev" 2> /dev/null | grep -q '^org.bluez.Network1' &&
	allowlisted; then
	ok "Network1 present on $client_dev, $client_addr allowlisted"
else
	echo "pairing again so $CLIENT_HCI fetches the gateway's service records (NAP included)"
	palm-gwctl pair 2 --one > /var/tmp/phase4-pair.txt 2>&1 &
	pair_pid=$!
	sleep 1
	run sudo "$FAKE_PALM" pair "$GW" --adapter "$CLIENT_HCI"
	wait "$pair_pid"
	cat /var/tmp/phase4-pair.txt
	if sudo busctl introspect org.bluez "$client_dev" | grep -q '^org.bluez.Network1'; then ok "Network1 present after pairing"; else bad "no Network1 after pairing"; exit 1; fi
fi

step "Stand-in LAN host $LAN_HOST in namespace $LANNS"
sudo ip netns add "$LANNS"
sudo ip link add lan-pi type veth peer name lan-host
sudo ip link set lan-host netns "$LANNS"
sudo ip addr add "$LAN_PI/24" dev lan-pi
sudo ip link set lan-pi up
sudo ip -n "$LANNS" addr add "$LAN_HOST/24" dev lan-host
sudo ip -n "$LANNS" link set lan-host up
sudo ip -n "$LANNS" link set lo up
sudo ip -n "$LANNS" route add default via "$LAN_PI"
for p in 22 80 443; do
	sudo ip netns exec "$LANNS" python3 -m http.server --bind "$LAN_HOST" "$p" > /dev/null 2>&1 &
done
for _ in $(seq 1 20); do
	got=$(sudo ip netns exec "$LANNS" nmap -sT -Pn -n -p 22,80,443 "$LAN_HOST" 2>&1 | awk '/^[0-9]+\/tcp/ { printf "%s/%s ", $1, $2 }')
	case "$got" in *closed*) sleep 0.5 ;; *) break ;; esac
done
case "$got" in *"22/tcp/open"*"80/tcp/open"*"443/tcp/open"*) ok "listeners on 22, 80, 443" ;; *) bad "listeners: $got" ;; esac

step "fake-palm pan: lease, ping, DNS, HTTP (held for the checks)"
start_pan 600s
for _ in $(seq 1 40); do
	grep -q "holding the connection" /var/tmp/phase4-pan.txt && break
	kill -0 "$pan_pid" 2> /dev/null || break
	sleep 0.5
done
cat /var/tmp/phase4-pan.txt
if grep -q "holding the connection" /var/tmp/phase4-pan.txt && ! grep -q "FAIL" /var/tmp/phase4-pan.txt; then
	ok "lease in $POOL, ping, DNS and GET $URL"
else
	bad "fake-palm pan"
	exit 1
fi
lease_ip=$(sed -n 's/.*leased address \([0-9.]*\) is in.*/\1/p' /var/tmp/phase4-pan.txt)
sleep 2 # one PAN loop tick for the lease to show
run palm-gwctl status | grep -A2 '^pan:'
if palm-gwctl status | grep -E "^  $client_addr .* pan bnep[0-9]+ +$lease_ip "; then
	ok "status lists $client_addr on a bnep port with $lease_ip"
else
	bad "status does not list the PAN client with its lease"
fi

step "bnep enslaved to the bridge, not managed by NetworkManager"
port=$(palm-gwctl status | sed -n "s/^  $client_addr .* pan \(bnep[0-9]*\) .*/\1/p")
run /usr/sbin/bridge link show dev "$port"
if /usr/sbin/bridge link show dev "$port" | grep -q "master br-palm"; then ok "$port is a port of br-palm"; else bad "$port not on br-palm"; fi
run nmcli -t -f DEVICE,TYPE,STATE device
nm=$(nmcli -t -f DEVICE,STATE device | grep "^$port:" || true)
case "$nm" in "" | *:unmanaged) ok "NetworkManager does not manage $port (${nm:-not listed})" ;; *) bad "NetworkManager: $nm" ;; esac

step "Isolation over PAN (same rules as PPP)"
in0=$(counter palm_input 'palm->pi')
t0=$(date +%s%N)
inns nc -z -w 3 10.42.0.1 22
rc=$?
ms=$((($(date +%s%N) - t0) / 1000000))
echo "nc -z -w 3 10.42.0.1 22: exit $rc after $ms ms"
if [ "$rc" != 0 ] && [ "$ms" -ge 2900 ]; then ok "ssh to 10.42.0.1 dropped"; else bad "ssh to 10.42.0.1 not dropped"; fi
pi_lan=$(ip -4 -o addr show "$(palm-gwctl status | sed -n 's/^network:.* uplink \([^ ,]*\).*/\1/p')" | awk '{print $4}' | cut -d/ -f1 | head -1)
case "$(scan "$pi_lan" 22)" in "22/tcp/filtered ") ok "22 filtered on the Pi's LAN address $pi_lan" ;; *) bad "22 on $pi_lan not filtered" ;; esac
in1=$(counter palm_input 'palm->pi')
echo "palm_input 'palm->pi' counter: $in0 -> $in1"
if [ "$in1" -gt "$in0" ]; then ok "palm->pi counter increments"; else bad "palm->pi counter"; fi
fw0=$(counter palm_forward 'palm->rfc1918')
lan=$(scan "$LAN_HOST" 22,80,443)
fw1=$(counter palm_forward 'palm->rfc1918')
echo "$LAN_HOST: $lan; palm->rfc1918 counter: $fw0 -> $fw1"
case "$lan" in *open*) bad "LAN host reachable without an allow rule" ;; *) ok "nothing open on $LAN_HOST" ;; esac
if [ "$fw1" -gt "$fw0" ]; then ok "palm->rfc1918 counter increments"; else bad "palm->rfc1918 counter"; fi

step "palm-gwctl disconnect ends the PAN connection"
t_disc=$(date '+%F %T')
run palm-gwctl disconnect "$client_addr"
for _ in $(seq 1 20); do
	kill -0 "$pan_pid" 2> /dev/null || break
	sleep 0.5
done
if kill -0 "$pan_pid" 2> /dev/null; then
	bad "fake-palm still connected 10 s after the disconnect"
	stop_pan
else
	wait "$pan_pid" 2>/dev/null
	pan_pid=""
	tail -2 /var/tmp/phase4-pan.txt
	if grep -q "the gateway ended it" /var/tmp/phase4-pan.txt; then ok "fake-palm saw the connection drop"; else bad "fake-palm did not report the drop"; fi
fi
sleep 2
run palm-gwctl status | grep -E '^pan:'
if pan_line | grep -q " 0 client"; then ok "no PAN clients left"; else bad "PAN client still listed"; fi
journal "$t_disc" | grep -E '"event":"pan_(drop|disconnected)"' | cut -c1-240
if journal "$t_disc" | grep -q '"event":"pan_disconnected"'; then ok "pan_disconnected logged"; else bad "no pan_disconnected"; fi

step "D-032: a trusted device that drops off the allowlist is refused"
gw_dev=/org/bluez/$gw_hci/dev_${client_addr//:/_}
trust() { sudo busctl get-property org.bluez "$gw_dev" org.bluez.Device1 Trusted; }
echo "gateway's BlueZ: $client_addr Trusted = $(trust) (BlueZ skips AuthorizeService for it)"
sudo cp -p "$STATE" "$STATE_BACKUP"
t_ref=$(date '+%F %T')
sudo systemctl stop palm-gwd
sudo python3 -c "import json; p='$STATE'; s=json.load(open(p)); s['devices'].pop('$client_addr', None); json.dump(s, open(p,'w'), indent=2)"
sudo systemctl start palm-gwd
wait_nap || bad "NAP did not come back after the restart"
if allowlisted; then bad "$client_addr still allowlisted"; else ok "$client_addr removed from the allowlist behind the daemon's back"; fi
journal "$t_ref" | grep -E '"event":"untrusted"' | cut -c1-240
echo "gateway's BlueZ: $client_addr Trusted = $(trust)"
if journal "$t_ref" | grep -q "\"event\":\"untrusted\",\"addr\":\"$client_addr\"" && trust | grep -q false; then
	ok "trust cleared at start (untrusted)"
else
	bad "trust not cleared"
fi
for _ in $(seq 1 20); do
	sudo busctl get-property org.bluez "$client_dev" org.bluez.Network1 Connected | grep -q false && break
	sleep 0.5
done
t_conn=$(date '+%F %T')
start_pan 20s
wait "$pan_pid" 2>/dev/null
pan_pid=""
cat /var/tmp/phase4-pan.txt
journal "$t_conn" | grep -E '"event":"(pan_|service_)' | cut -c1-240
if journal "$t_conn" | grep -q "\"event\":\"service_rejected\",\"addr\":\"$client_addr\".*\"uuid\":\"0000000f-"; then
	ok "AuthorizeService refused NAP for $client_addr"
elif journal "$t_conn" | grep -q "\"event\":\"pan_refused\",\"addr\":\"$client_addr\""; then
	ok "pan_refused for $client_addr (the poll backstop)"
else
	bad "neither service_rejected nor pan_refused"
fi
if journal "$t_conn" | grep -q "\"event\":\"pan_connected\",\"addr\":\"$client_addr\""; then bad "it was accepted as a client"; else ok "never accepted as a client"; fi
if grep -q "DHCP lease" /var/tmp/phase4-pan.txt; then bad "fake-palm got a lease"; else ok "fake-palm got no lease"; fi
sudo systemctl stop palm-gwd
sudo cp -p "$STATE_BACKUP" "$STATE" && sudo rm -f "$STATE_BACKUP"
sudo systemctl start palm-gwd
sudo busctl set-property org.bluez "$gw_dev" org.bluez.Device1 Trusted b true
wait_nap && ok "state and trust restored, NAP registered again" || bad "NAP not back after restoring the state"

step "Journal since the start"
if journal "$started" | grep -qE '"level":"ERROR"'; then
	journal "$started" | grep -E '"level":"ERROR"' | cut -c1-240
	bad "errors in the journal"
else
	ok "no errors in the journal"
fi

step "Result: $fails failed check(s). Log: $LOG"
exit "$fails"
