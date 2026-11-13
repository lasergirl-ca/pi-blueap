# Connecting a Palm to the gateway

This guide gets a Palm handheld onto the internet through a palm-gw gateway over Bluetooth. It
is written for someone holding the Palm. For the one step where the gateway shows a PIN you
also need the gateway's web page in a browser on your home network, or a shell on the gateway
(the Raspberry Pi), or someone who has one.

Written from a real Palm T|X (Palm OS 5.4), 2026-11-09 to 2026-11-12. Steps marked
*(not yet tested)* come from the design and have not been tried on a real device. Screen names
are given as the T|X shows them. Other models may word them slightly differently.

## What you need

- A Palm with Bluetooth: T|X, LifeDrive, Tungsten T3/T5/E2, Zire 72, Treo 650/680/700p, or a
  Palm OS 4 device with a Bluetooth card.
- The gateway running, within about 10 metres of the Palm.
- About ten minutes.

**Turn the Palm's Wi-Fi off first** (T|X and LifeDrive). Go to Prefs, then Wi-Fi, and switch it
off. If Wi-Fi is on, the Palm tries to use it instead of Bluetooth when you tap Connect.

## Step 1: Pair the Palm with the gateway

Pairing happens once per Palm. The gateway accepts new Palms only while a pairing window is open.
That stops strangers from pairing with it.

1. **On the gateway**, open a pairing window. Either:
   - in a browser on your home network, open `https://<the Pi's address>:8042/`, log in, go
     to **Pair**, choose how long the window stays open, and tap **Open**. The page shows the
     PIN and, below it, each Palm as it pairs; or
   - in a shell on the gateway:

   ```
   palm-gwctl pair 5 --one
   ```

   The command prints a 4-digit PIN and waits:

   ```
   Pairing window open for 5m0s.
   When asked for a passkey, enter:   6 0 2 4
   Waiting... (Ctrl-C closes the window)
   ```

   `5` is how many minutes the window stays open. `--one` closes it as soon as one Palm has
   paired. Leave the command running.

2. **On the Palm**, go to Prefs, then Bluetooth. Set Bluetooth to **On**.
3. Tap **Setup Devices**, then **Trusted Devices**, then **Add Device**. The Palm searches for
   about 10 seconds.
4. Tap **Palm Gateway** in the list and tap OK.
   - If it is not listed, tap the search again. One search sometimes misses the gateway even
     when it is visible.
5. When the Palm asks for a passkey, enter the PIN from step 1.
6. The gateway reports the result: the web page lists the Palm as paired, and the command
   finishes:

   ```
     00:07:E0:3C:5E:FA  "Zoë's Palm TX"  PIN requested
     00:07:E0:3C:5E:FA  "Zoë's Palm TX"  paired, added to allowlist
   Window closed (one device).
   ```

If the Palm offers to go on and set up a network connection, you can cancel and follow steps 2
and 3 below instead. If you continue, its wizard asks for a user name and password. See the note
in step 3.

**If pairing fails** ("Unable to connect" or similar), the usual cause is that no pairing window
was open, or the window timed out. Open a new window on the gateway and try again. A Palm
attempting to pair with no window open is refused on purpose.

## Step 2: Create the connection

This tells the Palm how to reach the gateway.

1. Go to Prefs, then **Connection**, and tap **New**.
2. Fill in:
   - Name: `Palm GW` (any name works)
   - Connect to: **PC**
   - Via: **Bluetooth**
   - Device: tap it and pick **Palm Gateway**
3. Tap OK.

**What "PC" actually does on a T|X.** Despite the name, the T|X looks for the gateway's
**LAN Access** service and starts the network link straight away. It does not use the
Serial Port service or the Windows "CLIENT" exchange that older guides describe. The gateway
offers all three services (Serial Port, LAN Access and Dial-up Networking), so this needs
nothing from you. It matters only if you are reading the gateway's logs:

```
connection    profile=lap channel=2
handshake     mode=none
```

## Step 3: Create the network service

This tells the Palm to run its internet connection over the connection from step 2.

1. Go to Prefs, then **Network**. Tap **Service**, then **New** (or Menu, then New).
2. Fill in:
   - Service: `Palm GW`
   - Connection: **Palm GW** (the one from step 2)
   - User Name and Password: see the note below
3. Tap **Details** and set:
   - Connection type: **PPP**
   - Idle timeout: **Never** (otherwise the Palm drops the link after a few idle minutes)
   - Query DNS: **ticked**
   - IP Address: **Automatic**
4. Tap OK, then OK again.

**User name and password.** The gateway never checks them, so any values work. The T|X will
not save a service with them blank, so type anything, for example `palm` and `palm`. Setting
the password to "unassigned" may also work *(not yet tested)*.

## Step 4: Connect

1. In Prefs, then Network, with service **Palm GW** selected, tap **Connect**.
2. The Palm shows "Opening connection...", then "Signing on...", then "Established". On a T|X
   this takes a second or two.
3. Open **Blazer** and load `http://neverssl.com/`. Modern HTTPS sites will not load, because
   Blazer predates current encryption. Plain `http://` sites are the ones to try.

To disconnect, tap **Disconnect** in the same Network screen. The gateway frees the connection
when the Palm hangs up.

## Checking from the Palm

The Palm has its own diagnostics. In Prefs, then Network, open Menu, then Options, then
**View Log**. It shows the link negotiation as the Palm saw it, including the address it was
given (10.42.0.10 to 10.42.0.99) and the DNS server (10.42.0.1). At the bottom of the log screen
you can type a command such as `ping neverssl.com` to test the link without Blazer *(not yet
tested)*.

The gateway also answers at `http://10.42.0.1/` from the Palm side: Blazer shows a page saying
the Palm is connected, with its address. The palm-gw app (below) shows much more.

## The palm-gw app

The palm-gw app runs the gateway from the Palm: everything the web page does, except
downloading captures and support bundles. It needs Palm OS 5 on an ARM Palm (T|X, LifeDrive,
Tungsten T3/T5/E2, Zire 72, Treo 650 and later) and the gateway's web access over Bluetooth,
which is on unless someone turned it off (`[web] bluetooth_side`).

**Install it.** Download `PalmGW.prc` from the
[latest release](https://github.com/lasergirl-ca/pi-blueap/releases/latest/download/PalmGW.prc)
on a computer, then HotSync it to the Palm, or copy it to the card's `/PALM/Launcher/`
folder. It shows in the Launcher as **palm-gw**, and About palm-gw shows the release it came
from.

**First connection.**
1. Connect the Palm to the gateway (step 4 above).
2. Open palm-gw. The **Gateways** screen lists none yet. Tap **New**.
3. **Log in** has the address `10.42.0.1` filled in. Type the gateway's web user (`admin`
   unless you chose another) and its web password. The password shows as stars; tick
   **Show** to see what you are writing. With the stars showing, the on-screen keyboard stays
   closed (it would show the password): use Graffiti, or tick Show first.
4. Leave **Remember this Palm** ticked if this Palm should log in by itself next time. The
   gateway gives the Palm a key of its own; the password is never stored. Tap **Log in**.
5. **Trust this gateway?** shows the gateway's certificate fingerprint. Compare it with the
   fingerprint on the web page's login screen, then tap **Trust**. The app remembers it and
   refuses a gateway whose certificate changes.

**Status** then shows the gateway: name, version, connections, internet and the pairing
window. **Pair** opens a pairing window for another Palm, with its PIN and a countdown. The
**Gateway** menu has the rest: Devices, Sessions, Internet (Wi-Fi), Firewall, Settings, Logs,
Diagnostics, About palm-gw and Log out. Anything that drops the Palm's own connection
(restarting palm-gwd, network settings, switching between Ethernet and Wi-Fi) asks first,
then goes back to Gateways with a note to connect again in about 20 seconds.

The app keeps several gateways, each known by its certificate. **Details** on the Gateways
screen shows one's address, user and fingerprint; the menu's **Forget** removes it.

| What the app says | What to do |
|---|---|
| The Palm's Bluetooth did not answer (0x305) | Switch Bluetooth off and on in Prefs, then try again |
| No connection to the gateway | Connect the Palm first (Prefs, Network, Connect) |
| Its certificate is not the one trusted | If the gateway's network settings changed, its certificate did too: Forget the gateway in the app and connect again, comparing the new fingerprint |
| The gateway no longer takes this Palm's key | The key was revoked (web page, Settings, Palm devices). Log in with the password |
| The TLS module would not load | Close other apps, or soft-reset the Palm |

## Other connection types

The steps above are the recommended set-up. Two alternatives also work with the gateway.

- **Dial-up as a "phone"** *(does not work on a T|X yet)*. Create the connection with
  **Connect to: Modem** (on some models, "Mobile Phone"), Via: Bluetooth, device Palm Gateway.
  In the network service, set the phone number to `0`. The gateway pretends to be a phone: it
  answers the Palm's modem commands and "dials" any number. On a T|X the dial succeeds but the
  Palm then stays at "Connecting" and gives up after about a minute and a half. Use the "PC"
  connection instead. Other models are untested.
- **Local Network** *(not yet tested)*. Where a model offers "Connect to: Local Network" via
  Bluetooth, it uses the same LAN Access service as the T|X's "PC" setting.

## Older and other models

- **Treo 650/680/700p, Tungsten T3/T5** *(not yet tested)*. The screens are the same, but
  Connection is a top-level item in Prefs.
- **Zire 72, LifeDrive** *(not yet tested)*. Expected to match the T|X. A LifeDrive is paired
  with the test gateway; its connection steps are still to be written from its own screens.
- **Palm OS 4 devices** such as the Tungsten T or m-series with a Bluetooth card *(not yet
  tested)*. Some of these wait for the computer to speak first. If one stalls at "Signing on...",
  add a login script in the network service under Details, then Script:
  - Send CR
  - Delay 1
  - Send: `CLIENT`
  - Wait for: `CLIENTSERVER`
  - End

  The gateway handles both behaviours, so the script is only needed if a device stalls.

## If something goes wrong

| What you see | Likely cause | What to do |
|---|---|---|
| Palm Gateway is not in the Add Device list | One search missed it, or the gateway is too far away | Search again; move closer |
| Pairing fails with "Unable to connect" | No pairing window open, or wrong PIN | Open a new window (the web page's Pair, or `palm-gwctl pair 5 --one`) and use the new PIN |
| Connect fails straight away after a Palm reset | The Palm lost its pairing but the gateway still has it | On the gateway run `palm-gwctl forget <address>`, delete Palm Gateway from Trusted Devices on the Palm, and pair again |
| Connect fails although pairing worked earlier | The gateway's allowlist no longer has this Palm | Pair again with a pairing window, or run `palm-gwctl allow <address>` on the gateway |
| Connected, but no web pages load | HTTPS site, or DNS | Try `http://neverssl.com/`; check Query DNS is ticked |
| "Modem not found", or stuck at "Connecting", with a Modem connection | Dial-up does not work on a T\|X yet | Use a connection with Connect to: PC |
| The Palm uses Wi-Fi instead | Wi-Fi is on | Turn Wi-Fi off in Prefs |
| The link drops after a few idle minutes | Palm idle timeout | Set Idle timeout to Never in the network service Details |
| "Error: Serial: timeout. Could be bad cable or faulty Modem. (0x0305)" | The Palm's own Bluetooth radio stopped answering the Palm | In Prefs, Bluetooth, switch Bluetooth off and on, then connect again. If it keeps happening, soft-reset the Palm. On a Pi 5, also check the gateway's power supply (`TROUBLESHOOTING.md`) |

On the gateway, `palm-gwctl status` lists paired devices and live connections, and the web
page's Logs shows every pairing attempt and connection with the Palm's Bluetooth address (or
`journalctl -u palm-gwd -f` in a shell). [`TROUBLESHOOTING.md`](TROUBLESHOOTING.md) covers
the gateway's side.
