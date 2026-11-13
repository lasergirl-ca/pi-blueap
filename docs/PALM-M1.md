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
   password shows as stars. Tick **Show** to see what you are writing (the on-screen keyboard
   works then too); untick it and it goes back to stars. With the stars showing, tapping abc
   or 123 says to tick Show first, and opens nothing. Tap **Log in**.
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

**2026-11-12, Zoe's T|X ("It works"),** with `PalmGW-m1-show.prc`, over Bluetooth to the test
gateway (test32). The gateway's log:

```
17:02:49  session_up        Zoë's Palm TX  ppp0  10.42.0.10
17:04:13  web_login_failed  side=bluetooth ip=10.42.0.10 api=true   (the first build: the
17:05:50  web_login_failed  side=bluetooth ip=10.42.0.10 api=true    password written blind)
17:09:31  session_up        Zoë's Palm TX  ppp0  10.42.0.10
17:09:38  web_login         side=bluetooth ip=10.42.0.10 user=sysadmin role=admin api=true
```

- The ARM TLS module loaded and completed TLS 1.3 to `10.42.0.1:8042` from the T|X.
- First-use trust and the login worked, then Status.
- The login came 7 s after the PPP link came up, including the handshake, the Trust screen
  and the login's own connection.
- The two failures came from writing the password blind with Graffiti. That is why Show was
  added (`1f64ab4`).

Still to note from the device: the handshake time Status shows ("Last call"), and the
heap in About.

## Remember and the logo (after the first device run)

Zoe asked for the login to be remembered, and gave the app its logo (D-063).
- **Log in:** a **Remember this Palm** box, ticked by default. With it, the gateway issues
  this Palm a device key. The Palm keeps the key, never the password.
- **Gateways:** Connect to a remembered gateway logs in with the key and goes straight to
  Status. Details says "remembered".
- **Gateway's Settings page:** **Palm devices** lists each key, with the Palm's HotSync name,
  and revokes one. A revoked Palm is asked for the password at its next Connect.
- **Launcher:** the palm-gw icon is Zoe's logo, in the Launcher and in list view.

What to check on the T|X with `PalmGW-m1-remember.prc`:
1. Log out, then Connect: Log in has Remember ticked. Log in with the password.
2. On the web UI's Settings page, Palm devices lists the T|X by its HotSync name.
3. Log out on the Palm and Connect again: no password, straight to Status.
4. Revoke the key on the web page, then Connect on the Palm. It says the gateway no longer
   takes its key and asks for the password.
5. The Launcher shows the logo.
