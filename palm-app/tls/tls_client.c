/* The Palm's TLS 1.3 client over wolfSSL (see tls_client.h), built with
 * the wolfSSL sources it needs for the ARM module and the host test
 * alike (Makefile). From CradleServer's palm/tls, which has it from
 * Tessera; changed for palm-gw (docs/PALM-APP.md, D-062): ALPN http/1.1,
 * no SNI or name check for a server given by IP address, the
 * certificate's SHA-256 recorded for first-use trust, and which pin
 * matched. */
#include <wolfssl/wolfcrypt/settings.h>
#include <wolfssl/ssl.h>
#include <wolfssl/wolfcrypt/asn_public.h>
#include <wolfssl/wolfcrypt/sha256.h>
#include <wolfssl/wolfcrypt/hash.h>
#include <wolfssl/wolfcrypt/memory.h>
#include <wolfssl/wolfcrypt/error-crypt.h>
#include <wolfssl/error-ssl.h>
#include "tls_client.h"
#include "tls_heap.c"

/* The protocol the client asks for; palm-gw's listeners offer it. */
#ifndef TLS_ALPN
#define TLS_ALPN "http/1.1"
#endif

static WOLFSSL_CTX *tls_ctx;
static WOLFSSL *tls_ssl;
static struct tls_io *tls_cur;
static unsigned char tls_pins[TLS_PIN_MAX][32];
static int tls_npins, tls_pinned, tls_last;
static unsigned char tls_spki[32]; /* the SPKI SHA-256 of the key the server showed */
static unsigned char tls_cert_hash[32]; /* the SHA-256 of its certificate, as the web UI shows it */
static int tls_match = -1; /* which pin matched; -1 for none (first use) */

/* The I/O callbacks read what the caller received and write what it will
 * send; with nothing to read, or no room to write, the call returns and
 * the caller moves bytes before calling again. */
static int tls_recv(WOLFSSL *ssl, char *buf, int sz, void *ctx)
{
	struct tls_io *io = tls_cur;
	size_t n;

	(void)ssl;
	(void)ctx;
	if (io == NULL || io->in_used >= io->in_len) {
		return WOLFSSL_CBIO_ERR_WANT_READ;
	}
	n = io->in_len - io->in_used;
	if (n > (size_t)sz) {
		n = (size_t)sz;
	}
	XMEMCPY(buf, io->in + io->in_used, n);
	io->in_used += n;
	return (int)n;
}

static int tls_send(WOLFSSL *ssl, char *buf, int sz, void *ctx)
{
	struct tls_io *io = tls_cur;
	size_t n;

	(void)ssl;
	(void)ctx;
	if (io == NULL || io->out_len >= io->out_cap) {
		return WOLFSSL_CBIO_ERR_WANT_WRITE;
	}
	n = io->out_cap - io->out_len;
	if (n > (size_t)sz) {
		n = (size_t)sz;
	}
	XMEMCPY(io->out + io->out_len, buf, n);
	io->out_len += n;
	return (int)n;
}

/* tls_same compares in constant time. */
static int tls_same(const unsigned char *a, const unsigned char *b, int n)
{
	unsigned char d = 0;
	int i;

	for (i = 0; i < n; i++) {
		d |= a[i] ^ b[i];
	}
	return d == 0;
}

/* tls_wipe zeroes memory the compiler may not optimize away. */
static void tls_wipe(void *p, size_t n)
{
	volatile unsigned char *v = p;

	while (n--) {
		*v++ = 0;
	}
}

/* tls_verify accepts the server's certificate only if its public key is
 * pinned (VSP Validating the server), as the Go client does: there is no
 * CA, and the dates are not checked. wolfSSL reports the missing signer,
 * which the pin replaces; any other error, a name mismatch above all,
 * stands. */
static int tls_verify(int preverify, WOLFSSL_X509_STORE_CTX *store)
{
	unsigned char spki[256], hash[32];
	word32 spki_len = sizeof spki;
	int i, err = store->error;

	if (store->error_depth != 0) {
		return preverify || err == ASN_NO_SIGNER_E;
	}
	if (err != 0 && err != ASN_NO_SIGNER_E && err != ASN_SELF_SIGNED_E) {
		return 0;
	}
	if (store->totalCerts < 1 || store->certs == NULL ||
		wc_GetSubjectPubKeyInfoDerFromCert(store->certs[0].buffer, store->certs[0].length, spki, &spki_len) != 0 ||
		wc_Sha256Hash(spki, spki_len, hash) != 0) {
		return 0;
	}
	XMEMCPY(tls_spki, hash, 32);
	if (wc_Sha256Hash(store->certs[0].buffer, store->certs[0].length, tls_cert_hash) != 0) {
		return 0;
	}
	/* No pins is a first connection: any key, recorded for the app to show
	 * and, once the user trusts it, to pin (docs/PALM-APP.md). */
	if (tls_npins == 0) {
		tls_pinned = 1;
		return 1;
	}
	for (i = 0; i < tls_npins; i++) {
		if (tls_same(hash, tls_pins[i], 32)) {
			tls_pinned = 1;
			tls_match = i;
			return 1;
		}
	}
	return 0;
}

/* An Ed25519 private key as PKCS #8, the form wolfSSL loads. */
static const unsigned char ed25519_pkcs8_head[16] = {
	0x30, 0x2e, 0x02, 0x01, 0x00, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65, 0x70, 0x04, 0x22, 0x04, 0x20,
};

static int tls_fail(int code)
{
	tls_last = code;
	return code;
}

/* tls_is_ip reports a dotted IPv4 address: SNI may not carry one (RFC
 * 6066), and wolfSSL's name check is for DNS names, so for an address the
 * pin alone says which server it is. */
static int tls_is_ip(const char *s)
{
	int dots = 0;

	if (*s == 0) {
		return 1;
	}
	for (; *s; s++) {
		if (*s == '.') {
			dots++;
		} else if (*s < '0' || *s > '9') {
			return 0;
		}
	}
	return dots == 3;
}

int tls_start(unsigned char *heap, size_t heap_size, const char *sni,
	const unsigned char *pins, int npins, const unsigned char *cert, size_t cert_len,
	const unsigned char *key_seed)
{
	unsigned char key[48];
	int i, ok;

	tls_close(NULL);
	if (npins < 0 || npins > TLS_PIN_MAX || sni == NULL) {
		return TLS_ERR_SETUP;
	}
	heap_init(heap, heap_size);
	wolfSSL_SetAllocators(heap_alloc, heap_free, heap_realloc);
	for (i = 0; i < npins; i++) {
		XMEMCPY(tls_pins[i], pins + 32 * i, 32);
	}
	tls_npins = npins;
	tls_pinned = 0;
	tls_match = -1;
	tls_last = 0;
	if (wolfSSL_Init() != WOLFSSL_SUCCESS) {
		return TLS_ERR_SETUP;
	}
	tls_ctx = wolfSSL_CTX_new(wolfTLSv1_3_client_method());
	if (tls_ctx == NULL) {
		return TLS_ERR_SETUP;
	}
	wolfSSL_CTX_SetIORecv(tls_ctx, tls_recv);
	wolfSSL_CTX_SetIOSend(tls_ctx, tls_send);
	wolfSSL_CTX_set_verify(tls_ctx, WOLFSSL_VERIFY_PEER, tls_verify);
	ok = wolfSSL_CTX_set_cipher_list(tls_ctx, "TLS13-CHACHA20-POLY1305-SHA256") == WOLFSSL_SUCCESS;
	if (ok && cert != NULL) {
		XMEMCPY(key, ed25519_pkcs8_head, sizeof ed25519_pkcs8_head);
		XMEMCPY(key + sizeof ed25519_pkcs8_head, key_seed, 32);
		ok = wolfSSL_CTX_use_certificate_buffer(tls_ctx, cert, (long)cert_len, WOLFSSL_FILETYPE_ASN1) == WOLFSSL_SUCCESS &&
			wolfSSL_CTX_use_PrivateKey_buffer(tls_ctx, key, sizeof key, WOLFSSL_FILETYPE_ASN1) == WOLFSSL_SUCCESS;
		tls_wipe(key, sizeof key);
	}
	if (ok) {
		tls_ssl = wolfSSL_new(tls_ctx);
		ok = tls_ssl != NULL &&
			(wolfSSL_KeepArrays(tls_ssl), 1) &&
			(tls_is_ip(sni) ||
				(wolfSSL_UseSNI(tls_ssl, WOLFSSL_SNI_HOST_NAME, sni, (word16)XSTRLEN(sni)) == WOLFSSL_SUCCESS &&
				wolfSSL_check_domain_name(tls_ssl, sni) == WOLFSSL_SUCCESS)) &&
			wolfSSL_UseALPN(tls_ssl, (char *)TLS_ALPN, sizeof TLS_ALPN - 1, WOLFSSL_ALPN_FAILED_ON_MISMATCH) == WOLFSSL_SUCCESS &&
			wolfSSL_UseKeyShare(tls_ssl, WOLFSSL_ECC_X25519) == WOLFSSL_SUCCESS;
	}
	if (!ok) {
		tls_close(NULL);
		return TLS_ERR_SETUP;
	}
	return TLS_DONE;
}

/* tls_result turns a wolfSSL return into ours. */
static int tls_result(int ret)
{
	int err;

	if (ret == WOLFSSL_SUCCESS) {
		return TLS_DONE;
	}
	err = wolfSSL_get_error(tls_ssl, ret);
	if (err == WOLFSSL_ERROR_WANT_READ || err == WOLFSSL_ERROR_WANT_WRITE) {
		return TLS_MORE;
	}
	tls_last = err;
	if (err == WOLFSSL_ERROR_ZERO_RETURN) {
		return TLS_ERR_CLOSED;
	}
	if (err == DOMAIN_NAME_MISMATCH || err == VERIFY_CERT_ERROR || err == ASN_NO_SIGNER_E) {
		return TLS_ERR_PIN;
	}
	return TLS_ERR_FAILED;
}

int tls_handshake(struct tls_io *io)
{
	char *proto = NULL;
	word16 len = 0;
	int r;

	if (tls_ssl == NULL) {
		return TLS_ERR_SETUP;
	}
	tls_cur = io;
	r = tls_result(wolfSSL_connect(tls_ssl));
	tls_cur = NULL;
	if (r != TLS_DONE) {
		return r;
	}
	if (!tls_pinned) {
		return tls_fail(TLS_ERR_PIN);
	}
	if (wolfSSL_ALPN_GetProtocol(tls_ssl, &proto, &len) != WOLFSSL_SUCCESS ||
		len != sizeof TLS_ALPN - 1 || XMEMCMP(proto, TLS_ALPN, len) != 0) {
		return tls_fail(TLS_ERR_ALPN);
	}
	return TLS_DONE;
}

int tls_write(struct tls_io *io, const unsigned char *data, size_t len)
{
	int r;

	if (tls_ssl == NULL || !tls_pinned) {
		return TLS_ERR_SETUP;
	}
	tls_cur = io;
	r = wolfSSL_write(tls_ssl, data, (int)len);
	r = r == (int)len ? TLS_DONE : tls_result(r);
	tls_cur = NULL;
	return r;
}

int tls_read(struct tls_io *io, unsigned char *data, size_t cap, size_t *got)
{
	int r;

	*got = 0;
	if (tls_ssl == NULL || !tls_pinned) {
		return TLS_ERR_SETUP;
	}
	tls_cur = io;
	r = wolfSSL_read(tls_ssl, data, (int)cap);
	if (r > 0) {
		*got = (size_t)r;
		r = TLS_DONE;
	} else {
		r = tls_result(r);
	}
	tls_cur = NULL;
	return r;
}

void tls_close(struct tls_io *io)
{
	if (tls_ssl != NULL) {
		if (io != NULL) {
			tls_cur = io;
			wolfSSL_shutdown(tls_ssl);
			tls_cur = NULL;
		}
		wolfSSL_free(tls_ssl);
		tls_ssl = NULL;
	}
	if (tls_ctx != NULL) {
		wolfSSL_CTX_free(tls_ctx);
		tls_ctx = NULL;
		wolfSSL_Cleanup();
	}
	tls_npins = 0;
	tls_pinned = 0;
	tls_match = -1;
	XMEMSET(tls_pins, 0, sizeof tls_pins);
}

/* The heap, for palm_libc.c's malloc. */
void *tessera_heap_alloc(size_t n)
{
	return heap_alloc(n);
}

void tessera_heap_free(void *p)
{
	heap_free(p);
}

void *tessera_heap_realloc(void *p, size_t n)
{
	return heap_realloc(p, n);
}

int tls_error(void)
{
	return tls_last;
}

size_t tls_heap_peak(void)
{
	return heap_peak;
}

int tls_exporter(unsigned char out[32])
{
	static const char label[] = "EXPORTER-VSP-v1";
	int ok;

	if (tls_ssl == NULL || !tls_pinned) {
		return TLS_ERR_SETUP;
	}
	/* RFC 8446 section 7.5 with an empty context; the handshake data it
	 * needs is freed once it is taken. */
	ok = wolfSSL_export_keying_material(tls_ssl, out, 32, label, sizeof label - 1, NULL, 0, 0) == WOLFSSL_SUCCESS;
	wolfSSL_FreeArrays(tls_ssl);
	return ok ? TLS_DONE : TLS_ERR_FAILED;
}

void tls_peer_spki(unsigned char out[32])
{
	XMEMCPY(out, tls_spki, 32);
}

void tls_peer_cert(unsigned char out[32])
{
	XMEMCPY(out, tls_cert_hash, 32);
}

int tls_peer_match(void)
{
	return tls_match;
}
