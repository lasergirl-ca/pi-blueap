# Troubleshooting palm-gw

This is the gateway-side guide, for whoever runs the Pi. The Palm user's short table is in
[PALM-SETUP.md, "If something goes wrong"](PALM-SETUP.md#if-something-goes-wrong); start there
for anything set on the Palm. PLAN.md section 12 lists the known traps from the design. Every
log excerpt below is real, taken from [TESTING.md](TESTING.md), trimmed with `…`. D-numbers
point to [DECISIONS.md](DECISIONS.md).

## Where to look

- **The gateway's log.** `palm-gwctl logs` shows the journal of palm-gwd, bluetooth and dnsmasq,
  decoded. `palm-gwctl logs 200 --grep 00:07:E0:3C:5E:FA` follows one Palm;
  `--event 'pan_*'` picks events; `-f` keeps printing. The raw form is
  `journalctl -u palm-gwd`. The web UI's Logs page shows the same lines.
- **Now.** `palm-gwctl status` lists BlueZ, the adapter, the pairing window, the allowlist and
  the live sessions. `palm-gwctl check` (or the web UI's Diagnostics page) checks units, kernel
  modules, adapters, uplinks and power. `sudo palm-gw setup --check` does the same detection
  the wizard does, including the supply.
- **The radio.** `palm-gwctl capture 60` records a btmon trace (1 to 600 s) into
  `/var/lib/palm-gw/captures/`; open it in Wireshark. Start it before the Palm tries, not after.
  `sudo btmon` shows the same live.
- **A support bundle.** `palm-gwctl bundle` writes
  `/var/lib/palm-gw/bundles/palm-gw-support-<date>.tar.gz`: status, check, config (password
  hashes redacted), the rendered files, the ruleset, routes and the journals of palm-gwd,
  bluetooth, dnsmasq, the bridge and the apply step.
  `--with-capture` adds the newest capture, which can hold link keys. The Diagnostics page makes
  and downloads both.

### Reading an event line

palm-gwd logs one JSON object per line. `level` is INFO, WARN or ERROR; `event` is the name to
search for; `addr` is the Palm's Bluetooth address (TESTING.md, Phase 5, 2026-11-10):

```
{"time":"2026-11-10T11:31:34.431434772-07:00","level":"WARN","msg":"BlueZ went away","component":"daemon","event":"bluez_lost"}
```

`palm-gwctl logs` prints the same thing decoded (TESTING.md, Phase 7 part 4, 2026-11-12):

```
Nov 12 10:46:32 palm-gwd  INFO  [wifi_joining] joining Wi-Fi component=daemon hidden=false ssid=<home>
```

A normal connection is `connection`, `session_start` and `session_up` within about 100 ms, and
ends with `session_end` (with `duration` and `reason`). PAN clients log `pan_connected` and
`pan_disconnected`.

### Keeping the log across a reset

Raspberry Pi OS keeps the journal in RAM, so a crash or power cut loses it. The test Pi keeps it
with a drop-in, `/etc/systemd/journald.conf.d/50-palm-gw-persistent.conf` (`Storage=persistent`,
`SystemMaxUse=100M`), then `systemctl restart systemd-journald`; `journalctl -b -1` then shows
the boot before. This is not part of palm-gw; delete the file to go back.

## The Palm says "Serial: timeout (0x0305)"

0x0305 is Palm OS's serial timeout. On a T|X the Bluetooth radio sits behind an internal serial
line, so the same message covers several different failures. Five kinds have been seen. Find
the time of the error and read the log and a capture around it.

### Kind 1: during a heavy transfer, on a weak supply

**What you see.** The error part-way through a large sync, with under-voltage in the kernel log
at the same time. Recorded (TESTING.md, the T|X's 0x0305 drops, 2026-11-12): 276,539 bytes down
in 1.9 minutes from 17:23:08, under-voltage at 17:24:04, 17:24:56 and 17:25:00, the Palm ending
PPP at 17:24:59, on a supply that negotiated 3,000 mA. On a 5 A supply this kind stopped. See
[the Pi reports under-voltage](#the-pi-reports-under-voltage-or-reboots-by-itself).

### Kind 2: just after a CradleSync session ends

**What you see.** The error while CradleSync's progress screen is still up, after the server
has finished. The session ends with `Connection timed out`, not a hang-up.

From a btmon capture and palm-gwd's log (TESTING.md, the T|X's 0x0305 drops, 2026-11-12):

```
19:31:59.969  PPP link up, 10.42.0.10 (RFCOMM, handshake and PPP in 0.6 s)
19:32:58.270  T|X -> 10.4.0.7:14243 SYN (CradleSync, Secure); TLS from 19:32:58.698
19:33:02.247  airlock -> T|X: a 24-byte TLS record, then FIN (the airlock ends the session)
19:33:02.262  T|X FIN-ACK; its last ACK at .290, receive window 0 (socket closed)
19:33:20.002  gateway -> T|X: LCP Echo-Request (no answer)
19:33:28.337  HCI Disconnection Complete, reason Connection Timeout (0x08)
19:33:28.338  pppd: Error reading standard input: Connection timed out; session_end 1m29s
```

**What it means.** Reason 0x08 is the link supervision timeout: the T|X stopped answering at the
radio level about 6 s after the server closed the session, and the controller dropped the link
20 s later. Nothing was owed either way, and the server logged exit 0. The cause is on the T|X,
in how a CradleSync session ends, not in palm-gw. **Open** on the CradleSync side.

**What to do.** Nothing on the gateway; check the server's log that the sync completed. The same
`Connection timed out` also ends a session when a Palm is switched off or carried out of range
(TESTING.md, Phase 5, USB dongle mode, 2026-11-10, 20:42:14).

### Kind 3: a pause in the middle of a sync

**What you see.** The error during a sync on a good supply. The link stays up and the T|X hangs
up itself (HCI reason 0x13, remote user terminated).

**Recorded** (TESTING.md, 2026-11-12, 17:44-17:49): PPP up in 40 ms, every packet completed by
the dongle in the same second, then IP traffic stopped both ways for 15 s, 32 s, and for good.
Counters on the next sync showed 0 invalid packets and 0 drops. Neither end sent anything for
long enough that the T|X gave up: a wait between the Palm's app and its server.

**What to do.** Look at the server's session log for the wait. `palm-gwctl firewall counters`
shows whether any drop rule fired, including `palm->invalid` (counted since test34).

### Kind 4: before any connection

**What you see.** The error straight after tapping Connect, and nothing at all in the gateway's
log or in a running capture.

**Recorded** (TESTING.md, the 20:17 and 20:18 errors, 2026-11-12): a 30-minute capture had
nothing from the T|X after 20:11:24, while the adapter was connectable (`hciconfig`: UP RUNNING
PSCAN ISCAN, 0 errors). The T|X failed before it paged the gateway, which the gateway cannot see
or change. After Bluetooth off and on in the T|X's Prefs it connected at once (20:19:01).

**What to do.** On the Palm: try again, or switch Bluetooth off and on in Prefs.

### Kind 5: the first Connect after pairing drops after about 40 s (D-050)

**What you see.** PPP comes up, nothing loads, and the T|X shows 0x0305 after 35-40 s. A second
tap straight after works.

From the stock-card run (TESTING.md, Phase 6 part 7, 2026-11-11):

```
21:57:13 PIN requested / device paired  00:07:E0:3C:5E:FA … trusted=true
21:57:46 RFCOMM connection  profile=lap channel=2 ; session started ; PPP link up  peer=10.42.0.10
21:58:25 IPCP terminated by peer ; Sent 0 bytes, received 4 bytes. ; session ended  duration=40s
```

**What it means.** The T|X decides it. PPP and IPCP come up, and the T|X sends nothing and
then hangs up itself (IPCP TermReq, LCP TermReq, HCI reason 0x13). A capture with `ppp.debug`
on (TESTING.md, Phase 8's final test, 2026-11-12) shows this first Connect and a good one are
the same on the wire, frame for frame, so nothing on the gateway's side differs. Seen on every
gateway the T|X has newly paired with: the test14 card, 2026-11-12 at 17:01 and 17:13, and on
both rc1 cards.

**What to do.** Tap Connect again. To help close it, catch one with a capture running and
`ppp.debug` on, then make a bundle with the capture:

```
palm-gwctl set ppp.debug true
palm-gwctl capture 600
palm-gwctl bundle --with-capture
```

## The Pi reports under-voltage, or reboots by itself

**What you see.** `palm-gw setup --check`, `palm-gwctl check` and the Diagnostics page warn about
the supply; heavy transfers stall; the Pi reboots with nothing in the log (the watchdog).

From a fresh install on a 3 A charger (TESTING.md, Phase 6 part 7, 2026-11-11):

```
power:     supply 3000 mA, get_throttled=0x50000
warnings:
  - the Pi has been under-voltage since boot (get_throttled=0x50000)
  - Pi 5 supply negotiated 3000 mA (under 5 A), so USB ports share 600 mA; …
```

**What it means.** `vcgencmd get_throttled` 0x0 is healthy. 0x50000 means under-voltage and
throttling have happened since boot. A Pi 5 takes only 5 V; a USB PD charger that offers 5 V at
3 A gives it 15 W, whatever it offers at 9 V or 20 V. On such a supply the test Pi logged 69
under-voltage events in a day, the T|X saw 0x0305, and once the Pi hung and its watchdog
rebooted it (DECISIONS.md, Hardware observations).

**What to do.** Use a supply that offers 5 V 5 A. After the swap on the test Pi,
`max_current` was 5000, `get_throttled` 0x0, and there was no under-voltage after boot. To
check after the fact, `journalctl -k -b | grep -i voltage`.

## The Palm sticks on "Initializing" after the Pi or BlueZ restarted

**What you see.** A paired Palm hangs on "Initializing" on every Connect after a reboot,
`systemctl restart bluetooth` or an install, and gives up after about 29 s. Pairing again cures
it until the next restart. The gateway logs no `connection` event.

btmon and the bluetoothd debug log (TESTING.md, Phase 5, real T|X, 2026-11-10):

```
> RFCOMM: Set Async Balance Mode (SABM) (0x2f)  Address: 0x13 cr 1 dlci 0x04   15:39:03.609
< Set Connection Encryption ... Encryption: Enabled with E0                     15:39:03.794
  (no UA or DM for dlci 4)
< RFCOMM: Disconnect (DISC) dlci 0x00                                          15:39:32.564

src/profile.c:ext_confirm() LAN Access using PPP authorizing connection from 00:07:E0:3C:5E:FA
(nothing until the Palm dropped the link 28 s later)
```

**What it means.** BlueZ holds the grant for a trusted device until its SDP services are
resolved, and a Palm's never are after a restart (D-037). The fix is
`ReverseServiceDiscovery = false` in palm-gw's managed block of `/etc/bluetooth/main.conf`.

**What to do.** Check the block is there:
`grep ReverseServiceDiscovery /etc/bluetooth/main.conf`. If it is missing (main.conf replaced by
a package upgrade, say), `sudo palm-gw setup --apply` writes it again.

## BlueZ restarted and the Palm dropped

**What you see.** Every PPP and PAN connection ends at once. That is expected.

`systemctl restart bluetooth` with fake-palm holding PPP and PAN (TESTING.md, Phase 5,
2026-11-10):

```
{"time":"2026-11-10T11:31:34.350400408-07:00","level":"INFO","msg":"session ended","component":"ppp","event":"session_end","addr":"60:15:6F:E9:EB:03",…}
{"time":"2026-11-10T11:31:34.431434772-07:00","level":"WARN","msg":"BlueZ went away","component":"daemon","event":"bluez_lost"}
{"time":"2026-11-10T11:31:34.492151361-07:00","level":"INFO","msg":"BlueZ is present","component":"daemon","event":"bluez_found"}
{"time":"2026-11-10T11:31:36.212146233-07:00","level":"INFO","msg":"NAP server registered","component":"pan","event":"pan_registered","hci":"hci1","bridge":"br-palm"}
```

**What it means.** palm-gwd stays up and registers the agent, the profiles and NAP again within
about 2 s; `palm-gwctl status` shows `(recovered N times)` on the `bluez:` line. With the T|X
the session ended with `Modem hangup` and no pppd was left.

**What to do.** Tap Connect. If the Palm then sticks on "Initializing", see above. Anything that
rewrites main.conf (`palm-gw setup`, a name change, `palm-gwctl class toggle`) restarts BlueZ.

## palm-gwd restarted and the Palm dropped

**What you see.** PPP Palms drop; a PAN client (fake-palm, a laptop, a phone) carries on.

`systemctl restart palm-gwd` (TESTING.md, Phase 5, 2026-11-10):

```
{"time":"2026-11-10T11:31:51.882453343-07:00","level":"INFO","msg":"session ended","component":"ppp","event":"session_end","addr":"60:15:6F:E9:EB:03",…}
{"time":"2026-11-10T11:31:51.965934859-07:00","level":"INFO","msg":"palm-gwd ready","ctl_socket":"/run/palm-gw/ctl.sock"}
```

With the T|X (19:45:30), pppd ended with `Terminating on signal 15`, the link dropped at
19:45:34, and the next tap connected (`19:47:08.237 connection`, `.321 session_up`).

**What it means.** pppd runs as palm-gwd's child, so PPP sessions end with it. PAN sessions
live in the kernel and BlueZ; the new daemon finds them, checks them against the allowlist and
lists them (D-034).

**What to do.** Tap Connect. `palm-gwctl restart`, `internet use`, `network` and upgrades all
restart palm-gwd, so do them with no Palm connected (`palm-gwctl status` shows who is). Settings
in the reload set (`ppp.debug`, `handshake.*`, the name, firewall rules) need no restart (D-030,
D-060).

## The Pi lost power with a Palm connected

**What you see.** The link drops; after the boot the Palm needs one tap to reconnect.

The power cable pulled with the T|X connected (TESTING.md, Phase 5, 2026-11-10): the Pi booted
at 19:52:56, `palm-gwd ready` at 19:53:03 with no WARN or ERROR lines, and the T|X connected on
the first tap (`20:03:04.844 connection`, `.937 session_up`).

**What it means.** Nothing is left over from the old boot. The one boot message,
`brcmf_cyw_external_auth: External authentication failed` before wlan0 joined, was a one-off
from the Wi-Fi driver. If the Palm sticks on "Initializing" afterwards, see above.

**What to do.** `palm-gwctl check` for the units. Without a persistent journal the old boot's
log is gone (see [Keeping the log](#keeping-the-log-across-a-reset)).

## After a reboot the gateway is on the wrong radio, or finds none

**What you see.** After a reboot the Palm cannot connect although it is paired, or palm-gwd logs
`attach to BlueZ failed` every 3 s.

**The radios swapped numbers.** With the onboard radio and a dongle both on, the kernel numbered
them differently on two boots of the same day (TESTING.md, Phase 5, 2026-11-10), and
`adapter = "hci0"` moved the gateway onto the dongle, where the Palm's pairing is not. Pin the
adapter by address (D-035); the `adapter:` line of `palm-gwctl status` then ends
`(configured by address)`.

**Dongle mode with the onboard address in the config.** `dtoverlay=disable-bt` turns the onboard
radio off at the next boot, so a config pinned to the onboard address finds nothing. Setup now
warns before the reboot (TESTING.md, Phase 5, USB dongle mode, 2026-11-10; today the warning
comes from `palm-gw setup --apply`):

```
WARNING: adapter = "2C:CF:67:2F:29:F5" in /etc/palm-gw/config.toml is not a USB dongle here; with the onboard radio off the gateway will not find it. Set adapter = "auto", "hci0" or the dongle's address before rebooting
```

**The dongle is rfkill-blocked.** Pi OS starts every new radio blocked and keeps saved states only
for the onboard ones. From the first fresh flash (TESTING.md, Phase 6 part 7, 2026-11-11):

```
{"level":"ERROR","msg":"attach to BlueZ failed; retrying in 3 s","component":"daemon","err":"bluez: set hci1.Powered: Failed"}
hci1 bluetooth soft=1
```

palm-gwd now clears a soft block on its own adapter and says so (D-046):

```
{"level":"WARN","msg":"adapter was rfkill soft-blocked; unblocked it","component":"daemon","event":"rfkill_unblocked","hci":"hci1"}
```

**What to do.** `palm-gwctl check` lists the adapters it sees. Set `adapter` in
`/etc/palm-gw/config.toml` to the address of the one the Palm paired with, or to `"auto"`.

## A quick reconnect fails with "Input/output error"

**What you see.** A PAN client that reconnects within a fraction of a second gets
`Input/output error`; the next try works. In the Phase 5 PAN cycle (TESTING.md, 2026-11-10),
fake-palm reconnected 40 ms after a disconnect, and bluetoothd logged:

```
bnep_connadd() Failed to add device bnep%d: File exists
```

**What it means.** The kernel still held the previous BNEP session for that address. No real
client reconnects that fast. A 100-session PPP storm in the same run left 0 sessions, 0 pppd and
7 open fds, as before it.

**What to do.** Wait a second and connect again. If sessions or pppd are ever left over,
`palm-gwctl status` and `pgrep -a pppd` show them, and palm-gwd logs `orphan_pppd`.

## Pairing fails: "Unable to connect" with no window open

**What you see.** The Palm reports that pairing failed, which is correct with no window open.

btmon (TESTING.md, Phase 1, 2026-11-09):

```
> HCI Event: PIN Code Request (0x16)                   05:32:19.101340  00:07:E0:3C:5E:FA
< HCI Command: PIN Code Request Negative Reply (0x000e) 05:32:19.101358  00:07:E0:3C:5E:FA
```

**What it means.** The adapter is not pairable outside a window, so the kernel refuses the PIN
itself, in 18 µs, before bluetoothd or the agent see it. If a request does reach the agent, it
logs `pairing_rejected` with `reason=no_window`.

**What to do.** Open a window (`palm-gwctl pair 5 --one`, or the web UI) and use the new PIN. If
the Palm does not list the gateway, search again: the T|X once missed it twice while it was
discoverable. If `palm-gwctl pair` says `permission denied on /run/palm-gw/ctl.sock`, log in
again after setup added you to the `palm-gw` group, or use sudo.

## The Palm paired, but its next Connect is refused

**What you see.** The PIN was accepted, but the log shows `pin_requested` with no `paired`, and
the Connect is refused with `service_rejected`.

From the second dongle pairing, `busctl monitor org.bluez` (TESTING.md, Phase 5, USB dongle mode,
2026-11-10; times in UTC):

```
03:34:36.922833 PropertiesChanged /org/bluez/hci0/dev_00_07_E0_3C_5E_FA  "Bonded" true
03:34:37.109726 GetAll reply: "Paired" true, "Bonded" true, "Trusted" false
```

**What it means.** BlueZ does not signal `Paired` while a device's services are unresolved,
which with D-037 a Palm's never are. The daemon now treats `Bonded` as `Paired` (D-040) and logs
`paired (trusted=true)` straight after `pin_requested`. If it happens on a current build,
`palm-gwctl allow <addr>` puts the Palm on the allowlist; or forget it and pair again.

## A device not on the allowlist: `pan_refused` and `service_rejected`

**What you see.** WARN lines naming a device you did not expect, or one you removed.

From the PAN acceptance run (TESTING.md, Phase 4, 2026-11-10):

```
{"msg":"PAN client refused","component":"pan","event":"pan_refused","addr":"60:15:6F:E9:EB:03","name":"piblueap #2","ifname":"bnep0","reason":"not_allowed"}
{"time":"2026-11-10T10:15:32.026756818-07:00","level":"WARN","msg":"service rejected","component":"agent","event":"service_rejected","addr":"60:15:6F:E9:EB:03","name":"piblueap #2","uuid":"0000000f-0000-1000-8000-00805f9b34fb"}
```

**What it means.** The gateway is doing its job. `service_rejected` is the agent refusing a
profile (UUID `0000000f-…` is BNEP, which BlueZ uses for PAN). `pan_refused` is the daemon's
backstop dropping a PAN connection that got past BlueZ. A WARN `untrusted` line means the daemon
cleared BlueZ's trust on a device it no longer allows (D-032). If the device should be allowed,
pair it through a window, or `palm-gwctl allow <addr>` if it is already paired.

## A T|X "Modem" connection connects but never gets online

**What you see.** With "Connect to: Modem, via Bluetooth", the T|X dials, gets `CONNECT`, sends no
PPP and gives up about 80 s later.

What a client with an RFCOMM TTY saw when the gateway tried to hold carrier off until the dial
(TESTING.md, Phase 5, T|X dial-up, 2026-11-10):

```
11:17:53.263 fake-palm: sent "ATQ0V1Z\r", got "\r\nOK\r\n"
11:17:53.299 fake-palm: sent "ATQ0E0\r", got "\r\nOK\r\n"
11:17:54.302 fake-palm: handshake (at-tx) failed: carrier is on before the dial (the gateway should hold it off)
```

**What it means.** The kernel keeps only DTR, RTS, OUT1, OUT2 and LOOP from a modem-line
request, so carrier cannot be lowered from user space, and the T|X appears to wait for it to
rise at `CONNECT` (D-025, D-036). T|X dial-up is unsupported. Use "Connect to: PC, via
Bluetooth" on the T|X: it uses LAN Access and works.

## A Pocket PC says "The answering modem has disconnected"

**What you see.** A Windows Mobile device using "Bluetooth Null Modem" on LAN Access hangs up
12-24 s after connecting.

**What happened** (TESTING.md, Phase 7 part 6, 2026-11-12): two of three tries sent nothing for
3 s, `ms_timeout_ms` ran out, pppd started without the `CLIENT`/`CLIENTSERVER` exchange, and the
Pocket PC hung up. The default is now 10000 ms (D-060). With it, three tries on test31 each
connected first time:

```
15:42:51.915 RFCOMM connection → 15:42:52.938 handshake ms_null_modem   (1.0 s)
15:44:13.083 RFCOMM connection → 15:44:14.098 handshake ms_null_modem   (1.1 s)
```

**What to do.** Check `palm-gwctl get handshake.ms_timeout_ms`; an older config file that names
3000 keeps it. `palm-gwctl set handshake.ms_timeout_ms 10000` applies to the next connection
without a restart. Palms start PPP at once and are not slowed by it. The Pocket PC's "Network
Log On" user and password do nothing here: palm-gw's PPP has no authentication.

## Palms connect but have no internet after changing the uplink

**What you see.** After moving the Pi from Ethernet to Wi-Fi or back, Palms connect and get an
address but nothing loads.

**What it means.** The NAT rule names the uplink interface. It is rendered into
`/etc/nftables.d/palm-gw.nft` by setup, so editing `uplink` in `config.toml` and restarting
palm-gwd leaves it on the old interface (TESTING.md, Phase 5, uplink back on eth0, 2026-11-10).
The right rule looks like this:

```
		ip saddr 10.42.0.0/24 oifname "eth0" masquerade
```

**What to do.** Change the uplink with `palm-gwctl internet use ethernet|wifi`,
`palm-gwctl network --uplink IF` or the web UI's internet wizard. They run
`palm-gw setup --apply`, which re-renders the ruleset and restarts palm-gwd. Then check:
`sudo nft list chain inet palm_gw postrouting | grep masq` and `ip route show default`. With
Wi-Fi as the uplink, keep it on 5 GHz; `palm-gw setup --check` shows the band.

## The web UI's certificate fingerprint changed

**What you see.** The browser shows a new fingerprint, or the Palm app refuses the gateway
because the certificate it pinned has changed.

**What happened** (TESTING.md, Phase 7 part 5, 2026-11-12): the certificate listed the uplink's
LAN address, so switching to Wi-Fi and back made two new ones (`D6:82:63:AA:…` became
`26:AA:9F:…`). Since D-059 it is kept across uplink and LAN-address changes:

```
installed: 26:AA:9F:DB:90:CC:DE:0E
$ palm-gwctl internet use wifi      → on wifi: 26:AA:9F:DB:90:CC:DE:0E  uplink=wlan0
$ palm-gwctl internet use ethernet  → back:    26:AA:9F:DB:90:CC:DE:0E  uplink=eth0
palm-gw-apply: /var/lib/palm-gw/tls/cert.pem unchanged   (both times)
```

**What to do.** A new certificate is still made for a new Palm network (subnet), a new host name,
or when fewer than 30 days of validity are left. After one, the Palm app needs to trust the new
fingerprint; compare it with the one on the login page. After an uplink switch a LAN browser may
also show a name mismatch; that is expected.

## Still open

- **D-050:** the T|X's first Connect after pairing ends itself at 35-40 s. Captured; the
  gateway's side is the same as a good Connect, so the cause is in the T|X.
- **0x0305 after a CradleSync session ends:** on the T|X side; to be compared with a session of
  the palm-gw app alone.
- **Pi 4 and the 24-hour soak:** every Phase 5 test is still to be repeated on a Pi 4; the soak
  is deferred to field use (D-038).
