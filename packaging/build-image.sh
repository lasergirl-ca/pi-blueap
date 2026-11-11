#!/bin/bash
# Builds the ready-to-flash palm-gw image (D-002, D-045): a stock Raspberry Pi OS Lite
# (64-bit) image with the palm-gw .deb installed, nothing else changed. Cloud-init, SSH and
# the first-boot resize stay exactly as Raspberry Pi ships them, so Raspberry Pi Imager's
# customisation (user, SSH key, Wi-Fi, hostname) works as it does for the stock image.
# The gateway is not configured: a login shell says "Run: sudo palm-gw setup" until it is.
#
#   build-image.sh BASE.img.xz PALM-GW.deb OUT.img.xz
#
# Runs as root on Linux arm64 with loop devices (`make image` runs it in a privileged
# Docker container). Needs xz, sfdisk/partx, losetup, e2fsck, resize2fs and zerofree.
# Writes OUT.img.xz, OUT.img.xz.sha256 and, for Raspberry Pi Imager, OUT.json (a
# repository file: rpi-imager --repo OUT.json). Environment:
#   WORK=DIR         scratch directory for the uncompressed image (default: next to OUT)
#   GROW_MB=N        space added to the root filesystem before installing (default 0:
#                    the stock image has ~430 MB free; the first boot grows it anyway)
#   IMAGE_URL=URL    where OUT.img.xz will be published, for the Imager entry
#   IMAGE_VERSION=V  the palm-gw version, for /etc/palm-gw-image and the Imager entry
set -euo pipefail

die() {
	printf 'build-image: %s\n' "$*" >&2
	exit 1
}
say() { printf '==> %s\n' "$*"; }

[ $# -eq 3 ] || die "usage: build-image.sh BASE.img.xz PALM-GW.deb OUT.img.xz"
base=$1 deb=$2 out=$3
case "$out" in *.img.xz) ;; *) die "OUT must end in .img.xz" ;; esac
[ -f "$base" ] || die "$base: no such file"
[ -f "$deb" ] || die "$deb: no such file"
[ "$(id -u)" -eq 0 ] || die "run as root"
[ "$(uname -m)" = aarch64 ] || die "run on arm64 (this is $(uname -m)): the image's programs run in a chroot"
GROW_MB=${GROW_MB:-0}
WORK=${WORK:-$(dirname "$out")}
IMAGE_VERSION=${IMAGE_VERSION:-$(dpkg-deb -f "$deb" Version 2>/dev/null || echo unknown)}
name=$(basename "$out" .img.xz)
img="$WORK/$name.img"
mnt="$WORK/$name.mnt"

loops=()
mounts=()
cleanup() {
	set +e
	local i
	for ((i = ${#mounts[@]} - 1; i >= 0; i--)); do umount "${mounts[i]}" 2>/dev/null || umount -l "${mounts[i]}"; done
	for i in "${loops[@]}"; do losetup -d "$i"; done
	[ -d "$mnt" ] && rmdir "$mnt"
	[ -f "$img" ] && rm -f "$img"
}
trap cleanup EXIT

mountit() { # mount args..., last is the target
	mount "$@"
	mounts+=("${*: -1}")
}

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

# partition N's start and length in bytes
part() {
	partx -g -o START,SECTORS -n "$1" "$img" | awk '{ print $1 * 512, $2 * 512 }'
}

say "Unpacking $(basename "$base")"
mkdir -p "$WORK"
xz -dc "$base" >"$img"
say "Base image: $(sha256sum "$img" | cut -c1-64), $(stat -c %s "$img") bytes"

if [ "$GROW_MB" -gt 0 ]; then
	say "Growing the root filesystem by $GROW_MB MB"
	truncate -s "+${GROW_MB}M" "$img"
	echo ", +" | sfdisk --quiet --no-reread --no-tell-kernel -N 2 "$img"
fi
read -r root_off root_len < <(part 2)
read -r boot_off boot_len < <(part 1)
root_loop=$(attach "$img" "$root_off" "$root_len")
loops+=("$root_loop")
e2fsck -pf "$root_loop" || [ $? -le 1 ] || die "e2fsck failed on the root filesystem"
if [ "$GROW_MB" -gt 0 ]; then
	resize2fs -p "$root_loop" >/dev/null
fi
boot_loop=$(attach "$img" "$boot_off" "$boot_len")
loops+=("$boot_loop")

mkdir -p "$mnt"
mountit "$root_loop" "$mnt"
mountit "$boot_loop" "$mnt/boot/firmware"
[ -f "$mnt/boot/firmware/config.txt" ] || die "no config.txt on the boot partition: not a Raspberry Pi OS image?"
[ -f "$mnt/etc/os-release" ] || die "no /etc/os-release on the root partition"
say "Base: $(sed -n 's/^PRETTY_NAME=//p' "$mnt/etc/os-release" | tr -d '"')"

# A chroot that can run apt: kernel filesystems, the build host's DNS, and no service starts.
mountit -t proc proc "$mnt/proc"
mountit -t sysfs sys "$mnt/sys"
mountit --bind /dev "$mnt/dev"
mountit --bind /dev/pts "$mnt/dev/pts"
[ ! -e "$mnt/usr/sbin/policy-rc.d" ] || die "the base image already has a policy-rc.d"
printf '#!/bin/sh\nexit 101\n' >"$mnt/usr/sbin/policy-rc.d"
chmod 0755 "$mnt/usr/sbin/policy-rc.d"
resolv_saved=""
if [ -e "$mnt/etc/resolv.conf" ] || [ -L "$mnt/etc/resolv.conf" ]; then
	mv "$mnt/etc/resolv.conf" "$mnt/etc/resolv.conf.palm-gw-build"
	resolv_saved=1
fi
cp -L /etc/resolv.conf "$mnt/etc/resolv.conf"

in_chroot() {
	env -i PATH=/usr/sbin:/usr/bin:/sbin:/bin LANG=C.UTF-8 DEBIAN_FRONTEND=noninteractive \
		chroot "$mnt" "$@"
}

say "Installing $(basename "$deb") with apt"
debname=$(basename "$deb")
cp "$deb" "$mnt/tmp/$debname"
chmod 0644 "$mnt/tmp/$debname"
# The stock image ships its package lists, and it already has every dependency, so the
# install needs no download. Only if a dependency is missing are the lists refreshed, and
# then the stock lists are put back so the image matches its base apart from palm-gw.
refreshed=""
if ! in_chroot apt-get install -y "/tmp/$debname"; then
	say "A dependency is missing from the base image: refreshing the package lists"
	refreshed=1
	cp -a "$mnt/var/lib/apt/lists" "$mnt/var/lib/apt/lists.palm-gw-build"
	in_chroot apt-get update -q
	in_chroot apt-get install -y "/tmp/$debname"
fi
# shellcheck disable=SC2016 # dpkg-query's own ${...} fields
in_chroot dpkg-query -W -f='${Package} ${Version} ${Status}\n' palm-gw

# Until the gateway is set up, a login says how.
cat >"$mnt/etc/profile.d/palm-gw-setup.sh" <<'EOF'
# palm-gw image (D-045): a reminder until the gateway is set up.
if [ -x /usr/bin/palm-gw ] && [ ! -e /etc/palm-gw/config.toml ] && [ -t 1 ]; then
	printf '\n  palm-gw is installed but not set up yet. Run:  sudo palm-gw setup\n\n'
fi
EOF
chmod 0644 "$mnt/etc/profile.d/palm-gw-setup.sh"
cat >"$mnt/etc/palm-gw-image" <<EOF
PALM_GW_VERSION=$IMAGE_VERSION
BASE_IMAGE=$(basename "$base")
BUILT=$(date -u +%Y-%m-%dT%H:%M:%SZ)
EOF

say "Cleaning up the chroot"
if [ -n "$refreshed" ]; then
	in_chroot apt-get clean
	rm -r "$mnt/var/lib/apt/lists"
	mv "$mnt/var/lib/apt/lists.palm-gw-build" "$mnt/var/lib/apt/lists"
fi
rm -f "$mnt/tmp/$debname" "$mnt/usr/sbin/policy-rc.d" "$mnt/etc/resolv.conf"
if [ -n "$resolv_saved" ]; then
	mv "$mnt/etc/resolv.conf.palm-gw-build" "$mnt/etc/resolv.conf"
fi
df -h "$mnt" | tail -1

for ((i = ${#mounts[@]} - 1; i >= 0; i--)); do umount "${mounts[i]}"; done
mounts=()
e2fsck -pf "$root_loop" || [ $? -le 1 ] || die "e2fsck failed after the install"
zerofree "$root_loop"
for i in "${loops[@]}"; do losetup -d "$i"; done
loops=()

say "Compressing to $(basename "$out")"
extract_size=$(stat -c %s "$img")
extract_sha=$(sha256sum "$img" | cut -c1-64)
xz -T0 -9 -c "$img" >"$out.part"
mv "$out.part" "$out"
rm -f "$img"
download_size=$(stat -c %s "$out")
download_sha=$(sha256sum "$out" | cut -c1-64)
(cd "$(dirname "$out")" && sha256sum "$(basename "$out")" >"$(basename "$out").sha256")

# A Raspberry Pi Imager repository with this one image (field names, device tags and
# capabilities as in Raspberry Pi's own entry for the base). init_format cloudinit-rpi makes
# Imager offer its customisation; an image opened with "Use custom" gets none (D-046).
url=${IMAGE_URL:-$(basename "$out")}
cat >"${out%.img.xz}.json" <<EOF
{
  "imager": {
    "devices": [
      {
        "name": "Raspberry Pi 5",
        "tags": ["pi5-64bit"],
        "icon": "https://downloads.raspberrypi.com/imager/icons/RPi_5.png",
        "description": "Raspberry Pi 5, 500 / 500+, and Compute Module 5",
        "matching_type": "exclusive",
        "capabilities": []
      },
      {
        "name": "Raspberry Pi 4",
        "tags": ["pi4-64bit"],
        "default": false,
        "icon": "https://downloads.raspberrypi.com/imager/icons/RPi_4.png",
        "description": "Raspberry Pi 4 Model B, 400, and Compute Module 4 / 4S",
        "matching_type": "inclusive",
        "capabilities": []
      }
    ]
  },
  "os_list": [
    {
      "name": "palm-gw $IMAGE_VERSION (Raspberry Pi OS Lite 64-bit)",
      "description": "Bluetooth access point for Palm OS handhelds. Run sudo palm-gw setup after the first login.",
      "icon": "https://downloads.raspberrypi.com/raspios_armhf/Raspberry_Pi_OS_(32-bit).png",
      "url": "$url",
      "extract_size": $extract_size,
      "extract_sha256": "$extract_sha",
      "image_download_size": $download_size,
      "image_download_sha256": "$download_sha",
      "release_date": "$(date -u +%Y-%m-%d)",
      "init_format": "cloudinit-rpi",
      "devices": ["pi5-64bit", "pi4-64bit"],
      "capabilities": ["rpi_connect"]
    }
  ]
}
EOF
say "Done: $out ($download_size bytes; $extract_size unpacked)"
