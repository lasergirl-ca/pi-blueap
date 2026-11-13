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
once. To check with the Palm during Phase 5's real-Palm runs. (Checked on 2026-11-10 on the
Phase 3 build: see "D-028 with the real Palm" under Phase 3.)

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

### D-028 with the real Palm, 2026-11-10

The Phase 2 item left open: ending a session from the gateway side with the real T|X. Zoe
connected `Palm GW` (LAN Access), then on the Pi:

```
$ palm-gwctl status | grep ppp
  00:07:E0:3C:5E:FA  "Zoë's Palm TX"      lap ppp0   up        10.42.0.10     none  up 15s
$ palm-gwctl disconnect 00:07:E0:3C:5E:FA      # sent 09:55:20.902
00:07:E0:3C:5E:FA: 1 session(s) terminated     # returned 09:55:20.945
$ palm-gwctl status | grep sessions
sessions:  0 of 8, pool 0 of 10.42.0.10-10.42.0.99 in use
```

Journal (palm-gwd and bluetoothd, pppd lines shortened):

```
09:55:20.904 pppd          Terminating on signal 15
09:55:20.906 pppd          Modem hangup
09:55:20.944 session_end   profile=lap ifname=ppp0 duration=15s exit=16 reason=disconnect
09:55:20.950 bluetoothd    ext_io_disconnected() Unable to get io data for LAN Access using PPP:
                           getpeername: Transport endpoint is not connected (107)
09:55:25.062 device_disconnected  addr=00:07:E0:3C:5E:FA
```

The RFCOMM channel closed 6 ms after the session ended, even though bluetoothd still held its own
reference (its `getpeername` fails because the socket was shut down under it), and the ACL followed
about 4 s later. Zoe: the T|X showed the connection dropped right away. D-028 confirmed on the Palm.

## Phase 4: PAN

**Accept:** `fake-palm pan` gets a lease in the DHCP range and reaches the internet with the same
isolation. An Android phone paired through a window and toggled "Internet access" on the Pi
entry also works. `brctl show`/`bridge link` shows `bnep0` enslaved; NetworkManager does not list
`bnep0` as a managed device. (D-033: accepted on fake-palm alone; the Android check moves to
Phase 5.)

### Development machine, 2026-11-10

`go test ./...`, `go vet ./...` (also `GOOS=linux GOARCH=arm64`) and `golangci-lint run ./...`
green. New tests in `internal/pan`: `TestParseLeases` (dnsmasq lease lines, `*` hostnames, the
`duid` line), `TestReadLeasesMissingFile`, `TestBridgeAndPorts` (bridge and `brif/` ports from a
fake `/sys/class/net`). The BNEP ioctls are Linux only and are exercised on the Pi.

### Raspberry Pi 5, 2026-11-10

Deployed as before (rsync without `.git` and `dist`, `make build`, `sudo make install
UPLINK=eth0`, `cmp` of the installed daemon, `systemctl restart palm-gwd`). At start:

```
{"msg":"NAP server registered","component":"pan","event":"pan_registered","hci":"hci0","bridge":"br-palm"}
$ palm-gwctl status | grep ^pan
pan:       NAP on br-palm since 10:03:17, 0 client(s), DHCP 10.42.0.100-10.42.0.199
```

Issues found on the way, each fixed before the final run:

1. `fake-palm pan` found no `org.bluez.Network1` on the gateway's device object under `hci1`:
   fake-palm had paired before NAP was registered, and BlueZ only refreshes a device's cached
   service records when it pairs. Pairing again inside a window (`palm-gwctl pair 2 --one`,
   `fake-palm pair 2C:CF:67:2F:29:F5 --adapter hci1`) brought `Network1` in. fake-palm now says
   so, and the script pairs again when `Network1` is missing.
2. The daemon refused fake-palm's own outgoing connection: on the gateway host the kernel's BNEP
   list holds both ends, and fake-palm's side (role PANU, `dst` = the gateway) looked like an
   unknown client. Only NAP-role (0x1116) connections count now (D-032).
3. `palm-gwctl disconnect` removed the gateway's `bnep0`, but fake-palm still showed
   `Network1.Connected = true`: bluetoothd keeps the L2CAP socket open. The daemon now also
   calls `Device1.Disconnect` (D-032), and fake-palm sees the drop within about 2 s.
4. A refused device got through for up to one poll tick: in one run, fake-palm (trusted, removed
   from the allowlist) got a lease and one ping reply before `pan_refused` at 10:09:30.8, 1 s
   after it connected. This run did exercise the poll backstop:

   ```
   10:09:29.852 fake-palm: PAN connected 60:15:6F:E9:EB:03 -> 2C:CF:67:2F:29:F5: bnep1 in 3.642s
   10:09:30.040 fake-palm: DHCP lease after 3.829s: ip=10.42.0.103 ...
   10:09:33.071 fake-palm: ping 10.42.0.1 ok: 3 packets transmitted, 1 received, 66.6667% packet loss
   10:09:38.072 fake-palm: FAIL: DNS through 10.42.0.1: ... i/o timeout
   {"msg":"PAN client refused","component":"pan","event":"pan_refused","addr":"60:15:6F:E9:EB:03","name":"piblueap #2","ifname":"bnep0","reason":"not_allowed"}
   ```

   To close that window the daemon now clears `Trusted` on non-allowlisted devices at attach and
   on reload, so BlueZ asks the agent first (D-032). The final run below shows that path.
5. BlueZ authorizes PAN with the BNEP UUID `0000000f-…`, not NAP's `00001116-…` (PLAN.md 8.6
   amended).
6. Script fixes: `btmgmt` waits forever without a terminal (the script uses `busctl` for the
   client adapter now), and `pkill -f "fake-palm pan"` matched the SSH command that started the
   script (now anchored to the binary's path).

### Acceptance run, 2026-11-10

`URL=http://example.com/ scripts/phase4-accept.sh` on the Pi (run detached with `nohup`, log read
afterwards):

```

==> Preconditions (2026-11-10 10:14:54)
palm-gwd dev, up 3s (phase 4)
adapter:   hci0 2C:CF:67:2F:29:F5 (serial)  alias "Palm Gateway"  class 0x420300  powered=yes discoverable=yes pairable=no  (configured)
pan:       NAP on br-palm since 10:14:52, 0 client(s), DHCP 10.42.0.100-10.42.0.199
firewall:  0 allow rule(s) (0 port, 0 icmp), 3 palm_to_pi, applied 10:14:51
gateway hci0 2C:CF:67:2F:29:F5; fake-palm hci1 60:15:6F:E9:EB:03
    PASS: NAP registered on the bridge

==> fake-palm knows the gateway's NAP and is allowlisted (pair again if not)
    PASS: Network1 present on /org/bluez/hci1/dev_2C_CF_67_2F_29_F5, 60:15:6F:E9:EB:03 allowlisted

==> Stand-in LAN host 192.168.77.2 in namespace palmlan
    PASS: listeners on 22, 80, 443

==> fake-palm pan: lease, ping, DNS, HTTP (held for the checks)
10:15:09.070 fake-palm: PAN connected 60:15:6F:E9:EB:03 -> 2C:CF:67:2F:29:F5: bnep1 in 3.696s
10:15:09.102 fake-palm: bnep1 moved into namespace fakepalm
10:15:09.272 fake-palm: DHCP lease after 3.898s: ip=10.42.0.103 mask=24 router=10.42.0.1 dns=10.42.0.1 server=10.42.0.1 lease=43200
10:15:09.272 fake-palm: leased address 10.42.0.103 is in 10.42.0.100-10.42.0.199
10:15:11.303 fake-palm: ping 10.42.0.1 ok: 3 packets transmitted, 3 received, 0% packet loss, time 2002ms; rtt min/avg/max/mdev = 16.996/29.083/46.287/12.493 ms
10:15:11.327 fake-palm: DNS through 10.42.0.1 ok: neverssl.com = 34.223.124.45, 2600:1f13:37c:1400:ba21:7165:5fc7:736e
10:15:11.455 fake-palm: GET http://example.com/ ok: 200 OK, 577 bytes in 128ms
10:15:11.456 fake-palm: holding the connection for 10m0s (Ctrl-C to stop)
    PASS: lease in 10.42.0.100-10.42.0.199, ping, DNS and GET http://example.com/
pan:       NAP on br-palm since 10:14:52, 1 client(s), DHCP 10.42.0.100-10.42.0.199
  60:15:6F:E9:EB:03  "piblueap #2"        pan bnep0  10.42.0.103    up 4s
firewall:  0 allow rule(s) (0 port, 0 icmp), 3 palm_to_pi, applied 10:14:51
  60:15:6F:E9:EB:03  "piblueap #2"        pan bnep0  10.42.0.103    up 4s
    PASS: status lists 60:15:6F:E9:EB:03 on a bnep port with 10.42.0.103

==> bnep enslaved to the bridge, not managed by NetworkManager
$ /usr/sbin/bridge link show dev bnep0
157: bnep0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 master br-palm state forwarding priority 32 cost 100 
    PASS: bnep0 is a port of br-palm
$ nmcli -t -f DEVICE,TYPE,STATE device
eth0:ethernet:connected
br-palm:bridge:connected
lo:loopback:connected (externally)
2C\:CF\:67\:2F\:29\:F5:bt:disconnected
wlan0:wifi:unavailable
lan-pi:ethernet:unmanaged
    PASS: NetworkManager does not manage bnep0 (not listed)

==> Isolation over PAN (same rules as PPP)
nc -z -w 3 10.42.0.1 22: exit 1 after 3017 ms
    PASS: ssh to 10.42.0.1 dropped
    PASS: 22 filtered on the Pi's LAN address 10.4.0.241
palm_input 'palm->pi' counter: 0 -> 5
    PASS: palm->pi counter increments
192.168.77.2: 22/tcp/filtered 80/tcp/filtered 443/tcp/filtered ; palm->rfc1918 counter: 0 -> 6
    PASS: nothing open on 192.168.77.2
    PASS: palm->rfc1918 counter increments

==> palm-gwctl disconnect ends the PAN connection
$ palm-gwctl disconnect 60:15:6F:E9:EB:03
60:15:6F:E9:EB:03: PAN connection dropped
10:15:24.481 fake-palm: PAN connection dropped: the gateway ended it
10:15:24.482 fake-palm: disconnected
    PASS: fake-palm saw the connection drop
pan:       NAP on br-palm since 10:14:52, 0 client(s), DHCP 10.42.0.100-10.42.0.199
    PASS: no PAN clients left
{"time":"2026-11-10T10:15:22.162919022-07:00","level":"INFO","msg":"PAN client dropped","component":"pan","event":"pan_drop","addr":"60:15:6F:E9:EB:03","ifname":"bnep0","reason":"disconnect","err":""}
{"time":"2026-11-10T10:15:22.505649508-07:00","level":"INFO","msg":"PAN client disconnected","component":"pan","event":"pan_disconnected","addr":"60:15:6F:E9:EB:03","name":"piblueap #2","ifname":"bnep0","ip":"10.42.0.103","duration":"13s"}
    PASS: pan_disconnected logged

==> D-032: a trusted device that drops off the allowlist is refused
gateway's BlueZ: 60:15:6F:E9:EB:03 Trusted = b true (BlueZ skips AuthorizeService for it)
    PASS: 60:15:6F:E9:EB:03 removed from the allowlist behind the daemon's back
{"time":"2026-11-10T10:15:26.952710036-07:00","level":"WARN","msg":"trusted device is not allowlisted; trust cleared","component":"daemon","event":"untrusted","addr":"60:15:6F:E9:EB:03","name":"piblueap #2","why":"attach","err":""}
gateway's BlueZ: 60:15:6F:E9:EB:03 Trusted = b false
    PASS: trust cleared at start (untrusted)
10:15:32.035 fake-palm: Network1.Connect(nap): Input/output error
{"time":"2026-11-10T10:15:32.026756818-07:00","level":"WARN","msg":"service rejected","component":"agent","event":"service_rejected","addr":"60:15:6F:E9:EB:03","name":"piblueap #2","uuid":"0000000f-0000-1000-8000-00805f9b34fb"}
    PASS: AuthorizeService refused NAP for 60:15:6F:E9:EB:03
    PASS: never accepted as a client
    PASS: fake-palm got no lease
    PASS: state and trust restored, NAP registered again

==> Journal since the start
    PASS: no errors in the journal

==> Result: 0 failed check(s). Log: /var/tmp/phase4-accept-20261110-101454.log
```

**Not covered:** a non-BlueZ PAN client. Zoe has no Android device and macOS 27 has no Bluetooth
PAN client, so per D-033 the Android (or Windows) check is carried into Phase 5.

**Phase 4 accepted** on fake-palm (D-033).

## Phase 5: Robustness and both boards (in progress)

**Accept:** No orphans, no fd leaks (`ls /proc/$(pidof palm-gwd)/fd | wc -l` stable across 100
sessions), all units active after each disruption, soak link survives or reconnects on first tap.
Both Pi 4 and Pi 5 pass identically; differences recorded.

Zoe's answers on 2026-11-10: a Pi 4 will come later (Phase 5 stays open until it is done), and the
Pi joins a 5 GHz Wi-Fi network for the Wi-Fi uplink runs.

### Pi 5 with fake-palm, 2026-11-10

`scripts/phase5-robust.sh` (new): fd counts across a 100-session `fake-palm storm` and 10 PAN
sessions, then `systemctl restart bluetooth` and `systemctl restart palm-gwd`, each with fake-palm
holding a PPP link (channel 2) and a PAN connection. After each: the clients see the drop (PPP) or
carry on (PAN after a daemon restart, D-034), the daemon is attached with NAP registered, every
unit is active, nothing is left over, and fresh PPP and PAN connections reach the internet. Run
detached (`nohup ... < /dev/null &`), log read afterwards.

Issues found on the way:

1. One PAN reconnect in ten failed with `Input/output error`: fake-palm reconnected 40 ms after
   disconnecting, while the kernel still had the previous BNEP session for that address
   (bluetoothd: `bnep_connadd() Failed to add device bnep%d: File exists`). The next attempt
   worked. The script now waits 1 s between PAN sessions; no real client reconnects that fast.
2. After `systemctl restart palm-gwd` the PAN connection stayed up. That is BlueZ keeping the
   session when NAP is unregistered; the new daemon adopts it and traffic still flows (D-034). The
   script now expects that.
3. Killing a held `fake-palm pan` left the link up: the client's bluetoothd could not bring down
   the bnep interface fake-palm had moved into its namespace (`bnep_if_down() ... No such device`).
   fake-palm now disconnects on SIGTERM and Ctrl-C.
4. After a reboot the gateway came up on the USB dongle: the kernel had numbered the dongle `hci0`
   and the onboard radio `hci1`, the reverse of the boot before, and the config said `adapter =
   "hci0"`. Fixed by D-035 (pin by address); the scripts now find fake-palm's adapter themselves.
   A second reboot (onboard `hci1` again) kept the gateway on the onboard radio, and
   `scripts/phase4-accept.sh` passed 22 of 22 straight after it.

Final run (gateway on `hci1` by address, fake-palm on `hci0`, Wi-Fi uplink):

```
==> Preconditions (2026-11-10 11:29:07) (11:29:07)
palm-gwd dev, up 10s (phase 4)
bluez:     ok, agent registered since 11:28:57
adapter:   hci1 2C:CF:67:2F:29:F5 (serial)  alias "Palm Gateway"  class 0x420300  powered=yes discoverable=yes pairable=no  (configured by address)
bluez devices: 2
sessions:  0 of 8, pool 0 of 10.42.0.10-10.42.0.99 in use
pan:       NAP on br-palm since 11:28:58, 0 client(s), DHCP 10.42.0.100-10.42.0.199
gateway hci1 2C:CF:67:2F:29:F5; fake-palm hci0 60:15:6F:E9:EB:03; palm-gwd pid 2004
    PASS: daemon attached, NAP registered
    PASS: all units active (bluetooth palm-gwd palm-gw-dnsmasq palm-gwd-nm-bridge nftables NetworkManager)
sessions 0, gateway pppd 0, PAN clients 0
    PASS: start: nothing left over
palm-gwd open fds: 7
      1 anon_inode:[eventfd]
      1 anon_inode:[eventpoll]
      1 /dev/null
      4 socket:[N]

==> fd leaks: 100 PPP sessions (fake-palm storm) (11:29:07)
11:30:47.425 fake-palm: [99] ch1: connect, hang up at once
11:30:47.726 fake-palm: storm done: 100 iterations, 0 client-side failures; waiting 6 s for the gateway to settle
11:30:53.734 fake-palm: gateway: 0 sessions, 0 pool addresses in use, 0 gateway pppd processes
11:30:53.734 fake-palm: PASS: zero sessions and zero pppd left
sessions 0, gateway pppd 0, PAN clients 0
    PASS: storm: nothing left over
palm-gwd open fds: 7 before, 7 after 100 sessions
    PASS: no fd growth across 100 sessions

==> fd leaks: 10 PAN sessions (11:30:53)
10 of 10 PAN sessions completed (lease, ping, DNS)
    PASS: every PAN session worked
sessions 0, gateway pppd 0, PAN clients 0
    PASS: PAN cycles: nothing left over
palm-gwd open fds: 7 at start, 7 after the PAN cycles
    PASS: no fd growth across 10 PAN sessions

==> BlueZ restart with a PPP link and a PAN connection up (11:31:28)
sessions:  1 of 8, pool 1 of 10.42.0.10-10.42.0.99 in use
  60:15:6F:E9:EB:03  "Palm Gateway"       lap ppp0   up        10.42.0.10     none  up 6s
pan:       NAP on br-palm since 11:28:58, 1 client(s), DHCP 10.42.0.100-10.42.0.199
  60:15:6F:E9:EB:03  "Palm Gateway"       pan bnep0  10.42.0.103    up 4s
    PASS: PPP link and PAN connection up
$ sudo systemctl restart bluetooth
    PASS: bluetooth restart: fake-palm's PPP link ended
    PASS: bluetooth restart: fake-palm's PAN connection ended
11:31:34.418 fake-palm: pppd exited: the gateway ended the link
11:31:34.571 fake-palm: disconnected
    PASS: bluetooth restart: daemon attached again, NAP registered
    PASS: all units active (bluetooth palm-gwd palm-gw-dnsmasq palm-gwd-nm-bridge nftables NetworkManager)
sessions 0, gateway pppd 0, PAN clients 0
    PASS: bluetooth restart: nothing left over
    PASS: bluetooth restart: new PPP connection with internet
    PASS: bluetooth restart: new PAN connection with internet
sessions 0, gateway pppd 0, PAN clients 0
    PASS: bluetooth restart, after the new connections: nothing left over
{"time":"2026-11-10T11:31:34.350400408-07:00","level":"INFO","msg":"session ended","component":"ppp","event":"session_end","addr":"60:15:6F:E9:EB:03","name":"Palm Gateway","profile":"lap","ifname":"pp
{"time":"2026-11-10T11:31:34.431434772-07:00","level":"WARN","msg":"BlueZ went away","component":"daemon","event":"bluez_lost"}
{"time":"2026-11-10T11:31:34.492151361-07:00","level":"INFO","msg":"BlueZ is present","component":"daemon","event":"bluez_found"}
{"time":"2026-11-10T11:31:35.211628615-07:00","level":"INFO","msg":"PAN client disconnected","component":"pan","event":"pan_disconnected","addr":"60:15:6F:E9:EB:03","name":"Palm Gateway","ifname":"bne
{"time":"2026-11-10T11:31:36.212146233-07:00","level":"INFO","msg":"NAP server registered","component":"pan","event":"pan_registered","hci":"hci1","bridge":"br-palm"}
{"time":"2026-11-10T11:31:42.190556389-07:00","level":"INFO","msg":"session ended","component":"ppp","event":"session_end","addr":"60:15:6F:E9:EB:03","name":"Palm Gateway","profile":"lap","ifname":"pp
{"time":"2026-11-10T11:31:45.211198408-07:00","level":"INFO","msg":"PAN client disconnected","component":"pan","event":"pan_disconnected","addr":"60:15:6F:E9:EB:03","name":"Palm Gateway","ifname":"bne
palm-gwd open fds: 7 at start, 7 after the BlueZ restart
    PASS: no fd growth across the BlueZ restart

==> palm-gwd restart with a PPP link and a PAN connection up (11:31:45)
sessions:  1 of 8, pool 1 of 10.42.0.10-10.42.0.99 in use
  60:15:6F:E9:EB:03  "Palm Gateway"       lap ppp0   up        10.42.0.10     none  up 6s
pan:       NAP on br-palm since 11:31:36, 1 client(s), DHCP 10.42.0.100-10.42.0.199
  60:15:6F:E9:EB:03  "Palm Gateway"       pan bnep0  10.42.0.103    up 5s
    PASS: PPP link and PAN connection up
$ sudo systemctl restart palm-gwd
    PASS: palm-gwd restart: fake-palm's PPP link ended
    PASS: palm-gwd restart: daemon attached again, NAP registered
pan:       NAP on br-palm since 11:31:52, 1 client(s), DHCP 10.42.0.100-10.42.0.199
  60:15:6F:E9:EB:03  "Palm Gateway"       pan bnep0  10.42.0.103    up 3s
    PASS: palm-gwd restart: the PAN connection carried on and the new daemon lists it
    PASS: palm-gwd restart: traffic still flows over it
11:31:51.919 fake-palm: pppd exited: the gateway ended the link
11:31:49.118 fake-palm: holding the connection for 10m0s (Ctrl-C to stop)
    PASS: palm-gwd restart: daemon attached again, NAP registered
    PASS: all units active (bluetooth palm-gwd palm-gw-dnsmasq palm-gwd-nm-bridge nftables NetworkManager)
sessions 0, gateway pppd 0, PAN clients 0
    PASS: palm-gwd restart: nothing left over
    PASS: palm-gwd restart: new PPP connection with internet
    PASS: palm-gwd restart: new PAN connection with internet
sessions 0, gateway pppd 0, PAN clients 0
    PASS: palm-gwd restart, after the new connections: nothing left over
palm-gwd pid 2004 -> 4385
{"time":"2026-11-10T11:31:51.882453343-07:00","level":"INFO","msg":"session ended","component":"ppp","event":"session_end","addr":"60:15:6F:E9:EB:03","name":"Palm Gateway","profile":"lap","ifname":"pp
{"time":"2026-11-10T11:31:51.882509713-07:00","level":"INFO","msg":"palm-gwd stopping","component":"daemon"}
{"time":"2026-11-10T11:31:51.939026388-07:00","level":"INFO","msg":"palm-gwd starting","version":"dev","phase":"4","config":"/etc/palm-gw/config.toml","state":"/var/lib/palm-gw"}
{"time":"2026-11-10T11:31:51.965934859-07:00","level":"INFO","msg":"palm-gwd ready","ctl_socket":"/run/palm-gw/ctl.sock"}
{"time":"2026-11-10T11:31:52.942100495-07:00","level":"INFO","msg":"NAP server registered","component":"pan","event":"pan_registered","hci":"hci1","bridge":"br-palm"}
{"time":"2026-11-10T11:31:56.94165303-07:00","level":"INFO","msg":"PAN client disconnected","component":"pan","event":"pan_disconnected","addr":"60:15:6F:E9:EB:03","name":"Palm Gateway","ifname":"bnep
{"time":"2026-11-10T11:32:02.654490715-07:00","level":"INFO","msg":"session ended","component":"ppp","event":"session_end","addr":"60:15:6F:E9:EB:03","name":"Palm Gateway","profile":"lap","ifname":"pp
{"time":"2026-11-10T11:32:05.941650928-07:00","level":"INFO","msg":"PAN client disconnected","component":"pan","event":"pan_disconnected","addr":"60:15:6F:E9:EB:03","name":"Palm Gateway","ifname":"bne
palm-gwd open fds: 7 at start (old process), 7 now
    PASS: the new daemon has no more fds than the old one did

==> Journal since the start (11:32:06)
    PASS: no orphan_pppd
    PASS: no errors in the journal

==> Result: 0 failed check(s). Log: /var/tmp/phase5-robust-20261110-112907.log (11:32:06)
```

**Reboots.** `systemctl reboot` twice, with the Wi-Fi uplink: all six units active about a minute
after boot, the daemon attached, NAP registered, default route on `wlan0`, palm-gwd at 7 open fds,
no ERROR lines in the boot's journal. (A real power cut with the Palm connected is still to do.)

### Wi-Fi uplink, 2026-11-10

Zoe added the Pi to the 5 GHz network `WarpCore`; NetworkManager had Wi-Fi switched off, so
`nmcli radio wifi on`. `wlan0` joined on channel 48 (5240 MHz), signal 81. Then:

```
$ sudo sed -i 's/^uplink .*/uplink = "wlan0" .../' /etc/palm-gw/config.toml      # backup in /var/tmp
$ sudo nmcli con mod "Wired connection 1" ipv4.never-default yes ipv6.never-default yes
$ sudo nmcli dev reapply eth0                       # eth0 keeps 10.4.0.241 for SSH, no default route
$ ip route show default
default via 10.4.0.1 dev wlan0 proto dhcp src 10.4.0.243 metric 600
$ sudo make install && sudo systemctl restart palm-gwd
$ sudo nft list chain inet palm_gw postrouting | grep masq
		ip saddr 10.42.0.0/24 oifname "wlan0" masquerade
```

With the gateway on the onboard radio and Wi-Fi busy on 5 GHz: `scripts/phase3-accept.sh` 14 of
14 (internet, DNS, isolation, allow rule and reload; the Pi's LAN address is now `10.4.0.243`) and
`scripts/phase4-accept.sh` 22 of 22. The robustness run above was also on the Wi-Fi uplink.

Fixed on the way: the installer's 2.4 GHz warning used `iw`, which Pi OS Lite does not ship, so it
could never fire; it now falls back to `nmcli` (reads 5240 MHz here). `scripts/phase3-accept.sh`
read the uplink from the `network:` status line with a trailing comma that Phase 4 removed; fixed.
One `phase4-accept.sh` run started straight after `phase3-accept.sh` hung in `fake-palm pan` with
no output; it did not recur in three later runs.

### T|X dial-up and carrier detect (D-025), 2026-11-10

Built the RFCOMM TTY path from D-025 (DUN channel moved to `/dev/rfcommN` with
`RFCOMMCREATEDEV`/`RFCOMM_REUSE_DLC`, carrier cleared with `TIOCMBIC TIOCM_CD`, raised before
`CONNECT`, pppd on the TTY) and a fake-palm `--dcd` check (its own RFCOMM TTY, reading
`TIOCMGET`). First run: `open /dev/rfcomm0: invalid argument`, because the request carried
channel 0 and the kernel checks the channel when the TTY opens; fixed by copying the socket's
addresses and channel into the request. Second run:

```
$ sudo fake-palm connect 2C:CF:67:2F:29:F5 --adapter hci1 --channel 3 --mode at-tx --dcd --netns fakepalm --internet
11:17:49.736 fake-palm: client side on /dev/rfcomm1
11:17:53.263 fake-palm: sent "ATQ0V1Z\r", got "\r\nOK\r\n"
11:17:53.299 fake-palm: sent "ATQ0E0\r", got "\r\nOK\r\n"
11:17:54.302 fake-palm: handshake (at-tx) failed: carrier is on before the dial (the gateway should hold it off)
palm-gwd: "DUN on an RFCOMM TTY, carrier off until the dial" tty=/dev/rfcomm0
```

The kernel's `tty_tiocmset` keeps only DTR, RTS, OUT1, OUT2 and LOOP from a modem-line request, so
carrier cannot be set from user space on any TTY, and the RFCOMM socket has no call for it. The
change was reverted; D-036 records why T|X dial-up stays unsupported.

### Real T|X over the Wi-Fi uplink, 2026-11-10

Baseline before the T|X runs (12:31:41): `palm-gwd` 7 fds, 0 sessions, no pppd.

The first attempt sat on "Initializing" on the T|X. bluetoothd accepted the LAP channel twice
but never handed the connection to `palm-gwd` (no `connection` event), while fake-palm connected
on the same channel at the same time. Cause unknown. After forgetting the T|X and pairing again
(`palm-gwctl forget 00:07:E0:3C:5E:FA`, `palm-gwctl pair 5 --one`), the LAP session came up at
12:40:09 and stayed reliable from then on.

A session over the Wi-Fi uplink: CradleSync "Secure HotSync" ran through the gateway (8m47s
session, about 2.1 MB received by the Palm). The only LAN-isolation drops were 3 TCP SYNs from
the Palm to `10.4.0.7:14243` on the home LAN, which is the isolation rule doing its job.

20 Connect/Disconnect toggles (Prefs ▸ Network, Disconnect then Connect): the gateway logged 23
Connects and 23 Disconnects between 14:02:55 and 14:33:39. Every Connect produced a `connection`
event and a `session_start` in the same second, with no failed or half-made attempts in the
gateway or bluetoothd logs. The only bluetoothd line is BlueZ's harmless
`ext_io_disconnected() ... getpeername: Transport endpoint is not connected` on each hang-up.
The last session ended with the T|X closing PPP cleanly (`LCP terminated by peer`, 3m17s,
353 KB sent to the Palm).

```
$ sudo journalctl -u palm-gwd --since 14:02:50 -o cat | grep -c '"session_start"'
23
$ sudo journalctl -u palm-gwd --since 14:02:50 -o cat | grep -c '"session_end"'
23
$ sudo journalctl -u palm-gwd --since 14:02:50 -o cat | grep -cE '"level":"(WARN|ERROR)"'
0
$ sudo ls /proc/$(pgrep -x palm-gwd)/fd | wc -l
7
$ pgrep -a pppd; palm-gwctl status | grep ^sessions
sessions:  0 of 8, pool 0 of 10.42.0.10-10.42.0.99 in use
```

BlueZ restart mid-session: the T|X had a LAP session up (4m52s). `sudo systemctl restart
bluetooth` at 15:38:02. The session ended cleanly (`Modem hangup`, no pppd left), and
`palm-gwd` was back at 15:38:04 with the agent, all three profiles and NAP registered again
(`recovered 1 times`).

**First tap failed:** the T|X stuck on "Initializing", twice (15:39:03 and 15:41:13). btmon:

```
> RFCOMM: Set Async Balance Mode (SABM) (0x2f)  Address: 0x13 cr 1 dlci 0x04   15:39:03.609
< HCI Authentication Requested ... Link Key Request Reply ... Auth Complete: Success
< Set Connection Encryption ... Encryption: Enabled with E0                     15:39:03.794
  (no UA or DM for dlci 4)
< RFCOMM: Disconnect (DISC) dlci 0x00                                          15:39:32.564
```

`bluetoothd` debug log (`kill -USR2`) for the second attempt:

```
src/profile.c:ext_confirm() incoming connect from 00:07:E0:3C:5E:FA
src/profile.c:ext_confirm() LAN Access using PPP authorizing connection from 00:07:E0:3C:5E:FA
(nothing until the Palm dropped the link 28 s later)
```

The agent was never called, and the T|X's `info` file has no `Services=` line
(`ServicesResolved` false). D-037 has the BlueZ code path. This is also what this morning's
"Initializing" was, after the reboots. The fix adds `ReverseServiceDiscovery = false` to the
managed block. After `sudo make install`, which wrote the block and restarted BlueZ at
16:05:18 with `ServicesResolved` still false, the T|X connected on the first tap:

```
src/profile.c:ext_confirm() LAN Access using PPP authorizing connection from 00:07:E0:3C:5E:FA
src/profile.c:ext_auth() 00:07:E0:3C:5E:FA authorized to connect to LAN Access using PPP
src/profile.c:ext_connect() LAN Access using PPP connected to 00:07:E0:3C:5E:FA
palm-gwd: 19:01:10.069 connection lap, 19:01:10.080 session_start, 19:01:10.139 session_up
```

The T|X's own idle timeout (Prefs ▸ Network ▸ Details ▸ Idle timeout) hangs up a quiet session.
One session at 19:01 ended after 1m29s with `LCP terminated by peer` and 4 bytes received. For
the restart tests and the soak it is set to Never.

`palm-gwd` restart mid-session: the T|X had a LAP session up (24 s). `sudo systemctl restart
palm-gwd` at 19:45:30. pppd ended with `Terminating on signal 15` and the session was logged as
ended (D-034: PPP sessions do not survive a daemon restart). `palm-gwd ready` came in the same
second, the link to the T|X dropped at 19:45:34, and there were 7 fds and no pppd. The T|X
reconnected on the first tap:

```
19:47:08.237 connection
19:47:08.253 session_start
19:47:08.321 session_up
```

Power cut with the T|X connected: the power cable was pulled with a LAP session up since 19:47.
The Pi booted at 19:52:56 (new boot ID), and `palm-gwd`, `bluetooth` and `palm-gw-dnsmasq`
were active, with `palm-gwd ready` at 19:53:03. There were no WARN or ERROR lines in the daemon
log and 7 fds. The radios came up swapped (onboard on hci0), and `adapter = "2C:CF:67:2F:29:F5"`
still picked the onboard one (D-035). Wi-Fi uplink: `default via 10.4.0.1 dev wlan0`, ping out
fine. The only boot noise was a one-off `brcmf_cyw_external_auth: External authentication
failed` before wlan0 associated. `ServicesResolved` was false again (a fresh `bluetoothd`), and
the T|X connected on the first tap:

```
20:03:04.844 connection
20:03:04.861 session_start
20:03:04.937 session_up
```

24-hour soak: deferred to field use (D-038). The logger ran from 20:03 to 20:05 and was stopped
with the T|X still connected (session up 2m15s).

### Still to do for Phase 5

- Everything above on a Pi 4 (Zoe will get one).
- Android or Windows as a PAN client if one turns up (D-033).

### Uplink back on eth0, 2026-11-10

`nmcli con mod "Wired connection 1" ipv4.never-default no ipv6.never-default no` and `nmcli dev
reapply eth0`. Then `uplink = "eth0"` in `/etc/palm-gw/config.toml` (the adapter stays pinned
by address, D-035) and `sudo make install` to re-render `/etc/nftables.d/palm-gw.nft`: a
`palm-gwd` restart alone leaves the NAT rule on the old interface, because the uplink is rendered
at install time (D-030). Wi-Fi stays associated as a backup route, which the gateway does not
use:

```
default via 10.4.0.1 dev eth0 proto dhcp src 10.4.0.241 metric 100
default via 10.4.0.1 dev wlan0 proto dhcp src 10.4.0.243 metric 600
network:   10.42.0.0/24 gw 10.42.0.1 uplink eth0
45:		ip saddr 10.42.0.0/24 oifname "eth0" masquerade
```

`scripts/phase3-accept.sh` (fake-palm, `URL=http://example.com/`): 14 of 14 passed, 0 failed
(`/var/tmp/phase3-accept-20261110-200653.log`).

### USB dongle mode on the Pi 5, 2026-11-10

`sudo make install ADAPTER=dongle`, over the existing install. The config is kept, and it
pinned the onboard radio by address (D-035). The installer added `dtoverlay=disable-bt` once and
recorded `added_disable_bt: true` (kept on a re-run), but it said nothing about the config. With
that config the gateway would have found no adapter after the reboot. The installer now warns:

```
==> Bluetooth adapter: dongle
    dtoverlay=disable-bt added to /boot/firmware/config.txt
WARNING: adapter = "2C:CF:67:2F:29:F5" in /etc/palm-gw/config.toml is not a USB dongle here; with the onboard radio off the gateway will not find it. Set adapter = "auto", "hci0" or the dongle's address before rebooting
```

After setting `adapter = "60:15:6F:E9:EB:03"`, the re-run says `adapter = "60:15:6F:E9:EB:03" in
/etc/palm-gw/config.toml is the USB dongle`. The same run fixed `install-info.json`'s
`adapters`: `bluetoothctl list` had included a `[CHG]` event line with colour codes, and only
the `Controller` lines are kept now.

Reboot at 20:23:35. Only `hci0` (usb) exists, and the RTL8761BU firmware loaded. `palm-gwd`,
`bluetooth`, `palm-gw-dnsmasq` and `nftables` were active, and `palm-gwd` was ready at 20:23:51
with no WARN or ERROR lines:

```
adapter:   hci0 60:15:6F:E9:EB:03 (usb)  alias "Palm Gateway"  class 0x420300  powered=yes discoverable=yes pairable=no  (configured by address)
```

**First T|X pairing with the dongle** (window from `palm-gwctl pair 5 --one`, run under
`systemd-run`, because the window closes when the command exits). The T|X paired (20:28:54
`pin_requested`) and connected (20:29:30 `session_up`). But the daemon logged no `paired`, the
T|X was not marked trusted, and the window closed only on its timeout. The T|X got in only
because it was still on the allowlist from the onboard pairing.

**Second pairing, after `palm-gwctl forget 00:07:E0:3C:5E:FA`** (a true new device), with
`busctl monitor org.bluez` recording. `pin_requested` at 20:34:36 was again followed by no
`paired`, and the next Connect was refused (20:36:02 `service_rejected`). The capture:

```
03:34:36.922833 PropertiesChanged /org/bluez/hci0/dev_00_07_E0_3C_5E_FA  "Bonded" true
03:34:37.109255 PropertiesChanged /org/bluez/hci0/dev_00_07_E0_3C_5E_FA  "Connected" false
03:34:37.109726 GetAll reply: "Paired" true, "Bonded" true, "Trusted" false
```

BlueZ never signals `Paired` while services are unresolved, and with D-037 a Palm's never are.
D-040 has the source. The fix makes the daemon treat `Bonded` as `Paired`, handle each pairing
once, and also handle a connected device's `InterfacesAdded`.

**Third pairing, with the fix, after forgetting the T|X again:**

```
20:39:47.025 device_connected      (from InterfacesAdded; unlogged before the fix)
20:39:59.219 pin_requested
20:39:59.341 paired                (trusted=true)
20:39:59.350 window_closed         (--one)
20:40:54.829 device_connected
20:40:55.269 connection
20:40:55.304 session_start
20:40:55.395 session_up
```

```
allowlist: 2 entries
  00:07:E0:3C:5E:FA  dynamic  Zoë's Palm TX
bluez devices: 2
  00:07:E0:3C:5E:FA  "Zoë's Palm TX"      class 0x100114  paired,trusted
```

The session ended at 20:42:14 after 1.3 minutes and 567 KB to the Palm, with pppd's `Error
reading standard input: Connection timed out`. That is a Bluetooth link timeout, not a PPP
hang-up, so the T|X was probably turned off or out of range. fds were 7 afterwards.

The Pi stays in dongle mode from here on, at Zoe's request. The gateway runs on the UB500, with
`dtoverlay=disable-bt` set. Until the onboard radio is enabled again, fake-palm has no client
radio, so the fake-palm acceptance scripts cannot run on this Pi. Re-enabling it means removing
the `dtoverlay=disable-bt` line and rebooting. The gateway stays pinned to the dongle by
address, and the onboard radio becomes fake-palm's client.

## Phase 6: Setup wizard and packaging (accepted 2026-11-11 on the Pi 5; Pi 4 untested)

Started 2026-11-10 with Phase 5's Pi 4 runs outstanding (D-041).

### Part 1: detection and `palm-gw setup --check`, 2026-11-10

`internal/setup/detect.go` covers what PLAN.md section 8.11 lists:
- **Board** from `/proc/device-tree/model`.
- **Adapters** from BlueZ, with their sysfs bus. With none, it checks `dtoverlay=disable-bt` and
  the kernel log for a firmware failure.
- **Uplink candidates** from `ip -j route show default`, leaving out `br-palm`, `bnep*`, `ppp*`,
  `lo`, `docker*` and `veth*`, with the Wi-Fi band from `iw`, or `nmcli` because Pi OS Lite has
  no `iw`.
- **Pi 4 serial-console conflict** from `cmdline.txt`. A Pi 5's `serial0` is its debug UART,
  so it is not flagged.
- **Pi 5 supply** from `/proc/device-tree/chosen/power/max_current` and `vcgencmd
  get_throttled`.
- **Existing config** for pre-filling.
- **Subnet collisions** against host addresses and routes, leaving out palm-gw's own `br-palm`
  and `ppp*`, so a re-run of setup does not collide with itself. It suggests the next free
  10.N.0.0/24.

`go test ./internal/setup/` uses fixtures taken from the Pi 5 rig's real `ip -j` output.

On the Pi 5 (dongle mode, Ethernet and Wi-Fi uplinks):

```
$ sudo palm-gw setup --check
board:     pi5 (Raspberry Pi 5 Model B Rev 1.0)
adapters:  hci0 60:15:6F:E9:EB:03  USB dongle (usb)  powered=yes
           onboard radio disabled (dtoverlay=disable-bt)
uplinks:   eth0 via 10.4.0.1 metric 100
           wlan0 via 10.4.0.1 metric 600  Wi-Fi 5240 MHz
power:     supply 1500 mA, get_throttled=0x0
config:    /etc/palm-gw/config.toml (will be pre-filled)
subnet:    10.42.0.0/24 free
warnings:
  - Pi 5 supply negotiated 1500 mA (under 5 A), so USB ports share 600 mA; a Bluetooth dongle needs about 50 mA, which is fine unless other USB devices draw a lot
```

The rig's supply negotiates 1500 mA, not 5 A. That is fine for the dongle, but worth knowing
if other USB devices are added.

### Part 2: the apply step in Go (D-042), 2026-11-10

`go test ./internal/setup/` covers:
- both layouts' static files: units point at the layout's binaries, the bridge unit reads
  `bridge.env`, and no template action is left over;
- `ApplySystem` on a fake root: every configuration file is written, the `nft -c` check runs
  before any unit starts, then the bridge, dnsmasq, sysctl and `palm-gwd` start in that order;
  a second run changes nothing and restarts neither the bridge nor Bluetooth, and the include
  line is not duplicated;
- dongle mode: it warns about an adapter pinned to the onboard radio, adds `disable-bt` once,
  keeps the mode when it is not given, and takes the line out again for onboard;
- `RemoveSystem`, then a reinstall that restores dongle mode, then a purge.

On the Pi 5 (dongle mode, T|X paired), `sudo make install` over the Phase 5 install (no
`ADAPTER=`):

```
==> Static files (source layout)
    /etc/systemd/system/palm-gwd-nm-bridge.service written
==> Configuration files
    /etc/palm-gw/bridge.env written
==> Bluetooth adapter mode
    dtoverlay=disable-bt already in /boot/firmware/config.txt
    adapter = "60:15:6F:E9:EB:03" is the USB dongle
==> Units
    systemctl restart palm-gwd-nm-bridge      (bridge.env is new)
    systemctl start palm-gw-dnsmasq           (dnsmasq.conf unchanged)
Reboot required: no
```

`sudo make uninstall` then removed the units, binaries, helper scripts, NetworkManager and
modules-load files, the nftables file and include line, sysctl file, `bridge.env`,
`dnsmasq.conf` and the `disable-bt` line, and deleted the `br-palm` connection. Afterwards
`cmp /etc/bluetooth/main.conf /var/lib/palm-gw/main.conf.orig` matched, only
`table inet filter` was left in nftables, and `/etc/palm-gw` held only `config.toml`.

`sudo make install` again restored everything, including dongle mode from `install-info.json`.
`config.toml` and `state.json` were byte-identical to copies taken before the uninstall. The
T|X was still `paired,trusted` and allowlisted, and all five units were active. `--apply` now
ends with detection's warnings (here, the 1500 mA supply), which replaces `install.sh`'s
shell checks for the Wi-Fi band and the Pi 4 console UART.

### Part 3: the wizard (`palm-gw setup`), 2026-11-11

`internal/setup/wizard.go` asks only what detection cannot answer:
- the **name**;
- the **adapter**, only when both an onboard radio and a dongle are present: onboard, dongle
  with the onboard radio kept, or dongle with the onboard radio turned off;
- the **uplink**, only with more than one default route;
- the **subnet**, only on a collision;
- the **web UI**: on or off, username, and password, which is kept if one is stored; plus
  **HTTPS** with a self-signed certificate;
- **pair now**.

Answers come from flags, then `PALM_GW_*`, then the existing config. `--yes` or
`PALM_GW_UNATTENDED=1` never prompts, and stops with a clear message if a required answer
(the first web password) is missing. config.toml is edited key by key (`SetKeys`): a new
install starts from the embedded `config.example.toml`, and a re-run changes only the lines
whose answers changed, keeping comments and alignment. The web password is stored as argon2id
in PHC form. The certificate is ECDSA P-256, covers the host name, the uplink address and
`10.42.0.1` (D-039), and is reused while it covers the same names. `config.toml` gains
`[web].username` (D-026). Prompts use charmbracelet/huh on a terminal (section 2), or plain
lines otherwise (`--plain`). `--dry-run` shows the `config.toml` changes and writes nothing.

`go test ./internal/setup/` covers:
- `SetKeys` on fixtures and on the shipped example (exactly the requested lines change, and
  it still validates);
- argon2id hashing and verification;
- the certificate: reused for the same names, new when an address is added;
- an interactive run with a password mismatch and retry;
- unattended runs: no password is refused; a subnet collision renumbers the gateway and both
  pools; web off needs no password;
- an unchanged re-run produces no edits.

On the Pi 5 (dongle mode, no web password stored), all `--dry-run`, with nothing written
(`config.toml` kept its 20:23 timestamp):

```
$ printf '\n2\ny\n\n<pw>\n<pw>\ny\nn\n' | sudo palm-gw setup --plain --dry-run
  Bluetooth: the USB dongle (hci0 60:15:6F:E9:EB:03)      (no adapter question: only the dongle is visible)
Which interface connects the Pi to the internet?
  1) eth0 via 10.4.0.1
  2) wlan0 via 10.4.0.1 (Wi-Fi 5240 MHz)
==> Dry run: nothing written or applied
    - uplink     = "eth0"                             # interface to NAT out of
    + uplink     = "wlan0"                            # interface to NAT out of
    - password_hash  = ""                 # argon2id; written by `palm-gw setup` or `palm-gw web set-password`
    + password_hash  = "(new argon2id hash)"
    adapter mode: dongle, pair at the end: false

$ sudo env PALM_GW_UNATTENDED=1 palm-gw setup --dry-run
palm-gw setup: answer required: a web UI password (PALM_GW_WEB_PASSWORD or --web-password-file), or turn the web UI off (PALM_GW_WEB=off)

$ sudo env PALM_GW_UNATTENDED=1 PALM_GW_WEB_PASSWORD=<pw> PALM_GW_NAME="Zoë Test GW" PALM_GW_UPLINK=wlan0 palm-gw setup --dry-run
    (three line changes: name, uplink, password_hash)

$ sudo env PALM_GW_UNATTENDED=1 PALM_GW_WEB=off palm-gw setup --dry-run
    - enabled        = true
    + enabled        = false
```

A short password file is refused (`at least 8 characters`). With no terminal and stdin closed,
the wizard stops with `cancelled; nothing was changed`.

**Interactive runs by Zoe** (`sudo palm-gw setup` over SSH, huh prompts), 2026-11-11:
- **06:41:** named the gateway "Palm Gateway - Surrey".
- **06:44:** a second run set the name back to "Palm Gateway". Username `sysadmin`, a password
  of her choice, HTTPS on.

Each run changed the name in the `main.conf` block, so each restarted BlueZ, and `palm-gwd`
recovered both times (`bluez_lost`, then `adapter configured` with the new alias). That is
correct behaviour, not churn: re-applying afterwards (`palm-gw setup --apply`) reported
`managed block unchanged` and left `main.conf`'s timestamp alone.

Result:
- `config.toml` (0640) has the argon2id hash and `username = "sysadmin"`. The key was appended
  at the end of `[web]`, because this config predates the key.
- The certificate `/var/lib/palm-gw/tls/cert.pem` (key 0600) is `CN=piblueap`, with
  SAN `DNS:piblueap, DNS:piblueap.local, IP:10.4.0.241, IP:10.42.0.1`, valid to 2036.
- All five units are active, and the T|X is still paired, trusted and allowlisted.

**Pairing during setup**, 2026-11-11. Zoe removed the pairing on the T|X, and
`palm-gwctl forget 00:07:E0:3C:5E:FA` removed it on the gateway, so the T|X was new to both
sides. She ran `sudo palm-gw setup` with the same answers (stored password kept) and "Pair a
Palm now? Yes". The wizard opened a `--one` window and showed the PIN. The T|X paired and
connected, and the wizard printed that the T|X was online before it exited. Gateway log:

```
07:24:56.983 window_open
07:25:23.905 pin_requested
07:25:24.054 paired            (trusted, allowlisted; D-040's Bonded path)
07:25:24.063 window_closed     (--one)
07:26:00.671 connection
07:26:00.819 session_up        (lap ppp0 10.42.0.10)
```

That covers the wizard's whole interactive path on the Pi 5. The fresh-flash acceptance
repeats it from a new image.

### Part 4: the .deb (D-043), 2026-11-11

`make deb DEB_VERSION=0.6.0~test1` on the Mac produced `dist/palm-gw_0.6.0~test1_arm64.deb`
(11 MB). On the Pi, `dpkg-deb -I` / `-c` showed:
- `Depends: bluez (>= 5.66), ppp (>= 2.4.9), dnsmasq-base, nftables, network-manager`;
- one conffile, `/etc/ppp/options.palm`;
- binaries in `/usr/bin`, `fake-palm` in `/usr/libexec/palm-gw`, and units, the bridge helper,
  NetworkManager and modules-load files under `/usr/lib`;
- documentation in `/usr/share/doc/palm-gw`, and postinst, prerm and postrm.

**From the source install to the package, on the Pi 5** (dongle mode, T|X paired):
1. Backed up `config.toml`, `state.json`, `install-info.json` and `tls/` to
   `/var/tmp/p6-backup`.
2. `sudo make uninstall PURGE=1` and `groupdel palm-gw`, for a true pre-install state.
3. Snapshotted `/etc`: sha256 of every file and symlink, every directory, and
   `/boot/firmware/config.txt`, 1414 lines in all.
4. **Fresh install:** `apt-get install ./palm-gw_0.6.0~test1_arm64.deb` gave `palm-gw:
   sysadmin added to group palm-gw` and `palm-gw is installed. Run 'sudo palm-gw setup' to
   finish.`. The units were `disabled` and `palm-gwd` was `inactive`, so nothing started
   without a config.
5. Restored the backups and ran `sudo palm-gw setup --yes --adapter dongle --pair no`, an
   unattended re-run with the stored password. It rendered every file, added `disable-bt`
   again, and reused the certificate (same fingerprint). Units ran from
   `/usr/lib/systemd/system`, and `install-info.json` recorded `"install_path": "deb"`.

**Upgrade:** `apt-get install ./palm-gw_0.6.0~test2_arm64.deb` over `~test1` printed
`Unpacking palm-gw (0.6.0~test2) over (0.6.0~test1)` and `palm-gw: applying the existing
configuration`. All five units were active afterwards. `sha256sum -c` of `config.toml`,
`state.json` and `tls/cert.pem` passed, and the T|X was still `paired,trusted` and
allowlisted.

**Purge:** `apt-get purge palm-gw`. Its prerm removed the bridge connection, the `main.conf`
block, the firewall table and include, the rendered files and `disable-bt`, and its postrm
deleted `/etc/palm-gw`, `/var/lib/palm-gw` and the group. Then:

```
$ diff /var/tmp/p6-etc-before.txt /var/tmp/p6-etc-after.txt; echo "diff rc=$?"
diff rc=0
```

`/etc` and `config.txt` were identical to the pre-install snapshot. Only `table inet filter`
was left in nftables, and there was no `br-palm` connection.

**Terminal junk under apt:** the first purge printed `]11;?\[6n` from prerm. That is
bubbletea's `init()` asking a terminal for its colours, and apt's pseudo-terminal never
answers. The maintainer scripts now capture `palm-gw`'s output. A remove-and-reinstall cycle
with `~test3` printed none (`grep -c ']11;\|\[6n'` gave 0).

**Restored:** `~test3` installed, the backups put back, and `palm-gw setup --yes --adapter
dongle` run. `apt remove` followed by `apt install` re-applied the kept config through
postinst. The Pi now runs the package (deb layout, dongle mode), with `config.toml` and
`state.json` byte-identical to the backups and the T|X paired and allowlisted.

A long-lived SSH session from before the purge kept its old membership of the deleted and
re-created `palm-gw` group, so `palm-gwctl` was refused the socket until a fresh login. As
postinst says, membership takes effect at the next login.

### Part 5: bootstrap installer, `palm-gw upgrade`, `make release` (D-044), 2026-11-11

No GitHub release exists yet, so each "release" was a directory on the Pi with what
`make release` produces: the package named after its tag, `install.sh` with the tag baked in,
and `SHA256SUMS`. `make deb DEB_VERSION=0.6.0~test4` and `~test5` built the packages;
`relbad` held `~test4`'s package under `~test5`'s name and sums. The installers were pointed
at the directories with `PALM_GW_BASE_URL=file:///tmp/relN` and `--from file:///tmp/relN`.
`make -n release RELEASE_TAG=v0.6.0-rc1` and `make -n publish RELEASE_TAG=v0.6.0-rc1`
showed package version `0.6.0~rc1`, asset `palm-gw_0.6.0-rc1_arm64.deb`, the tag baked into
`install.sh`, and `gh release create v0.6.0-rc1 --verify-tag … --prerelease`; nothing ran.

**Bootstrap refusals**, each piped as `cat install.sh | … bash`:

```
$ cat /tmp/rel4/install.sh | bash
palm-gw install: run me as root: curl -fsSL <url> | sudo bash
$ cat /tmp/rel4/install.sh | sudo PALM_GW_VERSION=1.2 bash
==> Raspberry Pi 5 Model B Rev 1.0, Debian GNU/Linux 13 (trixie)
palm-gw install: PALM_GW_VERSION must be a release tag such as v1.2.0 (got '1.2').
$ cat /tmp/relbad/install.sh | sudo PALM_GW_BASE_URL=file:///tmp/relbad bash
palm-gw install: palm-gw_0.6.0-test5_arm64.deb does not match SHA256SUMS; not installing it.
$ cat /tmp/rel5/install.sh | sudo PALM_GW_BASE_URL=file:///tmp/rel5 bash -s -- --verify-sig
palm-gw install: --verify-sig: this release is not signed (no public key in this script).
```

All exited 1 with the package left at `0.6.0~test3`. Without `PALM_GW_BASE_URL`, the baked
tag `v0.6.0-test4` gave a 404 from GitHub, as it should.

**Bootstrap over an installed gateway:** `cat /tmp/rel4/install.sh | sudo
PALM_GW_BASE_URL=file:///tmp/rel4 bash` checked the sum, ran `apt-get update` and
`apt-get install`, `Unpacking palm-gw (0.6.0~test4) over (0.6.0~test3)`, postinst re-applied
the config (every file `unchanged`), and the script ended with `palm-gw is upgraded and
running with your existing configuration.` All five units active; the T|X still allowlisted.

**`palm-gw upgrade`:**

```
$ palm-gw upgrade --check
palm-gw upgrade: no release has been published yet
$ palm-gwctl upgrade --check --version v0.6.0
palm-gw v0.6.0 is available (installed: 0.6.0~test4). Install it with: sudo palm-gw upgrade
$ palm-gw upgrade --from file:///tmp/rel5
palm-gw upgrade: run it as root: sudo palm-gw upgrade
$ sudo palm-gw upgrade --from file:///tmp/relbad
palm-gw upgrade: palm-gw_0.6.0-test5_arm64.deb: checksum mismatch (got 9a292491…, SHA256SUMS says c40d4da3…)
$ sudo palm-gw upgrade --from file:///tmp/rel4
palm-gw is up to date (installed: 0.6.0~test4, offered: 0.6.0~test4). Use --force to reinstall it.
$ sudo palm-gw upgrade --from file:///tmp/rel5
…
Unpacking palm-gw (0.6.0~test5) over (0.6.0~test4) ...
palm-gw: applying the existing configuration
…
palm-gw upgraded: 0.6.0~test4 -> 0.6.0~test5. Config and paired devices are unchanged.
$ sudo palm-gwctl upgrade --from file:///tmp/rel5
palm-gw is up to date (installed: 0.6.0~test5, offered: 0.6.0~test5). Use --force to reinstall it.
```

The `--check` against GitHub ran as `sysadmin` without sudo. No `/tmp/palm-gw-upgrade-*`
directory was left behind.

**Fresh bootstrap, unattended.** Backed up `config.toml`, `state.json`, `install-info.json`
and `tls/` to `/var/tmp/p6-backup` (mode 0700), then `apt-get purge palm-gw`: no
`/etc/palm-gw`, no `/var/lib/palm-gw`, no group, no `disable-bt`. Then:

```
$ cat /tmp/rel5/install.sh | sudo PALM_GW_BASE_URL=file:///tmp/rel5 PALM_GW_UNATTENDED=1 \
    PALM_GW_UPLINK=eth0 PALM_GW_ADAPTER=dongle PALM_GW_WEB=off PALM_GW_PAIR=no bash
==> palm-gw_0.6.0-test5_arm64.deb: checksum OK
The following NEW packages will be installed:
  palm-gw
palm-gw: sysadmin added to group palm-gw (takes effect at next login)
palm-gw is installed. Run 'sudo palm-gw setup' to finish.
==> Running palm-gw setup (unattended)
board:     pi5 (Raspberry Pi 5 Model B Rev 1.0)
adapters:  hci0 60:15:6F:E9:EB:03  USB dongle (usb)  powered=yes
uplinks:   eth0 via 10.4.0.1 metric 100
           wlan0 via 10.4.0.1 metric 600  Wi-Fi 5240 MHz
…
    /etc/palm-gw/config.toml written
…
    dtoverlay=disable-bt added to /boot/firmware/config.txt
…
palm-gw is running.
Reboot required: YES (the Bluetooth adapter mode changed)
```

No prompts. The reboot notice is right on paper, since the purge had taken the `disable-bt` line
out and setup put it back, but the Pi had not rebooted in between, so the radio never came
back. The web UI was left off so that no password was set in this run.

**Restored:** the backups put back and `palm-gw setup --yes --adapter dongle --pair no` run
(every file `unchanged`, `web password: unchanged`, `Reboot required: no`). `sha256sum -c`
passed for `config.toml`, `state.json`, `tls/cert.pem` and `tls/key.pem`; the T|X is
allowlisted and the units are active. The backup was shredded and the test directories
deleted. The Pi runs `0.6.0~test5`.

**Not tested:** the interactive wizard through `/dev/tty` under `curl | sudo bash` (Zoe at a
terminal; part 7's fresh-flash run covers it), downloads from a real GitHub release (none
published), and the non-arm64 and non-Pi refusals (no such host here).

### Part 6: `make image` (D-002, D-045), 2026-11-11

`make image DEB_VERSION=0.6.0~test6` on the Mac (Docker 29.8.2, arm64 VM, no emulation).

**Base image:** downloaded once into `dist/base/` and checked against the pinned hash:

```
$ echo '483db18a…422843  dist/base/2026-10-06-raspios-trixie-arm64-lite.img.xz.part' | sha256sum -c -
dist/base/2026-10-06-raspios-trixie-arm64-lite.img.xz.part: OK
==> Base image: b8a393dc1c3701450b5fffbfe74368a98fb6eab7415ce456cc0cead886ea8780, 3078619136 bytes
```

The unpacked hash and size match the `extract_sha256` and `extract_size` in Raspberry Pi's own
Imager list (`os_list_imagingutility_v4.json`).

**The build:**

```
==> Base: Debian GNU/Linux 13 (trixie)
==> Installing palm-gw_0.6.0~test6_arm64.deb with apt
0 upgraded, 1 newly installed, 0 to remove and 0 not upgraded.
palm-gw is installed. Run 'sudo palm-gw setup' to finish.
==> Cleaning up the chroot
/dev/loop0      2.3G  1.9G  268M  88% /work/palm-gw-0.6.0-test6-rpi-arm64.mnt
==> Compressing to palm-gw-0.6.0-test6-rpi-arm64.img.xz
==> Done: dist/palm-gw-0.6.0-test6-rpi-arm64.img.xz (548733632 bytes; 3078619136 unpacked)
```

apt installed palm-gw alone: the Lite image already has every dependency. The root
filesystem keeps 268 MB free before the first boot grows it.

**What changed against the stock image**, from `scripts/image-diff.sh` (every file on both
partitions, with mode, owner and hash; `<` is the stock image, `>` the palm-gw image; the
package's own files under `/usr` and `/var/lib/dpkg/info` are left out here):

```
=== partition 1: < base, > image
=== partition 2: < base, > image
< ./etc/group …            > ./etc/group …   > ./etc/group- …  (the palm-gw group)
< ./etc/gshadow …          > ./etc/gshadow … > ./etc/gshadow- …
> ./etc/palm-gw-image f 644 root:root
> ./etc/ppp/options.palm f 644 root:root
> ./etc/profile.d/palm-gw-setup.sh f 644 root:root
< ./var/cache/apt/pkgcache.bin …  > ./var/cache/apt/pkgcache.bin …
< ./var/lib/dpkg/status …  > ./var/lib/dpkg/status …  > ./var/lib/dpkg/status-old …
> ./var/lib/palm-gw d 750 root:root
  (and apt's and dpkg's logs, empty in the stock image, now record the install)
=== checks
palm-gw 0.6.0~test6 install ok installed
palm-gw:x:985:
palm-gw units enabled: none
resolv.conf: as in the base
boot partition has user-data
boot partition has network-config
boot partition has meta-data
loops still attached: 0
```

The boot partition (firmware, `config.txt`, `cmdline.txt` and cloud-init's `user-data`,
`network-config` and `meta-data`) is byte-identical to the stock image, and so are the apt
package lists.

Fixed on the way to that result:
- The first build deleted the package lists and apt's caches, which the stock image ships.
  It now installs from the stock lists, and only on a missing dependency refreshes them and
  then puts the stock ones back. That fallback ran once, when `apt-get install --no-download`
  rejected the local path ("Pathname to install is not absolute"), and left the lists
  identical to stock; `--no-download` is gone.
- The first build grew the root filesystem by 512 MB; it no longer does (`GROW_MB=0`).
- `xz -6` gave 615 MB; `xz -9` gives 549 MB.
- Loop devices left attached by an earlier comparison run inside Docker's VM made a fresh
  container's next free device (`loop8`) have no node. Both scripts now create a missing node,
  and the leaked devices were detached.

**Imager entry** (`dist/palm-gw-0.6.0-test6-rpi-arm64.json`): one OS with `"init_format":
"cloudinit-rpi"`, `"devices": ["pi5-64bit", "pi4-64bit"]`, the sizes and both hashes, and the
Pi 4 and Pi 5 device objects copied from Raspberry Pi's list.

**Not tested yet:** booting the image. That needs a spare card and Zoe, and is part 7: flash
it with Raspberry Pi Imager (customisation through `--repo` with the `.json`, or with "Use
custom"), boot, log in, see the reminder, run `sudo palm-gw setup`.

### Part 7: fresh flash of the image (D-045, D-046), 2026-11-11, first run

Hardware: a second Raspberry Pi 5 (onboard radio 2C:CF:67:2E:E7:4A) on a new 64 GB microSD
card, with a second TP-Link UB500 dongle (60:15:6F:EA:00:81), Ethernet uplink. The working
gateway (10.4.0.241) stayed up throughout.

**Flashing** with Raspberry Pi Imager 2.0.12 on the Mac. The first attempt used "Use custom"
and Imager skipped customisation altogether. Imager's source (`OSSelectionStep.qml`) sets a
custom file with no `init_format`, so customisation is never offered for one. The second
attempt opened Imager with a copy of the entry whose `url` is the local file:

```
$ "/Applications/Raspberry Pi Imager.app/Contents/MacOS/rpi-imager" --repo dist/palm-gw-0.6.0-test6-local.json
setSrc: initFormat parameter: "cloudinit-rpi" -> _initFormat set to: "cloudinit-rpi"
configureWriteThread: passing to thread - initFormat: "cloudinit-rpi" cloudinit empty: false …
```

Hostname `palmgw-test`, user `sysadmin` with Zoe's password and SSH key. Imager writes
`sudo: null` (as for stock Lite: neither entry offers passwordless sudo), so sudo asked for the
password until Zoe changed that by hand.

**First boot and login** (Zoe):

```
$ ssh sysadmin@10.4.1.118
Linux palmgw-test 6.18.50+rpt-rpi-2712 #1 SMP PREEMPT Debian 1:6.18.50-1+rpt1 (2026-09-11) aarch64
…
  palm-gw is installed but not set up yet. Run:  sudo palm-gw setup
```

`cloud-init status`: done. `/etc/palm-gw-image`: `PALM_GW_VERSION=0.6.0~test6`, base
`2026-10-06-raspios-trixie-arm64-lite.img.xz`.

**`sudo palm-gw setup`** (Zoe, interactive): detection listed both radios (dongle
`powered=no`), Ethernet, and two warnings (under-voltage since boot, a 3000 mA supply). She
chose the dongle with the onboard radio off, the web UI with HTTPS, and pairing. A mistyped
password confirmation was caught and asked again. The apply step wrote everything, then:

```
palm-gw setup: systemctl restart palm-gwd: exit status 1: Job for palm-gwd.service failed because a timeout was exceeded.
```

```
$ journalctl -u palm-gwd -o cat | grep -m1 'attach to BlueZ failed'
{"level":"ERROR","msg":"attach to BlueZ failed; retrying in 3 s","component":"daemon","err":"bluez: set hci1.Powered: Failed"}
$ for r in /sys/class/rfkill/rfkill*; do echo "$(cat $r/name) $(cat $r/type) soft=$(cat $r/soft)"; done
hci0 bluetooth soft=0
hci1 bluetooth soft=1
phy0 wlan soft=0
$ ls -l --time-style=full-iso /var/lib/systemd/rfkill/ | grep usb
-rw-r--r-- 1 root root 2 2026-10-05 17:27:44.862034643 -0700 platform-xhci-hcd.1-usb-0:1:1.0:bluetooth
$ sudo cat /var/lib/systemd/rfkill/platform-xhci-hcd.1-usb-0:1:1.0:bluetooth
1
```

The file's date is the image's build time because the clock was not yet set on first boot:
the stock image has no state for that port at all. Pi OS starts every new radio blocked
(`/etc/modprobe.d/rfkill_default.conf`: `options rfkill default_state=0`) and only ships
saved unblocked states for the onboard radios (D-046). Re-binding the dongle with its saved
state removed confirmed it:

```
$ sudo systemctl stop palm-gwd; sudo rm /var/lib/systemd/rfkill/platform-xhci-hcd.1-usb-0:1:1.0:bluetooth
$ echo 3-1:1.0 | sudo tee /sys/bus/usb/drivers/btusb/unbind; echo 3-1:1.0 | sudo tee /sys/bus/usb/drivers/btusb/bind
hci0 bluetooth soft=1
$ cat /sys/module/rfkill/parameters/default_state
0
$ sudo systemctl start palm-gwd
{"level":"WARN","msg":"adapter was rfkill soft-blocked; unblocked it","component":"daemon","event":"rfkill_unblocked",…}
```

Clearing it by hand
(`echo 0 > /sys/class/rfkill/rfkill1/soft`) let `palm-gwd` attach at its next retry.

**The fix, on the Pi** (`0.6.0~test8`, installed with `apt-get install`): the dongle blocked
again by hand, then `systemctl restart palm-gwd`:

```
restart rc=0
{"level":"WARN","msg":"adapter was rfkill soft-blocked; unblocked it","component":"daemon","event":"rfkill_unblocked","hci":"hci1"}
{"level":"INFO","msg":"adapter configured","component":"daemon","hci":"hci1","address":"60:15:6F:EA:00:81","alias":"Palm Gateway - Lyman",…}
{"level":"INFO","msg":"palm-gwd ready","ctl_socket":"/run/palm-gw/ctl.sock"}
$ sudo cat /var/lib/systemd/rfkill/platform-xhci-hcd.1-usb-0:1:1.0:bluetooth
0
```

(`0.6.0~test7` had no wait after the unblock; its first `Powered` failed and the 3 s retry
worked.) After a reboot for `dtoverlay=disable-bt`, the dongle is hci0, unblocked, and
`palm-gwctl status` shows it configured by address, PAN up on `br-palm`.

**Pairing** (Zoe, the T|X):

```
$ ssh sysadmin@10.4.1.118 palm-gwctl pair 5 --one
Pairing window open for 5m0s.
…
  00:07:E0:3C:5E:FA  "Zoë's Palm TX"  PIN requested
  00:07:E0:3C:5E:FA  "Zoë's Palm TX"  paired, added to allowlist
Window closed (one device).
```

**Also found:** after the reboot Wi-Fi was blocked again (`phy0 wlan soft=1`, `nmcli radio`:
WIFI disabled). NetworkManager logged "Wi-Fi enabled by radio killswitch; disabled by state
file": the stock `NetworkManager.state` has `WirelessEnabled=false`, and Imager's first boot
only unblocks rfkill (D-047). And sudo asked for a password: Imager's `sudo: null` (D-047).

Next: the same run from a fixed image (`0.6.0-test10`: the D-046 daemon, Wi-Fi kept enabled,
Imager's passwordless sudo box), so that `sudo palm-gw setup` goes through to pairing with
no hand fixes; then the `curl | sudo bash` installer with the interactive wizard on
`/dev/tty`.

### Part 7: fresh flash of the image, 2026-11-11, second run (test10)

The test10 image (`extract_sha256` 93dcd0c3…fdbe) was written with Imager opened on the
release's `.json` (`--repo palm-gw-0.6.0-test10-local.json`). Imager offered its customisation,
including the new "passwordless sudo" box (D-047), which was ticked. Its log shows the
cloud-init it wrote: `sudo: ALL=(ALL) NOPASSWD:ALL`, hostname `palmgw-test`, Wi-Fi country only
(no network). The written card verified against the image. Same Pi 5, same UB500 dongle in the
same port. Zoe ran `sudo palm-gw setup` (dongle mode, alias "Palm Gateway - Lyman") and
rebooted. After the reboot, before pairing:

```
$ cat /etc/palm-gw-image; dpkg-query -W palm-gw
PALM_GW_VERSION=0.6.0~test10
BASE_IMAGE=2026-10-06-raspios-trixie-arm64-lite.img.xz
BUILT=2026-11-12T00:45:00Z
palm-gw	0.6.0~test10
$ sudo -n true && echo "passwordless sudo OK"
passwordless sudo OK
$ for d in /sys/class/rfkill/*; do echo "$(basename $d) $(cat $d/name) $(cat $d/type) soft=$(cat $d/soft) hard=$(cat $d/hard)"; done
rfkill0 hci0 bluetooth soft=0 hard=0
rfkill1 phy0 wlan soft=0 hard=0
$ nmcli radio
WIFI-HW  WIFI     WWAN-HW  WWAN
enabled  enabled  missing  enabled
$ sudo grep WirelessEnabled /var/lib/NetworkManager/NetworkManager.state
WirelessEnabled=true
$ for f in /var/lib/systemd/rfkill/*; do echo "$f: $(sudo cat $f)"; done
/var/lib/systemd/rfkill/platform-1001100000.mmc:wlan: 0
/var/lib/systemd/rfkill/platform-107d50c000.serial:bluetooth: 0
…  (the stock image's serial and soc entries, all 0, dated Oct  5)
/var/lib/systemd/rfkill/platform-xhci-hcd.1-usb-0:1:1.0:bluetooth: 0
$ sudo palm-gwctl status
palm-gwd c454e89, up 2m11s (phase 6)
bluez:     ok, agent registered since 20:08:44
adapter:   hci0 60:15:6F:EA:00:81 (usb)  alias "Palm Gateway - Lyman"  class 0x420300  powered=yes discoverable=yes pairable=no  (configured by address)
pairing:   closed (palm-gwctl pair to open)
allowlist: 0 entries
…
```

- **Wi-Fi survives the reboot (D-047):** not soft-blocked, NetworkManager's switch on.
- **sudo needs no password (D-047).**
- **The dongle stays unblocked after the reboot (D-046):** palm-gwd configured it at boot with
  no error and no unblock needed. The stock image has no saved state for the USB port; the
  `xhci-hcd.1-usb-0:1:1.0:bluetooth` entry (0, unblocked) was saved after palm-gwd cleared the
  block on the first boot. The first boot's `rfkill_unblocked` line itself is gone: Pi OS keeps
  the journal in memory (`Storage=volatile`), so only the current boot's log exists.
- The Pi still reports under-voltage since boot (`throttled=0x50000`), not at the time of
  reading: the supply, as in the first run.

**Found:** `palm-gwctl pair` without sudo failed:

```
$ ssh sysadmin@10.4.1.118 palm-gwctl pair 5 --one
palm-gwctl: permission denied on /run/palm-gw/ctl.sock: add your user to the palm-gw group or use sudo
$ id; getent group palm-gw
uid=1000(sysadmin) gid=1000(sysadmin) groups=1000(sysadmin),4(adm),…,27(sudo),…
palm-gw:x:985:
```

In the image the package is installed before the user exists, so the postinst's `SUDO_USER`
step adds nobody. `palm-gw setup` now adds the sudo user to the group (D-048).

Pairing, with sudo on this card:

```
$ ssh sysadmin@10.4.1.118 sudo palm-gwctl pair 5 --one
…
$ sudo journalctl -u palm-gwd -b | grep -E "pair"     (trimmed)
21:17:33 "pairing window opened" minutes=5 one=true
21:17:47 "device paired" addr=00:07:E0:3C:5E:FA name="Zoë's Palm TX" trusted=true
21:17:47 "pairing window closed" reason=one_device
$ sudo palm-gwctl status
…
allowlist: 1 entries
  00:07:E0:3C:5E:FA  dynamic  Zoë's Palm TX
```

The D-048 fix, as `0.6.0~test11` on the same card. Installing with `sudo apt-get install` adds
`sysadmin` through the postinst, so the user was taken out of the group again to test setup's
step:

```
$ sudo apt-get install -y /var/tmp/palm-gw_0.6.0~test11_arm64.deb
…
$ getent group palm-gw
palm-gw:x:985:sysadmin
$ sudo gpasswd -d sysadmin palm-gw
Removing user sysadmin from group palm-gw
$ getent group palm-gw
palm-gw:x:985:
$ sudo palm-gw setup --apply
==> palm-gw group
    sysadmin added to palm-gw (takes effect at the next login)
$ getent group palm-gw
palm-gw:x:985:sysadmin
$ sudo palm-gw setup --apply
==> palm-gw group
    sysadmin already in palm-gw
```

A new ssh login, no sudo:

```
$ id -nG
sysadmin adm dialout cdrom sudo audio video plugdev games users netdev palm-gw gpio i2c spi render input lpadmin
$ palm-gwctl status
palm-gwd c454e89-dirty, up 2s (phase 6)
…
allowlist: 1 entries
  00:07:E0:3C:5E:FA  dynamic  Zoë's Palm TX
```

The image path with no hand fixes now runs from Imager to pairing, once the next image carries
D-048. Next: the `curl | sudo bash` installer with the interactive wizard on `/dev/tty`.

### Part 7: the one-liner on a freshly flashed stock card, 2026-11-11 (Phase 6 accept)

Stock Raspberry Pi OS Lite (64-bit) 2026-10-06, the same file as the image's base (download
sha256 483db18a…2843), written by Imager from a one-entry local list
(`dist/pios-lite-2026-10-06-local.json`, `file://` URL to `dist/base/`), because Zoe could not
find the entry in Imager's own list. Imager customisation: hostname `palmgw-test`, user
`sysadmin` with Zoe's key, passwordless sudo (ticked), Wi-Fi country only. Same Pi 5 and UB500
dongle. Before the install: no palm-gw, the dongle `hci1` soft-blocked (stock `default_state=0`),
and a checksum list of every file in `/etc` and `/boot/firmware` saved to
`/var/tmp/etc-before.sha256` (1171 files).

The release: `make deb DEB_VERSION=0.6.0~test12` from 51a60b2, laid out as `make release` does
(`palm-gw_0.6.0-test12_arm64.deb`, `install.sh` with `v0.6.0-test12` baked in, `SHA256SUMS`),
copied to `/var/tmp/rel12` on the Pi. Zoe ran the one-liner from her own terminal:

```
$ ssh -t sysadmin@10.4.1.118 'curl -fsSL file:///var/tmp/rel12/install.sh | sudo PALM_GW_BASE_URL=file:///var/tmp/rel12 bash'
==> Raspberry Pi 5 Model B Rev 1.0, Debian GNU/Linux 13 (trixie)
==> Downloading the release from file:///var/tmp/rel12/
==> palm-gw_0.6.0-test12_arm64.deb: checksum OK
==> Installing palm-gw_0.6.0-test12_arm64.deb and its dependencies (apt-get)
…
Setting up palm-gw (0.6.0~test12) ...
palm-gw: sysadmin added to group palm-gw (takes effect at next login)
palm-gw is installed. Run 'sudo palm-gw setup' to finish.
==> Starting the setup wizard
palm-gw setup: looking at this Pi
board:     pi5 (Raspberry Pi 5 Model B Rev 1.0)
adapters:  hci0 2C:CF:67:2E:E7:4A  onboard (serial)  powered=yes
           hci1 60:15:6F:EA:00:81  USB dongle (usb)  powered=no
uplinks:   eth0 via 10.4.0.1 metric 100
power:     supply 3000 mA, get_throttled=0x50000
config:    none yet (/etc/palm-gw/config.toml)
subnet:    10.42.0.0/24 free
warnings:
  - the Pi has been under-voltage since boot (get_throttled=0x50000)
  - Pi 5 supply negotiated 3000 mA (under 5 A), so USB ports share 600 mA; …
Gateway name, as the Palm will see it: Palm Gateway - Lyman
Which Bluetooth adapter should the gateway use? USB dongle (hci1 60:15:6F:EA:00:81), turn the onboard radio off (reboot needed)
  uplink: eth0 (the only interface with a default route)
Enable the web UI? Yes
Web UI username: sysadmin
Web UI password: ********
Web UI password again: ********
Use HTTPS with a self-signed certificate? Yes
Pair a Palm now? Yes
==> Configuration
…
==> Bluetooth adapter mode
    dtoverlay=disable-bt added to /boot/firmware/config.txt
    adapter = "60:15:6F:EA:00:81" is the USB dongle
==> palm-gw group
    sysadmin already in palm-gw
==> Units
…
==> Web UI certificate
    /var/lib/palm-gw/tls/cert.pem created

The adapter mode changed, so pair after the reboot: sudo reboot, then palm-gwctl pair

palm-gw is running.
  Web UI:    https://10.4.1.118:8042   (user: sysadmin; the web UI itself arrives in Phase 7)
  Cert:      SHA-256 CD:9F:82:E9:…:43:7D
…
Reboot required: YES (the Bluetooth adapter mode changed)
```

- The wizard ran on `/dev/tty` under `curl | sudo bash`, detected the board and the uplink
  without asking, and asked only for the name, the adapter, the web UI and pairing.
- The postinst added `sysadmin` (the `SUDO_USER`) to `palm-gw`; setup's D-048 step found it
  already there.
- **Pairing during setup:** with the dongle chosen, the onboard radio must be switched off by a
  reboot first, so the wizard defers pairing to after the reboot (Phase 6 part 3's design). This
  is the one way this run differs from the accept text ("pairs a Palm during setup, and the Palm
  has internet before the wizard exits"); on the onboard adapter no reboot is needed and the
  wizard pairs in place (part 3).

After `sudo reboot`, Zoe ran `sudo palm-gwctl pair 5 --one`. palm-gwd's log (trimmed):

```
21:56:04 pairing window opened  minutes=5 one=true
21:56:30 device connected / 21:56:45 PIN requested / 21:56:46 device disconnected
21:57:13 PIN requested / device paired  00:07:E0:3C:5E:FA "Zoë's Palm TX" trusted=true
21:57:13 pairing window closed  reason=one_device
21:57:46 RFCOMM connection  profile=lap channel=2 ; session started ; PPP link up  peer=10.42.0.10
21:58:25 IPCP terminated by peer ; Sent 0 bytes, received 4 bytes. ; session ended  duration=40s
```

The dongle came up unblocked (D-046) and powered as `hci0`. The first Connect after pairing
failed on the T|X with "Error: Serial: timeout. Could be bad cable or faulty Modem. (0x0305)":
PPP and IPCP came up on the Pi, the Pi sent no IP data, and the T|X closed the link after 40 s.
The bridge, dnsmasq (10.42.0.1:53 and :67), nftables and the PPP device were all up. A btmon
capture was started (`/var/tmp/retry.btsnoop` on the Pi) before a second tap, which worked:

```
22:14:24 RFCOMM connection  profile=lap channel=2 ; PPP link up  peer=10.42.0.10
22:25:14 Sent 580020 bytes, received 4637272 bytes. ; session ended  duration=10m50s
22:36:07 session started ; PPP link up  peer=10.42.0.10
```

Blazer on the T|X loaded a page (Zoe, 22:36). **The first failure is not explained.** It
happened once, on the first Connect after pairing, before any capture was running; the second
and third Connects worked. If it recurs, the btmon trace of a failing attempt is the next step.
Recorded as an open observation, not a fix.

**Purge against the stock snapshot (Phase 6 accept).** `sudo apt-get purge -y palm-gw` on
test12 stopped the units, removed `br-palm`, the main.conf block, the firewall table, the
rendered files, `disable-bt`, `/etc/palm-gw`, `/var/lib/palm-gw` and the group. A new checksum
list of `/etc` and `/boot/firmware` against `/var/tmp/etc-before.sha256`:

```
$ diff /var/tmp/etc-before.sha256 /var/tmp/etc-after.sha256
< 90a44682…  /boot/firmware/config.txt       > e62f4b13…  /boot/firmware/config.txt
< d2e46…  /etc/group-                          > de559…  /etc/group-
< 985c7…  /etc/gshadow-                        > 5b2e5…  /etc/gshadow-
< 60dac93f…  /etc/nftables.conf               > 6f6ffc04…  /etc/nftables.conf
$ for f in /boot/firmware/config.txt /etc/nftables.conf; do head -c -1 $f | sha256sum; done
90a446821551a9df…
60dac93ffe0ea440…
```

**Found:** both files kept one extra trailing newline, the blank line setup puts before its own
lines. Fixed in 601c611 (`removeLines` drops that separator; `TestRemoveSystem` now checks both
files byte for byte). `group-` and `gshadow-` are shadow-utils' backups, rewritten when
`groupdel` removes `palm-gw`; they are not files palm-gw owns. The two files were put back to
their stock bytes by hand, and `0.6.0~test13` (601c611) was released to `/var/tmp/rel13`.

**Unattended one-liner, test13,** from a shell with no terminal (ssh without `-t`):

```
$ curl -fsSL file:///var/tmp/rel13/install.sh | sudo PALM_GW_BASE_URL=file:///var/tmp/rel13 \
    PALM_GW_UNATTENDED=1 PALM_GW_NAME="Palm Gateway - Lyman" PALM_GW_ADAPTER=dongle \
    PALM_GW_WEB=off PALM_GW_PAIR=no bash
…
Setting up palm-gw (0.6.0~test13) ...
palm-gw: sysadmin added to group palm-gw (takes effect at next login)
==> Running palm-gw setup (unattended)
board:     pi5 (Raspberry Pi 5 Model B Rev 1.0)
adapters:  hci0 60:15:6F:EA:00:81  USB dongle (usb)  powered=yes
uplinks:   eth0 via 10.4.0.1 metric 100
…
    bluetooth.name = "Palm Gateway - Lyman"
    bluetooth.adapter = "60:15:6F:EA:00:81"
    web.enabled = false
…
    dtoverlay=disable-bt added to /boot/firmware/config.txt
…
palm-gw is running.
Reboot required: YES (the Bluetooth adapter mode changed)
exit 0
```

No prompts. (A first attempt with `bash < /dev/null` ran nothing and exited 0: the redirect
replaced the piped script. Not a palm-gw fault.) Then the purge again:

```
$ sudo apt-get purge -y palm-gw
…
$ diff /var/tmp/etc-before.sha256 /var/tmp/etc-after2.sha256
< …  /etc/group-      > …  /etc/group-
< …  /etc/gshadow-    > …  /etc/gshadow-
```

`/etc` and `/boot/firmware` match the stock card apart from the shadow-utils backups.
Reinstalled unattended (same answers) and rebooted for dongle mode. The Pi did not come back
on 10.4.1.118 within several minutes of the reboot (no ping, no ssh); to be looked at.

### Part 7: fresh flash of the image, 2026-11-11, third run (test14)

`make image DEB_VERSION=0.6.0~test14` from 73a44f8 (D-046, D-047, D-048 and the remove fix):
548,623,824 bytes, `extract_sha256` 90c52d61…0c43f. `scripts/image-diff.sh` against the
stock base: boot partition identical; on the root partition only the package's files,
`/etc/group` and `/etc/gshadow` (the `palm-gw` group, no members) with their `-` backups,
dpkg/apt bookkeeping, and `NetworkManager.state` (`WirelessEnabled=true`); no palm-gw unit
enabled. Imager (`--repo dist/palm-gw-0.6.0-test14-local.json`) wrote and verified the card
with passwordless sudo ticked.

First boot, before setup: `/etc/palm-gw-image` says test14, `sudo -n true` works, Wi-Fi
enabled, the dongle's radio soft-blocked (stock), `sysadmin` not in `palm-gw`. Zoe ran
`sudo palm-gw setup` (dongle, web UI on, pairing Yes), rebooted as the wizard asked, and paired
with `palm-gwctl pair 5 --one`, without sudo. After the reboot:

```
$ dpkg-query -W palm-gw; id -nG | tr " " "\n" | grep -x palm-gw
palm-gw	0.6.0~test14
palm-gw
$ for d in /sys/class/rfkill/*; do …; done
rfkill0 hci0 bluetooth soft=0
rfkill1 phy0 wlan soft=0
$ nmcli radio wifi; sudo grep WirelessEnabled /var/lib/NetworkManager/NetworkManager.state
enabled
WirelessEnabled=true
$ palm-gwctl status          (no sudo)
palm-gwd 73a44f8, up 2m38s (phase 6)
adapter:   hci0 60:15:6F:EA:00:81 (usb)  alias "Palm Gateway - Lyman"  class 0x420300  powered=yes discoverable=yes pairable=no  (configured by address)
allowlist: 1 entries
  00:07:E0:3C:5E:FA  dynamic  Zoë's Palm TX
bluez devices: 1
  00:07:E0:3C:5E:FA  "Zoë's Palm TX"      class 0x100114  paired,trusted,connected
$ sudo journalctl -u palm-gwd -b | grep pair      (trimmed)
23:34:21 pairing window opened  minutes=5 one=true
23:35:13 device paired  00:07:E0:3C:5E:FA "Zoë's Palm TX" trusted=true
23:35:13 pairing window closed  reason=one_device
$ sudo grep palm-gw /etc/group
palm-gw:x:985:sysadmin
```

The image now goes from Imager to a paired Palm with no hand fixes: the dongle unblocked
(D-046), Wi-Fi and passwordless sudo kept (D-047), `palm-gwctl` without sudo (D-048). This
boot's journal holds no `sudo palm-gwctl`, so the pairing ran as `sysadmin`.

## Phase 7: Web UI (in progress)

Built in parts: (1) listeners and logins; (2) dashboard, pairing and devices; (3) firewall
and settings, `palm-gw web set-password`; (4) logs, diagnostics, about; (5) `/api/v1/`;
(6) acceptance on the Pi; (7) the Palm app, after Zoe's Palm development how-to (D-049).

### Part 1: listeners, logins, the port-80 page, the 8042 rule (D-051), 2026-11-11

`go test ./internal/web/` (httptest, a fake control backend, a fake clock):
- every page needs a login on both sides (`GET /` → 303 to `/login`, `POST` → 401);
- a wrong username and a wrong password give the same 401 page; the right ones give a cookie
  that is HttpOnly, SameSite=Strict, and Secure only on TLS listeners;
- logout needs the CSRF token (403 without it);
- a LAN session is not valid on the Bluetooth side; sessions expire after `session_hours`;
- a viewer logs in, the page says read-only, and a POST other than logout gets 403;
- five failures per minute per side and source, then 429 with `Retry-After` even for the right
  password; the other side is unaffected; after a minute it works again;
- `allow_cidrs` checks the TCP peer, and `X-Forwarded-For`/`X-Real-IP` do not get round it;
- with no password set, the login page says to run `palm-gw web set-password`;
- the port-80 page shows the client's IP, the gateway and the admin link, and serves nothing
  else;
- the Bluetooth-side listener: plain HTTP gets **zero bytes**, TLS 1.2 is refused, TLS 1.3
  serves the login page;
- `wanted()`: LAN listeners on `lo`, `eth0`, `wlan0` and not on `br-palm`, `ppp*`, `bnep*`; the
  gateway's 8042 (TLS) and 80 once the bridge holds the address; no Bluetooth side with TLS off.

`go test ./internal/fw/`: the rendered table accepts 8042 on the gateway only when the
Bluetooth side is on (not with it off, TLS off or the web UI off), always drops 8042 from the
Palm side otherwise, limits port 80 to the gateway, and follows a non-default listen port.
`go test ./internal/ctl/`: `Call` runs a handler in-process. `go test ./internal/setup/`: the
wizard with TLS off writes `web.bluetooth_side = false`.

On the test Pi, `0.6.0~test15` installed over test14 (`nft -c` accepted the new table):

```
web listener up  addr=127.0.0.1:8042   side=lan        tls=true
web listener up  addr=10.4.1.118:8042  side=lan        tls=true
web listener up  addr=10.42.0.1:80     side=connected  tls=false
web listener up  addr=10.42.0.1:8042   side=bluetooth  tls=true
```

From the Mac (LAN):

```
$ curl -sk -o /dev/null -w "%{http_code}\n" https://10.4.1.118:8042/login
200
$ curl -sk https://10.4.1.118:8042/login | grep -oE "SHA-256:<br><code>[0-9A-F:]{20}"
SHA-256:<br><code>D6:82:63:AA:E0:1A:0B
$ echo | openssl s_client -connect 10.4.1.118:8042 | openssl x509 -noout -fingerprint -sha256
sha256 Fingerprint=D6:82:63:AA:E0:1A:0B:…
$ curl -sk -o /dev/null -w "%{http_code} -> %{redirect_url}\n" https://10.4.1.118:8042/
303 -> https://10.4.1.118:8042/login
$ curl -s -m 5 http://10.4.1.118:8042/      # plain HTTP on the TLS port
(exit 52: empty reply)
$ curl -s -m 5 http://10.4.1.118/           # port 80 on the LAN address
(exit 7: connection refused)
```

On the Pi (the gateway address):

```
$ curl -sk --tlsv1.3 -o /dev/null -w "%{http_code}\n" https://10.42.0.1:8042/login
200
$ curl -sk --tls-max 1.2 https://10.42.0.1:8042/login
(exit 35: TLS handshake refused)
$ curl -s -m 5 http://10.42.0.1:8042/
(exit 52: empty reply)
$ curl -s http://10.42.0.1/ | grep -E "<h1>|<code>|href"
  <h1>You are connected</h1>
    <dt>Your IP</dt><dd><code>10.42.0.1</code></dd>
    <dt>Gateway</dt><dd><code>10.42.0.1</code></dd>
  <p>Manage the gateway: <a href="https://10.42.0.1:8042/">https://10.42.0.1:8042/</a></p>
$ sudo nft list chain inet palm_gw palm_input | grep -E "dport (80|8042)"
    ip daddr 10.42.0.1 tcp dport 80 accept comment "the you-are-connected page (D-039)"
    ip daddr 10.42.0.1 tcp dport 8042 accept comment "web UI and API, TLS only (D-026, D-039)"
    tcp dport 8042 counter packets 0 bytes 0 drop comment "palm->webui-lan"
```

Logging in with Zoe's password is hers to do from a LAN browser (no password was set or typed
here). The Palm-side checks (fake-palm, a PAN laptop, `nmap`) are part 6.

### Part 2: dashboard, pairing, devices, the event feed (D-052), 2026-11-12

`go test -race ./internal/web/ ./internal/event/ ./internal/ctl/`, against a fake daemon with
a paired T|X in a LAP session, a static allowlist entry and a BlueZ-only device:
- the dashboard shows the adapter, the session (ppp0, 10.42.0.10, `566.4 KB` to the Palm),
  the last events with no PIN, and refreshes `/partials/dashboard` (the body alone);
- the pairing page opens a window (`minutes=2`, `one`), which **stays open after the request
  ends**, shows the PIN as `4 8 2 1` with a countdown, refuses a second window with a message,
  and closes on "Close the window" (`pair.close`); out-of-range minutes fall back to the default;
- a viewer sees that a window is open but not the PIN, gets no admin buttons, and every POST
  (pair, close, forget, allow) is 403 without reaching the daemon;
- devices: rows for "paired here", "config" and "BlueZ only"; forget asks on a page first and
  acts only on the POST; disconnect, rename (trimmed) and add-by-address reach the daemon; a
  daemon error is shown on the page; a malformed address is refused before the daemon and 404s
  in a path; mutations without the CSRF token are 403;
- `/events` is `text/event-stream` with `id:` lines and no PIN;
- the event bus numbers events and keeps the last 200 (`Recent(since, max)`); `ctl.Call` runs
  handlers in-process.

On the test Pi, `0.6.0~test16`:

```
$ palm-gwctl events 5
23:57:32  bluez_found
23:57:32  firewall_applied      why=start
23:57:33  pan_registered        bridge=br-palm
$ palm-gwctl rename 00:07:E0:3C:5E:FA "Zoe T|X"
00:07:E0:3C:5E:FA: label "Zoe T|X"
$ palm-gwctl rename 00:07:E0:3C:5E:FA
00:07:E0:3C:5E:FA: label cleared
$ palm-gwctl unpair
No pairing window was open.
```

From the Mac: `/static/htmx.min.js` (52,182 bytes), `app.js` and `style.css` are served;
`/pair` and `/events` without a session redirect to `/login`.

**From a LAN browser (Zoe, Mac at 10.4.0.54), test16.** Forget the T|X on Devices, delete the
gateway on the T|X, open a window on Pair, pair with the PIN shown, then Connect. palm-gwd's
log (trimmed):

```
00:09:21 web_login       user=sysadmin side=lan ip=10.4.0.54
00:09:30 forgotten       00:07:E0:3C:5E:FA
00:09:30 web_forget      00:07:E0:3C:5E:FA user=sysadmin side=lan
00:10:03 window_open     minutes=5 one=true
00:10:03 web_pair_open   user=sysadmin side=lan minutes=5 one=true
00:10:18 pin_requested   00:07:E0:3C:5E:FA "Zoë's Palm TX"
00:10:18 paired          00:07:E0:3C:5E:FA "Zoë's Palm TX"
00:10:18 window_closed   reason=one_device
00:10:32 session_start   lap peer=10.42.0.10
00:10:32 session_up      lap peer=10.42.0.10
```

The session was still up at 59 s (`palm-gwctl status`). Zoe: "Re-paired, works". Disconnect
from the Devices page was not exercised in this run.

**D-050's first-connect drop, again:** on the test14 card, before the web UI was installed, the
first Connect after `palm-gwctl pair` ended after 39 s (23:35:46 to 23:36:25, `pppd_exit`) and
the next after 15 s (23:36:34 to 23:36:49); later Connects held. The first Connect after the
web pairing above held. Still open; a btmon trace of a failing attempt is the next step.

### Part 3: firewall and settings pages, config.set, `palm-gw web set-password` (D-053), 2026-11-12

`go test -race ./internal/web/ ./internal/daemon/ ./internal/setup/`:
- firewall page: lists rules and the drop counters; add (ports split on spaces and commas,
  lower-cased) sets the whole list; bad ports never reach the daemon; edit rule 2, delete rule
  1, an out-of-range rule number is 400; test reachability shows "answers from the Pi (3 ms)" /
  "does not answer … connection refused" and whether a rule covers it;
- settings: only changed keys are set; a live key gives no restart prompt; `bluetooth.adapter`
  gives "Waiting for a restart" and the Restart button; the daemon's validation message is
  shown; a non-numeric idle timeout is refused; Restart sends the browser to `/login` after 10 s;
- password change: wrong current password 401, short 400, mismatch 400, none of them saved; a
  good change saves an argon2id hash that verifies;
- a viewer gets no forms and a 403 on every POST, and changes nothing;
- `EditConfig`: strings, bools, whole numbers (class keys in hex), string lists; unknown keys,
  wrong types, fractions and negatives refused; comments survive; the firewall block replaces
  itself, takes in hand-written tables and `allow = []`, and an empty list gives back the
  original file; a rename and back gives back the file;
- `ParseCounters` on `nft -j` output; `allowed()` for hosts and CIDRs; `writeAtomic` keeps the
  mode and leaves no temporary file; reload's live set now includes the name, PPP debug and idle
  timeout and the web accounts.

On the test Pi, `0.6.0~test17`, then `~test18` with two fixes (below):

```
$ palm-gwctl get bluetooth.name
Palm Gateway - Lyman
$ palm-gwctl get web.password_hash
(redacted)
$ palm-gwctl set bluetooth.name Palm Gateway - Lyman test
bluetooth.name saved.
applied now: bluetooth.name
$ palm-gwctl status | grep alias
alias "Palm Gateway - Lyman test"
$ palm-gwctl set bluetooth.name Palm Gateway - Lyman
…
alias "Palm Gateway - Lyman"
$ palm-gwctl set bluetooth.adapter bogus
palm-gwctl: not saved: config: bluetooth.adapter: must be "auto", hciN, or the adapter's address XX:XX:XX:XX:XX:XX, got "bogus"
$ palm-gwctl set ppp.idle_seconds soon
palm-gwctl: ppp.idle_seconds: want a whole number of 0 or more
$ palm-gwctl firewall add 192.0.2.10 tcp/9 --comment "doc address test"
firewall.allow saved.
applied now: firewall.allow
$ palm-gwctl firewall list
 1  192.0.2.10         tcp/9  # doc address test
$ sudo nft list set inet palm_gw lan_allow | grep elements
    elements = { 192.0.2.10 . tcp . 9 }
$ palm-gwctl firewall test 192.0.2.10 9          # test18
192.0.2.10:9 does not answer from the Pi: dial tcp4 192.0.2.10:9: i/o timeout. An allow rule lets Palms reach it.
$ palm-gwctl firewall rm 1
$ palm-gwctl firewall list
No [[firewall.allow]] rules: Palms reach the internet and nothing on the LAN.
$ palm-gwctl firewall counters
palm_forward   palm->rfc1918                     0 packets          0 bytes
palm_forward   palm->other                       0 packets          0 bytes
palm_input     palm->webui-lan                   0 packets          0 bytes
palm_input     palm->pi                          0 packets          0 bytes
```

(192.0.2.10 is a documentation address, not on the LAN.) **Found on test17:** (1) `firewall
test` came back as an error, because its reply had an `error` field; renamed `problem`.
(2) `config.toml` was 5 bytes longer after the rename and back: the comment the longer name
pushed right stayed there. Fixed in the editor (D-053). On test18 the same rename and back
took the file from `e071bb74…` (test17's drift) to `2951462f…`, its bytes before part 3, and
the firewall add and remove left it there.

`palm-gw web set-password`, on a root-owned copy of the config in `/var/tmp` with throwaway
passwords and no daemon socket (the real config was not touched):

```
$ printf "x\n" | sudo palm-gw web set-password --viewer --config /var/tmp/pw-test.toml …
palm-gw web set-password: the viewer account has no username yet; give one with --user
$ printf "short\n…-1\n…-2\n…-1\n…-1\n" | sudo palm-gw web set-password --viewer --user lookonly …
Password:   at least 8 characters
Password: Again:   The two do not match; try again.
Password: Again: Password set for "lookonly" in /var/tmp/pw-test.toml.
palm-gwd is not running; it will use the new password when it starts.
$ sudo grep ^viewer_ /var/tmp/pw-test.toml
viewer_username = "lookonly"
viewer_password_hash = "$argon2id$v=19$…"
$ palm-gw web set-password --config /var/tmp/pw-test.toml
palm-gw web set-password: run it as root: sudo palm-gw web set-password
```

The copy was shredded afterwards.

**From Zoe's browser (test18), 2026-11-12:** she renamed the gateway on Settings to "Palm
Gateway - Lyman - Test" and the T|X's Add Device search listed the new name ("New name
shows"). The log has `config_set key=bluetooth.name`, `adapter_renamed alias="Palm Gateway -
Lyman - Test"` and `web_setting user=sysadmin key=bluetooth.name` at 09:07:25, with no restart.
She kept the new name. She had also added a firewall rule at 09:01:53 (`config_set
key=firewall.allow`). This is Phase 7's "change the gateway name … after a re-scan".

### Part 3b: drop-downs and network settings from the browser (D-054), 2026-11-12

Zoe asked for the adapter as a drop-down, and for the settings shown as "changed with `sudo
palm-gw setup`" to be changeable on the page, made friendlier. Section 8.12 lists them on
`/settings` anyway. On test20 (test19 found the bug below):

```
$ palm-gwctl choices
Bluetooth adapters:
  USB dongle 60:15:6F:EA:00:81 (hci0)  <- in use
Internet connections (uplink = "eth0"):
  Ethernet eth0, via router 10.4.0.1  <- in use
Palm network: 10.42.0.0/24, gateway 10.42.0.1
Web UI: tls = self-signed, Bluetooth side on
$ palm-gwctl network --bluetooth-side off
Saved web.bluetooth_side.
Applying with palm-gw setup --apply (journalctl -u palm-gw-apply); palm-gwd restarts when it is done.
$ palm-gwctl choices | tail -2                                  # about 15 s later
Web UI: tls = self-signed, Bluetooth side off
Last network apply: OK, 2026-11-12 09:21:17
$ sudo nft list chain inet palm_gw palm_input | grep -c "web UI and API"
0
$ sudo cat /var/lib/palm-gw/last-apply.json
{ "finished": "2026-11-12T16:21:17Z", "ok": true, "changed": [ "/etc/nftables.d/palm-gw.nft" ],
  "warnings": [ "the Pi has been under-voltage since boot …", "Pi 5 supply negotiated 3000 mA …" ],
  "fingerprint": "D6:82:63:AA:E0:1A:0B:…" }
$ palm-gwctl network --bluetooth-side on                         # and back
$ sudo nft list chain inet palm_gw palm_input | grep -c "web UI and API"
1
$ sudo ss -ltnH "sport = :8042" | awk '{print $4}'
127.0.0.1:8042
10.4.1.118:8042
10.42.0.1:8042
$ palm-gwctl network --uplink eth9
palm-gwctl: uplink: this Pi has no interface called "eth9"
$ palm-gwctl network --subnet 10.4.0.0/24
palm-gwctl: 10.4.0.0/24 overlaps a network this Pi is on (eth0 10.4.0.0/23); 10.43.0.0/24 is free
$ palm-gwctl network --uplink eth0 --subnet 10.42.0.0/24 --tls self-signed
Nothing changed.
```

So palm-gwd, inside its sandbox, started `palm-gw setup --apply` with `systemd-run`, which
rewrote the firewall outside the sandbox and restarted palm-gwd. The certificate was kept
(same fingerprint). Afterwards the only difference in `config.toml` was an explicit
`bluetooth_side = true` line, the default, since Zoe's file predates the key. Her file was put
back from the copy taken first (sha `98805e63…`, with her new name and rule).

**Found on test19:** `network --bluetooth-side off` was refused with "config:
firewall.allow.bluetooth_side: unknown key". `[web]` had no `bluetooth_side` line, so the
editor added one after the section's last key. But its section pattern did not match
`[[firewall.allow]]`, so it thought `[web]` ran on into the firewall rules at the end of the
file, and put the line there. Validation caught it, so nothing was written. Fixed: `[[…]]`
ends a section (`TestSetKeyMissingBeforeArrayTables`).

Not tried on the Pi: changing the uplink (wlan0 is down, so eth0 is the only choice) or the
subnet. Both go through the same apply, and the unit tests cover the edits. The subnet change
also makes a new certificate, because the gateway address is in it.

### Part 3c: the NAT table (D-055), 2026-11-12

Zoe asked for the NAT table to be shown. On test21 (no Palm connected at that moment; the
T|X had been online shortly before):

```
$ ls /proc/net/nf_conntrack; which conntrack
ls: cannot access '/proc/net/nf_conntrack': No such file or directory
$ cat /proc/sys/net/netfilter/nf_conntrack_count
67
$ palm-gwctl nat
DEVICE             PROTO FROM                  TO                    OUT AS                STATE        TIMEOUT
unknown            tcp   10.42.0.10:46924      209.121.220.108:14244 10.4.1.118:46924      time-wait    45s
1 from 10.42.0.0/24, of 19 tracked.
```

The ctnetlink dump works from inside palm-gwd's sandbox. The entry is the T|X's last
connection closing after its session ended, so it has no device name (D-055). Still to see:
the card in a browser with the T|X connected and loading a page.

### Part 3d: the internet connection wizard (D-056), 2026-11-12

Zoe asked for a simple way to switch between Wi-Fi and Ethernet and to change Wi-Fi networks.
On test22, against the Pi's NetworkManager 1.52.1 (`wlan0` disconnected, no saved Wi-Fi,
regulatory domain CA from Imager). The scan's network names are not recorded here: they are the
neighbours'.

```
$ palm-gwctl internet
Ethernet eth0: Wired connection 1, 10.4.1.118/23 via 10.4.0.1  <- the internet connection
Wi-Fi wlan0: disconnected
$ palm-gwctl wifi scan          # names replaced
net1 100% 2.4 GHz wpa
net2 100% 2.4 GHz wpa
net3  95% 2.4 GHz wpa
net4  94% 2.4 GHz wpa
net5  47% 2.4 GHz enterprise not supported here
net6  40% 2.4 GHz wpa
$ palm-gwctl internet use ethernet
The internet connection is eth0.
```

Ethernet was already the uplink, so this changed no route metric and started no apply
(`palm-gw-apply` inactive). Not yet tried, because it needs Zoe's Wi-Fi password, which only
she types:
- joining her network from the Internet page, which switches the uplink to wlan0 and
  restarts palm-gwd;
- a Palm reaching the internet through Wi-Fi;
- "Use Ethernet for the internet" to switch back;
- a wrong password, to see the rollback message.

**From Zoe's browser (test22), 2026-11-12:** she joined her home Wi-Fi from the Internet page
and switched back with "Use Ethernet for the internet" ("it worked"). The log (her network's
name left out):

```
10:46:32  wifi_joining   ssid=<home> hidden=false
10:46:36  wifi_joined    ssid=<home>                       # 4 s
10:46:36  network_set    keys=[network.uplink]  unit=palm-gw-apply   # eth0 -> wlan0
10:47:28  network_set    keys=[network.uplink]  unit=palm-gw-apply   # wlan0 -> eth0
10:47:28  internet_use   iface=eth0 switching=true
$ palm-gwctl internet
Ethernet eth0: Wired connection 1, 10.4.1.118/23 via 10.4.0.1  <- the internet connection
Wi-Fi wlan0: <home>, 10.4.1.119/23 via 10.4.0.1
Saved Wi-Fi networks: <home>
```

Both applies ended OK (`last-apply.json`). Wi-Fi stays connected below Ethernet, as D-056
intends. At her request the settings page's link to the Internet page is now a button.
Not tried yet: the rollback after a wrong password.

### Part 4: logs, diagnostics, about, class toggle (D-057), 2026-11-12

On test24 and test25 (test24's run found the three fixes below). No Palm was connected.

```
$ palm-gwctl check
board:    pi5 (Raspberry Pi 5 Model B Rev 1.0)
adapter:  USB dongle 60:15:6F:EA:00:81 (hci0), powered yes
uplink:   Ethernet eth0, via router 10.4.0.1
uplink:   Wi-Fi wlan0 (5 GHz), via router 10.4.0.1
unit:     bluetooth            active
  … nftables, palm-gw-dnsmasq, palm-gwd-nm-bridge, palm-gwd, NetworkManager: active
module:   bnep                 loaded
  … rfcomm, ppp_generic, ppp_async: loaded
WARNING:  Pi 5 supply negotiated 3000 mA (under 5 A), …
$ palm-gwctl logs 5 --event 'wifi_*'
Nov 12 10:46:32 palm-gwd  INFO  [wifi_joining] joining Wi-Fi component=daemon hidden=false ssid=<home>
Nov 12 10:46:36 palm-gwd  INFO  [wifi_joined] Wi-Fi joined component=daemon ssid=<home>
$ palm-gwctl capture 3
Capturing for 3 s into capture-20261112-105951.btsnoop....
Wrote /var/lib/palm-gw/captures/capture-20261112-105951.btsnoop (open it in Wireshark).
$ sudo ls -l /var/lib/palm-gw/captures/
-rw-r----- 1 root root 292 Nov 12 10:59 capture-20261112-105951.btsnoop
$ palm-gwctl bundle --with-capture
Wrote /var/lib/palm-gw/bundles/palm-gw-support-20261112-105955.tar.gz (40802 bytes). Password hashes are redacted.
$ sudo tar -tzf …/palm-gw-support-20261112-105955.tar.gz      # 26 files
README.txt status.json check.json internet.json nat.json events.json config.toml install-info.json
last-apply.json dnsmasq.conf bridge.env palm-gw.nft bluetooth-main.conf nft-ruleset.txt ip.txt
routes.txt packages.txt units.txt kernel.txt journal-palm-gwd.txt journal-bluetooth.txt
journal-dnsmasq.txt journal-bridge.txt journal-apply.txt capture-20261112-105951.btsnoop
$ sudo tar -xzOf … | grep -ac argon2
0
$ sudo tar -xzOf … --wildcards '*/config.toml' | grep -c REDACTED
1
$ sudo tar -xzOf … | grep -ac 'PRIVATE KEY'
0
$ echo '{"cmd":"update.check"}' → ctl socket
{"ok":false,"output":"palm-gw upgrade: no release has been published yet"}
$ palm-gwctl class toggle            # then wait for palm-gw-apply and the restart
Class of Device 0x000300 -> 0x000104. palm-gw setup --apply rewrites main.conf and restarts bluetooth and palm-gwd.
adapter:   hci0 60:15:6F:EA:00:81 (usb) … class 0x420104 …
$ palm-gwctl class toggle
Class of Device 0x000104 -> 0x000300. …
adapter:   hci0 60:15:6F:EA:00:81 (usb) … class 0x420300 …
```

`/etc/bluetooth/main.conf` was `c54d1541…` before and after the two toggles, and bluetooth and
palm-gwd were active. The update check reached GitHub from inside the sandbox; no release is
published, as expected.

**Found on test24:**
- `logs 5 --event 'wifi_*'` found nothing. With a filter it read only 10× the lines asked
  for; it now reads at least 5000.
- systemd's "Started palm-gwd.service" lines showed as `init.scope`; the `UNIT` field now wins.
- Captures were 0644, as btmon's umask left them; they are now 0640.

Still to do from a browser (Phase 7's accept): the logs page with Follow; downloading a capture
and opening it in Wireshark; downloading a bundle and confirming the hash is redacted.

**From Zoe's browser (test25), 2026-11-12, "All works":** she logged in from 10.4.0.54 at
11:04:51 and started a 60 s capture from Diagnostics at 11:05:49. At 11:05:51 she downloaded
`capture-20261112-110549.btsnoop` and opened it in Wireshark. That was two seconds into the
capture, so she got the file as btmon had written it by then. The page listed the running
capture as a download, and the file was 0644 until the end.

Fixed on test26:
- The running capture is not listed until it ends.
- A download request for it gets 409 "still recording".
- The file is created 0640 before btmon opens it; btmon keeps an existing file's mode.

On the Pi:

```
during: running True capture-20261112-110756.btsnoop listed: [the four earlier captures]
after:  listed: ['capture-20261112-110756.btsnoop', …]
$ sudo ls -l /var/lib/palm-gw/captures      # while recording
-rw-r----- 1 root root 292 Nov 12 11:07 capture-20261112-110749.btsnoop
```

No bundle was made from the browser yet; the bundle's contents were checked from the shell
above.

### Part 5: the /api/v1 API (D-058), 2026-11-12

`httptest` covers what `docs/WEB-UI.md`'s test list asks of the API:
- the same reply for a wrong username and a wrong password;
- token expiry and logout;
- a token is not a cookie and a cookie is not a token;
- a token from one side is refused on the other;
- a viewer token gets 403 on every POST but logout, and never sees the PIN;
- the rate limit, counted per side;
- the text format encodes Windows-1252 (`ë` is 0xEB, `’` 0x92, `€` 0x80) and degrades to `?`;
- every call: pair and close, device rename/forget/disconnect, firewall allow and remove,
  settings, events, me.

On test27/test28, from the Mac, with no login (logging in needs Zoe's password):

```
$ curl -sk -D - https://10.4.1.118:8042/api/v1/status
HTTP/1.1 401 Unauthorized
Content-Type: application/json
Www-Authenticate: Bearer realm="palm-gw"
{"error":"login_required","message":"log in first: POST /api/v1/login, then send Authorization: Bearer <token>"}
$ curl -sk 'https://10.4.1.118:8042/api/v1/status?format=text' | od -c | head -2
0000000    e   r   r   o   r   =   l   o   g   i   n   _   r   e   q   u
0000020    i   r   e   d  \r  \n   m   e   s   s   a   g   e   =   l   o
$ curl -sk -H 'Authorization: Bearer 00' https://10.4.1.118:8042/api/v1/nothing
{"error":"not_found","message":"no such API call: GET /api/v1/nothing"}
$ curl -s -m 5 http://10.4.1.118:8042/api/v1/status; echo "exit $?"
exit 52                                     # plain HTTP on the TLS port: no reply
```

test27 HTML-escaped `<token>` as `<token>`; test28 does not. Still to do: Zoe logging
in through the API with her password, and the Bluetooth side (part 6).

**From Zoe's screenshot (test28), 2026-11-12:** after palm-gwd restarted for an install, an open
dashboard showed the login page inside its own layout. The dashboard's 3 s htmx refresh got a
303 to `/login`; htmx followed it and swapped the whole login page into the dashboard. Fixed
on test29: a request with `HX-Request: true` and no session gets 401 with `HX-Redirect:
/login`. htmx acts on that before any swap and loads the login page
(`TestHtmxRefreshWithoutSession`).

The same screenshot showed a different certificate fingerprint (`26:AA:9F:…`, was
`D6:82:63:AA:…`). The journal of `palm-gw-apply` shows `cert.pem created` at 10:46:36 (to Wi-Fi)
and at 10:47:28 (back to Ethernet): the certificate lists the uplink's address, so each switch
made a new one. Its names are now the same as before (palmgw-test, palmgw-test.local,
10.4.1.118, 10.42.0.1), but the key and fingerprint are new.

**D-059, at Zoe's choice:** the certificate is kept across uplink and LAN-address changes, and
remade only for a new gateway address or host name (`TestEnsureCertKeepsAcrossUplinks`).
On test30, with no Palm connected, switching the uplink to Wi-Fi and back kept it:

```
installed: 26:AA:9F:DB:90:CC:DE:0E
$ palm-gwctl internet use wifi      → on wifi: 26:AA:9F:DB:90:CC:DE:0E  uplink=wlan0
$ palm-gwctl internet use ethernet  → back:    26:AA:9F:DB:90:CC:DE:0E  uplink=eth0
palm-gw-apply: /var/lib/palm-gw/tls/cert.pem unchanged   (both times)
```

### Part 6: acceptance from the Bluetooth side, 2026-11-12

Both Pis are in dongle mode with one adapter each, so fake-palm ran on a separate machine, as
section 9 first intended. That machine was the first Pi (10.4.0.241, its palm-gwd stopped for
the run, at Zoe's OK), with a current fake-palm build. The gateway was the test Pi on test30.
The client side lived in network namespace `fakepalm` on the first Pi. Pairing went through
`palm-gwctl pair 2 --one` on the gateway (SSP, passkey confirmed by fake-palm, 4.9 s).

**PPP peer** (`fake-palm connect … --channel 2 --mode ppp --netns fakepalm --hold 1200s`; ppp1,
10.42.0.11), with the T|X connected as 10.42.0.10 at the same time:

```
PASS: TLS 1.3 to 10.42.0.1:8042, login page (200)        certificate 26:AA:9F:DB:90:CC:DE:0E…
PASS: TLS 1.2 refused (curl exit 35)
PASS: plain HTTP on 8042: no answer (curl exit 52)
PASS: port 80: connected page
PASS: the Pi LAN address 10.4.1.118:8042 dropped (timeout)
PASS: 10.4.1.118:22 dropped
PASS: 10.4.1.118:80 dropped
PASS: TCP open on 10.42.0.1: 53 80 8042                    (nmap -sT -p-, the second run)
      UDP 53 open, 67 open; 68, 123, 5353 open|filtered (no answer: dropped)
PASS: ICMP echo answered
PASS: DNS on 10.42.0.1:53
```

**The API over the Bluetooth side, with Zoe's password** (she typed it into a prompt in her own
terminal, in the same namespace, over the PPP link):

```
login: ok admin expires_in 259199
--- me (text)            session=yes name=Palm Gateway addr=60:15:6F:E9:EB:03 profile=lap ifname=ppp1 ip=10.42.0.11 …
--- status (text)        gateway=Palm Gateway - Lyman … sessions=2 pool_in_use=2 uplink=eth0 pairing=0
--- sessions (text)      name=Zoë's Palm TX (Windows-1252, decoded with iconv) … name=Palm Gateway …
--- pair 1 minute, one device
{"status":"ok","pin":"5640","expires_in":59,"one":true}
--- pair/close           {"status":"ok","was_open":true}
--- logout               {"status":"ok"}
--- status with the old token
401
```

**PAN client** (`fake-palm pan … --netns fakepalm --hold 900s`): lease 10.42.0.103 in the DHCP
pool after 7.4 s, ping and DNS OK. TLS 1.3 login page, TLS 1.2 refused, no plain HTTP on 8042,
the connected page on port 80, and 10.4.1.118 dropped on 8042, 22 and 80: all PASS, as over PPP.

```
$ nmap -sT -Pn -n -p- --max-rate 300 --max-retries 3 10.42.0.1      # 438 s
Not shown: 65532 filtered tcp ports (no-response)
53/tcp   open  domain
80/tcp   open  http
8042/tcp open  fs-agent
$ nmap -sU -p 53,67,68,123,5353 10.42.0.1
53/udp open, 67/udp open, 68/123/5353 open|filtered
```

**The scan rate matters on Bluetooth.** At `--min-rate 1500` the first scan missed 8042 over
both PPP and PAN. nmap probes common ports such as 53 and 80 first, and by the time it reached
8042 the link was saturated. Over PAN the scan straight after found nothing and ping failed
until the backlog cleared. The ruleset has no rate limit; at 300/s with three retries the
result is exact.

Clean-up: the first Pi's pairing with the gateway was removed (`RemoveDevice`), the namespace
deleted, and its palm-gwd started again. `palm-gwctl forget 60:15:6F:E9:EB:03` on the gateway.

**Seen on the way: Zoe's Pocket PC** (ASUS, Windows Mobile, `Zoe_PocketPC`, 00:1E:8C:EF:86:AD,
class 0x3A0110) paired through a window at 11:29:43 and dials LAN Access with "Bluetooth Null
Modem".
- Its first two tries (11:29:43, straight after pairing, and 11:32:15) sent nothing for 3 s.
  palm-gwd's `ms_timeout_ms` ran out and pppd started blind. The Pocket PC then said "The
  answering modem has disconnected" and reset the link 24 s and 12 s later.
- The third try (11:39:15) sent `CLIENT` after 1.2 s, got `CLIENTSERVER`, and PPP came up as
  10.42.0.12.
- Its own "Network Log On" user and password do nothing here: palm-gw's PPP has no
  authentication (`noauth`, section 7).
- A longer `ms_timeout_ms` would cover the slow `CLIENT`, at no cost to Palms, which send PPP
  at once. Not changed yet.

**Phase 7's accept, where it stands:**
- **Done:**
  - LAN browser: log in, pairing window with its PIN, pair a Palm and watch the feed, rename
    and re-scan, a capture in Wireshark.
  - PAN: the connected page.
  - Bluetooth side: login over TLS only, no plain HTTP, a pairing window and PIN through the
    API, the LAN address on 8042 dropped, and nmap.
- **Still to do:**
  - Disconnect and forget from the Devices page in a browser.
  - A firewall rule's effect seen from a Palm.
  - A support bundle downloaded from the browser.
  - The Palm app's checks (part 7).

**D-060, Zoe's "Yes, fix it" for the Pocket PC:** `ms_timeout_ms` defaults to 10000, and the
`[handshake]` keys apply on reload. On test31:

```
$ palm-gwctl set handshake.ms_timeout_ms 10000
handshake.ms_timeout_ms saved.
applied now: handshake.ms_timeout_ms
$ palm-gwctl get handshake.ms_timeout_ms
10000
```

Still to see: the Pocket PC connecting on the first try.

**Pocket PC on test31, 2026-11-12 ("Works first try"):** three connections, each first try:

```
15:42:51.915 RFCOMM connection → 15:42:52.938 handshake ms_null_modem   (1.0 s)
15:44:13.083 RFCOMM connection → 15:44:14.098 handshake ms_null_modem   (1.0 s)
15:46:19.260 RFCOMM connection → 15:46:20.334 handshake ms_null_modem   (1.1 s)
```

Each sent `CLIENT` within about a second, so none needed more than the old 3 s; the slow case
seen at 11:29 and 11:32 did not come back in these three. D-060's longer wait stays for when it
does.

**Firewall from a Palm (Zoe, 2026-11-12):** she added allow rules in the browser's Firewall page
and reached the allowed hosts from a Palm straight away, several times ("Firewall works I have
done it a few times"). Phase 7's "add a firewall allow entry and verify a Palm can reach it
immediately" is done.

### Part 7: the Palm app, milestone 1 (D-062), 2026-11-12

`docs/PALM-M1.md` has the build, the checklist and the device result. In short:
- On the Mac: `make -C palm-app/tls host-test` passes; `make -C palm-app/app test` passes 40
  checks.
- Against the test gateway, the host client completed TLS 1.3 (heap peak 16,240 bytes) and
  saw the fingerprint the login page shows.
- On Zoe's T|X, over Bluetooth, the app logged in through `/api/v1/login` on the Bluetooth
  side (`web_login side=bluetooth ip=10.42.0.10 api=true`, 17:09:38) and showed Status.

### Part 7: the Palm app, milestone 2: parity (D-064), 2026-11-12

`docs/PALM-M2.md` has the screens and the T|X checklist. On the Mac:

```
$ make -C palm-app/app
  pairf: 7032 bytes
  .text: 18328 bytes
  .data: 168 bytes
  .bss: 29858 bytes
  listf: 4172 bytes
  modef: 13160 bytes
check-sections: ok (102 sectioned function(s))
-rw-r--r--  1 zoe  staff  280222 Nov 12 18:55 build/PalmGW.prc
$ make -C palm-app/app test
gw: 18 ok
kv: 18 ok
http: 12 ok
```

- Every code resource is under 32 KB, and globals are under the 32 KB A5 limit.
- The build found two compiler limits, both now noted in the source. A `static const` table in
  a sectioned function is caught by `check-sections.sh`. m68k-palmos-gcc 2.95 at `-O2` fails
  ("unrecognizable insn") on a `c ? "text" : NULL` argument.
- A review agent checked every key and form field the app uses against `api.go` and `api2.go`
  and found no mismatch. Its Palm-side findings were all fixed before the commit:
  - label and line buffers that could overflow;
  - form values cut part-way, including passwords;
  - `apiLeft` running twice;
  - a wrong current password logging the user out;
  - password fields shown and left in memory;
  - one lost poll ending a Wi-Fi join;
  - Done going to Status from a screen opened elsewhere;
  - PAN sessions shown with 0 bytes;
  - byte counts past 2^31.
- **First try on the T|X (19:40):** Settings, Logs, Diagnostics and Internet said "no such
  API call": the test Pi still ran test34, from before the API parity commit (902c150).
  Sessions, an M1 call, showed the T|X's link and byte counts. The app showed the gateway's
  error on each screen and did not crash.
- **test35 (fca3b22) on the test Pi, 19:50,** installed with no session open:

  ```
  $ make deb DEB_VERSION=0.6.0~test35
  $ scp dist/palm-gw_0.6.0~test35_arm64.deb sysadmin@10.4.1.118:/var/tmp/
  $ ssh sysadmin@10.4.1.118 'sudo apt-get install -y /var/tmp/palm-gw_0.6.0~test35_arm64.deb'
  palm-gwd fca3b22, up 6s (phase 6)
  $ curl -sk https://10.4.1.118:8042/api/v1/<call>?format=text      # no token
  ```

  Every call the app uses answers `error=login_required` (status, settings, logs, check,
  diagnostics, internet, wifi/scan, nat, firewall/counters, devkeys, about, sessions,
  devices, firewall, events; POST firewall/test, pair, capture, bundle, password), and an
  unknown call still answers `error=not_found`.

### The T|X's "Serial: timeout (0x0305)" drops, 2026-11-12 (D-050, open)

Zoe saw the Palm's "Error: Serial: timeout ... (0x0305)" more often, while CradleSync synced
over the gateway. From the gateway's log:
- **The heavy session ended in under-voltage.** At 17:23:08 the T|X's PPP came up at once
  (`handshake: none`). pppd carried 276,539 bytes down and 19,478 up in 1.9 minutes. The kernel
  logged under-voltage at 17:24:04, 17:24:56 and 17:25:00, and the Palm ended PPP cleanly at
  17:24:59 ("IPCP/LCP terminated by peer"). The Pi had 69 under-voltage events that day. Its
  supply negotiated 3,000 mA (`/proc/device-tree/chosen/power/max_current`); a Pi 5 wants 5 A.
  The Bluetooth dongle is on a USB 2 port (bus 3, 480M).
- **The short drops carried nothing.** At 17:01 (38 s) and 17:13 (35 s), PPP came up, pppd
  sent 0 bytes and received 4, and the Palm ended it. No under-voltage was near either. Zoe:
  a second try straight after usually works.
- **D-060 is not involved.** Every T|X connection that day started PPP within 50 ms
  (`handshake: none`), so the longer wait for CLIENT never applied.
- **The Pi rebooted at about 17:31.** At 17:30:09 a 10-minute btmon capture was started and
  `ppp.debug` turned on, to catch a failing first connect. The capture file stops at 1,151
  bytes. The Pi rebooted around 17:31; it has a 1-minute hardware watchdog, and its clock
  restarted at the saved 17:27. The journal is volatile, so the cause is lost:
  `power_reset` was 0, and `get_throttled` was 0x0 after the boot.
- **After the reboot,** at 17:36:23, the T|X connected and PPP negotiated in 80 ms (LCP, then
  IPCP with address and DNS, all acknowledged both ways). The session stayed up past the
  35-40 s mark.

**The test Pi's journal is now persistent** (Zoe's OK, 2026-11-12): the drop-in
`/etc/systemd/journald.conf.d/50-palm-gw-persistent.conf` (`Storage=persistent`,
`SystemMaxUse=100M`) overrides Raspberry Pi OS's
`/usr/lib/systemd/journald.conf.d/40-rpi-volatile-storage.conf`. After `systemctl restart
systemd-journald` and `journalctl --flush`, the journal is at
`/var/log/journal/<machine-id>/system.journal` (16 MB so far), and `journalctl -b -1` will
show the boot before a reset. This is a change to the test Pi only, not to palm-gw; deleting
the file goes back to the journal in RAM. `ppp.debug` stays on until the first-connect drop
is caught.

**The test Pi's supply, 2026-11-12.**
- **Before:** Zoe's Anker 35 W charger. Its USB PD offers, read from
  `/proc/device-tree/chosen/power/usbpd_power_data_objects`, were 5 V 3 A, 9 V 3 A, 15 V 2 A
  and 20 V 1.5 A. A Pi 5 takes only 5 V, so it got 15 W, `max_current` 3000, and USB limited to
  600 mA.
- **After, at 17:41:** another supply that offers 5 V 5 A (also 9 V 3 A, 12 V 3 A, 15 V 3 A,
  20 V 2.25 A and PPS). The Pi negotiated `max_current` 5000, `EXT5V_V` reads 5.19 V,
  `get_throttled` is 0x0, and there was no under-voltage after the boot.
- `journalctl --list-boots` already shows the previous boot, which ended at 17:41:07 when the
  supply was swapped.
- A new 10-minute capture started at 17:44:01, with `ppp.debug` on, to catch a first-connect
  drop.

**What the capture showed (test33, 17:44-17:49, the new supply, no under-voltage).** The T|X
showed "Serial: timeout (0x0305)" again during a CradleSync. The btmon capture and pppd's
debug log:
- **Bluetooth and PPP stayed up throughout.** One ACL link, no mode changes; it ended with the
  Palm's disconnect (reason 0x13, remote user terminated). PPP negotiated in 40 ms. pppd sent
  no LCP echo (it does only after 20 s with nothing received). Keepalives crossed both ways at
  17:48:58 and 17:49:18, after the data stopped.
- **The dongle never fell behind.** Every ACL packet sent to the T|X was completed by the
  controller in the same second (Number of Completed Packets), with no backlog.
- **IP traffic stopped in both directions** three times: 15 s from 17:46:02, 32 s from
  17:47:20, then for good from 17:48:54. The first two times data resumed, at about 2.3 KB/s
  each way. 361 KB went out to the Palm and 442 KB came in.
- **Then temporary counters:** on the invalid-packet drop in `palm_forward`, on invalid
  packets from eth0 to `ppp*`, and on everything from eth0 to `ppp*`. The next CradleSync
  (17:53, finished fine) gave 0 invalid packets either way; 841 packets in from the internet,
  all forwarded; `ip -s link show ppp0` 0 errors and 0 drops each way.

**Conclusion.** The gateway neither drops nor holds back this traffic, and the radio link stays
up. The 0x0305 errors had two causes. Under-voltage on the 3 A supply during heavy transfers is
fixed by the 5 A supply. The rest are pauses where neither end sent anything, long enough for
the T|X to give up: an application-level wait between CradleSync and its airlock, to be read
in the airlock's session log. The short first-connect drops (35-40 s, nothing sent; D-050)
did not happen again in the evening's connects (17:36, 17:44, 17:53). `ppp.debug` stays on
until one does.

The invalid-packet drop in `palm_forward` had no counter, which is why it took temporary rules
to rule it out. It now counts as `palm->invalid` (test34), so the firewall page and
`palm-gwctl firewall counters` show it like the other drops.

**Another kind of 0x0305: the T|X's radio goes silent after a CradleSync (19:31-19:41).**
Zoe saw the error again, on the 5 A supply (`get_throttled` 0x0). A 10-minute btmon capture
plus palm-gwd's log, started before her next try:

```
$ ssh sysadmin@10.4.1.118 'sudo timeout 600 btmon -T -w /var/tmp/palm-watch.btsnoop'
$ ssh sysadmin@10.4.1.118 'sudo btmon -T -r /var/tmp/palm-watch.btsnoop' | ...
19:31:59.128  Connect Request from 00:07:E0:3C:5E:FA, accepted as Central
19:31:59.969  PPP link up, 10.42.0.10 (RFCOMM, handshake and PPP in 0.6 s)
19:32:19.962  api login by device key (the palm-gw app)
19:32:58.270  T|X -> 10.4.0.7:14243 SYN (CradleSync, Secure); TLS from 19:32:58.698
19:32:58-02   about 40 TLS records from the T|X, each answered within 50 ms; no stall
19:33:02.247  airlock -> T|X: a 24-byte TLS record, then FIN (the airlock ends the session)
19:33:02.262  T|X FIN-ACK; its last ACK at .290, receive window 0 (socket closed)
19:33:20.002  gateway -> T|X: LCP Echo-Request (no answer)
19:33:28.337  HCI Disconnection Complete, reason Connection Timeout (0x08)
19:33:28.338  pppd: Error reading standard input: Connection timed out; session_end 1m29s
```

- **Not the shape seen at 17:44.** Then the link stayed up and the T|X hung up itself
  (reason 0x13). This time the session ran without a pause (the link sat idle for 37 s
  before it, after the app's login, with LCP echoes answered), the airlock closed it after
  4 seconds, and about 6 seconds later the T|X stopped answering at the radio level. Zoe saw
  the error "during the sync", that is, while CradleSync's progress screen was still up. The gateway's controller kept the link for the
  20-second supervision timeout and then dropped it. Nothing was owed either way: the last
  packet was the T|X's final ACK.
- The 18:54:51 drop that afternoon, also after a CradleSync (3.4 min, 875 KB), ended the
  same way ("Connection timed out").
- **Ruled out on the gateway:**
  - the dongle (a TP-Link UB500, RTL8761BU) is alone on a USB 2 bus, with no USB 3 device;
  - `hciconfig` shows 0 errors either way and no reset since boot;
  - the Pi's own Wi-Fi is on 5 GHz (channel 48);
  - no mode changes (no sniff) during the link.
- **The airlock's side** (`journalctl -u cradle-airlock` on 10.4.0.7):

  ```
  19:32:58.290  connection accepted remote=tls:10.4.1.118:36815
  19:32:59.142  TLS established (TLS 1.3)
  19:32:59.622  palm connected user="Zoë Leto"
  19:32:59.794  turned off on the Palm off=backup,sync
  19:32:59.890  palm connection link=Bluetooth profile="Palm Gateway - Lyman" address=10.42.0.10
  19:33:00.019  palm authenticated
  19:33:01.946  clock: set
  19:33:02.247  session finished outcome=backup duration=3.103s exit=0 backedUp=0 failed=0
  ```

  The session was complete: Sync and Backup were unticked on the T|X, so there was nothing to
  do but authenticate and set the clock, and the airlock finished it with exit 0.
- **Conclusion.** The gateway carried the session without a pause, and the airlock finished it
  cleanly. About 6 seconds later the T|X's Bluetooth stopped answering, while CradleSync's
  progress screen was still up, and the T|X showed 0x0305. The cause is on the T|X, in how a
  CradleSync session ends; CradleServer's own notes record T|X resets in the seconds after a
  session's end. It belongs to CradleSync, not palm-gw. CradleSync closes
  with `NetLibSocketClose`, `restoreNetTimeouts` (it puts back the interface's inactivity
  timeout) and `NetLibClose(ref, false)`. To compare: let a session of the palm-gw app alone
  idle past a minute, and a CradleSync session end while watched, noting when the dialog
  appears. The capture stays at `/var/tmp/palm-watch.btsnoop` on the test Pi.

