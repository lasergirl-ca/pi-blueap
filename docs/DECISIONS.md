# Decisions and deviations from PLAN.md

Every deviation from `PLAN.md`, however small, with a one-paragraph reason, in the same commit as
the change. Hardware observations (section 16's two residual items) are recorded here too as they
are made. Sections 2 and 16 of the plan are final and are not reopened here.

## Phase 0

### D-001: go.mod declares `go 1.24.0`, not 1.22

The plan says Go 1.22+. The module directive is set to 1.24.0 because that is what Raspberry Pi OS
Trixie ships in `apt` (section 4), so on-device builds work with the distribution toolchain, and
because current versions of `golang.org/x/sys` require 1.24 (v0.40.0 is the newest that does not
require 1.26, and is pinned). Bookworm's 1.19 was already too old under the plan, so nothing is lost.
`github.com/godbus/dbus/v5` is not yet in go.mod: `go mod tidy` removes unused requirements, so it
is added in Phase 1 with its first use. The decided module set in section 2 is unchanged.

### D-002: Ready-to-flash Pi OS images are an added release artefact

Zoe asked (2026-11-08) for Pi images in addition to the `.deb` and the one-liner. This is new
scope on top of section 8.11 and the section 15 checklist: Phase 6 will add a `make image` target
that takes a stock Raspberry Pi OS Lite 64-bit image, installs the `.deb` into it (via
`pi-gen`-style chroot or `sdm`), pre-enables SSH, and leaves `palm-gw setup` to run on first
login; Phase 8 publishes `palm-gw-<ver>-<board>.img.xz` with the release. One image serves both
boards since Pi OS 64-bit boots on both. Details are decided when Phase 6 starts.

### D-003: Deploy files are rendered through the daemon binary (`palm-gwd -render`)

Section 8.11 has `install.sh` substituting `GATEWAY`, `PREFIX`, `UPLINK`, `DHCP_*` into the
`deploy/` files, and section 8.11's wizard uses Go `text/template`. Rather than keep a shell `sed`
implementation and a Go one, `install.sh` calls `palm-gwd -config … -render FILE`, which renders
with Go `text/template` and the validated configuration. The templates read exactly as the plan
writes them (`{{UPLINK}}`, `{{SUBNET}}`) because the values are exposed as zero-argument template
functions. Phase 6's `internal/setup/render.go` will reuse the same `config.Render`. A side effect:
a `-check` flag (validate and exit) exists on `palm-gwd` so `install.sh` can fail early with the
offending key.

### D-004: The NetworkManager bridge oneshot runs a helper script

Section 8.7 gives the bridge logic as a shell snippet for `palm-gwd-nm-bridge.service`. A systemd
unit cannot carry a multi-line conditional, so the snippet lives in `scripts/nm-bridge.sh`,
installed to `/usr/local/lib/palm-gw/nm-bridge.sh`, and the unit is rendered with the bridge name,
gateway and prefix as arguments. The script also runs `nmcli con modify` when the connection
already exists so a changed gateway in `config.toml` is applied on the next boot or reload.

### D-005: dnsmasq instance uses its own runtime directory and lease file

Section 8.11 gives `--pid-file=/run/palm-gw/dnsmasq.pid`. `/run/palm-gw` is `palm-gwd`'s
`RuntimeDirectory=`, and systemd removes a RuntimeDirectory when its unit stops, so sharing it
would let a dnsmasq restart delete the control socket (or vice versa). The unit therefore uses
`RuntimeDirectory=palm-gw-dnsmasq` and `--pid-file=/run/palm-gw-dnsmasq/dnsmasq.pid`. The lease
file is pinned to `/var/lib/palm-gw/dnsmasq.leases` so a system dnsmasq, if present, and ours never
share `/var/lib/misc/dnsmasq.leases`.

### D-006: `config.example.toml` ships the `[[firewall.allow]]` examples commented out

Section 7's listing has two live example allow rules (`192.168.1.20`, `192.168.1.0/24`). Because
`install.sh` copies `config.example.toml` to `/etc/palm-gw/config.toml` when none exists, live
examples would open the firewall to those addresses on every fresh install. They are kept in the
file as comments so the file still documents the syntax.

### D-007: `modules-load.d` lists `ppp_generic` and `ppp_async` as well as `bnep` and `rfcomm`

Section 8.6 lists `bnep, rfcomm`; the trap table in section 12 says the fix for "Couldn't open the
/dev/ppp device" is `modules-load.d` including `ppp_generic`. Listing all four up front costs
nothing and removes one failure mode.

### D-008: `install.sh` detects the uplink when `UPLINK=` is not given

Section 8.11 says install.sh writes the config "honouring `UPLINK=`". When the variable is empty it
now uses the interface of the current default route (what the Phase 6 wizard will also do),
falling back to `eth0`. It says which it picked. If a config already exists, `UPLINK=` is ignored
with a warning rather than editing a file the user may have changed.

### D-009: Unknown configuration keys are an error

Not in section 7's validation list, but in its spirit ("fail to start, with a message naming the
key"): a misspelt key such as `adaptor` would otherwise silently fall back to the default.
Validation also rejects pool bounds and a gateway that are the network or broadcast address, and
rejects a subnet written with host bits set (`10.42.0.1/24`), each with the key named.

### D-010: Unit paths assume the source install (`/usr/local/bin`)

The units reference `/usr/local/bin/palm-gwd` exactly as section 8.10 writes them. Section 8.11's
`.deb` puts binaries in `/usr/bin`, which Debian policy requires. How the two reconcile (the .deb
rendering its own copies of the units, most likely) is decided in Phase 6; nothing in Phase 0
depends on it.

### D-011: The daemon sets the group of its runtime directory, not only of the socket

Section 8.8 wants the control socket `0660 root:palm-gw` so an operator can use `palm-gwctl`
without sudo, and section 8.10 has `RuntimeDirectoryMode=0750`. On the Pi 5 the first run showed
that systemd creates `/run/palm-gw` as `root:root 0750`, so group members could not traverse the
directory to reach a socket that was already `root:palm-gw`. `ctl.Server.Listen` now also sets the
directory's group to `[ctl].group` (mode stays 0750). Verified: `palm-gwctl status` works as
`sysadmin` with no sudo after the fix.

### D-012: `install.sh` re-applies sysctl after the units start

`-net.ipv4.conf.<bridge>.proxy_arp = 0` cannot be applied before the bridge exists. The installer
now applies the sysctl file quietly before the units and again after `palm-gwd-nm-bridge` has
created the bridge, and prints the resulting `ip_forward` and `rp_filter` values. At boot,
`systemd-sysctl` runs before NetworkManager, so the bridge key is skipped then too (the leading `-`
makes that silent); the daemon will set it itself when it starts managing the bridge in Phase 4 if
the value turns out not to be 0 by default. Observed on the Pi 5 after reboot: proxy_arp is 0
regardless, because that is the kernel default.

## Phase 1

### D-013: The daemon re-asserts `Pairable=false` after registering the agent

Section 8.1 sets `Pairable=false` at start and section 8.2 then registers the agent. On the Pi 5
with BlueZ 5.82 the adapter was `Pairable: yes` with no window open, because BlueZ turns bondable
mode on whenever an agent registers (`adapter_set_io_capability` in `src/adapter.c`). The order in
the plan cannot be fixed by reordering: the agent must be registered before a window opens, and a
re-registration after BlueZ restarts would flip it again. So after every `RegisterAgent` the daemon
sets `Pairable` to whatever the pairing window says and reads it back, failing the attach if it did
not stick. Verified: `bluetoothctl show` reports `Pairable: no` after start and after three BlueZ
restarts, `yes` only while `palm-gwctl pair` is open.

### D-014: `adapter = "auto"` cannot use `Modalias` to spot a dongle (OPEN, plan unchanged)

Section 8.1 and section 16 say `auto` prefers the adapter whose `Adapter1.Modalias` starts with
`usb:`. On the Pi 5 the onboard UART radio reports `Modalias = usb:v1D6Bp0246d0552`. That value is
BlueZ's own Device ID (vendor 0x1D6B is the Linux Foundation, product 0x0246 is BlueZ, the version
is 5.82), set from `DeviceID` in `main.conf`, and every adapter on the host reports the same thing.
It does not describe the hardware, so with a dongle plugged in, `auto` would pick by "lowest hciN"
in practice, not "the dongle". Section 16 is final under kickoff rule 2, so the code still follows
the plan and this is raised with Zoe instead of changed. The proposed fix is to read the bus from
sysfs (`/sys/class/bluetooth/hciN/device` resolves under a `usb` path for a dongle, under the UART
for the onboard radio), which is not device-tree parsing. Nothing in the current rig uses `auto`.

### D-015: Values the managed block overrides are parked in `main.conf`, not in install-info.json

Section 8.1 says uninstall "restores the original values it recorded in `install-info.json`".
Instead, a live key that the block overrides (for example a user's own `Name = My Pi`) is
commented out in place as `#palm-gw-disabled: Name = My Pi`, and removing the block uncomments it.
This keeps the original next to where it came from, works the same for the Phase 6 `.deb` (which
has no install-info.json of its own until setup runs), and is tested to round-trip byte for byte.
`install.sh` additionally keeps a copy at `/var/lib/palm-gw/main.conf.orig` on first install.
On the Pi 5 the stock Trixie file has no live keys in those sections, so nothing was parked, and
stripping the block reproduced the original file exactly (`cmp` against `main.conf.orig`).

### D-016: Agent `Release` does not re-register by itself

Section 8.2 says on `Release` "the daemon will re-register on the next BlueZ appearance". The first
Pi run re-registered from the `Release` handler as well, which raced the main loop: the main loop's
own `RegisterAgent` then failed with `org.bluez.Error.AlreadyExists` on every retry. Now `Release`
only marks the agent unregistered; the main loop re-registers on `bluez_found`. If BlueZ releases
the agent and is still running 3 s later in the same session (another agent took over), the loop
re-attaches. `RegisterAgent` returning `AlreadyExists` is treated as success.

### D-017: Phase 1 pairing acceptance ran against a real Palm TX, not a laptop

Section 10's Phase 1 test says to pair "from a laptop" with `bluetoothctl`. No Linux laptop was
available, and the Mac's terminal lacked Bluetooth permission, so the user paired a Palm TX by
hand while the Pi recorded `btmon`. This is a stronger test of what matters: the Palm uses legacy
PIN pairing (`RequestPinCode`), which a modern laptop would replace with Secure Simple Pairing.
What it did not exercise is the SSP path (`RequestConfirmation`), which only modern clients use;
that path has unit coverage only until a laptop or phone pairs in a later phase.

## Hardware observations

### Test rig (kickoff rule 4), 2026-11-08

- **Palm TX** (address 00:07:E0:3C:5E:FA, class 0x100114), added 2026-11-09. Pairs with a
  legacy PIN. Its network wizard will not accept a blank user name and password.
- **Pi 5 Model B Rev 1.0**, 8 GB, booting from a 64 GB card, Raspberry Pi OS Lite 64-bit Trixie
  (Debian 13.7), kernel 6.18.50+rpt-rpi-2712. `ssh sysadmin@10.4.0.241`, key auth, passwordless
  sudo. Hostname `piblueap`.
- Versions found: BlueZ 5.82, ppp 2.5.2, dnsmasq 2.91, nftables 1.1.3, NetworkManager 1.52.1,
  Go 1.24.4 from apt. All at or above section 4's "expected" column.
- Uplink `eth0` (10.4.0.241/23 via DHCP, also has a global IPv6 address). `wlan0` present and
  down; Wi-Fi uplink is to be tested too per Zoe. Home LAN 10.4.0.0/23 does not overlap
  10.42.0.0/24.
- Onboard adapter `2C:CF:67:2F:29:F5`, shown by `bluetoothctl list`. A USB dongle is available
  on request but is not plugged in.
- `cmdline.txt` has `console=serial0,115200`. On a Pi 5 the debug UART is a separate connector,
  so this is not a conflict (section 3); the installer's warning is Pi 4 only.
- **No Pi 4 is available.** Every phase will record Pi 4 as untested until one appears, as
  section 10 allows.
- Palm models and whether a Linux laptop exists for `fake-palm`: not yet known.
- With the managed block applied and no profiles registered yet, BlueZ advertises
  **Class 0x400300**: major/minor 0x000300 from `main.conf` as intended, plus service bit 22
  (Telephony), which BlueZ sets because its built-in SIM Access plugin registers SAP. The
  Networking bit (0x020000) appears only once the LAP/NAP records are registered in Phases 2 and 4.
  Whether the Telephony bit bothers any Palm's device picker is a hardware observation to make
  when a Palm searches; if it does, the fix is disabling the `sap` plugin, recorded here first.
- The adapter `Name` property stays `piblueap` (BlueZ's hostname plugin sets it); the name a
  Palm sees is `Alias`, which the daemon sets to "Palm Gateway". Confirmed by `bluetoothctl show`.
- `golangci-lint` is not packaged in Trixie. Its release install script (`curl | sh`) was not run
  on the Pi; it is being built from source with `go install` instead. Lint is authoritative on the
  development machine and in CI either way.
