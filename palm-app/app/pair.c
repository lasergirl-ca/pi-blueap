/* Pair and Settings (D-064), in their own code section (pairf). Every
 * function's prototype carries the section, here or in ui.h. */
#include <PalmOS.h>
#include "resources.h"
#include "http.h"
#include "ui.h"

static void pairShow(FormType *f) PAIR_SEC;
static void pairOpen(FormType *f) PAIR_SEC;
static Boolean pairCheck(void) PAIR_SEC;
static void setLoad(FormType *f) PAIR_SEC;
static void setSave(FormType *f) PAIR_SEC;
static void setNetwork(void) PAIR_SEC;
static void setPassword(void) PAIR_SEC;
static void setRestart(void) PAIR_SEC;
static Boolean yes(const KvRecord *r, const Char *key) PAIR_SEC;

static Boolean gPairOpen;       /* a window this form knows of is open */
static UInt32 gPairUntil;       /* when it closes, TimGetSeconds */
static UInt32 gPairNextCheck;   /* when to ask the gateway again */
static UInt32 gEventSeq;        /* the last event seen */
static Char gPin[24];
static Char gPairText[400];
static Char gPaired[60];

static Boolean yes(const KvRecord *r, const Char *key)
{
	return kvInt(r, key, 0) != 0;
}

Boolean pairTicking(void)
{
	return gPairOpen && FrmGetActiveFormID() == PairForm;
}

/* pairShow lays out the PIN, the time left and the steps. */
static void pairShow(FormType *f)
{
	UInt32 now = TimGetSeconds(), left = gPairUntil > now ? gPairUntil - now : 0;

	if (gPairOpen) {
		StrPrintF(gPairText, "Closes in %lu:%02lu.%s%s\nOn the Palm: Prefs, Bluetooth, Setup Devices, Trusted Devices, Add Device. Pick %s and enter the PIN above.",
			left / 60, left % 60, gPaired[0] ? "\nPaired: " : "", gPaired, gCur.name[0] ? gCur.name : "the gateway");
	} else {
		gPin[0] = 0;
		StrPrintF(gPairText, "No pairing window is open.%s%s\nOpen one, then add the gateway on the Palm being paired. A new PIN is made for each window.",
			gPaired[0] ? "\nPaired: " : "", gPaired);
	}
	setField(f, PairPin, gPin, true);
	setField(f, PairText, gPairText, true);
}

/* pairCheck asks the gateway about the window and what happened since;
 * false when the call failed. */
static Boolean pairCheck(void)
{
	static Char path[64];
	KvRecord r;
	UInt16 pos = 0;
	Char ev[24], name[48];

	StrPrintF(path, "events?since=%lu", gEventSeq);
	if (api("GET", apiPath(path), NULL)) {
		if (apiHead(&r)) {
			gEventSeq = (UInt32)kvInt(&r, "last", (Int32)gEventSeq);
		}
		while (apiItem(&pos, &r)) {
			kvGet(&r, "event", ev, sizeof ev);
			if (StrCompare(ev, "paired") == 0) {
				if (!kvGet(&r, "name", name, sizeof name) || name[0] == 0) {
					kvGet(&r, "addr", name, sizeof name);
				}
				StrNCopy(gPaired, name, sizeof gPaired - 1);
			}
		}
	}
	if (apiGone()) {
		return false;
	}
	if (!api("GET", apiPath("status"), NULL) || !apiHead(&r)) {
		return false;
	}
	gPairOpen = yes(&r, "pairing");
	if (gPairOpen) {
		gPairUntil = TimGetSeconds() + (UInt32)kvInt(&r, "pairing_expires_in", 0);
		kvGet(&r, "pin", gPin, sizeof gPin);
	}
	return true;
}

void pairTick(void)
{
	FormType *f = FrmGetActiveForm();
	if (!pairTicking()) {
		return;
	}
	if (TimGetSeconds() >= gPairNextCheck) {
		pairCheck();
		gPairNextCheck = TimGetSeconds() + 10;
	}
	pairShow(f);
}

static void pairOpen(FormType *f)
{
	static UInt16 mins[] = { 1, 2, 5, 10, 15, 30 };
	Int16 i = LstGetSelection(getObject(f, PairMinList));
	Boolean one = CtlGetValue(getObject(f, PairOne)) != 0;
	KvRecord r;
	Char m[6];

	if (i < 0 || i > 5) {
		i = 2;
	}
	StrPrintF(m, "%u", mins[i]);
	formReset();
	formAdd("minutes", m);
	formAdd("one", one ? "1" : "0");
	gPaired[0] = 0;
	if (!api("POST", apiPath("pair"), gForm) || !apiHead(&r)) {
		fail("The pairing window did not open.");
		return;
	}
	kvGet(&r, "pin", gPin, sizeof gPin);
	gPairUntil = TimGetSeconds() + (UInt32)kvInt(&r, "expires_in", 0);
	gPairOpen = true;
	gPairNextCheck = TimGetSeconds() + 10;
	pairShow(f);
}

Boolean pairHandle(EventType *e)
{
	FormType *f = FrmGetActiveForm();
	KvRecord r;

	switch (e->eType) {
	case frmOpenEvent:
		LstSetSelection(getObject(f, PairMinList), 2);
		CtlSetLabel(getObject(f, PairMinTrig), LstGetSelectionText(getObject(f, PairMinList), 2));
		CtlSetValue(getObject(f, PairOne), 1);
		gPin[0] = gPairText[0] = 0;
		setField(f, PairPin, gPin, false);
		setField(f, PairText, gPairText, false);
		FrmDrawForm(f);
		/* the events so far, so only new ones count */
		gEventSeq = 0;
		if (api("GET", apiPath("events"), NULL) && apiHead(&r)) {
			gEventSeq = (UInt32)kvInt(&r, "last", 0);
		}
		gPaired[0] = 0;
		pairCheck();
		gPairNextCheck = TimGetSeconds() + 10;
		pairShow(f);
		return true;
	case ctlSelectEvent:
		switch (e->data.ctlSelect.controlID) {
		case PairOpen:
			pairOpen(f);
			return true;
		case PairClose:
			if (api("POST", apiPath("pair/close"), "")) {
				gPairOpen = false;
				pairShow(f);
			} else {
				fail("The window was not closed.");
			}
			return true;
		case PairDone:
			FrmGotoForm(StatusForm);
			return true;
		}
		break;
	case menuEvent:
		if (e->data.menu.itemID == PairMenuClass) {
			if (confirm("Some Palms list only devices that look like a computer. Switch the gateway's device class? ",
					"Bluetooth and palm-gwd restart: this Palm's connection drops for about 20 seconds; switch again to go back.")) {
				if (api("POST", apiPath("class-toggle"), "") && apiHead(&r)) {
					Char c[12];
					kvGet(&r, "class", c, sizeof c);
					info("The device class is now ", c);
					apiLeft("Bluetooth is restarting with the new device class. Connect again in about 20 seconds.");
				} else {
					fail("The device class was not switched.");
				}
			}
			return true;
		}
		break;
	default:
		break;
	}
	return false;
}

/* ---- Settings ---- */

#define adMax 6
static Char gAdLabel[adMax][64];
static Char gAdValue[adMax][40];
static Char *gAdChoice[adMax];
static Char gAdTrig[28];
static UInt16 gAdCount;
static Int16 gAdWas;            /* the adapter selected when the form opened */
static Int16 gIdleWas;
static Boolean gDebugWas;
static Char gNameWas[48];
static Char gSetInfo[400];
static Char gSubnet[24];
static Boolean gHttps, gBtSide;
static UInt32 gIdleSecs[] = { 0, 300, 600, 900, 1800, 3600 };

static void setLoad(FormType *f)
{
	KvRecord r;
	UInt16 pos = 0, i;
	Int32 idle;
	Char v[64], up[16], gw[24], tls[16], last[200];
	ListType *ad = getObject(f, SetAdList), *il = getObject(f, SetIdleList);

	gAdCount = 0;
	gAdWas = gIdleWas = -1;
	if (!api("GET", apiPath("settings"), NULL) || !apiHead(&r)) {
		StrPrintF(gSetInfo, "Settings not read.\n%s", gWhy);
		setField(f, SetInfo, gSetInfo, true);
		return;
	}
	kvGet(&r, "name", gNameWas, sizeof gNameWas);
	setEditable(f, SetName, gNameWas);
	idle = kvInt(&r, "idle_seconds", 0);
	for (i = 0; i < 6; i++) {
		if ((Int32)gIdleSecs[i] == idle) {
			gIdleWas = (Int16)i;
		}
	}
	LstSetSelection(il, gIdleWas);
	CtlSetLabel(getObject(f, SetIdleTrig), gIdleWas >= 0 ? LstGetSelectionText(il, gIdleWas) : "Other");
	gDebugWas = yes(&r, "ppp_debug");
	CtlSetValue(getObject(f, SetDebug), gDebugWas);
	kvGet(&r, "uplink", up, sizeof up);
	kvGet(&r, "subnet", gSubnet, sizeof gSubnet);
	kvGet(&r, "gateway", gw, sizeof gw);
	kvGet(&r, "tls", tls, sizeof tls);
	gHttps = StrCompare(tls, "off") != 0;
	gBtSide = yes(&r, "bluetooth_side");
	last[0] = 0;
	if (yes(&r, "applying")) {
		StrCopy(last, "\nNetwork settings are being applied now.");
	} else if (kvGet(&r, "last_apply_error", v, sizeof v) && v[0] != 0) {
		StrPrintF(last, "\nThe last apply failed: %s", v);
	}
	if (kvGet(&r, "subnet_clash", v, sizeof v)) {
		StrPrintF(last + StrLen(last), "\nThe Palm network clashes with %s.", v);
	}
	StrPrintF(gSetInfo, "Internet: %s (Internet form)\nPalm network: %s, gateway %s\nWeb UI: %s, %s over Bluetooth%s\nMenu: network, password, Palm devices, restart.",
		up, gSubnet, gw, gHttps ? "HTTPS" : "plain HTTP", gBtSide ? "also" : "not", last);
	while (gAdCount < adMax && apiItem(&pos, &r)) {
		kvGet(&r, "label", gAdLabel[gAdCount], sizeof gAdLabel[0]);
		kvGet(&r, "value", gAdValue[gAdCount], sizeof gAdValue[0]);
		gAdChoice[gAdCount] = gAdLabel[gAdCount];
		if (yes(&r, "selected")) {
			gAdWas = (Int16)gAdCount;
		}
		gAdCount++;
	}
	LstSetListChoices(ad, gAdChoice, (Int16)gAdCount);
	LstSetHeight(ad, (Int16)(gAdCount > 0 ? gAdCount : 1));
	LstSetSelection(ad, gAdWas);
	StrNCopy(gAdTrig, gAdWas >= 0 ? gAdLabel[gAdWas] : "?", sizeof gAdTrig - 1);
	gAdTrig[sizeof gAdTrig - 1] = 0;
	CtlSetLabel(getObject(f, SetAdTrig), gAdTrig);
	setField(f, SetInfo, gSetInfo, true);
	FrmDrawForm(f);
}

static void setSave(FormType *f)
{
	Char name[48], secs[8];
	Int16 ad = LstGetSelection(getObject(f, SetAdList)), idle = LstGetSelection(getObject(f, SetIdleList));
	Boolean debug = CtlGetValue(getObject(f, SetDebug)) != 0, adapterChanged = false;

	formReset();
	getEditable(f, SetName, name, sizeof name);
	if (StrCompare(name, gNameWas) != 0) {
		formAdd("name", name);
	}
	if (ad >= 0 && ad != gAdWas && ad < (Int16)gAdCount) {
		formAdd("adapter", gAdValue[ad]);
		adapterChanged = true;
	}
	if (idle >= 0 && idle != gIdleWas) {
		StrPrintF(secs, "%lu", gIdleSecs[idle]);
		formAdd("idle_seconds", secs);
	}
	if (debug != gDebugWas) {
		formAdd("ppp_debug", debug ? "1" : "0");
	}
	if (gForm[0] == 0 && formOK()) {
		info("Nothing changed.", NULL);
		return;
	}
	if (!formOK() || !api("POST", apiPath("settings"), gForm)) {
		fail("Not saved.");
		return;
	}
	if (adapterChanged) {
		if (confirm("Saved. The new adapter is used after palm-gwd restarts, which drops this Palm's connection. ", "Restart now?")) {
			setRestart();
			return;
		}
	} else {
		info("Saved. The name changes at once; the rest applies from the next connection.", NULL);
	}
	setLoad(f);
}

static void setNetwork(void)
{
	Char subnet[24];
	KvRecord r;
	Boolean https = gHttps, bt = gBtSide;

	StrCopy(subnet, gSubnet);
	if (!inputDialog("Network and web access", "Palm network (A.B.C.0/24):", subnet, sizeof subnet, NULL, NULL, 0, NULL, NULL, 0,
			"HTTPS (needed over Bluetooth)", &https, "Web UI over Bluetooth too", &bt)) {
		return;
	}
	if (!confirm("Applying rewrites the firewall and the Palm network, then restarts palm-gwd: this Palm's connection drops for about 20 seconds. ",
			"A new Palm network also makes a new certificate, which this app must trust again.")) {
		return;
	}
	formReset();
	formAdd("subnet", subnet);
	formAdd("tls", https ? "self-signed" : "off");
	formAdd("bluetooth_side", https && bt ? "1" : "0");
	if (!formOK() || !api("POST", apiPath("network"), gForm) || !apiHead(&r)) {
		fail("Not applied.");
		return;
	}
	if (!yes(&r, "applying")) {
		info("Nothing changed.", NULL);
		return;
	}
	apiLeft("Applying the network settings: palm-gwd restarts. Connect again in about 20 seconds.");
}

static void setPassword(void)
{
	Char cur[65], nw[65], again[65];
	Boolean ok;

	cur[0] = nw[0] = again[0] = 0;
	gInSecret = 7;
	if (!inputDialog("Change password", "Current password:", cur, sizeof cur, "New password (8 or more):", nw, sizeof nw,
			"New password again:", again, sizeof again, NULL, NULL, NULL, NULL)) {
		return;
	}
	if (StrCompare(nw, again) != 0 || StrLen(nw) < 8) {
		MemSet(cur, sizeof cur, 0);
		gWhy[0] = 0;
		fail(StrLen(nw) < 8 ? "The new password needs 8 characters or more." : "The two new passwords differ.");
		MemSet(nw, sizeof nw, 0);
		MemSet(again, sizeof again, 0);
		return;
	}
	formReset();
	formAdd("current", cur);
	formAdd("new", nw);
	MemSet(cur, sizeof cur, 0);
	MemSet(nw, sizeof nw, 0);
	MemSet(again, sizeof again, 0);
	/* a wrong current password answers 401: an answer, not a logout */
	gKeep401 = true;
	ok = formOK() && api("POST", apiPath("password"), gForm);
	gKeep401 = false;
	formReset();
	if (ok) {
		info("Password changed. Palms that log in with a device key keep working.", NULL);
	} else {
		fail("The password was not changed.");
	}
}

static void setRestart(void)
{
	if (api("POST", apiPath("restart"), "")) {
		apiLeft("palm-gwd is restarting. Connect again in about 10 seconds.");
	} else {
		fail("palm-gwd was not restarted.");
	}
}

Boolean setHandle(EventType *e)
{
	FormType *f = FrmGetActiveForm();

	switch (e->eType) {
	case frmOpenEvent:
		gSetInfo[0] = 0;
		setField(f, SetInfo, gSetInfo, false);
		FrmDrawForm(f);
		setLoad(f);
		return true;
	case popSelectEvent:
		if (e->data.popSelect.controlID == SetAdTrig && e->data.popSelect.selection >= 0) {
			StrNCopy(gAdTrig, gAdLabel[e->data.popSelect.selection], sizeof gAdTrig - 1);
			gAdTrig[sizeof gAdTrig - 1] = 0;
			LstSetSelection(getObject(f, SetAdList), e->data.popSelect.selection);
			CtlSetLabel(getObject(f, SetAdTrig), gAdTrig);
			return true;
		}
		break;
	case ctlSelectEvent:
		switch (e->data.ctlSelect.controlID) {
		case SetSave:
			setSave(f);
			return true;
		case SetDone:
			FrmGotoForm(StatusForm);
			return true;
		}
		break;
	case menuEvent:
		switch (e->data.menu.itemID) {
		case SetMenuNetwork:
			setNetwork();
			return true;
		case SetMenuPassword:
			setPassword();
			return true;
		case SetMenuKeys:
			openList(lmKeys);
			return true;
		case SetMenuRestart:
			if (confirm("Restart palm-gwd? Connected Palms, this one too, are disconnected.", NULL)) {
				setRestart();
			}
			return true;
		}
		break;
	default:
		break;
	}
	return false;
}
