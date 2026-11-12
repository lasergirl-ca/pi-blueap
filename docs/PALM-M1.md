# Palm app, milestone 1: connect and see

Part of Phase 7 part 7 (`docs/PALM-APP.md`, D-062).

## What was built

- `palm-app/tls/`: the TLS 1.3 module (CradleServer's wolfSSL client, changed for palm-gw),
  220,772 bytes of ARM code and 6,948 of data. `make -C palm-app/tls host-test` runs the same
  client on the Mac against a server set up as the gateway's: first use, a pin among three,
  a wrong pin refused. Against the test gateway: TLS 1.3, ALPN `http/1.1`, heap peak 16,240
  bytes, and the fingerprint the web login page shows.
- `palm-app/app/`: `PalmGW.prc` (`make -C palm-app/app`), 68K code 15,260 bytes. Forms:
  - **Gateways:** the saved list (name and address), Connect, New, Details; the menu has
    Forget and About.
  - **Log in:** address (`10.42.0.1` for a new one), user, password. The password field shows
    stars, and the password is never stored.
  - **Trust:** a new gateway's certificate fingerprint in four lines, or a warning if a saved
    gateway's certificate changed.
  - **Status:** the gateway's name, version, uptime, adapter, connections, address pool,
    uplink, pairing window (with the PIN while one is open), and the last handshake's time.
    Refresh and Log out.
- `make -C palm-app/app test`: 40 host checks of the gateway record, the text parser and HTTP.

## What to check on the T|X

1. HotSync or copy `PalmGW.prc` to the T|X (`/PALM/Launcher/` on the card). It shows as
   **palm-gw** in the Launcher, with the default icon for now.
2. Connect the T|X to the gateway over Bluetooth, as you do for Blazer.
3. Open palm-gw. The Gateways screen says there are none yet. Tap **New**.
4. Log in is filled with `10.42.0.1`. Type the user (`sysadmin`) and your web password: the
   password shows as stars. Write the password with Graffiti: tapping abc or 123 while the
   password has the focus says the keyboard would show it, and opens nothing. Tap **Log in**.
5. "Trust this gateway?" shows the fingerprint in four lines. Compare it with the web login
   page (it starts `26:AA:9F:DB:90:CC:DE:0E`). Tap **Trust**.
6. Status shows "Palm Gateway - Lyman" and the gateway's details, including your own
   connection (1 PPP or more). Note the "Last call" handshake time.
7. Tap **Refresh**: the time and counts update.
8. **Log out**: back to Gateways, which now lists the gateway. **Details** shows its user,
   address and full fingerprint.
9. **Connect** it again: Log in has the user filled in and asks only for the password; no Trust
   step this time.
10. Menu > **About palm-gw**: the build stamp and the last handshake's time and heap.
11. Optional, to test the warning: on the Pi, move the certificate aside (`sudo mv
    /var/lib/palm-gw/tls /var/lib/palm-gw/tls.keep && sudo systemctl restart palm-gwd` makes
    a new one), then Connect: the app must warn that the certificate changed and send nothing
    unless you trust it. Put it back (`sudo rm -r /var/lib/palm-gw/tls && sudo mv
    /var/lib/palm-gw/tls.keep /var/lib/palm-gw/tls && sudo systemctl restart palm-gwd`) and
    Forget then re-trust it in the app, or it stays on the new key.

Also useful: the handshake time over Bluetooth, and whether the app stays responsive during
it (it does not yet show progress beyond the text line, and cannot be cancelled).

## Before the device

A read-only review of the forms (PALM-DEV.md section 9's checklist) found seven problems, all
fixed before the first device build:
1. The on-screen keyboard dialog bypassed the password masking.
2. Trusting a changed key left the old record as a second entry.
3. The trust dialog could cut off the fingerprint.
4. Nothing kept the Palm awake during a call.
5. A long adapter value could overflow a buffer on Status.
6. A check in the handshake loop could never fire.
7. Plaintext (the login form, the token) stayed in the TLS buffer after a call.

## Device result

(to come)
