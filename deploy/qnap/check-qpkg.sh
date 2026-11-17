#!/bin/sh
# Checks a built QNAP package from the outside: the version in its
# configuration and the one tern reports, and that each bundled tool starts.
# Run it on a Linux machine of the package's architecture (x86_64 or
# aarch64); a glibc system also shows that rsync and ssh need no loader.
# The release workflow runs it on every package before publishing it.
#
#	deploy/qnap/check-qpkg.sh <package> <version>
#
# <version> is the git tag the package was built from, e.g. v0.2.0.
set -eu

die() {
	echo "check-qpkg: $*" >&2
	exit 1
}

[ $# -eq 2 ] || die "usage: $0 <package> <version>"
pkg=$1
want=$2
[ -f "$pkg" ] || die "no such file: $pkg"

case $(basename "$pkg") in
*_x86_64.qpkg) arch=x86_64 ;;
*_arm_64.qpkg) arch=aarch64 ;;
*) die "$(basename "$pkg"): not a tern_<version>_<arch>.qpkg name" ;;
esac
[ "$(uname -s)" = Linux ] && [ "$(uname -m)" = "$arch" ] ||
	die "$(basename "$pkg") runs on Linux $arch, this is $(uname -s) $(uname -m)"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir "$tmp/control" "$tmp/data"

# QDK's layout: the installer script (script_len bytes), a 20 KiB tar that
# holds control.tar.gz, then data.tar.gz. The script names both sizes.
number() { # <grep pattern ending in the number>
	head -c 65536 "$pkg" | grep -a -m1 -o "$1" | grep -o '[0-9]*$' || true
}
script_len=$(number '^script_len=[0-9]*')
data_len=$(number 'f\.seek([0-9]*')
[ -n "$script_len" ] && [ -n "$data_len" ] || die "$(basename "$pkg"): not a QDK package (sizes not found)"

tail -c +$((script_len + 1)) "$pkg" | head -c 20480 | tar -xO | tar -xz -C "$tmp/control" ||
	die "cannot unpack the control archive"
tail -c +$((script_len + 20480 + 1)) "$pkg" | head -c "$data_len" | tar -xz -C "$tmp/data" ||
	die "cannot unpack the data archive"

failed=0
check() { # <what> <got> <want>
	if [ "$2" = "$3" ]; then
		echo "ok   $1: $2"
	else
		echo "FAIL $1: got '$2', want '$3'"
		failed=1
	fi
}

check "QPKG_VER" "$(sed -n 's/^QPKG_VER="\(.*\)"$/\1/p' "$tmp/control/qpkg.cfg")" "${want#v}"
check "tern --version" "$("$tmp/data/bin/tern" --version 2>&1)" "tern $want"

for tool in "restic version" "rclone version" "rsync --version" "ssh -V"; do
	# shellcheck disable=SC2086 # split into command and argument
	set -- $tool
	if out=$("$tmp/data/bin/$1" "$2" 2>&1); then
		echo "ok   $1: $(echo "$out" | head -1)"
	else
		echo "FAIL $1 does not start: $(echo "$out" | head -3)"
		failed=1
	fi
done

[ "$failed" = 0 ] || die "$(basename "$pkg") failed the checks above"
