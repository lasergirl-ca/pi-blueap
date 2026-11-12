/* palm-gw's ?format=text replies (docs/WEB-UI.md section 6): key=value
 * lines ending in CRLF, a head record, then one record per item, each
 * record ending in a blank line. The text is read in place; nothing is
 * allocated. Tested by tests/test_kv.c. */
#ifndef PALMGW_KV_H
#define PALMGW_KV_H

#include "ptypes.h"

typedef struct {
	const Char *p;   /* the record's first byte */
	const Char *end; /* just past its last line */
} KvRecord;

/* kvNext finds the record at or after *pos in text[0..len) and moves *pos
 * past it; false when there are no more. */
Boolean kvNext(const Char *text, UInt16 len, UInt16 *pos, KvRecord *r);

/* kvGet copies the value of key in r into out (cap bytes with the NUL,
 * cut short if longer); false when the record has no such key. */
Boolean kvGet(const KvRecord *r, const Char *key, Char *out, UInt16 cap);

/* kvInt reads a value as a number (an optional minus, then digits); def
 * when the key is missing or not a number. */
Int32 kvInt(const KvRecord *r, const Char *key, Int32 def);

#endif
