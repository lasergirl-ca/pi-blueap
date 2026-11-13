/* The code sections past the first 32 KB code resource (palmgw.def,
 * docs/PALM-DEV.md section 3). The attribute goes on every prototype, in a
 * header the callers see, or a call lands in the wrong code resource;
 * tools/check-sections.sh fails the build if one does. */
#ifndef PALMGW_SECTIONS_H
#define PALMGW_SECTIONS_H

#ifdef __palmos__
#define PAIR_SEC __attribute__((section("pairf"))) /* Pair and Settings */
#define LIST_SEC __attribute__((section("listf"))) /* the list, text and input forms */
#define MODE_SEC __attribute__((section("modef"))) /* what each list and text form shows and does */
#else
#define PAIR_SEC
#define LIST_SEC
#define MODE_SEC
#endif

#endif
