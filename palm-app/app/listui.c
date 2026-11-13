/* The generic forms (D-064), in their own code section (listf): a list
 * with up to three buttons and a menu, a scrolling text with up to three
 * buttons, and an input dialog. What they show and do is modes.c's. */
#include <PalmOS.h>
#include "resources.h"
#include "http.h"
#include "ui.h"

static void listButtons(FormType *f, UInt16 first) LIST_SEC;
static void textScroll(FormType *f) LIST_SEC;
static void inputField(FormType *f, UInt16 lid, UInt16 fid, const Char *label, const Char *value) LIST_SEC;
static void inputCheck(FormType *f, UInt16 id, const Char *label, Char *store, Boolean value) LIST_SEC;
static void inputRead(FormType *f, UInt16 id, Char *out, UInt16 cap) LIST_SEC;
static Boolean inputHandle(EventType *e) LIST_SEC;
static void inputStars(FormType *f, UInt16 i) LIST_SEC;
static void inputWipe(FormType *f, UInt16 id) LIST_SEC;
static void goBack(void) LIST_SEC;

UInt16 gMode;
UInt16 gRowCount;
Char gRows[rowMax][rowLen];
Char gKeys[rowMax][keyLen];
Char gInfo[200];
Char gText[textMax];
Char gLabel[4][12];
static Char *gRowPtr[rowMax];
static Char gTitle[32];
static UInt16 gBackForm = StatusForm, gBackMode;
Char gForm[formMax];
static Boolean gFormBad;

void formReset(void)
{
	MemSet(gForm, sizeof gForm, 0);
	gFormBad = false;
}

void formAdd(const Char *name, const Char *value)
{
	UInt16 n = StrLen(gForm);
	if (!httpFormAdd(gForm, sizeof gForm, name, value)) {
		MemSet(gForm + n, sizeof gForm - n, 0); /* no half value, no half escape */
		gFormBad = true;
	}
}

Boolean formOK(void)
{
	if (gFormBad) {
		StrCopy(gWhy, "A value is too long.");
	}
	return !gFormBad;
}

/* the form to go back to: Status, Settings, or the list a text came from */
static void remember(void)
{
	gBackForm = FrmGetActiveFormID();
	if (gBackForm != SetForm && gBackForm != ListForm) {
		gBackForm = StatusForm;
	}
	gBackMode = gMode;
}

static void goBack(void)
{
	UInt16 to = gBackForm;
	gBackForm = StatusForm;
	if (to == ListForm) {
		gMode = gBackMode;
	}
	FrmGotoForm(to);
}

void openList(UInt16 mode)
{
	remember();
	gMode = mode;
	FrmGotoForm(ListForm);
}

void openText(UInt16 mode)
{
	remember();
	gMode = mode;
	FrmGotoForm(TextForm);
}

Boolean confirm(const Char *a, const Char *b)
{
	return FrmCustomAlert(ConfirmAlert, a, b != NULL ? b : " ", " ") == 0;
}

void info(const Char *a, const Char *b)
{
	FrmCustomAlert(InfoAlert, a, b != NULL ? b : " ", " ");
}

/* listButtons names the buttons from gLabel and hides the unused ones. */
static void listButtons(FormType *f, UInt16 first)
{
	UInt16 i;
	for (i = 0; i < 3; i++) {
		if (gLabel[i][0] != 0) {
			CtlSetLabel(getObject(f, (UInt16)(first + i)), gLabel[i]);
			showObject(f, (UInt16)(first + i), true);
		} else {
			showObject(f, (UInt16)(first + i), false);
		}
	}
}

/* ---- the list ---- */

Int16 listSelection(void)
{
	FormType *f = FrmGetActiveForm();
	if (FrmGetActiveFormID() != ListForm || gRowCount == 0) {
		return -1;
	}
	return LstGetSelection(getObject(f, ListList));
}

void listReload(void)
{
	FormType *f = FrmGetActiveForm();
	ListType *lst = getObject(f, ListList);
	UInt16 i;

	gRowCount = 0;
	gInfo[0] = 0;
	StrCopy(gInfo, "Asking the gateway...");
	setField(f, ListInfo, gInfo, true);
	if (!modeLoad(gMode, true)) {
		StrNCopy(gInfo, gWhy[0] ? gWhy : "Not read.", sizeof gInfo - 1);
		gRowCount = 0;
	}
	if (apiGone()) {
		return; /* the session ended meanwhile (apiLeft): the form is going */
	}
	for (i = 0; i < gRowCount; i++) {
		gRowPtr[i] = gRows[i];
	}
	LstSetListChoices(lst, gRowPtr, (Int16)gRowCount);
	LstSetSelection(lst, gRowCount > 0 ? 0 : noListSelection);
	modeSetup(gMode, true, gTitle);
	listButtons(f, ListB1);
	setField(f, ListInfo, gInfo, false);
	FrmDrawForm(f);
}

Boolean listHandle(EventType *e)
{
	FormType *f = FrmGetActiveForm();
	UInt16 id;

	switch (e->eType) {
	case frmOpenEvent:
		gRowCount = 0;
		gInfo[0] = 0;
		modeSetup(gMode, true, gTitle);
		FrmSetTitle(f, gTitle);
		FrmSetMenu(f, modeMenu(gMode));
		LstSetListChoices(getObject(f, ListList), gRowPtr, 0);
		listButtons(f, ListB1);
		setField(f, ListInfo, gInfo, false);
		FrmDrawForm(f);
		listReload();
		return true;
	case ctlSelectEvent:
		id = e->data.ctlSelect.controlID;
		if (id == ListB4) {
			goBack();
			return true;
		}
		if (id >= ListB1 && id <= ListB3) {
			if (modeButton(gMode, true, (UInt16)(id - ListB1 + 1), listSelection()) && !apiGone()) {
				listReload();
			}
			return true;
		}
		break;
	case menuEvent:
		if (e->data.menu.itemID == MenuRefresh) {
			listReload();
			return true;
		}
		if (modeMenuItem(gMode, e->data.menu.itemID, listSelection()) && !apiGone()) {
			listReload();
		}
		return true;
	default:
		break;
	}
	return false;
}

/* ---- the text ---- */

void textAdd(const Char *s)
{
	UInt16 n = StrLen(gText), m = StrLen(s);
	if (n + m + 1 > textMax) {
		m = (UInt16)(textMax - 1 - n);
	}
	MemMove(gText + n, s, m);
	gText[n + m] = 0;
}

void textLine(const Char *label, const Char *value)
{
	if (value == NULL || value[0] == 0) {
		return;
	}
	if (label != NULL) {
		textAdd(label);
		textAdd(": ");
	}
	textAdd(value);
	textAdd("\n");
}

static void textScroll(FormType *f)
{
	FieldType *fld = getObject(f, TextText);
	UInt16 pos, text, vis;
	FldGetScrollValues(fld, &pos, &text, &vis);
	SclSetScrollBar(getObject(f, TextScroll), (Int16)pos, 0, (Int16)(text > vis ? text - vis : 0), (Int16)vis);
}

void textReload(void)
{
	FormType *f = FrmGetActiveForm();

	StrCopy(gText, "Asking the gateway...");
	setField(f, TextText, gText, true);
	gText[0] = 0;
	if (!modeLoad(gMode, false)) {
		gText[0] = 0;
		textLine("Not read", gWhy[0] ? gWhy : "the call failed");
	}
	if (apiGone()) {
		return;
	}
	setField(f, TextText, gText, true);
	textScroll(f);
}

Boolean textHandle(EventType *e)
{
	FormType *f = FrmGetActiveForm();
	FieldType *fld;
	UInt16 id;

	switch (e->eType) {
	case frmOpenEvent:
		gText[0] = 0;
		modeSetup(gMode, false, gTitle);
		FrmSetTitle(f, gTitle);
		listButtons(f, TextB1);
		setField(f, TextText, gText, false);
		FrmDrawForm(f);
		textReload();
		return true;
	case ctlSelectEvent:
		id = e->data.ctlSelect.controlID;
		if (id == TextDone) {
			goBack();
			return true;
		}
		if (id >= TextB1 && id <= TextB3) {
			if (modeButton(gMode, false, (UInt16)(id - TextB1 + 1), -1) && !apiGone()) {
				textReload();
			}
			return true;
		}
		break;
	case sclRepeatEvent:
		fld = getObject(f, TextText);
		FldScrollField(fld, (UInt16)(e->data.sclRepeat.newValue > e->data.sclRepeat.value ?
			e->data.sclRepeat.newValue - e->data.sclRepeat.value : e->data.sclRepeat.value - e->data.sclRepeat.newValue),
			e->data.sclRepeat.newValue > e->data.sclRepeat.value ? winDown : winUp);
		return false;
	case keyDownEvent:
		if (e->data.keyDown.chr == vchrPageUp || e->data.keyDown.chr == vchrPageDown) {
			fld = getObject(f, TextText);
			FldScrollField(fld, 10, e->data.keyDown.chr == vchrPageUp ? winUp : winDown);
			textScroll(f);
			return true;
		}
		break;
	default:
		break;
	}
	return false;
}

/* ---- the input dialog ---- */

#define inSecretMax 65
UInt16 gInSecret;
static Char gInTitle[32];
static Char gInCheck[2][32];
static Char gInSec[3][inSecretMax]; /* a secret field's text, as typed */
static UInt16 gInSecLen[3];
static Boolean gInShow;
static UInt16 gInFields[3] = { InF1, InF2, InF3 };

static void inputField(FormType *f, UInt16 lid, UInt16 fid, const Char *label, const Char *value)
{
	if (label == NULL) {
		showObject(f, lid, false);
		showObject(f, fid, false);
		return;
	}
	FrmCopyLabel(f, lid, label);
	setEditable(f, fid, value != NULL ? value : "");
}

static void inputCheck(FormType *f, UInt16 id, const Char *label, Char *store, Boolean value)
{
	if (label == NULL) {
		showObject(f, id, false);
		return;
	}
	StrNCopy(store, label, 31);
	store[31] = 0;
	CtlSetLabel(getObject(f, id), store);
	CtlSetValue(getObject(f, id), value);
}

static void inputRead(FormType *f, UInt16 id, Char *out, UInt16 cap)
{
	if (out != NULL) {
		getEditable(f, id, out, cap);
	}
}

/* inputStars shows secret field i as stars, or as text while Show is on. */
static void inputStars(FormType *f, UInt16 i)
{
	static Char stars[inSecretMax];
	UInt16 j;

	if (gInShow) {
		setEditable(f, gInFields[i], gInSec[i]);
	} else {
		for (j = 0; j < gInSecLen[i]; j++) {
			stars[j] = '*';
		}
		stars[gInSecLen[i]] = 0;
		setEditable(f, gInFields[i], stars);
	}
	FldDrawField(getObject(f, gInFields[i]));
}

/* inputWipe zeroes a field's text before the form frees it. */
static void inputWipe(FormType *f, UInt16 id)
{
	FieldType *fld = getObject(f, id);
	MemHandle h = FldGetTextHandle(fld);
	if (h != NULL) {
		MemSet(MemHandleLock(h), MemHandleSize(h), 0);
		MemHandleUnlock(h);
	}
}

/* inputHandle keeps secret fields out of sight, as Log in does: a star per
 * character, the characters in gInSec, no on-screen keyboard while hidden;
 * the second box is Show. */
static Boolean inputHandle(EventType *e)
{
	FormType *f = FrmGetActiveForm();
	FieldType *fld;
	UInt16 i, focus;
	WChar c;

	if (gInSecret == 0) {
		return false;
	}
	if (e->eType == ctlSelectEvent && e->data.ctlSelect.controlID == InC2) {
		for (i = 0; i < 3; i++) {
			if (gInSecret & (1 << i)) {
				if (gInShow) {
					getEditable(f, gInFields[i], gInSec[i], inSecretMax);
					gInSecLen[i] = StrLen(gInSec[i]);
				}
			}
		}
		gInShow = e->data.ctlSelect.on;
		for (i = 0; i < 3; i++) {
			if (gInSecret & (1 << i)) {
				inputStars(f, i);
			}
		}
		return true; /* a box, not a button: the dialog stays */
	}
	if (e->eType != keyDownEvent || gInShow) {
		return false;
	}
	focus = FrmGetFocus(f);
	for (i = 0; i < 3; i++) {
		if ((gInSecret & (1 << i)) && focus == FrmGetObjectIndex(f, gInFields[i])) {
			break;
		}
	}
	if (i == 3) {
		return false;
	}
	c = e->data.keyDown.chr;
	if (e->data.keyDown.modifiers & virtualKeyMask) {
		if (c == vchrKeyboard || c == vchrKeyboardAlpha || c == vchrKeyboardNumeric) {
			info("The keyboard would show the password: tick Show first, or use Graffiti.", NULL);
			return true;
		}
		return false;
	}
	fld = getObject(f, gInFields[i]);
	if (c == chrBackspace) {
		if (gInSecLen[i] > 0) {
			gInSecLen[i]--;
			gInSec[i][gInSecLen[i]] = 0;
			FldDelete(fld, gInSecLen[i], (UInt16)(gInSecLen[i] + 1));
		}
		return true;
	}
	if (c < 0x20 || c > 0xFF || c == 0x7F) {
		return c == chrLineFeed || c == chrCarriageReturn;
	}
	if (gInSecLen[i] + 1 < inSecretMax) {
		gInSec[i][gInSecLen[i]++] = (Char)c;
		gInSec[i][gInSecLen[i]] = 0;
		FldSetInsPtPosition(fld, FldGetTextLength(fld));
		FldInsert(fld, "*", 1);
	}
	return true;
}

Boolean inputDialog(const Char *title, const Char *l1, Char *v1, UInt16 c1, const Char *l2, Char *v2, UInt16 c2,
	const Char *l3, Char *v3, UInt16 c3, const Char *t1, Boolean *b1, const Char *t2, Boolean *b2)
{
	FormType *prev = FrmGetActiveForm(), *f = FrmInitForm(InForm);
	const Char *labels[3];
	Char *values[3];
	UInt16 caps[3], hit, i;

	labels[0] = l1; labels[1] = l2; labels[2] = l3;
	values[0] = v1; values[1] = v2; values[2] = v3;
	caps[0] = c1; caps[1] = c2; caps[2] = c3;
	FrmSetActiveForm(f);
	FrmSetEventHandler(f, inputHandle);
	StrNCopy(gInTitle, title, sizeof gInTitle - 1);
	gInTitle[sizeof gInTitle - 1] = 0;
	FrmSetTitle(f, gInTitle);
	gInShow = false;
	if (gInSecret != 0) {
		t2 = "Show";
		b2 = NULL;
	}
	inputField(f, InL1, InF1, l1, v1);
	inputField(f, InL2, InF2, l2, v2);
	inputField(f, InL3, InF3, l3, v3);
	for (i = 0; i < 3; i++) {
		gInSec[i][0] = 0;
		gInSecLen[i] = 0;
		if ((gInSecret & (1 << i)) && labels[i] != NULL) {
			if (values[i] != NULL) {
				StrNCopy(gInSec[i], values[i], inSecretMax - 1);
				gInSec[i][inSecretMax - 1] = 0;
				gInSecLen[i] = StrLen(gInSec[i]);
			}
			inputStars(f, i);
		}
	}
	inputCheck(f, InC1, t1, gInCheck[0], b1 != NULL && *b1);
	inputCheck(f, InC2, t2, gInCheck[1], b2 != NULL && *b2);
	if (l1 != NULL) {
		FrmSetFocus(f, FrmGetObjectIndex(f, InF1));
	}
	hit = FrmDoDialog(f);
	for (i = 0; i < 3; i++) {
		if (labels[i] == NULL || values[i] == NULL) {
			continue;
		}
		if (hit != InOK) {
			/* nothing read */
		} else if ((gInSecret & (1 << i)) && !gInShow) {
			StrNCopy(values[i], gInSec[i], caps[i] - 1);
			values[i][caps[i] - 1] = 0;
		} else {
			inputRead(f, gInFields[i], values[i], caps[i]);
		}
		inputWipe(f, gInFields[i]);
	}
	if (hit == InOK) {
		if (t1 != NULL && b1 != NULL) {
			*b1 = CtlGetValue(getObject(f, InC1)) != 0;
		}
		if (t2 != NULL && b2 != NULL) {
			*b2 = CtlGetValue(getObject(f, InC2)) != 0;
		}
	}
	MemSet(gInSec, sizeof gInSec, 0);
	gInSecret = 0;
	FrmDeleteForm(f);
	FrmSetActiveForm(prev);
	return hit == InOK;
}
