# WireGuard uplink: design

**Status:** design agreed 2026-11-12 (D-018, D-069); to be built after the first release. Zoe's answers to the
open questions are at the end. In short: full or split tunnel and fail-closed or fall-back are
both settings, keys are imported from a `.conf` or generated on the Pi, the peer is plain
WireGuard, and **everything behind the tunnel gets the same firewall guards as the home LAN**.

## Goal

Let the gateway act as a WireGuard client, so Palms and other gateway clients reach the network
behind a WireGuard concentrator. Their traffic goes through the tunnel and is NATed to the
gateway's tunnel address, so the concentrator sees one peer, not a subnet.

```
Palm ── Bluetooth PPP/PAN ── Pi (10.42.0.1) ══ wg0 tunnel ══ concentrator ── its networks / internet
                               │
                               └── eth0 / wlan0 uplink (carries the encrypted tunnel only)
```

## What stays the same

- Bluetooth, pairing, PPP and PAN do not change. The tunnel is purely a routing decision on the Pi.
- The Pi's own traffic (updates, SSH, the web UI) keeps using the normal uplink by default.
  Question 3 asks whether that is right.
- The existing firewall structure stays. The `inet palm_gw` table gains rules and does not
  replace anything.
- **The guards stay the same everywhere.** `palm_forward` matches destinations by address,
  whatever interface a packet leaves by. So the private-address drop (`palm->rfc1918`) and the
  `[[firewall.allow]]` list apply to hosts behind the tunnel exactly as they do to the home LAN:
  Palms reach the internet through the tunnel, and a private host behind the concentrator only
  when an allow rule names it (host, protocol and port, or ICMP).

## How it would work

### 1. The tunnel interface

NetworkManager already manages `br-palm`, and it supports WireGuard connections natively. The
gateway would create one NetworkManager profile, `palm-gw-wg`, for interface `wg0`:

- Private key generated on the Pi at setup and kept in NetworkManager's secret store (root
  only). The public key is printed so the concentrator's admin can add the peer.
- Alternatively, import a `wg-quick` style `.conf` file that the concentrator's admin hands
  out, which already contains the keys, the endpoint and the tunnel address.
- `PersistentKeepalive = 25`, because the Pi will usually sit behind home NAT.
- `ipv4.never-default yes`, so the tunnel never becomes the Pi's own default route.

This avoids a new Go dependency (`wgctrl`) and a new daemon. NetworkManager brings the tunnel up
at boot and restores it after network changes.

### 2. Policy routing for the Palm side only

Routing rules send traffic from the Palm subnet to a separate routing table that uses the
tunnel. Local destinations are kept out of it first:

```
ip rule add to 10.42.0.0/24      lookup main priority 4240   # Pi -> Palm replies, Palm -> Palm
ip rule add to <uplink LAN>      lookup main priority 4241   # firewall.allow hosts (NetSync etc.)
ip rule add from 10.42.0.0/24    lookup 4242 priority 4242
table 4242:  default dev wg0
```

The first rule matters because the Pi's own replies to Palms come from 10.42.0.1, which is
inside 10.42.0.0/24. Without it they would be routed into the tunnel. The second keeps
`[[firewall.allow]]` LAN hosts reachable directly. Its prefix is the uplink's connected network,
re-read when the uplink changes. NetworkManager can hold the rules and the table in the
`palm-gw-wg` profile (`ipv4.routing-rules`, `ipv4.route-table`). The Pi's own outgoing traffic
uses its uplink address as the source, so it never matches the third rule.

### 3. NAT into the tunnel

In `table inet palm_gw`, chain `postrouting`, next to the existing uplink masquerade:

```
ip saddr 10.42.0.0/24 oifname "wg0" masquerade
```

In `palm_forward`, allow the tunnel as an exit, with MSS clamping (WireGuard's MTU is 1420, so
this matters more than on Ethernet). The two rules go **after** the allow sets and the
`palm->rfc1918` drop, next to the uplink's, so the tunnel is an exit to the internet and to
allowed hosts only:

```
ct state established,related accept
ct state invalid counter drop comment "palm->invalid"
ip daddr . meta l4proto . th dport @lan_allow accept     # home LAN and tunnel hosts alike
ip daddr @lan_allow_icmp ip protocol icmp accept
ip daddr { 10.0.0.0/8, … } counter drop comment "palm->rfc1918"
oifname "wg0" tcp flags syn tcp option maxseg size set rt mtu
oifname "wg0" accept
oifname "{{UPLINK}}" accept                               # only when on_down = "fallback" or split
counter drop comment "palm->other"
```

Nothing may start a connection from the tunnel into the Palm network, so a concentrator that
routes 10.42.0.0/24 back to the Pi still cannot reach a Palm:

```
chain forward { …
    iifname "wg0" oifname { "ppp*", "{{BRIDGE}}" } ct state new counter drop comment "wg->palm"
}
```

The firewall page and `palm-gwctl firewall counters` show `wg->palm` with the other drops.

### 4. When the tunnel is down

Two possible behaviours. Question 2 asks which you want.

- **Fail closed:** table 4242 also holds `unreachable default metric 4096`. With `wg0` down,
  Palm traffic gets "network unreachable" instead of leaking out the normal uplink.
- **Fall back:** no unreachable route. When `wg0` is down the rule's table has no default route,
  lookup continues in the main table, and Palm traffic leaves through `eth0` as it does today.

### 5. DNS

Palms ask dnsmasq on 10.42.0.1. dnsmasq's upstream queries come from the Pi itself, so by
default they bypass the tunnel. That leaks which names the Palms look up. Options:

- Set `dns_upstream` to a resolver on the concentrator's side and route just that address
  through `wg0`. This is a one-line route with no policy rule.
- Or leave DNS on the normal uplink. This is simpler, but the queries are visible locally.

### 6. Reaching networks behind the concentrator

The same way as the home LAN: not at all, unless an allow rule names the host. Today
`palm_forward` drops all private destinations (`palm->rfc1918`) and lets through only what
`[[firewall.allow]]` lists. A host behind the concentrator is added the same way, on the web
UI's Firewall page, `palm-gwctl firewall add`, or the Palm app. The reachability test on that page
runs from the Pi, so it follows the same routes as a Palm. With a split tunnel, the tunnel
carries `allowed_ips` only, so a private host there needs both: its network in `allowed_ips`
and its host in an allow rule.

### 7. Configuration

```toml
[wireguard]
enabled        = false
interface      = "wg0"
config_file    = ""                 # optional wg-quick .conf to import at setup
address        = "10.99.0.7/32"     # the Pi's tunnel address
endpoint       = "vpn.example.net:51820"
peer_public_key = "…"
allowed_ips    = ["0.0.0.0/0"]      # what the tunnel carries for Palm traffic
keepalive_seconds = 25
mode           = "full"             # "full": all Palm traffic; "split": only allowed_ips
on_down        = "fail_closed"      # or "fallback"; full mode only (split never stops traffic)
dns_via_tunnel = true               # dnsmasq's upstream through the tunnel (dns_upstream there)
```

Both `mode` and `on_down` are settings in the web UI and the Palm app, not just in the file.

`config.Validate()` would check that the keys are valid, that `address` does not overlap
`network.subnet`, and that `mode = "split"` comes with specific `allowed_ips`.

### 8. Operation

- **`palm-gw setup`** gains a WireGuard step: skip, import a `.conf`, or generate keys and print
  the public key.
- **`palm-gwctl status`** gains a line showing the tunnel state, the last handshake age, bytes
  in and out, and the active mode:

  ```
  wireguard: up, handshake 42s ago, rx 1.2 MiB tx 310 KiB, full tunnel, fail closed
  ```

- **`palm-gwctl wg up|down|show`**, a WireGuard card on the web UI's Internet page (state, mode,
  on-down behaviour, import a `.conf` or make keys and show the public key, up and down), and
  the same in the Palm app's Internet screen (D-064's parity; the `.conf` import stays in the
  browser, as file downloads do).
- **Health check.** The daemon reads the handshake age every 30 s. If it goes over 3 minutes
  with Palm sessions live, it logs `wg_stale`. The daemon never edits the tunnel itself:
  NetworkManager owns it.

## Testing

- Unit tests for the config validation and the rendered nft rules (`nft -c`) in CI.
- On the Pi, with a test concentrator. A second Pi, a VPS, or the real one would all work.
- **Through the tunnel.** A Palm (or `fake-palm` in a network namespace) loads
  `http://neverssl.com/`. The concentrator sees the request from the Pi's tunnel address, and a
  capture on `eth0` shows only UDP to the endpoint.
- **Tunnel down, fail closed.** With `wg0` down, Palm traffic gets "unreachable", and a capture
  on `eth0` shows no Palm traffic.
- **Tunnel down, fall back.** With `wg0` down, Palm traffic leaves through `eth0`.
- **The Pi's own traffic.** It never enters the tunnel, unless question 3 says it should.
- **Recovery.** Reboot, an uplink change from Ethernet to Wi-Fi, and an endpoint IP change all
  bring the tunnel back without help.

## Zoe's answers, 2026-11-12

1. **Full or split:** both, as a setting (`mode`).
2. **When the tunnel is down:** both, as a setting (`on_down`): stop Palm traffic, or fall
   back to the normal uplink.
3. **The Pi's own traffic:** not asked; it stays on the normal uplink (the sketch's default).
4. **Keys:** both: import a `.conf` from the concentrator's admin, or generate keys on the Pi
   and show the public key.
5. **DNS:** not asked; through the tunnel by default when `mode = "full"` (`dns_via_tunnel`).
6. **Networks behind the concentrator:** the same guards as the home LAN: private hosts there
   only through allow rules; no separate `reach` list.
7. **"Devices":** everything on the Palm network, 10.42.0.0/24: PPP Palms and PAN clients.
8. **Concentrator:** plain WireGuard.

## Build parts

1. Config: the `[wireguard]` table, validation, defaults; `palm-gwctl get/set`.
2. Setup: the NetworkManager profile (import or generate), policy routing for `mode` and
   `on_down`, the nft rules above (`nft -c` in tests), `palm-gw setup --check` for the tunnel.
3. Daemon and CLI: the status line, `wg_stale`, `palm-gwctl wg up|down|show`, the counters.
4. Web UI and API: the WireGuard card, `/api/v1` calls for it.
5. The Palm app: WireGuard on the Internet screen.
6. On the Pi with a test peer: the tests above, each guard checked from a Palm.
