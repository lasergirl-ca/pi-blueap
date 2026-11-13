# Palm app, milestone 2: everything the web UI does

Part of Phase 7 part 7 (`docs/PALM-APP.md`, D-064). Milestone 1 (`PALM-M1.md`) connected and
showed Status; this milestone adds a screen or a menu item for every web action.

## What was built

- `/api/v1` gained a call for every web action (`internal/web/api2.go`, the table in
  `WEB-UI.md` section 6), committed before the app work.
- `PalmGW.prc` (`make -C palm-app/app`) now has four 68K code resources, each under 32 KB:
  `.text` (connection, TLS, Gateways, Log in, Trust, Status), `pairf` (Pair and Settings),
  `listf` (the list, text and input forms all screens share) and `modef` (what each screen
  reads and does). `tools/check-sections.sh` runs at every link and fails it on a call that
  crosses sections without the attribute, or on a `static const` table in a sectioned function.
- From Status, the **Pair** button and the **Gateway** menu:

  | Screen | Shows | Does |
  |---|---|---|
  | Pair a device | the window's PIN and time left; who paired | Open (1 to 30 min, one device or many), Close; menu: the Class of Device switch |
  | Devices | allowed devices, * for connected | Disconnect, Forget, Rename; menu: add by address |
  | Sessions | each connection: profile, interface, IP, time, bytes | Refresh |
  | Internet | the uplink, Wi-Fi state, the scan | Join (password if not saved), Use Ethernet / Use Wi-Fi, Scan; menu: hidden network, forget, Wi-Fi on/off |
  | Firewall | the LAN-host rules | Add, Edit, Remove; menu: reachability test, drop counters, NAT table |
  | Settings | name, adapter, hang-up time, connection logging | Save; menu: network and web access, password, Palm devices (revoke keys), restart |
  | Logs | the newest 30 lines | Refresh, Filter (event, device or text) |
  | Diagnostics | the self-check, captures and bundles on the Pi | Capture, Bundle (download both from the web UI) |
  | About palm-gw | app build, last handshake, gateway version, board, OS, BlueZ | Updates (asks GitHub once) |

- Restart, network changes, the Class of Device switch, an adapter change and switching the
  uplink all ask first, then end the session and go back to Gateways with a note to connect
  again in about 20 seconds.

## What to check on the T|X

Install `PalmGW.prc` as before (it replaces the old one), connect over Bluetooth and log in
with the remembered key. Steps 9 to 11 drop the Palm's own link; do them last, or skip them.

1. **Status** has a **Pair** button. The Gateway menu lists Pair a device, Devices, Sessions,
   Internet, Firewall, Settings, Logs, Diagnostics, About palm-gw and Log out.
2. **Sessions** shows the T|X's own PPP connection with its address and byte counts.
3. **Devices** lists the T|X with a *. Rename it, then check the new label in the web UI's
   Devices page. (Disconnect and Forget on the T|X itself would drop this connection; try them
   on another device if one is paired.)
4. **Pair a device**: tap Open. A PIN and a countdown appear, and the web UI shows the same
   window open. Tap Close: both close. For the Phase 7 accept, if a second Bluetooth device
   is at hand: Open again, pair it with the PIN the T|X shows ("Paired:" names it within 10
   seconds), then on Devices disconnect it and forget it.
5. **Firewall**: Add a rule (a LAN host and `tcp/80`), Edit it, then Remove it; the web UI's
   Firewall page follows each step. From the menu: Test reachability against that host,
   Drop counters, NAT table (it lists the T|X's own connections). Done on Counters or NAT
   goes back to Firewall, not Status.
6. **Internet** shows the uplink and the Wi-Fi scan; Scan scans again.
7. **Logs** shows the newest lines; Filter with `web_login` shows the app's logins.
8. **Diagnostics** shows the self-check. Capture with 10 seconds: the web UI's Diagnostics
   page lists the capture when it ends. Bundle: the web UI lists the bundle.
9. **About palm-gw** shows the app build, the handshake time and the gateway's version. Tap
   Updates.
10. **Settings**: change the name and Save; Status shows the new name. Menu, Palm devices: the
    T|X's key is listed (do not revoke it unless you want to log in with the password again);
    Done goes back to Settings. Menu, Change password: the fields show stars, Show reveals
    them. Type a wrong current password and any new one of 8 or more characters, twice:
    the app says the current password is wrong and stays logged in; nothing changes.
11. *Drops the link:* Settings, menu, Restart palm-gwd. The app asks first, then goes back to
    Gateways with a note. Reconnect after about 20 seconds.
12. *Drops the link, optional:* Internet, Use Wi-Fi (or Use Ethernet) switches the uplink;
    the app goes back to Gateways. Switch back from the web UI or the app.

## Results

Not yet run on the T|X.
