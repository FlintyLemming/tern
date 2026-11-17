#!/bin/sh
# Builds rsync and the OpenSSH client from pinned, SHA256-checked sources,
# linked statically, and installs them into <dest>. The QNAP package brings
# its own: QTS ships rsync 3.0.7, too old for --info=progress2, and its ssh
# varies with the firmware. The versions are the ones in the image (Alpine
# 3.24); the hashes match Alpine's own recipes for them.
#
# Runs in the Alpine stage of deploy/qnap/Dockerfile, for the architecture
# being built. usage: build-tools.sh <dest-dir>
set -eu

RSYNC_VERSION=3.5.2
OPENSSH_VERSION=10.3p1
RSYNC_SHA256=a10d591825251e070f74c98df3daad195def4ed81b4a2de16169026aea33f6c8
OPENSSH_SHA256=56682a36bb92dcf4b4f016fd8ec8e74059b79a8de25c15d670d731e7d18e45f4

die() {
	echo "build-tools: $*" >&2
	exit 1
}

[ $# -eq 1 ] || die "usage: $0 <dest-dir>"
dest=$1
jobs=$(nproc)

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fetch() { # <url> <expected sha256>
	file=$tmp/$(basename "$1")
	wget -q -O "$file" "$1" || die "download failed: $1"
	actual=$(sha256sum "$file" | cut -d' ' -f1)
	[ "$actual" = "$2" ] || die "SHA256 mismatch for $(basename "$1"): expected $2, got $actual"
	tar -xzf "$file" -C "$tmp"
}

# quiet <log> <command...>: runs the command with its output in <log>, and
# shows the end of it if the command fails.
quiet() {
	log=$1
	shift
	"$@" >"$log" 2>&1 || {
		tail -n 40 "$log" >&2
		die "failed: $*"
	}
}

# static <binary>: no program interpreter means no dynamic loader needed.
static() {
	if readelf -l "$1" | grep -q 'program interpreter'; then
		die "$(basename "$1") is not statically linked"
	fi
}

fetch "https://download.samba.org/pub/rsync/src/rsync-$RSYNC_VERSION.tar.gz" "$RSYNC_SHA256"
fetch "https://cdn.openbsd.org/pub/OpenBSD/OpenSSH/portable/openssh-$OPENSSH_VERSION.tar.gz" "$OPENSSH_SHA256"

# rsync: tern runs it with -aHs and neither ACLs, xattrs nor compression;
# everything optional stays out, popt and zlib come from rsync's own tree.
(
	cd "$tmp/rsync-$RSYNC_VERSION"
	quiet configure.out ./configure --prefix=/usr --disable-md2man --disable-xxhash --disable-zstd --disable-lz4 \
		--disable-openssl --disable-acl-support --disable-xattr-support --disable-idn \
		--with-included-popt --with-included-zlib LDFLAGS=-static
	quiet make.out make -j"$jobs" rsync
	strip rsync
)
rsync_bin=$tmp/rsync-$RSYNC_VERSION/rsync

# ssh only: tern hands it a generated ssh_config (-F), so the system-wide
# configuration paths compiled in do not matter.
(
	cd "$tmp/openssh-$OPENSSH_VERSION"
	quiet configure.out ./configure --prefix=/usr --sysconfdir=/etc/ssh --with-ldflags=-static \
		--without-pam --without-selinux --without-kerberos5 --without-libedit \
		--without-security-key-builtin --without-zlib --disable-lastlog --disable-utmp --disable-wtmp
	quiet make.out make -j"$jobs" ssh
	strip ssh
)
ssh_bin=$tmp/openssh-$OPENSSH_VERSION/ssh

static "$rsync_bin"
static "$ssh_bin"
"$rsync_bin" --version | head -1
"$ssh_bin" -V

mkdir -p "$dest"
install -m 0755 "$rsync_bin" "$dest/rsync"
install -m 0755 "$ssh_bin" "$dest/ssh"
echo "build-tools: installed rsync $RSYNC_VERSION and ssh $OPENSSH_VERSION into $dest"
