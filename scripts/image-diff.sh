#!/bin/bash
# image-diff.sh BASE.img.xz IMAGE.img.xz: lists every file (path, type, mode, owner and a
# sha256 prefix) on both partitions of both images, prints the differences, then checks the
# palm-gw install in IMAGE (D-045). Runs as root on Linux arm64 with loop devices, e.g.:
#   docker run --rm --privileged --platform linux/arm64 -v "$PWD":/src -w /src debian:trixie-slim \
#     bash -c 'apt-get update -qq && apt-get install -y -qq fdisk xz-utils && scripts/image-diff.sh BASE IMAGE'
set -euo pipefail
[ $# -eq 2 ] || {
	echo "usage: image-diff.sh BASE.img.xz IMAGE.img.xz" >&2
	exit 2
}
W=${WORK:-/work}
mkdir -p "$W"
loops=()
mounts=()
cleanup() {
	local m l
	for m in "${mounts[@]}"; do umount "$m" || true; done
	for l in "${loops[@]}"; do losetup -d "$l" || true; done
	rm -f "$W/base.img" "$W/image.img"
}
trap cleanup EXIT

# attach FILE OFFSET LENGTH [losetup options...]: a loop device for one partition. In a
# container /dev holds only the loop nodes that existed when it started, so the free device
# losetup picks may have no node yet: make it.
attach() {
	local dev
	dev=$(losetup -f)
	[ -e "$dev" ] || mknod "$dev" b 7 "${dev#/dev/loop}"
	losetup "${@:4}" -o "$2" --sizelimit "$3" "$dev" "$1"
	echo "$dev"
}

manifest() { # IMG.xz NAME: unpack, mount both partitions read-only, list every file
	local img="$W/$2.img" n off len l dir
	xz -dc "$1" >"$img"
	for n in 1 2; do
		read -r off len < <(partx -g -o START,SECTORS -n "$n" "$img" | awk '{ print $1 * 512, $2 * 512 }')
		l=$(attach "$img" "$off" "$len" -r)
		loops+=("$l")
		dir="$W/$2.p$n"
		mkdir -p "$dir"
		mount -o ro "$l" "$dir"
		mounts+=("$dir")
		(cd "$dir" && find . -xdev \( -type f -printf '%p f %m %u:%g ' \
			-exec sh -c 'sha256sum <"$1" | cut -c1-16' _ {} \; \) \
			-o \( ! -type f -printf '%p %y %m %u:%g %l\n' \)) | sort >"$W/$2.p$n.txt"
	done
}
manifest "$1" base
manifest "$2" image

for n in 1 2; do
	echo "=== partition $n: < base, > image"
	diff "$W/base.p$n.txt" "$W/image.p$n.txt" | grep '^[<>]' | cut -c1-200 || true
done

r="$W/image.p2"
echo "=== checks"
# shellcheck disable=SC2016 # dpkg-query's own ${...} fields
chroot "$r" dpkg-query -W -f='${Package} ${Version} ${Status}\n' palm-gw
grep '^palm-gw:' "$r/etc/group"
cat "$r/etc/palm-gw-image"
enabled=$(find "$r/etc/systemd/system" -name 'palm-gw*' | sed "s|^$r||")
echo "palm-gw units enabled: ${enabled:-none}"
for f in usr/sbin/policy-rc.d etc/resolv.conf.palm-gw-build var/lib/apt/lists.palm-gw-build; do
	if [ -e "$r/$f" ]; then echo "left behind: /$f"; fi
done
cmp -s "$W/base.p2/etc/resolv.conf" "$r/etc/resolv.conf" && echo "resolv.conf: as in the base"
for f in user-data network-config meta-data cmdline.txt config.txt; do
	if [ -e "$W/image.p1/$f" ]; then echo "boot partition has $f"; fi
done
