# 4G mobile uplink: design

**Status:** design agreed 2026-11-22 (D-074); not built yet. To be tested on a third Pi 5
with a proper supply (Lyman is in the field; Surrey's supply is too weak for a modem).

## Goal

Let an LTE USB modem, first the SIMCom SIM7600G-H dongle, be the gateway's internet
connection, firewalled as a WAN: it faces the internet, not a trusted LAN. Either as the
gateway's only internet, or as a backup that takes over when Ethernet or Wi-Fi loses the
internet and hands back when it returns (a setting, `role`).

```
Palm ── Bluetooth PPP/PAN ── Pi (10.42.0.1) ── wwan0 (4G, the WAN) ── carrier ── internet
                               │
                               └── eth0 / wlan0 (the LAN; in backup mode, the first choice)
```

## How the dongle appears

The SIM7600G-H has two USB modes, chosen by the module itself:
- **QMI (its default, USB ID `1e0e:9001`):** serial ports (`ttyUSB0`-`4`, one of them AT),
  `cdc-wdm0` and a network interface `wwan0` (`qmi_wwan`). ModemManager drives it, and
  NetworkManager connects it with a `gsm` connection and the APN. The Pi gets the carrier's
  address on `wwan0`, often public, IPv4 and IPv6. This is the main path: signal, carrier,
  access technology and the SIM's state all come from ModemManager.
- **RNDIS/ECM (`AT+CUSBPIDSWITCH=9011,1,1`, USB ID `1e0e:9011`):** the module routes by
  itself and the Pi sees `usb0` with a DHCP address in the module's own network
  (192.168.225.0/24). It needs no ModemManager but gives double NAT and no signal reading.
  The gateway accepts it as a "mobile (router mode)" interface with the same WAN firewall.

Other LTE modems ModemManager supports (Quectel EC25/EG25, Huawei in stick mode) take the
first path unchanged; HiLink-style sticks that act as routers take the second.

## Configuration

```toml
[mobile]
enabled  = false
role     = "only"      # "only": the gateway's internet | "backup": takes over when network.uplink loses the internet
apn      = ""          # empty: ModemManager's guess from the SIM
roaming  = false       # allow data while roaming
check    = "1.1.1.1"   # what "has the internet" is tested against, through each uplink
data_warning_mb = 0    # 0 = off; otherwise a wan_data_warning event once a month passes it
```

A SIM PIN, if the SIM has one, is kept like WireGuard's private key: in NetworkManager's
root-only secrets, set from setup, the web UI or `palm-gwctl`, never in `config.toml`.

## Roles

- **only:** `network.uplink` becomes the modem's interface (`wwan0` or `usb0`), through the
  same apply as switching between Ethernet and Wi-Fi (D-056). The Pi's own traffic uses 4G too.
- **backup:** Ethernet or Wi-Fi stays `network.uplink`. Both have a default route, the modem's
  with a higher metric, so the kernel prefers the LAN. palm-gwd checks every 30 s that
  `check` answers through the uplink (bound to its interface). After 3 failures it lowers the
  modem's route metric below the uplink's and logs `uplink_failover`; after 2 minutes of the
  uplink answering again, it hands back and logs `uplink_restored`. The firewall allows
  Palm traffic out of both at all times, so a switch needs no apply and no restart.

WireGuard rides whichever uplink carries the Pi's default route; its keepalive re-establishes
the tunnel from the new address.

## The WAN firewall

The modem's interface is treated as hostile, in `table inet palm_gw` (so IPv6 too):

```
chain input {        # the Pi itself
    iifname "{{WAN}}" ct state established,related accept
    iifname "{{WAN}}" icmpv6 type { nd-neighbor-solicit, nd-neighbor-advert, nd-router-advert, packet-too-big } accept
    iifname "{{WAN}}" icmp type { destination-unreachable, time-exceeded } accept
    iifname "{{WAN}}" counter drop comment "wan->pi"         # no SSH, web UI or anything else from the internet
}
chain forward {
    iifname "{{WAN}}" oifname { "{{BRIDGE}}", "ppp*" } ct state new counter drop comment "wan->palm"
}
chain postrouting { ip saddr {{SUBNET}} oifname "{{WAN}}" masquerade }
chain palm_forward { ... after palm->rfc1918:
    oifname "{{WAN}}" tcp flags syn tcp option maxseg size set rt mtu
    oifname "{{WAN}}" accept
}
```

- Palms keep their guards: private addresses (the module's own 192.168.225.0/24 in router
  mode included) only through allow rules.
- The web UI never listens on the WAN interface's addresses.
- ICMPv6 neighbour discovery and router adverts stay allowed, or IPv6 on `wwan0` would break.

## DNS and data

- The carrier's DNS reaches the Pi through NetworkManager; dnsmasq follows the Pi's
  `resolv.conf` unless `network.dns_upstream` or `wireguard.dns` is set.
- Cellular data costs money: status shows bytes in and out since boot and for the month (kept
  in the state file), and `data_warning_mb` warns once a month passes it.

## Operation

- **Status:** `mobile: up wwan0, LTE, <carrier>, signal 72%, role backup (standby: Ethernet has
  the internet), 41 MB this month`.
- **`palm-gwctl mobile`** `[show|on|off|role only|backup|apn NAME|pin]`.
- **Web UI:** a Mobile card on the Internet page (signal, carrier, SIM state, role, APN, data).
- **Palm app:** the same on the Internet screen, in a Mobile menu (D-064's parity).
- **Setup:** the wizard offers "Mobile (4G)" as the uplink when a modem is present;
  `palm-gw setup --check` reports the modem, the SIM and the supply.
- **Packaging:** ModemManager becomes a dependency, so the image ships it. Its default
  "strict" filter policy probes only ports that look like modems.

## Power

The SIM7600G-H draws up to about 2 A in transmit bursts. A Pi 5 gives USB at most 600 mA unless
its supply negotiates 5 A (PLAN.md section 3), so it needs the 5 V 5 A supply or a powered USB
hub. `palm-gw setup --check` warns when a modem is present on a weaker supply.

## Tests

- Unit: config validation, the WAN rules rendered in each role (`nft -c` on a Pi), the
  failover state machine with a fake prober.
- On the third Pi with a SIM: only mode; backup mode, failing over when the Ethernet cable is
  pulled and handing back; a T|X browsing through 4G; inbound connections to the cellular
  address from outside refused (from Surrey, which is on the internet directly); IPv6; router
  mode (`usb0`); WireGuard over 4G.

## Build parts

1. Config, validation, the SIM PIN secret.
2. Setup: ModemManager, the NetworkManager `gsm` connection, the WAN firewall, `--check`.
3. Daemon: status, the backup prober and failover, data counting, `palm-gwctl mobile`.
4. Web UI card and `/api/v1/mobile`.
5. Palm app: the Internet screen's Mobile menu.
6. On the third Pi with a SIM.

## Open

- The carrier and APN of the SIM, and whether it gives a public IPv4 or IPv6 address.
- Whether the Pi's own traffic (updates) should avoid 4G in backup mode while it is active.
