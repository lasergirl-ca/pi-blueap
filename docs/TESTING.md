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


## Phase 2: RFCOMM profiles and PPP

**Accept:** `fake-palm connect --mode client` on channel 1 gets `CLIENTSERVER`, a `pppN`, a
`10.42.0.1x` address, and pings `10.42.0.1`. `--mode ppp` on channel 2 and `--mode at` on channel 3
both succeed. `fake-palm storm` leaves zero sessions and zero `pppd` processes. `palm-gwctl
disconnect` tears a session down within 3 s. Then the **real Palm T|X**: with the connection set up
per section 11, tapping Connect reaches "Established" and Blazer loads `http://10.42.0.1/` and
`http://neverssl.com/`.

### Development machine, 2026-11-09

`go test ./...`, `go test -race ./internal/ppp ./internal/profile`, `golangci-lint run` (also with
`GOOS=linux`) all clean. New tests:

- `internal/profile` handshake, driven over a socketpair: PPP flag first, `CLIENT`, `CLIENT` split
  across two writes, `CLIENTCLIENT`, a repeated `CLIENT`, `CLIENT` followed at once by a PPP frame
  (left in the socket for pppd), CR before `CLIENT` (the Palm OS 4 login script), AT commands
  ending in an `ATDT0` dial, lowercase `at` with CRLF, `ATZ\rATDT0\r` in one read, an AT line split
  mid-command, `ATH` hang-up, AT on SPP (not emulated), garbage, silence (timeout), peer close,
  cancellation, and both handshakes switched off.
- `internal/profile` SDP: each Appendix C record parses with `encoding/xml` and carries the channel
  in its RFCOMM element.
- `internal/ppp`: pool order, exhaustion and reuse; a fake pppd script that starts a child in its
  process group (like the `notty` shunt): disconnect kills both within 3 s; `per_device_max`
  replacement; `max_sessions` refusal (and replacement still allowed when full); termination during
  the handshake; a pppd that ignores SIGTERM is SIGKILLed after 2 s; a pppd that exits by itself
  releases its address; the orphan tripwire over a fake `/proc`.
- `internal/bluez`: sysfs bus detection and `auto` adapter choice (D-014).
- Added after the real-Palm dial-up runs: the T|X's slow first AT command (D-023: a 600 ms pause
  that outlasts `ms_timeout_ms` but not `at_timeout_ms`; DUN silence times out at
  `at_timeout_ms`), the T|X's compound dial line, `&D2` not taken as a dial, and
  `TestParseATLine` (D-024); `handshake.at_timeout_ms` validation in `internal/config`.

### Raspberry Pi 5, 2026-11-09

Same tests pass natively on the Pi (`go test ./...` in `~/palm-gw`). Deployed `palm-gwd`
(phase 2):

```
$ palm-gwctl status
palm-gwd f202472-dirty, up 3s (phase 2)
profiles:  spp ch1, lap ch2, dun ch3
sessions:  0 of 8, pool 0 of 10.42.0.10-10.42.0.99 in use
$ bluetoothctl show | grep -E "UUID: (Serial|LAN|Dial)"
	UUID: Dialup Networking         (00001103-0000-1000-8000-00805f9b34fb)
	UUID: Serial Port               (00001101-0000-1000-8000-00805f9b34fb)
	UUID: LAN Access Using PPP      (00001102-0000-1000-8000-00805f9b34fb)
```

With the profiles registered BlueZ adds the Networking service bit, so the adapter class is now
0x420300 (was 0x400300 in Phase 1).

**Real Palm TX.** Set up per section 11 (Connection `Palm GW`: Connect to PC, via Bluetooth,
device Palm Gateway; Network service `Palm GW`: PPP, Query DNS, automatic IP; Palm Wi-Fi off).
The test page was served with `python3 -m http.server 80 --bind 10.42.0.1` as a transient unit;
`palm_input` already accepts tcp/80, so no temporary firewall rule was needed. Tapping Connect
reached "Established", and Blazer loaded both `http://10.42.0.1/` and `http://neverssl.com/`.

```
08:21:41.334 connection    profile=lap channel=2 addr=00:07:E0:3C:5E:FA
08:21:41.351 handshake     mode=none profile=lap
08:21:41.351 session_start profile=lap handshake=none peer=10.42.0.10 pid=10076
             pppd: Using interface ppp0
             pppd: local  IP address 10.42.0.1
             pppd: remote IP address 10.42.0.10
08:21:41.417 session_up    ifname=ppp0 local=10.42.0.1 peer=10.42.0.10
$ journalctl -u palm-test-http
10.42.0.10 - - [09/Nov/2026 08:22:55] "GET / HTTP/1.1" 200 -
$ palm-gwctl status
sessions:  1 of 8, pool 1 of 10.42.0.10-10.42.0.99 in use
  00:07:E0:3C:5E:FA  "Zoë's Palm TX"      lap ppp0   up        10.42.0.10     none  up 3m17s
```

From RFCOMM connect to PPP up took 83 ms. DNS went through dnsmasq on 10.42.0.1 from `ppp0`
(neverssl.com resolved) although dnsmasq has `interface=br-palm`; `listen-address=10.42.0.1`
covers it.

**The Palm TX's "PC" connection uses LAP, not SPP.** `btmon` shows its SDP Service Search for
UUID 0x1102 ("LAN Access Using PPP"), then RFCOMM on channel 2, then PPP with no `CLIENT`.
Section 11 expected SPP with the Microsoft handshake. The gateway serves all three, so this
works; it is recorded in DECISIONS.md (hardware observations) and section 11 will be corrected
in `docs/PALM-SETUP.md`.

**`palm-gwctl disconnect`** on the Palm's live session:

```
$ t0=$(date +%s%N); palm-gwctl disconnect 00:07:E0:3C:5E:FA; echo "disconnect returned after $(( ($(date +%s%N)-t0)/1000000 )) ms"
00:07:E0:3C:5E:FA: 1 session(s) terminated
disconnect returned after 64 ms
$ palm-gwctl status | grep sessions
sessions:  0 of 8, pool 0 of 10.42.0.10-10.42.0.99 in use
$ pgrep -a pppd; ip -br link | grep ppp
$
08:25:11.852 session_end addr=00:07:E0:3C:5E:FA profile=lap ifname=ppp0 duration=3m31s exit=16 reason=disconnect
```

pppd's own syslog lines also land in the palm-gwd journal next to the structured copies the
daemon logs from its stderr; harmless duplication.

**Real Palm TX, dial-up (DUN).** Run to try the Modem set-up from `docs/PALM-SETUP.md`. LAN
Access was switched off in `/etc/palm-gw/config.toml` (`[lap] enabled = false`) and palm-gwd
restarted, so SDP offered only Serial Port and Dial-up Networking; `btmon` ran as a transient unit
writing to `/var/tmp/phase2-palm2-btmon.txt`. A new Connection `Palm GW DUN` (Connect to: Modem,
via Bluetooth, device Palm Gateway) and Network service (phone `0`, PPP, Query DNS, automatic IP)
were created on the Palm. Every attempt, whichever connection was tapped, searched SDP for
Dial-up Networking (0x1103) and opened RFCOMM channel 3. None searched for Serial Port.

Attempt 1 (09:42, 11:08, 11:09; Palm: "modem not found (0x1106)"). The Palm sends its first AT
command 3.5 s after the channel opens; the handshake gave up at `ms_timeout_ms` (3 s) and started
pppd blind, so the Palm's `ATQ0V1Z` got LCP frames instead of `OK`:

```
11:08:13.446 connection profile=dun channel=3
11:08:16.447 handshake  mode=timeout profile=dun         # 3.000 s later
btmon  11:08:16.949  > ATQ0V1Z\r                          # 3.503 s after the channel opened
       then +++ , ATQ0H0\r , ATQ0V1Z\r ... DISC
```

Fixed by D-023 (`at_timeout_ms`, 10 s on the DUN channel), deployed, retried.

Attempt 2 (11:19, 11:37, 14:11). All three AT commands were answered `OK`, then the handshake
timed out 5 s after the last one with no dial:

```
btmon  11:19:10.335  > ATQ0V1Z\r                     < \r\nOK\r\n
       11:19:12.404  > ATQ0E0\r                      < \r\nOK\r\n
       11:19:13.091  > ATQ0E0&D0&C1V1S7=90L1DT0\r    < \r\nOK\r\n
11:19:18.092 handshake mode=timeout at_commands=3 dial=""
```

The dial `DT0` is the last command in a compound line; the emulator only matched `^ATD`. Fixed by
D-024 (the line is parsed as V.250 basic syntax), deployed, retried.

Attempt 3 (14:24, then 14:28 with `at_connect_banner = "CONNECT"`; Palm: stuck at "Connecting",
View Log empty). The dial is recognised and answered, pppd starts, and the Palm never sends a
byte of PPP. It only returns RFCOMM credits for the LCP Configure-Requests it receives:

```
14:24:24.895 handshake     mode=at at_commands=3 dial=ATQ0E0&D0&C1V1S7=90L1DT0
14:24:24.895 session_start profile=dun handshake=at peer=10.42.0.10 pid=14502
btmon  14:24:24.895  < \r\nCONNECT 115200\r\n
       14:24:24.903  < 7e ff 7d 23 c0 21 7d 21 ...   (LCP Configure-Request, repeated every 3 s)
       14:24:33.918  > credits only, no data, until the Palm hangs up
14:25:45.912 session_end   profile=dun duration=1m27s exit=16 reason=pppd_exit
```

The bare `CONNECT` banner made no difference (the Palm sent `+++` when cancelled at 14:28:42).
RFCOMM modem status from the gateway is `rtc 1 rtr 1 dv 1` from the moment the channel opens, so
the Palm never sees carrier (DV) rise at `CONNECT`, although its `&C1` asks for DCD to follow
carrier. That is the leading suspect, unconfirmed. **Dial-up from a T|X does not work yet and is
parked (D-025).** It is not part of this phase's acceptance: the real-Palm check uses the "PC"
connection (passed above over LAN Access), and DUN acceptance is `fake-palm --mode at`.

Not run: tapping the "PC" connection with LAN Access off, to see whether it falls back to Serial
Port or to dial-up. The 09:42 attempt was probably that test (it searched for 0x1103), but which
connection was tapped was not recorded.

Afterwards `/etc/palm-gw/config.toml` was restored from `/var/tmp/config.toml.before-lap-test`
(LAN Access on, banner `CONNECT 115200`), palm-gwd restarted (`profiles: spp ch1, lap ch2, dun
ch3`), and the `palm-test-http` and `palm-test-btmon` units stopped.

### fake-palm acceptance on the Pi, 2026-11-10

**Rig (D-027).** A TP-Link UB500 (USB `2357:0604`, Realtek RTL8761BU, firmware
`rtl8761bu_fw.bin` 0xdfc6d922) on the Pi's USB 3 port became `hci1` (`60:15:6F:E9:EB:03`,
bus usb). The gateway stays on the onboard `hci0` (`2C:CF:67:2F:29:F5`, serial). fake-palm was
rebuilt on the Pi from the working tree and run by `scripts/phase2-accept.sh`.

**What the first runs found, each fixed and redeployed before the final run:**

1. `hci1` would not power on (`Set Powered ... status 0x03`). It was soft-blocked:
   `/sys/class/rfkill/rfkill17` (`hci1`) had `soft=1`, because systemd-rfkill restores a saved
   block for that USB port path (`/var/lib/systemd/rfkill/platform-xhci-hcd.1-usb-0:1:1.0:bluetooth`
   was `1`). The script now unblocks the client adapter and waits for it to power up. Right after
   an unblock, bluetoothd powers the adapter itself and a parallel `power on` answers `Busy`
   (0x0a), so it retries.
2. Pairing then passed (SSP, `paired ... in 3.913s (paired=true bonded=true)`, gateway logged
   `paired, added to allowlist`, window closed after one device). All four connects reached PPP and
   pinged `10.42.0.1`, but DNS through the gateway answered SERVFAIL ("server misbehaving"). Cause:
   fake-palm's client pppd ran with section 9's `usepeerdns`, and Debian's
   `/etc/ppp/ip-up.d/0000usepeerdns` rewrote the Pi's own `/etc/resolv.conf` to
   `nameserver 10.42.0.1` (a namespace shares `/etc`). The dnsmasq log showed `ignoring
   nameserver 10.42.0.1 - local interface` with no other upstream. fake-palm no longer passes
   `usepeerdns` with `--netns` (D-027). The Pi's resolv.conf was restored by the ip-down hook and
   is unchanged.
3. With DNS working, `GET http://neverssl.com/` timed out on 2 of 4 channels. It was neverssl.com:
   from the Pi itself, 3 of 5 `curl -4 http://neverssl.com/` timed out and the rest took 2 to 5 s
   to connect, `ping 34.223.124.45` lost 4 of 4, while `example.com` and
   `detectportal.firefox.com` answered in about 10 ms and `ping 1.1.1.1` lost none. fake-palm
   gained `--url` (default unchanged), and the script takes `URL=` (D-027). The final run uses
   `http://example.com/`.
4. `palm-gwctl disconnect` ended the gateway's session in 32 ms, but fake-palm's pppd held its
   link for the full 60 s. bluetoothd keeps its own reference to every connected RFCOMM socket
   (`/proc/<bluetoothd>/fd/40` was the session's socket, its receive queue growing from 92 to 337
   bytes after the session ended), so pppd exiting did not close the channel. Fixed by D-028: the
   daemon keeps its copy and calls `shutdown(SHUT_RDWR)` when a session ends. The script now
   also checks that fake-palm sees the link drop.
5. One run tested an old daemon: `install.sh` prefers `dist/linux-arm64/` over `bin/`, and the
   rsync had copied a stale `dist/` (2026-11-09 07:57) from the Mac. That run's DUN handshake timed
   out after 3 s, because that binary had no D-023. `dist/` was deleted on the Pi and is now
   excluded from the sync. After `sudo make install`, `cmp ~/palm-gw/bin/palm-gwd
   /usr/local/bin/palm-gwd` matched.
6. A later run flagged `orphan_pppd`. It was the reaper correctly catching the `ppp` package's
   test binary, run on the Pi at 07:24, whose fake pppd was not in the session table. The script
   now reads the journal only from its own start.

**Final run**, palm-gwd built from the working tree (`f202472-dirty`, with D-023, D-024 and
D-028), fake-palm already paired by the earlier run:

```
$ URL=http://example.com/ ~/palm-gw/scripts/phase2-accept.sh
==> Preconditions (2026-11-10 07:29:08)
gateway hci0 2C:CF:67:2F:29:F5; fake-palm hci1 60:15:6F:E9:EB:03 (bus usb)
    PASS: two adapters
==> Pair fake-palm with the gateway (once)
    PASS: 60:15:6F:E9:EB:03 already in the allowlist
==> Connect: channel 1, mode client
07:29:11.883 fake-palm: RFCOMM connected 60:15:6F:E9:EB:03 -> 2C:CF:67:2F:29:F5 channel 1 in 3.42s
07:29:12.033 fake-palm: PPP up: ppp0 local 10.42.0.10 remote 10.42.0.1 (3.57s after connect)
07:29:14.075 fake-palm: ping 10.42.0.1 ok: 3 packets transmitted, 3 received, 0% packet loss
07:29:14.115 fake-palm: DNS through 10.42.0.1 ok: neverssl.com = 34.223.124.45, 2600:1f13:...
07:29:14.191 fake-palm: GET http://example.com/ ok: 200 OK, 577 bytes in 76ms
    PASS: channel 1 client
==> Connect: channel 2, mode ppp
07:29:16.473 fake-palm: PPP up: ppp0 local 10.42.0.10 remote 10.42.0.1 (244ms after connect)
07:29:18.647 fake-palm: GET http://example.com/ ok: 200 OK, 577 bytes in 108ms
    PASS: channel 2 ppp
==> Connect: channel 3, mode at
07:29:21.066 fake-palm: PPP up: ppp0 local 10.42.0.10 remote 10.42.0.1 (360ms after connect)
07:29:23.207 fake-palm: GET http://example.com/ ok: 200 OK, 577 bytes in 76ms
    PASS: channel 3 at
==> Connect: channel 3, mode at-tx
07:29:29.058 fake-palm: PPP up: ppp0 local 10.42.0.10 remote 10.42.0.1 (3.807s after connect)
07:29:31.215 fake-palm: GET http://example.com/ ok: 200 OK, 577 bytes in 80ms
    PASS: channel 3 at-tx
==> Disconnect within 3 s
  60:15:6F:E9:EB:03  "piblueap #2"        lap ppp0   up        10.42.0.10     none  up 1s
$ palm-gwctl disconnect 60:15:6F:E9:EB:03
60:15:6F:E9:EB:03: 1 session(s) terminated
disconnect returned after 37 ms
    PASS: disconnect in 37 ms, 0 sessions left
07:29:36.528 fake-palm: FAIL: DNS through 10.42.0.1: ... connect: network is unreachable
07:29:36.529 fake-palm: pppd exited: the gateway ended the link
    PASS: fake-palm saw the link drop within 3049 ms
==> Storm
07:30:05.109 fake-palm: storm done: 20 iterations, 0 client-side failures; waiting 6 s for the gateway to settle
07:30:11.116 fake-palm: gateway: 0 sessions, 0 pool addresses in use, 0 gateway pppd processes
    PASS: storm
==> Afterwards
sessions 0, pppd processes 0
    PASS: zero sessions and zero pppd
    PASS: no orphan_pppd
==> Result: 0 failed step(s). Log: /var/tmp/phase2-accept-20261110-072908.log
```

The `FAIL: DNS` line inside the disconnect step is expected: the gateway cut the link while
fake-palm's probe was still running. The journal for the run shows the four handshakes as
`ms_null_modem` (spp, `clients=1`), `none` (lap), `at` (`dial=ATDT0`) and `at` (`at_commands=3`,
`dial=ATQ0E0&D0&C1V1S7=90L1DT0`). The 3.4 s RFCOMM connect on channel 1 (first connection of a
run, and also seen at 07:10 and 07:15) is page and connection setup between two controllers in
one box; later connections take 110 to 170 ms. Full logs:
`/var/tmp/phase2-accept-20261110-072908.log` (final), `-070710.log` (first pairing, DNS failure),
`-071030.log` (neverssl.com), `-072521.log` (stale binary).

**Development machine:** `go test ./...`, `go vet ./...` (also `GOOS=linux`) and
`golangci-lint run ./...` green after these changes. The new `TestHangUpDropsLinkDespiteOtherHolders`
(a socketpair with a second descriptor standing in for bluetoothd) passed 300 times on macOS and
300 times on the Pi. It failed once on macOS, on its first run, by timing out after 2 s, and that
could not be reproduced in the 600 runs since. Recorded here in case it recurs.

**Not covered here:** the real Palm against D-028. The "PC" connection passed before the change
(above). Ending a session from the gateway side should now make the T|X show the link dropped at
once. To check with the Palm during Phase 5's real-Palm runs.

**Phase 2 accepted** on fake-palm, all channels, storm and disconnect, plus the real Palm T|X
over LAN Access. T|X dial-up is parked (D-025).

## Phase 3: Routing, NAT and isolation

**Accept:** From `fake-palm` (and the T|X): internet works; `nmap -sT -Pn 192.168.1.0/24 -p
22,80,443 -e pppN` from the fake-Palm side shows nothing open except hosts/ports in
`[[firewall.allow]]`; `dig @10.42.0.1 example.com` answers; `ssh 10.42.0.1` from the Palm side is
dropped (not refused); `nft list counters` shows the drop rule incrementing. Add a
`[[firewall.allow]]` block, `palm-gwctl reload`, and the new host is reachable without restarting
anything.

### Development machine, 2026-11-10

`go test ./...`, `go vet ./...` (also `GOOS=linux`) and `golangci-lint run ./...` green. New tests:
- `internal/fw`: `TestScript` (the plan's example, an empty config, and a config with `icmp`, a CIDR
  with host bits, a /32 inside a /23 and duplicates) and `TestNftAcceptsTemplateAndScript`
  (`nft -c` over the rendered table plus the elements, with and without `mss_clamp`; skipped
  without nft or without CAP_NET_ADMIN).
- `internal/daemon`: `TestLiveMergeSplitsAppliedFromRestart` (D-030).

### Raspberry Pi 5, 2026-11-10

`go test ./...` in `~/palm-gw` green. `sudo /var/tmp/fw.test -test.v` (the `fw` tests as root, so
`nft -c` runs against nftables 1.1.3): both PASS. `sudo make install UPLINK=eth0` re-rendered
`/etc/nftables.d/palm-gw.nft` (new `pi_allow` set) and restarted the units, and the installed
binary matched the build (`cmp`). Status after the restart:

```
palm-gwd dev, up 4s (phase 3)
firewall:  0 allow rule(s) (0 port, 0 icmp), 3 palm_to_pi, applied 07:54:59
$ sudo nft list set inet palm_gw pi_allow
		elements = { udp . 53,
			     udp . 67 }
```

`nmap` 7.95 and `dig` (bind9-dnsutils 9.20) were installed on the Pi for this phase.

**Acceptance with fake-palm** (`scripts/phase3-accept.sh`, D-031: a stand-in LAN host
`192.168.77.2` in namespace `palmlan`, because the real LAN was not scanned). The first run had two
script bugs, both fixed:
- The listener check ran before `python3 -m http.server` was up.
- `curl` in the namespace resolved through the Pi's own LAN resolver `10.4.0.1`, which the
  firewall rightly drops from the Palm side (`palm->rfc1918`). It now resolves through
  `10.42.0.1`, as a Palm does.

Final run:

```
$ URL=http://example.com/ ~/palm-gw/scripts/phase3-accept.sh
==> Preconditions (2026-11-10 07:58:01)
firewall:  0 allow rule(s) (0 port, 0 icmp), 3 palm_to_pi, applied 07:57:59
    PASS: gateway 2C:CF:67:2F:29:F5, firewall managed, no allow rules
==> Stand-in LAN host 192.168.77.2 in namespace palmlan
from inside palmlan: 22/tcp/open 80/tcp/open 443/tcp/open
    PASS: listeners on 22, 80, 443
==> fake-palm link (channel 2), held for the checks
07:58:15.485 fake-palm: PPP up: ppp0 local 10.42.0.10 remote 10.42.0.1 (3.229s after connect)
07:58:17.663 fake-palm: GET http://example.com/ ok: 200 OK, 577 bytes in 100ms
  60:15:6F:E9:EB:03  "piblueap #2"        lap ppp0   up        10.42.0.10     none  up 9s
==> Internet and DNS from the Palm side
GET http://example.com/ via 104.20.23.154: 200
    PASS: GET http://example.com/
$ inns dig +short +time=3 +tries=1 @10.42.0.1 example.com
172.66.147.243
104.20.23.154
    PASS: dig @10.42.0.1 example.com answers
==> The Pi itself from the Palm side
nc -z -w 3 10.42.0.1 22: exit 1 after 3016 ms
    PASS: ssh to 10.42.0.1 dropped (timed out, not refused)
gateway address: 22/tcp/filtered 80/tcp/closed 443/tcp/filtered 8042/tcp/filtered
Pi LAN address 10.4.0.241: 22/tcp/filtered 80/tcp/closed 443/tcp/filtered 8042/tcp/filtered
palm_input 'palm->pi' counter: 18 -> 31
    PASS: palm->pi drop counter increments
    PASS: 22 filtered on 10.42.0.1
    PASS: 22 filtered on the Pi's LAN address
==> LAN host without an allow rule
192.168.77.2: 22/tcp/filtered 80/tcp/filtered 443/tcp/filtered
palm_forward 'palm->rfc1918' counter: 23 -> 32
    PASS: nothing open on 192.168.77.2
    PASS: palm->rfc1918 drop counter increments
==> Add [[firewall.allow]] for 192.168.77.2 tcp/80, then palm-gwctl reload
$ palm-gwctl reload
reloaded: firewall.allow
$ sudo nft list set inet palm_gw lan_allow
		elements = { 192.168.77.2 . tcp . 80 }
192.168.77.2: 22/tcp/filtered 80/tcp/open 443/tcp/filtered
    PASS: only the allowed port is reachable
    PASS: GET http://192.168.77.2/ through the allow rule
    PASS: same PPP session throughout (nothing restarted)
==> Remove the rule, reload again
$ palm-gwctl reload
reloaded: firewall.allow
192.168.77.2: 22/tcp/filtered 80/tcp/filtered 443/tcp/filtered
    PASS: closed again
==> Result: 0 failed check(s). Log: /var/tmp/phase3-accept-20261110-075801.log
```

Port 80 on the Pi shows `closed` (an RST), not `filtered`: `palm_input` accepts it for the status
page (section 8.12), and nothing listens there until Phase 7. Afterwards the script restored
`config.toml` and removed `palmlan` and the veth pair (checked: `ip netns list` shows only
`fakepalm`, no `lan-pi` link).

**The rest of Phase 3, checked by hand on the Pi:**

```
$ sudo nft list chain inet palm_gw palm_forward | grep maxseg       # MSS clamp
		oifname "eth0" tcp flags syn tcp option maxseg size set rt mtu
$ sudo systemctl reload nftables          # reloads the table from disk: sets empty
$ sudo nft list set inet palm_gw pi_allow | grep -c elements
0
refilled after ~6s                         # by the daemon's 30 s re-apply (D-030)
		elements = { udp . 53,
			     udp . 67 }
```

`reload` with `dns_upstream = ["1.1.1.1", "9.9.9.9"]` and `name = "Palm Gateway 2"` in
`config.toml`:

```
$ palm-gwctl reload
reloaded: network.dns_upstream
dnsmasq configuration rewritten and dnsmasq restarted
not applied until palm-gwd restarts (sudo systemctl restart palm-gwd): bluetooth.name
$ sudo grep -E "^(no-resolv|server=)" /etc/palm-gw/dnsmasq.conf
no-resolv
server=1.1.1.1
server=9.9.9.9
$ dig +short +time=3 @10.42.0.1 example.com
104.20.23.154
172.66.147.243
adapter:   hci0 ... alias "Palm Gateway" ...          # name unchanged until a restart, as listed
```

Restoring the file and reloading again put dnsmasq back on the Pi's resolv.conf (no `server=`
lines), and DNS kept answering. A reload with nothing changed prints `reloaded: no live settings
changed`. (That run printed the installer hint for any restart-only key. It now prints it only
for the rendered keys: subnet, gateway, uplink, mss_clamp, pan.bridge.)

**Real Palm T|X on the Phase 3 build.** Zoe had deleted the T|X's pairing on the Palm, so the
gateway side was cleared too and the Palm paired again first:

```
$ palm-gwctl forget 00:07:E0:3C:5E:FA
00:07:E0:3C:5E:FA: allowlist removed, BlueZ device removed
$ palm-gwctl pair 5 --one
When asked for a passkey, enter:   8 9 4 1
  00:07:E0:3C:5E:FA  "Zoë's Palm TX"  PIN requested
  00:07:E0:3C:5E:FA  "Zoë's Palm TX"  paired, added to allowlist
Window closed (one device).
```

Then Connect on the `Palm GW` connection, and Blazer loaded `http://example.com/` (Zoe, 08:04).
The gateway's log:

```
08:03:17.443 connection    profile=lap channel=2 addr=00:07:E0:3C:5E:FA name="Zoë's Palm TX"
08:03:17.460 handshake     mode=none profile=lap
08:03:17.460 session_start profile=lap handshake=none peer=10.42.0.10 pid=28176
08:03:17.519 session_up    ifname=ppp0 local=10.42.0.1 peer=10.42.0.10
$ palm-gwctl status
  00:07:E0:3C:5E:FA  "Zoë's Palm TX"      lap ppp0   up        10.42.0.10     none  up 1m15s
firewall:  0 allow rule(s) (0 port, 0 icmp), 3 palm_to_pi, applied 08:04:26
```

**Phase 3 accepted** on fake-palm (stand-in LAN host, D-031) and the real Palm T|X.
