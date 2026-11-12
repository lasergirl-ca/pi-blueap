/* The TLS module: the wolfSSL client (tls_client.c) behind the step
 * buffer of tlsmod.h, built position-independent for palm-app/loader and
 * linked into PalmGW.prc. One call per step, no blocking, no calls back
 * into 68k code (docs/PALM-APP.md). From CradleServer's palm/tls/module.c. */
#include <stddef.h>
#include <stdint.h>
#include <wolfssl/wolfcrypt/settings.h>
#include <wolfssl/wolfcrypt/sha256.h>
#include <wolfssl/wolfcrypt/chacha.h>
#include "tls_client.h"
#include "tlsmod.h"

/* GCC emits calls to these for struct copies and zeroed arrays; the rest
 * of the C library wolfSSL wants is palm_libc.c. */
void *memset(void *s, int c, size_t n)
{
	unsigned char *p = s;
	while (n--) {
		*p++ = (unsigned char)c;
	}
	return s;
}

void *memcpy(void *dst, const void *src, size_t n)
{
	unsigned char *d = dst;
	const unsigned char *s = src;
	while (n--) {
		*d++ = *s++;
	}
	return dst;
}

/* libgcc's 64-bit division is not built with -msingle-pic-base, so the
 * module has its own: shift and subtract, enough for the few calls. */
static uint64_t udivmod64(uint64_t n, uint64_t d, uint64_t *rem)
{
	uint64_t q = 0, r = 0;
	int i;

	if (d == 0) {
		*rem = 0;
		return 0;
	}
	for (i = 63; i >= 0; i--) {
		r = r << 1 | (n >> i & 1);
		if (r >= d) {
			r -= d;
			q |= (uint64_t)1 << i;
		}
	}
	*rem = r;
	return q;
}

uint64_t __udivdi3(uint64_t n, uint64_t d)
{
	uint64_t r;
	return udivmod64(n, d, &r);
}

uint64_t __umoddi3(uint64_t n, uint64_t d)
{
	uint64_t r;
	udivmod64(n, d, &r);
	return r;
}

/* ---- random bytes: ChaCha20 with fast key erasure over a 32-byte state
 * the 68k side seeds (TM_SEED) from what it can gather ---- */

static unsigned char rng_state[32];
static int rng_ready;

static void rng_reseed(const unsigned char *seed)
{
	static const unsigned char label[] = "palm-gw tls rng v1";
	wc_Sha256 sha;
	unsigned char hash[WC_SHA256_DIGEST_SIZE];

	wc_InitSha256(&sha);
	wc_Sha256Update(&sha, label, sizeof label - 1);
	wc_Sha256Update(&sha, rng_state, sizeof rng_state);
	wc_Sha256Update(&sha, seed, 32);
	wc_Sha256Final(&sha, hash);
	memcpy(rng_state, hash, 32);
	memset(hash, 0, sizeof hash);
	rng_ready = 1;
}

int tessera_tls_random(unsigned char *out, unsigned int size)
{
	ChaCha cc;
	static const unsigned char iv[12] = { 0 };
	unsigned char next[32];
	unsigned char zero[64];
	unsigned int n;

	if (!rng_ready) {
		return -1;
	}
	/* the first 32 bytes of the stream become the new state, the rest the
	 * output: an output never reveals the state that made the next one */
	wc_Chacha_SetKey(&cc, rng_state, 32);
	wc_Chacha_SetIV(&cc, iv, 0);
	memset(zero, 0, sizeof zero);
	wc_Chacha_Process(&cc, next, zero, 32);
	while (size > 0) {
		n = size > sizeof zero ? sizeof zero : size;
		wc_Chacha_Process(&cc, out, zero, n);
		out += n;
		size -= n;
	}
	memcpy(rng_state, next, 32);
	memset(next, 0, sizeof next);
	memset(&cc, 0, sizeof cc);
	return 0;
}

/* ---- the steps ---- */

static unsigned get16(const unsigned char *p) { return (unsigned)p[0] << 8 | p[1]; }
static unsigned long get32(const unsigned char *p)
{
	return (unsigned long)p[0] << 24 | (unsigned long)p[1] << 16 | (unsigned long)p[2] << 8 | p[3];
}
static void put16(unsigned char *p, unsigned v)
{
	p[0] = (unsigned char)(v >> 8);
	p[1] = (unsigned char)v;
}
static void put32(unsigned char *p, unsigned long v)
{
	p[0] = (unsigned char)(v >> 24);
	p[1] = (unsigned char)(v >> 16);
	p[2] = (unsigned char)(v >> 8);
	p[3] = (unsigned char)v;
}

static unsigned long step(unsigned char *buf)
{
	struct tls_io io;
	size_t got = 0;
	unsigned app = get16(buf + TM_APPLEN);
	int r = TLS_DONE;

	io.in = buf + TM_IN;
	io.in_len = get16(buf + TM_INLEN);
	io.in_used = 0;
	io.out = buf + TM_OUT;
	io.out_cap = TM_IOMAX;
	io.out_len = 0;
	buf[TM_STATUS] = TM_OK;
	if (io.in_len > TM_IOMAX || app > TM_APPMAX) {
		buf[TM_STATUS] = TM_ERR_LEN;
		return TM_MAGIC;
	}
	switch (buf[TM_OP]) {
	case TM_OP_START:
		if (buf[TM_NPINS] > TM_PINMAX) {
			buf[TM_STATUS] = TM_ERR_LEN;
			return TM_MAGIC;
		}
		rng_reseed(buf + TM_SEED);
		buf[TM_SNI + 63] = 0;
		r = tls_start((unsigned char *)get32(buf + TM_HEAP), get32(buf + TM_HEAPLEN),
			(const char *)buf + TM_SNI, buf + TM_PINS, buf[TM_NPINS], NULL, 0, NULL);
		break;
	case TM_OP_HANDSHAKE:
		r = tls_handshake(&io);
		if (r == TLS_DONE) {
			tls_peer_spki(buf + TM_PEERSPKI);
			tls_peer_cert(buf + TM_PEERCERT);
			buf[TM_MATCH] = (unsigned char)(signed char)tls_peer_match();
		}
		break;
	case TM_OP_READ:
		r = tls_read(&io, buf + TM_APP, app, &got);
		put16(buf + TM_APPLEN, (unsigned)got);
		break;
	case TM_OP_WRITE:
		r = tls_write(&io, buf + TM_APP, app);
		break;
	case TM_OP_CLOSE:
		tls_close(&io);
		memset(rng_state, 0, sizeof rng_state);
		rng_ready = 0;
		break;
	case TM_OP_RESEED:
		rng_reseed(buf + TM_SEED);
		break;
	default:
		buf[TM_STATUS] = TM_ERR_OP;
		return TM_MAGIC;
	}
	buf[TM_RESULT] = (unsigned char)(signed char)r;
	put32(buf + TM_ERROR, r < 0 ? (unsigned long)(long)tls_error() : 0);
	put16(buf + TM_INUSED, (unsigned)io.in_used);
	put16(buf + TM_OUTLEN, (unsigned)io.out_len);
	put32(buf + TM_PEAK, (unsigned long)tls_heap_peak());
	return TM_MAGIC;
}

/* module_main is called by entry.S with the 68k side's parameter block:
 * the global offset table's address, then the shared buffer's, both
 * little-endian (palm/loader/loader.h). */
unsigned long module_main(const unsigned char *params, void *call68k)
{
	unsigned char *buf = (unsigned char *)((unsigned long)params[4] | (unsigned long)params[5] << 8 |
		(unsigned long)params[6] << 16 | (unsigned long)params[7] << 24);
	(void)call68k;
	return step(buf);
}
