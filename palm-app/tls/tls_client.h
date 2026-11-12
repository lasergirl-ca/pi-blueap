/* The Palm's TLS 1.3 client (VSP Transport) over wolfSSL, with no
 * sockets: the caller moves bytes between the network and the buffers
 * each call is given, so a native call never blocks or calls back into
 * 68k code. The same code builds for the host test.
 *
 * One connection at a time; its state is module globals. */
#ifndef TESSERA_TLS_CLIENT_H
#define TESSERA_TLS_CLIENT_H

#include <stddef.h>

#define TLS_PIN_MAX 4

/* Results */
#define TLS_DONE       0  /* handshake complete, or the data moved */
#define TLS_MORE       1  /* send what is in out, then call again with more input */
#define TLS_ERR_SETUP  -1 /* bad arguments, or out of memory */
#define TLS_ERR_PIN    -2 /* the server's key matches no pin, or its name is wrong */
#define TLS_ERR_ALPN   -3 /* the server did not select TLS_ALPN ("http/1.1") */
#define TLS_ERR_FAILED -4 /* any other TLS failure; tls_error says which */
#define TLS_ERR_CLOSED -5 /* the server closed the connection */

/* I/O for one call: in holds bytes received and not yet given to the
 * client (in_used says how many it took); out has room for out_cap bytes
 * to send (out_len says how many it wrote). */
struct tls_io {
	const unsigned char *in;
	size_t in_len, in_used;
	unsigned char *out;
	size_t out_cap, out_len;
};

/* tls_start sets up a connection to the server named sni, trusted if its
 * certificate's SPKI SHA-256 is one of pins (npins of 32 bytes), and
 * presenting cert (DER) with its Ed25519 key (the 32-byte seed), or no
 * certificate when cert is NULL (enrolment). With no pins (npins 0), any
 * key is accepted and recorded (tls_peer_spki). heap is the memory wolfSSL
 * allocates from. */
int tls_start(unsigned char *heap, size_t heap_size, const char *sni,
	const unsigned char *pins, int npins, const unsigned char *cert, size_t cert_len,
	const unsigned char *key_seed);

/* tls_handshake runs the handshake as far as io allows. */
int tls_handshake(struct tls_io *io);

/* tls_write encrypts len bytes of data into io->out; tls_read decrypts
 * up to cap bytes into data from io->in, setting *got. */
int tls_write(struct tls_io *io, const unsigned char *data, size_t len);
int tls_read(struct tls_io *io, unsigned char *data, size_t cap, size_t *got);

/* tls_exporter writes the TLS exporter value VSP binds its session to:
 * "EXPORTER-VSP-v1", an empty context, 32 bytes. Call it once, after the
 * handshake. */
int tls_exporter(unsigned char out[32]);

/* tls_peer_spki is the SPKI SHA-256 of the key the server showed; with no
 * pins (npins 0, an enrolment connection) any key for the name is taken,
 * and this is what the trust store must later pin. */
void tls_peer_spki(unsigned char out[32]);

/* tls_peer_cert is the SHA-256 of the server's certificate, the
 * fingerprint palm-gw's web login page shows, for first-use trust; and
 * tls_peer_match says which of the pins matched (0 to npins-1), or -1 on a
 * connection with no pins (palm-gw, D-062). */
void tls_peer_cert(unsigned char out[32]);
int tls_peer_match(void);

/* tls_close ends the connection, writing a close_notify into io->out if
 * io is not NULL, and frees everything. */
void tls_close(struct tls_io *io);

/* tls_selftest checks the client's code offline (tls_selftest.c) and
 * returns the TLS_TEST_* bits of the checks that pass: wolfSSL's
 * ChaCha20-Poly1305, X25519 and Ed25519 against their RFCs' vectors, the
 * ECDSA P-256 self-signature of cert (DER), and a ClientHello, whose size
 * goes in *hello_len. scratch holds at least cert_len bytes. It ends any
 * connection. */
#define TLS_TEST_CHACHA  0x01
#define TLS_TEST_X25519  0x02
#define TLS_TEST_ED25519 0x04
#define TLS_TEST_ECDSA   0x08
#define TLS_TEST_HELLO   0x10
#define TLS_TEST_ALL     0x1F
int tls_selftest(unsigned char *heap, size_t heap_size, const unsigned char *cert, size_t cert_len,
	unsigned char *scratch, size_t *hello_len);

/* tls_error is wolfSSL's error code for the last failure. */
int tls_error(void);

/* tls_heap_peak is the most heap the connection has used. */
size_t tls_heap_peak(void);

#endif
