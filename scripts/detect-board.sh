#!/bin/sh
# palm-gw: print the board id (pi4 | pi5) from the device tree (PLAN.md section 3).
# Exit 1 on an unsupported board unless ALLOW_UNSUPPORTED=1, in which case print "other".
set -eu
if [ ! -r /proc/device-tree/model ]; then
  echo "detect-board: /proc/device-tree/model not readable; not a Raspberry Pi?" >&2
  [ "${ALLOW_UNSUPPORTED:-0}" = 1 ] && { echo other; exit 0; }
  exit 1
fi
model=$(tr -d '\0' < /proc/device-tree/model)
case "$model" in
  *"Pi 4"*) echo pi4 ;;
  *"Pi 5"*) echo pi5 ;;
  *)
    echo "detect-board: unsupported board: $model" >&2
    [ "${ALLOW_UNSUPPORTED:-0}" = 1 ] && { echo other; exit 0; }
    exit 1 ;;
esac
