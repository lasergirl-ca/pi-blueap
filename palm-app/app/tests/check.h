/* The host tests' one helper (docs/PALM-DEV.md section 9). */
#include <stdio.h>
#include <string.h>

static int failures;

#define check(name, cond) \
	do { \
		if (cond) { \
			printf("ok   %s\n", name); \
		} else { \
			printf("FAIL %s (%s:%d)\n", name, __FILE__, __LINE__); \
			failures++; \
		} \
	} while (0)
