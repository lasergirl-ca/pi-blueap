/* What the forms in the other code sections share with app.c (D-064).
 * Everything app.c offers is in the main code section; everything the
 * other files offer carries its section attribute here, on the prototype
 * (docs/PALM-DEV.md section 3). */
#ifndef PALMGW_UI_H
#define PALMGW_UI_H

#include <PalmOS.h>
#include "sections.h"
#include "gw.h"
#include "kv.h"

/* ---- from app.c (the main section) ---- */

enum { callOK = 0, callNoNet, callNoModule, callTLS, callTimeout, callBadReply, callPin };

extern GatewayType gCur;
extern Char gToken[80];
extern Char gWhy[120];
extern Char gReply[];
extern UInt16 gStatus, gBodyOff, gBodyLen;
extern UInt32 gHandshakeTicks, gHeapPeak;

UInt16 apiCall(const Char *method, const Char *path, const Char *body, Boolean auth);
void replyError(void);
void *getObject(FormType *f, UInt16 id);
void setField(FormType *f, UInt16 id, Char *text, Boolean draw);
void setEditable(FormType *f, UInt16 id, const Char *text);
void getEditable(FormType *f, UInt16 id, Char *out, UInt16 cap);
void fail(const Char *what);
void showObject(FormType *f, UInt16 id, Boolean show);

/* api runs a call that must answer 200; false with gWhy set when it does
 * not. The reply's records are then read with apiHead and apiItem. */
Boolean api(const Char *method, const Char *path, const Char *body);
Boolean apiHead(KvRecord *r);
Boolean apiItem(UInt16 *pos, KvRecord *r);
/* apiPath builds "/api/v1/<what>?format=text" into a buffer that stays
 * until the next call. */
Char *apiPath(const Char *what);
/* apiLeft forgets the token: the session ended (restart, network apply).
 * It runs once: later calls, and api() itself, do nothing until the next
 * login (apiGone). FrmGotoForm only queues the change, so code that goes on
 * after a call tests apiGone, not the active form. */
void apiLeft(const Char *why);
#define apiGone() (gToken[0] == 0)
/* gKeep401: the next api() call's 401 is an answer (a wrong current
 * password), not the end of the session. api() clears it. */
extern Boolean gKeep401;

/* ---- Pair and Settings (pair.c) ---- */
Boolean pairHandle(EventType *e) PAIR_SEC;
void pairTick(void) PAIR_SEC;
Boolean pairTicking(void) PAIR_SEC;
Boolean setHandle(EventType *e) PAIR_SEC;

/* ---- the generic forms (listui.c) ---- */
#define rowMax  24
#define rowLen  64
#define keyLen  40
#define textMax 4000

enum { lmDevices = 1, lmRules, lmWifi, lmKeys };
enum { tmSessions = 11, tmCounters, tmNAT, tmLogs, tmDiag, tmAbout };

extern UInt16 gMode;
extern UInt16 gRowCount;
extern Char gRows[rowMax][rowLen];
extern Char gKeys[rowMax][keyLen];
extern Char gInfo[200];
extern Char gText[textMax];
extern Char gLabel[4][12];

/* gForm is the one request body the forms build (formReset, formAdd); a
 * value that does not fit is left out whole and formOK refuses the call.
 * formReset also wipes it, so a password goes with the next reset. */
#define formMax 512
extern Char gForm[formMax];
void formReset(void) LIST_SEC;
void formAdd(const Char *name, const Char *value) LIST_SEC;
Boolean formOK(void) LIST_SEC;

/* openList and openText remember the form (and list mode) they came
 * from, and Done goes back there. */
void openList(UInt16 mode) LIST_SEC;
void openText(UInt16 mode) LIST_SEC;
Boolean listHandle(EventType *e) LIST_SEC;
Boolean textHandle(EventType *e) LIST_SEC;
void listReload(void) LIST_SEC;
void textReload(void) LIST_SEC;
Int16 listSelection(void) LIST_SEC;
void textAdd(const Char *s) LIST_SEC;
void textLine(const Char *label, const Char *value) LIST_SEC;
/* inputDialog asks for up to three values and two ticks; a NULL label
 * hides that field or box. True for OK. Fields named in gInSecret (bit 0
 * the first) show a star per character, with the second box as Show;
 * inputDialog clears gInSecret. Labels fit in 32 characters. */
extern UInt16 gInSecret;
Boolean inputDialog(const Char *title, const Char *l1, Char *v1, UInt16 c1, const Char *l2, Char *v2, UInt16 c2,
	const Char *l3, Char *v3, UInt16 c3, const Char *t1, Boolean *b1, const Char *t2, Boolean *b2) LIST_SEC;
Boolean confirm(const Char *a, const Char *b) LIST_SEC;
void info(const Char *a, const Char *b) LIST_SEC;

/* ---- what each list and text form shows and does (modes.c) ---- */
/* modeLoad fills gRows/gKeys/gInfo (lists) or gText (text forms); false
 * when the call failed (gWhy says why). modeSetup names the buttons
 * (gLabel, "" hides one) and the title, and modeMenu is the list's menu. */
void modeSetup(UInt16 mode, Boolean list, Char *title) MODE_SEC;
UInt16 modeMenu(UInt16 mode) MODE_SEC;
Boolean modeLoad(UInt16 mode, Boolean list) MODE_SEC;
/* modeButton handles button 1-3 of a list (sel is the row, or -1) or a
 * text form; modeMenuItem a list's menu item. True when the form should
 * load again. */
Boolean modeButton(UInt16 mode, Boolean list, UInt16 button, Int16 sel) MODE_SEC;
Boolean modeMenuItem(UInt16 mode, UInt16 item, Int16 sel) MODE_SEC;

#endif
