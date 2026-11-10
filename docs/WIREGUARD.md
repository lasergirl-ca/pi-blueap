# WireGuard uplink: design sketch

**Status:** draft for discussion, 2026-11-09. Nothing here is built. This is the added scope
recorded as DECISIONS.md D-018, planned for the last phase (Phase 8). The open questions at the
end need Zoe's answers before any code is written.

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
this matters more than on Ethernet):

```
oifname "wg0" tcp flags syn tcp option maxseg size set rt mtu
oifname "wg0" accept
```

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

Today `palm_forward` drops all RFC 1918 destinations (`palm->rfc1918`), so Palms cannot reach
private LANs. Networks behind the concentrator are usually private addresses, so this rule would
block them. The proposal is a `reach` list in config: those prefixes are allowed only when the
packet leaves through `wg0`. The home LAN stays blocked.

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
on_down        = "fail_closed"      # or "fallback"
dns_via_tunnel = true
reach          = []                 # private prefixes behind the concentrator Palms may reach
```

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

- **`palm-gwctl wg up|down|show`**, and a matching panel in the web UI.
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

## Open questions for Zoe

1. **Full or split tunnel?** Should all Palm traffic go through the concentrator, or only
   traffic to certain networks, with the rest using the normal uplink?
2. **When the tunnel is down,** should Palm traffic stop (fail closed) or fall back to the
   normal uplink?
3. **The Pi's own traffic.** Should updates, SSH and the web UI also use the tunnel, or stay on
   the normal uplink?
4. **Keys.** Will the concentrator's admin hand you a ready `.conf` file, or should the gateway
   generate its own keys and print the public key for them?
5. **DNS.** Should Palm DNS lookups go through the tunnel, and is there a resolver on the
   concentrator's side to use?
6. **Networks behind the concentrator.** Should Palms reach private networks there (which
   ones?), or only the internet through it?
7. **"Devices" in your request.** Does this mean PAN clients such as phones and laptops on
   `br-palm` (covered above, since they share 10.42.0.0/24), or something else, such as LAN
   hosts using the Pi as a gateway?
8. **Concentrator software.** Is it plain WireGuard, or a product built on it (for example
   Firezone, Netmaker, Tailscale or a firewall appliance)? Some products need their own client
   and would change the approach.
