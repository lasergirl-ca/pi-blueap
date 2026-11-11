# Web UI on the LAN and the Bluetooth side: design

**Status:** design, 2026-11-09, revised 2026-11-10. To be built in Phase 7 (section 10), in
phase order. Recorded as DECISIONS.md D-026 (UI on the Bluetooth side), amended by D-039 (full
admin from the Palm app over TLS). Both amend section 2. Section 8.12 stays the base design; this
file describes only what changes. The open questions at the end need Zoe's answers before
Phase 7.

## What Zoe decided

2026-11-09 (D-026):
- The UI is reachable from the **LAN** and from **inside the Bluetooth network**, with a
  **username and password** on both.
- Built in Phase 7, not earlier.

2026-11-10 (D-039), replacing D-026's read-only Bluetooth side:
- **Palms use a native Palm OS app** (`.prc`, ARM only, so Palm OS 5) with a username and
  password that grant **full admin**. That means pairing windows (the PIN is shown on the Palm),
  disconnect, forget, rename, firewall allow rules and settings.
- **TLS 1.3 only** on the Bluetooth side. The app uses Zoe's wolfSSL port and pins the
  certificate. Plain HTTP is refused there, so the password never crosses the legacy-PIN radio
  link in the clear (section 13).
- The app lives in this repository under **`palm-app/`**.
- The plain page on port 80 is no longer HTML 3.2 for Blazer. It becomes a small **HTML5 page
  for Linux laptops** on PAN.

## The two sides

```
LAN browser ── eth0/wlan0 ──► <Pi LAN IP>:8042   full UI + API, admin login (HTTPS or HTTP per [web].tls)
PAN laptop  ── br-palm  ─┐
Palm app    ── ppp*     ─┴──► 10.42.0.1:8042     full UI + API, admin login, TLS 1.3 only
PAN laptop  ── br-palm/ppp* ► 10.42.0.1:80       "you are connected" HTML5 page, no login
```

### 1. Two listeners, one route set

The daemon runs two `http.Server`s:

- **LAN listener.** Bound exactly as in section 8.12: LAN addresses only, re-evaluated on
  interface changes.
- **Bluetooth listener.** Bound to the gateway address only (`10.42.0.1:8042`). That one address
  serves both PAN clients (on `br-palm`) and PPP peers (it is the local end of every `ppp*`
  link). **TLS only**: the listener is created with `tls.Listen`, so there is no plain-HTTP
  path on it at all.

Both serve the same routes. Which side a request is on comes only from the listener that
accepted it, never from `Host`, `X-Forwarded-For` or anything else the client sends. The side
still matters for:
- the TLS-only rule;
- the rate limiter, which is kept per listener and per source IP;
- the warning shown before an action that restarts Bluetooth, since that ends the requester's
  own link;
- the "this device" panel.

A table-driven test walks every route and checks that each one requires a login on both
listeners, and that the Bluetooth listener never answers plain HTTP.

### 2. Firewall

In `palm_input`, the explicit drop for the web port becomes an accept for the gateway address
only:

```nft
ip daddr {{GATEWAY}} tcp dport 8042 accept                 # UI and API over TLS (D-026, D-039)
tcp dport 8042 counter drop comment "palm->webui-lan"      # e.g. the Pi's LAN address
```

A Palm still cannot reach the LAN listener through the Pi's LAN address, and SSH and the
ctl socket stay unreachable. With `bluetooth_side = false` the render uses the section 8.7 drop
line unchanged. Firewall changes made from the app apply to Palms immediately, just as they do
from the LAN.

### 3. Accounts

- Section 8.12's single admin account gains a **username** (default `admin`, asked in
  `palm-gw setup`, changed with `palm-gw web set-password --user`). Login checks both fields.
  Wrong username and wrong password give the same error and take the same time.
- An optional **viewer** account (`viewer_username`, `viewer_password_hash`) can log in on
  either side and is read-only everywhere. It is useful for showing someone status without
  handing over admin.
- Failed logins are rate-limited per source IP on both listeners. On the Bluetooth side, after
  repeated failures the source's device is also logged with its Bluetooth address, because the
  ppp or bnep link identifies it.

### 4. TLS

- The self-signed certificate from setup gains `10.42.0.1` as a subject alternative name, so the
  same certificate serves both listeners. The fingerprint shown at setup and on the login page
  is the one to pin.
- Laptops on PAN: HTTPS as on the LAN. They accept the certificate once, or compare the
  fingerprint.
- The Palm app: TLS 1.3 through Zoe's wolfSSL port (section 13). Go's TLS 1.3 server and
  wolfSSL interoperate (X25519, AES-128-GCM or ChaCha20-Poly1305). The app pins the
  certificate fingerprint, entered or confirmed on first connect, because a Palm has no CA
  store worth trusting. A Palm T|X (312 MHz ARM) should finish a TLS 1.3 handshake in about a
  second; to be measured.
- `bluetooth_plain_http` is gone (D-039). `config.Validate()` requires `tls != "off"` when
  `bluetooth_side` is on.

### 5. What the Bluetooth side shows

Everything the LAN UI shows, with these differences:
- A **This device** panel: the requester's own session or lease (profile, IP, duration, bytes),
  found by source IP in the session table or the dnsmasq lease file.
- Actions that restart `bluetooth.service` (class toggle, adapter settings) or change the
  Palm subnet carry a warning that the requester's own link will drop.
- Pages that are mostly downloads (btmon capture, support bundle) work, but are slow over a
  Bluetooth 1.1 link. The app does not offer them.

### 6. API for the Palm app

Served on both listeners under `/api/v1/`. It mirrors `palm-gwctl` (section 8.8), so the
daemon's ctl handlers do the work and the API is a thin layer over them, as the CLI is.

| Method and path | Does |
|---|---|
| `POST /api/v1/login` (form fields `username`, `password`) | returns `token`, `expires` |
| `POST /api/v1/logout` | ends the token |
| `GET /api/v1/status` | adapter state, gateway name, uptime, session and pool counts, uplink state, pairing window state and PIN while one is open |
| `GET /api/v1/sessions` | one record per session: name, address, profile, interface, IP, duration, bytes |
| `GET /api/v1/devices` | allowlisted devices: name, address, connected, last seen |
| `GET /api/v1/me` | the requester's own session, or `none` |
| `GET /api/v1/events?since=N` | recent events, for a polling event list |
| `POST /api/v1/pair` (`minutes`, `one`) | opens a pairing window and returns its PIN |
| `POST /api/v1/pair/close` | closes it |
| `POST /api/v1/devices/<addr>/disconnect` | as `palm-gwctl disconnect` |
| `POST /api/v1/devices/<addr>/forget` | as `palm-gwctl forget` |
| `POST /api/v1/devices/<addr>/rename` (`name`) | sets the alias |
| `GET /api/v1/firewall` | the allow rules |
| `POST /api/v1/firewall/allow` / `POST /api/v1/firewall/remove` | add or remove an allow rule, then reload |
| `POST /api/v1/settings` | the settings section 8.12 exposes; replies name anything that needs a restart |

- **Auth.** `Authorization: Bearer <token>`. The token is 128 random bits, kept in memory, and
  expires after `session_hours`. A token is not sent automatically the way a cookie is, so CSRF
  tokens are not needed on the API. The browser UI keeps its cookie and CSRF scheme. A viewer
  token gets 403 on every `POST` except login and logout.
- **Format.** JSON by default. With `?format=text`, the reply is `key=value` lines, records
  separated by a blank line, `\r\n` line endings, charset Windows-1252 (Palm OS's character set,
  so a name like "Zoë's Palm TX" displays correctly; characters outside it become `?`). This is
  easy to parse in C on Palm OS without a JSON library. Errors are `error=<code>` plus
  `message=<text>`. Example:

  ```
  status=ok
  gateway=Palm Gateway
  sessions=1
  pool_in_use=1

  ```

- **Size.** Replies are kept under 2 KB in normal use. That is small for a Bluetooth link but
  kind to Palm memory.

### 7. The Palm app (`palm-app/`)

- C, ARM only (Palm OS 5). The toolchain and build follow Zoe's Palm development how-to, which
  she will hand over when Phase 7 starts. CI builds the `.prc` if the toolchain can run in a
  container; otherwise it is built by hand and the result recorded in `TESTING.md`.
- Screens: login (gateway address, username, password, certificate fingerprint on first use),
  status, sessions, devices with disconnect, forget and rename, pairing (open a window and
  show the PIN large), firewall allow rules, settings.
- It does not store the password. It may remember the gateway address, username and pinned
  fingerprint.

### 8. The port-80 page

`http://10.42.0.1/` serves a small HTML5 page with no login, only on the gateway address. It
says "You are connected", gives the client's IP and the gateway, and links to
`https://10.42.0.1:8042/`. It is aimed at Linux laptops on PAN. Palms use the app. Nothing on it
changes state.

### 9. Configuration

Added to `[web]`:

```toml
username             = "admin"
bluetooth_side       = true    # UI and API on the gateway address, TLS only (D-026, D-039)
viewer_username      = ""      # optional read-only account
viewer_password_hash = ""
```

`config.Validate()` requires `tls != "off"` when `bluetooth_side` is on. It rejects a
`viewer_username` equal to `username`. `allow_cidrs` applies to the LAN listener only.

## Testing (Phase 7)

- `httptest`:
  - every route needs a login on both listeners;
  - the Bluetooth listener refuses plain HTTP;
  - the side cannot be spoofed with `Host` or `X-Forwarded-For`;
  - login with the wrong username or wrong password gives the same reply;
  - token expiry and logout;
  - a viewer token gets 403 on mutations;
  - rate limiting on both listeners;
  - the text format encodes Windows-1252 and degrades to `?`.
- Rendered nftables rules for `bluetooth_side` on and off, checked with `nft -c`.
- On the Pi:
  - From `fake-palm` in its network namespace (a PPP peer), and from a PAN client:
    - Login over TLS works, plain HTTP on 8042 gets no answer, and a pairing window opened
      through the API shows its PIN.
    - `https://<Pi LAN IP>:8042/` is dropped.
    - `nmap` shows 53, 67, 80, 8042 and ICMP only, with 8042 open only on `10.42.0.1`.
  - From the LAN: the full UI, as in the section 10 Phase 7 test.
- The Palm app on a real T|X:
  - Log in, pinning the certificate.
  - Open a pairing window and pair a second device with the PIN shown on the T|X.
  - Disconnect and forget that device.
  - Add and remove a firewall allow rule, and check its effect.
  - Measure the TLS handshake time.

## Open questions for Zoe

1. **Admin from laptops on PAN.** D-039 gives the Bluetooth listener the full route set, so a
   laptop on PAN that logs in over HTTPS also gets admin. Is that what you want, or should
   browsers on the Bluetooth side stay read-only and only the app get admin?
2. **More than one admin.** Is one admin username enough, or do you want several accounts?
3. **Your Palm development how-to.** Please drop it into the repository (for example
   `docs/PALM-DEV.md`) or point to it before Phase 7 starts.

Resolved on 2026-11-10 (D-039): the app is written in this repository, ARM only; it gets admin;
it must use TLS; other devices are visible, since the app is an admin interface.
