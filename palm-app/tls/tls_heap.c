/* The memory wolfSSL allocates from: first fit over one block the caller
 * gives tls_start, with neighbouring free blocks merged. Palm OS has no
 * malloc a native module can use, and this also records the peak, which
 * the spike measures. Blocks are 8-byte aligned; each has an 8-byte
 * header giving its size and whether it is in use. */
#include <stddef.h>

struct heap_block {
	size_t size;     /* bytes after this header */
	size_t used;     /* 1 in use, 0 free */
};

static unsigned char *heap_base;
static size_t heap_size, heap_used, heap_peak;

#define HEAP_ALIGN(n) (((n) + 7) & ~(size_t)7)
#define HEAP_HDR      sizeof(struct heap_block)

static void heap_init(unsigned char *base, size_t size)
{
	struct heap_block *b;
	size_t skip = (size_t)(-(long)(size_t)base & 7);

	heap_base = base + skip;
	heap_size = (size - skip) & ~(size_t)7;
	heap_used = heap_peak = 0;
	b = (struct heap_block *)heap_base;
	b->size = heap_size - HEAP_HDR;
	b->used = 0;
}

static struct heap_block *heap_next(struct heap_block *b)
{
	unsigned char *n = (unsigned char *)b + HEAP_HDR + b->size;
	return n < heap_base + heap_size ? (struct heap_block *)n : NULL;
}

static void *heap_alloc(size_t n)
{
	struct heap_block *b, *rest;

	if (heap_base == NULL || n == 0) {
		return NULL;
	}
	n = HEAP_ALIGN(n);
	for (b = (struct heap_block *)heap_base; b != NULL; b = heap_next(b)) {
		if (b->used || b->size < n) {
			continue;
		}
		if (b->size >= n + HEAP_HDR + 16) {
			rest = (struct heap_block *)((unsigned char *)b + HEAP_HDR + n);
			rest->size = b->size - n - HEAP_HDR;
			rest->used = 0;
			b->size = n;
		}
		b->used = 1;
		heap_used += b->size + HEAP_HDR;
		if (heap_used > heap_peak) {
			heap_peak = heap_used;
		}
		return (unsigned char *)b + HEAP_HDR;
	}
	return NULL;
}

/* heap_free wipes the block, since wolfSSL keeps keys in what it frees,
 * and merges it with free neighbours. */
static void heap_free(void *p)
{
	struct heap_block *b, *n, *prev = NULL;
	volatile unsigned char *w;
	size_t i;

	if (p == NULL) {
		return;
	}
	for (b = (struct heap_block *)heap_base; b != NULL; prev = b, b = heap_next(b)) {
		if ((unsigned char *)b + HEAP_HDR != p) {
			continue;
		}
		if (!b->used) {
			return;
		}
		w = (volatile unsigned char *)p;
		for (i = 0; i < b->size; i++) {
			w[i] = 0;
		}
		b->used = 0;
		heap_used -= b->size + HEAP_HDR;
		n = heap_next(b);
		if (n != NULL && !n->used) {
			b->size += HEAP_HDR + n->size;
		}
		if (prev != NULL && !prev->used) {
			prev->size += HEAP_HDR + b->size;
		}
		return;
	}
}

static void *heap_realloc(void *p, size_t n)
{
	struct heap_block *b;
	unsigned char *q;
	size_t i, keep;

	if (p == NULL) {
		return heap_alloc(n);
	}
	b = (struct heap_block *)((unsigned char *)p - HEAP_HDR);
	if (HEAP_ALIGN(n) <= b->size) {
		return p;
	}
	q = heap_alloc(n);
	if (q == NULL) {
		return NULL;
	}
	keep = b->size;
	for (i = 0; i < keep; i++) {
		q[i] = ((unsigned char *)p)[i];
	}
	heap_free(p);
	return q;
}
