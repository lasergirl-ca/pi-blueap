/* The few pieces of HTTP/1.1 the app needs, without the network: building
 * a request and reading a reply that ends when the server closes
 * (Connection: close). Tested by tests/test_http.c. */
#ifndef PALMGW_HTTP_H
#define PALMGW_HTTP_H

#include "ptypes.h"

/* httpRequest writes a request into out (cap bytes) and returns its
 * length, or 0 when it does not fit. method is "GET" or "POST"; path
 * already has its query (?format=text); token is the bearer token or
 * NULL; body is a form (application/x-www-form-urlencoded) or NULL. */
UInt16 httpRequest(Char *out, UInt16 cap, const Char *method, const Char *host, const Char *path,
	const Char *token, const Char *body);

/* httpFormAdd appends name=value, URL-encoded, to the form in out (cap
 * bytes with the NUL); false when it does not fit. */
Boolean httpFormAdd(Char *out, UInt16 cap, const Char *name, const Char *value);

/* httpReply reads a whole reply: its status code, and where its body
 * starts and how long it is. False when it is not an HTTP/1.x reply with
 * a complete header. */
Boolean httpReply(const Char *reply, UInt16 len, UInt16 *status, UInt16 *bodyOff, UInt16 *bodyLen);

#endif
