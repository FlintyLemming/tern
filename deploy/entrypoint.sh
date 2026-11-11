#!/bin/sh
# Starts tern, as root or, when PUID is set, as that user and PGID.
#
# With PUID set, the data and runtime directories (and the secrets directory,
# which holds the key file the process has to read) are handed to that user
# first. Whether that worked is not judged by chown: the user has to be able
# to write there, or tern does not start at all.
set -eu
umask 077

PUID=${PUID:-0}
PGID=${PGID:-0}
DATA_DIR=${TERN_DATA_DIR:-/data}
SECRETS_DIR=${TERN_SECRETS_DIR:-/secrets}
RUNTIME_DIR=${TERN_RUNTIME_DIR:-/dev/shm/tern}

mkdir -p "$DATA_DIR" "$RUNTIME_DIR"

if [ "$PUID" = 0 ]; then
	exec tern "$@"
fi

# can_write <dir>: the user creates and removes a file there.
can_write() {
	su-exec "$PUID:$PGID" sh -c 'probe="$1/.tern-write-probe" && : >"$probe" && rm -f "$probe"' sh "$1" 2>/dev/null
}

chown -R "$PUID:$PGID" "$DATA_DIR" 2>/dev/null || true
chown "$PUID:$PGID" "$RUNTIME_DIR" 2>/dev/null || true
if [ -d "$SECRETS_DIR" ]; then
	chown -R "$PUID:$PGID" "$SECRETS_DIR" 2>/dev/null || true
fi

for dir in "$DATA_DIR" "$SECRETS_DIR"; do
	[ "$dir" = "$SECRETS_DIR" ] && [ ! -d "$dir" ] && continue
	if ! can_write "$dir"; then
		echo "tern: uid $PUID cannot write $dir" >&2
		exit 1
	fi
done

exec su-exec "$PUID:$PGID" tern "$@"
