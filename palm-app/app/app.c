/* palm-gw for Palm OS 5: the gateway's admin app (docs/PALM-APP.md,
 * D-062). The forms, the Net Library and HTTP are 68k; TLS 1.3 is the
 * native ARM module in ../tls, loaded by ../loader and driven one step at
 * a time, the bytes moved here between steps (from CradleServer's
 * CradleSync).
 *
 * Milestone 1: the Gateways list, first-use trust, Log in, Status and Log
 * out. Each API call is its own connection (Connection: close): TCP, the
 * TLS handshake, one request, the reply until the server closes. */
#include <PalmOS.h>
#include <NetMgr.h>
#include "resources.h"
#include "loader.c"
#include "../tls/tlsmod.h"
#include "../tls/tls_client.h"
#include "gw.h"
#include "kv.h"
#include "http.h"

#ifndef APP_BUILD
#define APP_BUILD "dev"
#endif

#define appCreator    'PlGw'
#define gwDBName      "palm-gw Gateways"
#define gwDBType      'GWay'
#define gwMax         16
#define tlsModuleID   2000 /* armh, armc and armd resources from here */
#define tlsFeature    2    /* the feature pointer that holds the module's code */
#define tlsStackSize  32768UL
#define tlsHeapSize   (56UL * 1024)
#define replyMax      8192
#define defaultAddr   "10.42.0.1"
#define defaultPort   8042
#define netTimeout    (SysTicksPerSecond() / 4) /* a quarter second per receive, then check the clock */
#define callSeconds   45 /* a whole call: connect, handshake, request, reply */

/* ---- the TLS module (CradleSync's driving code, adapted) ---- */

typedef struct {
	ModuleType m;
	Boolean loaded;
	UInt8 *buf;
	UInt8 *stack;
	UInt8 *heap;
	UInt16 inLen;
	Int8 result;
	Int32 wolfErr;
} TlsType;

static TlsType gTls;
static UInt16 gNetRef;
static Boolean gNetOpen;
static NetSocketRef gSock = -1;

/* What the last call learnt and how long it took. */
static UInt8 gPeerSpki[32], gPeerCert[32];
static UInt32 gHandshakeTicks;
static UInt32 gHeapPeak;

static UInt16 get16(const UInt8 *p) { return (UInt16)((UInt16)p[0] << 8 | p[1]); }
static UInt32 get32(const UInt8 *p) { return (UInt32)p[0] << 24 | (UInt32)p[1] << 16 | (UInt32)p[2] << 8 | p[3]; }
static void put16(UInt8 *p, UInt16 v) { p[0] = (UInt8)(v >> 8); p[1] = (UInt8)v; }
static void put32(UInt8 *p, UInt32 v) { p[0] = (UInt8)(v >> 24); p[1] = (UInt8)(v >> 16); p[2] = (UInt8)(v >> 8); p[3] = (UInt8)v; }

static void tlsUnload(void)
{
	if (gTls.heap != NULL) {
		MemSet(gTls.heap, tlsHeapSize, 0);
		MemPtrFree(gTls.heap);
	}
	if (gTls.stack != NULL) {
		MemPtrFree(gTls.stack);
	}
	if (gTls.buf != NULL) {
		MemSet(gTls.buf, TM_BUF_SIZE, 0);
		MemPtrFree(gTls.buf);
	}
	if (gTls.loaded) {
		ModuleUnload(&gTls.m);
	}
	MemSet(&gTls, sizeof gTls, 0);
}

static Err tlsLoad(void)
{
	Err err;
	if (gTls.loaded) {
		return errNone;
	}
	if ((err = ModuleLoad(&gTls.m, appCreator, tlsFeature, tlsModuleID)) != errNone) {
		return err;
	}
	gTls.loaded = true;
	gTls.buf = MemPtrNew(TM_BUF_SIZE);
	gTls.stack = MemPtrNew(tlsStackSize);
	gTls.heap = MemPtrNew(tlsHeapSize);
	if (gTls.buf == NULL || gTls.stack == NULL || gTls.heap == NULL) {
		tlsUnload();
		return memErrNotEnoughSpace;
	}
	ModuleSetStack(&gTls.m, gTls.stack, tlsStackSize);
	return errNone;
}

/* tlsStep runs one step of the client; -99 when the module did not run. */
static Int8 tlsStep(UInt8 op)
{
	UInt8 *b = gTls.buf;
	b[TM_OP] = op;
	put16(b + TM_INLEN, gTls.inLen);
	if (ModuleCall(&gTls.m, b) != TM_MAGIC || b[TM_STATUS] != TM_OK) {
		gTls.result = -99;
		return -99;
	}
	gTls.result = (Int8)b[TM_RESULT];
	gTls.wolfErr = (Int32)get32(b + TM_ERROR);
	return gTls.result;
}

/* entropy gathers what the Palm has for the module's generator: the
 * jitter of the tick counter against a busy loop, the clocks and the
 * system's own generator (CradleSync's localEntropy, with more samples).
 * The module hashes it into its state. */
static void entropy(UInt8 *out, UInt16 n)
{
	UInt16 i, j;
	UInt32 t, spins, v;

	MemSet(out, n, 0);
	for (j = 0; j < 4; j++) {
		for (i = 0; i + 4 <= n; i += 4) {
			t = TimGetTicks();
			spins = 0;
			while (TimGetTicks() == t) {
				spins++;
			}
			v = spins ^ (TimGetSeconds() << 16) ^ (t << 8) ^ ((UInt32)SysRandom(0) << 20) ^ SysRandom(0);
			out[i] ^= (UInt8)(v >> 24);
			out[i + 1] ^= (UInt8)(v >> 16);
			out[i + 2] ^= (UInt8)(v >> 8);
			out[i + 3] ^= (UInt8)v;
		}
	}
}

/* ---- the network ---- */

static void netClose(void)
{
	Err err;
	if (gSock >= 0) {
		NetLibSocketClose(gNetRef, gSock, SysTicksPerSecond(), &err);
		gSock = -1;
	}
}

static Err netUp(void)
{
	Err err;
	UInt16 ifErrs = 0;

	if (gNetOpen) {
		return errNone;
	}
	if ((err = SysLibFind("Net.lib", &gNetRef)) != errNone) {
		return err;
	}
	err = NetLibOpen(gNetRef, &ifErrs);
	if (err == netErrAlreadyOpen) {
		err = errNone;
	}
	if (err != errNone) {
		return err;
	}
	if (ifErrs != 0) {
		NetLibClose(gNetRef, false);
		return ifErrs;
	}
	gNetOpen = true;
	return errNone;
}

static void netDown(void)
{
	netClose();
	if (gNetOpen) {
		NetLibClose(gNetRef, false);
		gNetOpen = false;
	}
}

/* netConnect opens a TCP connection to host (a dotted address or a name). */
static Err netConnect(const Char *host, UInt16 port)
{
	NetSocketAddrINType addr;
	Err err;

	if ((err = netUp()) != errNone) {
		return err;
	}
	gSock = NetLibSocketOpen(gNetRef, netSocketAddrINET, netSocketTypeStream, netSocketProtoIPTCP,
		SysTicksPerSecond() * 10, &err);
	if (gSock < 0) {
		return err;
	}
	MemSet(&addr, sizeof addr, 0);
	addr.family = netSocketAddrINET;
	addr.port = NetHToNS(port);
	addr.addr = NetLibAddrAToIN(gNetRef, host);
	if (addr.addr == (NetIPAddr)-1) {
		static NetHostInfoBufType info; /* kept for the app's life: the library may answer late */
		NetHostInfoPtr h;
		MemSet(&info, sizeof info, 0);
		h = NetLibGetHostByName(gNetRef, host, &info, SysTicksPerSecond() * 20, &err);
		if (h == NULL || h->addrListP == NULL || h->addrListP[0] == NULL) {
			netClose();
			return netErrDNSUnreachable;
		}
		MemMove(&addr.addr, h->addrListP[0], sizeof addr.addr);
	}
	if (NetLibSocketConnect(gNetRef, gSock, (NetSocketAddrType *)&addr, sizeof addr,
			SysTicksPerSecond() * 20, &err) < 0) {
		netClose();
		return err;
	}
	return errNone;
}

/* tlsFlush sends what the client wrote and drops the input it took. */
static Err tlsFlush(void)
{
	UInt8 *b = gTls.buf;
	UInt16 used = get16(b + TM_INUSED), out = get16(b + TM_OUTLEN), sent = 0;
	Int16 n;
	Err err = errNone;

	while (sent < out) {
		n = NetLibSend(gNetRef, gSock, b + TM_OUT + sent, (UInt16)(out - sent), 0, NULL, 0, SysTicksPerSecond() * 10, &err);
		if (n <= 0) {
			return err ? err : netErrSocketClosedByRemote;
		}
		sent = (UInt16)(sent + n);
		EvtResetAutoOffTimer(); /* a long call must not be put to sleep (CradleSync's alive) */
	}
	if (used > gTls.inLen) {
		used = gTls.inLen;
	}
	MemMove(b + TM_IN, b + TM_IN + used, gTls.inLen - used);
	gTls.inLen = (UInt16)(gTls.inLen - used);
	put16(b + TM_OUTLEN, 0);
	put16(b + TM_INUSED, 0);
	return errNone;
}

/* tlsFill reads more from the socket: 1 when bytes came, 0 when none yet,
 * -1 when the connection closed or failed. */
static Int16 tlsFill(void)
{
	Int16 n;
	Err err = errNone;

	if (gTls.inLen >= TM_IOMAX) {
		return 1;
	}
	n = NetLibReceive(gNetRef, gSock, gTls.buf + TM_IN + gTls.inLen, (UInt16)(TM_IOMAX - gTls.inLen), 0, NULL, NULL,
		netTimeout, &err);
	if (n > 0) {
		gTls.inLen = (UInt16)(gTls.inLen + n);
		EvtResetAutoOffTimer();
		return 1;
	}
	if (n == 0 || (err != errNone && err != netErrTimeout)) {
		return -1;
	}
	return 0;
}

/* ---- calls to the gateway ---- */

/* The outcome of a call, for the forms to explain. */
enum {
	callOK = 0,
	callNoNet,      /* the network did not come up, or no connection */
	callNoModule,   /* the TLS module would not load */
	callTLS,        /* the TLS handshake failed */
	callTimeout,
	callBadReply,
	callPin         /* connected, but the key is not the expected one */
};

static Char gReply[replyMax];
static UInt16 gReplyLen, gStatus, gBodyOff, gBodyLen;
static Err gNetErr;

/* readAll reads the reply until the server closes, or the deadline. */
static UInt16 readAll(UInt32 deadline)
{
	UInt8 *b = gTls.buf;
	UInt16 got;
	Int8 r;
	Int16 f;

	gReplyLen = 0;
	for (;;) {
		put16(b + TM_APPLEN, TM_APPMAX);
		r = tlsStep(TM_OP_READ);
		if (r == -99) {
			return callTLS;
		}
		got = get16(b + TM_APPLEN);
		if (got > 0) {
			if (gReplyLen + got > replyMax - 1) {
				got = (UInt16)(replyMax - 1 - gReplyLen);
			}
			MemMove(gReply + gReplyLen, b + TM_APP, got);
			gReplyLen = (UInt16)(gReplyLen + got);
		}
		if (tlsFlush() != errNone) {
			break;
		}
		if (r == TLS_ERR_CLOSED) {
			break;
		}
		if (r != TLS_MORE && r != TLS_DONE) {
			return callTLS;
		}
		if (got > 0) {
			continue;
		}
		if (TimGetTicks() > deadline) {
			return callTimeout;
		}
		f = tlsFill();
		if (f < 0) {
			break; /* the server closed the socket: Connection: close */
		}
	}
	gReply[gReplyLen] = 0;
	if (!httpReply(gReply, gReplyLen, &gStatus, &gBodyOff, &gBodyLen)) {
		return callBadReply;
	}
	return callOK;
}

/* writeAll sends the request through the client. */
static UInt16 writeAll(const Char *req, UInt16 len, UInt32 deadline)
{
	UInt8 *b = gTls.buf;
	UInt16 off = 0, n;
	Int8 r;

	while (off < len) {
		n = (UInt16)(len - off > TM_APPMAX ? TM_APPMAX : len - off);
		MemMove(b + TM_APP, req + off, n);
		for (;;) {
			put16(b + TM_APPLEN, n);
			r = tlsStep(TM_OP_WRITE);
			if (tlsFlush() != errNone) {
				return callNoNet;
			}
			if (r == TLS_DONE) {
				break;
			}
			if (r != TLS_MORE || TimGetTicks() > deadline || tlsFill() < 0) {
				return r == TLS_MORE ? callTimeout : callTLS;
			}
		}
		off = (UInt16)(off + n);
	}
	return callOK;
}

/* handshake connects to host:port and runs TLS with no pins: the module
 * takes any key and reports its SPKI hash and certificate hash, which the
 * caller checks before anything is sent (docs/PALM-APP.md). */
static UInt16 handshake(const Char *host, UInt16 port, UInt32 deadline)
{
	UInt8 *b;
	UInt32 t0;
	UInt16 out;
	Int8 r;
	Int16 f;

	if ((gNetErr = netConnect(host, port)) != errNone) {
		return callNoNet;
	}
	if (tlsLoad() != errNone) {
		netClose();
		return callNoModule;
	}
	b = gTls.buf;
	MemSet(b, TM_IN, 0);
	gTls.inLen = 0;
	put32(b + TM_HEAP, (UInt32)gTls.heap);
	put32(b + TM_HEAPLEN, tlsHeapSize);
	b[TM_NPINS] = 0;
	StrNCopy((Char *)b + TM_SNI, host, 63);
	entropy(b + TM_SEED, 32);
	if (tlsStep(TM_OP_START) != TLS_DONE) {
		netClose();
		return callTLS;
	}
	t0 = TimGetTicks();
	for (;;) {
		r = tlsStep(TM_OP_HANDSHAKE);
		if (r != TLS_MORE && r != TLS_DONE) {
			netClose();
			return callTLS;
		}
		out = get16(b + TM_OUTLEN); /* tlsFlush zeroes it */
		if (tlsFlush() != errNone) {
			netClose();
			return callNoNet;
		}
		if (r == TLS_DONE) {
			break;
		}
		if (out != 0) {
			continue; /* it may have more to say before it needs input */
		}
		while ((f = tlsFill()) == 0) {
			if (TimGetTicks() > deadline) {
				netClose();
				return callTimeout;
			}
		}
		if (f < 0) {
			netClose();
			return callTLS;
		}
	}
	gHandshakeTicks = TimGetTicks() - t0;
	gHeapPeak = get32(b + TM_PEAK);
	MemMove(gPeerSpki, b + TM_PEERSPKI, 32);
	MemMove(gPeerCert, b + TM_PEERCERT, 32);
	return callOK;
}

/* closeCall ends a call: a close_notify if the socket is still there,
 * and no plaintext (the login form, the bearer token) left in the buffer. */
static void closeCall(void)
{
	if (gTls.loaded) {
		if (gSock >= 0) {
			tlsStep(TM_OP_CLOSE);
			tlsFlush();
		}
		MemSet(gTls.buf + TM_APP, TM_APPMAX, 0);
	}
	netClose();
}

/* ---- the saved gateways ---- */

static GatewayType gGws[gwMax];
static UInt16 gGwCount;
static Char gGwLabels[gwMax][gwNameMax + gwAddrMax + 4];
static Char *gGwChoices[gwMax];

static DmOpenRef gwOpen(Boolean create)
{
	LocalID id = DmFindDatabase(0, gwDBName);
	if (id == 0 && create) {
		if (DmCreateDatabase(0, gwDBName, appCreator, gwDBType, false) != errNone) {
			return NULL;
		}
		id = DmFindDatabase(0, gwDBName);
	}
	return id == 0 ? NULL : DmOpenDatabase(0, id, dmModeReadWrite);
}

/* gwLoad reads every saved gateway, most recently used first. */
static void gwLoad(void)
{
	DmOpenRef db = gwOpen(false);
	UInt16 i, j, n;
	MemHandle h;
	GatewayType t;

	gGwCount = 0;
	if (db == NULL) {
		return;
	}
	n = DmNumRecords(db);
	for (i = 0; i < n && gGwCount < gwMax; i++) {
		h = DmQueryRecord(db, i);
		if (h == NULL) {
			continue;
		}
		if (gwUnpack(MemHandleLock(h), (UInt16)MemHandleSize(h), &gGws[gGwCount])) {
			gGwCount++;
		}
		MemHandleUnlock(h);
	}
	DmCloseDatabase(db);
	for (i = 1; i < gGwCount; i++) {
		for (j = i; j > 0 && gGws[j].lastUsed > gGws[j - 1].lastUsed; j--) {
			t = gGws[j];
			gGws[j] = gGws[j - 1];
			gGws[j - 1] = t;
		}
	}
	for (i = 0; i < gGwCount; i++) {
		StrPrintF(gGwLabels[i], "%s (%s)", gGws[i].name[0] ? gGws[i].name : "palm-gw", gGws[i].addr);
		gGwChoices[i] = gGwLabels[i];
	}
}

/* gwLabel is a gateway's name, or "palm-gw" before status has named it. */
static Char *gwLabel(GatewayType *g)
{
	static Char unnamed[] = "palm-gw";
	return g->name[0] ? g->name : unnamed;
}

/* gwFind is the index of the saved gateway with this key, or -1. */
static Int16 gwFind(const UInt8 *spki)
{
	UInt16 i;
	for (i = 0; i < gGwCount; i++) {
		if (MemCmp(gGws[i].pin, spki, 32) == 0) {
			return (Int16)i;
		}
	}
	return -1;
}

/* gwSave writes g, replacing the saved gateway with the same key. */
static Boolean gwSave(const GatewayType *g)
{
	DmOpenRef db = gwOpen(true);
	UInt8 rec[gwRecordMax];
	UInt16 len, i, n, at = dmMaxRecordIndex;
	MemHandle h;
	GatewayType old;
	Boolean ok = false;

	if (db == NULL) {
		return false;
	}
	len = gwPack(g, rec);
	n = DmNumRecords(db);
	for (i = 0; i < n; i++) {
		h = DmQueryRecord(db, i);
		if (h != NULL && gwUnpack(MemHandleLock(h), (UInt16)MemHandleSize(h), &old)) {
			if (MemCmp(old.pin, g->pin, 32) == 0) {
				at = i;
			}
		}
		if (h != NULL) {
			MemHandleUnlock(h);
		}
		if (at != dmMaxRecordIndex) {
			break;
		}
	}
	if (at != dmMaxRecordIndex) {
		h = DmResizeRecord(db, at, len);
		if (h != NULL) {
			h = DmGetRecord(db, at);
		}
	} else {
		h = DmNewRecord(db, &at, len);
	}
	if (h != NULL) {
		ok = DmWrite(MemHandleLock(h), 0, rec, len) == errNone;
		MemHandleUnlock(h);
		DmReleaseRecord(db, at, true);
	}
	DmCloseDatabase(db);
	return ok;
}

static void gwForget(const GatewayType *g)
{
	DmOpenRef db = gwOpen(false);
	UInt16 i;
	MemHandle h;
	GatewayType old;
	Boolean hit;

	if (db == NULL) {
		return;
	}
	for (i = DmNumRecords(db); i > 0; i--) {
		h = DmQueryRecord(db, (UInt16)(i - 1));
		hit = false;
		if (h != NULL) {
			hit = gwUnpack(MemHandleLock(h), (UInt16)MemHandleSize(h), &old) && MemCmp(old.pin, g->pin, 32) == 0;
			MemHandleUnlock(h);
		}
		if (hit) {
			DmRemoveRecord(db, (UInt16)(i - 1));
		}
	}
	DmCloseDatabase(db);
}

/* ---- the session ---- */

static GatewayType gCur;       /* the gateway being logged in to, or logged in */
static Boolean gCurKnown;      /* gCur is a saved, trusted gateway */
static Char gToken[80];        /* the API token; in memory only */
static Char gPass[65];         /* the password as typed; wiped after the login */
static UInt16 gPassLen;
static Char gWhy[120];         /* the last call's failure, in words */

static void describe(UInt16 r)
{
	switch (r) {
	case callNoNet:
		StrPrintF(gWhy, "No connection to the gateway (error 0x%x). Is the Palm connected over Bluetooth or Wi-Fi?", gNetErr);
		break;
	case callNoModule:
		StrCopy(gWhy, "The TLS module would not load: too little memory?");
		break;
	case callTLS:
		StrPrintF(gWhy, "The secure connection failed (TLS %d, wolfSSL %ld).", gTls.result, gTls.wolfErr);
		break;
	case callTimeout:
		StrCopy(gWhy, "The gateway did not answer in time.");
		break;
	case callBadReply:
		StrCopy(gWhy, "The gateway's reply was not understood.");
		break;
	case callPin:
		StrCopy(gWhy, "This is not the gateway that was trusted.");
		break;
	default:
		gWhy[0] = 0;
	}
}

/* apiCall runs one request against gCur, checking its key first. For a
 * known gateway the key must be its pin; for a new one (pin all zero) the
 * key is taken as is, and the caller decides about trust. The reply's
 * body is at gReply + gBodyOff. */
static UInt16 apiCall(const Char *method, const Char *path, const Char *body, Boolean auth)
{
	static Char req[768];
	static const UInt8 zero[32];
	UInt32 deadline = TimGetTicks() + SysTicksPerSecond() * callSeconds;
	UInt16 r, len;

	r = handshake(gCur.addr, gCur.port, deadline);
	if (r != callOK) {
		describe(r);
		return r;
	}
	if (MemCmp(gCur.pin, zero, 32) != 0 && MemCmp(gCur.pin, gPeerSpki, 32) != 0) {
		closeCall();
		describe(callPin);
		return callPin;
	}
	len = httpRequest(req, sizeof req, method, gCur.addr, path, auth ? gToken : NULL, body);
	r = len == 0 ? callBadReply : writeAll(req, len, deadline);
	MemSet(req, sizeof req, 0);
	if (r == callOK) {
		r = readAll(deadline);
	}
	closeCall();
	describe(r);
	return r;
}

/* replyError puts the gateway's error message, or the HTTP status, in gWhy. */
static void replyError(void)
{
	KvRecord rec;
	UInt16 pos = 0;
	Char msg[100];

	if (kvNext(gReply + gBodyOff, gBodyLen, &pos, &rec) && kvGet(&rec, "message", msg, sizeof msg)) {
		StrCopy(gWhy, msg);
	} else {
		StrPrintF(gWhy, "The gateway answered %u.", gStatus);
	}
}

/* ---- forms ---- */

static void *getObject(FormType *f, UInt16 id)
{
	return FrmGetObjectPtr(f, FrmGetObjectIndex(f, id));
}

/* setField shows text in a field from a buffer that stays alive (the
 * field keeps the pointer: docs/PALM-DEV.md section 7). */
static void setField(FormType *f, UInt16 id, Char *text, Boolean draw)
{
	FieldType *fld = getObject(f, id);
	FldSetTextPtr(fld, text);
	FldRecalculateField(fld, true);
	if (draw) {
		FldDrawField(fld);
	}
}

/* setEditable puts a copy of text in an editable field (its own handle). */
static void setEditable(FormType *f, UInt16 id, const Char *text)
{
	FieldType *fld = getObject(f, id);
	MemHandle h = MemHandleNew(StrLen(text) + 1), old = FldGetTextHandle(fld);
	if (h == NULL) {
		return;
	}
	StrCopy(MemHandleLock(h), text);
	MemHandleUnlock(h);
	FldSetTextHandle(fld, h);
	if (old != NULL) {
		MemHandleFree(old);
	}
}

static void getEditable(FormType *f, UInt16 id, Char *out, UInt16 cap)
{
	Char *p = FldGetTextPtr(getObject(f, id));
	StrNCopy(out, p != NULL ? p : "", (Int16)(cap - 1));
	out[cap - 1] = 0;
}

static void fail(const Char *what)
{
	FrmCustomAlert(ErrorAlert, what, gWhy[0] ? "\n" : " ", gWhy[0] ? gWhy : " ");
}

static void about(void)
{
	static Char last[64];
	if (gHandshakeTicks != 0) {
		StrPrintF(last, "Last TLS handshake %lu ms, heap %lu bytes.",
			gHandshakeTicks * 1000UL / SysTicksPerSecond(), gHeapPeak);
	} else {
		StrCopy(last, "No connection yet.");
	}
	FrmCustomAlert(AboutAlert, APP_BUILD, last, " ");
}

/* -- Gateways -- */

static Int16 mainSelection(void)
{
	FormType *f = FrmGetActiveForm();
	if (gGwCount == 0) {
		return -1;
	}
	return LstGetSelection(getObject(f, MainList));
}

static void mainFill(FormType *f, Boolean draw)
{
	static Char hint[] = "No gateways yet.\n\nConnect this Palm to a palm-gw over Bluetooth (or join its network over Wi-Fi), then tap New.";
	ListType *lst = getObject(f, MainList);

	gwLoad();
	LstSetListChoices(lst, gGwChoices, (Int16)gGwCount);
	if (gGwCount == 0) {
		FrmHideObject(f, FrmGetObjectIndex(f, MainList));
		FrmHideObject(f, FrmGetObjectIndex(f, MainConnect));
		FrmHideObject(f, FrmGetObjectIndex(f, MainDetails));
		setField(f, MainHint, hint, false);
		FrmShowObject(f, FrmGetObjectIndex(f, MainHint));
	} else {
		FrmHideObject(f, FrmGetObjectIndex(f, MainHint));
		FrmShowObject(f, FrmGetObjectIndex(f, MainList));
		FrmShowObject(f, FrmGetObjectIndex(f, MainConnect));
		FrmShowObject(f, FrmGetObjectIndex(f, MainDetails));
		LstSetSelection(lst, 0);
	}
	if (draw) {
		FrmDrawForm(f);
	}
}

static void mainDetails(void)
{
	Int16 i = mainSelection();
	static Char fp[97], who[gwNameMax + gwUserMax + 8];
	if (i < 0) {
		return;
	}
	gwFingerprint(gGws[i].cert, false, fp);
	StrPrintF(who, "%s, user %s", gGws[i].name, gGws[i].user);
	FrmCustomAlert(DetailsAlert, who, gGws[i].addr, fp);
}

static void startLogin(Int16 i)
{
	if (i >= 0) {
		gCur = gGws[i];
		gCurKnown = true;
	} else {
		MemSet(&gCur, sizeof gCur, 0);
		StrCopy(gCur.addr, defaultAddr);
		gCur.port = defaultPort;
		gCurKnown = false;
	}
	FrmGotoForm(LoginForm);
}

static Boolean mainHandle(EventType *e)
{
	FormType *f = FrmGetActiveForm();
	Int16 i;

	switch (e->eType) {
	case frmOpenEvent:
		mainFill(f, true);
		return true;
	case ctlSelectEvent:
		switch (e->data.ctlSelect.controlID) {
		case MainConnect:
			startLogin(mainSelection());
			return true;
		case MainNew:
			startLogin(-1);
			return true;
		case MainDetails:
			mainDetails();
			return true;
		}
		break;
	case menuEvent:
		switch (e->data.menu.itemID) {
		case MainMenuDetails:
			mainDetails();
			return true;
		case MainMenuForget:
			i = mainSelection();
			if (i >= 0 && FrmCustomAlert(ForgetAlert, gwLabel(&gGws[i]), " ", " ") == 0) {
				gwForget(&gGws[i]);
				mainFill(f, true);
			}
			return true;
		case MainMenuAbout:
			about();
			return true;
		}
		break;
	default:
		break;
	}
	return false;
}

/* -- Trust -- */

/* askTrust shows the certificate fingerprint and asks; true to trust. */
static Boolean askTrust(Boolean changed)
{
	static Char text[400], fp[97];
	FormType *prev = FrmGetActiveForm(), *f = FrmInitForm(TrustForm);
	UInt16 hit;

	gwFingerprint(gPeerCert, true, fp);
	/* The fingerprint first, so a long name or address never pushes it out
	 * of the field. */
	if (changed) {
		StrPrintF(text, "Fingerprint:\n%s\n\nWARNING: %s has a new certificate. Trust it only if you changed it.",
			fp, gwLabel(&gCur));
	} else {
		StrPrintF(text, "Fingerprint:\n%s\n\nA new palm-gw at %s. Compare with its web login page, then trust it.",
			fp, gCur.addr);
	}
	FrmSetActiveForm(f);
	setField(f, TrustText, text, false);
	hit = FrmDoDialog(f);
	FrmDeleteForm(f);
	FrmSetActiveForm(prev);
	return hit == TrustYes;
}

/* -- Log in -- */

static Char gProgress[200];
static Char gWhich[gwNameMax];

static void progress(const Char *text)
{
	FormType *f = FrmGetActiveForm();
	StrNCopy(gProgress, text, sizeof gProgress - 1);
	if (FrmGetActiveFormID() == LoginForm) {
		setField(f, LoginProgress, gProgress, true);
	}
}

/* passKey keeps the password out of sight: the field shows a star per
 * character, and the characters go to gPass. */
static Boolean passKey(EventType *e)
{
	FormType *f = FrmGetActiveForm();
	FieldType *fld = getObject(f, LoginPass);
	WChar c = e->data.keyDown.chr;

	if (FrmGetFocus(f) != FrmGetObjectIndex(f, LoginPass) || (e->data.keyDown.modifiers & virtualKeyMask)) {
		return false;
	}
	if (c == chrBackspace) {
		if (gPassLen > 0) {
			gPassLen--;
			gPass[gPassLen] = 0;
			FldDelete(fld, gPassLen, (UInt16)(gPassLen + 1));
		}
		return true;
	}
	if (c < 0x20 || c > 0xFF || c == 0x7F) {
		return c != chrLineFeed && c != chrCarriageReturn ? false : true;
	}
	if (gPassLen + 1 < sizeof gPass) {
		gPass[gPassLen++] = (Char)c;
		gPass[gPassLen] = 0;
		FldSetInsPtPosition(fld, FldGetTextLength(fld));
		FldInsert(fld, "*", 1);
	}
	return true;
}

static void wipePass(void)
{
	MemSet(gPass, sizeof gPass, 0);
	gPassLen = 0;
}

/* passKeyboard keeps the on-screen keyboard away from the password: the
 * system would open its dialog before passKey sees anything, show the
 * password in plain text, and write it into the field around gPass. True
 * when it took the event. */
static Boolean passKeyboard(EventType *e)
{
	FormType *f;
	WChar c;

	if (e->eType != keyDownEvent || !(e->data.keyDown.modifiers & virtualKeyMask) ||
		FrmGetActiveFormID() != LoginForm) {
		return false;
	}
	c = e->data.keyDown.chr;
	if (c != vchrKeyboard && c != vchrKeyboardAlpha && c != vchrKeyboardNumeric) {
		return false;
	}
	f = FrmGetActiveForm();
	if (FrmGetFocus(f) != FrmGetObjectIndex(f, LoginPass)) {
		return false;
	}
	progress("The keyboard would show the password: use Graffiti for it.");
	return true;
}

/* doLogin: the handshake (which says which gateway this is), trust if it
 * is new, the login, then status, which names the gateway for the list. */
static void doLogin(void)
{
	FormType *f = FrmGetActiveForm();
	static Char form[200];
	Char user[gwUserMax], addr[gwAddrMax];
	KvRecord rec;
	UInt16 r, pos = 0;
	Int16 idx;
	static const UInt8 zero[32];
	static GatewayType replaced; /* a saved gateway whose changed key was trusted */
	Boolean replacing = false;

	getEditable(f, LoginAddr, addr, sizeof addr);
	getEditable(f, LoginUser, user, sizeof user);
	if (addr[0] == 0 || user[0] == 0 || gPassLen == 0) {
		gWhy[0] = 0;
		fail("Fill in the address, user and password.");
		return;
	}
	if (FldGetTextLength(getObject(f, LoginPass)) != gPassLen) {
		/* something edited the field around passKey (a paste, the keyboard
		 * dialog): what it holds is not the password */
		wipePass();
		setEditable(f, LoginPass, "");
		FldDrawField(getObject(f, LoginPass));
		gWhy[0] = 0;
		fail("Type the password again, with Graffiti.");
		return;
	}
	if (!gCurKnown || StrCompare(addr, gCur.addr) != 0) {
		StrCopy(gCur.addr, addr);
	}

	/* The handshake alone, to see who answers. */
	progress("Connecting and checking the gateway's certificate...");
	r = handshake(gCur.addr, gCur.port, TimGetTicks() + SysTicksPerSecond() * callSeconds);
	closeCall();
	if (r != callOK) {
		describe(r);
		progress("");
		fail("Could not reach the gateway.");
		return;
	}
	idx = gwFind(gPeerSpki);
	if (gCurKnown && MemCmp(gCur.pin, gPeerSpki, 32) != 0 && idx < 0) {
		if (!askTrust(true)) {
			progress("Not trusted; nothing was sent.");
			return;
		}
		replaced = gCur;
		replacing = true;
	} else if (idx >= 0) {
		/* a saved gateway, perhaps reached at another address */
		Char keep[gwAddrMax];
		StrCopy(keep, gCur.addr);
		gCur = gGws[idx];
		StrCopy(gCur.addr, keep);
		gCurKnown = true;
		StrCopy(gWhich, gwLabel(&gCur));
		setField(f, LoginWhich, gWhich, true);
	} else if (!askTrust(false)) {
		progress("Not trusted; nothing was sent.");
		return;
	}
	MemMove(gCur.pin, gPeerSpki, 32);
	MemMove(gCur.cert, gPeerCert, 32);
	StrCopy(gCur.user, user);

	progress("Logging in...");
	form[0] = 0;
	httpFormAdd(form, sizeof form, "username", user);
	httpFormAdd(form, sizeof form, "password", gPass);
	r = apiCall("POST", "/api/v1/login?format=text", form, false);
	MemSet(form, sizeof form, 0);
	if (r != callOK) {
		progress("");
		fail("Login failed.");
		return;
	}
	if (gStatus != 200 || !kvNext(gReply + gBodyOff, gBodyLen, &pos, &rec) ||
		!kvGet(&rec, "token", gToken, sizeof gToken)) {
		replyError();
		progress("");
		fail("Login refused.");
		return;
	}
	wipePass();
	MemSet(gReply, sizeof gReply, 0); /* the token was in it */
	gCur.lastUsed = TimGetSeconds();
	if (MemCmp(gCur.pin, zero, 32) != 0) {
		if (replacing) {
			gwForget(&replaced); /* the old key is no longer trusted */
		}
		gwSave(&gCur);
	}
	FrmGotoForm(StatusForm);
}

static Boolean loginHandle(EventType *e)
{
	FormType *f = FrmGetActiveForm();

	switch (e->eType) {
	case frmOpenEvent:
		wipePass();
		gProgress[0] = 0;
		StrCopy(gWhich, gCurKnown ? gwLabel(&gCur) : "new");
		setField(f, LoginWhich, gWhich, false);
		setEditable(f, LoginAddr, gCur.addr);
		setEditable(f, LoginUser, gCurKnown ? gCur.user : "");
		setField(f, LoginProgress, gProgress, false);
		FrmDrawForm(f);
		FrmSetFocus(f, FrmGetObjectIndex(f, gCurKnown ? LoginPass : LoginUser));
		return true;
	case frmCloseEvent:
		wipePass();
		break;
	case keyDownEvent:
		return passKey(e);
	case ctlSelectEvent:
		switch (e->data.ctlSelect.controlID) {
		case LoginGo:
			doLogin();
			return true;
		case LoginCancel:
			wipePass();
			FrmGotoForm(MainForm);
			return true;
		}
		break;
	default:
		break;
	}
	return false;
}

/* -- Status -- */

static Char gStatusText[1024];

static void addLine(const Char *label, const Char *value)
{
	UInt16 n = StrLen(gStatusText);
	if (n + StrLen(label) + StrLen(value) + 3 < sizeof gStatusText) {
		StrPrintF(gStatusText + n, "%s: %s\n", label, value);
	}
}

static void secs(Int32 s, Char *out)
{
	if (s >= 86400L) {
		StrPrintF(out, "%ldd %ldh", s / 86400L, s % 86400L / 3600);
	} else if (s >= 3600) {
		StrPrintF(out, "%ldh %ldm", s / 3600, s % 3600 / 60);
	} else {
		StrPrintF(out, "%ldm %lds", s / 60, s % 60);
	}
}

static void statusScroll(FormType *f)
{
	FieldType *fld = getObject(f, StatusText);
	UInt16 pos, text, vis;
	FldGetScrollValues(fld, &pos, &text, &vis);
	SclSetScrollBar(getObject(f, StatusScroll), (Int16)pos, 0, (Int16)(text > vis ? text - vis : 0), (Int16)vis);
}

/* statusLoad asks for status and lays it out; false when it failed. */
static Boolean statusLoad(FormType *f, Boolean draw)
{
	KvRecord rec;
	UInt16 r, pos = 0;
	Char v[64], w[24];

	gStatusText[0] = 0;
	r = apiCall("GET", "/api/v1/status?format=text", NULL, true);
	if (r != callOK || gStatus != 200 || !kvNext(gReply + gBodyOff, gBodyLen, &pos, &rec)) {
		if (r == callOK) {
			replyError();
		}
		StrPrintF(gStatusText, "Status not read.\n%s", gWhy);
	} else {
		if (kvGet(&rec, "gateway", v, sizeof v)) {
			addLine("Gateway", v);
			if (StrCompare(v, gCur.name) != 0) {
				StrNCopy(gCur.name, v, gwNameMax - 1);
				gwSave(&gCur);
			}
		}
		if (kvGet(&rec, "version", v, sizeof v)) {
			addLine("Version", v);
		}
		secs(kvInt(&rec, "uptime", 0), w);
		addLine("Up", w);
		if (kvGet(&rec, "adapter", v, sizeof v - 6)) { /* room for ", off" */
			StrPrintF(v + StrLen(v), "%s", kvInt(&rec, "powered", 0) ? ", on" : ", off");
			addLine("Adapter", v);
		}
		StrPrintF(v, "%ld PPP, %ld PAN", kvInt(&rec, "sessions", 0), kvInt(&rec, "pan_clients", 0));
		addLine("Connected", v);
		StrPrintF(v, "%ld in use", kvInt(&rec, "pool_in_use", 0));
		addLine("Address pool", v);
		if (kvGet(&rec, "uplink", v, sizeof v)) {
			addLine("Internet via", v);
		}
		if (kvInt(&rec, "pairing", 0)) {
			kvGet(&rec, "pin", w, sizeof w);
			StrPrintF(v, "open, %ld s left%s%s", kvInt(&rec, "pairing_expires_in", 0), w[0] ? ", PIN " : "", w);
		} else {
			StrCopy(v, "closed");
		}
		addLine("Pairing", v);
		StrPrintF(v, "%lu ms TLS handshake", gHandshakeTicks * 1000UL / SysTicksPerSecond());
		addLine("Last call", v);
	}
	FrmSetTitle(f, gCur.name[0] ? gCur.name : "Status");
	setField(f, StatusText, gStatusText, draw);
	statusScroll(f);
	return r == callOK && gStatus == 200;
}

static void logout(void)
{
	if (gToken[0] != 0) {
		apiCall("POST", "/api/v1/logout?format=text", "", true);
	}
	MemSet(gToken, sizeof gToken, 0);
	FrmGotoForm(MainForm);
}

static Boolean statusHandle(EventType *e)
{
	FormType *f = FrmGetActiveForm();
	FieldType *fld;

	switch (e->eType) {
	case frmOpenEvent:
		FrmDrawForm(f);
		statusLoad(f, true);
		return true;
	case ctlSelectEvent:
		switch (e->data.ctlSelect.controlID) {
		case StatusRefresh:
			statusLoad(f, true);
			return true;
		case StatusLogout:
			logout();
			return true;
		}
		break;
	case sclRepeatEvent:
		fld = getObject(f, StatusText);
		FldScrollField(fld, (UInt16)(e->data.sclRepeat.newValue > e->data.sclRepeat.value ?
			e->data.sclRepeat.newValue - e->data.sclRepeat.value : e->data.sclRepeat.value - e->data.sclRepeat.newValue),
			e->data.sclRepeat.newValue > e->data.sclRepeat.value ? winDown : winUp);
		return false;
	case menuEvent:
		switch (e->data.menu.itemID) {
		case StatusMenuLogout:
			logout();
			return true;
		case StatusMenuAbout:
			about();
			return true;
		}
		break;
	default:
		break;
	}
	return false;
}

/* ---- the application ---- */

static Boolean appHandle(EventType *e)
{
	FormType *f;
	if (e->eType != frmLoadEvent) {
		return false;
	}
	f = FrmInitForm(e->data.frmLoad.formID);
	FrmSetActiveForm(f);
	switch (e->data.frmLoad.formID) {
	case MainForm:
		FrmSetEventHandler(f, mainHandle);
		break;
	case LoginForm:
		FrmSetEventHandler(f, loginHandle);
		break;
	case StatusForm:
		FrmSetEventHandler(f, statusHandle);
		break;
	}
	return true;
}

static void appStop(void)
{
	FrmCloseAllForms();
	MemSet(gToken, sizeof gToken, 0);
	wipePass();
	netDown();
	tlsUnload();
}

UInt32 PilotMain(UInt16 cmd, MemPtr params, UInt16 flags)
{
	EventType e;
	Err err;
	UInt32 rom;

	(void)params;
	(void)flags;
	if (cmd != sysAppLaunchCmdNormalLaunch) {
		return 0;
	}
	FtrGet(sysFtrCreator, sysFtrNumROMVersion, &rom);
	if (rom < sysMakeROMVersion(5, 0, 0, sysROMStageRelease, 0)) {
		FrmCustomAlert(ErrorAlert, "palm-gw needs Palm OS 5 (a T|X, LifeDrive, Tungsten T3, T5 or E2).", " ", " ");
		return 0;
	}
	FrmGotoForm(MainForm);
	do {
		EvtGetEvent(&e, evtWaitForever);
		if (passKeyboard(&e)) {
			continue;
		}
		if (SysHandleEvent(&e)) {
			continue;
		}
		if (MenuHandleEvent(NULL, &e, &err)) {
			continue;
		}
		if (appHandle(&e)) {
			continue;
		}
		FrmDispatchEvent(&e);
	} while (e.eType != appStopEvent);
	appStop();
	return 0;
}
