/* The 68k loader for a native ARM module built with module.ld and
 * cmd/palmlink (see README.md). */
#ifndef TESSERA_LOADER_H
#define TESSERA_LOADER_H

#include <PalmOS.h>

typedef struct {
	MemPtr code;      /* the code, in the storage heap (a feature pointer) */
	MemPtr dataChunk; /* the data block's chunk in the dynamic heap */
	UInt8 *data;      /* the data block, 8-byte aligned inside dataChunk */
	UInt32 got;       /* address of the global offset table */
	UInt32 stackTop;  /* the module's own stack (ModuleSetStack), or 0 */
	UInt32 creator;
	UInt16 feature;
} ModuleType;

/* Errors besides the Memory and Data Manager's */
#define moduleErrMissing  (appErrorClass | 0x40) /* a resource is missing */
#define moduleErrFormat   (appErrorClass | 0x41) /* the header is not one this loader reads */
#define moduleErrTooLarge (appErrorClass | 0x42) /* the data block is over one chunk */

/* ModuleLoad reads the module's resources (code 'armc', data 'armd' and
 * header 'armh', from firstID), copies the code into a feature pointer
 * (creator, feature) and the data into the dynamic heap, and relocates
 * the data. */
Err ModuleLoad(ModuleType *m, UInt32 creator, UInt16 feature, DmResID firstID);

/* ModuleSetStack gives the module a stack of its own, size bytes at
 * stack, for later calls; NULL returns it to the system's native stack.
 * The caller keeps the memory until ModuleUnload. */
void ModuleSetStack(ModuleType *m, void *stack, UInt32 size);

/* ModuleCall runs the module's entry with userData and returns what it
 * returns. */
UInt32 ModuleCall(const ModuleType *m, void *userData);

/* ModuleUnload frees what ModuleLoad allocated. */
void ModuleUnload(ModuleType *m);

#endif
