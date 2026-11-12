/* The few C library functions wolfSSL calls that a Palm OS native module
 * has no library for. memcpy and memset are the module's own (module.c);
 * malloc and friends are wolfSSL's fallback allocator, given the TLS
 * client's heap, though tls_start points wolfSSL at it directly. */
#include <stddef.h>

void *tessera_heap_alloc(size_t n);
void tessera_heap_free(void *p);
void *tessera_heap_realloc(void *p, size_t n);

void *malloc(size_t n)
{
	return tessera_heap_alloc(n);
}

void free(void *p)
{
	tessera_heap_free(p);
}

void *realloc(void *p, size_t n)
{
	return tessera_heap_realloc(p, n);
}

size_t strlen(const char *s)
{
	size_t n = 0;
	while (s[n]) {
		n++;
	}
	return n;
}

int memcmp(const void *a, const void *b, size_t n)
{
	const unsigned char *x = a, *y = b;
	for (; n; n--, x++, y++) {
		if (*x != *y) {
			return *x - *y;
		}
	}
	return 0;
}

void *memmove(void *dst, const void *src, size_t n)
{
	unsigned char *d = dst;
	const unsigned char *s = src;

	if (d < s) {
		while (n--) {
			*d++ = *s++;
		}
	} else {
		while (n--) {
			d[n] = s[n];
		}
	}
	return dst;
}

int strncmp(const char *a, const char *b, size_t n)
{
	for (; n; n--, a++, b++) {
		if (*a != *b || *a == 0) {
			return (unsigned char)*a - (unsigned char)*b;
		}
	}
	return 0;
}

/* strcasecmp, ASCII only: wolfSSL's ECC code finds curves by name with it.
 * CradleServer's module left it unresolved, a call to address 0 should
 * that path ever run (palm-gw, D-062). */
int strcasecmp(const char *a, const char *b)
{
	unsigned char x, y;
	do {
		x = (unsigned char)*a++;
		y = (unsigned char)*b++;
		if (x >= 'A' && x <= 'Z') {
			x += 32;
		}
		if (y >= 'A' && y <= 'Z') {
			y += 32;
		}
	} while (x != 0 && x == y);
	return (int)x - (int)y;
}

int strcmp(const char *a, const char *b)
{
	for (; *a && *a == *b; a++, b++) {
	}
	return (unsigned char)*a - (unsigned char)*b;
}

char *strncpy(char *dst, const char *src, size_t n)
{
	size_t i = 0;

	for (; i < n && src[i]; i++) {
		dst[i] = src[i];
	}
	for (; i < n; i++) {
		dst[i] = 0;
	}
	return dst;
}

char *strstr(const char *hay, const char *needle)
{
	size_t n = strlen(needle);

	for (; *hay; hay++) {
		if (strncmp(hay, needle, n) == 0) {
			return (char *)hay;
		}
	}
	return n == 0 ? (char *)hay : NULL;
}
