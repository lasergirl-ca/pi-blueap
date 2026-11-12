# palm-gw: a Bluetooth access point for Palm OS handhelds, on a Raspberry Pi

`palm-gw` turns a Raspberry Pi 4 or Pi 5 into a re-creation of the Belkin F8T030 Bluetooth
Access Point. A Palm T|X, LifeDrive, Tungsten or Treo pairs with the Pi once, taps **Connect**,
and gets routed, NAT'd internet over Bluetooth (Serial Port, LAN Access or Dial-Up Networking
profile, all PPP over RFCOMM). Pocket PCs, Android phones and laptops get the same through
Bluetooth PAN. Palms are firewalled away from the home LAN unless you allow a host.

**Status: Phase 6 of 8 (setup wizard and packaging) accepted on a Pi 5.** Pairing, PPP, PAN,
the firewall, the setup wizard, the .deb, the one-line installer and the image work on a Pi 5; the web UI and the Palm admin app come in Phase 7. See
[PLAN.md](PLAN.md) for the full specification and build phases, and
[docs/TESTING.md](docs/TESTING.md) for what has been verified so far.

## Quick start

On a Raspberry Pi 4 or 5 running Raspberry Pi OS Lite (64-bit), once a release is published:

```sh
curl -fsSL https://github.com/lasergirl-ca/pi-blueap/releases/latest/download/install.sh | sudo bash
```

It installs the package and runs the setup wizard (`sudo palm-gw setup`, which you can run
again any time). `sudo palm-gw upgrade` installs a newer release later.

Or flash the ready-made image (`palm-gw-<ver>-rpi-arm64.img.xz`, Raspberry Pi OS Lite with
palm-gw installed) with Raspberry Pi Imager, set the user, SSH and Wi-Fi in Imager as usual,
and run `sudo palm-gw setup` after the first login. Open Imager with the release's `.json`
so it offers those settings (`rpi-imager --repo palm-gw-<ver>-rpi-arm64.json`; on a Mac,
`"/Applications/Raspberry Pi Imager.app/Contents/MacOS/rpi-imager" --repo …`). Imager's
"Use custom" writes the image without them: no user, no SSH.

Developers build the package on their own machine and install it on the Pi:

```sh
make deb                              # on the development machine
scp dist/palm-gw_*_arm64.deb pi:
ssh pi 'sudo apt install ./palm-gw_*_arm64.deb && sudo palm-gw setup'
```

## Layout

| Path | What |
|---|---|
| `cmd/palm-gwd` | the daemon |
| `cmd/palm-gwctl` | operator CLI for the control socket (also `palm-gwd ctl ...`) |
| `cmd/palm-gw` | user-facing entry: `setup`, `status`, `web`, `upgrade` |
| `cmd/fake-palm` | Linux-side test client that stands in for a Palm |
| `internal/config` | schema, defaults, validation, template rendering |
| `internal/ctl` | control socket protocol, server, client, CLI |
| `internal/setup` | detection, the wizard, and applying or removing the system files |
| `internal/release` | finding and fetching a release for `palm-gw upgrade` |
| `deploy/` | systemd units, dnsmasq and nftables templates, sysctl, modules, pppd options, the bridge helper |
| `packaging/` | nfpm config, maintainer scripts, the bootstrap installer |
| `scripts/` | `install.sh` and `uninstall.sh` (from source), `detect-board.sh`, acceptance test scripts |
| `docs/` | `DECISIONS.md` (every deviation from the plan), `TESTING.md` (acceptance evidence) |

## Licence

MIT. See [LICENSE](LICENSE).
