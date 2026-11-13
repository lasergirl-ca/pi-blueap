/* What each list and text form shows and does (D-064), in its own code
 * section (modef): Devices, Firewall, Internet (Wi-Fi), Palm devices;
 * Sessions, drop counters, NAT, Logs, Diagnostics, About. */
#include <PalmOS.h>
#include "resources.h"
#include "http.h"
#include "ui.h"

#ifndef APP_BUILD
#define APP_BUILD "dev"
#endif

static void setLabels(const Char *a, const Char *b, const Char *c) MODE_SEC;
static void row(const Char *text, const Char *key) MODE_SEC;
static Boolean flag(const KvRecord *r, const Char *key) MODE_SEC;
static Boolean loadDevices(void) MODE_SEC;
static Boolean loadRules(void) MODE_SEC;
static Boolean loadWifi(void) MODE_SEC;
static Boolean loadKeys(void) MODE_SEC;
static Boolean loadSessions(void) MODE_SEC;
static Boolean loadCounters(void) MODE_SEC;
static Boolean loadNAT(void) MODE_SEC;
static Boolean loadLogs(void) MODE_SEC;
static Boolean loadDiag(void) MODE_SEC;
static Boolean loadAbout(void) MODE_SEC;
static Boolean post(const Char *what, const Char *failed) MODE_SEC;
static Boolean deviceAction(const Char *action, Int16 sel) MODE_SEC;
static Boolean ruleEdit(Int16 sel) MODE_SEC;
static Boolean wifiJoin(const Char *ssid, Boolean hidden) MODE_SEC;
static Boolean wifiUse(void) MODE_SEC;
static void bytes(UInt32 n, Char *out) MODE_SEC;

#define lineMax 160 /* a row is built here, then cut to rowLen */

static Boolean gWifiInUse;    /* the uplink is the Wi-Fi interface */
static Boolean gWifiOn;
static Char gLogEvent[24], gLogText[40];

static void setLabels(const Char *a, const Char *b, const Char *c)
{
	StrCopy(gLabel[0], a);
	StrCopy(gLabel[1], b);
	StrCopy(gLabel[2], c);
}

static void row(const Char *text, const Char *key)
{
	if (gRowCount >= rowMax) {
		return;
	}
	StrNCopy(gRows[gRowCount], text, rowLen - 1);
	gRows[gRowCount][rowLen - 1] = 0;
	StrNCopy(gKeys[gRowCount], key, keyLen - 1);
	gKeys[gRowCount][keyLen - 1] = 0;
	gRowCount++;
}

static Boolean flag(const KvRecord *r, const Char *key)
{
	return kvInt(r, key, 0) != 0;
}

static void bytes(UInt32 n, Char *out)
{
	if (n >= 1048576UL) {
		StrPrintF(out, "%lu.%lu MB", n / 1048576UL, n % 1048576UL * 10 / 1048576UL);
	} else if (n >= 1024) {
		StrPrintF(out, "%lu KB", n / 1024);
	} else {
		StrPrintF(out, "%lu B", n);
	}
}

void modeSetup(UInt16 mode, Boolean list, Char *title)
{
	(void)list;
	switch (mode) {
	case lmDevices:
		StrCopy(title, "Devices");
		setLabels("Discon", "Forget", "Rename");
		break;
	case lmRules:
		StrCopy(title, "Firewall: LAN hosts");
		setLabels("Add", "Edit", "Remove");
		break;
	case lmWifi:
		StrCopy(title, "Internet");
		setLabels("Join", gWifiInUse ? "Use Eth" : "Use WiFi", "Scan");
		break;
	case lmKeys:
		StrCopy(title, "Palm devices");
		setLabels("Revoke", "", "");
		break;
	case tmSessions:
		StrCopy(title, "Sessions");
		setLabels("Refresh", "", "");
		break;
	case tmCounters:
		StrCopy(title, "Drop counters");
		setLabels("Refresh", "", "");
		break;
	case tmNAT:
		StrCopy(title, "NAT table");
		setLabels("Refresh", "", "");
		break;
	case tmLogs:
		StrCopy(title, "Logs");
		setLabels("Refresh", "Filter", "");
		break;
	case tmDiag:
		StrCopy(title, "Diagnostics");
		setLabels("Refresh", "Capture", "Bundle");
		break;
	case tmAbout:
		StrCopy(title, "About palm-gw");
		setLabels("Updates", "", "");
		break;
	default:
		StrCopy(title, "palm-gw");
		setLabels("", "", "");
	}
}

UInt16 modeMenu(UInt16 mode)
{
	switch (mode) {
	case lmRules:
		return MenuRules;
	case lmWifi:
		return MenuWifi;
	case lmKeys:
		return MenuKeys;
	default:
		return MenuDevices;
	}
}

Boolean modeLoad(UInt16 mode, Boolean list)
{
	(void)list;
	switch (mode) {
	case lmDevices:
		return loadDevices();
	case lmRules:
		return loadRules();
	case lmWifi:
		return loadWifi();
	case lmKeys:
		return loadKeys();
	case tmSessions:
		return loadSessions();
	case tmCounters:
		return loadCounters();
	case tmNAT:
		return loadNAT();
	case tmLogs:
		return loadLogs();
	case tmDiag:
		return loadDiag();
	case tmAbout:
		return loadAbout();
	}
	return false;
}

/* ---- the lists ---- */

static Boolean loadDevices(void)
{
	KvRecord r;
	UInt16 pos = 0, n = 0, on = 0;
	Char name[40], addr[20], via[32], text[lineMax];

	if (!api("GET", apiPath("devices"), NULL)) {
		return false;
	}
	while (apiItem(&pos, &r)) {
		kvGet(&r, "name", name, sizeof name);
		kvGet(&r, "addr", addr, sizeof addr);
		kvGet(&r, "via", via, sizeof via);
		if (flag(&r, "connected")) {
			StrPrintF(text, "* %s, %s", name[0] ? name : addr, via);
			on++;
		} else {
			StrPrintF(text, "  %s", name[0] ? name : addr);
		}
		row(text, addr);
		n++;
	}
	StrPrintF(gInfo, "%u allowed, %u connected (*). Menu: add one by address.", n, on);
	return true;
}

static Boolean loadRules(void)
{
	KvRecord r;
	UInt16 pos = 0;
	Char n[4], host[24], ports[40], comment[32], text[lineMax];

	if (!api("GET", apiPath("firewall"), NULL)) {
		return false;
	}
	while (apiItem(&pos, &r)) {
		kvGet(&r, "n", n, sizeof n);
		kvGet(&r, "host", host, sizeof host);
		kvGet(&r, "ports", ports, sizeof ports);
		kvGet(&r, "comment", comment, sizeof comment);
		StrPrintF(text, "%s %s%s%s", host, ports, comment[0] ? ", " : "", comment);
		row(text, n);
	}
	StrCopy(gInfo, gRowCount ? "Palms reach these LAN hosts and ports; the rest of the LAN is dropped." :
		"No rules: Palms reach the internet and nothing on the LAN.");
	return true;
}

static Boolean loadWifi(void)
{
	KvRecord r;
	UInt16 pos = 0;
	Char kind[12], iface[12], net[34], ip[24], text[lineMax], v[64], sig[6], band[10], sec[12];
	Boolean any = false;

	if (!api("GET", apiPath("internet"), NULL) || !apiHead(&r)) {
		return false;
	}
	gWifiOn = flag(&r, "wifi_on");
	gWifiInUse = false;
	gInfo[0] = 0;
	if (kvGet(&r, "join_ssid", v, sizeof v) && !flag(&r, "join_done")) {
		StrPrintF(gInfo, "Joining %s... ", v);
	} else if (kvGet(&r, "join_error", v, sizeof v) && v[0] != 0) {
		StrPrintF(gInfo, "Last join failed: %s ", v);
	}
	while (apiItem(&pos, &r)) {
		kvGet(&r, "kind", kind, sizeof kind);
		if (StrCompare(kind, "saved") == 0) {
			continue;
		}
		kvGet(&r, "iface", iface, sizeof iface);
		kvGet(&r, "network", net, sizeof net);
		kvGet(&r, "ip", ip, sizeof ip);
		if (flag(&r, "in_use")) {
			StrPrintF(text, "Internet via %s %s. ", StrCompare(kind, "wifi") == 0 ? "Wi-Fi" : "Ethernet", iface);
			StrNCat(gInfo, text, sizeof gInfo);
			gWifiInUse = StrCompare(kind, "wifi") == 0;
		}
		if (StrCompare(kind, "wifi") == 0) {
			StrPrintF(text, "Wi-Fi: %s.", !gWifiOn ? "off" : flag(&r, "connected") ? net : "not connected");
			StrNCat(gInfo, text, sizeof gInfo);
			any = true;
		}
	}
	if (!any) {
		StrNCat(gInfo, "This Pi has no Wi-Fi.", sizeof gInfo);
	}
	if (!any || !gWifiOn) {
		return true;
	}
	if (!api("GET", apiPath("wifi/scan"), NULL)) {
		return false;
	}
	pos = 0;
	while (apiItem(&pos, &r)) {
		kvGet(&r, "ssid", net, sizeof net);
		kvGet(&r, "signal", sig, sizeof sig);
		kvGet(&r, "band", band, sizeof band);
		kvGet(&r, "security", sec, sizeof sec);
		StrPrintF(text, "%s%s %s%% %s%s%s", flag(&r, "active") ? "* " : "", net, sig, band,
			StrCompare(sec, "open") == 0 ? " open" : StrCompare(sec, "wpa") == 0 || StrCompare(sec, "wpa3") == 0 ? "" : " (nmcli)",
			flag(&r, "saved") ? " saved" : "");
		row(text, net);
	}
	return true;
}

static Boolean loadKeys(void)
{
	KvRecord r;
	UInt16 pos = 0;
	Char id[16], name[48], user[34], last[24], text[lineMax];

	if (!api("GET", apiPath("devkeys"), NULL)) {
		return false;
	}
	while (apiItem(&pos, &r)) {
		kvGet(&r, "id", id, sizeof id);
		kvGet(&r, "name", name, sizeof name);
		kvGet(&r, "user", user, sizeof user);
		kvGet(&r, "last_used", last, sizeof last);
		last[10] = 0; /* the date */
		StrPrintF(text, "%s (%s) %s", name, user, last[0] ? last : "never");
		row(text, id);
	}
	StrCopy(gInfo, gRowCount ? "Palms that log in with their own key. Revoke a lost one." : "No Palm has a key.");
	return true;
}

/* post sends gForm as a change, telling the user when it failed; a value
 * too long for gForm stops it before it is sent. */
static Boolean post(const Char *what, const Char *failed)
{
	Boolean ok = formOK() && api("POST", apiPath(what), gForm);
	formReset();
	if (!ok && !apiGone()) {
		fail(failed);
	}
	return ok;
}

static Boolean deviceAction(const Char *action, Int16 sel)
{
	static Char what[64];
	StrPrintF(what, "devices/%s/%s", gKeys[sel], action);
	return post(what, "The gateway did not do it.");
}

static Boolean ruleEdit(Int16 sel)
{
	Char host[24], ports[48], comment[32];
	KvRecord r;
	UInt16 pos = 0, i = 0;

	host[0] = ports[0] = comment[0] = 0;
	if (sel >= 0) {
		/* the rule's values, from the list just read */
		if (!api("GET", apiPath("firewall"), NULL)) {
			fail("Rules not read.");
			return false;
		}
		while (apiItem(&pos, &r)) {
			if (i++ == (UInt16)sel) {
				kvGet(&r, "host", host, sizeof host);
				kvGet(&r, "ports", ports, sizeof ports);
				kvGet(&r, "comment", comment, sizeof comment);
			}
		}
	}
	if (!inputDialog(sel >= 0 ? "Edit rule" : "Add a rule", "Host or network (A.B.C.D/N):", host, sizeof host,
			"Ports (tcp/22 udp/53 icmp):", ports, sizeof ports, "Comment:", comment, sizeof comment, NULL, NULL, NULL, NULL)) {
		return false;
	}
	formReset();
	if (sel >= 0) {
		formAdd("n", gKeys[sel]);
	}
	formAdd("host", host);
	formAdd("ports", ports);
	formAdd("comment", comment);
	return post(sel >= 0 ? "firewall/save" : "firewall/allow", "The rule was not saved.");
}

/* wifiJoin starts a join and follows it: the gateway reports how it went,
 * or restarts to switch the internet connection to Wi-Fi. */
static Boolean wifiJoin(const Char *given, Boolean hidden)
{
	Char ssid[34], pass[64], v[100];
	KvRecord r;
	UInt16 i, misses = 0;
	FormType *f;

	StrNCopy(ssid, given != NULL ? given : "", sizeof ssid - 1);
	ssid[sizeof ssid - 1] = 0;
	pass[0] = 0;
	gInSecret = 2;
	if (!inputDialog(hidden ? "Join a hidden network" : "Join Wi-Fi", "Network:", ssid, sizeof ssid,
			"Password (if not saved):", pass, sizeof pass, NULL, NULL, 0, NULL, NULL, NULL, NULL)) {
		return false;
	}
	if (!confirm("Join it and use it for the internet? If the uplink moves to Wi-Fi, palm-gwd restarts and this Palm's connection drops.", NULL)) {
		MemSet(pass, sizeof pass, 0);
		return false;
	}
	formReset();
	formAdd("ssid", ssid);
	formAdd("password", pass);
	formAdd("hidden", hidden ? "1" : "0");
	MemSet(pass, sizeof pass, 0);
	if (!post("wifi/join", "The join did not start.")) {
		return false;
	}
	f = FrmGetActiveForm();
	for (i = 0; i < 20; i++) {
		StrPrintF(gInfo, "Joining %s... (up to a minute)", ssid);
		setField(f, ListInfo, gInfo, true);
		SysTaskDelay(SysTicksPerSecond() * 3);
		if (!api("GET", apiPath("internet"), NULL) || !apiHead(&r)) {
			if (apiGone()) {
				return false;
			}
			/* one lost reply is not a switch; three in a row are */
			if (++misses < 3) {
				continue;
			}
			apiLeft("The gateway stopped answering: it is probably switching the internet connection to Wi-Fi. Connect again in about 20 seconds.");
			return false;
		}
		misses = 0;
		if (flag(&r, "join_done")) {
			if (flag(&r, "join_ok")) {
				if (flag(&r, "join_switching")) {
					apiLeft("Joined. palm-gwd is restarting to use Wi-Fi for the internet. Connect again in about 20 seconds.");
					return false;
				}
				info("Joined ", ssid);
			} else {
				kvGet(&r, "join_error", v, sizeof v);
				gWhy[0] = 0;
				fail(v);
			}
			return true;
		}
	}
	info("Still joining; Refresh shows how it went.", NULL);
	return true;
}

static Boolean wifiUse(void)
{
	KvRecord r;

	if (!confirm(gWifiInUse ? "Use Ethernet for the internet? " : "Use the connected Wi-Fi network for the internet? ",
			"palm-gwd restarts to switch, and this Palm's connection drops for about 20 seconds.")) {
		return false;
	}
	formReset();
	formAdd("use", gWifiInUse ? "ethernet" : "wifi");
	if (!post("internet/use", "The internet connection was not switched.") || !apiHead(&r)) {
		return false;
	}
	if (flag(&r, "switching")) {
		apiLeft("Switching the internet connection: palm-gwd restarts. Connect again in about 20 seconds.");
		return false;
	}
	return true;
}

Boolean modeButton(UInt16 mode, Boolean list, UInt16 button, Int16 sel)
{
	Char v1[48], v2[8];
	Boolean b1 = false;
	KvRecord r;

	(void)list;
	formReset();
	switch (mode) {
	case lmDevices:
		if (sel < 0) {
			return false;
		}
		if (button == 1) {
			return deviceAction("disconnect", sel);
		}
		if (button == 2) {
			return confirm("Forget this device? It has to pair again to connect.", NULL) && deviceAction("forget", sel);
		}
		v1[0] = 0;
		if (!inputDialog("Rename", "Label (empty to clear):", v1, sizeof v1, NULL, NULL, 0, NULL, NULL, 0, NULL, NULL, NULL, NULL)) {
			return false;
		}
		formAdd("name", v1);
		return deviceAction("rename", sel);
	case lmRules:
		if (button == 1) {
			return ruleEdit(-1);
		}
		if (sel < 0) {
			return false;
		}
		if (button == 2) {
			return ruleEdit(sel);
		}
		if (!confirm("Remove this rule? Palms lose access to that host at once.", NULL)) {
			return false;
		}
		formAdd("n", gKeys[sel]);
		return post("firewall/remove", "The rule was not removed.");
	case lmWifi:
		if (button == 1) {
			return wifiJoin(sel >= 0 ? gKeys[sel] : NULL, false);
		}
		if (button == 2) {
			return wifiUse();
		}
		if (api("GET", apiPath("wifi/scan?rescan=1"), NULL)) {
			return true;
		}
		fail("The scan failed.");
		return false;
	case lmKeys:
		if (sel < 0 || !confirm("Revoke this Palm's key? It has to log in with the password again.", NULL)) {
			return false;
		}
		formAdd("id", gKeys[sel]);
		return post("devkeys/revoke", "The key was not revoked.");
	case tmLogs:
		if (button == 2) {
			return inputDialog("Filter the logs", "Event (paired, web_login):", gLogEvent, sizeof gLogEvent,
				"Device or text:", gLogText, sizeof gLogText, NULL, NULL, 0, NULL, NULL, NULL, NULL);
		}
		return true;
	case tmDiag:
		if (button == 2) {
			StrCopy(v2, "60");
			if (!inputDialog("Bluetooth capture", "Seconds (1 to 600):", v2, sizeof v2, NULL, NULL, 0, NULL, NULL, 0,
					NULL, NULL, NULL, NULL)) {
				return false;
			}
			formAdd("seconds", v2);
			if (!post("capture", "The capture did not start.")) {
				return false;
			}
			info("Capturing. Download it from the web UI's Diagnostics page when it ends.", NULL);
			return true;
		}
		if (button == 3) {
			if (!inputDialog("Support bundle", NULL, NULL, 0, NULL, NULL, 0, NULL, NULL, 0,
					"Add the newest capture", &b1, NULL, NULL)) {
				return false;
			}
			formAdd("with_capture", b1 ? "1" : "0");
			if (!post("bundle", "The bundle was not made.")) {
				return false;
			}
			if (apiHead(&r)) {
				kvGet(&r, "bundle", v1, sizeof v1);
				info("Made. Download it from the web UI's Diagnostics page: ", v1);
			}
			return true;
		}
		return true;
	case tmAbout:
		if (post("update-check", "The update check failed.") && apiHead(&r)) {
			Char res[100];
			kvGet(&r, "result", res, sizeof res);
			info(res, NULL);
		}
		return false;
	}
	return true;
}

Boolean modeMenuItem(UInt16 mode, UInt16 item, Int16 sel)
{
	Char host[40], port[8], addr[20], name[40], ssid[34];
	Boolean on = item == MenuWifiOn;
	KvRecord r;

	/* an if chain: m68k-palmos-gcc 2.95 fails on this switch's jump table
	 * ("unrecognizable insn") */
	formReset();
	if (item == MenuDevAdd) {
		addr[0] = name[0] = 0;
		if (!inputDialog("Add a device", "Address (00:07:E0:12:34:56):", addr, sizeof addr, "Name:", name, sizeof name,
				NULL, NULL, 0, NULL, NULL, NULL, NULL)) {
			return false;
		}
		formAdd("addr", addr);
		formAdd("name", name);
		return post("devices/allow", "The device was not added.");
	}
	if (item == MenuRuleTest) {
		host[0] = port[0] = 0;
		if (!inputDialog("Test reachability", "Host:", host, sizeof host, "TCP port:", port, sizeof port, NULL, NULL, 0,
				NULL, NULL, NULL, NULL)) {
			return false;
		}
		formAdd("host", host);
		formAdd("port", port);
		if (post("firewall/test", "The test did not run.") && apiHead(&r)) {
			Char ms[8], why[80];
			kvGet(&r, "ms", ms, sizeof ms);
			kvGet(&r, "problem", why, sizeof why);
			/* m68k-palmos-gcc 2.95 -O2 fails ("unrecognizable insn") on
			 * info(gText, c ? " ms" : NULL), so the reply is built whole */
			if (flag(&r, "reached")) {
				StrPrintF(gText, "%s answers from the Pi in %s ms. ", host, ms);
			} else {
				StrPrintF(gText, "%s does not answer from the Pi: %s. ", host, why);
			}
			StrCat(gText, flag(&r, "allowed") ? "A rule lets Palms reach it." : "No rule lets Palms reach it.");
			info(gText, NULL);
		}
		return false;
	}
	if (item == MenuRuleCount || item == MenuRuleNat) {
		openText(item == MenuRuleCount ? tmCounters : tmNAT);
		return false;
	}
	if (item == MenuWifiHidden) {
		return wifiJoin(NULL, true);
	}
	if (item == MenuWifiForget) {
		/* any saved network, in range or not: the selected one to start */
		StrNCopy(ssid, sel >= 0 ? gKeys[sel] : "", sizeof ssid - 1);
		ssid[sizeof ssid - 1] = 0;
		if (!inputDialog("Forget a network", "Saved network:", ssid, sizeof ssid, NULL, NULL, 0, NULL, NULL, 0,
				NULL, NULL, NULL, NULL) || ssid[0] == 0) {
			return false;
		}
		formAdd("ssid", ssid);
		return post("wifi/forget", "The network was not forgotten.");
	}
	if (item == MenuWifiOn || item == MenuWifiOff) {
		formAdd("on", on ? "1" : "0");
		return post("wifi/radio", "The Wi-Fi radio was not switched.");
	}
	(void)mode;
	return false;
}

/* ---- the texts ---- */

static Boolean loadSessions(void)
{
	KvRecord r;
	UInt16 pos = 0, n = 0;
	Char name[40], prof[8], ifn[10], ip[20], line[200], rx[16], tx[16];
	Int32 dur;

	if (!api("GET", apiPath("sessions"), NULL)) {
		return false;
	}
	while (apiItem(&pos, &r)) {
		kvGet(&r, "name", name, sizeof name);
		kvGet(&r, "profile", prof, sizeof prof);
		kvGet(&r, "ifname", ifn, sizeof ifn);
		kvGet(&r, "ip", ip, sizeof ip);
		dur = kvInt(&r, "duration", 0);
		StrPrintF(line, "%s\n  %s %s %s, up %ldm %lds\n", name, prof, ifn, ip, dur / 60, dur % 60);
		textAdd(line);
		if (kvGet(&r, "rx_bytes", rx, sizeof rx)) { /* PPP only */
			bytes(kvU32(&r, "rx_bytes", 0), rx);
			bytes(kvU32(&r, "tx_bytes", 0), tx);
			StrPrintF(line, "  from it %s, to it %s\n", rx, tx);
			textAdd(line);
		}
		n++;
	}
	if (n == 0) {
		textAdd("No Palm or laptop is connected.");
	}
	return true;
}

static Boolean loadCounters(void)
{
	KvRecord r;
	UInt16 pos = 0;
	Char rule[32], line[80], b[16];

	if (!api("GET", apiPath("firewall/counters"), NULL)) {
		return false;
	}
	textAdd("What the drop rules have stopped:\n");
	while (apiItem(&pos, &r)) {
		kvGet(&r, "rule", rule, sizeof rule);
		bytes(kvU32(&r, "bytes", 0), b);
		StrPrintF(line, "%s: %lu packets, %s\n", rule, kvU32(&r, "packets", 0), b);
		textAdd(line);
	}
	return true;
}

static Boolean loadNAT(void)
{
	KvRecord r;
	UInt16 pos = 0;
	Char dev[32], proto[6], from[24], to[24], out[24], st[16], line[160];

	if (!api("GET", apiPath("nat"), NULL) || !apiHead(&r)) {
		return false;
	}
	StrPrintF(line, "%ld from the Palm network, of %ld the Pi tracks.\n", kvInt(&r, "count", 0), kvInt(&r, "total", 0));
	textAdd(line);
	while (apiItem(&pos, &r)) {
		kvGet(&r, "device", dev, sizeof dev);
		kvGet(&r, "proto", proto, sizeof proto);
		kvGet(&r, "from", from, sizeof from);
		kvGet(&r, "to", to, sizeof to);
		kvGet(&r, "out", out, sizeof out);
		kvGet(&r, "state", st, sizeof st);
		StrPrintF(line, "%s %s %s > %s\n  %s%s, %s, %lds\n", dev[0] ? dev : "?", proto, from, to,
			out[0] ? "as " : "not translated", out, st, kvInt(&r, "timeout", 0));
		textAdd(line);
	}
	return true;
}

static Boolean loadLogs(void)
{
	static Char what[260];
	KvRecord r;
	UInt16 pos = 0;
	Char t[20], unit[12], ev[24], addr[20], msg[100], fields[100], line[320];

	formReset();
	formAdd("n", "30");
	if (gLogEvent[0]) {
		formAdd("event", gLogEvent);
	}
	if (gLogText[0]) {
		formAdd("q", gLogText);
	}
	StrPrintF(what, "logs?%s", gForm);
	formReset();
	if (!api("GET", apiPath(what), NULL)) {
		return false;
	}
	if (gLogEvent[0] || gLogText[0]) {
		StrPrintF(line, "Filter: %s %s (Filter to change)\n", gLogEvent, gLogText);
		textAdd(line);
	}
	while (apiItem(&pos, &r)) {
		kvGet(&r, "time", t, sizeof t);
		kvGet(&r, "unit", unit, sizeof unit);
		kvGet(&r, "event", ev, sizeof ev);
		kvGet(&r, "addr", addr, sizeof addr);
		kvGet(&r, "msg", msg, sizeof msg);
		kvGet(&r, "fields", fields, sizeof fields);
		StrPrintF(line, "%s %s %s%s%s %s %s %s\n", t + 7, unit, ev[0] ? "[" : "", ev, ev[0] ? "]" : "", msg, addr, fields);
		textAdd(line);
	}
	return true;
}

static Boolean loadDiag(void)
{
	KvRecord r;
	UInt16 pos = 0;
	Char kind[10], text[100], line[140], v[48];

	if (!api("GET", apiPath("check"), NULL) || !apiHead(&r)) {
		return false;
	}
	kvGet(&r, "board", v, sizeof v);
	textLine("Board", v);
	kvGet(&r, "power", v, sizeof v);
	textLine("Power", v);
	while (apiItem(&pos, &r)) {
		kvGet(&r, "kind", kind, sizeof kind);
		kvGet(&r, "text", text, sizeof text);
		StrPrintF(line, "%s %s: %s\n", flag(&r, "ok") ? "  " : "!!", kind, text);
		textAdd(line);
	}
	if (api("GET", apiPath("diagnostics"), NULL) && apiHead(&r)) {
		if (flag(&r, "capturing")) {
			kvGet(&r, "capture", v, sizeof v);
			StrPrintF(line, "\nCapturing into %s, %ld s left.\n", v, kvInt(&r, "capture_left", 0));
			textAdd(line);
		}
		StrPrintF(line, "\n%ld captures and %ld bundles on the Pi: download them from the web UI's Diagnostics page.\n",
			kvInt(&r, "captures", 0), kvInt(&r, "bundles", 0));
		textAdd(line);
	}
	return true;
}

static Boolean loadAbout(void)
{
	KvRecord r;
	Char v[64], line[100];
	Int32 up;

	textLine("App build", APP_BUILD);
	if (gHandshakeTicks != 0) {
		StrPrintF(line, "%lu ms, heap %lu bytes", gHandshakeTicks * 1000UL / SysTicksPerSecond(), gHeapPeak);
		textLine("Last TLS handshake", line);
	}
	textAdd("\n");
	if (!api("GET", apiPath("about"), NULL) || !apiHead(&r)) {
		return false;
	}
	kvGet(&r, "version", v, sizeof v);
	textLine("Gateway", v);
	up = kvInt(&r, "up", 0);
	StrPrintF(line, "%ldd %ldh %ldm", up / 86400L, up % 86400L / 3600, up % 3600 / 60);
	textLine("Running for", line);
	kvGet(&r, "board", v, sizeof v);
	textLine("Board", v);
	kvGet(&r, "os", v, sizeof v);
	textLine("System", v);
	kvGet(&r, "kernel", v, sizeof v);
	textLine("Kernel", v);
	kvGet(&r, "bluez", v, sizeof v);
	textLine("BlueZ", v);
	kvGet(&r, "adapter_mode", v, sizeof v);
	textLine("Adapter mode", v);
	textAdd("\nGPLv3, with wolfSSL. Updates asks GitHub once; installing is sudo palm-gw upgrade on the Pi.");
	return true;
}
