/* A saved gateway: what the app keeps about one palm-gw it has trusted
 * (docs/PALM-APP.md, the Gateways database). Packed with explicit
 * offsets, never as a struct, and checked by tests/test_gw.c. */
#ifndef PALMGW_GW_H
#define PALMGW_GW_H

#include "ptypes.h"

#define gwVersion   1
#define gwNameMax   48 /* the gateway's Bluetooth name, from status */
#define gwAddrMax   40 /* a dotted address or a host name */
#define gwUserMax   33 /* palm-gw usernames are 1 to 32 characters */
#define gwKeyMax    96 /* a device key, "pgk1." + 12 + "." + 64 (D-063) */
#define gwFixed     71 /* the fixed part, before the strings */
#define gwRecordMax (gwFixed + gwNameMax + gwAddrMax + gwUserMax + gwKeyMax)

typedef struct {
	UInt8 pin[32];  /* SHA-256 of the server key's SubjectPublicKeyInfo */
	UInt8 cert[32]; /* SHA-256 of its certificate, as the web login page shows it */
	UInt16 port;
	UInt32 lastUsed; /* seconds, the Palm's clock */
	Char name[gwNameMax];
	Char addr[gwAddrMax];
	Char user[gwUserMax];
	Char key[gwKeyMax]; /* this Palm's device key, or empty; a trailer after user (D-063) */
} GatewayType;

/* gwPack writes g into out (at least gwRecordMax bytes) and returns the
 * record's length. gwUnpack reads one back; false for a record it does not
 * understand (another version, or cut short). */
UInt16 gwPack(const GatewayType *g, UInt8 *out);
Boolean gwUnpack(const UInt8 *rec, UInt16 len, GatewayType *g);

/* gwFingerprint writes the certificate hash as the web UI shows it: 32
 * colon-separated hex pairs, upper case, in 4 lines of 8 pairs when lines
 * is true (for a 160-pixel form), else one line; out holds 97 bytes. */
void gwFingerprint(const UInt8 *hash, Boolean lines, Char *out);

#endif
