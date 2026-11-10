# Web UI on the LAN and the Bluetooth side: design

**Status:** design, 2026-11-09. To be built in Phase 7 (section 10), in phase order. Recorded as
DECISIONS.md D-026, which amends section 2. Section 8.12 stays the base design; this file
describes only what changes. The open questions at the end need Zoe's answers before Phase 7.

## What Zoe decided (2026-11-09)

- The UI is reachable from the **LAN** and from **inside the Bluetooth network**, with a
  **username and password** on both.
- On the **Bluetooth side it is read-only.** Pairing, forgetting, firewall changes and settings
  work only from the LAN. Reason: a Palm's radio link uses legacy PIN pairing and cannot be
  trusted to keep a password secret (section 13). If someone captures a password on that link,
  the worst they can do from the Bluetooth side is look.
- **Laptops and phones** (on the LAN, or on the Bluetooth side through PAN) get the normal htmx
  UI from section 8.12. **Palms** use a native Palm OS app (`.prc`) that talks to a small API.
  Blazer is not a target for the admin UI. It keeps the plain `/palm` page on port 80.
- Built in Phase 7, not earlier.

## The two sides

```
LAN browser ── eth0/wlan0 ──► <Pi LAN IP>:8042   full UI + API, admin login
PAN laptop  ── br-palm  ─┐
Palm app    ── ppp*     ─┴──► 10.42.0.1:8042     read-only UI + API, login
Palm Blazer ── ppp*/br-palm ► 10.42.0.1:80       /palm page, no login (unchanged)
```

### 1. Two listeners, two route sets

The daemon runs two `http.Server`s:

- **LAN listener.** Bound exactly as in section 8.12: LAN addresses only, re-evaluated on
  interface changes. All routes.
- **Bluetooth listener.** Bound to the gateway address only (`10.42.0.1:8042`). That one address
  serves both PAN clients (on `br-palm`) and PPP peers (it is the local end of every `ppp*`
  link). Read-only routes only.

Which side a request is on comes only from the listener that accepted it, never from `Host`,
`X-Forwarded-For` or anything else the client sends. The mutating routes are **not registered**
on the Bluetooth listener, so they return 404 instead of relying on a role check. A table-driven
test walks every route on the LAN mux and checks that each mutating one 404s on the Bluetooth
mux, so a new route added later cannot leak across by accident.

### 2. Firewall

In `palm_input`, the explicit drop for the web port becomes an accept for the gateway address
only:

```nft
ip daddr {{GATEWAY}} tcp dport 8042 accept                 # read-only UI and API (D-026)
tcp dport 8042 counter drop comment "palm->webui-lan"      # e.g. the Pi's LAN address
```

A Palm still cannot reach the LAN listener through the Pi's LAN address, and SSH and the
ctl socket stay unreachable. With `bluetooth_side = false` the render uses the section 8.7 drop
line unchanged.

### 3. Accounts

- Section 8.12's single admin account gains a **username** (default `admin`, asked in
  `palm-gw setup`, changed with `palm-gw web set-password --user`). Login checks both fields.
  Wrong username and wrong password give the same error and take the same time.
- An optional **viewer** account (`viewer_username`, `viewer_password_hash`). It can log in on
  either side and is read-only everywhere. Open question 2 asks whether the Bluetooth side should
  accept *only* the viewer account. That would mean the admin password never crosses the radio.

### 4. TLS

- The self-signed certificate from setup gains `10.42.0.1` as a subject alternative name, so the
  same certificate serves both listeners. The fingerprint shown at setup and on the login page
  is the one to pin.
- Laptops on PAN: HTTPS as on the LAN.
- The Palm app: TLS 1.3 if it uses Zoe's wolfSSL port (section 13). Go's TLS 1.3 server and
  wolfSSL interoperate (X25519, AES-128-GCM or ChaCha20-Poly1305). The app pins the certificate
  fingerprint, because a Palm has no CA store worth trusting. A Palm T|X (312 MHz ARM) should
  finish a TLS 1.3 handshake in about a second; to be measured.
- Plain HTTP on the Bluetooth listener is **off** by default (`bluetooth_plain_http = false`).
  It exists only in case the app cannot do TLS. With it on, the password crosses the radio
  unencrypted, which is the reason for open question 2.

### 5. Read-only content on the Bluetooth side

| Shown | Not shown on the Bluetooth side, and why |
|---|---|
| Dashboard: adapter state (name, powered, discoverable; pairing window state but **not the PIN**), live sessions, PAN clients, uplink up/down, last 10 events | `/pair` (opening a window and showing the PIN is a pairing control) |
| `/devices` as a list, without Disconnect, Forget, Rename or Add | `/firewall` (it is a map of LAN hosts and ports, useful to an attacker even read-only) |
| **This device**: the requester's own session or lease (profile, IP, duration, bytes), found by source IP in the session table or the dnsmasq lease file | `/settings`, `/logs`, `/diagnostics` (btmon capture, support bundle), `/about` update check |

The htmx pages are the same templates as on the LAN, rendered with a `readOnly` flag that omits
every form and button. The flag only changes how pages look. Security comes from the route
sets in step 1.

### 6. API for the Palm app

Served on both listeners under `/api/v1/`. Read-only in v1; there is no mutating API.

| Method and path | Returns |
|---|---|
| `POST /api/v1/login` (form fields `username`, `password`) | `token`, `expires` |
| `POST /api/v1/logout` | `ok` |
| `GET /api/v1/status` | adapter state, gateway name, uptime, session and pool counts, uplink state |
| `GET /api/v1/sessions` | one record per session: name, address, profile, interface, IP, duration, bytes |
| `GET /api/v1/devices` | allowlisted devices: name, address, connected, last seen |
| `GET /api/v1/me` | the requester's own session, or `none` |

- **Auth.** `Authorization: Bearer <token>`. The token is 128 random bits, kept in memory, and
  expires after `session_hours`. A token is not sent automatically the way a cookie is, so CSRF
  tokens are not needed on the API. The browser UI keeps its cookie and CSRF scheme. Failed
  logins are rate-limited per source IP as in section 8.12, on both listeners.
- **Format.** JSON by default. With `?format=text`, the reply is `key=value` lines, records
  separated by a blank line, `\r\n` line endings, charset Windows-1252 (Palm OS's character set,
  so a name like "Zoë's Palm TX" displays correctly; characters outside it become `?`). This is
  easy to parse in C on Palm OS without a JSON library. Example:

  ```
  status=ok
  gateway=Palm Gateway
  sessions=1
  pool_in_use=1

  ```

- **Size.** Replies are kept under 2 KB in normal use. That is small for a Bluetooth link but
  kind to Palm memory.

### 7. Configuration

Added to `[web]`:

```toml
username             = "admin"
bluetooth_side       = true    # read-only UI and API on the gateway address (D-026)
bluetooth_plain_http = false   # allow http:// there; credentials then cross the radio unencrypted
viewer_username      = ""      # optional read-only account
viewer_password_hash = ""
```

`config.Validate()` requires `tls != "off"` when `bluetooth_side` is on and
`bluetooth_plain_http` is off. It rejects a `viewer_username` equal to `username`. `allow_cidrs`
applies to the LAN listener only.

## Testing (Phase 7)

- `httptest`: every mutating route 404s on the Bluetooth mux; the side cannot be spoofed with
  `Host` or `X-Forwarded-For`; login with the wrong username or wrong password gives the same
  reply; token expiry and logout; rate limiting on both listeners; the text format encodes
  Windows-1252 and degrades to `?`; templates render with `readOnly` and no forms.
- Rendered nftables rules for `bluetooth_side` on and off, checked with `nft -c`.
- On the Pi:
  - From `fake-palm` in its network namespace (a PPP peer), and from a PAN client:
    - Login and the API work.
    - `POST /pair` returns 404.
    - `https://<Pi LAN IP>:8042/` is dropped.
    - `nmap` shows 53, 67, 80, 8042 and ICMP only, with 8042 open only on `10.42.0.1`.
  - From the LAN: the full UI, as in the section 10 Phase 7 test.
- The Palm app, against the API on a real T|X, once it exists (open question 1).

## Open questions for Zoe

1. **The Palm app.** Does it exist already, or is it to be written? If it is to be written, by
   whom, and does it belong in this repository (C with the Palm OS SDK and prc-tools) or in its
   own? What should it show beyond status, sessions, devices and "this device"?
2. **Admin login on the Bluetooth side.** Should the Bluetooth side accept the admin account,
   or only the viewer account? Recommendation: viewer only. Then even a captured password
   cannot do anything on the LAN side.
3. **TLS in the app.** Will the app use your wolfSSL port, so the Bluetooth side can be
   TLS-only? If not, `bluetooth_plain_http` has to be on for the app to work.
4. **Other devices.** On the Bluetooth side, should a logged-in user see other Palms' names and
   addresses (sessions, devices list, events), or only "this device"? Recommendation: show
   everything read-only, since it is behind a login.
5. **More than one admin.** Is one admin username enough, or do you want several accounts?
