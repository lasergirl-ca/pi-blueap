#!/bin/sh
# Fail the build if any object calls a function in a different named code
# section with a PC-relative (DISP16) branch. That happens when a caller cannot
# see the function's section attribute, and on the device it jumps into the
# wrong code resource without a crash (FuelLog, Oct 22 2026). A PC-relative
# call between two functions in the same section is correct and allowed.
# Static functions are checked too, from each object's own symbol table: a
# static helper without the attribute lands in .text while its callers sit in
# the named section (FuelLog M2).
# From FuelLog (docs/PALM-DEV.md section 3). tools/check-sections.sh <linked-exe> <objs...>,
# with m68k-palmos-objdump on PATH.
set -e
exe="$1"; shift
map=$(mktemp); local=$(mktemp)
# COFF symbol lines look like: [ 12](sec  1)(fl 0x00)(ty   0)(scl   2) (nx 0) 0x0000001c Name
# scl 3 lines name a section (ty 0) or a static function (ty 20); scl 2 lines are externals.
symmap() {
    m68k-palmos-objdump -t "$1" | awk '
        match($0, /\(sec +[0-9]+\)/) { sec = substr($0, RSTART + 5, RLENGTH - 6) + 0 }
        /\(scl +3\)/ && /\(ty +0\)/ { secname[sec] = $NF }
        /\(scl +3\)/ && /\(ty +20\)/ && sec > 0 { sym[$NF] = sec }
        /\(scl +2\)/ && sec > 0 { sym[$NF] = sec }
        END { for (s in sym) { n = secname[sym[s]]; if (n != "") print s, n } }
    '
}
symmap "$exe" > "$map"
if [ ! -s "$map" ]; then echo "check-sections: no functions outside .text"; rm -f "$map" "$local"; exit 0; fi
status=0
for obj in "$@"; do
    symmap "$obj" > "$local"
    out=$(m68k-palmos-objdump -r "$obj" | awk -v obj="$obj" '
        FILENAME == ARGV[1] { symsec[$1] = $2; next }
        FILENAME == ARGV[2] { if (!($1 in symsec)) symsec[$1] = $2; next }
        /^RELOCATION RECORDS FOR \[/ { sec = $0; sub(/.*\[/, "", sec); sub(/\].*/, "", sec) }
        /DISP16/ { s = $NF; if ((s in symsec) && symsec[s] != sec)
            print "check-sections: " obj " (" sec ") calls " s " (" symsec[s] ") with a PC-relative branch; its declaration lacks the section attribute" }
    ' "$local" "$map" -)
    if [ -n "$out" ]; then echo "$out"; status=1; fi
done
# A `static const` table inside a sectioned function is emitted into that
# code section but addressed A5-relative (END16), so it reads garbage on the
# device (FuelLog M2: vanished buttons, a fatal exception). Make such tables
# plain `static` (they go to .data) or file-scope.
for obj in "$@"; do
    out=$(m68k-palmos-objdump -r "$obj" | awk -v obj="$obj" '
        /END16/ && ($3 ~ /^(pairf|listf|modef|\.text)([+-]|$)/) {
            print "check-sections: " obj " addresses " $3 " A5-relative at " $1 "; a static const table inside a sectioned function?" }')
    if [ -n "$out" ]; then echo "$out"; status=1; fi
done
n=$(wc -l < "$map" | tr -d ' ')
rm -f "$map" "$local"
[ $status -eq 0 ] && echo "check-sections: ok ($n sectioned function(s))"
exit $status
