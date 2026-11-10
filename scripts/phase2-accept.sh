#!/usr/bin/env bash
# Phase 2 acceptance with fake-palm on the gateway host itself (PLAN.md section 10, Phase 2).
#
# Needs a second Bluetooth adapter for fake-palm (default hci1, e.g. a USB dongle) while the
# gateway keeps its configured adapter. fake-palm's pppd runs in a network namespace so its
# routes do not collide with the gateway's. Run as the operator user (in group palm-gw, with
# passwordless sudo) on the Pi:
#
#   scripts/phase2-accept.sh                  # everything
#   CLIENT_HCI=hci1 GW=2C:CF:67:2F:29:F5 scripts/phase2-accept.sh
#   URL=http://example.com/ scripts/phase2-accept.sh   # if neverssl.com is down
#
# Output is shown and saved to /var/tmp/phase2-accept-<time>.log for docs/TESTING.md.
# Exit status is the number of failed steps.

set -u

CLIENT_HCI=${CLIENT_HCI:-}   # default: the adapter the gateway is not using
NETNS=${NETNS:-fakepalm}
FAKE_PALM=${FAKE_PALM:-$HOME/fake-palm}
CTL=${CTL:-/run/palm-gw/ctl.sock}
URL=${URL:-http://neverssl.com/}   # page fetched through each link; section 9's default
LOG=/var/tmp/phase2-accept-$(date +%Y%m%d-%H%M%S).log

exec 9>/var/tmp/phase2-accept.lock
if ! flock -n 9; then
	echo "another phase2-accept.sh is running" >&2
	exit 1
fi
exec > >(tee "$LOG") 2>&1

fails=0
started=$(date '+%F %T')
step() { printf '\n==> %s\n' "$*"; }
ok() { printf '    PASS: %s\n' "$*"; }
bad() { printf '    FAIL: %s\n' "$*"; fails=$((fails + 1)); }
run() { printf '$ %s\n' "$*"; "$@"; }

sessions() { palm-gwctl status | sed -n 's/^sessions: *\([0-9]*\) of.*/\1/p'; }
gw_pppd() { pgrep -c -x pppd 2>/dev/null || true; }

step "Preconditions ($(date '+%F %T'))"
run palm-gwctl status | sed -n '1,4p'
gw_hci=$(palm-gwctl status | sed -n 's/^adapter: *\(hci[0-9]*\).*/\1/p')
GW=${GW:-$(palm-gwctl status | sed -n 's/^adapter: *hci[0-9]* \([0-9A-F:]\{17\}\).*/\1/p')}
# fake-palm's adapter: the first one the gateway is not using (hciN order can change at boot).
if [ -z "$CLIENT_HCI" ]; then
	for h in /sys/class/bluetooth/hci*; do
		h=$(basename "$h")
		case "$h" in *:*) continue ;; esac
		[ "$h" != "$gw_hci" ] && { CLIENT_HCI=$h; break; }
	done
fi
if [ -z "$GW" ] || [ -z "$gw_hci" ]; then
	bad "cannot read the gateway adapter from palm-gwctl status"
	exit 1
fi
if [ "$gw_hci" = "$CLIENT_HCI" ]; then
	bad "the gateway is using $CLIENT_HCI, the adapter meant for fake-palm (set [bluetooth] adapter)"
	exit 1
fi
if [ ! -e "/sys/class/bluetooth/$CLIENT_HCI" ]; then
	bad "$CLIENT_HCI not present (plug in the second adapter; check dmesg and bluetoothctl list)"
	exit 1
fi
bus=$(basename "$(readlink -f "/sys/class/bluetooth/$CLIENT_HCI/device/subsystem")")
run bluetoothctl list
# systemd-rfkill restores a saved soft block per USB port, so a dongle can arrive blocked.
rf=$(grep -lx "$CLIENT_HCI" /sys/class/rfkill/rfkill*/name 2>/dev/null | head -1)
if [ -n "$rf" ] && [ "$(cat "$(dirname "$rf")/soft")" = "1" ]; then
	echo "$CLIENT_HCI is soft-blocked by rfkill; unblocking"
	echo 0 | sudo tee "$(dirname "$rf")/soft" > /dev/null
fi
powered() { sudo btmgmt --index "${CLIENT_HCI#hci}" info | grep -q "current settings:.*powered"; }
# After an unblock bluetoothd powers the adapter itself and a parallel "power on" gets Busy.
for _ in 1 2 3 4 5; do
	powered && break
	run sudo btmgmt --index "${CLIENT_HCI#hci}" power on
	sleep 1
done
if ! powered; then
	bad "$CLIENT_HCI does not power on (check rfkill and dmesg)"
	exit 1
fi
client_addr=$(sudo btmgmt --index "${CLIENT_HCI#hci}" info | sed -n 's/^[[:space:]]*addr \([0-9A-F:]\{17\}\).*/\1/p')
echo "gateway $gw_hci $GW; fake-palm $CLIENT_HCI ${client_addr:-?} (bus $bus)"
if [ -z "$client_addr" ]; then
	bad "cannot read the address of $CLIENT_HCI"
	exit 1
fi
ok "two adapters"

step "Pair fake-palm with the gateway (once)"
if palm-gwctl status | grep -qi "$client_addr"; then
	ok "$client_addr already in the allowlist"
else
	palm-gwctl pair 2 --one > /var/tmp/phase2-accept-pair.txt 2>&1 &
	pair_pid=$!
	sleep 2
	cat /var/tmp/phase2-accept-pair.txt
	run sudo "$FAKE_PALM" pair "$GW" --adapter "$CLIENT_HCI"
	wait "$pair_pid"
	cat /var/tmp/phase2-accept-pair.txt
	if palm-gwctl status | grep -qi "$client_addr"; then ok "paired and allowlisted"; else bad "pairing"; fi
fi

connect() { # channel mode
	step "Connect: channel $1, mode $2"
	if run sudo "$FAKE_PALM" connect "$GW" --adapter "$CLIENT_HCI" --channel "$1" --mode "$2" \
		--netns "$NETNS" --internet --url "$URL"; then
		ok "channel $1 $2"
	else
		bad "channel $1 $2"
	fi
	sleep 2
}
connect 1 client
connect 2 ppp
connect 3 at
connect 3 at-tx
run journalctl -u palm-gwd --since "$started" --no-pager -o cat --grep '"event":"handshake"'

step "Disconnect within 3 s"
sudo "$FAKE_PALM" connect "$GW" --adapter "$CLIENT_HCI" --channel 2 --mode ppp --netns "$NETNS" --hold 60s &
hold_pid=$!
for _ in $(seq 1 30); do
	[ "$(sessions)" = "1" ] && palm-gwctl status | grep -qE "ppp[0-9]+ +up" && break
	sleep 1
done
run palm-gwctl status | sed -n '/^sessions/,$p' | head -3
t0=$(date +%s%N)
run palm-gwctl disconnect "$client_addr"
ms=$((($(date +%s%N) - t0) / 1000000))
echo "disconnect returned after $ms ms"
n=$(sessions)
if [ "$ms" -le 3000 ] && [ "$n" = "0" ]; then ok "disconnect in $ms ms, $n sessions left"; else bad "disconnect: $ms ms, $n sessions left"; fi
# The client must see the link drop too (D-028), not sit out its 60 s hold.
for _ in $(seq 1 10); do kill -0 "$hold_pid" 2>/dev/null || break; sleep 1; done
ms=$((($(date +%s%N) - t0) / 1000000))
if kill -0 "$hold_pid" 2>/dev/null; then
	bad "fake-palm still connected 10 s after the disconnect"
	sudo pkill -f "fake-palm connect" || true
	sudo ip netns pids "$NETNS" | xargs -r sudo kill   # its client pppd, in its own process group
else
	ok "fake-palm saw the link drop within $ms ms"
fi
wait "$hold_pid"

step "Storm"
if run sudo "$FAKE_PALM" storm "$GW" --adapter "$CLIENT_HCI" --netns "$NETNS" --ctl "$CTL"; then
	ok "storm"
else
	bad "storm"
fi

step "Afterwards"
sleep 3
run palm-gwctl status | sed -n '/^sessions/,/^[a-z]/p'
n=$(sessions)
p=$(gw_pppd)
echo "sessions $n, pppd processes $p"
if [ "$n" = "0" ] && [ "$p" = "0" ]; then ok "zero sessions and zero pppd"; else bad "left over: $n sessions, $p pppd"; fi
if journalctl -u palm-gwd --since "$started" --no-pager -o cat | grep -q '"event":"orphan_pppd"'; then
	bad "orphan_pppd logged"
else
	ok "no orphan_pppd"
fi

step "Result: $fails failed step(s). Log: $LOG"
exit "$fails"
