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
