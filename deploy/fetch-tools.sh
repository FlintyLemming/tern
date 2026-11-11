#!/bin/sh
# Downloads the pinned restic and rclone builds for $TARGETARCH, verifies
# their SHA256 against the values below, and installs them into <dest>.
# Nothing is installed unless both archives verify. Needs only what busybox
# brings: wget, sha256sum, bunzip2, unzip.
#
# usage: TARGETARCH=amd64|arm64 fetch-tools.sh <dest-dir>
set -eu

RESTIC_VERSION=0.19.1
RCLONE_VERSION=1.76.0

RESTIC_SHA256_amd64=f415415624dcc452f2a02b8c33641791a8c6d6d3b65bbb3543fcf9a25151585c
RESTIC_SHA256_arm64=a5f64aaab53d51e311fa3829124c5b703f2d14cf187d8640b6be3b2b49376465
RCLONE_SHA256_amd64=b00f00b3266c3764413afe599d3ec316f6f43b3e0ea8a0bc1d186d2133e46df7
RCLONE_SHA256_arm64=ee169016675e23a69d77240565133ff7dbc9b33d16fa8f7053d4af39f1eaeb78

die() {
	echo "fetch-tools: $*" >&2
	exit 1
}

[ $# -eq 1 ] || die "usage: TARGETARCH=amd64|arm64 $0 <dest-dir>"
dest=$1

case "${TARGETARCH:-}" in
amd64)
	restic_sha=$RESTIC_SHA256_amd64
	rclone_sha=$RCLONE_SHA256_amd64
	;;
arm64)
	restic_sha=$RESTIC_SHA256_arm64
	rclone_sha=$RCLONE_SHA256_arm64
	;;
*)
	die "TARGETARCH must be amd64 or arm64, got '${TARGETARCH:-}'"
	;;
esac
arch=$TARGETARCH

restic_file=restic_${RESTIC_VERSION}_linux_${arch}.bz2
rclone_file=rclone-v${RCLONE_VERSION}-linux-${arch}.zip
restic_url=https://github.com/restic/restic/releases/download/v${RESTIC_VERSION}/${restic_file}
rclone_url=https://github.com/rclone/rclone/releases/download/v${RCLONE_VERSION}/${rclone_file}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fetch() { # <url> <output file>
	wget -q -O "$2" "$1" || die "download failed: $1"
}

verify() { # <file> <expected sha256>
	actual=$(sha256sum "$1" | cut -d' ' -f1)
	[ "$actual" = "$2" ] || die "SHA256 mismatch for $(basename "$1"): expected $2, got $actual"
}

fetch "$restic_url" "$tmp/$restic_file"
verify "$tmp/$restic_file" "$restic_sha"
fetch "$rclone_url" "$tmp/$rclone_file"
verify "$tmp/$rclone_file" "$rclone_sha"

bunzip2 -c "$tmp/$restic_file" >"$tmp/restic"
unzip -q -j -o "$tmp/$rclone_file" "rclone-v${RCLONE_VERSION}-linux-${arch}/rclone" -d "$tmp"
[ -s "$tmp/restic" ] && [ -s "$tmp/rclone" ] || die "extraction produced an empty binary"

mkdir -p "$dest"
chmod 0755 "$tmp/restic" "$tmp/rclone"
mv "$tmp/restic" "$dest/restic"
mv "$tmp/rclone" "$dest/rclone"
echo "fetch-tools: installed restic $RESTIC_VERSION and rclone $RCLONE_VERSION ($arch) into $dest"
