# palm-gw: a Bluetooth access point for Palm OS handhelds, on a Raspberry Pi

`palm-gw` turns a Raspberry Pi 4 or Pi 5 into a re-creation of the Belkin F8T030 Bluetooth
Access Point. A Palm T|X, LifeDrive, Tungsten or Treo pairs with the Pi once, taps **Connect**,
and gets routed, NAT'd internet over Bluetooth (Serial Port, LAN Access or Dial-Up Networking
profile, all PPP over RFCOMM). Pocket PCs, Android phones and laptops get the same through
Bluetooth PAN. Palms are firewalled away from the home LAN unless you allow a host.

**Status: Phase 0 of 8 (scaffold).** The daemon loads and validates its configuration and
serves a control socket; nothing talks to Bluetooth yet. See [PLAN.md](PLAN.md) for the full
specification and build phases, and [docs/TESTING.md](docs/TESTING.md) for what has been
verified so far.

## Quick start (will be)

```sh
curl -fsSL https://github.com/lasergirl-ca/pi-blueap/releases/latest/download/install.sh | sudo bash
```

followed by `sudo palm-gw setup`, then open `https://<pi>:8042`. Until Phase 6 ships that
path, developers install from source:

```sh
make build-arm64                      # on the development machine
scp -r dist Makefile scripts deploy config.example.toml pi:~/palm-gw/
ssh pi 'cd palm-gw && sudo make install UPLINK=eth0'
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
| `deploy/` | systemd units, dnsmasq and nftables templates, sysctl, modules, pppd options |
| `scripts/` | `install.sh`, `uninstall.sh`, `detect-board.sh`, `nm-bridge.sh` |
| `docs/` | `DECISIONS.md` (every deviation from the plan), `TESTING.md` (acceptance evidence) |

## Licence

MIT. See [LICENSE](LICENSE).
