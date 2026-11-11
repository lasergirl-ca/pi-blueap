#!/bin/bash
# palm-gw bootstrap installer (PLAN.md section 8.11, Path A):
#
#   curl -fsSL https://github.com/lasergirl-ca/pi-blueap/releases/latest/download/install.sh | sudo bash
#
# Checks that this is a Raspberry Pi running a 64-bit Raspberry Pi OS (Bookworm or
# later), downloads the release's .deb and SHA256SUMS, checks the sum, installs the
# package with apt (which pulls in bluez, ppp, dnsmasq-base, nftables and
# NetworkManager), then runs the setup wizard on your terminal.
#
# Environment (put it after sudo: curl ... | sudo PALM_GW_UNATTENDED=1 ... bash):
#   PALM_GW_VERSION=v1.2.0   install that release instead of this script's own
#   PALM_GW_UNATTENDED=1     no questions: answers come from PALM_GW_UPLINK,
#                            PALM_GW_ADAPTER, PALM_GW_NAME, PALM_GW_WEB_PASSWORD, ...
#                            (see `palm-gw setup --help`)
#   PALM_GW_BASE_URL=URL     fetch from this release directory (https:// or file://)
#                            instead of GitHub, e.g. a mirror or a local test build
# Flags (curl ... | sudo bash -s -- --verify-sig):
#   --verify-sig             also check the release's minisign signature (needs
#                            minisign; fails if the release is not signed)
#
# The whole script is one function, called on the last line, so a download cut short
# runs nothing.
set -euo pipefail

# make release replaces these.
RELEASE_TAG="@RELEASE_TAG@"
REPO="lasergirl-ca/pi-blueap"
# minisign public key for --verify-sig; empty until the project signs releases.
MINISIGN_PUBKEY=""

die() {
	printf 'palm-gw install: %s\n' "$*" >&2
	exit 1
}
say() { printf '==> %s\n' "$*"; }

check_host() {
	[ "$(id -u)" -eq 0 ] || die "run me as root: curl -fsSL <url> | sudo bash"
	[ "$(uname -s)" = Linux ] || die "palm-gw runs on Linux only (this is $(uname -s))."
	[ "$(uname -m)" = aarch64 ] || die "palm-gw needs a 64-bit ARM system (this is $(uname -m)). Flash Raspberry Pi OS Lite (64-bit)."
	command -v dpkg >/dev/null || die "dpkg not found: palm-gw needs Raspberry Pi OS or Debian."
	local arch
	arch=$(dpkg --print-architecture)
	[ "$arch" = arm64 ] || die "the system's packages are $arch, not arm64: this is a 32-bit OS on a 64-bit kernel. Flash Raspberry Pi OS Lite (64-bit)."

	local model=""
	[ -r /proc/device-tree/model ] && model=$(tr -d '\0' </proc/device-tree/model)
	case "$model" in
	# "Raspberry Pi 4"* also covers the Pi 400.
	"Raspberry Pi 4"* | "Raspberry Pi 5"* | "Raspberry Pi Compute Module 4"* | "Raspberry Pi Compute Module 5"*) ;;
	"Raspberry Pi"*) die "this is a $model; palm-gw supports the Raspberry Pi 4 and 5." ;;
	"") die "this is not a Raspberry Pi (no /proc/device-tree/model)." ;;
	*) die "this is a $model, not a Raspberry Pi." ;;
	esac

	command -v apt-get >/dev/null || die "apt-get not found: palm-gw needs Raspberry Pi OS or Debian."
	[ -d /run/systemd/system ] || die "systemd is not running: palm-gw needs it."
	command -v curl >/dev/null || die "curl not found: sudo apt-get install curl"
	[ -f /boot/firmware/config.txt ] || die "no /boot/firmware/config.txt: this looks like Bullseye or older. palm-gw needs Raspberry Pi OS Bookworm or later."
	local ver="" name=""
	if [ -r /etc/os-release ]; then
		# shellcheck source=/dev/null
		ver=$(. /etc/os-release && echo "${VERSION_ID:-}")
		# shellcheck source=/dev/null
		name=$(. /etc/os-release && echo "${PRETTY_NAME:-}")
	fi
	case "$ver" in
	"" | *[!0-9]*) ;; # testing/sid: let apt's dependencies decide
	*) [ "$ver" -ge 12 ] || die "this is Debian $ver; palm-gw needs Bookworm (12) or later." ;;
	esac
	say "$model, ${name:-Debian $ver}"
}

release_url() {
	if [ -n "${PALM_GW_BASE_URL:-}" ]; then
		echo "${PALM_GW_BASE_URL%/}"
		return
	fi
	local tag="${PALM_GW_VERSION:-}"
	if [ -z "$tag" ]; then
		case "$RELEASE_TAG" in
		@*) tag="" ;; # run from a checkout: take the latest release
		*) tag="$RELEASE_TAG" ;;
		esac
	fi
	if [ -z "$tag" ]; then
		echo "https://github.com/$REPO/releases/latest/download"
		return
	fi
	case "$tag" in
	v[0-9]*) ;;
	*) die "PALM_GW_VERSION must be a release tag such as v1.2.0 (got '$tag')." ;;
	esac
	echo "https://github.com/$REPO/releases/download/$tag"
}

fetch() { # url dest
	curl -fsSL --retry 3 --proto '=https,file' -o "$2" "$1" || die "could not download $1"
}

main() {
	local verify_sig=0
	while [ $# -gt 0 ]; do
		case "$1" in
		--verify-sig) verify_sig=1 ;;
		*) die "unknown option $1 (the only option is --verify-sig)" ;;
		esac
		shift
	done

	check_host

	local base tmp
	base=$(release_url)
	# apt reads a local .deb as the _apt user where it can: keep it world-readable.
	tmp=$(mktemp -d /tmp/palm-gw-install.XXXXXX)
	chmod 0755 "$tmp"
	# shellcheck disable=SC2064 # expand $tmp now
	trap "rm -rf '$tmp'" EXIT

	say "Downloading the release from $base/"
	fetch "$base/SHA256SUMS" "$tmp/SHA256SUMS"
	local deb
	deb=$(awk '$2 ~ /^\*?palm-gw_[A-Za-z0-9.+~-]+_arm64\.deb$/ { sub(/^\*/, "", $2); print $2 }' "$tmp/SHA256SUMS")
	[ -n "$deb" ] || die "SHA256SUMS lists no palm-gw arm64 package."
	[ "$(printf '%s\n' "$deb" | wc -l)" -eq 1 ] || die "SHA256SUMS lists more than one package."
	if [ "$verify_sig" = 1 ]; then
		[ -n "$MINISIGN_PUBKEY" ] || die "--verify-sig: this release is not signed (no public key in this script)."
		command -v minisign >/dev/null || die "--verify-sig needs minisign: sudo apt-get install minisign"
		fetch "$base/SHA256SUMS.minisig" "$tmp/SHA256SUMS.minisig"
		minisign -Vm "$tmp/SHA256SUMS" -P "$MINISIGN_PUBKEY" >/dev/null || die "SHA256SUMS: bad signature."
		say "SHA256SUMS: signature OK"
	fi
	fetch "$base/$deb" "$tmp/$deb"
	(cd "$tmp" && awk -v d="$deb" '{ n = $2; sub(/^\*/, "", n) } n == d' SHA256SUMS | sha256sum -c --strict --status) ||
		die "$deb does not match SHA256SUMS; not installing it."
	chmod 0644 "$tmp/$deb"
	say "$deb: checksum OK"

	local had_config=0
	[ -f /etc/palm-gw/config.toml ] && had_config=1

	say "Installing $deb and its dependencies (apt-get)"
	export DEBIAN_FRONTEND=noninteractive
	# stdin from /dev/null: under curl | bash it is the rest of this script.
	apt-get update -q </dev/null || die "apt-get update failed."
	apt-get install -y "$tmp/$deb" </dev/null || die "apt-get install failed."

	if [ "$had_config" = 1 ]; then
		# The package's postinst has re-applied the existing config.
		say "palm-gw is upgraded and running with your existing configuration."
		echo "    To change it: sudo palm-gw setup"
		return 0
	fi
	if [ "${PALM_GW_UNATTENDED:-}" = 1 ]; then
		say "Running palm-gw setup (unattended)"
		palm-gw setup --yes </dev/null
		return
	fi
	# Under curl | sudo bash, stdin is the script: the wizard reads the terminal.
	if [ -t 0 ]; then
		say "Starting the setup wizard"
		palm-gw setup
	elif (: </dev/tty) 2>/dev/null; then
		say "Starting the setup wizard"
		palm-gw setup </dev/tty
	else
		say "palm-gw is installed. No terminal to ask questions on, so finish with:"
		echo "    sudo palm-gw setup"
	fi
}

main "$@"
