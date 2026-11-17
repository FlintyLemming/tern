#!/bin/sh
# Installs a built QNAP package on a NAS over SSH, for testing:
#
#	deploy/qnap/install.sh <ssh-host> [package]
#
# The package defaults to the newest x86_64 one in build/qnap. The SSH user
# needs passwordless sudo on the NAS. qpkg_cli refuses packages without
# QNAP's signature, so the self-extracting package is run as root, which is
# what App Center's manual install does once "allow apps without a valid
# digital signature" is on. Prints the package's entries in qpkg.conf and
# whether tern answers.
set -eu
cd "$(dirname "$0")/../.."

[ $# -ge 1 ] || {
	echo "usage: $0 <ssh-host> [package]" >&2
	exit 1
}
host=$1
pkg=${2:-$(ls -t build/qnap/tern_*_x86_64.qpkg 2>/dev/null | head -1)}
[ -n "$pkg" ] && [ -f "$pkg" ] || {
	echo "no package; build one with: make qpkg QPKG_PLATFORMS=linux/amd64" >&2
	exit 1
}
name=$(basename "$pkg")
stage=/share/Public/.tern-install

ssh "$host" "mkdir -p $stage"
scp -q "$pkg" "$host:$stage/$name"
# shellcheck disable=SC2029 # expanded here on purpose
ssh "$host" "cd $stage && sudo sh ./$name 2>&1 | tail -5; cd / && rm -rf $stage
	echo
	for k in Version Enable Install_Path WebUI Web_Port Use_Proxy Proxy_Path Desktop Win_Width Win_Height Visible; do
		printf '%s = ' \$k; /sbin/getcfg tern \$k -f /etc/config/qpkg.conf
	done
	echo
	grep tern /etc/app_proxy.conf || echo 'no tern entry in /etc/app_proxy.conf'
	sudo /etc/init.d/tern.sh status"
