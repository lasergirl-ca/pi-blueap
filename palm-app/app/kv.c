/* The text reply parser (kv.h). */
#include "kv.h"

/* lineEnd finds the end of the line at p (before CR LF or LF) and where
 * the next one starts. */
static const Char *lineEnd(const Char *p, const Char *end, const Char **next)
{
	const Char *e = p;
	while (e < end && *e != '\n') {
		e++;
	}
	*next = e < end ? e + 1 : end;
	if (e > p && e[-1] == '\r') {
		e--;
	}
	return e;
}

Boolean kvNext(const Char *text, UInt16 len, UInt16 *pos, KvRecord *r)
{
	const Char *end = text + len, *p = text + *pos, *next, *e;

	/* skip blank lines before the record */
	while (p < end) {
		e = lineEnd(p, end, &next);
		if (e != p) {
			break;
		}
		p = next;
	}
	if (p >= end) {
		*pos = len;
		return false;
	}
	r->p = p;
	while (p < end) {
		e = lineEnd(p, end, &next);
		if (e == p) {
			break;
		}
		p = next;
	}
	r->end = p;
	*pos = (UInt16)(p - text);
	return true;
}

Boolean kvGet(const KvRecord *r, const Char *key, Char *out, UInt16 cap)
{
	const Char *p = r->p, *next, *e, *k;
	UInt16 n;

	while (p < r->end) {
		e = lineEnd(p, r->end, &next);
		for (k = key; *k != 0 && p + (k - key) < e && p[k - key] == *k; k++) {
		}
		if (*k == 0 && p + (k - key) < e && p[k - key] == '=') {
			p += (k - key) + 1;
			for (n = 0; p < e && n + 1 < cap; n++, p++) {
				out[n] = *p;
			}
			out[n] = 0;
			return true;
		}
		p = next;
	}
	if (cap > 0) {
		out[0] = 0;
	}
	return false;
}

Int32 kvInt(const KvRecord *r, const Char *key, Int32 def)
{
	Char v[12];
	Int32 n = 0;
	UInt16 i = 0;
	Boolean neg = false;

	if (!kvGet(r, key, v, sizeof v)) {
		return def;
	}
	if (v[0] == '-') {
		neg = true;
		i = 1;
	}
	if (v[i] == 0) {
		return def;
	}
	for (; v[i] != 0; i++) {
		if (v[i] < '0' || v[i] > '9') {
			return def;
		}
		n = n * 10 + (v[i] - '0');
	}
	return neg ? -n : n;
}

UInt32 kvU32(const KvRecord *r, const Char *key, UInt32 def)
{
	Char v[24];
	UInt32 n = 0;
	UInt16 i;

	if (!kvGet(r, key, v, sizeof v) || v[0] == 0) {
		return def;
	}
	for (i = 0; v[i] != 0; i++) {
		if (v[i] < '0' || v[i] > '9') {
			return def;
		}
		if (n > (0xFFFFFFFFUL - (UInt32)(v[i] - '0')) / 10) {
			return 0xFFFFFFFFUL;
		}
		n = n * 10 + (UInt32)(v[i] - '0');
	}
	return n;
}
