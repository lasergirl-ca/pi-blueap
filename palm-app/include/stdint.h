/* The exact-width types for arm-palmos-gcc 3.3, which predates the
 * compiler-provided <stdint.h>. ARM on Palm OS 5 has 32-bit int and long
 * and 64-bit long long. Only what the ARM module's code needs. */
#ifndef PALM_ARM_STDINT_H
#define PALM_ARM_STDINT_H

typedef signed char        int8_t;
typedef unsigned char      uint8_t;
typedef short              int16_t;
typedef unsigned short     uint16_t;
typedef int                int32_t;
typedef unsigned int       uint32_t;
typedef long long          int64_t;
typedef unsigned long long uint64_t;
typedef int                intptr_t;
typedef unsigned int       uintptr_t;

#define INT16_MIN  (-32767 - 1)
#define INT16_MAX  32767
#define UINT16_MAX 65535
#define INT32_MAX  2147483647
#define UINT32_MAX 4294967295U
#define SIZE_MAX   4294967295U

#endif
