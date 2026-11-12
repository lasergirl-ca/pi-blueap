/* wolfSSL settings for the Palm's TLS client (palm/tls). Built from
 * wolfSSL 5.9.4's tiny TLS 1.3 profile with X.509 verification, then
 * fitted to what the vault server speaks (VSP Transport):
 *
 *   - TLS 1.3 only, client only, ALPN "vsp/1"
 *   - TLS_CHACHA20_POLY1305_SHA256, X25519 key share
 *   - the server's self-signed ECDSA P-256 certificate, accepted only if
 *     its SPKI hash matches a trust store pin (tls_client.c checks)
 *   - an Ed25519 client certificate, the device's
 *
 * The same file builds the host test, so the client is tested against
 * the Go server on every CI run. */
#ifndef TESSERA_WOLFSSL_USER_SETTINGS_H
#define TESSERA_WOLFSSL_USER_SETTINGS_H

#define WOLFSSL_TINY_TLS13_CERT

#ifdef __palmos__
/* arm-palmos-gcc 3.3 predates the type-size macros wolfSSL looks for.
 * ARM on Palm OS 5: 32-bit int and long, 64-bit long long. Don't define
 * WOLF_C89 without W64LIT_FORCE_LONG_LONG (see palm/spike/README.md). */
#define SIZEOF_LONG 4
#define SIZEOF_LONG_LONG 8
/* No C library: ASCII case mapping, which is all wolfSSL needs (domain
 * name checks), and palm_libc.c for the rest. */
#define CTYPE_USER
#define XTOUPPER(c) ((c) >= 'a' && (c) <= 'z' ? (c) - 32 : (c))
#define XTOLOWER(c) ((c) >= 'A' && (c) <= 'Z' ? (c) + 32 : (c))
#endif

/* Naming Curve25519 here skips the profile's default curves, so P-256
 * (the server's certificate) is named too. */
#define HAVE_ECC
#define ECC_USER_CURVES
#define HAVE_CHACHA
#define HAVE_POLY1305
#define HAVE_CURVE25519
#define CURVE25519_SMALL
#define HAVE_ED25519
#define ED25519_SMALL
#define WOLFSSL_SHA512 /* Ed25519 needs it */
#define HAVE_ALPN
#define HAVE_SNI
#define WOLFSSL_KEY_SHARE_DEFAULT_GROUP WOLFSSL_ECC_X25519
#define WOLFSSL_ALWAYS_VERIFY_CB
#define KEEP_PEER_CERT
#define HAVE_KEYING_MATERIAL /* the TLS exporter, for VSP's channel binding */

/* No sockets or files: the 68k side moves the bytes. */
#define WOLFSSL_USER_IO
#define WOLFSSL_NO_SOCK
#define NO_FILESYSTEM
#define NO_WRITEV
#define WOLFSSL_NO_GETPID
#define NO_DEV_RANDOM

/* Random bytes from the module's own generator (palm/spike/rng.c), not
 * wolfSSL's DRBG. */
#define WC_NO_HASHDRBG
#define CUSTOM_RAND_GENERATE_BLOCK tessera_tls_random
int tessera_tls_random(unsigned char *out, unsigned int size);

/* No certificate dates: the server is trusted by its pin, as the Go
 * client trusts it, so the Palm needs no clock here. */
#define NO_ASN_TIME

#endif
