/* The 68k loader for a native ARM module (see loader.h and README.md).
 *
 * The code goes into the storage heap through a feature pointer, the one
 * way to get a contiguous block larger than a resource, and is written
 * there with DmWrite, with the code's own relocations, before it first
 * runs; nothing writes to it after that. The data block, the module's
 * read-only data, writable data, global offset table and zeroed data, goes
 * into the dynamic heap. Each word the header lists gets the code's
 * address, the data block's, or the code's less the data block's added.
 * The ARM side is little-endian, so those words are read and written a
 * byte at a time. */
#include <PalmOS.h>
#include <PceNativeCall.h>
#include "loader.h"

#define codeType   'armc'
#define dataType   'armd'
#define headerType 'armh'
#define headerSize 32
/* A relocation: the offset of a little-endian word, which block it is in,
 * and what to add to it (palmlink). */
#define relocInCode   0x20000000UL
#define relocKindMask 0xC0000000UL
#define relocCode     0x00000000UL /* add the code's address */
#define relocData     0x40000000UL /* add the data block's */
#define relocCodeData 0x80000000UL /* add the code's less the data block's */
#define relocOffMask  (relocInCode - 1)

static UInt32 be32(const UInt8 *p)
{
	return (UInt32)p[0] << 24 | (UInt32)p[1] << 16 | (UInt32)p[2] << 8 | p[3];
}

static UInt16 be16(const UInt8 *p)
{
	return (UInt16)((UInt16)p[0] << 8 | p[1]);
}

static void putLE32(UInt8 *p, UInt32 v)
{
	p[0] = (UInt8)v;
	p[1] = (UInt8)(v >> 8);
	p[2] = (UInt8)(v >> 16);
	p[3] = (UInt8)(v >> 24);
}

static UInt32 getLE32(const UInt8 *p)
{
	return (UInt32)p[3] << 24 | (UInt32)p[2] << 16 | (UInt32)p[1] << 8 | p[0];
}

/* copyPieces copies resources type firstID.. in order to dst, through
 * DmWrite for the storage heap or MemMove for the dynamic heap, and
 * checks they add up to size. */
static Err copyPieces(DmResType type, DmResID firstID, UInt16 count, void *dst, UInt32 size, Boolean storage)
{
	UInt32 offset = 0, n;
	UInt16 i;
	MemHandle h;
	Err err = errNone;

	for (i = 0; i < count && err == errNone; i++) {
		h = DmGetResource(type, firstID + i);
		if (h == NULL) {
			return moduleErrMissing;
		}
		n = MemHandleSize(h);
		if (offset + n > size) {
			err = moduleErrFormat;
		} else if (storage) {
			err = DmWrite(dst, offset, MemHandleLock(h), n);
			MemHandleUnlock(h);
		} else {
			MemMove((UInt8 *)dst + offset, MemHandleLock(h), (Int32)n);
			MemHandleUnlock(h);
		}
		offset += n;
		DmReleaseResource(h);
	}
	if (err == errNone && offset != size) {
		err = moduleErrFormat;
	}
	return err;
}

void ModuleUnload(ModuleType *m)
{
	if (m->code != NULL) {
		FtrPtrFree(m->creator, m->feature);
		m->code = NULL;
	}
	if (m->dataChunk != NULL) {
		MemPtrFree(m->dataChunk);
		m->dataChunk = NULL;
	}
}

Err ModuleLoad(ModuleType *m, UInt32 creator, UInt16 feature, DmResID firstID)
{
	MemHandle hh;
	const UInt8 *h;
	UInt32 codeSize, dataSize, bssSize, gotOffset, relocs, i, r, w, off, total;
	UInt8 *word, le[4];
	UInt16 codePieces, dataPieces;
	Err err;

	MemSet(m, sizeof *m, 0);
	m->creator = creator;
	m->feature = feature;
	hh = DmGetResource(headerType, firstID);
	if (hh == NULL) {
		return moduleErrMissing;
	}
	h = MemHandleLock(hh);
	err = moduleErrFormat;
	if (MemHandleSize(hh) < headerSize || h[0] != 'T' || h[1] != 'S' || h[2] != 'M' || h[3] != 'L' ||
		be16(h + 4) != 2) {
		goto out;
	}
	codePieces = be16(h + 6);
	codeSize = be32(h + 8);
	dataPieces = be16(h + 12);
	dataSize = be32(h + 16);
	bssSize = be32(h + 20);
	gotOffset = be32(h + 24);
	relocs = be32(h + 28);
	if (MemHandleSize(hh) != headerSize + 4 * relocs || codeSize == 0 || gotOffset > dataSize + bssSize) {
		goto out;
	}
	total = dataSize + bssSize + 8;
	if (total > 65000UL) {
		err = moduleErrTooLarge;
		goto out;
	}

	/* A feature pointer left by an earlier run that did not unload. */
	FtrPtrFree(creator, feature);
	err = FtrPtrNew(creator, feature, codeSize, &m->code);
	if (err != errNone) {
		m->code = NULL;
		goto out;
	}
	if ((err = copyPieces(codeType, firstID, codePieces, m->code, codeSize, true)) != errNone) {
		goto out;
	}

	m->dataChunk = MemPtrNew(total);
	if (m->dataChunk == NULL) {
		err = memErrNotEnoughSpace;
		goto out;
	}
	m->data = (UInt8 *)(((UInt32)m->dataChunk + 7) & ~7UL);
	if ((err = copyPieces(dataType, firstID, dataPieces, m->data, dataSize, false)) != errNone) {
		goto out;
	}
	MemSet(m->data + dataSize, (Int32)bssSize, 0);
	for (i = 0; i < relocs; i++) {
		r = be32(h + headerSize + 4 * i);
		off = r & relocOffMask;
		if (off + 4 > ((r & relocInCode) ? codeSize : dataSize)) {
			err = moduleErrFormat;
			goto out;
		}
		word = ((r & relocInCode) ? (UInt8 *)m->code : m->data) + off;
		w = getLE32(word);
		switch (r & relocKindMask) {
		case relocCode:
			w += (UInt32)m->code;
			break;
		case relocData:
			w += (UInt32)m->data;
			break;
		case relocCodeData:
			w += (UInt32)m->code - (UInt32)m->data;
			break;
		default:
			err = moduleErrFormat;
			goto out;
		}
		if (r & relocInCode) {
			/* The code is in the storage heap: DmWrite, before it ever runs. */
			putLE32(le, w);
			if ((err = DmWrite(m->code, off, le, 4)) != errNone) {
				goto out;
			}
		} else {
			putLE32(word, w);
		}
	}
	m->got = (UInt32)m->data + gotOffset;
	err = errNone;
out:
	MemHandleUnlock(hh);
	DmReleaseResource(hh);
	if (err != errNone) {
		ModuleUnload(m);
	}
	return err;
}

void ModuleSetStack(ModuleType *m, void *stack, UInt32 size)
{
	/* ARM wants the stack 8-byte aligned at a call. */
	m->stackTop = stack == NULL ? 0 : ((UInt32)stack + size) & ~7UL;
}

UInt32 ModuleCall(const ModuleType *m, void *userData)
{
	/* The parameter block entry.S reads: the GOT's address, userData's
	 * and the stack's top, little-endian. */
	UInt8 params[12];

	putLE32(params, m->got);
	putLE32(params + 4, (UInt32)userData);
	putLE32(params + 8, m->stackTop);
	return PceNativeCall((NativeFuncType *)m->code, params);
}
