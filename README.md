# palm-gw: a Bluetooth access point for Palm OS handhelds, on a Raspberry Pi

`palm-gw` turns a Raspberry Pi 4 or Pi 5 into a re-creation of the Belkin F8T030 Bluetooth
Access Point. A Palm T|X, LifeDrive, Tungsten or Treo pairs with the Pi once, taps **Connect**,
and gets routed, NAT'd internet over Bluetooth (Serial Port, LAN Access or Dial-Up Networking
profile, all PPP over RFCOMM). Pocket PCs, Android phones and laptops get the same through
Bluetooth PAN. Palms are firewalled away from the home LAN unless you allow a host.

**Status: v0.6.0, the first release.** Pairing, PPP, PAN, the firewall, the setup wizard, the
.deb, the one-line installer, the image, the web UI and the Palm app work on a Pi 5 with a real
Palm T|X. **Not yet tested:** a Pi 4 (D-041), a LifeDrive, and the Palm app's full checklist on
the T|X (D-070). WireGuard for the Palm network follows in a later version (D-069). See
[PLAN.md](PLAN.md) for the specification and [docs/TESTING.md](docs/TESTING.md) for what has
been verified.

## What you need

- A Raspberry Pi 4 or Pi 5 with Raspberry Pi OS Lite (64-bit), on Ethernet (recommended) or
  5 GHz Wi-Fi.
- **The right power supply.** A Pi 5 needs a 5 V 5 A USB-C PD supply (the official 27 W one);
  a 5 V 3 A charger makes it brown out under load and drop the Palm. A Pi 4 needs 5.1 V 3 A.
- Optionally a USB Bluetooth dongle, used instead of the Pi's own radio.
- A Palm with Bluetooth (T|X, LifeDrive, Tungsten T3/T5/E2, Zire 72, Treo 650 and later), or
  any Bluetooth PAN device.

## Quick start

**Flash the ready-made image.** It is Raspberry Pi OS Lite (64-bit) with palm-gw installed, for
a Pi 4 or a Pi 5.

1. Download
   [`palm-gw.rpi-imager-manifest`](https://github.com/lasergirl-ca/pi-blueap/releases/latest/download/palm-gw.rpi-imager-manifest)
   and double-click it. Raspberry Pi Imager (2.0 or later) opens with **palm-gw** in its list.
   (Or, in Imager: **App Options**, **Content Repository**, **Edit**, **Use custom URL**, and
   paste that link.)
2. Choose your Pi, **palm-gw** and the card. When Imager offers to customise, set a user, turn
   on SSH, and set Wi-Fi if the Pi will not be on Ethernet, as for any Pi.
3. Put the card in the Pi and power it up with the right supply (above). Log in over SSH and
   run `sudo palm-gw setup`; the login reminds you. It asks for the gateway's name, the
   Bluetooth adapter and the uplink, and sets up the web UI's user and password.
4. Open the web address the wizard prints at the end (`https://<the Pi's address>:8042/`) in
   a browser on your home network, and log in with the user (`admin` unless you chose
   another) and password from the wizard. The browser warns about the certificate the first
   time: it is the gateway's own, and the login page shows its fingerprint.
5. On **Pair**, open a pairing window and pair the Palm with the PIN it shows. Then follow
   [docs/PALM-SETUP.md](docs/PALM-SETUP.md) to create the Palm's connection and tap Connect.
6. Optionally, install the palm-gw app on the Palm
   ([`PalmGW.prc`](https://github.com/lasergirl-ca/pi-blueap/releases/latest/download/PalmGW.prc))
   to run the gateway from the Palm itself.

**Or install on a Pi that already runs Raspberry Pi OS Lite (64-bit):**

```sh
curl -fsSL https://github.com/lasergirl-ca/pi-blueap/releases/latest/download/install.sh | sudo bash
```

It installs the package and starts the same wizard. `sudo palm-gw upgrade` installs a newer
release later, either way.

Developers build the package on their own machine and install it on the Pi:

```sh
make deb                              # on the development machine
scp dist/palm-gw_*_arm64.deb pi:
ssh pi 'sudo apt install ./palm-gw_*_arm64.deb && sudo palm-gw setup'
```

## Documentation

| Document | For |
|---|---|
| [docs/PALM-SETUP.md](docs/PALM-SETUP.md) | Connecting a Palm, and the palm-gw app on the Palm |
| [docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md) | When something goes wrong, with real log excerpts |
| [docs/WEB-UI.md](docs/WEB-UI.md) | The web UI, its listeners and the `/api/v1` API |
| [docs/WIREGUARD.md](docs/WIREGUARD.md) | Reaching the gateway from outside over WireGuard |
| [docs/PALM-APP.md](docs/PALM-APP.md), [docs/PALM-DEV.md](docs/PALM-DEV.md) | The Palm app's design, and building Palm OS code |
| [docs/DECISIONS.md](docs/DECISIONS.md) | Every deviation from the plan, with its reason |
| [docs/TESTING.md](docs/TESTING.md) | The acceptance evidence, phase by phase |

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
| `palm-app/` | the Palm OS app: the 68K UI (`app/`), the ARM TLS module (`tls/`), the loader |
| `docs/` | the documents above |

## Licence

GNU General Public License, version 3. See [LICENSE](LICENSE). The Palm app in `palm-app/` links wolfSSL, which is GPLv3, and the whole project carries the same licence (D-061).
