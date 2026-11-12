#include "check.h"
#include "../gw.h"

int main(void)
{
	GatewayType g, h;
	UInt8 rec[gwRecordMax];
	Char fp[97];
	UInt16 n, i;

	memset(&g, 0, sizeof g);
	for (i = 0; i < 32; i++) {
		g.pin[i] = (UInt8)i;
		g.cert[i] = (UInt8)(0xF0 - i);
	}
	g.port = 8042;
	g.lastUsed = 0xD1C2B3A4UL;
	strcpy(g.name, "Palm Gateway - Lyman");
	strcpy(g.addr, "10.42.0.1");
	strcpy(g.user, "sysadmin");
	n = gwPack(&g, rec);
	check("packed length", n == gwFixed + 21 + 10 + 9);
	check("version byte", rec[0] == gwVersion);
	check("pin at 1", rec[1] == 0 && rec[32] == 31);
	check("cert at 33", rec[33] == 0xF0 && rec[64] == 0xF0 - 31);
	check("port big-endian at 65", rec[65] == 0x1F && rec[66] == 0x6A);
	check("last used big-endian at 67", rec[67] == 0xD1 && rec[70] == 0xA4);
	check("name at 71", memcmp(rec + 71, "Palm Gateway - Lyman\0", 21) == 0);
	memset(&h, 0xAA, sizeof h);
	check("unpack", gwUnpack(rec, n, &h));
	check("round trip", memcmp(h.pin, g.pin, 32) == 0 && memcmp(h.cert, g.cert, 32) == 0 && h.port == 8042 &&
		h.lastUsed == g.lastUsed && strcmp(h.name, g.name) == 0 && strcmp(h.addr, g.addr) == 0 &&
		strcmp(h.user, g.user) == 0);
	check("cut short", !gwUnpack(rec, (UInt16)(n - 1), &h));
	rec[0] = 2;
	check("other version", !gwUnpack(rec, n, &h));

	/* A name longer than the field is cut, and still unpacks. */
	memset(g.name, 'x', sizeof g.name);
	g.name[sizeof g.name - 1] = 0;
	n = gwPack(&g, rec);
	check("long name unpacks", gwUnpack(rec, n, &h) && strlen(h.name) == gwNameMax - 1);

	/* The fingerprint as the web login page shows it. */
	for (i = 0; i < 32; i++) {
		g.cert[i] = (UInt8)(i == 0 ? 0x26 : i == 1 ? 0xAA : i);
	}
	gwFingerprint(g.cert, false, fp);
	check("fingerprint one line", strncmp(fp, "26:AA:02:03:04", 14) == 0 && strlen(fp) == 95 && strcmp(fp + 93, "1F") == 0);
	gwFingerprint(g.cert, true, fp);
	check("fingerprint in lines", strncmp(fp, "26:AA:02:03:04:05:06:07\n08:", 27) == 0 && strlen(fp) == 95);
	return failures != 0;
}
