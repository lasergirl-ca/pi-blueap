# Palm Bluetooth Gateway (`palm-gw`): Implementation Plan

A Raspberry Pi 4 / Raspberry Pi 5 re-creation of the Belkin F8T030 Bluetooth Access Point, serving Palm OS 5 handhelds (Tungsten T/T2/T3/T5, T|X, LifeDrive, Zire 72, Treo 650/680/700p) over the Bluetooth **LAN Access Profile** and **Serial Port Profile** (both are PPP over RFCOMM), the **Dial-Up Networking Profile** (PPP over RFCOMM behind a tiny AT-command emulator), and later devices over **PAN (NAP)**. Palms get routed, NAT'd internet and are firewalled away from the home LAN by default.

This document is the specification for an implementing agent. Every design question has been resolved; there are no "verify on hardware" items left. Where the plan previously depended on something that could vary by BlueZ version, kernel, nftables version or Pi model, it has been redesigned so that it no longer depends on it (see section 16, Resolved questions). The two things that can only be observed on real hardware (whether the onboard radio pairs reliably with a given Palm, and which Class of Device a given Palm's search accepts) are handled by configuration and automatic fallbacks rather than by the implementer guessing. Nothing in this document should be silently changed; if something turns out to be wrong, fix it and record why in `docs/DECISIONS.md`.

---

## 0. Table of contents

1. Goals and non-goals
2. Decisions
3. Target hardware: Pi 4 and Pi 5 specifics
4. Target software: Raspberry Pi OS and package versions
5. Architecture and data flow
6. Repository layout
7. Configuration schema
8. Component specifications
   - 8.1 BlueZ adapter configuration
   - 8.2 Agent (pairing and authorisation)
   - 8.3 RFCOMM profiles: SPP, LAP, DUN
   - 8.4 Pre-PPP handshakes: Microsoft null-modem and AT emulator
   - 8.5 PPP spawner and session table
   - 8.6 PAN (NAP)
   - 8.7 Network plumbing: bridge, DHCP/DNS, routing, firewall
   - 8.8 Control socket and `palm-gwctl`
   - 8.9 Logging, state and metrics
   - 8.10 systemd units and sandboxing
   - 8.11 Installation: three paths, one wizard
   - 8.12 Management web UI
9. Test tooling: `fake-palm`
10. Build phases with acceptance tests
11. Palm-side setup (what the user does on the handheld)
12. Known traps and how to recognise them
13. Security posture
14. Test matrix
15. Deliverables checklist
16. Resolved questions
Appendix A: Bluetooth background for the implementer
Appendix B: D-Bus interface reference used by this project
Appendix C: SDP record templates
Appendix D: pppd option rationale

---

## 1. Goals and non-goals

### Goals

- A Palm T|X with a Bluetooth network connection configured once gets an IP address and working internet within 10 seconds of tapping **Connect**, every time, including after Pi reboots and BlueZ restarts.
- Several Palms connected simultaneously, each on its own PPP interface.
- A Pocket PC, Windows or Android device using Bluetooth PAN (PANU role) gets the same internet access over BNEP.
- Fresh Raspberry Pi OS Lite image to working gateway with `sudo make install` and one reboot, on **both** Pi 4 and Pi 5 from the same repository and the same binary.
- Palms are isolated to the internet by default. Reaching a LAN host (HotSync/NetSync server, a vault, a printer) is one block in a config file.
- Operable without a screen on the Pi: pairing is opened from an SSH session with `palm-gwctl pair`, which prints the PIN to type on the Palm.
- Every connection, disconnection, pairing attempt and rejection is logged with the Bluetooth device address.

- **Easy install**: one command on a fresh image (`curl -fsSL https://…/install.sh | sudo bash`) or `sudo apt install ./palm-gw_*.deb`, followed by an interactive `palm-gw setup` wizard that detects the board, adapter and uplink and asks only the questions it cannot answer itself.
- **Easy management**: a small local web UI (status, pair with the PIN shown on screen, device list, firewall allowlist, logs, support bundle) plus the `palm-gwctl` CLI for the same operations. No editing of TOML required for day-to-day use.

### Non-goals for v1

- HotSync/NetSync server on the Pi (separate project; the firewall must simply be able to allow it).
- OBEX, audio, HID or any other profile.
- IPv6 towards the Palm side (Palm NetLib is IPv4 only). IPv6 on the uplink is irrelevant to this project and must not be broken by it.
- Bridged (Layer 2) mode onto the home LAN.
- Running on anything older than Pi 4 or on 32-bit Raspberry Pi OS.

---

## 2. Decisions

| Topic | Decision |
|---|---|
| Language | **Go 1.22+**, module `palm-gw`, D-Bus via `github.com/godbus/dbus/v5`, raw Bluetooth sockets in test tooling via `golang.org/x/sys/unix`. No cgo. |
| Config format | TOML via `github.com/BurntSushi/toml`. |
| Target OS | Raspberry Pi OS **Lite, 64-bit**, current stable (Debian 13 "Trixie" base). Must also build and run on Bookworm (Debian 12) because many Pis are still on it; note any Bookworm-specific divergence in `docs/DECISIONS.md`. |
| Target hardware | **Pi 4 Model B and Pi 5**, same binary (`GOARCH=arm64`). Pi Zero 2 W and Pi 400/500 are expected to work but are not tested in v1. |
| Bluetooth adapter | **Onboard radio (`hci0`) by default**, with a fully documented and tested USB dongle mode (CSR8510-class, `hci1` or `hci0` with onboard disabled). Adapter is always a config value, never a constant. |
| Networking model | **Routed with NAT.** Palm subnet `10.42.0.0/24`, gateway `10.42.0.1`. Uplink is `eth0` or `wlan0`, from config. |
| Bridge management | **NetworkManager** (`nmcli`), because it is the default network stack on Raspberry Pi OS since Bookworm. systemd-networkd is not used. NetworkManager is told to leave `bnep*` and `ppp*` interfaces alone. |
| Profiles served to Palms | **SPP (0x1101), LAP (0x1102), DUN (0x1103)**, all server role, each on its own fixed RFCOMM channel, all terminating in `pppd`. Reason: Palm OS's "Connect to: PC" connection type looks for a serial port, "Local Network" looks for LAN Access, "Mobile Phone" looks for DUN. Serving all three means any of the Palm's connection types works. |
| Profile served to others | **PAN NAP (0x1116)** onto bridge `br-palm`. |
| PPP implementation | System **`pppd`** from the `ppp` package, one process per session, driven through `notty` with the RFCOMM socket as stdin/stdout. No PPP in Go. |
| Pairing | **Legacy PIN pairing** (BT 1.1 and 1.2 Palms have no Secure Simple Pairing). PIN is **random per pairing window** by default, printed by `palm-gwctl pair`; a fixed PIN is an opt-in config value. Pairable mode is **off** except during a window. |
| Authorisation | Allowlist of Bluetooth device addresses. Devices paired via `palm-gwctl pair` are added to a state file automatically; config may add static entries. Empty allowlist plus `allow_any = true` is the only way to accept unknown paired devices, and the daemon logs a warning at startup if that is set. |
| Firewall | **nftables**. Default deny from Palm interfaces to RFC1918 and to the Pi itself except DNS/DHCP/ICMP; explicit allowlist of LAN `host:proto/port` tuples from config. |
| DHCP/DNS | **dnsmasq**, bound to `10.42.0.1` only. PPP peers get addresses from `pppd` (IPCP) and DNS via `ms-dns`; PAN peers get DHCP from dnsmasq. |
| Privileges | Daemon runs as **root** (pppd, nftables and BlueZ profile registration require it). systemd hardening applied as far as it goes without breaking those three. |
| Logging | Go `log/slog`, JSON handler to stderr, collected by journald. |
| Control plane | Unix domain socket `/run/palm-gw/ctl.sock`, newline-delimited JSON, used by `palm-gwctl` (same binary, subcommand) and by the web UI. The daemon is the only thing that touches BlueZ, pppd and nftables; CLI and web are thin clients. |
| Packaging | **Debian package** (`palm-gw_<ver>_arm64.deb`, built with `nfpm`) as the primary install path, with a bootstrap `install.sh` that downloads the right release and installs it. `make install` from source remains for developers. Upgrades are `apt install ./newer.deb` or `palm-gwctl upgrade`. |
| Setup wizard | `palm-gw setup`: interactive terminal wizard (Go, `charmbracelet/huh` for prompts, falls back to plain prompts when not a TTY) that writes `/etc/palm-gw/config.toml` and the templated system files. Fully scriptable with flags for unattended installs. |
| Web UI | **Yes, in v1.** Go `net/http` with embedded templates and htmx; no JS build step, no Node. Full UI on the Pi's LAN address(es), port 8042; the same UI and API, **TLS only**, on the gateway address `10.42.0.1:8042` for the Bluetooth side, used by a native ARM Palm OS app with full admin (amended by D-026 and D-039, design in `docs/WEB-UI.md`). Username and password set during `setup`, session cookie (bearer token for the Palm app's API), TLS optional via self-signed cert generated at setup. |
| Licence | MIT. |

---

## 3. Target hardware: Pi 4 and Pi 5 specifics

Both boards use the same Wi-Fi/Bluetooth combo chip family (Cypress/Infineon **CYW43455**, Bluetooth 5.0, Basic Rate/EDR plus LE). That is the good news: one radio driver, one firmware file, one set of quirks. Everything that differs between the two boards is listed here so the installer can branch on it.

| | Pi 4 Model B | Pi 5 |
|---|---|---|
| SoC | BCM2711 (Cortex-A72) | BCM2712 (Cortex-A76) + RP1 I/O controller |
| Combo chip | CYW43455 | CYW43455 (Infineon branding) |
| BT host interface | UART (PL011 `ttyAMA0` by default) via in-kernel serdev (`hci_uart` + `btbcm`) | UART via serdev, same drivers |
| BT firmware | Loaded by the kernel `btbcm` driver from the `firmware-brcm80211` package; the project never references the path. The installer checks only that `bluetoothctl list` shows an adapter. | same |
| Disable onboard BT | `dtoverlay=disable-bt` in `/boot/firmware/config.txt` | same |
| Disable onboard Wi-Fi | `dtoverlay=disable-wifi` | same |
| Serial console vs BT | BT and the console UART can conflict on Pi 4 if `dtoverlay=miniuart-bt` or `enable_uart=1` with console on `ttyAMA0`. Installer must warn if `console=serial0` or `ttyAMA0` appears in `/boot/firmware/cmdline.txt`. | Debug UART is a separate 3-pin connector; no conflict. |
| Ethernet | 1 GbE (BCM54213PE) | 1 GbE via RP1 |
| USB for a dongle | USB 3.0 via VL805 (xHCI); any port | USB 3.0 via RP1; any port. **Pi 5 limits total USB current to 600 mA unless powered by a 5 A PSU** (or `usb_max_current_enable=1` in `config.txt`). A CSR8510 dongle draws about 50 mA, so this is fine. The wizard prints `vcgencmd get_throttled` (bit 0 = under-voltage now, bit 16 = has occurred) and, on Pi 5, `vcgencmd pmic_read_adc` for the supply voltage; it warns, never blocks. |
| Boot config path | `/boot/firmware/config.txt` (Bookworm and later) | same |
| 64-bit OS | Supported; 32-bit also exists but is out of scope | 64-bit only |
| Expected `hci0` in `bluetoothctl list` | `BCM4345C0` style name, bus UART | same |

### Radio coexistence (both boards)

Wi-Fi and Bluetooth share one antenna and one chip. The chip arbitrates between them, and Bluetooth 1.1 devices (the Palms) are slow and tolerate very little timing slop during inquiry, paging and the first seconds of a link. Symptoms of coexistence trouble: Palm finds the Pi only one try in three, pairing fails with "Unable to connect", or PPP negotiates and then stalls after a few seconds while Wi-Fi is busy.

Mitigations, in order of preference:

1. **Use Ethernet as the uplink and disable Wi-Fi** (`dtoverlay=disable-wifi`, or `nmcli radio wifi off`). Config value `uplink = "eth0"`. This is the recommended deployment.
2. If Wi-Fi uplink is required, keep it on 5 GHz (no spectrum overlap with Bluetooth at 2.4 GHz). The installer should detect a 2.4 GHz Wi-Fi uplink and print a warning.
3. Plug in a USB Bluetooth dongle and point `adapter` at it. Disable the onboard BT with `dtoverlay=disable-bt` so there is no ambiguity about `hci0`.

### USB dongle mode

Recommended dongles: anything with a **CSR8510 A10** (Bluetooth 4.0, Class 1 or 2) or a **Realtek RTL8761B** (Bluetooth 5.0). Both do classic BR/EDR paging reliably with BT 1.1 peers. Avoid LE-only or "BLE 5.3" marketed sticks that lack BR/EDR.

Installer behaviour: `make install ADAPTER=dongle` sets `dtoverlay=disable-bt`, disables `hciuart.service` if present, writes `adapter = "hci0"` (the dongle will become `hci0` after reboot), and tells the user to reboot. When a `config.toml` already exists it is kept. If its `adapter` is not `hci0`, `auto`, or the address of a USB adapter on the host (for example, the onboard radio pinned by address, D-035), the installer warns that the gateway will not find it once the onboard radio is off. `make install` without the flag leaves the onboard radio and writes `adapter = "hci0"` too; the difference is whether the onboard radio is disabled. The daemon must additionally support `adapter = "auto"` meaning "the first adapter whose `Address` is not the onboard one if a second adapter exists, otherwise `hci0`", and log which it picked.

### Identifying the board at install time

```sh
model=$(tr -d '\0' < /proc/device-tree/model)      # "Raspberry Pi 4 Model B Rev 1.5" / "Raspberry Pi 5 Model B Rev 1.0"
case "$model" in
  *"Pi 4"*) BOARD=pi4 ;;
  *"Pi 5"*) BOARD=pi5 ;;
  *) echo "Unsupported board: $model" >&2; exit 1 ;;
esac
```

`install.sh` must record `BOARD`, kernel version (`uname -r`), BlueZ version (`bluetoothctl --version`), `pppd --version`, and the adapter list into `/var/lib/palm-gw/install-info.json` so troubleshooting reports have hardware context.

---

## 4. Target software: Raspberry Pi OS and package versions

| Component | Expected | Minimum | Notes |
|---|---|---|---|
| Raspberry Pi OS | Trixie (Debian 13) Lite 64-bit | Bookworm (Debian 12) | Image from Raspberry Pi Imager, SSH enabled, user created, hostname set. |
| Kernel | 6.12.x (Trixie) | 6.6.x (Bookworm) | Needs `bnep`, `rfcomm`, `hci_uart`, `btbcm`, `ppp_generic`, `ppp_async` modules. All are in the stock Pi kernel. |
| BlueZ | 5.8x | 5.66 | `ProfileManager1`, `Agent1`, `NetworkServer1` all stable since 5.x. **Do not** depend on `hciconfig`, `hcitool`, `rfcomm`, `sdptool`, `dund`, `pand`; they are deprecated and may be absent. `btmon` and `bluetoothctl` are the supported tools. |
| ppp | 2.5.x | 2.4.9 | `notty`, `ms-dns`, `linkname`, `ipparam` all present. |
| dnsmasq | 2.90+ | 2.89 | `bind-dynamic`, `listen-address`. |
| nftables | 1.0.x | 1.0.6 | Sets with concatenated types and `flags interval`. |
| NetworkManager | 1.4x | 1.42 | Default on Pi OS Bookworm+. `nmcli` for bridge creation. |
| Go | 1.22+ | 1.22 | Cross-compile from macOS or build on-device. `apt` Go on Trixie is 1.24; on Bookworm it is 1.19 (too old), so the installer must prefer a prebuilt release binary and fall back to downloading the official Go toolchain if asked to build from source on Bookworm. |

### Pre-install state the installer must check and fix

- `bluetooth.service` enabled and active; `hci0` present (`bluetoothctl list` non-empty). If empty on Pi 4/5: check `dtoverlay=disable-bt` is **not** in `config.txt` (unless dongle mode) and that firmware is present.
- Whether `systemd-resolved` is running. Pi OS does not enable it by default, but if it is, dnsmasq's `bind-dynamic` plus `listen-address=10.42.0.1` avoids the port 53 conflict on `127.0.0.53`. Installer prints a note.
- Whether an existing `dnsmasq` or `nftables` config exists; the installer **adds** drop-in files and never overwrites `/etc/nftables.conf` or `/etc/dnsmasq.conf`. It includes `/etc/nftables.d/palm-gw.nft` via an `include` line appended to `/etc/nftables.conf` only if not already present.
- `net.ipv4.ip_forward` not already pinned to 0 by another sysctl file.

---

## 5. Architecture and data flow

```
 Palm T|X (BT 1.1)                         Raspberry Pi 4 / 5                                  Internet
 ┌────────────────┐     RFCOMM ch 1/2/3    ┌───────────────────────────────────────────────┐
 │ Prefs▸Network  │ ─────────────────────▶ │ BlueZ (bluetoothd)                            │
 │  "Palm GW"     │   ACL link, E0 crypto  │   Adapter1  hci0  Class 0x020300 "Palm GW"    │
 │  Connect       │                        │   Agent1 ◀──────────── palm-gwd /palm/agent   │
 └────────────────┘                        │   ProfileManager1 ◀─── palm-gwd /palm/{spp,lap,dun}
                                           │   NetworkServer1(nap, br-palm)                │
                                           │        │ NewConnection(dev, fd)               │
                                           │        ▼                                      │
                                           │ palm-gwd (Go)                                 │
                                           │   handshake: "CLIENT"→"CLIENTSERVER" / AT→CONNECT
                                           │   session table, address pool, ctl socket     │
                                           │        │ fork/exec, fd as stdin+stdout        │
                                           │        ▼                                      │
                                           │ pppd notty  ──▶ ppp0  10.42.0.1 ⇄ 10.42.0.10  │
                                           │ pppd notty  ──▶ ppp1  10.42.0.1 ⇄ 10.42.0.11  │
 Pocket PC / Android (PANU)                │                                               │
 ┌────────────────┐      BNEP over L2CAP   │ bnep0 ──▶ br-palm 10.42.0.1/24 ◀── dnsmasq    │
 │ "Internet      │ ─────────────────────▶ │                     (DHCP .100-.199, DNS)     │
 │  access"       │                        │                                               │
 └────────────────┘                        │ nftables: forward policy drop                 │
                                           │   ppp*/br-palm ▶ lan_allow set  ▶ accept      │
                                           │   ppp*/br-palm ▶ RFC1918        ▶ drop        │
                                           │   ppp*/br-palm ▶ uplink         ▶ accept + masquerade ─────▶
                                           └───────────────────────────────────────────────┘
```

### Why one daemon

BlueZ wants a single long-lived D-Bus client to own the agent and the profile objects; if that client disappears, BlueZ unregisters them. Putting agent, profiles, PAN registration and the pppd supervisor in one process means one lifecycle, one restart, one log stream, and one place that knows the mapping from Bluetooth address to PPP interface to IP address. The alternative (bluez-tools `bt-agent` plus a separate profile server plus shell scripts) is what people did in 2012 and it falls apart under reconnect storms.

### Lifecycle

1. `palm-gwd` starts, loads and validates config, opens the control socket, connects to the system bus.
2. Waits for `org.bluez` to appear (`NameOwnerChanged` or a bounded retry loop; BlueZ may start after us despite unit ordering).
3. Resolves the adapter object path, sets `Alias`, `Powered=true`, `Discoverable=true`, `DiscoverableTimeout=0`, `Pairable=false`.
4. Registers the agent (`RegisterAgent` + `RequestDefaultAgent`), the three RFCOMM profiles, and the NAP server.
5. Regenerates the nftables `lan_allow` set from config and applies it.
6. Serves connections until SIGTERM; on shutdown unregisters NAP and profiles, terminates pppd children, releases the agent.
7. If `org.bluez` goes away (BlueZ restart), tears down the BlueZ-side state, kills live pppd sessions (their sockets are dead anyway), and goes back to step 2.

---

## 6. Repository layout

```
palm-gw/
├── go.mod, go.sum
├── cmd/
│   ├── palm-gwd/main.go             # daemon entry: flags, config, run loop, signals
│   ├── palm-gwctl/main.go           # CLI client for the control socket (also `palm-gwd ctl ...`)
│   ├── palm-gw/main.go              # user-facing entry: `palm-gw setup`, `palm-gw status`, `palm-gw web`, `palm-gw upgrade`
│   └── fake-palm/main.go            # Linux-side test client (section 9)
├── internal/
│   ├── config/
│   │   ├── config.go                # types, Load(), Validate()
│   │   └── config_test.go
│   ├── bluez/
│   │   ├── bus.go                   # system bus connect, wait for org.bluez, ObjectManager helpers
│   │   ├── adapter.go               # find adapter by hciN/auto, set props
│   │   ├── device.go                # Device1 address lookup, Trusted/Remove
│   │   ├── introspect.go            # XML for Agent1, Profile1 exports
│   │   └── fd.go                    # UnixFD -> *os.File, set blocking, dup safety
│   ├── agent/
│   │   ├── agent.go                 # Agent1 implementation
│   │   └── window.go                # pairing window state machine, PIN generation
│   ├── allow/
│   │   ├── allow.go                 # allowlist: static (config) + dynamic (state file)
│   │   └── allow_test.go
│   ├── profile/
│   │   ├── profile.go               # Profile1 implementation, registration with ServiceRecord
│   │   ├── sdp.go                   # SDP XML templates (Appendix C) with channel substitution
│   │   ├── handshake.go             # MS null-modem + AT emulator (section 8.4)
│   │   ├── handshake_test.go
│   │   └── sdp_test.go
│   ├── ppp/
│   │   ├── spawn.go                 # exec pppd with socket as stdin/stdout, process group
│   │   ├── pool.go                  # peer address pool
│   │   ├── pool_test.go
│   │   └── sessions.go              # session table, reaping, per-address dedupe
│   ├── pan/
│   │   └── pan.go                   # NetworkServer1 Register/Unregister
│   ├── fw/
│   │   ├── nft.go                   # generate lan_allow set + apply via `nft -f -`
│   │   └── nft_test.go
│   ├── ctl/
│   │   ├── server.go                # unix socket JSON protocol
│   │   ├── client.go
│   │   └── proto.go                 # request/response types
│   ├── state/
│   │   └── state.go                 # /var/lib/palm-gw/state.json (dynamic allowlist, last-seen)
│   ├── setup/
│   │   ├── wizard.go                # interactive + flag-driven setup (section 8.11)
│   │   ├── detect.go                # board, adapters, uplink candidates, PSU, console UART, Wi-Fi band
│   │   ├── render.go                # templates deploy/ files from config
│   │   └── detect_test.go
│   └── web/
│       ├── server.go                # net/http, auth, routes (section 8.12)
│       ├── handlers_*.go            # status, pair, devices, firewall, logs, bundle, settings
│       ├── templates/*.html         # embedded via embed.FS; htmx for live updates
│       ├── static/                  # htmx.min.js (vendored), one CSS file
│       └── server_test.go
├── packaging/
│   ├── nfpm.yaml                    # builds palm-gw_<ver>_arm64.deb
│   ├── postinst, prerm, postrm      # create group, enable units, run `palm-gw setup --check` hints
│   └── bootstrap-install.sh         # the `curl | sudo bash` script: detects arch/board, fetches release .deb, installs, launches setup
├── deploy/
│   ├── systemd/palm-gwd.service
│   ├── systemd/palm-gwd-nm-bridge.service      # oneshot: ensure br-palm via nmcli (idempotent); reads /etc/palm-gw/bridge.env (D-042)
│   ├── lib/nm-bridge.sh                         # its helper (D-042; was scripts/)
│   ├── bluetooth/main.conf.block                # managed block merged into /etc/bluetooth/main.conf (section 8.1)
│   ├── NetworkManager/conf.d/palm-gw.conf      # unmanaged bnep*, ppp*
│   ├── dnsmasq/palm-gw.conf.tmpl                 # rendered to /etc/palm-gw/dnsmasq.conf
│   ├── systemd/palm-gw-dnsmasq.service          # own dnsmasq instance (dnsmasq-base)
│   ├── nftables/palm-gw.nft.tmpl                # rendered to /etc/nftables.d/palm-gw.nft
│   ├── sysctl.d/90-palm-gw.conf
│   ├── modules-load.d/palm-gw.conf             # bnep, rfcomm
│   ├── ppp/options.palm
│   ├── ppp/ip-up.d/palm-gw                      # optional: notify daemon via ctl socket
│   └── ppp/ip-down.d/palm-gw
├── config.example.toml
├── Makefile
├── scripts/
│   ├── install.sh                   # idempotent installer (section 8.11)
│   ├── uninstall.sh
│   ├── detect-board.sh
│   └── support-bundle.sh            # tar of journal, btmon capture, configs, install-info.json
├── docs/
│   ├── PALM-SETUP.md                # section 11, written for someone holding a Palm
│   ├── TROUBLESHOOTING.md           # section 12 expanded with real logs once seen
│   ├── TESTING.md                   # exact commands used for each acceptance test
│   ├── DECISIONS.md                 # every deviation from PLAN.md, with reason, plus hardware observations
│   └── BLUETOOTH-PRIMER.md          # Appendix A, expanded
├── .github/workflows/ci.yml         # go vet, golangci-lint, go test, cross-build arm64
├── LICENSE
└── README.md
```

---

## 7. Configuration schema

File: `/etc/palm-gw/config.toml`. Every key has a default; an empty file is valid and yields a working gateway with `allow_any = false` (so nothing can connect until a device is paired through `palm-gwctl pair`).

```toml
# /etc/palm-gw/config.toml

[bluetooth]
adapter          = "hci0"          # "hci0", "hci1", the adapter's address, or "auto" (section 3; address form per D-035)
name             = "Palm Gateway"  # Adapter Alias; this is what the Palm shows in its device list
class            = 0x000300        # major/minor Class of Device only (BlueZ sets the service bits itself); see section 8.1
class_fallback   = 0x000104        # used by `palm-gwctl class toggle` when a Palm cannot see the gateway
pin              = ""              # "" = random 4-digit PIN per pairing window; "1234" = fixed (discouraged)
pin_length       = 4               # for random PINs; Palm OS accepts up to 16 but 4 to 6 is practical on a stylus keyboard
pair_window_default_minutes = 5    # used when `palm-gwctl pair` has no argument
allow_any        = false           # true = accept any *paired* device even if not in allowlist (warned at start)
allow            = []              # static allowlist of device addresses, e.g. ["00:07:E0:12:34:56"]
trust_on_pair    = true            # set Device1.Trusted after a successful pairing in a window

[spp]                              # Serial Port Profile; Palm "Connect to: PC"
enabled = true
channel = 1
[lap]                              # LAN Access Profile; Palm "Connect to: Local Network" (where offered)
enabled = true
channel = 2
[dun]                              # Dial-Up Networking; Palm "Connect to: Mobile Phone" (AT emulator)
enabled = true
channel = 3
[pan]
enabled = true
bridge  = "br-palm"

[handshake]
ms_null_modem        = true        # answer "CLIENT" with "CLIENTSERVER"
ms_timeout_ms        = 3000        # how long to wait for CLIENT or PPP before starting pppd blind
at_emulator          = true        # DUN channel: answer AT commands, CONNECT on ATD
at_connect_banner    = "CONNECT 115200"
at_timeout_ms        = 10000       # DUN channel: how long to wait for the first AT command (D-023)

[network]
subnet     = "10.42.0.0/24"
gateway    = "10.42.0.1"
ppp_pool   = ["10.42.0.10", "10.42.0.99"]      # inclusive range for PPP peers
dhcp_pool  = ["10.42.0.100", "10.42.0.199"]    # dnsmasq range for PAN clients
uplink     = "eth0"                             # interface to NAT out of
dns_upstream = []                               # [] = use the Pi's resolv.conf; or ["1.1.1.1", "9.9.9.9"]
mss_clamp  = true                               # clamp TCP MSS to path MTU on forward (harmless, helps PPPoE uplinks)

[ppp]
options_file   = "/etc/ppp/options.palm"
debug          = false             # adds `debug` to pppd; very chatty
idle_seconds   = 0                 # 0 = never; else pppd `idle N` disconnects idle links
max_sessions   = 8
per_device_max = 1                 # a new connection from an address with a live session kills the old one

[firewall]
manage_nftables = true             # false = daemon never touches nft (you own the rules)
palm_to_pi      = ["udp/53", "udp/67", "icmp"]   # extra services Palms may reach on the Pi itself; DNS, DHCP, port 80 and ping are always open (D-029)

[[firewall.allow]]                 # LAN hosts Palms may reach; everything else RFC1918 is dropped
host    = "192.168.1.20"
ports   = ["tcp/14238", "tcp/14237"]
comment = "NetSync (HotSync over TCP)"

[[firewall.allow]]
host    = "192.168.1.0/24"         # CIDR is allowed too
ports   = ["udp/5353"]
comment = "mDNS, if you want Palm apps to find things"

[logging]
level  = "info"                    # debug | info | warn | error
format = "json"                    # json | text
btmon_on_failure = false           # if true, on repeated pairing/connect failures the daemon runs `btmon -w` for 60 s into /var/lib/palm-gw/captures/

[ctl]
socket = "/run/palm-gw/ctl.sock"
group  = "palm-gw"                 # socket is 0660 root:palm-gw so an operator user can run palm-gwctl without sudo

[web]
enabled        = true
listen         = ["0.0.0.0:8042"]   # full UI: bound to LAN addresses only at runtime (the daemon skips br-palm/ppp*)
username       = "admin"
bluetooth_side = true               # UI and API on the gateway address, port 8042, TLS only (D-026, D-039, docs/WEB-UI.md)
viewer_username      = ""           # optional read-only account
viewer_password_hash = ""
tls            = "self-signed"      # "off" | "self-signed" | "file"
tls_cert_file  = ""
tls_key_file   = ""
password_hash  = ""                 # argon2id; written by `palm-gw setup` or `palm-gw web set-password`
session_hours  = 72
allow_cidrs    = []                 # [] = any LAN source; or ["192.168.1.0/24"] to restrict who may even see the login page
```

### Validation rules (fail to start, with a message naming the key)

- `adapter` matches `^hci[0-9]+$` or is `auto`.
- Channels are 1 to 30 and unique across enabled profiles.
- `subnet` parses; `gateway`, both pool bounds, are inside it; pools do not overlap each other or the gateway; `ppp_pool` has at least `max_sessions` addresses.
- `uplink` exists at start (warn, do not fail, if it is `wlan0` and currently down).
- Every `firewall.allow.ports` entry matches `^(tcp|udp)/[0-9]{1,5}$` or `icmp`; `host` is an IP or CIDR.
- If `pin` is set it is 1 to 16 digits.
- `allow_any = true` logs a WARN line at startup every time.

---

## 8. Component specifications

### 8.1 BlueZ adapter configuration

**Managed block in `/etc/bluetooth/main.conf`.** BlueZ only gained `main.conf.d` support in a commit from January 2026, which is newer than the BlueZ 5.82 in Trixie and the 5.66 in Bookworm, so a drop-in directory cannot be relied on. The installer instead edits `/etc/bluetooth/main.conf` in place with an idempotent managed block (`# BEGIN palm-gw` … `# END palm-gw`) using a small Go INI editor in `internal/setup/render.go` that sets or replaces the keys below inside the correct sections and leaves everything else untouched. Uninstall removes the block and restores the original values it recorded in `install-info.json`. The keys written:

```ini
[General]
# What the Palm sees in its device list. The daemon also sets Alias at runtime.
Name = Palm Gateway

# Class of Device. BlueZ documents this key as "only the major and minor device class bits are
# considered": the 11 service-class bits are computed by BlueZ from the UUIDs of registered
# services (LAP and NAP both set the Networking bit). So we set only:
#   major 0x03 (0x000300): LAN / Network Access Point
#   minor 0x00: "fully available" (load factor 0)
Class = 0x000300

# Stay discoverable forever; the daemon controls Pairable separately and keeps it off outside pairing windows.
DiscoverableTimeout = 0
PairableTimeout = 0

# If a Palm is hard-reset and re-pairs with a key the Pi doesn't know, let it.
JustWorksRepairing = always

# Palms are BR/EDR only. Leaving LE on is harmless, but "dual" is the default anyway.
ControllerMode = dual

# D-037: otherwise BlueZ holds a trusted Palm's connection until it has browsed the Palm's SDP
# record, and after a bluetoothd restart nothing starts that browse ("Initializing" forever).
ReverseServiceDiscovery = false

[Policy]
AutoEnable = true
```

**Class fallback is a feature, not an open question.** Palm OS's device picker filters inquiry results by major device class in some screens, and different Palm OS builds differ. The daemon logs the adapter's effective `Class` property at startup. `palm-gwctl class toggle` (and the "Palm can't see the gateway?" button on the web UI's Pair page) rewrites `Class` to `class_fallback` (`0x000104`, Computer/Desktop, which every Palm connection type accepts because it is what a PC looks like), restarts `bluetooth.service`, re-registers, and persists the choice. The Pair page explains this in one sentence. No implementer decision is needed.

**Runtime properties the daemon sets on `org.bluez.Adapter1`** (adapter path `/org/bluez/hciN`):

| Property | Value | Why |
|---|---|---|
| `Alias` | `config.bluetooth.name` | Belt and braces with `Name` in main.conf |
| `Powered` | `true` | |
| `Discoverable` | `true` | Palm must be able to find the Pi by inquiry every time; the Palm does not cache well |
| `DiscoverableTimeout` | `0` | Never time out |
| `Pairable` | `false` at start; `true` only inside a pairing window | Section 13 |
| `PairableTimeout` | `0` | The daemon enforces the window itself and does not rely on BlueZ's timer |

Finding the adapter: use `org.freedesktop.DBus.ObjectManager.GetManagedObjects` on `/` and pick the object whose interfaces include `org.bluez.Adapter1` and whose path ends with the configured `hciN`. For `auto`: if exactly one adapter exists, use it; if more than one, prefer the one whose sysfs bus is USB (`/sys/class/bluetooth/hciN/device/subsystem` resolves to `usb`; a dongle), else the lowest `hciN`. Log the choice with address and bus. No device-tree parsing. (`Adapter1.Modalias` cannot be used: it is BlueZ's own DeviceID and identical for every adapter; see DECISIONS.md D-014.)

### 8.2 Agent (pairing and authorisation)

Object path `/palm/agent`, interface `org.bluez.Agent1`, registered with `org.bluez.AgentManager1.RegisterAgent("/palm/agent", "DisplayYesNo")` then `RequestDefaultAgent("/palm/agent")`. Capability `DisplayYesNo` is right because the Pi can "display" (log/print) a PIN and can answer yes/no, but has no keyboard for entering a remote PIN; this steers BlueZ to ask us `RequestPinCode` for legacy peers and `RequestConfirmation` for SSP peers.

| Method | Behaviour |
|---|---|
| `RequestPinCode(device) → string` | If no pairing window is open: return `org.bluez.Error.Rejected` and log `pairing_rejected reason=no_window addr=…`. Otherwise return the window's PIN. Log `pin_requested addr=… name=…`. This is the path **every Palm takes**. |
| `DisplayPinCode(device, pincode)` | Log it. (Used when the remote generates the PIN; Palms do not.) |
| `RequestPasskey(device) → uint32` | SSP numeric entry; reject unless a window is open, else return a random 6-digit passkey and print it via the ctl stream. |
| `DisplayPasskey(device, passkey, entered)` | Log. |
| `RequestConfirmation(device, passkey)` | SSP numeric comparison (modern PANU clients). Accept if a window is open, else reject. Log the passkey so the operator can compare in `palm-gwctl pair` output. |
| `RequestAuthorization(device)` | Accept if a window is open or the device is allowlisted. |
| `AuthorizeService(device, uuid)` | **The allowlist gate.** Accept if the device address is allowlisted (static or dynamic) or `allow_any`; otherwise return `org.bluez.Error.Rejected` and log `service_rejected addr=… uuid=…`. This is called for every profile connection because profiles are registered with `RequireAuthorization = true` and devices are left untrusted unless `trust_on_pair` marks them (trusted devices skip this call; see below). |
| `Cancel()` | Clear any pending PIN display. |
| `Release()` | Log; the daemon will re-register on the next BlueZ appearance. |

Device address: read `org.bluez.Device1.Address` (and `Name`/`Alias` for logs) from the object path BlueZ passes in.

**Pairing window** (`internal/agent/window.go`): state `{open bool, pin string, expires time.Time, paired []addr}`. Opening a window sets `Adapter1.Pairable=true`, generates the PIN (crypto/rand, `pin_length` digits, no leading-zero restriction), and starts a timer. Closing sets `Pairable=false`. While a window is open, the daemon subscribes to `PropertiesChanged` on `org.bluez.Device1` and when `Paired` or `Bonded` flips to `true` for a device (BlueZ holds `Paired` back until services resolve, which with D-037 never happens for a Palm; D-040), adds it to the dynamic allowlist, optionally sets `Trusted=true`, logs `paired addr=… name=…`, and streams the event to any `palm-gwctl pair` client attached to the ctl socket. The window closes early after `pair --one` pairs one device.

**Trusted vs allowlist**: if `trust_on_pair = true`, BlueZ will not call `AuthorizeService` for that device again, so the allowlist check in `AuthorizeService` never runs for it. That is acceptable (trusted implies allowlisted by construction), but `palm-gwctl forget <addr>` must therefore both remove the device from the allowlist **and** call `Adapter1.RemoveDevice(path)` so the trust and link key are gone. Document this.

### 8.3 RFCOMM profiles: SPP, LAP, DUN

Three `org.bluez.Profile1` objects at `/palm/spp`, `/palm/lap`, `/palm/dun`, registered with `org.bluez.ProfileManager1.RegisterProfile(path, uuid, options)` on `/org/bluez`:

| Path | UUID | Options |
|---|---|---|
| `/palm/spp` | `00001101-0000-1000-8000-00805f9b34fb` | `Name="Serial Port"`, `Role="server"`, `Channel=uint16(cfg.spp.channel)`, `RequireAuthentication=true`, `RequireAuthorization=true`, `AutoConnect=false`, `ServiceRecord=<Appendix C, SPP>` |
| `/palm/lap` | `00001102-0000-1000-8000-00805f9b34fb` | `Name="LAN Access using PPP"`, same flags, `ServiceRecord=<Appendix C, LAP>` |
| `/palm/dun` | `00001103-0000-1000-8000-00805f9b34fb` | `Name="Dial-up Networking"`, same flags, `ServiceRecord=<Appendix C, DUN>` |

**Always supply `ServiceRecord`.** BlueZ synthesises SDP records for a handful of well-known UUIDs (SPP among them) but LAP is an obsolete profile and the plan does not trust auto-generation for it. Supplying all three records explicitly also lets the record advertise the exact channel and a human-readable name that shows up in the Palm's service picker.

`NewConnection(device ObjectPath, fd UnixFD, fd_properties map[string]Variant) *dbus.Error`:

1. Convert `fd` to `*os.File` via `os.NewFile(uintptr(fd), "rfcomm")`. godbus has already received the descriptor over SCM_RIGHTS; **this process owns it** and must close it when the session ends. bluetoothd keeps its own reference until the link drops, so ending a session must `shutdown(SHUT_RDWR)` the socket, not just close it (D-028).
2. `unix.SetNonblock(fd, false)`. BlueZ may hand over a non-blocking socket; pppd expects blocking stdin/stdout.
3. Read the device address and name. Check `per_device_max`: if a live session exists for this address, terminate it (SIGTERM, 2 s, SIGKILL) and log `session_replaced`.
4. Check `max_sessions`; if full, close the fd, return `org.bluez.Error.Rejected`, log `session_refused reason=max_sessions`.
5. Run the handshake for this profile (8.4). The handshake may consume bytes; whatever it did not consume stays in the socket buffer for pppd.
6. Allocate a peer address from the pool; spawn pppd (8.5); record the session.
7. Return `nil` (success) promptly. **Do not block in `NewConnection` for the lifetime of the PPP session**; BlueZ has a method-call timeout and will consider the profile dead. Everything after step 2 that may take time (handshake wait up to 3 s is acceptable; pppd lifetime is not) runs in a goroutine, and `NewConnection` returns as soon as the fd has been taken.

`RequestDisconnection(device ObjectPath) *dbus.Error`: find the session by device, terminate pppd, return nil.

`Release()`: log; mark profiles unregistered so the re-registration loop runs.

godbus export: for each object, `conn.Export(profileImpl, path, "org.bluez.Profile1")` and `conn.Export(introspect.Introspectable(profile1XML), path, "org.freedesktop.DBus.Introspectable")`. The introspection XML must declare `NewConnection` with signature `oha{sv}` and `RequestDisconnection` with `o`, or BlueZ's `RegisterProfile` may succeed and then silently never call you. Method receivers must have the exact Go signatures godbus expects (`*dbus.Error` as the last return value).

### 8.4 Pre-PPP handshakes

Palm OS does not just start PPP. Depending on which connection type the user picked on the Palm, the first bytes on the RFCOMM link are one of three things. The handshake code peeks at the socket and handles all three; it is the same code for SPP and LAP, with the AT emulator added for DUN.

**Case A: Microsoft null-modem handshake** (Palm "Connect to: PC"; also Windows DUN clients). The client sends the ASCII string `CLIENT` (no terminator) and waits for `CLIENTSERVER`. It may repeat `CLIENT` every second or so until answered, and some clients send one more `CLIENT` after receiving the reply. If never answered, the Palm sits at "Signing on..." until its timeout and reports "The connection was lost" or similar. This is the number one cause of "it pairs but never connects".

**Case B: PPP straight away** (Palm "Connect to: Local Network" via LAP, some third-party Palm network stacks). First byte is `0x7E` (PPP flag) and the frame is an LCP Configure-Request.

**Case C: AT commands** (Palm "Connect to: Mobile Phone" via DUN). The Palm thinks the Pi is a phone and sends `AT&F\r`, `ATZ\r`, `ATE0V1\r` and so on, then dials `ATDT<number>\r` (the number is whatever the user typed; the plan tells them to enter `0`). It expects `\r\nOK\r\n` after each command and `\r\nCONNECT\r\n` (any speed suffix) after dialling, and only then starts PPP.

Algorithm (`internal/profile/handshake.go`), operating on the raw fd with `unix.Recvfrom(fd, buf, unix.MSG_PEEK)` so nothing is consumed unless intended:

```
deadline = now + cfg.handshake.ms_timeout_ms
          (DUN with at_emulator: cfg.handshake.at_timeout_ms instead; D-023)
loop:
  wait readable until deadline (poll/select on fd)
  if timeout: log "handshake_timeout, starting pppd blind"; return
  n = peek up to 64 bytes
  if n >= 1 and buf[0] == 0x7E:            # Case B
      log "handshake=none (ppp)"; return   # leave bytes for pppd
  if ms_null_modem and buf startswith "CLIENT":
      consume exactly len("CLIENT") bytes  # not more; a PPP frame might follow immediately
      write "CLIENTSERVER"
      log "handshake=ms_null_modem"
      deadline = now + 1500ms              # give it a moment for a repeat CLIENT or PPP
      continue
  if at_emulator and profile == DUN and buf startswith "AT" or "at":
      read one line up to '\r' (consume it); strip '\n'
      if line contains a D (dial) command: write "\r\n" + at_connect_banner + "\r\n"; log "handshake=at dial=<line>"; return
          # parse the whole command line, not just ^ATD: "ATQ0E0&D0&C1V1S7=90L1DT0" dials (D-024)
      else: write "\r\nOK\r\n"; deadline = now + 5000ms; continue
  otherwise:
      log "handshake=unknown first_bytes=<hex>"; return   # let pppd try; it will log LCP garbage if wrong
```

Unit tests use `net.Pipe` or a socketpair (`unix.Socketpair(AF_UNIX, SOCK_STREAM)`) to drive each case, including `CLIENT` split across two writes, `CLIENTCLIENT`, `CLIENT` immediately followed by `0x7E…`, lowercase `at`, and `ATDT0\r` arriving in the same read as `ATZ\r`.

Also honour `ATE0` loosely: the emulator never echoes anyway. Respond `OK` to everything that is not a dial, including `AT+CGDCONT=…` and `AT+CFUN`. Treat `ATH` as hang-up (close the socket, no pppd).

### 8.5 PPP spawner and session table

**Command line** per session:

```
/usr/sbin/pppd notty file /etc/ppp/options.palm noauth nocrtscts \
    <gateway>:<peer> ms-dns <gateway> \
    linkname palm-<peer_last_octet> \
    ipparam <bdaddr> \
    [debug] [idle N]
```

`exec.Cmd` setup:

- `Stdin = f`, `Stdout = f` (the RFCOMM `*os.File`), `Stderr =` a pipe that the daemon reads line by line and logs at INFO with `addr=`, `ifname=` tags. pppd with `nodetach` and `logfd 2` writes its connection log to stderr.
- `SysProcAttr = &syscall.SysProcAttr{Setpgid: true}` so that pppd **and the character-shunt child that `notty` forks** can be killed together with `syscall.Kill(-pid, SIGTERM)`. Without this the shunt lingers holding the RFCOMM socket and the channel never frees.
- The daemon keeps its copy of `f` and, when the session ends for any reason, calls `shutdown(SHUT_RDWR)` on it and then closes it, so the Palm sees the link drop (D-028). (Keep the fd number out of `ExtraFiles`; it is stdin/stdout.)
- `Wait()` in a goroutine; on exit release the pool address, remove the session, log `session_end addr=… ifname=… duration=… exit=…`.

Learning the interface name: pppd prints `Using interface pppN` to its log (stderr, thanks to `logfd 2`) as soon as the unit is created, before LCP finishes. The stderr reader goroutine parses that line (regex `^Using interface (ppp\d+)$`) and records the name; it also parses `local  IP address`, `remote IP address`, `LCP terminated by peer`, `No response to \d+ echo-requests` and `Connect time` lines into structured session fields. The `/run/ppp-palm-<n>.pid` file (pid, then interface name on line two) is read as a fallback if the log line has not appeared within 5 s. The `ip-up.d`/`ip-down.d` hooks are not required and are not shipped in v1; the daemon owns the lifecycle.

**`/etc/ppp/options.palm`** (rationale in Appendix D):

```
local
noauth
nodetach
logfd 2
nodefaultroute
noipv6
nocrtscts
noccp
novj
novjccomp
nobsdcomp
nodeflate
nopcomp
noaccomp
asyncmap 0
mtu 1500
mru 1500
lcp-echo-interval 20
lcp-echo-failure 3
lcp-max-configure 30
ipcp-accept-remote
```

`ms-dns` is passed on the command line from `config.network.gateway` rather than templated into the file, so the options file is static.

**Address pool** (`internal/ppp/pool.go`): inclusive range from config, hands out the lowest free address, holds it until the session ends, has unit tests for exhaustion and reuse. Peer addresses are deterministic enough for the firewall allowlist to reference ranges, and the session table maps them back to Bluetooth addresses for the logs.

**Session table**: map by Bluetooth address → `{pid, pgid, ifname, local, peer, profile, started, handshake}`. Exposed by `palm-gwctl status`. A reaper goroutine every 10 s cross-checks `pgrep -x pppd` against the table and logs `orphan_pppd pid=…` if a pppd exists that the daemon does not know about (this should never happen; it is a tripwire for fd or process leaks).

### 8.6 PAN (NAP)

`org.bluez.NetworkServer1` lives on the adapter object (`/org/bluez/hciN`).

- At startup, after the bridge exists (poll `/sys/class/net/br-palm` for up to 30 s, since NetworkManager brings it up asynchronously): `Register("nap", "br-palm")`.
- On shutdown: `Unregister("nap")`. Existing PAN connections survive this and a restarted daemon adopts them (D-034).
- BlueZ creates `bnepN` interfaces for each PANU client and enslaves them to the bridge itself. NetworkManager must be told not to manage `bnep*` (8.7) or it will try to DHCP on them and fight.
- Kernel module `bnep` must be loaded (`deploy/modules-load.d/palm-gw.conf`: `bnep`, `rfcomm`).
- PANU clients are also subject to `AuthorizeService` (BlueZ passes the BNEP UUID `0000000f-…`, not NAP's), so the allowlist applies to them too.
- **Amended by D-032:** trusted devices skip `AuthorizeService`, so the daemon also clears `Trusted` on devices the allowlist no longer accepts (at attach and on `reload`), follows PANU clients through the kernel's BNEP connection list (only NAP-role connections), and drops any that are not allowed. Ending a PAN connection is `BNEPCONNDEL` plus `Device1.Disconnect`, because bluetoothd keeps the L2CAP socket open (the PAN side of D-028).

### 8.7 Network plumbing

**Bridge via NetworkManager** (`deploy/systemd/palm-gwd-nm-bridge.service`, a oneshot that runs before `palm-gwd.service`, idempotent):

```sh
if ! nmcli -t -f NAME con show | grep -qx br-palm; then
  nmcli con add type bridge ifname br-palm con-name br-palm \
      ipv4.method manual ipv4.addresses "${GATEWAY}/${PREFIX}" \
      ipv6.method disabled bridge.stp no connection.autoconnect yes
fi
nmcli con up br-palm
```

`deploy/NetworkManager/conf.d/palm-gw.conf`:

```ini
[keyfile]
unmanaged-devices=interface-name:bnep*;interface-name:ppp*
```

If NetworkManager is not present (a user on a custom image), the installer falls back to a `systemd-networkd` `.netdev`/`.network` pair and records that in `DECISIONS.md`. Both paths must be in the repo; NetworkManager is the default.

**sysctl** `deploy/sysctl.d/90-palm-gw.conf`:

```
net.ipv4.ip_forward = 1
# Loose, not strict: PPP peers live inside the same /24 as br-palm and are reached via /32 routes that
# pppd installs; strict rp_filter is correct here but loose removes any dependence on route timing.
net.ipv4.conf.all.rp_filter = 2
net.ipv4.conf.default.rp_filter = 2
net.ipv4.conf.br-palm.proxy_arp = 0
```

**dnsmasq** as a project-owned instance (`dnsmasq-base`, unit `palm-gw-dnsmasq.service`, config `/etc/palm-gw/dnsmasq.conf` rendered from `deploy/dnsmasq/palm-gw.conf.tmpl`; the daemon rewrites it and restarts the unit on `palm-gwctl reload`). The system `dnsmasq.service`, if the user has one, is left alone.

```
# Only speak on the Palm gateway address. bind-dynamic (not bind-interfaces) so dnsmasq
# survives br-palm appearing after it starts, and binds the address itself so PPP peers
# (which arrive via pppN, not br-palm) are also served.
bind-dynamic
listen-address=10.42.0.1
interface=br-palm
except-interface=lo
no-dhcp-interface=eth0
no-dhcp-interface=wlan0

dhcp-range=10.42.0.100,10.42.0.199,255.255.255.0,12h
dhcp-option=option:router,10.42.0.1
dhcp-option=option:dns-server,10.42.0.1
dhcp-authoritative

domain-needed
bogus-priv
cache-size=1000
# Upstream: either follow the Pi's resolv.conf (default) or pin servers:
# no-resolv
# server=1.1.1.1
# server=9.9.9.9
```

**nftables** rendered from `deploy/nftables/palm-gw.nft.tmpl` to `/etc/nftables.d/palm-gw.nft`, included from `/etc/nftables.conf`. The `lan_allow` set is declared here and populated by the daemon at start and on reload with `nft -f -` on a generated `add element` script; the daemon never rewrites this file.

```nft
# Rendered from deploy/nftables/palm-gw.nft.tmpl: UPLINK and SUBNET come from config.
# No wildcard interface names inside sets (support varies by nft version); wildcards appear only
# in plain iifname/oifname rule matches, which every nftables since 0.9 supports.
table inet palm_gw {
    # Filled by palm-gwd from [[firewall.allow]]: dst addr . l4proto . dport
    set lan_allow {
        type ipv4_addr . inet_proto . inet_service
        flags interval
    }
    set lan_allow_icmp {
        type ipv4_addr
        flags interval
    }
    # Filled by palm-gwd from firewall.palm_to_pi (D-029)
    set pi_allow {
        type inet_proto . inet_service
    }

    # Classifier chains: the base chains only decide "did this come from the Palm side?"
    chain forward {
        type filter hook forward priority filter; policy accept;
        iifname "br-palm" jump palm_forward
        iifname "ppp*"    jump palm_forward
    }
    chain input {
        type filter hook input priority filter; policy accept;
        iifname "br-palm" jump palm_input
        iifname "ppp*"    jump palm_input
    }
    chain postrouting {
        type nat hook postrouting priority srcnat; policy accept;
        ip saddr {{SUBNET}} oifname "{{UPLINK}}" masquerade
    }

    chain palm_forward {
        ct state established,related accept
        ct state invalid drop
        ip daddr . meta l4proto . th dport @lan_allow accept
        ip daddr @lan_allow_icmp ip protocol icmp accept
        ip daddr { 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16, 100.64.0.0/10 } counter drop comment "palm->rfc1918"
        oifname "{{UPLINK}}" tcp flags syn tcp option maxseg size set rt mtu
        oifname "{{UPLINK}}" accept
        counter drop comment "palm->other"
    }
    chain palm_input {
        ct state established,related accept
        udp dport 53 accept
        tcp dport 53 accept
        udp dport 67 accept                                   # DHCP from PAN clients
        tcp dport 80 accept                                   # the "you are connected" page (section 8.12, D-039)
        icmp type { echo-request, destination-unreachable, time-exceeded } accept
        meta l4proto . th dport @pi_allow accept              # firewall.palm_to_pi (D-029)
        ip daddr {{GATEWAY}} tcp dport 8042 accept            # web UI and API over TLS (D-026, D-039); omitted when web.bluetooth_side = false
        tcp dport 8042 counter drop comment "palm->webui"     # explicit, for the counter: the LAN listener stays unreachable
        counter drop comment "palm->pi"                       # no SSH, no ctl socket, nothing else
    }
}
```

`{{UPLINK}}` and `{{SUBNET}}` are rendered at setup. Because the base chains have `policy accept` and only `jump` for Palm-side interfaces, this table coexists with any other firewall the user has (Docker, ufw, a hand-written ruleset) without hijacking it; the `palm_*` chains are still default-deny for Palm traffic because their last rule is `counter drop`. The `mss_clamp` line is omitted from the render when the option is false.

The daemon's generated element script, from the example config (all three sets are replaced in one transaction; hosts are written as masked prefixes and a host already covered by a wider entry for the same port is dropped, because nft rejects overlapping elements; the daemon re-applies it every 30 s so a `systemctl reload nftables` does not leave the sets empty):

```nft
flush set inet palm_gw lan_allow
flush set inet palm_gw lan_allow_icmp
flush set inet palm_gw pi_allow
add element inet palm_gw lan_allow { 192.168.1.20/32 . tcp . 14237, 192.168.1.20/32 . tcp . 14238, 192.168.1.0/24 . udp . 5353 }
add element inet palm_gw pi_allow { udp . 53, udp . 67 }
```

Unit test `internal/fw/nft_test.go` checks the generated text for several configs, including CIDR hosts and `icmp` (which has no port and therefore goes into `lan_allow_icmp`). A second test runs `nft -c -f` on the rendered template plus the generated elements when `nft` is present on the test host (skipped otherwise), so syntax errors are caught in CI's arm64 job, which runs on a Debian container with nftables installed.

### 8.8 Control socket and `palm-gwctl`

Unix socket `/run/palm-gw/ctl.sock`, mode `0660`, owner `root:palm-gw`. Protocol: one JSON object per line in each direction; long-running commands (`pair`) stream events until the client disconnects.

| Command | Request | Response |
|---|---|---|
| `status` | `{"cmd":"status"}` | adapter address/name/class/powered/discoverable/pairable, pairing window state, sessions `[ {addr,name,profile,ifname,local,peer,started,handshake} ]`, PAN clients from `/sys/class/net/br-palm/brif/`, allowlist, uptime, BlueZ version |
| `pair` | `{"cmd":"pair","minutes":5,"one":true}` | first line `{"event":"window_open","pin":"4821","expires":"…"}`, then `{"event":"pin_requested","addr":…}`, `{"event":"paired","addr":…,"name":…}`, `{"event":"window_closed","reason":"one_device"|"timeout"|"cancelled"}`. Disconnecting the client cancels the window. |
| `disconnect` | `{"cmd":"disconnect","addr":"…"}` | `{"ok":true}` |
| `forget` | `{"cmd":"forget","addr":"…"}` | removes from dynamic allowlist, `RemoveDevice` in BlueZ, kills session |
| `allow` | `{"cmd":"allow","addr":"…"}` | add to dynamic allowlist without pairing (device paired some other way) |
| `reload` | `{"cmd":"reload"}` | re-reads config, re-applies the nft sets, rewrites the dnsmasq conf and restarts dnsmasq if it changed; applies the allowlist and pairing settings; lists every other changed key as needing a restart (D-030) |
| `btmon` | `{"cmd":"btmon","seconds":60}` | runs `btmon -w /var/lib/palm-gw/captures/<ts>.btsnoop` for N seconds; response has the path |
| `events` | `{"cmd":"events"}` | streams every daemon event as it happens until the client disconnects (used by the web UI's SSE and by `palm-gwctl events`) |
| `config.get` / `config.set` | `{"cmd":"config.set","path":"firewall.allow","value":[…]}` | typed edits to `config.toml` through the daemon, validated with `config.Validate()` before writing, followed by an implicit `reload`; this is how the web UI and `palm-gwctl set` change settings without anyone hand-editing TOML |
| `check` | `{"cmd":"check"}` | the `setup --check` health report as JSON |
| `class.toggle` | `{"cmd":"class.toggle"}` | swaps `bluetooth.class` with `class_fallback`, rewrites the managed block in `main.conf`, restarts `bluetooth.service`, waits for re-registration; response has the class now in effect |
| `bundle` | `{"cmd":"bundle"}` | writes a support bundle to `/var/lib/palm-gw/bundles/<ts>.tar.gz` with secrets redacted; response has the path |

CLI mapping: `palm-gwctl status [--json]`, `palm-gwctl pair [minutes] [--one]`, `palm-gwctl disconnect <addr>`, `palm-gwctl forget <addr>`, `palm-gwctl allow <addr>`, `palm-gwctl rename <addr> <label>`, `palm-gwctl reload`, `palm-gwctl capture [seconds]`, `palm-gwctl events`, `palm-gwctl get <key>`, `palm-gwctl set <key> <value>` (e.g. `palm-gwctl set bluetooth.name "Zoë's Palm GW"`), `palm-gwctl firewall add <host> <proto/port>… [--comment]`, `palm-gwctl firewall rm <index>`, `palm-gwctl check`, `palm-gwctl class toggle`, `palm-gwctl bundle`, `palm-gwctl upgrade [--check]`. Every command has `--json`. Human output for `pair`:

```
Pairing window open for 5 minutes.
On the Palm: Prefs > Connection > New > Via: Bluetooth > tap "Palm Gateway" when it appears.
When asked for a passkey, enter:   4 8 2 1
Waiting...
  00:07:E0:A1:B2:C3  "Palm T|X"      PIN requested
  00:07:E0:A1:B2:C3  "Palm T|X"      paired, added to allowlist
Window closed (one device).
```

### 8.9 Logging, state and metrics

- `slog` JSON to stderr. Every log line has `component=` (agent|profile|ppp|pan|fw|ctl) and, where applicable, `addr=`, `name=`, `profile=`, `ifname=`.
- Event names are stable strings (`pin_requested`, `paired`, `pairing_rejected`, `service_rejected`, `session_start`, `session_end`, `session_replaced`, `session_refused`, `handshake`, `orphan_pppd`, `bluez_lost`, `bluez_found`) so they can be grepped and later shipped to a log pipeline.
- State file `/var/lib/palm-gw/state.json`: dynamic allowlist with `first_paired`, `last_seen`, `name`. Written atomically (temp + rename).
- `install-info.json` as per section 3.
- Metrics: out of scope for v1 beyond `palm-gwctl status --json`. Leave a `// TODO prometheus` with a design note: expose sessions, bytes (from `/sys/class/net/pppN/statistics`), pairing attempts.

### 8.10 systemd units and sandboxing

`deploy/systemd/palm-gwd.service`:

```ini
[Unit]
Description=Palm Bluetooth Gateway (SPP/LAP/DUN over PPP, PAN NAP)
Documentation=https://github.com/<owner>/palm-gw
After=bluetooth.service NetworkManager.service palm-gwd-nm-bridge.service nftables.service palm-gw-dnsmasq.service
Wants=bluetooth.service palm-gwd-nm-bridge.service nftables.service palm-gw-dnsmasq.service
# Not Requires=bluetooth.service: the daemon handles BlueZ going away and coming back itself.

[Service]
Type=notify
ExecStart=/usr/local/bin/palm-gwd -config /etc/palm-gw/config.toml   # {{BIN_DIR}}: /usr/bin in the .deb (D-042)
ExecReload=/usr/local/bin/palm-gwctl reload
Restart=always
RestartSec=3
RuntimeDirectory=palm-gw
RuntimeDirectoryMode=0750
StateDirectory=palm-gw
WatchdogSec=60

# Hardening. Start here; relax only what breaks, and document each relaxation in DECISIONS.md.
User=root
ProtectSystem=strict
ReadWritePaths=/run /var/lib/palm-gw /var/log /etc/ppp
ProtectHome=yes
PrivateTmp=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
RestrictRealtime=yes
RestrictNamespaces=yes
SystemCallArchitectures=native
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK AF_BLUETOOTH
CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_RAW CAP_NET_BIND_SERVICE CAP_KILL CAP_SETUID CAP_SETGID CAP_CHOWN CAP_DAC_OVERRIDE CAP_FOWNER CAP_SYS_ADMIN
AmbientCapabilities=
NoNewPrivileges=no       # pppd needs to keep privileges; keep this `no` unless tested otherwise

[Install]
WantedBy=multi-user.target
```

Sandbox settings and why each is safe here, so no trial and error is needed: `ProtectKernelTunables=yes` makes `/proc/sys` read-only, which is fine because the project sets sysctls via `sysctl.d` and pppd is run without `ktune`. `ProtectKernelModules=yes` removes `CAP_SYS_MODULE`; kernel autoloading of `rfcomm`, `bnep`, `ppp_generic` and `ppp_async` on socket or `/dev/ppp` open still works because that is done by the kernel, not the process, and `modules-load.d` preloads them anyway. `RestrictAddressFamilies` includes `AF_NETLINK` (nft, pppd route changes) and `AF_BLUETOOTH` (not used directly by the daemon, which only receives fds, but pppd's shunt inherits a Bluetooth socket). `MemoryDenyWriteExecute=yes` is fine for Go and for pppd. `DevicePolicy=closed` plus `DeviceAllow=/dev/ppp rw` and `DeviceAllow=/dev/ptmx rw` and `DeviceAllow=/dev/pts/* rw` are **required** because `notty` creates a pseudo-tty pair; add all three to the unit from the start. `ProtectSystem=strict` with `ReadWritePaths=/run /var/lib/palm-gw /var/log /etc/ppp /etc/palm-gw` covers pppd's pid files, the state file, the config writes from the web UI and the dnsmasq config rewrite. `PrivateTmp=yes` is fine. Do **not** set `PrivateDevices` (hides `/dev/ppp`), `PrivateNetwork` (obviously), `ProtectProc=invisible` (the orphan tripwire reads `/proc`), or `NoNewPrivileges=yes` (pppd calls `setuid` back and forth around privileged options).

Debian's stock `/etc/ppp/options` is always read by pppd before any `file` option and contains `auth`, `lock`, `crtscts` and echo settings. Later options override earlier ones, and `noauth` is a privileged option that root may set, so `options.palm` wins; the daemon nevertheless passes `noauth` and `nocrtscts` again on the command line after `file options.palm` so the result does not depend on the distribution's defaults. `lock` is harmless with `notty`.

`Type=notify` + `WatchdogSec`: the daemon calls `sd_notify READY=1` after profiles are registered and `WATCHDOG=1` every 20 s from the main loop (use `github.com/coreos/go-systemd/v22/daemon`). If the D-Bus loop wedges, systemd restarts us.

### 8.11 Installation: three paths, one wizard

The user experience target is: flash Pi OS Lite, SSH in, paste one line, answer three or four questions, reboot if told to, open `https://<pi>:8042`. Everything below exists to make that true on both boards.

**Path A: bootstrap one-liner (primary).**

```sh
curl -fsSL https://github.com/<owner>/palm-gw/releases/latest/download/install.sh | sudo bash
```

`packaging/bootstrap-install.sh`:
1. Refuses non-root, non-Linux, non-arm64, and non-Raspberry-Pi (`/proc/device-tree/model`), with a clear message for each.
2. Checks for `apt`, `systemd`, and that `/boot/firmware/config.txt` exists (Bookworm+ layout). On Bullseye or older: stop and say so.
3. Fetches the latest release's `palm-gw_<ver>_arm64.deb` and its `SHA256SUMS`, verifies the checksum (and the `minisign`/`cosign` signature if the project publishes one; design for it, implement as a flag `--verify-sig`).
4. `apt-get install -y ./palm-gw_<ver>_arm64.deb` (pulls `bluez`, `ppp`, `dnsmasq`, `nftables`, `network-manager` as package dependencies, so no separate apt step).
5. Runs `palm-gw setup` interactively if stdin is a TTY (note: `curl | sudo bash` leaves stdin attached to the pipe; the script must re-open `/dev/tty` for the wizard, or print `Run: sudo palm-gw setup` when no TTY is available).
6. Supports `PALM_GW_VERSION=v1.2.0` and `PALM_GW_UNATTENDED=1` with `PALM_GW_UPLINK=eth0 PALM_GW_ADAPTER=onboard PALM_GW_WEB_PASSWORD=…` for scripted installs.

**Path B: the .deb directly.** `sudo apt install ./palm-gw_<ver>_arm64.deb` then `sudo palm-gw setup`. `packaging/nfpm.yaml` defines:
- Files: binaries to `/usr/bin/` (`palm-gwd`, `palm-gwctl`, `palm-gw`, `fake-palm` goes in a separate `palm-gw-tools` package or `/usr/libexec/palm-gw/`), templates to `/usr/share/palm-gw/templates/`, units to `/usr/lib/systemd/system/`, docs to `/usr/share/doc/palm-gw/`.
- Conffiles: `/etc/palm-gw/config.toml` (only created by setup; shipped as `config.example.toml` under `/usr/share/doc/palm-gw/`).
- Depends: `bluez (>= 5.66), ppp (>= 2.4.9), dnsmasq-base | dnsmasq, nftables, network-manager`. Note `dnsmasq-base` vs `dnsmasq`: the full `dnsmasq` package ships a system service that binds port 53 on all interfaces by default and will conflict; the plan depends on `dnsmasq` and ships a drop-in that restricts it, **or** runs its own `dnsmasq` instance from `palm-gw-dnsmasq.service` using `dnsmasq-base` with `--conf-file=/etc/palm-gw/dnsmasq.conf`. **Decided: own instance from `dnsmasq-base`**, so the project never fights a user's existing dnsmasq. Update section 8.7 accordingly: the unit is `palm-gw-dnsmasq.service`, `ExecStart=/usr/sbin/dnsmasq -k --conf-file=/etc/palm-gw/dnsmasq.conf --pid-file=/run/palm-gw/dnsmasq.pid`.
- `postinst`: create `palm-gw` group, `systemctl daemon-reload`, enable `palm-gwd-nm-bridge`, `palm-gw-dnsmasq`, `nftables`, `palm-gwd` but **do not start** `palm-gwd` until a config exists (`ConditionPathExists=/etc/palm-gw/config.toml` in the unit); print "Run `sudo palm-gw setup` to finish."
- `prerm`/`postrm`: stop units; on purge remove `/etc/palm-gw`, `/var/lib/palm-gw`, the NM `br-palm` connection, the include line in `/etc/nftables.conf`, and the `dtoverlay=disable-bt` line **only if** `install-info.json` says the package added it.

**Path C: from source.** `make install` builds and runs the same steps as the .deb's file layout plus `postinst`, for developers. Tracks placed files in `/var/lib/palm-gw/installed-files.txt` for `make uninstall`. **Amended by D-042:** the static files are written by `palm-gw setup --install-files --layout source` and everything rendered from `config.toml` by `palm-gw setup --apply`, the same step the wizard and the `.deb` run; `make uninstall` is `palm-gw setup --remove`.

**The wizard: `palm-gw setup`.** Interactive when on a TTY, flag-driven otherwise (`--uplink`, `--adapter`, `--name`, `--web-password-file`, `--subnet`, `--yes`). It never asks a question it can answer by looking.

Detection (`internal/setup/detect.go`), each with a unit test using fixture files:
- Board: `/proc/device-tree/model` → pi4 | pi5 | other (warn, continue).
- Adapters: BlueZ `ObjectManager` → list of `hciN` with `Address`, sysfs bus (usb vs onboard), `Powered`. If zero: check `config.txt` for `disable-bt`, check `dmesg` for firmware load failure, and explain.
- Uplink candidates: interfaces that are up with a default route (`ip -j route show default`), excluding `br-palm`, `bnep*`, `ppp*`, `lo`, `docker*`, `veth*`. One candidate → no question. Several → ask. For `wlan0`: read band via `iw dev wlan0 link` and warn on 2.4 GHz with the onboard adapter.
- Pi 4 console UART conflict: grep `cmdline.txt`.
- Pi 5 PSU: `vcgencmd get_throttled`, `/proc/device-tree/chosen/power/max_current` → warn if dongle mode and under 5 A.
- Existing config: if present, pre-fill answers and offer "keep / change".
- Subnet collision: if `10.42.0.0/24` overlaps any address on the Pi or any route, propose `10.43.0.0/24` and so on.

Questions (only those not resolved by detection):
1. Gateway name as the Palm will see it [Palm Gateway].
2. Bluetooth adapter: onboard / USB dongle (hciN, address) / disable onboard and use dongle (requires reboot).
3. Uplink interface (if more than one candidate).
4. Web UI: enable? [Y] → password (twice, masked; or `--web-password-file`), TLS self-signed? [Y].
5. Open a pairing window now? [Y] → prints the PIN and the Palm steps from section 11, waits up to 5 minutes, then continues. This makes the first Palm pair during setup, which is the moment the user is most motivated.

Then the wizard writes `config.toml`, renders every templated system file (`internal/setup/render.go`, Go `text/template` over `deploy/`), applies `config.txt` changes if needed, runs `nft -c`, `(re)starts` units in the right order, waits for `palm-gwd` to report READY, and prints:

```
palm-gw is running.
  Web UI:    https://192.168.1.50:8042   (user: admin)
  CLI:       palm-gwctl status
  Pair:      palm-gwctl pair   (or the Pair button in the web UI)
  Docs:      /usr/share/doc/palm-gw/PALM-SETUP.md
Reboot required: no
```

`palm-gw setup --check` runs detection only and prints a health report (used by `postinst` and by the web UI's Diagnostics page). `palm-gw upgrade` fetches the latest .deb and installs it, preserving config; `palm-gwctl upgrade` is an alias.

`Makefile` targets:

| Target | Does |
|---|---|
| `build` | `CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(git describe)"` for host |
| `build-arm64` | same with `GOOS=linux GOARCH=arm64` into `dist/` |
| `test`, `lint`, `vet` | obvious |
| `install` | runs `scripts/install.sh` (needs root); accepts `ADAPTER=onboard|dongle` (unset keeps the current mode, D-042), `UPLINK=eth0|wlan0`, `CONFIG=path` |
| `uninstall` | `scripts/uninstall.sh`: stops units, removes every file the installer placed (tracked in `/var/lib/palm-gw/installed-files.txt`), removes the `include` line it added to `/etc/nftables.conf`, deletes the `br-palm` NM connection, leaves `/etc/palm-gw/config.toml` and the state file unless `PURGE=1` |
| `support-bundle` | `scripts/support-bundle.sh` → `palm-gw-support-<ts>.tar.gz` |
| `deb` | `nfpm package -p deb` → `dist/palm-gw_<ver>_arm64.deb` |
| `release` | `deb` plus `dist/SHA256SUMS`, `dist/install.sh` (the bootstrap script with the version baked in), and a GitHub release via `gh release create` |

### 8.12 Management web UI

Served by `palm-gwd` itself (same process, so it reads the session table directly and does not need a second service account for the ctl socket). Go `net/http`, `html/template`, `embed.FS`, htmx for partial refresh; no build tooling. Total front-end footprint target: under 60 KB.

**Binding and exposure.** The daemon enumerates addresses at start and binds `[web].listen` only on interfaces that are not `br-palm`, `bnep*`, or `ppp*`; it re-evaluates on `InterfacesAdded`/netlink changes. Independently, the nftables `input` chain drops port 8042 from Palm interfaces (section 8.7 `input` chain's final `counter drop` already does this; add an explicit `tcp dport 8042 counter drop` line above it for clarity). The full UI is therefore reachable from the home LAN and never from a Palm. **Amended by D-026 and D-039:** a second listener on the gateway address (`10.42.0.1:8042`), TLS only, serves the full route set and the `/api/v1/` API to the Bluetooth side for the ARM Palm OS app (`palm-app/`) and PAN laptops; the firewall accepts 8042 to the gateway address only. Details in `docs/WEB-UI.md`.

**Auth.** Single admin account with a username (D-026; optional read-only viewer account in `docs/WEB-UI.md`), password hashed with argon2id (`golang.org/x/crypto/argon2`), HttpOnly Secure SameSite=Strict session cookie, CSRF token on every mutating form, rate limit of 5 failed logins per minute per source IP. `palm-gw web set-password` resets it from the shell. With `tls = "self-signed"`, a cert for the Pi's hostname and current IPs is generated at setup into `/var/lib/palm-gw/tls/` and its fingerprint is printed so the user can pin it; the login page shows the same fingerprint.

**Pages.**

| Route | Content |
|---|---|
| `/` Dashboard | Adapter state (name, address, powered, discoverable, pairable with countdown if a window is open); live sessions table (device name, address, profile, interface, IP, duration, bytes from `/sys/class/net/<if>/statistics`); PAN clients; uplink state; last 10 events. Refreshes every 3 s via htmx polling of `/partials/*`. |
| `/pair` | Big **Open pairing window** button (minutes selector, "close after one device" checkbox). While open: the PIN in very large type, the Palm steps from section 11 next to it, and a live event feed (PIN requested, paired, rejected) streamed with SSE (`/events`). **Close window** button. |
| `/devices` | Allowlisted devices: name, address, first paired, last seen, connected now, trusted. Per row: Disconnect, Forget (confirm dialog), Rename (local label stored in state.json). "Add by address" form for devices paired elsewhere. |
| `/firewall` | Table of `[[firewall.allow]]` entries with add/edit/delete; a "test reachability" button that runs a TCP connect from the Pi to the host:port and reports; shows live counters for the drop rules so the user can see what a Palm tried to reach. Saves to `config.toml` (round-tripped with `toml` preserving comments where the library allows; if not, a managed block `# BEGIN palm-gw web` … `# END` is used) and triggers `reload`. |
| `/settings` | Gateway name, adapter choice (lists adapters), uplink choice, subnet, PPP options (idle timeout, debug toggle), web password change, TLS mode. Each save validates via the same `config.Validate()` and shows errors inline; applies via `reload` or, where a restart is needed (adapter change), says so and offers a Restart button. |
| `/logs` | Last 500 journal lines for `palm-gwd`, `palm-gw-dnsmasq`, `bluetooth` (via `journalctl -o json -u … -n 500`), filterable by event name and device address; Follow toggle (SSE). |
| `/diagnostics` | Output of `palm-gw setup --check` (board, adapters, PSU, UART conflict, Wi-Fi band, module presence, unit states); **Capture 60 s of btmon** button that produces a downloadable `.btsnoop`; **Download support bundle** button (`scripts/support-bundle.sh` equivalent in Go: journals, configs with password hash redacted, install-info, last capture). |
| `/about` | Version, build, update check (`palm-gw upgrade --check`; opt-in, off by default, never automatic). |

**"You are connected" page** (amended by D-039): served **only** on the gateway address (the opposite binding rule), a small HTML5 page for Linux laptops on PAN, showing "You are connected. IP: x. Gateway: y." and a link to `https://10.42.0.1:8042/`, at `http://10.42.0.1/`. It is on port 80, read-only and unauthenticated. Palms use the app instead of Blazer.

**Tests.** `httptest` coverage of auth (no cookie → redirect, bad CSRF → 403, rate limit), every mutating route against a fake ctl backend, and template rendering with an empty state. A Playwright smoke test in CI against the daemon running with a mock BlueZ is a stretch goal; record as such.

`scripts/install.sh` steps (Path C, and what the .deb `postinst` plus `palm-gw setup` do between them), each idempotent and each echoing what it did or that it was already done. Since D-042, steps 5 to 11 are `palm-gw setup --install-files` and `palm-gw setup --apply`:

1. Refuse to run without root; detect board (section 3); refuse non-arm64 OS.
2. `apt-get install -y bluez ppp dnsmasq nftables` (and `network-manager` if missing, with a warning that this changes network management).
3. Create group `palm-gw`; add the invoking `SUDO_USER` to it.
4. Install binary to `/usr/local/bin/palm-gwd`, symlink `palm-gwctl`.
5. Place all `deploy/` files, substituting `GATEWAY`, `PREFIX`, `UPLINK`, `DHCP_*` from the config (write config from `config.example.toml` first if none exists, honouring `UPLINK=`).
6. Append `include "/etc/nftables.d/palm-gw.nft"` to `/etc/nftables.conf` if absent; `nft -c -f /etc/nftables.conf` to check; enable `nftables.service`.
7. `ADAPTER=dongle`: add `dtoverlay=disable-bt` to `config.txt`; `systemctl disable --now hciuart 2>/dev/null || true`; set `REBOOT_REQUIRED=1`.
8. Pi 4 only: scan `cmdline.txt` for `console=serial0`/`ttyAMA0` and warn (section 3).
9. Check Wi-Fi uplink band (`iw dev wlan0 link`) and warn if 2.4 GHz with onboard adapter.
10. `systemctl daemon-reload; systemctl restart bluetooth; systemctl enable --now palm-gwd-nm-bridge palm-gw-dnsmasq nftables palm-gwd`.
11. Write `install-info.json`; print next steps: `palm-gwctl pair`, link to `docs/PALM-SETUP.md`, and "reboot now" if required.

---

## 9. Test tooling: `fake-palm`

A Linux machine with its own Bluetooth adapter (a laptop, or a second Pi) stands in for a Palm so the server can be developed and regression-tested without touching a 20-year-old device every iteration. `cmd/fake-palm` is Go, uses `golang.org/x/sys/unix` raw `AF_BLUETOOTH` sockets, and does not depend on deprecated BlueZ tools.

Subcommands:

- `fake-palm discover`: run an inquiry via BlueZ D-Bus (`Adapter1.StartDiscovery`), print devices with Class and Name, assert the gateway shows up with the expected class.
- `fake-palm sdp <addr>`: use BlueZ `Device1.Connect`/`UUIDs` property after `Pair` to list advertised service UUIDs; assert SPP, LAP, DUN present. (Full SDP record parsing is not required; `UUIDs` is enough. If `sdptool` happens to exist, `fake-palm sdp --raw` shells out to it for the full record.)
- `fake-palm pair <addr> --pin 4821`: register a minimal Agent1 on the client side that answers `RequestPinCode` with the given PIN, call `Device1.Pair`. This exercises **legacy pairing** from the client side only if the client adapter is forced to legacy; otherwise SSP is used. Document that real legacy pairing is only tested with a real Palm, and use `btmon` on the Pi to confirm `PIN Code Request` events when the Palm pairs.
- `fake-palm connect <addr> --channel 1 --mode client|ppp|at`: open an RFCOMM socket (`unix.Socket(AF_BLUETOOTH, SOCK_STREAM, BTPROTO_RFCOMM)`, `SockaddrRFCOMM{Addr, Channel}`), then:
  - `client`: send `CLIENT`, expect `CLIENTSERVER` within 3 s, then run pppd.
  - `ppp`: run pppd immediately.
  - `at`: send `ATZ\r`, expect `OK`; `ATDT0\r`, expect `CONNECT`; then run pppd.
  - "run pppd" = `pppd notty noauth nodetach noipdefault ipcp-accept-local ipcp-accept-remote usepeerdns nodefaultroute novj noccp file /dev/null` with the socket as stdin/stdout, same `Setpgid` handling as the server. With `--netns` (fake-palm on the gateway itself) it drops `usepeerdns` and uses `defaultroute` instead (D-027). Then the test asserts a `pppN` interface appears with a `10.42.0.x` peer, pings `10.42.0.1`, resolves a name through `10.42.0.1`, and (if `--internet`) fetches `http://neverssl.com/` bound to that interface.
- `fake-palm storm <addr> --count 20 --interval 500ms`: connect/disconnect rapidly to exercise `per_device_max`, pool reuse and process-group cleanup. Asserts afterwards that `pgrep pppd` on the server (via `palm-gwctl status` over SSH, or just inspection) shows exactly zero sessions.
- `fake-palm pan <addr>`: connect as PANU via `org.bluez.Network1.Connect("nap")`, assert DHCP lease in `10.42.0.100–199`, ping, DNS, internet. With `--netns` (fake-palm on the gateway itself) it moves the `bnepN` interface into the namespace first; DHCP is one `busybox udhcpc` exchange. BlueZ ends a `Network1` connection when its caller leaves the bus, so `--hold` keeps the process (and the connection) alive.

`docs/TESTING.md` records the exact invocations and expected output for each acceptance test in section 10.

---

## 10. Build phases with acceptance tests

Work strictly in order. Do not start a phase until the previous phase's acceptance test passes **on both a Pi 4 and a Pi 5** (or, where only one board is available, on that board with the other recorded as untested in `DECISIONS.md`).

### Phase 0: Scaffold
Module, config loader and validation, slog setup, ctl socket skeleton with `status` returning config echo, Makefile, CI (vet, lint, test, arm64 build), `install.sh` that places files but does not yet enable `palm-gwd`.
**Accept:** `make test lint build-arm64` green on macOS and on the Pi. `palm-gwd -config bad.toml` exits 1 with a message naming the offending key. `make install` on a fresh image completes without error and `systemctl status nftables palm-gw-dnsmasq palm-gwd-nm-bridge` are all active; `ip addr show br-palm` shows `10.42.0.1/24`.

### Phase 1: Adapter and agent
BlueZ connection with wait-for-name loop, adapter discovery (`hciN` and `auto`), property setting, Agent1 with pairing window, dynamic allowlist and state file, `palm-gwctl pair/forget/allow/status`.
**Accept:** From a laptop, `bluetoothctl scan on` shows "Palm Gateway" with the expected class (check `Class:` in `bluetoothctl info`). `palm-gwctl pair 2 --one` prints a PIN; pairing from the laptop succeeds and the window closes; the address appears in `status` and `state.json`. Pairing with no window open is refused (`btmon` shows the rejection). `palm-gwctl forget` removes the device from both the state file and `bluetoothctl devices`. Kill `bluetooth.service` and restart it: the daemon logs `bluez_lost` then `bluez_found` and `status` is healthy within 10 s.

### Phase 2: RFCOMM profiles and PPP
Profile registration with explicit SDP records for SPP/LAP/DUN, `NewConnection` fd handling, handshake (all three cases), pppd spawner with process groups, pool, session table, orphan tripwire.
**Accept:** `fake-palm connect --mode client` on channel 1 gets `CLIENTSERVER`, a `pppN`, a `10.42.0.1x` address, and pings `10.42.0.1`. `--mode ppp` on channel 2 and `--mode at` on channel 3 both succeed. `fake-palm storm` leaves zero sessions and zero `pppd` processes. `palm-gwctl disconnect` tears a session down within 3 s. Then the **real Palm T|X**: with the connection set up per section 11, tapping Connect reaches "Established" and Blazer loads `http://10.42.0.1/` (serve a one-line page with `python3 -m http.server` on the Pi for this test, bound to 10.42.0.1; remember to allow it in `palm_to_pi` temporarily) and `http://neverssl.com/`.

### Phase 3: Routing, NAT and isolation
Daemon-managed `lan_allow` set, MSS clamp, dnsmasq templating, `reload`.
**Accept:** From `fake-palm` (and the T|X): internet works; `nmap -sT -Pn 192.168.1.0/24 -p 22,80,443 -e pppN` from the fake-Palm side shows nothing open except hosts/ports in `[[firewall.allow]]`; `dig @10.42.0.1 example.com` answers; `ssh 10.42.0.1` from the Palm side is dropped (not refused); `nft list counters` shows the drop rule incrementing. Add a `[[firewall.allow]]` block, `palm-gwctl reload`, and the new host is reachable without restarting anything.

### Phase 4: PAN
Bridge polling, `NetworkServer1.Register`, NM unmanaged config, DHCP for PANU.
**Accept:** `fake-palm pan` gets a lease in the DHCP range and reaches the internet with the same isolation. An Android phone paired through a window and toggled "Internet access" on the Pi entry also works. (D-033: accepted on fake-palm alone; the Android check moves to Phase 5.) `brctl show`/`bridge link` shows `bnep0` enslaved; NetworkManager does not list `bnep0` as a managed device.

### Phase 5: Robustness and both boards
Reconnect storms with the real Palm (toggle Connect/Disconnect 20 times), T|X dial-up carrier detect on an RFCOMM TTY (D-025; not possible from user space, D-036), BlueZ restart mid-session, `systemctl restart palm-gwd` mid-session, power-cycle the Pi with a Palm connected, USB dongle mode on both boards, Wi-Fi uplink mode, 24-hour soak with a Palm connected and `lcp-echo` keeping the link up (deferred to field use, D-038).
**Accept:** No orphans, no fd leaks (`ls /proc/$(pidof palm-gwd)/fd | wc -l` stable across 100 sessions), all units active after each disruption, soak link survives or reconnects on first tap. Both Pi 4 and Pi 5 pass identically; differences recorded.

### Phase 6: Setup wizard and packaging
`internal/setup` detection and wizard, template rendering, `.deb` via nfpm with postinst/prerm/postrm, bootstrap `install.sh`, `palm-gw upgrade`, own-instance dnsmasq unit.
**Accept:** On a freshly flashed Pi OS Lite image on each board: the one-liner installs, the wizard detects board and uplink correctly without asking, asks only for name/adapter/password, pairs a Palm during setup, and the Palm has internet before the wizard exits. Unattended mode (`PALM_GW_UNATTENDED=1` with env answers) produces the same result with no prompts. `apt install` of a newer .deb over an older one preserves config and the paired-device state and leaves the gateway running. `apt purge` leaves `/etc` clean (diff against a pre-install snapshot). Running `palm-gw setup` a second time pre-fills and changes only what the user changes.

### Phase 7: Web UI
Server, auth, all pages in section 8.12, the port-80 "you are connected" page, nftables rule for 8042, the TLS-only Bluetooth-side listener, `/api/v1/`, and the ARM Palm OS app in `palm-app/` (D-026, D-039, `docs/WEB-UI.md`).
**Accept:** From a LAN browser: log in, open a pairing window, see the PIN, pair a Palm and watch the event feed; disconnect and forget devices; add a firewall allow entry and verify a Palm can reach it immediately; change the gateway name and see the Palm's device list show the new name after a re-scan; download a btmon capture and open it in Wireshark; download a support bundle and confirm the password hash is redacted. From a PAN laptop: `http://10.42.0.1/` shows the connected page. From the Bluetooth side (fake-palm, a PAN laptop, and the Palm app on the T|X): `https://10.42.0.1:8042/` logs in, plain HTTP on 8042 gets no answer, the app opens a pairing window and shows its PIN, disconnects and forgets a device and adds a firewall rule, and the Pi's LAN address on 8042 is dropped (D-026, D-039). `nmap` from the Palm side confirms only 53/67/80/8042 and ICMP are reachable on the Pi, 8042 only on `10.42.0.1`.

### Phase 8: Documentation and release
`PALM-SETUP.md` (section 11) with photos or exact screen text from a T|X and a LifeDrive; `TROUBLESHOOTING.md` populated with real log excerpts from every failure induced in Phase 5; `TESTING.md`; `DECISIONS.md` complete; `README.md` with a 60-second quick start (the one-liner, the wizard, the web URL); `make release` artefact installed from scratch on a clean image of each board as the final test.

---

## 11. Palm-side setup (what the user does on the handheld)

Written here so the implementer understands what the server is talking to; `docs/PALM-SETUP.md` expands it for the end user with screenshots.

### Palm T|X and LifeDrive (Palm OS 5.4, Garnet)

**Pairing**
1. On the Pi: `palm-gwctl pair 5` and read the PIN.
2. Palm: **Prefs ▸ Communication ▸ Bluetooth**. Bluetooth: **On**. Device name: anything. Discoverable: Yes (not required but harmless). Tap **Setup Devices ▸ Trusted Devices ▸ Add Device**. The Palm runs an inquiry (about 10 s). Tap **Palm Gateway**. Enter the PIN when asked. The device is now trusted on the Palm.

**Connection (recommended: "PC" type, uses SPP + Microsoft handshake)**
3. **Prefs ▸ Communication ▸ Connection ▸ New**. Name: `Palm GW`. Connect to: **PC**. Via: **Bluetooth**. Device: tap and pick **Palm Gateway**. **Details**: Speed is irrelevant for Bluetooth; Flow control: Automatic. OK.

**Alternative connection types** (document as alternatives; the server supports all three):
- Connect to: **Local Network** via Bluetooth, if that option appears on the device, uses LAP (channel 2) and goes straight to PPP.
- Connect to: **Mobile Phone** via Bluetooth, Phone: **Standard GSM** or any; uses DUN (channel 3). In the Network service the phone number must be set to `0` (anything the AT emulator can treat as `ATD…`).

**Network service**
4. **Prefs ▸ Communication ▸ Network**. Service: **New** (or Menu ▸ New). Name: `Palm GW`. Connection: **Palm GW**. User Name: leave **blank**. Password: leave **blank** (tap and set to "unassigned" if it insists). **Details**: Connection type: **PPP**. Idle timeout: Never (or the Palm will drop the link; the Pi's `idle_seconds` is the better place for this). Query DNS: **checked**. IP Address: **Automatic**. OK.
5. Tap **Connect**. Expected sequence on the Palm: "Opening connection..." → "Signing on..." (this is where the `CLIENTSERVER` handshake happens) → "Established". Then open Blazer.

**Built-in diagnostics on the Palm.** In the Network preferences, Menu ▸ Options ▸ **View Log** shows the PPP negotiation as the Palm sees it (LCP, IPCP, assigned address, DNS). At the bottom of the log screen you can type `PING hostname` to test DNS and routing from the Palm without opening Blazer. `docs/PALM-SETUP.md` includes this; it is the Palm-side counterpart of `btmon`.

**Palm OS 4 devices** (Tungsten T, original m-series with the SDIO card) sometimes do not send `CLIENT` themselves. The period workaround was a login script under Network ▸ Details ▸ Script: `Send CR`, `Delay 1`, `Send: CLIENT`, `Wait for: CLIENTSERVER`, `End`. The server handles both behaviours already (it answers `CLIENT` if it arrives and starts PPP directly if the first byte is a PPP flag), so the script is optional, but document it for completeness.

**Treo 650/680/700p and Tungsten T3/T5**: same screens, slightly different wording ("Prefs ▸ Connection" is top-level). Zire 72: identical to T|X.

**What the Palm must not have**: a Network service pointing at the old Wi-Fi connection with "Auto-connect" ticked, which will steal the Connect button. And on a T|X, if Wi-Fi is on, Palm OS prefers Wi-Fi for NetLib; turn Wi-Fi off in the Wi-Fi preferences while testing Bluetooth.

### Why the Palm needs the Microsoft handshake

Palm's "PC" connection type was designed for Windows XP's **Incoming Connection** feature over a Bluetooth serial port. Windows RAS over a null-modem link does a two-word pre-PPP exchange so that both ends know a modem is not in the way. Palm implemented the client side. Nothing on the Palm lets you turn it off, so every server that wants to talk to a Palm "PC" connection must answer it.

---

## 12. Known traps and how to recognise them

| Symptom | Likely cause | Confirm with | Fix |
|---|---|---|---|
| Palm pairs, then "Signing on..." forever, then "connection lost" | No `CLIENTSERVER` reply | `btmon` shows RFCOMM data `43 4C 49 45 4E 54` ("CLIENT") from the Palm and nothing back; daemon log `handshake_timeout` | `ms_null_modem = true`; check the handshake code consumed exactly 6 bytes |
| Palm cannot find the Pi in Add Device | Not discoverable, wrong class, Wi-Fi coexistence | `bluetoothctl show` → `Discoverable: yes`, `Class:`; `btmon` shows inquiry responses? | Set `Discoverable`; try class `0x020104`; disable Wi-Fi or use dongle |
| Palm finds the Pi but "Unable to connect" during pairing | No pairing window (agent rejected), or PIN mismatch | daemon log `pairing_rejected reason=no_window`; `btmon` shows `PIN Code Request` then `PIN Code Request Negative Reply` | open a window; re-enter PIN |
| Pairs and connects on first try, never again after Palm reset | Palm lost its link key, Pi still has old one | `btmon` shows `Link Key Request` → Palm `Negative Reply` → `PIN Code Request` | `JustWorksRepairing = always` (already set); if still stuck, `palm-gwctl forget <addr>` and re-pair |
| Palm pairs (PIN accepted) but its first Connect is refused | The daemon missed the pairing, so the Palm was not allowlisted (D-040) | daemon log has `pin_requested` but no `paired`; `service_rejected` on Connect; `busctl get-property org.bluez <dev> org.bluez.Device1 Bonded` is `true` | fixed by D-040; on an older build, `palm-gwctl allow <addr>` or re-pair |
| Paired Palm stuck on "Initializing" after the Pi or BlueZ restarted; re-pairing cures it until the next restart | BlueZ waiting for a reverse SDP browse that never starts (D-037) | `btmon` shows the Palm's RFCOMM `SABM` with no `UA`/`DM`; `bluetoothd` debug log stops at `authorizing connection`; no `connection` event in the daemon log | `ReverseServiceDiscovery = false` in the managed block (already set); check the block is present in `/etc/bluetooth/main.conf` |
| "Established" but no web pages | DNS or NAT | On Palm, try `http://1.1.1.1/` vs a hostname; on Pi `nft list counters`, `tcpdump -i pppN` | check `ms-dns`, dnsmasq `listen-address`, `ip_forward`, masquerade on correct `uplink` |
| Works for 30 to 90 s then drops | Wi-Fi coexistence, or `lcp-echo-failure` too aggressive, or Palm idle timeout | pppd stderr: `LCP terminated by peer` vs `No response to N echo-requests` | Ethernet uplink / dongle; raise `lcp-echo-interval`; Palm Network Details → Idle timeout: Never |
| RFCOMM channel in use / second Palm cannot connect | Orphan pppd shunt holding the fd | `pgrep -a pppd`, daemon log `orphan_pppd` | `Setpgid` + kill process group; close daemon's fd copy after `Start()` |
| `RegisterProfile` succeeds, `NewConnection` never called | Introspection XML missing or wrong method signature | `busctl introspect org.bluez /palm/spp` must list `NewConnection` with signature `oha{sv}`; `dbus-monitor --system "interface='org.bluez.Profile1'"` shows the call and any error reply | Fix exported method signature, export `Introspectable` |
| pppd: `Couldn't open the /dev/ppp device` | Module or systemd sandbox | `ls -l /dev/ppp`, `journalctl -u palm-gwd` shows EPERM | `modules-load.d` includes `ppp_generic`; add `DeviceAllow=/dev/ppp rw` |
| dnsmasq fails to start: address in use | `systemd-resolved` or another dnsmasq | `ss -ulpn sport = :53` | `bind-dynamic` + `listen-address` only; remove `interface=` lines that bind broadly |
| `bnep0` appears but PANU gets no DHCP | NetworkManager grabbed `bnep0` | `nmcli device` lists `bnep0` as managed | `unmanaged-devices` config; `systemctl restart NetworkManager` |
| Pi 4: Bluetooth vanishes after enabling serial console | UART conflict | `dmesg \| grep -i hci` shows init timeout | Keep console on mini-UART (default), do not use `miniuart-bt`, or use a dongle |
| Pi 5: dongle not detected | USB current limit on a weak PSU | `vcgencmd get_throttled`, `dmesg` shows `over-current` | 5 A PSU or `usb_max_current_enable=1` |
| Palm connects via Wi-Fi instead of Bluetooth | NetLib interface preference | Palm shows Wi-Fi icon during connect | Turn Palm Wi-Fi off; ensure the Network service points to the Bluetooth connection |

Always-first diagnostic: `sudo btmon` on the Pi while attempting the operation from the Palm. It shows inquiry, paging, pairing (`PIN Code Request`), SDP searches (which UUIDs the Palm asks for; this answers the SPP-vs-LAP question definitively), RFCOMM channel open, and the first data bytes. `palm-gwctl capture 60` wraps this.

---

## 13. Security posture

The Bluetooth 1.1/1.2 link the Palms use has **legacy pairing**: the link key is derived from the PIN and values exchanged in the clear, and an attacker who captures the pairing exchange can brute-force a 4-digit PIN offline in well under a second and then decrypt the session (E0 cipher, with known weaknesses besides). The Palms cannot do better; the gateway's job is to make that weakness irrelevant.

Controls, and what each defends against:

| Control | Defends against |
|---|---|
| `Pairable=false` except during an operator-opened window of a few minutes, random PIN per window, `--one` closes after a single pairing | Opportunistic pairing by a nearby device; PIN capture requires being present during the window |
| Allowlist by device address, enforced at `AuthorizeService` for every profile including NAP | An attacker who pairs anyway (or spoofs an address but lacks the link key) still cannot open a channel |
| Palm subnet default-deny to RFC1918 and to the Pi itself except DNS/DHCP/ICMP; explicit `[[firewall.allow]]` | A compromised or spoofed Palm cannot reach the LAN, the vault, the Pi's SSH, or the ctl socket |
| Per-device single session, `max_sessions` cap, process-group kill, orphan tripwire | Resource exhaustion by reconnect storms |
| No `ktune`, no `defaultroute`, no `proxyarp` towards the LAN, `rp_filter` | PPP peers injecting routes or ARP into the Pi |
| Logging with device address for every pairing, authorisation and session event; `btmon` capture on demand | Forensics |
| Daemon-owned, `0660 root:palm-gw` ctl socket; no network listener | Local privilege boundary for operators |
| Web UI and API on the Bluetooth side are TLS 1.3 only, with the certificate pinned by the Palm app; logins rate-limited per source (D-026, D-039) | The admin password is never exposed to the weak legacy-PIN radio link |
| systemd hardening to the extent pppd and BlueZ allow | Blast radius of a daemon bug |

What the gateway deliberately does **not** claim: confidentiality or integrity of the Bluetooth hop. Applications on the Palm that need that use TLS (the user's wolfSSL port on Palm OS provides TLS 1.3). Stock Palm apps (Blazer, VersaMail) use SSL 3.0/TLS 1.0 at best and should be treated as cleartext for threat-modelling purposes; the firewall's isolation is what protects the rest of the house from them, not the radio.

---

## 14. Test matrix

| Dimension | Values |
|---|---|
| Board | Pi 4 Model B (4 GB or 8 GB), Pi 5 (4 GB or 8 GB) |
| OS | Pi OS Lite 64-bit Trixie (primary), Bookworm (secondary) |
| Adapter | onboard CYW43455, CSR8510 USB dongle (onboard disabled) |
| Uplink | `eth0`, `wlan0` 5 GHz, `wlan0` 2.4 GHz (expected degraded with onboard adapter; record) |
| Palm client | T|X (OS 5.4.9), LifeDrive (OS 5.4.1), plus any Tungsten/Treo available; `fake-palm` for every automated run |
| Palm connection type | PC (SPP + MS handshake), Local Network (LAP), Mobile Phone (DUN + AT) |
| PAN client | `fake-palm pan` (Linux), Android phone (or another non-BlueZ PANU; outstanding from Phase 4, D-033) |
| Scenarios | first pair, re-pair after Palm hard reset, 20× reconnect storm, BlueZ restart mid-session, daemon restart mid-session, Pi reboot with Palm connected, 24 h soak, two Palms simultaneously, Palm + PANU simultaneously, `reload` with new firewall rule mid-session |

Minimum to call v1 done: every row of the matrix exercised on both boards with onboard adapter and Ethernet uplink; dongle mode exercised on both boards; Wi-Fi uplink exercised on at least one board; all Palm connection types exercised on the T|X; results in `TESTING.md`.

---

## 15. Deliverables checklist

- [ ] Repository laid out as in section 6, `make test lint` clean, CI green
- [ ] `make build-arm64` from macOS produces a binary that runs unchanged on Pi 4 and Pi 5
- [ ] `curl … | sudo bash` on a fresh Pi OS Lite 64-bit image (each board) installs the .deb, runs the wizard, pairs a Palm, and yields a working gateway after at most one reboot
- [ ] Unattended install via environment variables produces the same result with no prompts
- [ ] Dongle mode selectable in the wizard and via `PALM_GW_ADAPTER=dongle`, with the onboard radio disabled and a reboot prompt
- [ ] `.deb` upgrade in place preserves config and paired devices; `apt purge` leaves the system as it was (diff `/etc` before and after on a test image)
- [ ] Web UI: every page in section 8.12 works from a LAN browser, is unreachable from the Palm side, and `/palm` is reachable only from the Palm side
- [ ] `palm-gwctl` offers every operation the web UI offers
- [ ] All acceptance tests in section 10 pass; commands and outputs in `docs/TESTING.md`
- [ ] `docs/DECISIONS.md` records every deviation from this plan with reasons, and the hardware observations from section 16's two residual items (onboard pairing reliability per Palm model, class value each Palm accepted)
- [ ] `docs/PALM-SETUP.md` is usable by a person holding a T|X who has never seen this repository
- [ ] `docs/TROUBLESHOOTING.md` contains at least one real log excerpt per row of section 12 that was reproducible
- [ ] `docs/BLUETOOTH-PRIMER.md` exists (Appendix A expanded) so a future maintainer understands why the handshake and the three profiles exist
- [ ] A tagged release with `dist/palm-gw_<ver>_linux_arm64.tar.gz`

---

## 16. Resolved questions

Everything that an earlier draft of this plan left to be verified, and how it was closed. The implementer should not reopen these.

| Question | Resolution | Basis |
|---|---|---|
| Does BlueZ read `/etc/bluetooth/main.conf.d/`? | **No, not in any shipping Debian.** Support was committed to BlueZ in January 2026, after the 5.82 in Trixie and 5.66 in Bookworm. The installer edits `main.conf` in place with a managed block. | BlueZ commit "Support for config fragments" (main.conf.d), dated 2026-01-15 |
| What does the `Class` key in `main.conf` control? | Only the major and minor device class bits. BlueZ computes the service-class bits from registered service UUIDs. So the key is `0x000300`, not `0x020300`. | BlueZ `main.conf` comment on `Class` |
| Will BlueZ generate SDP records for SPP/LAP/DUN if we omit `ServiceRecord`? | Not reliably: automatic generation exists only for a few audio profiles, and `ServiceRecord`, when provided, always takes precedence. The project always supplies records. | BlueZ `ProfileManager1` documentation; BlueZ commit adding automatic SDP record generation for external profiles |
| Which Bluetooth service does a Palm use when its connection is "Connect to: PC via Bluetooth"? | The Serial Port Profile. The classic Linux recipe registered only `sdptool add SP` and ran `dund --listen --msdun`, and the Palm connected as "Connect To: PC, Via: Bluetooth". The classic Windows recipe used the Bluetooth LAN Access service instead, which the Palm also accepts. Hence the project serves SPP **and** LAP (and DUN for the phone-style connection). | OSNews "Make your Arch Linux a Bluetooth Access Point"; Geekzone "How to share your Internet connection via Bluetooth to a Palm OS device" |
| Is the `CLIENT`/`CLIENTSERVER` handshake real, and do all Palms do it? | Real: `dund --msdun` existed for exactly this, and Palm OS 4 users who lacked it added a login script that sent `CLIENT` and waited for `CLIENTSERVER`. Not all Palms send it (the OS 4 script exists because some did not), so the server must handle both "CLIENT first" and "PPP first", which the peek-based handshake does. | Same two sources |
| Can wildcard interface names like `ppp*` be used inside nftables sets? | Avoided entirely. Wildcards are used only in plain `iifname`/`oifname` rule matches, which every supported nftables version handles; the Palm-side classification is done by two `jump` rules into shared chains. | Design change; nftables manual |
| Will strict `rp_filter` drop PPP peer traffic because peers share the `/24` with `br-palm`? | pppd installs a `/32` route per peer so strict mode would work, but the plan uses loose mode (`rp_filter = 2`) so correctness does not depend on route installation timing. | Design change |
| How does the daemon learn the `pppN` name? | From pppd's own `Using interface pppN` log line on stderr, with the `linkname` pid file as a fallback. No ip-up hooks required. | pppd behaviour with `logfd 2` |
| Will Debian's stock `/etc/ppp/options` (`auth`, `crtscts`, `lock`) interfere? | Later options override earlier ones and root may set privileged options, so `options.palm` wins; the daemon also repeats `noauth nocrtscts` on the command line. `lock` is a no-op with `notty`. | pppd option semantics |
| Which systemd sandbox options break pppd `notty`? | Anything that hides `/dev/ppp` or the pty devices. The unit ships with `DevicePolicy=closed` and explicit `DeviceAllow` for `/dev/ppp`, `/dev/ptmx`, `/dev/pts/*`, and never sets `PrivateDevices`, `ProtectProc=invisible` or `NoNewPrivileges=yes`. | pppd requirements for `notty` (pseudo-tty pair) and privileged options |
| Which dnsmasq package, and will it collide with an existing one? | `dnsmasq-base` (binary only, no system service) run as a project-owned `palm-gw-dnsmasq.service` with its own config, bound to `10.42.0.1` with `bind-dynamic`. It cannot collide with a system dnsmasq or `systemd-resolved`. | Debian packaging of dnsmasq |
| NetworkManager vs systemd-networkd for the bridge? | NetworkManager (default on Pi OS since Bookworm). `bnep*` and `ppp*` are declared unmanaged. The networkd alternative is not shipped. | Raspberry Pi OS Bookworm release notes |
| Which Pi firmware path for the Bluetooth controller? | Irrelevant to the project: the kernel driver loads it. The installer checks only that an adapter exists and otherwise points at `dtoverlay=disable-bt`. | Design change |
| How to pick the adapter for `auto`? | One adapter: use it. Several: prefer the one on the USB bus per sysfs, else lowest `hciN`. No device-tree parsing. (Amended 2026-11-09, D-014.) | Design change |
| Pi 5 USB current for a dongle? | 600 mA total unless a 5 A supply is negotiated or `usb_max_current_enable=1`; a dongle needs about 50 mA. The wizard reports `vcgencmd get_throttled` as a warning only. | Raspberry Pi 5 documentation |

**The two residual hardware observations**, handled by design rather than left open:

1. *Does the onboard CYW43455 pair reliably with a given Bluetooth 1.1 Palm?* It should (the controller supports BR/EDR and legacy pairing), but coexistence with Wi-Fi and 20-year-old radios can be marginal. The plan treats Ethernet uplink plus onboard radio as the default, Wi-Fi off as the first lever, and a USB dongle as the fully supported second lever (`PALM_GW_ADAPTER=dongle`, or the Settings page). Record the outcome per Palm model in `DECISIONS.md`.
2. *Which Class of Device does a given Palm's device picker show?* The default `0x000300` (LAN access point) is correct per the Bluetooth assigned numbers; if a Palm filters it out, `palm-gwctl class toggle` switches to `0x000104` (desktop computer) and persists it. Record which value each Palm model needed.

## Appendix A: Bluetooth background for the implementer

**Stack layers you will touch.** Baseband and LMP live in the radio firmware (pairing, encryption, paging, inquiry); HCI is the host-to-controller interface (`btmon` shows HCI events and the ACL data above them); L2CAP multiplexes channels over one ACL link; **RFCOMM** is a serial-port emulation on top of L2CAP (PSM 3) with up to 30 "channels" per device, each like a COM port; **SDP** (PSM 1) is the service directory a client queries to learn which UUID is on which RFCOMM channel; **BNEP** (PSM 15) carries Ethernet frames for PAN.

**Profiles relevant here.**
- *SPP* (0x1101): raw serial over RFCOMM. The Palm's "PC" connection type uses this and expects a Windows-style RAS server at the other end.
- *LAP* (0x1102): "LAN Access using PPP". Defined in Bluetooth 1.1, deprecated in 1.2 in favour of PAN. Semantically identical to SPP for our purposes: a PPP server listening on an RFCOMM channel. The SDP record can carry an IP subnet attribute (0x0200) that nobody reads.
- *DUN* (0x1103): serial to a modem. The client sends Hayes AT commands, dials, and runs PPP after `CONNECT`. Palm's "Mobile Phone" connection type uses this.
- *PAN* (NAP 0x1116, GN 0x1117, PANU 0x1115): Ethernet over BNEP. The Pi is a NAP; clients are PANUs. Palm OS 5 does not implement PAN, which is why the Palms use the three profiles above.

**Pairing in Bluetooth 1.1/1.2.** The host asks the controller to authenticate; with no stored link key the controller raises a `PIN Code Request` to the host (BlueZ asks our agent); both sides compute an initialisation key from the PIN, the address and a random number, exchange values, and derive a link key that is stored by both. Subsequent connections do `Link Key Request` and skip the PIN. Encryption (E0) is negotiated per connection; BlueZ requires it when `RequireAuthentication=true` on the profile.

**Secure Simple Pairing (2.1+)** uses ECDH and is what modern PANU clients will do; our agent handles `RequestConfirmation` for them. BlueZ picks legacy or SSP automatically based on the remote's capabilities.

**Class of Device** is a 24-bit field in the inquiry response: 11 service-class bits (bit 17 = Networking, bit 20 = Object Transfer, bit 21 = Audio ...), a 5-bit major class (0x01 computer, 0x02 phone, 0x03 LAN/network access point, 0x04 audio/video ...), and a 6-bit minor class. Client UIs often filter by major class, which is why the plan sets ours to "LAN access point" and offers "computer" as a fallback.

**Why `btmon` is the whole debugging story.** It attaches to the kernel's monitor socket and prints every HCI command, event and ACL packet in both directions with protocol decoding (L2CAP, RFCOMM, SDP, BNEP). Every one of the traps in section 12 is visible in a `btmon` trace. `btmon -w file.btsnoop` writes a capture that Wireshark opens.

## Appendix B: D-Bus interface reference used by this project

All on the **system bus**, service name `org.bluez`.

| Object | Interface | Members used |
|---|---|---|
| `/` | `org.freedesktop.DBus.ObjectManager` | `GetManagedObjects()`, signals `InterfacesAdded`, `InterfacesRemoved` |
| `/org/bluez` | `org.bluez.AgentManager1` | `RegisterAgent(o agent, s capability)`, `UnregisterAgent(o)`, `RequestDefaultAgent(o)` |
| `/org/bluez` | `org.bluez.ProfileManager1` | `RegisterProfile(o profile, s uuid, a{sv} options)`, `UnregisterProfile(o)` |
| `/org/bluez/hciN` | `org.bluez.Adapter1` | props `Address`, `Name`, `Alias`(rw), `Class`, `Powered`(rw), `Discoverable`(rw), `DiscoverableTimeout`(rw), `Pairable`(rw), `PairableTimeout`(rw), `Modalias`; method `RemoveDevice(o)` |
| `/org/bluez/hciN` | `org.bluez.NetworkServer1` | `Register(s uuid, s bridge)`, `Unregister(s uuid)` |
| `/org/bluez/hciN/dev_XX_XX_…` | `org.bluez.Device1` | props `Address`, `Name`, `Alias`, `Class`, `Paired`, `Bonded` (D-040), `Trusted`(rw), `Connected`, `UUIDs`; `PropertiesChanged` signal via `org.freedesktop.DBus.Properties` |
| `/palm/agent` (ours) | `org.bluez.Agent1` | `Release()`, `RequestPinCode(o)→s`, `DisplayPinCode(o,s)`, `RequestPasskey(o)→u`, `DisplayPasskey(o,u,q)`, `RequestConfirmation(o,u)`, `RequestAuthorization(o)`, `AuthorizeService(o,s)`, `Cancel()` |
| `/palm/{spp,lap,dun}` (ours) | `org.bluez.Profile1` | `Release()`, `NewConnection(o device, h fd, a{sv} fd_properties)`, `RequestDisconnection(o device)` |

Errors we return: `org.bluez.Error.Rejected`, `org.bluez.Error.Canceled`. In godbus: `dbus.NewError("org.bluez.Error.Rejected", nil)`.

`fd_properties` in `NewConnection` may contain `Version` and `Features` (uint16) for some profiles; ignore them.

## Appendix C: SDP record templates

Substitute `{{CHANNEL}}` at runtime. Attribute IDs: 0x0001 ServiceClassIDList, 0x0004 ProtocolDescriptorList, 0x0005 BrowseGroupList, 0x0009 BluetoothProfileDescriptorList, 0x0100 ServiceName, 0x0200 (LAP) IpSubnet.

**SPP**
```xml
<?xml version="1.0" encoding="UTF-8" ?>
<record>
  <attribute id="0x0001"><sequence><uuid value="0x1101"/></sequence></attribute>
  <attribute id="0x0004">
    <sequence>
      <sequence><uuid value="0x0100"/></sequence>
      <sequence><uuid value="0x0003"/><uint8 value="{{CHANNEL}}"/></sequence>
    </sequence>
  </attribute>
  <attribute id="0x0005"><sequence><uuid value="0x1002"/></sequence></attribute>
  <attribute id="0x0009"><sequence><sequence><uuid value="0x1101"/><uint16 value="0x0102"/></sequence></sequence></attribute>
  <attribute id="0x0100"><text value="Serial Port"/></attribute>
</record>
```

**LAP**
```xml
<?xml version="1.0" encoding="UTF-8" ?>
<record>
  <attribute id="0x0001"><sequence><uuid value="0x1102"/></sequence></attribute>
  <attribute id="0x0004">
    <sequence>
      <sequence><uuid value="0x0100"/></sequence>
      <sequence><uuid value="0x0003"/><uint8 value="{{CHANNEL}}"/></sequence>
    </sequence>
  </attribute>
  <attribute id="0x0005"><sequence><uuid value="0x1002"/></sequence></attribute>
  <attribute id="0x0009"><sequence><sequence><uuid value="0x1102"/><uint16 value="0x0100"/></sequence></sequence></attribute>
  <attribute id="0x0100"><text value="LAN Access using PPP"/></attribute>
  <attribute id="0x0200"><uint16 value="0x0000"/></attribute>
</record>
```

**DUN**
```xml
<?xml version="1.0" encoding="UTF-8" ?>
<record>
  <attribute id="0x0001"><sequence><uuid value="0x1103"/><uuid value="0x1201"/></sequence></attribute>
  <attribute id="0x0004">
    <sequence>
      <sequence><uuid value="0x0100"/></sequence>
      <sequence><uuid value="0x0003"/><uint8 value="{{CHANNEL}}"/></sequence>
    </sequence>
  </attribute>
  <attribute id="0x0005"><sequence><uuid value="0x1002"/></sequence></attribute>
  <attribute id="0x0009"><sequence><sequence><uuid value="0x1103"/><uint16 value="0x0100"/></sequence></sequence></attribute>
  <attribute id="0x0100"><text value="Dial-up Networking"/></attribute>
  <attribute id="0x0305"><boolean value="false"/></attribute>   <!-- AudioFeedbackSupport -->
</record>
```

`internal/profile/sdp_test.go` must parse each generated record with `encoding/xml` to confirm well-formedness and that the channel landed in the RFCOMM element.

## Appendix D: pppd option rationale

| Option | Why |
|---|---|
| `notty` | No tty; pppd forks a character shunt between the pty it creates and our stdin/stdout (the RFCOMM socket). This is the whole trick. |
| `local` | No modem control lines to watch. |
| `noauth` | The Bluetooth link already authenticated the device; PAP/CHAP on top adds nothing and Palm users leave username/password blank. |
| `nodetach` | Stay in the foreground so the daemon can supervise. |
| `logfd 2` | Log to stderr, which the daemon captures. |
| `nodefaultroute` | The Palm must never be allowed to become the Pi's default route. |
| `noipv6` | Palm NetLib is IPv4 only; avoids a wasted IPV6CP negotiation. |
| `nocrtscts` | No hardware flow control on a socket. |
| `noccp`, `nobsdcomp`, `nodeflate` | Compression Control Protocol negotiation confuses some Palm stacks and buys nothing at Bluetooth 1.1 speeds vs. CPU on a 312 MHz ARM. |
| `novj`, `novjccomp` | Van Jacobson TCP header compression has caused stalls on Palm OS 5 NetLib in the field. Start without; a later experiment may re-enable per device. |
| `nopcomp`, `noaccomp` | Protocol/address-control field compression; disable for maximum interop with old stacks. Revisit if throughput matters. |
| `asyncmap 0` | Do not escape control characters; the link is 8-bit clean. |
| `mtu 1500`, `mru 1500` | Match Ethernet so no fragmentation across the NAT. |
| `ms-dns <gateway>` | Hand the Palm the Pi's dnsmasq as DNS via IPCP option 129 (Palm asks for it when "Query DNS" is checked). |
| `lcp-echo-interval 20`, `lcp-echo-failure 3` | Detect a Palm that wandered out of range in about a minute and free the session. |
| `lcp-max-configure 30` | Palms can be slow to answer the first LCP Configure-Request after the handshake; do not give up early. |
| `ipcp-accept-remote` | If the Palm proposes its own address, accept it (it normally asks for 0.0.0.0 and takes ours). |
| `proxyarp` | **Remove**: with routed NAT the Palm addresses are not on the LAN; proxy ARP would be wrong. Listed here so nobody adds it later thinking it is missing. |
| `idle N` (conditional) | From `config.ppp.idle_seconds`; drop idle links so the pool recycles. |
| `debug` (conditional) | Full LCP/IPCP packet logging; use when a Palm fails after "Signing on". |
