#include "check.h"
#include "../kv.h"

static const char reply[] =
	"status=ok\r\ncount=2\r\n\r\n"
	"name=Zo\xeb's Palm TX\r\naddr=00:07:E0:3C:5E:FA\r\nconnected=1\r\n\r\n"
	"name=LifeDrive\r\naddr=00:07:E0:90:A0:C6\r\nconnected=0\r\nduration=-12\r\nrx=3000000000\r\nbig=99999999999\r\n\r\n";

int main(void)
{
	KvRecord r;
	UInt16 pos = 0, len = (UInt16)strlen(reply);
	Char v[32], small[5];

	check("head", kvNext(reply, len, &pos, &r));
	check("status", kvGet(&r, "status", v, sizeof v) && strcmp(v, "ok") == 0);
	check("count", kvInt(&r, "count", -1) == 2);
	check("no such key", !kvGet(&r, "name", v, sizeof v) && v[0] == 0);
	check("prefix is not a key", !kvGet(&r, "stat", v, sizeof v));
	check("item 1", kvNext(reply, len, &pos, &r));
	check("Windows-1252 kept", kvGet(&r, "name", v, sizeof v) && strcmp(v, "Zo\xeb's Palm TX") == 0);
	check("bool", kvInt(&r, "connected", -1) == 1);
	check("cut to cap", kvGet(&r, "name", small, sizeof small) && strcmp(small, "Zo\xeb'") == 0);
	check("item 2", kvNext(reply, len, &pos, &r));
	check("negative", kvInt(&r, "duration", 0) == -12);
	check("not a number", kvInt(&r, "addr", 7) == 7);
	check("u32 past 2^31", kvU32(&r, "rx", 0) == 3000000000UL);
	check("u32 saturates", kvU32(&r, "big", 0) == 0xFFFFFFFFUL);
	check("u32 not a number", kvU32(&r, "duration", 5) == 5);
	check("u32 missing", kvU32(&r, "tx", 9) == 9);
	check("end", !kvNext(reply, len, &pos, &r));

	/* LF line endings and no trailing blank line still parse. */
	pos = 0;
	check("LF only", kvNext("error=login_required\nmessage=log in first", 41, &pos, &r) &&
		kvGet(&r, "message", v, sizeof v) && strcmp(v, "log in first") == 0);
	return failures != 0;
}
