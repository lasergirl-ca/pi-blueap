/* HTTP requests and replies (http.h). */
#include "http.h"

static UInt16 slen(const Char *s)
{
	UInt16 n = 0;
	while (s[n] != 0) {
		n++;
	}
	return n;
}

/* app appends s to out[*n], keeping room for the NUL; false when full. */
static Boolean app(Char *out, UInt16 cap, UInt16 *n, const Char *s)
{
	while (*s != 0) {
		if (*n + 1 >= cap) {
			return false;
		}
		out[(*n)++] = *s++;
	}
	out[*n] = 0;
	return true;
}

static Boolean appNum(Char *out, UInt16 cap, UInt16 *n, UInt16 v)
{
	Char d[6];
	UInt16 i = sizeof d - 1;

	d[i] = 0;
	do {
		d[--i] = (Char)('0' + v % 10);
		v = (UInt16)(v / 10);
	} while (v != 0);
	return app(out, cap, n, d + i);
}

UInt16 httpRequest(Char *out, UInt16 cap, const Char *method, const Char *host, const Char *path,
	const Char *token, const Char *body)
{
	UInt16 n = 0;
	Boolean ok = app(out, cap, &n, method) && app(out, cap, &n, " ") && app(out, cap, &n, path) &&
		app(out, cap, &n, " HTTP/1.1\r\nHost: ") && app(out, cap, &n, host) &&
		app(out, cap, &n, "\r\nUser-Agent: palm-gw-palm/1\r\nConnection: close\r\n");

	if (ok && token != NULL) {
		ok = app(out, cap, &n, "Authorization: Bearer ") && app(out, cap, &n, token) && app(out, cap, &n, "\r\n");
	}
	if (ok && body != NULL) {
		ok = app(out, cap, &n, "Content-Type: application/x-www-form-urlencoded\r\nContent-Length: ") &&
			appNum(out, cap, &n, slen(body)) && app(out, cap, &n, "\r\n");
	}
	ok = ok && app(out, cap, &n, "\r\n");
	if (ok && body != NULL) {
		ok = app(out, cap, &n, body);
	}
	return ok ? n : 0;
}

Boolean httpFormAdd(Char *out, UInt16 cap, const Char *name, const Char *value)
{
	static const Char hex[] = "0123456789ABCDEF";
	UInt16 n = slen(out);
	const UInt8 *v = (const UInt8 *)value;
	Char esc[4];

	if (n > 0 && !app(out, cap, &n, "&")) {
		return false;
	}
	if (!app(out, cap, &n, name) || !app(out, cap, &n, "=")) {
		return false;
	}
	for (; *v != 0; v++) {
		if ((*v >= 'a' && *v <= 'z') || (*v >= 'A' && *v <= 'Z') || (*v >= '0' && *v <= '9') ||
			*v == '-' || *v == '.' || *v == '_' || *v == '~') {
			esc[0] = (Char)*v;
			esc[1] = 0;
		} else {
			esc[0] = '%';
			esc[1] = hex[*v >> 4];
			esc[2] = hex[*v & 15];
			esc[3] = 0;
		}
		if (!app(out, cap, &n, esc)) {
			return false;
		}
	}
	return true;
}

Boolean httpReply(const Char *reply, UInt16 len, UInt16 *status, UInt16 *bodyOff, UInt16 *bodyLen)
{
	UInt16 i, code = 0;

	if (len < 12 || reply[0] != 'H' || reply[1] != 'T' || reply[2] != 'T' || reply[3] != 'P' ||
		reply[4] != '/' || reply[5] != '1' || reply[6] != '.' || reply[8] != ' ') {
		return false;
	}
	for (i = 9; i < 12; i++) {
		if (reply[i] < '0' || reply[i] > '9') {
			return false;
		}
		code = (UInt16)(code * 10 + (reply[i] - '0'));
	}
	for (i = 12; i + 3 < len; i++) {
		if (reply[i] == '\r' && reply[i + 1] == '\n' && reply[i + 2] == '\r' && reply[i + 3] == '\n') {
			*status = code;
			*bodyOff = (UInt16)(i + 4);
			*bodyLen = (UInt16)(len - i - 4);
			return true;
		}
	}
	return false;
}
