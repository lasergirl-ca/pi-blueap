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
depends on it. Decided by D-042.

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

### D-014: `adapter = "auto"` spots a dongle by its sysfs bus, not `Modalias` (section 16 amended)

Section 8.1 and section 16 say `auto` prefers the adapter whose `Adapter1.Modalias` starts with
`usb:`. On the Pi 5 the onboard UART radio reports `Modalias = usb:v1D6Bp0246d0552`. That value is
BlueZ's own Device ID (vendor 0x1D6B is the Linux Foundation, product 0x0246 is BlueZ, the version
is 5.82), set from `DeviceID` in `main.conf`, and every adapter on the host reports the same thing.
It does not describe the hardware, so with a dongle plugged in, `auto` would pick by "lowest hciN"
in practice, not "the dongle". Section 16 is final under kickoff rule 2, so the code still follows
the plan and this is raised with Zoe instead of changed. The proposed fix is to read the bus from
sysfs (`/sys/class/bluetooth/hciN/device` resolves under a `usb` path for a dongle, under the UART
for the onboard radio), which is not device-tree parsing. Nothing in the current rig uses `auto`.

Resolved 2026-11-09: Zoe approved the change. `bluez.HWBus` reads
`/sys/class/bluetooth/hciN/device/subsystem` (`usb` for a dongle, `serial` for the Pi 5's onboard
radio), `auto` prefers `usb`, and `status` and the `adapter configured` log line show the bus.
PLAN.md sections 8.1, 8.11 and 16 are amended to match.

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

## Phase 2

### D-018: Added scope: WireGuard client uplink in the last phase

Zoe asked on 2026-11-09 for the ability to set up a WireGuard client on the gateway and connect
the Palms and other devices through a NAT to a WireGuard concentrator. This is not in PLAN.md. It
is recorded as added scope for the last phase (Phase 8, alongside the release work and the Pi
images from D-002) so that Phases 2 to 7 keep their order. Expected shape, to be confirmed with
Zoe before building: a `[wireguard]` config table, a `wg0` interface managed by the gateway,
Palm-side traffic (PPP and PAN) policy-routed into the tunnel and masqueraded there instead of on
`uplink`, and setup and status support in the wizard, `palm-gwctl` and the web UI. Open
questions for then: full tunnel or split, behaviour when the tunnel is down (fail closed or fall
back to the uplink), and whether the Pi's own traffic uses the tunnel. A design sketch with the
full list of open questions is in docs/WIREGUARD.md (2026-11-09); nothing is built yet.

### D-019: `NewConnection` checks the allowlist and the adapter itself

Section 8.2 puts allowlist enforcement in `AuthorizeService`, and the profiles register with
`RequireAuthorization=true`. BlueZ skips `AuthorizeService` for a device marked `Trusted`, which
every device paired in a window is (`trust_on_pair`), so a device that was forgotten from the
allowlist but is still trusted in BlueZ would get through. `NewConnection` therefore also refuses
a device that is not allowed (`session_refused reason=not_allowed`), and refuses connections that
arrived on another adapter (`reason=other_adapter`). The daemon's device-change handler likewise
ignores devices under other adapters, so a test dongle on the same host cannot add entries.

### D-020: The orphan tripwire reads `/proc` and matches `linkname palm-`

Section 8.5 says the reaper cross-checks `pgrep -x pppd` against the table. A test client's pppd
on the same host (fake-palm) would then always look like an orphan. The reaper instead reads
`/proc/*/comm` and `cmdline`, counts only pppd processes carrying the gateway's own
`linkname palm-<n>`, and compares their process group with the sessions' process groups (the
`notty` shunt shares pppd's group). No subprocess is spawned every 10 s.

### D-021: pppd's stderr is an `os.Pipe`, not `cmd.StderrPipe`

Section 8.5 implies reading stderr until EOF and then calling `Wait`. If anything else in pppd's
process group outlived pppd, it would hold the pipe open and `Wait` would never run, leaking the
session and its address. The daemon now waits for pppd first, SIGKILLs the rest of the group,
then drains the log with a 2 s cap. Found by a unit test with a fake pppd that exits while its
child lives on.

### D-022: `docs/PALM-SETUP.md` drafted in Phase 2, not Phase 8

Section 10 puts `PALM-SETUP.md` in Phase 8. A first draft was written on 2026-11-09 while waiting
for a USB Bluetooth adapter, so the Palm TX findings went in while fresh: the "PC" connection uses
LAN Access with no `CLIENT` exchange, the Palm sometimes misses the gateway on one search, and
the T|X will not save a network service with a blank user name and password. Steps not yet
tried on hardware are marked "(not yet tested)". Phase 8 still owns the finished version
(screen text or photos from a T|X and a LifeDrive, per section 10).

### D-023: DUN waits `at_timeout_ms` (10 s) for the first AT command, not `ms_timeout_ms`

Section 8.4 uses one deadline, `ms_timeout_ms` (3 s), for every profile before starting pppd
blind. On 2026-11-09 a Palm T|X dialling the DUN channel ("Connect to: Modem") sent its first
command, `ATQ0V1Z`, 3.5 s after the RFCOMM channel opened, in all three attempts. By then pppd
was already running and sending LCP frames, so the Palm never got its `OK`, retried with `+++`
and `ATQ0H0`, and reported "modem not found (0x1106)". A DUN terminal always speaks first, so
nothing is lost by waiting longer on that channel. A new key, `handshake.at_timeout_ms`
(default 10000), sets the first wait on the DUN channel when `at_emulator` is on. SPP and LAP
keep `ms_timeout_ms`. PLAN.md section 7 and the 8.4 pseudocode are amended to match.

### D-024: The AT emulator parses the whole command line to find the dial

Section 8.4 says a line that matches `^ATD` is a dial. One AT command line can carry several
commands, and a Palm T|X dials with `ATQ0E0&D0&C1V1S7=90L1DT0`: the dial `DT0` comes last. On
2026-11-09, with D-023 deployed, the gateway answered that line with `OK`, waited for a dial
that had already come, and timed out three times. The emulator now walks the line as V.250
basic syntax: `&D0`, `S7=90` and similar settings are skipped, and a `D` command dials and takes
the rest of the line. An `H` command anywhere in a line with no dial still hangs up (the T|X sends
`ATQ0H0` when it gives up). The 8.4 pseudocode is amended to match.

### D-025: Dial-up from a Palm T|X parked until Phase 5

After D-023 and D-024 the T|X dials and gets `CONNECT`, but never starts PPP: it stays at
"Connecting" and its View Log is empty (TESTING.md, Phase 2). The leading suspect is carrier
detect. The Palm sends `&C1` (DCD follows carrier), and an incoming RFCOMM channel on Linux
reports DV (DCD) as on from the first modem status message, so DCD never rises at `CONNECT`.
Testing that means handing the DUN channel to an RFCOMM TTY (`RFCOMMCREATEDEV` with
`RFCOMM_REUSE_DLC` on the socket, then `TIOCMBIC`/`TIOCMBIS` `TIOCM_CD` on `/dev/rfcommN`, with
pppd on the TTY). That changes how pppd attaches on one channel for a guess, so it is parked as
a Phase 5 hardening item rather than done now. Phase 2 acceptance does not depend on it: the
real-Palm check uses the "PC" connection, which works over LAN Access, and DUN acceptance is
`fake-palm --mode at`. `docs/PALM-SETUP.md` says Modem does not work on a T|X yet. Section 10's
Phase 5 list gains the item.

### D-026: Web UI also on the Bluetooth side, read-only (amends section 2)

Section 2 says the web UI listens on the LAN only, never on `br-palm` or `ppp*`. On 2026-11-09 Zoe
asked for the UI to be reachable from the LAN or from inside the Bluetooth network, with a
username and password. Because section 2 is final, the conflict was put to her first, with the
risk: Palms cannot use modern TLS in a browser, the radio uses legacy PIN pairing, and a
captured admin password could open the firewall to the home LAN. She chose a read-only
Bluetooth side. Palms use a native Palm OS app (`.prc`) against a small read API, and laptops
get the normal htmx UI. The full UI, including pairing, forget, firewall and settings, stays
LAN-only. A second listener on the gateway address has only the read-only routes registered
(the mutating ones 404 there), the firewall accepts 8042 to the gateway address only, and the
admin account gains a username. It is built in Phase 7 in phase order, and design-only for now.
Sections 2, 7, 8.7, 8.12, 13 and the Phase 7 acceptance test are amended. The design and five
open questions (the app, admin versus viewer login on the Bluetooth side, TLS in the app, what
other devices are visible, more than one admin) are in `docs/WEB-UI.md`.

### D-027: fake-palm runs on the gateway itself, with a second adapter and a network namespace

Section 9 assumes `fake-palm` runs on a separate Linux machine. There is none here (the Mac
cannot use Bluetooth from a terminal), so it runs on the Pi with its own USB adapter (`hci1`)
while the gateway keeps its configured `hci0`. Both share one bluetoothd. fake-palm registers
its own agent for its `Pair` call, and the daemon ignores devices under other adapters. The
client pppd runs in a network namespace (`--netns`), so its `pppN`, address and default route do
not collide with the gateway's. RFCOMM sockets work only in the initial namespace, so the socket
is opened there and handed to pppd, and the probes re-run inside the namespace. Air-side
behaviour is the same as with two machines, since the two controllers still talk over the
radio. `scripts/phase2-accept.sh`, a test helper not in section 6's layout, runs the whole
Phase 2 fake-palm acceptance this way and saves its output. fake-palm also gains
`--mode at-tx`, which replays the Palm T|X's dial-up sequence and timing (D-023, D-024).
With `--netns` the client pppd runs without section 9's `usepeerdns`. A namespace shares `/etc`,
so Debian's `/etc/ppp/ip-up.d/0000usepeerdns` rewrote the Pi's own `/etc/resolv.conf` to
`nameserver 10.42.0.1` for as long as fake-palm was connected. dnsmasq then ignored its own
address, had no upstream, and answered SERVFAIL (first run, 2026-11-10). The probe resolves
through the gateway address explicitly, so it tests the same thing without the option. The
script also unblocks `hci1` if rfkill has it soft-blocked: systemd-rfkill restores a saved
block per USB port, and the UB500 arrived blocked. fake-palm gains `--url` (default section 9's
`http://neverssl.com/`) because that host was dropping half its TCP connections from the Pi's
own uplink on 2026-11-10 while other sites answered in 10 ms; the script passes `$URL`.

### D-028: the daemon shuts the RFCOMM socket down when a session ends

Section 8.3 step 1 says BlueZ closes its own copy of the descriptor when `NewConnection` returns,
and section 8.5 has the daemon close its copy once pppd has inherited it. On the Pi (BlueZ 5.82)
bluetoothd keeps its copy for the life of the link: `/proc/<bluetoothd>/fd/40` was the session's
socket, and its receive queue grew after the session ended. So when the gateway ended a session
(`palm-gwctl disconnect`, forget, a handshake timeout or `ATH`), pppd exited and the session left
the table, but the RFCOMM channel stayed up, and fake-palm's pppd held its link for the full
60 s instead of seeing a hangup (2026-11-10). A real Palm would have kept showing "connected".
pppd does not send LCP Terminate-Request either, because the group SIGTERM kills its `notty`
shunt first. Now the daemon keeps its copy of the socket until the session ends and then calls
`shutdown(SHUT_RDWR)`, which acts on the socket itself whoever else holds it, before closing
(`ppp.HangUp`). The refusal path uses it too. Signals and the 3 s teardown are unchanged.
`scripts/phase2-accept.sh` now also checks that fake-palm sees the link drop.

### D-029: `palm_to_pi` fills a `pi_allow` set; the fixed input rules stay

Section 7 has `firewall.palm_to_pi` ("what Palms may send to the Pi itself") and Phase 2's test
note says to allow a test web server "in `palm_to_pi` temporarily", but section 8.7's `palm_input`
chain hard-codes DNS (udp and tcp 53), DHCP (udp 67), port 80 and ICMP and never reads the key.
The template now declares a third daemon-managed set, `pi_allow` (`inet_proto . inet_service`),
and `palm_input` accepts `meta l4proto . th dport @pi_allow` just before its drops. The daemon fills
it from `palm_to_pi`. The fixed rules stay, because DNS, DHCP and the status page are what the
gateway is for, and removing `udp/53` from the list should not break every Palm. So the key adds
services, it does not remove the fixed ones, and its default (`udp/53`, `udp/67`, `icmp`) only
repeats them. `icmp` in the list is covered by the fixed ICMP rule. The comment in section 7 and
`config.example.toml` says so.

### D-030: `reload` applies the live settings and lists the rest; templates are embedded

Section 8.8 has `reload` re-read the configuration, re-apply the sets, rewrite dnsmasq, and
"re-register profiles if channels changed". Swapping the whole configuration under running
sessions would half-apply changes such as the subnet or uplink, which are also rendered into the
nftables table and the bridge at install time. `reload` therefore copies only the live settings onto
the running configuration:
- `[firewall]` (the three sets)
- `network.dns_upstream` and `network.dhcp_pool` (dnsmasq is re-rendered, and restarted only if
  the file changed)
- `bluetooth.allow` and `allow_any`
- the pairing settings the daemon reads per use (`pin`, `pin_length`,
  `pair_window_default_minutes`, `trust_on_pair`)

Every other changed key is listed in the reply as needing a `palm-gwd` restart, including profile
channels. Re-registering profiles on reload is left for Phase 5's robustness work. The merged
configuration is validated before anything is applied, and the daemon swaps it atomically, so
running goroutines never see a half-updated one. To render dnsmasq at run time the daemon needs
the template, so a small `deploy` Go package embeds `deploy/dnsmasq/palm-gw.conf.tmpl` and the
nftables template (section 6 has no Go file in `deploy/`). The daemon also writes the dnsmasq file
at start, so an edit followed by a restart is not lost. It re-applies the sets every 30 s, because
`systemctl reload nftables` reloads the table from disk and empties them. That is logged only
when the outcome changes.

### D-031: Phase 3's LAN scan targets a stand-in LAN host on the Pi

Phase 3's acceptance runs `nmap -sT -Pn 192.168.1.0/24 -p 22,80,443 -e pppN` from the fake-Palm
side against the real LAN. Zoe's LAN is 10.4.0.0/23, and scanning it from the development
session was not approved (the session's permission check refused a scan of the home network).
`scripts/phase3-accept.sh` therefore puts a stand-in LAN host in its own network namespace on the
Pi (`192.168.77.2` behind a veth pair, with listeners on 22, 80 and 443). Traffic from the Palm
side to it takes the same path as to a real LAN host (`palm_forward`, the RFC 1918 drop, the
`lan_allow` set), so the isolation and the allow-and-reload checks are the same. The script also
scans the Pi's own LAN address from the Palm side. A scan of the real LAN can be added with
Zoe's go-ahead.

### D-032: PAN clients are checked against the allowlist by the daemon too

Section 8.6 relies on `AuthorizeService` to apply the allowlist to PANU clients. As with RFCOMM
(D-019), BlueZ skips that call for a device marked `Trusted`, which every device paired in a
window is, and the NAP server has no `NewConnection`-style hook where the daemon could check
again. Three things close the gap. (1) When the daemon attaches to BlueZ and after every
`reload`, it clears `Trusted` on any device under its adapter that the allowlist no longer
accepts (event `untrusted`), so BlueZ asks the agent, which refuses, before the connection is set
up. Such a device exists only if the allowlist changed behind BlueZ's back (a static entry removed
from config.toml, or the state file edited); `forget` already removes the device from BlueZ.
(2) As a backstop, a loop reads the kernel's BNEP connection list (`BNEPGETCONNLIST`) every
second, logs `pan_connected`/`pan_lease`/`pan_disconnected`, and drops a server-side connection
from a device that is not allowed (`pan_refused`). Only connections with the NAP role (0x1116)
count: on a host running fake-palm the kernel list also holds fake-palm's own outgoing PANU
connection. (3) Ending a PAN connection takes two steps. `BNEPCONNDEL` stops the traffic and
removes `bnepN`, but bluetoothd keeps its own descriptor for the L2CAP socket (the PAN side of
D-028), so the channel stays open and the client still believes it is connected. The daemon
therefore also calls `Device1.Disconnect` on the device, which drops its Bluetooth link and with
it any RFCOMM session it has. `palm-gwctl disconnect` and a `pan_refused` do this; `forget`
removes the device anyway. Also observed: BlueZ's network server authorizes an incoming PAN
connection with the BNEP UUID `0000000f-…`, not NAP's `00001116-…` as section 8.6 says.

### D-033: Phase 4 accepted on fake-palm alone; the Android check moves to Phase 5

Phase 4's acceptance includes an Android phone joining the NAP. Zoe has no Android device, and
macOS 27 no longer offers a Bluetooth PAN client, so Zoe chose to accept Phase 4 on fake-palm
alone (BlueZ as the PANU, on its own adapter, with SSP pairing in a window). The real-client check
(an Android phone, or another non-BlueZ PANU such as Windows) stays in the section 14 test matrix
and is to be run during Phase 5 if a device is available, with the result in docs/TESTING.md.

### D-034: PAN connections survive a `palm-gwd` restart; PPP sessions do not

PLAN.md 8.6 has the daemon `Unregister("nap")` on shutdown, and Phase 5 restarts the daemon
mid-session. Unregistering the NAP server does not end BlueZ's existing PAN sessions: the kernel
BNEP session and bluetoothd's L2CAP socket stay, `bnepN` stays on the bridge, and traffic keeps
flowing. The new daemon finds the connection in the kernel's BNEP list at start, checks it
against the allowlist like any other, and lists it (`pan_connected` at start). PPP sessions are
the daemon's children and end with it (systemd stops the unit's control group). The daemon does
not drop PAN clients on purpose at shutdown: a restart (an upgrade, a `reload` that needs one)
should disturb as little as possible, and a PANU that is not wanted is still refused by the new
daemon. `scripts/phase5-robust.sh` checks that the connection carries on and still passes traffic.

### D-035: `[bluetooth].adapter` may be the adapter's Bluetooth address

Section 7 allows `"hciN"` or `"auto"`. On the Pi 5 with the onboard radio and a USB dongle, the
kernel numbered them differently on two boots of the same day (onboard `hci0` and dongle `hci1`,
then the other way round), so `adapter = "hci0"` moved the gateway onto the dongle after a
reboot, where the Palm's pairing is not. `adapter` now also takes the adapter's address
(`"2C:CF:67:2F:29:F5"`), matched against `Adapter1.Address`; `status` shows `configured by
address`. With one adapter, the common case and dongle mode with the onboard radio disabled,
`hci0` stays stable and remains the installer's default. The test rig's config is pinned by
address. The acceptance scripts now pick fake-palm's adapter as whichever one the gateway is not
using, instead of assuming `hci1`.

### D-036: Carrier detect cannot be driven from user space; T|X dial-up stays unsupported

D-025 parked T|X dial-up on the theory that the Palm waits for carrier (RFCOMM DV) to rise at
`CONNECT`, and proposed moving the DUN channel onto an RFCOMM TTY to hold DV off until the dial.
Built and tried on 2026-11-10: the TTY works (`RFCOMMCREATEDEV` with `RFCOMM_REUSE_DLC`, which
needs the socket's addresses and channel in the request because the kernel checks the channel
again when the TTY opens), and the AT emulator and pppd run on it. But the kernel's generic
`tty_tiocmset` masks every `TIOCMSET`/`TIOCMBIS`/`TIOCMBIC` request down to the output lines DTR
and RTS (plus OUT1, OUT2, LOOP) before the RFCOMM driver sees it, so `TIOCM_CD` never reaches
`rfcomm_tty_tiocmset` and DV stays on. fake-palm's `--dcd` check (an RFCOMM TTY on the client side
reading `TIOCMGET`) confirmed carrier on before the dial. The RFCOMM socket API has no way to set
the V.24 signals either; the kernel sets RTC, RTR and DV when it allocates the DLC. So the
D-025 theory cannot be tested without a kernel change, and the TTY code was removed rather than
kept unused. T|X dial-up (Connect to: Modem) stays unsupported; the T|X uses the "PC" connection
over LAN Access, which works. DUN remains for clients that do not wait for a DV transition
(fake-palm `--mode at`, and possibly other Palms; to be tried with the LifeDrive if one turns up).

### D-037: The managed block sets `ReverseServiceDiscovery = false`

PLAN.md section 8.1 lists the keys in the `main.conf` managed block; this adds one. Found with the
real T|X on 2026-11-10: after any `bluetoothd` restart (a reboot, `systemctl restart bluetooth`,
`make install`, `class toggle`), a paired, trusted Palm stuck on "Initializing" on every
Connect until it was paired again. btmon showed the Palm's SABM on the LAP channel, the link
authenticated and encrypted, and then no UA or DM until the Palm gave up 29 s later.
`bluetoothd`'s debug log stopped at `ext_confirm() ... authorizing connection`. Our agent was
never asked, and `palm-gwd` never got `NewConnection`.

The cause is in BlueZ (5.82 and 5.66 alike). For a trusted device, `adapter_authorize` queues the
grant behind `device_wait_for_svc_complete`, which waits for the device's SDP services to be
resolved. They count as resolved after a restart only if the stored `info` file lists
`Services=`, and a Palm's SDP record holds nothing BlueZ stores, so the T|X's file has none.
When they are not resolved, the wait is only released by a browse. BlueZ schedules a reverse
browse only from `device_bonding_complete`, so a plain reconnect never gets one and the grant
never comes. Pairing again runs the browse, which is why re-pairing "fixed" it until the next
restart. fake-palm never hit it, because its adapter's record has services stored.

`ReverseServiceDiscovery = false` makes `device_wait_for_svc_complete` release at once. Its
other effects:
- BlueZ no longer browses SDP records of devices that pair with us. The gateway only serves
  profiles and never connects out to a Palm's services, so nothing uses those records.
- BlueZ skips the GATT client for LE devices that connect to the Pi. LE devices the Pi itself
  connects to, such as a keyboard, keep their GATT client.

Side effect found the same evening: BlueZ then never signals `Paired` for a Palm, so the daemon
also watches `Bonded` (D-040).

Alternatives not taken:
- Having `palm-gwd` start a browse with `Device1.Connect` when an allowlisted device connects
  while `ServicesResolved` is false: it races the Palm's own timeout, and `Connect` would also
  try outgoing profile connections.
- Writing `Services=` into BlueZ's storage files.

### D-038: The 24-hour soak is deferred to field use

PLAN.md section 10, Phase 5 lists a 24-hour soak with a Palm connected. On 2026-11-10 Zoe decided
to skip it on the bench and get it in the field instead, by using the gateway day to day with the
T|X. The rest of Phase 5's accept criteria for the real T|X were met that day:
- 23 of 23 toggles connected on the first tap.
- BlueZ restart, `palm-gwd` restart and power cut each reconnected on the first tap.
- fds stayed at the baseline and no pppd was orphaned.

The `palm-test-soak` logger was started at 20:03 and stopped at 20:05 without results. Anything a
long session turns up in the field (a dropped link that does not come back on the first tap,
growing fds or RSS in `palm-gwctl status` and `/proc`) goes into TESTING.md and a new decision.

### D-039: The Palm app gets full admin over TLS; the Blazer page becomes an HTML5 page (amends D-026 and section 2)

D-026 made the Bluetooth side read-only, because a Palm's radio link uses legacy PIN pairing and
cannot be trusted to keep a password secret. On 2026-11-10 Zoe asked for a native Palm app,
ARM only, whose username and password grant admin access, instead of a special Palm web page.
Because this reopens a section 2 decision, the risk was put to her first, with options from
full admin down to read-only.

She chose:
- **Full admin from the app.** Pairing windows, with the PIN shown on the Palm. Disconnect,
  forget and rename. Firewall allow rules. Settings.
- **TLS 1.3 only on the Bluetooth listener.** The app uses her wolfSSL port and pins the
  certificate fingerprint. `bluetooth_plain_http` is removed: the admin password must never
  cross the radio in the clear. End-to-end TLS is what makes the weak radio link acceptable,
  which removes the reason for D-026's read-only rule.
- **The app lives in this repository**, under `palm-app/`. It is ARM only, so Palm OS 5 devices
  such as the T|X, LifeDrive and Tungsten T3/T5. Palm OS 4 devices get no admin interface, and
  the LAN UI remains for everyone. Zoe has a Palm development how-to to hand over when Phase 7
  starts.
- **The plain `/palm` page on port 80** is no longer HTML 3.2 for Blazer. It becomes a small
  HTML5 page for Linux laptops on PAN: still no login, still only "you are connected", the
  addresses, and a link to `https://10.42.0.1:8042/`.

Consequences:
- The Bluetooth listener registers the same routes as the LAN listener, so a laptop on PAN
  logging in over HTTPS also gets full admin. This is Claude's reading of Zoe's answers (TLS is
  what makes admin acceptable there too), to be confirmed before Phase 7.
- Actions that restart Bluetooth (class toggle, some settings) end the Palm's own link. The app
  and UI warn before them.
- The mutating API mirrors `palm-gwctl`.
- The table-driven "mutating routes 404 on the Bluetooth mux" test becomes "the Bluetooth
  listener refuses plain HTTP and requires a login for every route".

Still built in Phase 7, in phase order. Sections 2, 7, 8.7, 8.12, 13 and the Phase 7 acceptance
test are amended. The design is in `docs/WEB-UI.md`.

### D-040: A pairing is detected from `Bonded` as well as `Paired`

Found on 2026-11-10 in the dongle-mode test, as a consequence of D-037. With
`ReverseServiceDiscovery = false`, a new Palm paired correctly in BlueZ (`Paired`, `Bonded`,
link key stored), but `palm-gwd` never logged `paired`. So the device was not allowlisted or
trusted, the `--one` window did not close, and the Palm's next Connect was refused
(`service_rejected`). The first dongle pairing only got through because the T|X was still on
the allowlist from the onboard pairing.

A `busctl monitor org.bluez` capture of the pairing showed `PropertiesChanged` with
`Bonded: true`, then `Connected: false`, and no `Paired` signal at all, although `GetAll`
answered `Paired: true` a moment later. BlueZ's `device_set_paired` (5.66 and 5.82 alike) holds
the `Paired` signal back while the device's services are unresolved (`pending_paired`) and sends
it only when an SDP browse completes. With reverse service discovery off, a Palm is never
browsed, so the signal never comes. `Bonded` is signalled at once.

The daemon now treats `Bonded` turning true the same as `Paired`:
- **Each pairing is handled once**, per device path. A device whose services do get resolved,
  such as a laptop, can report both `Bonded` and `Paired`.
- **The record is cleared** when `Paired` or `Bonded` turns false, or when BlueZ removes the
  device (`InterfacesRemoved`, which covers `forget`), so a later re-pair counts again.
- **`InterfacesAdded` for a device that is already connected is handled too.** BlueZ's gdbus
  layer suppresses `PropertiesChanged` until a new object has been announced, and before the
  fix the first `device_connected` of a never-seen Palm went unlogged. A device announced while
  not connected is ignored. That covers devices found by an inquiry, and every stored device
  after a `bluetoothd` restart, which would otherwise look like a pairing outside a window.

Verified on the T|X with the gateway on the dongle and the T|X forgotten first. `paired`
(trusted) came 0.12 s after `pin_requested`, the window closed, and the session came up on the
first Connect. Unit tests cover every signal shape.

### D-041: Phase 6 starts while Phase 5 waits for a Pi 4

The kickoff rules say to work the section 10 phases strictly in order, and Phase 5's accept line
asks that "both Pi 4 and Pi 5 pass identically". On 2026-11-10 every Phase 5 item on the Pi 5
was done:
- the fake-palm robustness runs and the reboots;
- the Wi-Fi uplink;
- the real T|X toggles, BlueZ restart, `palm-gwd` restart and power cut;
- dongle mode.

The 24-hour soak was deferred to field use (D-038). No Pi 4 is available yet. Zoe chose to start
Phase 6 rather than wait. Phase 5 stays open: when the Pi 4 arrives, the Pi 4 half of Phase 5
(robustness script, T|X runs, dongle mode, PLAN.md section 3's Pi 4 notes) is run and recorded
before Phase 8's release, along with Phase 6's own Pi 4 acceptance. Phase 6 work does not
change anything Phase 5 accepted without re-running the affected Phase 5 checks on the Pi 5.

### D-042: Static files by install layout; config files by `palm-gw setup --apply`

Settles D-010 (unit paths for a source install versus the `.deb`) and moves the installer's
system-file work from shell into Go (`internal/setup`). Section 8.11 has the wizard render
`deploy/` itself, and the `.deb` has to do the same in postinst, so the two now share one code
path. The files split by what they depend on:

- **Static files, which depend only on the install layout:**
  - the three units;
  - `nm-bridge.sh`, moved from `scripts/` to `deploy/lib/` so it can be embedded;
  - NetworkManager's unmanaged list;
  - the modules-load list;
  - the pppd options.

  They are text templates with `{{BIN_DIR}}` and `{{LIB_DIR}}`.
  - The **source** layout uses `/usr/local/bin`, `/usr/local/lib/palm-gw`,
    `/etc/systemd/system`, `/etc/NetworkManager/conf.d` and `/etc/modules-load.d`.
  - The **deb** layout uses `/usr/bin`, `/usr/lib/palm-gw`, `/usr/lib/systemd/system`,
    `/usr/lib/NetworkManager/conf.d` and `/usr/lib/modules-load.d`. systemd, NetworkManager
    and modules-load all read those directories, so the package owns the files outright.
  - `/etc/ppp/options.palm` is the same in both layouts, and a conffile in the `.deb`.
  - `palm-gw setup --install-files --layout source|deb [--root DIR]` writes them. `install.sh`
    uses it with the source layout, and `make deb` will use it with the deb layout into a
    staging tree.
- **Configuration files, which depend on `config.toml`:**
  - `/etc/palm-gw/dnsmasq.conf`;
  - `/etc/nftables.d/palm-gw.nft` and the include line in `/etc/nftables.conf`;
  - `/etc/sysctl.d/90-palm-gw.conf`;
  - the `main.conf` block;
  - `dtoverlay=disable-bt` in `config.txt`;
  - a new `/etc/palm-gw/bridge.env`.

  `palm-gw setup --apply` renders them, checks the firewall with `nft -c`, and starts the units
  in order: bridge, then dnsmasq, then sysctl, then `palm-gwd`. The bridge unit now reads
  `bridge.env` (`EnvironmentFile=`) instead of having the bridge, gateway and prefix rendered
  into its `ExecStart`, so the unit is static and can ship in the package. The apply step
  restarts the bridge only when `bridge.env` changed, and dnsmasq only when its configuration
  changed, because restarting the bridge drops PAN clients. It ends with detection's warnings
  (Phase 6 part 1).
- **`palm-gw setup --remove [--purge]`** undoes both:
  - it stops the units, deletes the bridge connection, and strips the `main.conf` block and the
    nftables include;
  - it removes `disable-bt` only if palm-gw added it;
  - it deletes every path in `/var/lib/palm-gw/installed-files.txt`.

  `config.toml` and `state.json` stay unless `--purge` is given. `install-info.json` also stays,
  with `added_disable_bt` cleared, so a reinstall restores the adapter mode. `make uninstall`
  calls it.
- **The adapter mode is now two-way.** `make install` without `ADAPTER=` keeps the current mode
  (previously the default `onboard` was a no-op). `--adapter-mode onboard` takes out a
  `disable-bt` line palm-gw added, and warns about one it did not add.

`palm-gwd -render` and `-main-conf` (D-003) stay for compatibility, but the installer no longer
uses them. Verified on the Pi 5 in dongle mode:
- a reinstall rewrote only the changed files and kept dongle mode;
- `make uninstall` removed every placed file and left `main.conf` byte-for-byte equal to
  `main.conf.orig`;
- a reinstall afterwards restored dongle mode, with `config.toml` and `state.json` unchanged
  and the T|X still paired.

### D-043: The .deb: what postinst, prerm and postrm do

Section 8.11's Path B, built with nfpm (`packaging/nfpm.yaml`, `make deb`, nfpm v2.47.0 run with
`go run`, so nothing is installed on the development machine). Decisions beyond the plan:

**Contents**
- The binaries go in `/usr/bin`, and `fake-palm` in `/usr/libexec/palm-gw/` (section 8.11
  allows either; one package is simpler).
- The static files are rendered for the deb layout into `dist/stage` by
  `palm-gw setup --install-files --layout deb` (D-042), and the package owns them.
- `/etc/ppp/options.palm` is the only conffile.
- Documentation goes in `/usr/share/doc/palm-gw/`, with `LICENSE` as `copyright`.

**Version**
- A tagged commit `v1.2.3` builds `1.2.3`.
- Anything else builds `0.0.0~git<commit time>.<sha>`, which sorts by commit time, so each
  snapshot upgrades the last.

**postinst (configure)**
- Creates the `palm-gw` group, adds `SUDO_USER` to it, and creates `/var/lib/palm-gw`.
- Without a config it prints "Run 'sudo palm-gw setup' to finish" and enables nothing. This
  differs from section 8.11, which has postinst enable the units. Setup enables them through
  `--apply`. The bridge and dnsmasq units now carry `ConditionPathExists=` on their rendered
  files, like `palm-gwd` on `config.toml`, so nothing fails at boot before setup.
- With a config (an upgrade, or a reinstall after `apt remove`), it runs
  `palm-gw setup --apply`, so the gateway is running again when apt returns.
- A failed apply is reported but does not fail the install, so dpkg never leaves the package
  half-configured.
- `systemctl` calls are skipped when systemd is not running, as in the image build's chroot.

**prerm**
- On remove, it runs `palm-gw setup --remove` while the binary still exists.
- On upgrade, it does nothing, because the new postinst re-applies.

**postrm (purge)**
- Deletes `/etc/palm-gw` and `/var/lib/palm-gw`, including the certificate.
- **Deletes the `palm-gw` group.** Debian practice usually leaves system groups behind, but
  the Phase 6 accept line wants `/etc` identical to a pre-install snapshot, and the group lives
  in `/etc/group`.
- BlueZ's pairing records under `/var/lib/bluetooth` are never touched.

**Output under apt**
- bubbletea's package `init()` queries the terminal for its colours whenever stdout is a
  terminal. apt runs maintainer scripts on a pseudo-terminal that never answers, so the query
  showed as junk and waited out its timeout.
- The maintainer scripts therefore capture `palm-gw`'s output and print it afterwards.
  `install.sh` already pipes it.

**Switching an existing source install to the .deb**
- Run `sudo make uninstall` first (config and state are kept). Otherwise the source units in
  `/etc/systemd/system` shadow the package's.

### D-044: Releases, the bootstrap installer and `palm-gw upgrade`

Section 8.11's Path A, `palm-gw upgrade` and `make release`. Decisions beyond the plan:

**Release artefacts** (`make release`, into `dist/release/`)
- Only from a clean tree at a `v*` tag.
- `palm-gw_<tag without v>_arm64.deb`, `install.sh` (the bootstrap script with the tag baked
  in) and `SHA256SUMS` for both.
- The .deb is named after the tag, not the package version: a pre-release tag `v0.6.0-rc1`
  builds package version `0.6.0~rc1` (so it sorts before `0.6.0`), and a `~` has no place in a
  download URL. The installers read the file name from `SHA256SUMS`, so the name is free.
- **`make publish` is separate.** It pushes the tag and runs `gh release create`; the plan has
  `release` do that. Building artefacts should never publish anything by accident.
- A tag with a hyphen is published as a pre-release, which `releases/latest` skips.

**The bootstrap script** (`packaging/bootstrap-install.sh`)
- Refuses non-root, non-Linux, non-aarch64, a 32-bit userland on a 64-bit kernel (dpkg
  architecture not `arm64`), boards other than the Pi 4/400/CM4 and Pi 5/CM5, a missing
  `apt-get`, systemd or `/boot/firmware/config.txt`, and Debian older than 12.
- Fetches from the tag baked in by `make release`; `PALM_GW_VERSION` picks another; a copy run
  from a checkout uses `releases/latest`. `PALM_GW_BASE_URL` (https or file) points at any
  release directory: a mirror, or a local build for testing.
- Runs `apt-get update` first, because a fresh image's lists are stale.
- Reads the terminal for the wizard by redirecting from `/dev/tty`, since stdin is the
  script; apt-get gets `/dev/null` for the same reason. With an existing config, it only
  reports the upgrade (postinst has re-applied it, D-043). With `PALM_GW_UNATTENDED=1` it runs
  `palm-gw setup --yes`, whose answers come from the `PALM_GW_*` variables.
- The whole script is one function called on the last line, so a cut-short download runs
  nothing.
- `--verify-sig` is designed in (minisign over `SHA256SUMS`, the key baked into the script)
  but fails with "this release is not signed" until the project publishes a key.

**`palm-gw upgrade`** (`internal/release`, `cmd/palm-gw/upgrade.go`)
- The latest tag comes from the GitHub API (`/releases/latest`). `--check` needs no root and
  never runs on its own; it is what the web UI's About page will call (section 8.12).
- Versions are compared with `dpkg --compare-versions`, against the installed package, not
  the binary's build string.
- `--from URL` takes a release directory instead of GitHub; Go's HTTP client is given a
  `file://` transport for it. `--version vX.Y.Z` picks a release; `--force` reinstalls.
- The download goes in a world-readable temporary directory, because apt reads a local .deb
  as the `_apt` user.
- A source install (`/usr/local/bin`) is refused with "git pull && sudo make install".
- `palm-gwctl upgrade` (and `palm-gwd ctl upgrade`) exec `palm-gw upgrade` with the same
  arguments.

**CI** now runs shellcheck over the scripts that ship: the bootstrap script, the maintainer
scripts and the bridge helper.

### D-045: The image: stock Pi OS Lite plus the .deb, built in Docker

D-002's `make image`, decided now:

**What the image is**
- The pinned Raspberry Pi OS Lite (64-bit) image (Trixie, 2026-10-06; `PIOS_URL` and
  `PIOS_SHA256` in the Makefile), with the palm-gw `.deb` installed by `apt-get` in a chroot.
  Nothing else changes (but see D-047), so the gateway is **not** configured and nothing of
  palm-gw starts.
- **Cloud-init is left alone.** Trixie images are customised on first boot by cloud-init from
  `user-data` and `network-config` on the boot partition, which Raspberry Pi Imager writes. The
  image keeps those files and services as shipped, so Imager's settings (user, SSH key, Wi-Fi,
  hostname) work as they do for the stock image. D-002's "pre-enables SSH" is therefore done
  by Imager, like on the stock image, instead of being baked in: a baked-in SSH server with no
  user would be useless, and a baked-in user would be a shared default password.
- Until `/etc/palm-gw/config.toml` exists, a login shell prints "Run: sudo palm-gw setup"
  (`/etc/profile.d/palm-gw-setup.sh`). D-002 said setup "runs on first login"; launching a
  root wizard from a login script is too surprising, so it is a reminder instead.
  `/etc/palm-gw-image` records the palm-gw version, the base image and the build time.
- One image for both boards, named `palm-gw-<ver>-rpi-arm64.img.xz` (D-002 had `<board>`),
  with a `.sha256` and a Raspberry Pi Imager repository file (`.json`): one OS entry with
  `init_format: cloudinit-rpi` and the `pi5-64bit`/`pi4-64bit` device tags, as in Raspberry
  Pi's own list, for `rpi-imager --repo palm-gw-….json`.

**How it is built** (`packaging/build-image.sh`)
- On a Linux arm64 host as root; `make image` runs it in a privileged `debian:trixie-slim`
  container under Docker, which on Apple silicon is arm64 without emulation. Work happens in
  the container's own filesystem, not the bind-mounted checkout.
- The image keeps the stock size: its root filesystem has about 430 MB free and palm-gw needs
  30 MB, and Pi OS grows the root filesystem to the card on first boot. `GROW_MB=N` adds room
  if a later base needs it. Partitions are mounted through offset loop devices, since
  containers get no partition device nodes.
- The chroot gets `/proc`, `/sys`, `/dev`, the build host's `resolv.conf` (the image's own is
  put back) and a `policy-rc.d` that refuses service starts; postinst sees no systemd and
  starts nothing (D-043).
- The base already has every dependency (bluez, ppp, dnsmasq-base, nftables,
  NetworkManager), so `apt-get install` installs palm-gw from the stock package
  lists and downloads nothing. Only if that fails are the lists refreshed, and the stock
  lists are put back afterwards, so the image's apt state matches its base.
- Afterwards: `zerofree` on the root filesystem, then `xz -9` (549 MB, against 615 MB with
  `-6`; the stock image is 550 MB). Compressing takes about 12 minutes on the Mac.
- `scripts/image-diff.sh` lists every file on both partitions of the base and the built
  image and prints the differences, which is how the image is checked.

### D-046: palm-gwd clears a soft rfkill block on its adapter

Found on the first fresh flash (Phase 6 part 7): with a USB dongle, `palm-gwd` never came up
("bluez: set hci1.Powered: Failed", then the start timed out) and the wizard stopped before
pairing. The dongle was soft-blocked by rfkill. Raspberry Pi OS ships
`/etc/modprobe.d/rfkill_default.conf` (`options rfkill default_state=0`), so every radio it
has not seen before starts blocked, and `systemd-rfkill` saves that state per port
(`/var/lib/systemd/rfkill/`). The image comes with saved unblocked states for the onboard
Bluetooth of every Pi model only. So a USB Bluetooth dongle starts blocked on any fresh Pi OS
install, in any port, and BlueZ lists it but cannot power it. (Re-binding the dongle with its
saved state removed reproduced it: `soft=1`.) Lite has no `rfkill` command in a user's PATH.

- Before configuring the adapter, `palm-gwd` checks `/sys/class/rfkill` for the adapter's
  switch and, if it is soft-blocked, clears it through `/dev/rfkill` (`internal/rfkill`),
  logs `rfkill_unblocked` as a warning and waits a second, since bluetoothd refuses `Powered`
  for a moment after the unblock. `systemd-rfkill` saves the new state, so the block does not
  come back on the next boot. A hard block is logged as an error and left alone.
- The unit gains `DeviceAllow=/dev/rfkill rw` (section 8.10's sandbox otherwise unchanged; the
  daemon already has `CAP_NET_ADMIN`). `/sys` stays read-only under `ProtectKernelTunables`.
- Only the gateway's own adapter is touched: a radio someone else blocked stays blocked.
- Pi OS's default stays as it is: only the gateway's adapter is unblocked, on any port and
  on any install path (image, `.deb`, source), so the image needs no Bluetooth change.

Also from the same run, for the image's Imager entry: Raspberry Pi Imager 2.0.12 offers
customisation (user, SSH key, hostname, Wi-Fi) only for an OS chosen from a list whose entry
has an `init_format`; an image picked with "Use custom" is written without any. The README
now says to open Imager with the release's `.json`, and the entry copies the base's
`"capabilities": ["rpi_connect"]` from Raspberry Pi's own entry.

### D-047: The image keeps Wi-Fi enabled; Imager offers passwordless sudo

Two more changes to the image (D-045), both from the first fresh flash:
- **Wi-Fi.** The stock image's `/var/lib/NetworkManager/NetworkManager.state` says
  `WirelessEnabled=false`. Imager's first boot unblocks Wi-Fi (`rfkill unblock wifi`) when a
  Wi-Fi country is set, but NetworkManager keeps its own switch off and blocks Wi-Fi again on
  the next boot, so a Wi-Fi uplink would not survive a reboot. The image sets
  `WirelessEnabled=true`, as raspi-config does when it unblocks Wi-Fi offline. The rfkill side
  is left to Imager's Wi-Fi country, so Pi OS's "no Wi-Fi until a country is set" still holds.
  This is the one change to the base outside the `.deb`.
- **sudo.** Imager writes `sudo: null` for the user it creates unless the OS entry declares the
  `passwordless_sudo` capability, which adds Imager's own opt-in "passwordless sudo" box (with
  its warning) to the user step. The image's Imager entry declares it. Nothing is baked in:
  without the box ticked, sudo asks for the password, as on stock.

### D-048: `palm-gw setup` adds the sudo user to the palm-gw group

Section 8.11 step 3 adds the invoking `SUDO_USER` to `palm-gw`, and the .deb's `postinst` does
it (D-043). In the image (D-045) the package is installed at build time, before Imager creates
the user, so nobody joins the group: after the first fresh flash, `palm-gwctl pair` without sudo
failed with "permission denied on /run/palm-gw/ctl.sock". `palm-gw setup` (the wizard and
`--apply`) now does the same step: it adds `SUDO_USER` to `palm-gw` if they are not in it, says so,
and the wizard's summary tells them to log in again before `palm-gwctl` works without sudo.
Running as root directly (no `SUDO_USER`) skips the step. A failed `usermod` is a warning, not a
failure of setup.

### D-049: Web UI accounts and the Bluetooth side for browsers

Zoe's answers to the open questions in docs/WEB-UI.md, 2026-11-11:
- A laptop on PAN that logs in to `https://10.42.0.1:8042/` as admin gets full admin, the same
  route set as the LAN and the Palm app (D-039). Browsers on the Bluetooth side are not limited
  to read-only.
- One admin account (with a username, default `admin`) plus the optional read-only viewer
  account. Section 8.12's single account is kept rather than a list of admins.
- The server, browser UI and `/api/v1/` come first in Phase 7; the Palm app waits for Zoe's
  Palm development how-to.
- Phase 6 is finished before Phase 7 starts: the one-liner on a freshly flashed Pi OS Lite card
  with the interactive wizard is still to run.

### D-050: Phase 6 accepted on the Pi 5; dongle mode pairs after the reboot

Phase 6's accept text says the wizard "pairs a Palm during setup, and the Palm has internet
before the wizard exits". With the USB dongle chosen, the onboard radio is switched off by
`dtoverlay=disable-bt`, which takes effect only at the next boot, so the wizard ends with
"pair after the reboot: sudo reboot, then palm-gwctl pair" (part 3's design) instead of pairing.
On the onboard adapter no reboot is needed and the wizard pairs in place. Zoe's gateway uses the
dongle, so the fresh-card runs on 2026-11-11 (the one-liner on stock Pi OS Lite, and the test14
image) paired right after the reboot, and the T|X loaded a page in Blazer. Phase 6 is accepted
on the Pi 5 with that reading. The Pi 4 stays untested, as for Phase 5 (section 10 allows it).

Left open: the first Connect after pairing on the stock-card run timed out on the T|X ("Serial:
timeout", PPP up, no IP data from the Pi, 40 s); the next Connects worked. It has not recurred;
a btmon trace of a failing attempt is the next step if it does.

### D-051: Web UI part 1: how the listeners and logins work

Phase 7 part 1 (`internal/web`). Where it departs from or fills in section 8.12 and
docs/WEB-UI.md:
- **Address changes are polled, not watched.** The listeners are re-derived from the host's
  IPv4 addresses every 5 s (LAN addresses on non-Palm interfaces; the gateway address once
  `br-palm` holds it) instead of following netlink notifications. Simpler, and a DHCP change on
  the LAN is picked up within 5 s.
- **Port 80 and 8042 on the Palm side are accepted only on the gateway address.** The template
  used to accept port 80 to any Pi address from the Palm side; nothing listens there except on
  the gateway, so the rule is narrowed to match section 10's "8042 only on 10.42.0.1" for 80
  too. The 8042 drop follows the `[web].listen` port (`WEB_PORT`).
- **Plain HTTP on a TLS port gets no answer.** net/http answers it with "400 Client sent an HTTP
  request to an HTTPS server" in the clear; palm-gwd's TLS listeners hide the handshake from
  net/http so the connection just closes. This applies to the LAN listener as well as the
  Bluetooth side.
- **TLS versions:** the Bluetooth side is TLS 1.3 only (D-039); the LAN listener accepts 1.2 and
  up, for older LAN browsers.
- **Sessions** live in memory (a daemon restart logs everyone out) and are bound to the side
  they logged in on: a cookie from the LAN is not valid on the Bluetooth side or the reverse.
  The cookie is `Secure` on TLS listeners only.
- **The rate limit** is 5 failed logins per minute per side and source address; the sixth try
  gets 429 with `Retry-After`, even with the right password.
- **A wrong username costs the same as a wrong password:** one argon2id verification either way,
  against a throwaway hash when the username does not match, and the same error page.
- **Accounts reload live:** `web.username`, `web.password_hash` and the viewer's two keys are in
  the live set of `reload` (D-030), so `palm-gw web set-password` plus `palm-gwctl reload`
  needs no restart. The rest of `[web]` needs a restart.
- **The certificate is never replaced by the daemon.** With `tls = "self-signed"` palm-gwd uses
  the certificate setup made and creates one only if none is there, so a fingerprint the Palm
  app pinned stays valid when the Pi's LAN address changes.
- **The web UI's actions are control-socket commands,** run in-process through
  `ctl.Server.Call`, so everything the UI does `palm-gwctl` can do too (section 15).
- **`palm-gw setup` without HTTPS turns the Bluetooth side off** and says so, since
  `config.Validate()` now refuses `tls = "off"` with `bluetooth_side = true`.

### D-052: Web UI part 2: dashboard, pairing and devices

- **htmx 2.0.11 is vendored** as `internal/web/static/htmx.min.js` (52,182 bytes, 0BSD), taken
  from the npm tarball after checking its SHA-512 against the registry's `dist.integrity`
  (`sha512-Thx/WtpeOQqSrqBCw/A1cwGJ…tamA==`). It is configured by a meta tag with
  `includeIndicatorStyles`, `allowEval` and `allowScriptTags` off, so the CSP can stay
  `default-src 'self'`. Only the dashboard's 3 s refresh uses it. The pairing page's live list
  and countdowns are a 2.5 KB script of our own (`static/app.js`) on `EventSource`. Front end in
  all: 58 KB, under section 8.12's 60 KB.
- **The pairing window a browser opens belongs to the server, not the request.** The ctl `pair`
  command closes its window when its caller goes away; the web UI runs it for its own lifetime,
  so the window lasts its minutes, closes after one device if asked, or on "Close the window",
  which uses the new `pair.close` command (which also closes a window `palm-gwctl pair` opened;
  `palm-gwctl unpair` is the CLI for it).
- **PINs never go into event lists.** The dashboard's events, the SSE feed and
  `palm-gwctl events` drop any `pin` field. The PIN appears only on the pairing page, and only
  for the admin account: a viewer sees that a window is open but not its PIN, since the PIN is
  what lets a device pair.
- **The event bus numbers its events and keeps the last 200** (`seq`), so the dashboard shows the
  last 10, a reconnecting SSE client resumes from `Last-Event-ID`, and `events` takes
  `recent`, `since` and `no_wait`.
- **Forget asks on a page of its own** (`GET /devices/<addr>/forget`, then a POST), not a
  JavaScript dialog, so it works without scripts and under the CSP.
- **The status reply gained what the pages show:** each session's byte counters (from
  `/sys/class/net/<if>/statistics`) and each allowlisted device's first-paired and last-seen
  times. `palm-gwctl status --json` shows them too.
- `rename` stores a label for devices paired here; a device allowed only in `config.toml` has no
  state entry to label, and the daemon says so.

## Hardware observations

### Test rig (kickoff rule 4), 2026-11-08

- **Palm TX** (address 00:07:E0:3C:5E:FA, class 0x100114), added 2026-11-09. Pairs with a
  legacy PIN. Its network wizard will not accept a blank user name and password. Its
  Connection "Connect to: PC, via Bluetooth" searches SDP for LAN Access (0x1102), connects on the
  LAP channel and starts PPP at once with no `CLIENT` (btmon, 2026-11-09), contrary to section
  11's "PC uses SPP and the Microsoft handshake". docs/PALM-SETUP.md must say so.
  Its Connection "Connect to: Modem, via Bluetooth" searches SDP for Dial-up Networking
  (0x1103), waits 3.5 s after the RFCOMM channel opens, sends `ATQ0V1Z`, `ATQ0E0`, then dials
  with `ATQ0E0&D0&C1V1S7=90L1DT0` (the dial last in a compound line), and after `CONNECT` sends
  no PPP and gives up about 80 s later (D-023, D-024, D-025). With LAN Access switched off, no
  attempt searched for Serial Port (2026-11-09).
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
