#!/usr/bin/env bash
# Phase 5 robustness with fake-palm on the gateway host (PLAN.md section 10, Phase 5; D-027).
#
# fd leaks across 100 PPP sessions and repeated PAN sessions, then a BlueZ restart and a palm-gwd
# restart while fake-palm holds a PPP link and a PAN connection. After each disruption: every
# unit active, no pppd left over, the daemon back on BlueZ, and a fresh PPP and PAN connection
# work. Run detached as the operator user on the Pi (btmgmt-free, so no terminal is needed):
#
#   nohup env URL=http://example.com/ scripts/phase5-robust.sh > /dev/null 2>&1 < /dev/null &
#
# Output is saved to /var/tmp/phase5-robust-<time>.log. Exit status is the number of failed checks.

set -u

CLIENT_HCI=${CLIENT_HCI:-}   # default: the adapter the gateway is not using
NETNS=${NETNS:-fakepalm}
FAKE_PALM=${FAKE_PALM:-$HOME/fake-palm}
CTL=${CTL:-/run/palm-gw/ctl.sock}
URL=${URL:-http://neverssl.com/}
STORM=${STORM:-100}
PAN_CYCLES=${PAN_CYCLES:-10}
UNITS="bluetooth palm-gwd palm-gw-dnsmasq palm-gwd-nm-bridge nftables NetworkManager"
LOG=/var/tmp/phase5-robust-$(date +%Y%m%d-%H%M%S).log

exec 9>/var/tmp/phase5-robust.lock
if ! flock -n 9; then
	echo "another phase5-robust.sh is running" >&2
	exit 1
fi
exec > >(tee "$LOG") 2>&1

fails=0
started=$(date '+%F %T')
step() { printf '\n==> %s (%s)\n' "$*" "$(date '+%T')"; }
ok() { printf '    PASS: %s\n' "$*"; }
bad() { printf '    FAIL: %s\n' "$*"; fails=$((fails + 1)); }
run() { printf '$ %s\n' "$*"; "$@"; }
journal() { journalctl -u palm-gwd --since "$1" --no-pager -o cat; }

gwd_pid() { systemctl show -p MainPID --value palm-gwd; }
fds() { sudo ls "/proc/$(gwd_pid)/fd" | wc -l; }
fd_list() { sudo ls -l "/proc/$(gwd_pid)/fd" | awk 'NR > 1 { print $NF }' | sed 's/[0-9]\+/N/g' | sort | uniq -c; }
sessions() { palm-gwctl status | sed -n 's/^sessions: *\([0-9]*\) of.*/\1/p'; }
pan_clients() { palm-gwctl status | sed -n 's/^pan: .* \([0-9]*\) client.*/\1/p'; }
gw_pppd() { pgrep -fc "linkname palm-" || true; }
attached() { palm-gwctl status | grep -q "^bluez: *ok, agent registered" && palm-gwctl status | grep -q "^pan: *NAP on"; }
wait_attached() {
	for _ in $(seq 1 "$1"); do
		attached && return 0
		sleep 1
	done
	return 1
}
units_active() {
	local bad_units=""
	for u in $UNITS; do
		systemctl is-active --quiet "$u" || bad_units="$bad_units $u"
	done
	if [ -z "$bad_units" ]; then ok "all units active ($UNITS)"; else bad "not active:$bad_units"; fi
}
# The gateway side must end up clean: no sessions, no pppd of ours, no PAN clients.
clean() {
	for _ in $(seq 1 20); do
		[ "$(sessions)" = 0 ] && [ "$(gw_pppd)" = 0 ] && [ "$(pan_clients)" = 0 ] && break
		sleep 1
	done
	echo "sessions $(sessions), gateway pppd $(gw_pppd), PAN clients $(pan_clients)"
	if [ "$(sessions)" = 0 ] && [ "$(gw_pppd)" = 0 ] && [ "$(pan_clients)" = 0 ]; then ok "$1: nothing left over"; else bad "$1: leftovers"; fi
}
fp_kill() {
	sudo pkill -f "^(sudo )?$FAKE_PALM (connect|pan) " 2> /dev/null
	sudo ip netns pids "$NETNS" 2> /dev/null | xargs -r sudo kill 2> /dev/null
	sleep 3 # fake-palm pan disconnects on SIGTERM
}
trap fp_kill EXIT

# hold_both: fake-palm holds a PPP link on channel 2 and a PAN connection, both for 10 minutes.
hold_both() {
	sudo "$FAKE_PALM" connect "$GW" --adapter "$CLIENT_HCI" --channel 2 --mode ppp --netns "$NETNS" \
		--hold 600s > /var/tmp/phase5-ppp.txt 2>&1 &
	ppp_pid=$!
	for _ in $(seq 1 30); do
		palm-gwctl status | grep -qE "ppp[0-9]+ +up" && break
		sleep 1
	done
	# PAN in its own namespace: the PPP link already holds the default route in $NETNS.
	sudo "$FAKE_PALM" pan "$GW" --adapter "$CLIENT_HCI" --netns "${NETNS}-pan" --hold 600s > /var/tmp/phase5-pan.txt 2>&1 &
	pan_pid=$!
	for _ in $(seq 1 30); do
		grep -q "holding the connection" /var/tmp/phase5-pan.txt && break
		kill -0 "$pan_pid" 2> /dev/null || break
		sleep 1
	done
	sleep 2
	palm-gwctl status | grep -E "^(sessions|pan):|^  .* (lap|pan) "
	if [ "$(sessions)" = 1 ] && [ "$(pan_clients)" = 1 ]; then ok "PPP link and PAN connection up"; else bad "could not bring up PPP and PAN"; fi
}
# after_disruption NAME [pan-survives]: the clients see the drop (or, with pan-survives, the PAN
# connection carries on and the new daemon takes it over, D-034), the gateway comes back clean,
# and new connections work.
after_disruption() {
	for _ in $(seq 1 30); do
		if ! kill -0 "$ppp_pid" 2> /dev/null && { [ "${2:-}" = pan-survives ] || ! kill -0 "$pan_pid" 2> /dev/null; }; then
			break
		fi
		sleep 1
	done
	if kill -0 "$ppp_pid" 2> /dev/null; then bad "$1: fake-palm's PPP link still up after 30 s"; else ok "$1: fake-palm's PPP link ended"; fi
	if [ "${2:-}" = pan-survives ]; then
		if wait_attached 60; then ok "$1: daemon attached again, NAP registered"; else bad "$1: daemon not attached within 60 s"; fi
		sleep 2
		run palm-gwctl status | grep -E "^pan:|^  .* pan "
		if kill -0 "$pan_pid" 2> /dev/null && [ "$(pan_clients)" = 1 ]; then ok "$1: the PAN connection carried on and the new daemon lists it"; else bad "$1: PAN connection not carried over"; fi
		if sudo ip netns exec "${NETNS}-pan" ping -c 2 -W 2 10.42.0.1 > /dev/null; then ok "$1: traffic still flows over it"; else bad "$1: no traffic over the carried-over PAN connection"; fi
	else
		if kill -0 "$pan_pid" 2> /dev/null; then bad "$1: fake-palm's PAN connection still up after 30 s"; else ok "$1: fake-palm's PAN connection ended"; fi
	fi
	tail -1 /var/tmp/phase5-ppp.txt
	tail -1 /var/tmp/phase5-pan.txt
	fp_kill
	if wait_attached 60; then ok "$1: daemon attached again, NAP registered"; else bad "$1: daemon not attached within 60 s"; fi
	units_active
	clean "$1"
	if sudo "$FAKE_PALM" connect "$GW" --adapter "$CLIENT_HCI" --channel 2 --mode ppp --netns "$NETNS" --internet --url "$URL" > /var/tmp/phase5-again.txt 2>&1; then
		ok "$1: new PPP connection with internet"
	else
		bad "$1: new PPP connection"
		tail -5 /var/tmp/phase5-again.txt
	fi
	if sudo "$FAKE_PALM" pan "$GW" --adapter "$CLIENT_HCI" --netns "${NETNS}-pan" --internet --url "$URL" > /var/tmp/phase5-again.txt 2>&1; then
		ok "$1: new PAN connection with internet"
	else
		bad "$1: new PAN connection"
		tail -5 /var/tmp/phase5-again.txt
	fi
	clean "$1, after the new connections"
}

step "Preconditions ($started)"
run palm-gwctl status | grep -E '^(palm-gwd|bluez|adapter|pan|sessions)'
gw_hci=$(palm-gwctl status | sed -n 's/^adapter: *\(hci[0-9]*\).*/\1/p')
GW=$(palm-gwctl status | sed -n 's/^adapter: *hci[0-9]* \([0-9A-F:]\{17\}\).*/\1/p')
# fake-palm's adapter: the first one the gateway is not using (hciN order can change at boot).
if [ -z "$CLIENT_HCI" ]; then
	for h in /sys/class/bluetooth/hci*; do
		h=$(basename "$h")
		case "$h" in *:*) continue ;; esac
		[ "$h" != "$gw_hci" ] && { CLIENT_HCI=$h; break; }
	done
fi
if [ -z "$GW" ] || [ "$gw_hci" = "$CLIENT_HCI" ] || [ ! -e "/sys/class/bluetooth/$CLIENT_HCI" ]; then
	bad "need the gateway on one adapter and $CLIENT_HCI present for fake-palm"
	exit 1
fi
rf=$(grep -lx "$CLIENT_HCI" /sys/class/rfkill/rfkill*/name 2> /dev/null | head -1)
[ -n "$rf" ] && [ "$(cat "$(dirname "$rf")/soft")" = 1 ] && echo 0 | sudo tee "$(dirname "$rf")/soft" > /dev/null
adapter_prop() { sudo busctl get-property org.bluez "/org/bluez/$CLIENT_HCI" org.bluez.Adapter1 "$1" | cut -d' ' -f2- | tr -d '"'; }
for _ in 1 2 3 4 5; do
	[ "$(adapter_prop Powered)" = true ] && break
	sudo busctl set-property org.bluez "/org/bluez/$CLIENT_HCI" org.bluez.Adapter1 Powered b true 2> /dev/null
	sleep 1
done
echo "gateway $gw_hci $GW; fake-palm $CLIENT_HCI $(adapter_prop Address); palm-gwd pid $(gwd_pid)"
if attached; then ok "daemon attached, NAP registered"; else bad "daemon not attached"; exit 1; fi
units_active
clean "start"
fd0=$(fds)
echo "palm-gwd open fds: $fd0"
fd_list

step "fd leaks: $STORM PPP sessions (fake-palm storm)"
run sudo "$FAKE_PALM" storm "$GW" --adapter "$CLIENT_HCI" --count "$STORM" --interval 300ms --netns "$NETNS" --ctl "$CTL" 2>&1 | tail -4
clean "storm"
fd1=$(fds)
echo "palm-gwd open fds: $fd0 before, $fd1 after $STORM sessions"
if [ "$fd1" -le "$fd0" ]; then ok "no fd growth across $STORM sessions"; else bad "fds grew from $fd0 to $fd1"; fd_list; fi

step "fd leaks: $PAN_CYCLES PAN sessions"
pan_ok=0
for i in $(seq 1 "$PAN_CYCLES"); do
	if sudo "$FAKE_PALM" pan "$GW" --adapter "$CLIENT_HCI" --netns "${NETNS}-pan" > /var/tmp/phase5-pancycle.txt 2>&1; then
		pan_ok=$((pan_ok + 1))
	else
		echo "[$i] failed:"
		tail -3 /var/tmp/phase5-pancycle.txt
	fi
	# Reconnecting within ~50 ms of a disconnect can race the kernel's teardown of the old
	# BNEP session ("File exists" in bluetoothd); no real client is that quick.
	sleep 1
done
echo "$pan_ok of $PAN_CYCLES PAN sessions completed (lease, ping, DNS)"
if [ "$pan_ok" = "$PAN_CYCLES" ]; then ok "every PAN session worked"; else bad "$((PAN_CYCLES - pan_ok)) PAN session(s) failed"; fi
clean "PAN cycles"
fd2=$(fds)
echo "palm-gwd open fds: $fd0 at start, $fd2 after the PAN cycles"
if [ "$fd2" -le "$fd0" ]; then ok "no fd growth across $PAN_CYCLES PAN sessions"; else bad "fds grew from $fd0 to $fd2"; fd_list; fi

step "BlueZ restart with a PPP link and a PAN connection up"
hold_both
t=$(date '+%F %T')
run sudo systemctl restart bluetooth
after_disruption "bluetooth restart"
journal "$t" | grep -E '"event":"(bluez_lost|bluez_found|session_end|pan_registered|pan_disconnected)"' | cut -c1-200
fd3=$(fds)
echo "palm-gwd open fds: $fd0 at start, $fd3 after the BlueZ restart"
if [ "$fd3" -le "$fd0" ]; then ok "no fd growth across the BlueZ restart"; else bad "fds grew from $fd0 to $fd3"; fd_list; fi

step "palm-gwd restart with a PPP link and a PAN connection up"
hold_both
old=$(gwd_pid)
t=$(date '+%F %T')
run sudo systemctl restart palm-gwd
after_disruption "palm-gwd restart" pan-survives
echo "palm-gwd pid $old -> $(gwd_pid)"
journal "$t" | grep -E '"event":"(session_end|pan_registered|pan_disconnected)"|palm-gwd (stopping|starting|ready)' | cut -c1-200
fd4=$(fds)
echo "palm-gwd open fds: $fd0 at start (old process), $fd4 now"
if [ "$fd4" -le "$fd0" ]; then ok "the new daemon has no more fds than the old one did"; else bad "fds: $fd0 then $fd4"; fd_list; fi

step "Journal since the start"
journal "$started" | grep -E '"event":"orphan_pppd"' | cut -c1-200
if journal "$started" | grep -q '"event":"orphan_pppd"'; then bad "orphan_pppd logged"; else ok "no orphan_pppd"; fi
if journal "$started" | grep -qE '"level":"ERROR"'; then
	journal "$started" | grep -E '"level":"ERROR"' | cut -c1-240
	bad "errors in the journal"
else
	ok "no errors in the journal"
fi

step "Result: $fails failed check(s). Log: $LOG"
exit "$fails"
