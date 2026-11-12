/* Packing and showing a saved gateway (gw.h). */
#include "gw.h"

static void put16(UInt8 *p, UInt16 v)
{
	p[0] = (UInt8)(v >> 8);
	p[1] = (UInt8)v;
}

static void put32(UInt8 *p, UInt32 v)
{
	p[0] = (UInt8)(v >> 24);
	p[1] = (UInt8)(v >> 16);
	p[2] = (UInt8)(v >> 8);
	p[3] = (UInt8)v;
}

static UInt16 get16(const UInt8 *p) { return (UInt16)((UInt16)p[0] << 8 | p[1]); }

static UInt32 get32(const UInt8 *p)
{
	return (UInt32)p[0] << 24 | (UInt32)p[1] << 16 | (UInt32)p[2] << 8 | p[3];
}

/* putStr copies s and its NUL, at most max bytes with the NUL. */
static UInt16 putStr(UInt8 *out, const Char *s, UInt16 max)
{
	UInt16 n = 0;
	while (s[n] != 0 && n + 1 < max) {
		out[n] = (UInt8)s[n];
		n++;
	}
	out[n] = 0;
	return (UInt16)(n + 1);
}

/* getStr reads a NUL-terminated string at rec[*off], or fails when there
 * is none before end or it does not fit max. */
static Boolean getStr(const UInt8 *rec, UInt16 len, UInt16 *off, Char *out, UInt16 max)
{
	UInt16 n = 0;
	while (*off + n < len && rec[*off + n] != 0) {
		if (n + 1 >= max) {
			return false;
		}
		out[n] = (Char)rec[*off + n];
		n++;
	}
	if (*off + n >= len) {
		return false;
	}
	out[n] = 0;
	*off = (UInt16)(*off + n + 1);
	return true;
}

UInt16 gwPack(const GatewayType *g, UInt8 *out)
{
	UInt16 i, n = gwFixed;

	out[0] = gwVersion;
	for (i = 0; i < 32; i++) {
		out[1 + i] = g->pin[i];
		out[33 + i] = g->cert[i];
	}
	put16(out + 65, g->port);
	put32(out + 67, g->lastUsed);
	n = (UInt16)(n + putStr(out + n, g->name, gwNameMax));
	n = (UInt16)(n + putStr(out + n, g->addr, gwAddrMax));
	n = (UInt16)(n + putStr(out + n, g->user, gwUserMax));
	return n;
}

Boolean gwUnpack(const UInt8 *rec, UInt16 len, GatewayType *g)
{
	UInt16 i, off = gwFixed;

	if (len < gwFixed + 3 || rec[0] != gwVersion) {
		return false;
	}
	for (i = 0; i < 32; i++) {
		g->pin[i] = rec[1 + i];
		g->cert[i] = rec[33 + i];
	}
	g->port = get16(rec + 65);
	g->lastUsed = get32(rec + 67);
	return getStr(rec, len, &off, g->name, gwNameMax) &&
		getStr(rec, len, &off, g->addr, gwAddrMax) &&
		getStr(rec, len, &off, g->user, gwUserMax);
}

void gwFingerprint(const UInt8 *hash, Boolean lines, Char *out)
{
	static const Char hex[] = "0123456789ABCDEF";
	UInt16 i, n = 0;

	for (i = 0; i < 32; i++) {
		out[n++] = hex[hash[i] >> 4];
		out[n++] = hex[hash[i] & 15];
		if (i == 31) {
			break;
		}
		out[n++] = (Char)(lines && i % 8 == 7 ? '\n' : ':');
	}
	out[n] = 0;
}
