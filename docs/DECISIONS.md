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

## Hardware observations

None yet. Hardware line (kickoff rule 4) still to be provided: boards, host, SSH user, dongle,
Palm models, fake-palm laptop, uplink.
