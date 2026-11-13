# The palm-gw Palm app: design and milestones

Phase 7 part 7 (PLAN.md section 10; D-039, D-062). The app gives a Palm OS 5 handheld the
admin interface `docs/WEB-UI.md` section 7 describes, over `/api/v1/` (section 6) and TLS 1.3.
The toolchain and the Palm OS lessons it follows are in `docs/PALM-DEV.md`; the ARM module
work it reuses comes from CradleServer (`~/Code/CradleServer/palm`), which took it from Tessera.

## Decided (Zoe, 2026-11-12)

- **ARM split:** the UI, the Net Library and HTTP are a 68K application; TLS 1.3 is a native
  ARM module, CradleServer's wolfSSL client behind its step buffer (`tlsmod.h`), which has run
  on the T|X in CradleSync's Secure transport. One `PceNativeCall` per step, no blocking and no
  calls back into 68K code. So the app needs Palm OS 5 (T|X, LifeDrive, Tungsten T3/T5, E2).
- **Licence:** the whole repository is GPLv3 (D-061), since the module links wolfSSL.
- **Trust on first use:** the first connection to a gateway shows its certificate's SHA-256
  fingerprint, the one the web login page shows, and asks before anything is sent. From then
  on the app pins the server key's SPKI hash and refuses any other.
- **Several gateways ("APs"):** the app keeps a list. Every gateway is `10.42.0.1` to a Palm
  on its Bluetooth link, so a gateway is known by its pinned key, not its address. The
  handshake reports the server key's SPKI hash, and the app compares it with every pin it
  holds before it sends a byte. That one handshake says which saved gateway answered, however
  many are saved. An unknown key is a new gateway and goes through first-use trust. A
  gateway picked from the list that shows a different key gets a warning instead; trusting
  it replaces the old key. (A new key at 10.42.0.1 reached through New is not a warning:
  every gateway has that address over Bluetooth.) A gateway reached over Wi-Fi at a LAN
  address is the same gateway, by the same pin.
- **How the pin is checked:** the module runs the handshake with no pins (it takes any key and
  reports both hashes), and the app refuses to go on unless the key is the expected one. The
  module's own pin list is not used: the same check, without a second handshake to learn a
  changed key's fingerprint. Every later call checks the key again.

## Pieces

| Path | What | From |
|---|---|---|
| `palm-app/loader/` | the native module loader (`entry.S`, `loader.c`, `module.ld`) | CradleServer, as is |
| `cmd/palmlink/` | turns four links of a module into `armc`/`armd`/`armh` resources | CradleServer, as is |
| `palm-app/tls/` | the TLS module: `tls_client.c`, `module.c`, `tlsmod.h`, `tls_heap.c`, `palm_libc.c`, `user_settings.h`; a host test | CradleServer, with the changes below |
| `palm-app/third_party/wolfssl/` | wolfSSL 5.9.4, only the files the build includes | CradleServer, unmodified |
| `palm-app/app/` | the 68K application: forms, gateways database, Net Library, HTTP, the text format | new |

**Changes to the TLS client for palm-gw:**
- **ALPN:** `http/1.1`, which the gateway now offers.
- **Server addressed by IP:** no SNI (RFC 6066 forbids an IP there) and no name check. The pin
  says which gateway it is, and the gateway's certificate names it by IP (10.42.0.1), which
  wolfSSL's DNS-name check does not cover.
- **Pins:** a handshake with no pins accepts the key and reports both the SPKI hash (to pin)
  and the certificate's SHA-256 (to show, as the web login page does). Up to four pins can be
  given, with the one that matched reported; the app does its own comparison instead (above).
- **Not needed and left unused:** the Ed25519 client certificate and the exporter.

## The app

**Gateways database** (`palm-gw Gateways`, creator `PlGw`, type `GWay`): one record per
gateway, a packed layout with a version byte, with a host test for packing:

| Offset | Size | Field |
|---|---|---|
| 0 | 1 | version (1) |
| 1 | 32 | SPKI SHA-256 pin |
| 33 | 32 | certificate SHA-256 (shown in the list and the trust dialog) |
| 65 | 2 | port (8042) |
| 67 | 4 | last used, seconds (TimGetSeconds) |
| 71 | … | NUL-terminated: name (from `status`), address (`10.42.0.1` or a LAN address), username |

The password is never stored (WEB-UI section 7). The token lives in memory for the session.

**Forms:**
1. **Gateways:** the list (name, last address, last used), Connect, New, Forget, and Details
   (the full fingerprint).
2. **Log in:** address (default `10.42.0.1`), username, password; shows which gateway it is
   once the handshake knows. The password field shows stars; **Show** beside it shows what is
   typed (Zoe: writing Graffiti blind is hard). While the stars show, the on-screen keyboard
   is refused for the password, since its dialog would show it and edit around the masking;
   with Show ticked it works as for any field.
3. **Trust:** "New gateway at 10.42.0.1", the fingerprint in four lines of eight groups, Trust
   or Cancel.
4. **Status:** the gateway's name and version, adapter, sessions, pool, uplink, pairing window.
   Menu: Pair, Devices, Sessions, Firewall, Settings, Log out.
5. **Pair:** minutes and "one device", Open; the PIN in large type with the time left; Close.
6. **Devices:** the allowlist, with Disconnect, Forget and Rename.
7. **Sessions**, **Firewall** (rules, Add, Remove), **Settings** (name, idle hang-up, debug).

**HTTP:** one request per connection (`Connection: close`). The handshake on a Bluetooth
link takes long enough that keep-alive would help; it comes after the T|X measures the
handshake. Requests ask `?format=text`. Replies are parsed as `key=value` lines into records.
Windows-1252 is the Palm's own character set, so values are shown as they come.

## Milestones

Each has a `docs/PALM-M<n>.md`: what was built, what to check on the T|X, and the device result.
Nothing is marked done before Zoe has seen it on the device.

- **M1: connect and see.**
  - The build: 68K app and TLS module into `PalmGW.prc`, `make -C palm-app`.
  - The Gateways list with first-use trust, then Log in, Status and Log out.
  - Measure the handshake time and heap on the T|X.
- **M2: pairing and devices.**
  - Pair: open a window, show the PIN, close it.
  - Devices: Disconnect, Forget, Rename. The Phase 7 accept: pair a second device with the
    PIN shown on the T|X, then disconnect and forget it.
- **M3: firewall, sessions and settings.**
  - Add and remove an allow rule and check its effect.
  - Sessions and Settings; the warning before anything that restarts Bluetooth.
- **M4: finish.** Icons, About with the build stamp, error texts, and `docs/PALM-SETUP.md`'s
  app section.
