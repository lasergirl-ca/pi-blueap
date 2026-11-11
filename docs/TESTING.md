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

## Phase 6: Setup wizard and packaging (in progress)

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
