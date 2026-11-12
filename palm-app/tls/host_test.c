/* The TLS client on the build machine, driven the way the Palm app drives
 * it: bytes moved between calls, nothing blocking inside. It connects,
 * runs the handshake with the pins given (none: first use, any key), prints
 * what the app would learn (the server key's SPKI SHA-256, the
 * certificate's SHA-256, which pin matched), then sends one HTTP request
 * and prints the reply. From CradleServer's palm/tls/host_test.c.
 *
 *   host_test HOST PORT PATH [PINHEX ...]
 *
 * Output lines: "spki <hex>", "cert <hex>", "match <n>", "heap <bytes>",
 * then the HTTP reply. Exit 0 when the handshake and the request worked;
 * 2 when the server's key matched no pin; 1 for anything else. */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <netdb.h>
#include <sys/socket.h>
#include <netinet/in.h>
#include <arpa/inet.h>
#include "tls_client.h"

int tessera_tls_random(unsigned char *out, unsigned int size)
{
	FILE *f = fopen("/dev/urandom", "rb");
	if (f == NULL || fread(out, 1, size, f) != size) {
		return -1;
	}
	fclose(f);
	return 0;
}

static unsigned char heap[128 * 1024];
static unsigned char inbuf[4096], outbuf[4096];
static struct tls_io io;
static int sock;

/* pump sends what the client wrote, drops what it consumed and, when it
 * wants more and said nothing, reads from the socket. */
static int pump(int r)
{
	size_t sent = 0;
	while (sent < io.out_len) {
		ssize_t n = write(sock, io.out + sent, io.out_len - sent);
		if (n <= 0) {
			return -1;
		}
		sent += (size_t)n;
	}
	memmove(inbuf, inbuf + io.in_used, io.in_len - io.in_used);
	io.in_len -= io.in_used;
	io.in_used = 0;
	io.out_len = 0;
	if (r == TLS_MORE && sent == 0) {
		ssize_t n = read(sock, inbuf + io.in_len, sizeof inbuf - io.in_len);
		if (n <= 0) {
			return -1;
		}
		io.in_len += (size_t)n;
	}
	return 0;
}

static int hexbyte(const char *s)
{
	int v = 0, i;
	for (i = 0; i < 2; i++) {
		char c = s[i];
		v <<= 4;
		if (c >= '0' && c <= '9') {
			v |= c - '0';
		} else if (c >= 'a' && c <= 'f') {
			v |= c - 'a' + 10;
		} else if (c >= 'A' && c <= 'F') {
			v |= c - 'A' + 10;
		} else {
			return -1;
		}
	}
	return v;
}

static void printhex(const char *label, const unsigned char *b)
{
	int i;
	printf("%s ", label);
	for (i = 0; i < 32; i++) {
		printf("%02x", b[i]);
	}
	printf("\n");
}

int main(int argc, char **argv)
{
	unsigned char pins[TLS_PIN_MAX * 32], hash[32], app[2048];
	struct addrinfo hints, *ai;
	char req[512];
	int npins = 0, r, i, b;
	size_t got;

	if (argc < 4 || argc - 4 > TLS_PIN_MAX) {
		fprintf(stderr, "usage: host_test HOST PORT PATH [PINHEX ...]\n");
		return 1;
	}
	for (i = 4; i < argc; i++, npins++) {
		if (strlen(argv[i]) != 64) {
			fprintf(stderr, "a pin is 64 hex digits\n");
			return 1;
		}
		for (b = 0; b < 32; b++) {
			int v = hexbyte(argv[i] + 2 * b);
			if (v < 0) {
				fprintf(stderr, "bad pin\n");
				return 1;
			}
			pins[npins * 32 + b] = (unsigned char)v;
		}
	}
	memset(&hints, 0, sizeof hints);
	hints.ai_family = AF_INET;
	hints.ai_socktype = SOCK_STREAM;
	if (getaddrinfo(argv[1], argv[2], &hints, &ai) != 0) {
		printf("cannot resolve %s\n", argv[1]);
		return 1;
	}
	sock = socket(AF_INET, SOCK_STREAM, 0);
	if (sock < 0 || connect(sock, ai->ai_addr, ai->ai_addrlen) != 0) {
		printf("cannot connect to %s:%s\n", argv[1], argv[2]);
		return 1;
	}
	freeaddrinfo(ai);

	io.in = inbuf;
	io.out = outbuf;
	io.out_cap = sizeof outbuf;
	if (tls_start(heap, sizeof heap, argv[1], pins, npins, NULL, 0, NULL) != TLS_DONE) {
		printf("tls_start failed\n");
		return 1;
	}
	for (;;) {
		r = tls_handshake(&io);
		if (r != TLS_DONE && r != TLS_MORE) {
			printf("handshake failed: %d (wolfSSL %d)\n", r, tls_error());
			return r == TLS_ERR_PIN ? 2 : 1;
		}
		if (pump(r) != 0) {
			printf("connection lost in the handshake\n");
			return 1;
		}
		if (r == TLS_DONE) {
			break;
		}
	}
	tls_peer_spki(hash);
	printhex("spki", hash);
	tls_peer_cert(hash);
	printhex("cert", hash);
	printf("match %d\nheap %lu\n", tls_peer_match(), (unsigned long)tls_heap_peak());

	snprintf(req, sizeof req, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", argv[3], argv[1]);
	for (;;) {
		r = tls_write(&io, (const unsigned char *)req, strlen(req));
		if (pump(r) != 0) {
			printf("connection lost sending\n");
			return 1;
		}
		if (r == TLS_DONE) {
			break;
		}
		if (r != TLS_MORE) {
			printf("write failed: %d\n", r);
			return 1;
		}
	}
	for (;;) {
		got = 0;
		r = tls_read(&io, app, sizeof app, &got);
		fwrite(app, 1, got, stdout);
		if (r == TLS_ERR_CLOSED) {
			break;
		}
		if (r != TLS_DONE && r != TLS_MORE) {
			printf("\nread failed: %d (wolfSSL %d)\n", r, tls_error());
			return 1;
		}
		if (got == 0 && pump(TLS_MORE) != 0) {
			break; /* the server closed the socket: Connection: close */
		}
	}
	tls_close(NULL);
	close(sock);
	return 0;
}
