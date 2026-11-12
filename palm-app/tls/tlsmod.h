/* The buffer the palm-gw app shares with the TLS module, and the steps it
 * asks for (docs/PALM-APP.md). One PceNativeCall per step; the 68k side
 * moves bytes with the Net Library between steps, so no call blocks and
 * nothing in the module calls back into 68k code. From CradleServer's
 * palm/tls/tlsmod.h, with room for several pins and the peer's hashes
 * (D-062).
 *
 * The buffer holds bytes only: integers big-endian, no pointers except
 * the heap's address, which the module uses as is (an address the 68k
 * side passes is the pointer itself on the Palm). */
#ifndef PALMGW_TLSMOD_H
#define PALMGW_TLSMOD_H

#define TM_OP      0  /* u8: TM_OP_*, the step */
#define TM_STATUS  1  /* u8: TM_OK or TM_ERR_* (the module's own checks) */
#define TM_RESULT  2  /* s8: the client's result (TLS_DONE, TLS_MORE, TLS_ERR_*) */
#define TM_ERROR   4  /* s32 BE: wolfSSL's error code after a failure */
#define TM_INLEN   8  /* u16 BE: bytes received, waiting at TM_IN */
#define TM_INUSED  10 /* u16 BE out: how many of them the client took */
#define TM_OUTLEN  12 /* u16 BE out: bytes to send, at TM_OUT */
#define TM_APPLEN  14 /* u16 BE: application bytes at TM_APP: in for WRITE; the cap in, the count out for READ */
#define TM_HEAP    16 /* u32 BE: address of the client's heap (START) */
#define TM_HEAPLEN 20 /* u32 BE: its size (START) */
#define TM_PEAK    24 /* u32 BE out: the heap's peak use so far */
#define TM_NPINS   28 /* u8: how many pins at TM_PINS (START); 0 = first use, any key */
#define TM_MATCH   29 /* s8 out (HANDSHAKE): which pin matched, -1 for none */
#define TM_PINS    32 /* TM_PINMAX x 32 bytes: SPKI SHA-256 pins (START) */
#define TM_PINMAX  4
#define TM_SNI     160 /* the server's name or address, NUL-terminated, up to 63 bytes (START) */
#define TM_SEED    224 /* 32 bytes: entropy from the 68k side for the module's generator (START, RESEED) */
#define TM_PEERSPKI 256 /* 32 bytes out (HANDSHAKE): the server key's SPKI SHA-256 */
#define TM_PEERCERT 288 /* 32 bytes out (HANDSHAKE): its certificate's SHA-256 */
#define TM_IN      320
#define TM_IOMAX   4096
#define TM_OUT     (TM_IN + TM_IOMAX)
#define TM_APP     (TM_OUT + TM_IOMAX)
#define TM_APPMAX  2048
#define TM_BUF_SIZE (TM_APP + TM_APPMAX)

#define TM_OP_START     1 /* heap, pins, name and seed in: set the client up */
#define TM_OP_HANDSHAKE 2 /* drive the handshake: TLS_MORE until TLS_DONE */
#define TM_OP_READ      3 /* up to TM_APPLEN bytes into TM_APP; TM_APPLEN out: how many */
#define TM_OP_WRITE     4 /* TM_APPLEN bytes from TM_APP */
#define TM_OP_CLOSE     5 /* close_notify into TM_OUT, state wiped */
#define TM_OP_RESEED    6 /* mix TM_SEED into the generator */

#define TM_OK      0
#define TM_ERR_OP  1
#define TM_ERR_LEN 2

/* Returned through PceNativeCall, so the 68k side can tell that the
 * module really ran. */
#define TM_MAGIC 0x9A1D6C57UL

#endif
