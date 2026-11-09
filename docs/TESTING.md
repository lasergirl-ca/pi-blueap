# Acceptance test record

Exact commands and output for each phase's acceptance test (PLAN.md section 10), appended as each
phase closes. Hardware runs are recorded per board. Where a run has not happened yet it says so.

## Phase 0: Scaffold

**Accept:** `make test lint build-arm64` green on macOS and on the Pi. `palm-gwd -config bad.toml`
exits 1 with a message naming the offending key. `make install` on a fresh image completes without
error and `systemctl status nftables palm-gw-dnsmasq palm-gwd-nm-bridge` are all active;
`ip addr show br-palm` shows `10.42.0.1/24`.

### Development machine (macOS 15, Apple Silicon, Go 1.27.2, golangci-lint 2.15.0), 2026-11-08

```
$ make test
go test ./...
ok  	palm-gw/internal/config	0.230s
ok  	palm-gw/internal/ctl	0.205s

$ make lint
golangci-lint run ./...
0 issues.

$ make vet
go vet ./...

$ make build-arm64
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags '-s -w -X main.version=dev' -o dist/linux-arm64/ ./cmd/...
$ file dist/linux-arm64/palm-gwd
dist/linux-arm64/palm-gwd: ELF 64-bit LSB executable, ARM aarch64, version 1 (SYSV), statically linked, Go BuildID=..., stripped
```

Test coverage in `internal/config` exercises every rule in PLAN.md section 7's validation list
(adapter regex, channel range and uniqueness, subnet/gateway/pool containment and overlap, pool
size vs `max_sessions`, port syntax, PIN digits) plus the uplink runtime check with an injected
interface lookup. `internal/ctl` tests cover a status round trip, redaction of the PIN over the
socket, unknown command and handler errors, a bad JSON request, and cancellation of a streaming
handler when the client disconnects.

**Bad configuration exits 1 and names every offending key:**

```
$ cat bad.toml
[bluetooth]
adapter = "usb0"

[dun]
channel = 1

[logging]
level = "loud"

$ palm-gwd -config bad.toml; echo exit=$?
palm-gwd: config: 3 problems:
  - bluetooth.adapter: must be "auto" or match ^hci[0-9]+$, got "usb0"
  - dun.channel: channel 1 is already used by [spp]
  - logging.level: must be debug, info, warn or error, got "loud"
exit=1

$ printf '[bluetooth]\nadaptor = "hci0"\n' > typo.toml; palm-gwd -config typo.toml; echo exit=$?
palm-gwd: config: bluetooth.adaptor: unknown key
exit=1

$ : > empty.toml; palm-gwd -config empty.toml -check; echo exit=$?
empty.toml: configuration is valid
exit=0

$ palm-gwd -config noif.toml; echo exit=$?        # default uplink eth0 does not exist on a Mac
{"time":"...","level":"ERROR","msg":"configuration rejected","err":"config: network.uplink: interface \"eth0\" does not exist"}
palm-gwd: config: network.uplink: interface "eth0" does not exist
exit=1
```

**Every deploy/ file renders from `config.example.toml`** (`palm-gwd -config config.example.toml -render FILE`):
nftables, dnsmasq, nm-bridge unit (`ExecStart=/usr/local/lib/palm-gw/nm-bridge.sh br-palm 10.42.0.1 24`),
sysctl (`-net.ipv4.conf.br-palm.proxy_arp = 0`), bluetooth main.conf block, and the four plain files
pass through unchanged. With `dns_upstream = ["1.1.1.1","9.9.9.9"]` the dnsmasq render gains
`no-resolv` and two `server=` lines; with `mss_clamp = false` the nft render has no `maxseg` rule.
`nft -c` of the rendered ruleset could not run here (no `nft` on macOS); CI's Bookworm job does it.

**Control socket round trip** (daemon run as a normal user with a temp socket; the uplink was the
Mac's default-route interface):

```
$ palm-gwd -config live.toml &
$ ls -la $T/ctl.sock
srw-rw----  1 zoe  staff  0 Nov  8 11:23 .../ctl.sock
$ palm-gwctl -socket $T/ctl.sock status
palm-gwd dev, up 1s (phase 0)
config:    .../live.toml
adapter:   hci0  name "Palm Gateway"  class 0x000300
profiles:  spp ch1 on, lap ch2 on, dun ch3 on, pan on (br-palm)
network:   10.42.0.0/24 gw 10.42.0.1 uplink en0  ppp 10.42.0.10-10.42.0.99  dhcp 10.42.0.100-10.42.0.199
allowlist: 0 static, allow_any=true
warning:   bluetooth.allow_any = true: any paired device may connect, not only allowlisted ones
$ palm-gwctl status --json -socket $T/ctl.sock | head -5
{
  "version": "dev",
  "phase": "0",
  "started_at": "2026-11-08T11:23:37.955711-07:00",
  "uptime_seconds": 1,
$ palm-gwd ctl -socket $T/ctl.sock status | head -1          # alias
palm-gwd dev, up 1s (phase 0)
$ printf '{"cmd":"pair"}\n' | nc -U -w1 $T/ctl.sock
{"error":"unknown command \"pair\""}
$ printf 'hello\n' | nc -U -w1 $T/ctl.sock
{"error":"bad request: invalid character 'h' looking for beginning of value"}
$ kill -TERM %1; wait; echo exit=$?
exit=0                                                        # socket file removed on exit
```

Daemon log (text format) for the run above:

```
level=INFO msg="palm-gwd starting" version=dev phase=0 config=.../live.toml
level=WARN msg="bluetooth.allow_any = true: any paired device may connect, not only allowlisted ones"
level=WARN msg="ctl group does not exist; socket is root-only" component=ctl group=palm-gw
level=INFO msg="ctl socket listening" component=ctl path=.../ctl.sock
level=INFO msg="palm-gwd ready" ctl_socket=.../ctl.sock
level=INFO msg="palm-gwd stopping"
```

Note for anyone repeating this on macOS: Unix socket paths are limited to 104 bytes, so a socket
under a deep temp directory fails with `bind: invalid argument`. Use `mktemp -d -t pgw`.

### Raspberry Pi 5 Model B Rev 1.0, Raspberry Pi OS Lite 64-bit Trixie, kernel 6.18.50, 2026-11-08

Rig: `ssh sysadmin@10.4.0.241`, key auth, passwordless sudo; BlueZ 5.82, ppp 2.5.2, dnsmasq 2.91,
nftables 1.1.3, NetworkManager 1.52.1; onboard adapter `2C:CF:67:2F:29:F5`; uplink `eth0`.
Tree copied with `make build-arm64 && tar … | ssh … tar -x` into `~/palm-gw`; `make`, `golang-go`
(1.24.4) and `file` installed from apt.

**`make test`, `make vet`, `make build-arm64` on the Pi:**

```
sysadmin@piblueap:~/palm-gw $ make test
go test ./...
ok  	palm-gw/internal/config	0.007s
ok  	palm-gw/internal/ctl	0.007s
sysadmin@piblueap:~/palm-gw $ make vet
go vet ./...
sysadmin@piblueap:~/palm-gw $ make build-arm64
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags '-s -w -X main.version=dev' -o dist/linux-arm64/ ./cmd/...
```

`make lint` on the Pi: see the lint note at the end of this section.

**`sudo make install UPLINK=eth0`** (first run; every step reports what it did):

```
==> Board: pi5, kernel 6.18.50+rpt-rpi-2712
==> Installing packages
    all present: bluez ppp dnsmasq-base nftables
==> Operator group
    group palm-gw created
    sysadmin added to palm-gw (takes effect at next login)
==> Binaries
    /usr/local/bin/palm-gwd written            (+ palm-gwctl, palm-gw, fake-palm, lib/palm-gw/*.sh)
==> Configuration
    /etc/palm-gw/config.toml written from config.example.toml with uplink = "eth0"
/etc/palm-gw/config.toml: configuration is valid
==> System files
    /etc/systemd/system/palm-gwd.service written      (+ nm-bridge, dnsmasq units, NM conf.d, dnsmasq.conf, sysctl, modules-load, options.palm)
==> nftables
    /etc/nftables.d/palm-gw.nft written
    include line appended to /etc/nftables.conf
==> Bluetooth adapter: onboard
==> Units
    nftables enabled
    palm-gwd-nm-bridge enabled and started
    palm-gw-dnsmasq enabled and started
    palm-gwd installed but not enabled (Phase 0 scaffold)
==> Recording install-info.json
Reboot required: no
install exit=0
```

A second `sudo make install` run reported every file `unchanged`, `group palm-gw exists`,
`include line already in /etc/nftables.conf`, and warned that `UPLINK=eth0` was ignored because a
config already exists. (Idempotent.)

**Acceptance checks:**

```
$ for u in nftables palm-gw-dnsmasq palm-gwd-nm-bridge; do systemctl is-active $u; done
active
active
active
$ ip addr show br-palm
4: br-palm: <NO-CARRIER,BROADCAST,MULTICAST,UP> mtu 1500 qdisc noqueue state DOWN group default qlen 1000
    inet 10.42.0.1/24 brd 10.42.0.255 scope global noprefixroute br-palm
$ nmcli -t -f NAME,TYPE,DEVICE,STATE con show | grep palm
br-palm:bridge:br-palm:activated
$ sudo nft list tables | grep palm_gw
table inet palm_gw
$ sudo ss -ulpn 'sport = :53'
UNCONN 0 0 10.42.0.1:53 0.0.0.0:* users:(("dnsmasq",pid=3403,fd=6))
$ journalctl -u palm-gw-dnsmasq -o cat | head -4
started, version 2.91 cachesize 1000
DHCP, IP range 10.42.0.100 -- 10.42.0.199, lease time 12h
DHCP, sockets bound exclusively to interface br-palm
using nameserver 10.4.0.1#53
$ sudo sysctl -n net.ipv4.ip_forward net.ipv4.conf.all.rp_filter net.ipv4.conf.br-palm.proxy_arp
1
2
0
$ lsmod | grep -E '^(bnep|rfcomm|ppp_generic|ppp_async)' | awk '{print $1}' | tr '\n' ' '
ppp_async ppp_generic rfcomm bnep
$ sudo cat /var/lib/palm-gw/install-info.json | head -12
{ "board": "pi5", "model": "Raspberry Pi 5 Model B Rev 1.0", "kernel": "6.18.50+rpt-rpi-2712",
  "os": "Debian GNU/Linux 13 (trixie)", "bluez": "5.82", "pppd": "2.5.2", "networkmanager": "1.52.1",
  "nftables": "v1.1.3", "dnsmasq": "2.91", "adapter_mode": "onboard",
  "adapters": "Controller 2C:CF:67:2F:29:F5 piblueap [default]", "uplink": "eth0", "added_disable_bt": false }
```

**Daemon under its systemd unit** (one-off `systemctl start`; the unit stays disabled in Phase 0):

```
$ sudo systemctl start palm-gwd; systemctl is-active palm-gwd
active
$ systemctl show palm-gwd -p Type,NotifyAccess,WatchdogUSec
Type=notify NotifyAccess=main WatchdogUSec=1min
$ ls -ld /run/palm-gw /run/palm-gw/ctl.sock
drwxr-x--- 2 root palm-gw 60 Nov  8 12:41 /run/palm-gw
srw-rw---- 1 root palm-gw  0 Nov  8 12:41 /run/palm-gw/ctl.sock
$ palm-gwctl status                      # as sysadmin, no sudo
palm-gwd 0e07ed2-dirty, up 1s (phase 0)
config:    /etc/palm-gw/config.toml
adapter:   hci0  name "Palm Gateway"  class 0x000300
profiles:  spp ch1 on, lap ch2 on, dun ch3 on, pan on (br-palm)
network:   10.42.0.0/24 gw 10.42.0.1 uplink eth0  ppp 10.42.0.10-10.42.0.99  dhcp 10.42.0.100-10.42.0.199
allowlist: 0 static, allow_any=false
$ journalctl -u palm-gwd -o cat | grep -c '"msg":"palm-gwd ready"'
1
$ sudo systemctl stop palm-gwd; systemctl is-active palm-gwd
inactive                                 # /run/palm-gw removed by systemd
```

The first attempt failed with `permission denied on /run/palm-gw/ctl.sock` because systemd created
`/run/palm-gw` as `root:root`; fixed by having the daemon set the directory's group (DECISIONS
D-011), redeployed, and re-verified as shown above.

**Reboot:** `sudo systemctl reboot`; back in about 45 s. After boot, with nobody logged in:

```
nftables active, palm-gw-dnsmasq active, palm-gwd-nm-bridge active, palm-gwd inactive (not enabled, as intended)
br-palm 10.42.0.1/24 ; table inet palm_gw present ; dnsmasq on 10.42.0.1:53 ; ip_forward=1 rp_filter=2 proxy_arp=0
modules bnep rfcomm ppp_generic ppp_async loaded ; bluetoothctl list shows the onboard controller
```

**Uninstall and reinstall:** `sudo make uninstall` stopped and removed the three units, deleted the
`br-palm` NetworkManager connection, deleted `table inet palm_gw`, removed the include line and all
14 tracked files, and kept `/etc/palm-gw/config.toml` and `/var/lib/palm-gw`. `sudo make install
UPLINK=eth0` straight after brought everything back: three units active, `br-palm 10.42.0.1/24`.

**Lint on the Pi:** `golangci-lint` is not packaged in Trixie; built with
`GOTOOLCHAIN=auto go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0`
(34 s on the Pi 5; Go auto-fetched 1.26.10 because the linter requires it).

```
sysadmin@piblueap:~/palm-gw $ PATH=$PATH:~/go/bin make lint
golangci-lint run ./...
0 issues.
```

Copying note: `tar` on macOS adds `._*` AppleDouble files that the Go toolchain rejects; delete them
on the Pi (`find . -name '._*' -delete`) or copy with `COPYFILE_DISABLE=1 tar …`.

### Raspberry Pi 4

Not available in the test rig (DECISIONS.md, hardware observations). Recorded as untested for
Phase 0, as section 10 allows when only one board is present.

### CI

`.github/workflows/ci.yml` runs vet, golangci-lint, tests and the arm64 cross-build on
ubuntu-latest, and in a `golang:1.24-bookworm` container renders the nftables and dnsmasq templates
from `config.example.toml` and checks them with `nft -c -f` (nftables 1.0.6, the plan's minimum)
and `dnsmasq --test`. The first run (commit 0e07ed2) passed the Go job and failed the nft job with
`netlink: Error: cache initialization failed: Operation not permitted`: `nft -c` needs a netlink
socket even for a dry run. Fixed by running that container with `--cap-add=NET_ADMIN`.

## Phase 1: Adapter and agent

**Accept:** From a laptop, `bluetoothctl scan on` shows "Palm Gateway" with the expected class.
`palm-gwctl pair 2 --one` prints a PIN; pairing from the laptop succeeds and the window closes; the
address appears in `status` and `state.json`. Pairing with no window open is refused (`btmon` shows
the rejection). `palm-gwctl forget` removes the device from both the state file and
`bluetoothctl devices`. Kill `bluetooth.service` and restart it: the daemon logs `bluez_lost` then
`bluez_found` and `status` is healthy within 10 s.

**Result: passed on the Pi 5, 2026-11-09**, with a Palm TX as the pairing client in place of a
laptop (D-017). The Raspberry Pi 4 is untested because none is available.

### Development machine, 2026-11-08

`make test lint build-arm64` green. New unit tests: `internal/agent` (pairing window open, close
reasons, `--one`, timeout, adapter failure, PIN randomness), `internal/allow`, `internal/state`
(atomic save, reload, corrupt file is an error), `internal/setup` (main.conf block against Debian's
stock file: placement, parking live keys, idempotence, byte-exact removal, mode preserved).

### Raspberry Pi 5, 2026-11-08

**Install** (`sudo make install`, over the Phase 0 install): main.conf block written, bluetooth
restarted, `palm-gwd enabled and (re)started`.

```
$ palm-gwctl status
palm-gwd c851da6-dirty, up 5s (phase 1)
bluez:     ok, agent registered since 17:31:44
adapter:   hci0 2C:CF:67:2F:29:F5  alias "Palm Gateway"  class 0x400300  powered=yes discoverable=yes pairable=no  (configured)
pairing:   closed (palm-gwctl pair to open)
allowlist: 0 entries
$ bluetoothctl show | grep -E 'Alias|Class|Discoverable:|Pairable'
	Alias: Palm Gateway
	Class: 0x00400300 (4195072)
	Discoverable: yes
	Pairable: no
```

The first run showed `Pairable: yes` with no window; fixed (DECISIONS D-013) and re-verified above.

**BlueZ restart** (after fixing the release race in DECISIONS D-016), three consecutive runs of
`systemctl stop bluetooth; sleep 2; systemctl start bluetooth`, polling until `status` shows the
agent registered, the adapter found and `Pairable: no`:

```
run 1: while down: bluez:     NOT RUNNING (lost 1 times) | healthy 1116 ms after bluetooth start
run 2: while down: bluez:     NOT RUNNING (lost 2 times) | healthy 1147 ms after bluetooth start
run 3: while down: bluez:     NOT RUNNING (lost 3 times) | healthy 1146 ms after bluetooth start
$ systemctl show palm-gwd -p NRestarts
NRestarts=0
17:33:51.033 agent released by BlueZ
17:33:51.034 BlueZ went away bluez_lost
17:33:53.128 BlueZ is present bluez_found
17:33:54.153 adapter configured
17:33:54.154 agent registered
```

**Pairing window without a client:** `palm-gwctl pair 1 --one` prints the PIN and the Palm steps;
`Pairable: yes` while it is open, `no` after Ctrl-C; a second `pair` while one is open is refused
(`a pairing window is already open (closes 5:33PM); cancel it first`); opening a window immediately
after a BlueZ restart works.

**allow / forget without a client:**

```
$ palm-gwctl allow 00:07:E0:12:34:56
00:07:E0:12:34:56 added to the allowlist
$ palm-gwctl allow 00:07:e0:12:34:56
00:07:E0:12:34:56 was already in the allowlist
$ palm-gwctl forget 00:07:E0:12:34:56
00:07:E0:12:34:56: allowlist removed, BlueZ device not known
$ palm-gwctl forget nonsense; echo $?
palm-gwctl forget: expected one Bluetooth address XX:XX:XX:XX:XX:XX
2
```

**main.conf round trip:** stripping the block from the live file reproduced
`/var/lib/palm-gw/main.conf.orig` exactly (`cmp`); re-applying printed `unchanged`.

### Pairing with a real Palm, 2026-11-09

Client: Palm TX (address 00:07:E0:3C:5E:FA, Bluetooth class 0x100114, PDA), driven by hand by
the user. A `btmon -T` trace ran on the Pi throughout (`/var/tmp/phase1-btmon.txt`).

**Discovery.** Prefs ▸ Bluetooth ▸ Trusted Devices ▸ Add Device lists "Palm Gateway".

**Pairing inside a window (legacy PIN).** From the Mac:

```
$ ssh sysadmin@10.4.0.241 palm-gwctl pair 5 --one
Pairing window open for 5m0s.
On the Palm: Prefs > Communication > Bluetooth > Setup Devices > Trusted Devices > Add Device,
then tap the gateway when it appears in the list.
When asked for a passkey, enter:   6 0 2 4
Waiting... (Ctrl-C closes the window)
  00:07:E0:3C:5E:FA  "Zoë's Palm TX"  PIN requested
  00:07:E0:3C:5E:FA  "Zoë's Palm TX"  paired, added to allowlist
Window closed (one device).
```

Daemon log for the same run (trimmed):

```
05:25:12.562 window_open      minutes=5 one=true
05:25:53.416 pin_requested    addr=00:07:E0:3C:5E:FA name="Zoë's Palm TX"
05:25:59.091 paired           addr=00:07:E0:3C:5E:FA trusted=true
05:25:59.099 window_closed    reason=one_device
```

`btmon` shows the legacy flow: HCI PIN Code Request, PIN Code Request Reply with the window's PIN,
then Link Key Notification. Afterwards:

```
$ palm-gwctl status
adapter:   hci0 2C:CF:67:2F:29:F5  alias "Palm Gateway"  class 0x400300  powered=yes discoverable=yes pairable=no  (configured)
pairing:   closed (palm-gwctl pair to open)
allowlist: 1 entries
  00:07:E0:3C:5E:FA  dynamic  Zoë's Palm TX
$ sudo cat /var/lib/palm-gw/state.json
{ "version": 1, "devices": { "00:07:E0:3C:5E:FA": { "address": "00:07:E0:3C:5E:FA",
  "name": "Zoë's Palm TX", "first_paired": "2026-11-09T05:25:59.091120413-07:00", ... } } }
$ bluetoothctl info 00:07:E0:3C:5E:FA | grep -E "Paired|Bonded|Trusted"
	Paired: yes
	Bonded: yes
	Trusted: yes
```

Pairable returned to `no` when the window closed.

**Bug found and fixed: `--one` after the minutes was ignored.** The first run
(`palm-gwctl pair 5 --one`, 05:21) logged `one=false` and left the window open after the Palm
paired. Go's `flag` package stops at the first positional argument, and the CLI re-parsed only
once after the command name. `parseInterleaved` in `internal/ctl/cli.go` now resumes after every
positional argument; `TestParseInterleaved` covers flags before, between and after positionals,
`--`, and unknown flags. The run above is with the fix deployed.

**`forget` on a real paired device** (after the first pairing, 05:23):

```
$ palm-gwctl forget 00:07:E0:3C:5E:FA
00:07:E0:3C:5E:FA: allowlist removed, BlueZ device removed
$ sudo cat /var/lib/palm-gw/state.json
{ "version": 1, "devices": {} }
$ bluetoothctl devices
$
```

**Pairing with no window open is refused.** After `palm-gwctl forget` (05:31:09), the user deleted
"Palm Gateway" from the Palm's Trusted Devices, ran Add Device, picked the gateway and entered
`1234`. The Palm reported that pairing failed. `btmon`:

```
> HCI Event: Connect Request (0x04)                    05:32:12.523443  00:07:E0:3C:5E:FA (Palm Inc.)
> HCI Event: Connect Complete (0x03)                   05:32:12.727555  Status: Success
> HCI Event: Remote Name Req Complete (0x07)           05:32:12.768554  Name: Zoë's Palm TX
> HCI Event: PIN Code Request (0x16)                   05:32:19.101340  00:07:E0:3C:5E:FA
< HCI Command: PIN Code Request Negative Reply (0x000e) 05:32:19.101358  00:07:E0:3C:5E:FA
```

The kernel answers the PIN request itself in 18 µs because the adapter is not bondable
(`Pairable: no`), so the request never reaches bluetoothd or the agent. The agent's own
`no_window` rejection is the second layer and is covered by unit tests. The Palm disconnected at
05:32:19.337; `state.json` stayed empty and `bluetoothctl info` showed `Paired: no`.

The Palm did not list the gateway on the user's first two no-window attempts. `hciconfig` showed
`PSCAN ISCAN` and HCI Read Scan Enable returned 0x03 both with and without a window, so the Pi
was discoverable throughout; the third attempt found it. A
single Palm inquiry appears to miss the Pi at times. Watch for this in Phase 3 testing.

**Palm network wizard.** After pairing, the Palm TX's Setup Devices wizard asks for a user name
and password and will not continue with them blank (PLAN.md section 11 says to leave them blank, or set "unassigned").
The user entered placeholder values. The gateway's PPP options use `noauth`, so the Palm is never
asked for them; to confirm in Phase 2.

### Raspberry Pi 4

Not available; untested.

