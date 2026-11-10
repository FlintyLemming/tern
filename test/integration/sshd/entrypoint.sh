#!/bin/sh
set -eu

# Host keys are generated per container: tests learn them by probing.
ssh-keygen -A >/dev/null

# The runner appends public keys here through the shared volume.
touch /keys/authorized_keys
chmod 0755 /keys
chmod 0644 /keys/authorized_keys

chown backup:backup /srv

exec /usr/sbin/sshd -D -e
