/* Palm's fixed-size types on the Palm, and the same names from <stdint.h>
 * on the build machine, so the modules without UI compile for both
 * (docs/PALM-DEV.md section 4). */
#ifndef PALMGW_PTYPES_H
#define PALMGW_PTYPES_H

#ifdef __palmos__
#include <PalmTypes.h>
#else
#include <stddef.h>
#include <stdint.h>
typedef uint8_t UInt8;
typedef int8_t Int8;
typedef uint16_t UInt16;
typedef int16_t Int16;
typedef uint32_t UInt32;
typedef int32_t Int32;
typedef char Char;
typedef unsigned char Boolean;
#ifndef true
#define true 1
#define false 0
#endif
#endif

#endif
