#!/usr/bin/env bash
# Phase 3 acceptance on the gateway host (PLAN.md section 10, Phase 3; D-027, D-031).
#
# fake-palm (second adapter, network namespace $NETNS) holds one PPP link while the checks run
# from inside its namespace. The "LAN" is a stand-in host in its own namespace ($LANNS, address
# $LAN_HOST on a veth pair), so nothing on the real LAN is scanned: traffic from the Palm side
# to it goes through palm_forward exactly as it would to a real LAN host. Needs nmap and dig
# (bind9-dnsutils). Run as the operator user on the Pi:
#
#   URL=http://example.com/ scripts/phase3-accept.sh
#
# Output is shown and saved to /var/tmp/phase3-accept-<time>.log. Exit status is the number of
# failed checks. The configuration is restored on exit.

set -u

CLIENT_HCI=${CLIENT_HCI:-hci1}
NETNS=${NETNS:-fakepalm}
LANNS=${LANNS:-palmlan}
LAN_PI=${LAN_PI:-192.168.77.1}     # the Pi's end of the stand-in LAN
LAN_HOST=${LAN_HOST:-192.168.77.2} # the stand-in LAN host
FAKE_PALM=${FAKE_PALM:-$HOME/fake-palm}
URL=${URL:-http://neverssl.com/}
CONF=/etc/palm-gw/config.toml
LOG=/var/tmp/phase3-accept-$(date +%Y%m%d-%H%M%S).log
BACKUP=/var/tmp/phase3-config.toml.orig

exec 9>/var/tmp/phase3-accept.lock
if ! flock -n 9; then
	echo "another phase3-accept.sh is running" >&2
	exit 1
fi
exec > >(tee "$LOG") 2>&1

fails=0
step() { printf '\n==> %s\n' "$*"; }
ok() { printf '    PASS: %s\n' "$*"; }
bad() { printf '    FAIL: %s\n' "$*"; fails=$((fails + 1)); }
run() { printf '$ %s\n' "$*"; "$@"; }
inns() { sudo ip netns exec "$NETNS" "$@"; }

hold_pid=""
cleanup() {
	set +e
	if [ -n "$hold_pid" ]; then
		sudo pkill -f "fake-palm connect" 2>/dev/null
		sudo ip netns pids "$NETNS" 2>/dev/null | xargs -r sudo kill 2>/dev/null
		wait "$hold_pid" 2>/dev/null
	fi
	if [ -f "$BACKUP" ]; then
		sudo cp "$BACKUP" "$CONF" && sudo rm -f "$BACKUP"
		palm-gwctl reload > /dev/null
	fi
	sudo ip netns pids "$LANNS" 2>/dev/null | xargs -r sudo kill 2>/dev/null
	sudo ip netns del "$LANNS" 2>/dev/null
	sudo ip link del lan-pi 2>/dev/null
}
trap cleanup EXIT

counter() { # chain comment -> packet count
	sudo nft list chain inet palm_gw "$1" | sed -n "s/.*counter packets \([0-9]*\) .*comment \"$2\".*/\1/p"
}

# scan HOST PORTS -> "port/state ..." for nmap's open, closed and filtered ports
scan() {
	inns nmap -sT -Pn -n --max-retries 1 --host-timeout 60s -p "$2" "$1" -e ppp0 2>&1 |
		awk '/^[0-9]+\/tcp/ { printf "%s/%s ", $1, $2 } END { print "" }'
}

step "Preconditions ($(date '+%F %T'))"
GW=$(palm-gwctl status | sed -n 's/^adapter: *hci[0-9]* \([0-9A-F:]\{17\}\).*/\1/p')
run palm-gwctl status | grep -E '^(palm-gwd|adapter|firewall)'
for t in nmap dig; do command -v "$t" > /dev/null || { bad "$t not installed"; exit 1; }; done
if ! palm-gwctl status | grep -q '^firewall: .*applied'; then bad "firewall sets not applied"; exit 1; fi
sudo cp -p "$CONF" "$BACKUP"
if sudo grep -q '^\[\[firewall.allow\]\]' "$CONF"; then
	bad "$CONF already has [[firewall.allow]] blocks; this test expects none"
	exit 1
fi
ok "gateway $GW, firewall managed, no allow rules"

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
echo "from inside $LANNS: $got"
case "$got" in *"22/tcp/open"*"80/tcp/open"*"443/tcp/open"*) ok "listeners on 22, 80, 443" ;; *) bad "listeners" ;; esac

step "fake-palm link (channel 2), held for the checks"
sudo "$FAKE_PALM" connect "$GW" --adapter "$CLIENT_HCI" --channel 2 --mode ppp --netns "$NETNS" \
	--internet --url "$URL" --hold 600s &
hold_pid=$!
for _ in $(seq 1 30); do
	palm-gwctl status | grep -qE "ppp[0-9]+ +up" && break
	sleep 1
done
sleep 8 # let fake-palm finish its own probe (ping, DNS, HTTP)
run palm-gwctl status | grep -E "ppp[0-9]+ +up" || { bad "no PPP session"; exit 1; }
session=$(palm-gwctl status | grep -E "ppp[0-9]+ +up")

step "Internet and DNS from the Palm side"
# Resolve through the gateway like a Palm does (DNS from IPCP); the namespace shares the Pi's
# resolv.conf, whose LAN resolver the firewall rightly blocks from this side.
host=${URL#*://}
host=${host%%/*}
ip=$(inns dig +short +time=3 +tries=1 @10.42.0.1 "$host" A | grep -E '^[0-9.]+$' | head -1)
code=$(inns curl -s -o /dev/null -m 15 -w '%{http_code}' --resolve "$host:80:$ip" "$URL")
echo "GET $URL via $ip: $code"
if [ "$code" = 200 ]; then ok "GET $URL"; else bad "GET $URL"; fi
run inns dig +short +time=3 +tries=1 @10.42.0.1 example.com
if inns dig +short +time=3 +tries=1 @10.42.0.1 example.com | grep -qE '^[0-9.]+$'; then ok "dig @10.42.0.1 example.com answers"; else bad "dig"; fi

step "The Pi itself from the Palm side"
in0=$(counter palm_input 'palm->pi')
t0=$(date +%s%N)
inns nc -z -w 3 10.42.0.1 22
rc=$?
ms=$((($(date +%s%N) - t0) / 1000000))
echo "nc -z -w 3 10.42.0.1 22: exit $rc after $ms ms"
if [ "$rc" != 0 ] && [ "$ms" -ge 2900 ]; then ok "ssh to 10.42.0.1 dropped (timed out, not refused)"; else bad "ssh to 10.42.0.1 not dropped"; fi
pi_lan=$(ip -4 -o addr show "$(palm-gwctl status | sed -n 's/^network:.* uplink \([^,]*\),.*/\1/p')" | awk '{print $4}' | cut -d/ -f1 | head -1)
echo "gateway address: $(scan 10.42.0.1 22,80,443,8042)"
echo "Pi LAN address $pi_lan: $(scan "$pi_lan" 22,80,443,8042)"
in1=$(counter palm_input 'palm->pi')
echo "palm_input 'palm->pi' counter: $in0 -> $in1"
if [ "$in1" -gt "$in0" ]; then ok "palm->pi drop counter increments"; else bad "palm->pi counter"; fi
case "$(scan 10.42.0.1 22)" in "22/tcp/filtered ") ok "22 filtered on 10.42.0.1" ;; *) bad "22 on 10.42.0.1 not filtered" ;; esac
case "$(scan "$pi_lan" 22)" in "22/tcp/filtered ") ok "22 filtered on the Pi's LAN address" ;; *) bad "22 on the Pi's LAN address not filtered" ;; esac

step "LAN host without an allow rule"
fw0=$(counter palm_forward 'palm->rfc1918')
before=$(scan "$LAN_HOST" 22,80,443)
fw1=$(counter palm_forward 'palm->rfc1918')
echo "$LAN_HOST: $before"
echo "palm_forward 'palm->rfc1918' counter: $fw0 -> $fw1"
case "$before" in *open*) bad "something open without an allow rule" ;; *) ok "nothing open on $LAN_HOST" ;; esac
if [ "$fw1" -gt "$fw0" ]; then ok "palm->rfc1918 drop counter increments"; else bad "palm->rfc1918 counter"; fi

step "Add [[firewall.allow]] for $LAN_HOST tcp/80, then palm-gwctl reload"
printf '\n[[firewall.allow]]\nhost    = "%s"\nports   = ["tcp/80"]\ncomment = "phase3-accept stand-in LAN host"\n' "$LAN_HOST" |
	sudo tee -a "$CONF" > /dev/null
run palm-gwctl reload
run sudo nft list set inet palm_gw lan_allow
after=$(scan "$LAN_HOST" 22,80,443)
echo "$LAN_HOST: $after"
case "$after" in "22/tcp/filtered 80/tcp/open 443/tcp/filtered ") ok "only the allowed port is reachable" ;; *) bad "after reload: $after" ;; esac
code=$(inns curl -s -o /dev/null -m 10 -w '%{http_code}' "http://$LAN_HOST/")
if [ "$code" = 200 ]; then ok "GET http://$LAN_HOST/ through the allow rule"; else bad "GET http://$LAN_HOST/: $code"; fi
now=$(palm-gwctl status | grep -E "ppp[0-9]+ +up")
if [ "${now%%up [0-9]*}" = "${session%%up [0-9]*}" ]; then ok "same PPP session throughout (nothing restarted)"; else bad "session changed: $now"; fi

step "Remove the rule, reload again"
sudo cp "$BACKUP" "$CONF"
run palm-gwctl reload
again=$(scan "$LAN_HOST" 22,80,443)
echo "$LAN_HOST: $again"
case "$again" in *open*) bad "still open after removing the rule" ;; *) ok "closed again" ;; esac

step "Result: $fails failed check(s). Log: $LOG"
exit "$fails"
