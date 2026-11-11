#!/bin/sh
# Regenerates the rsync output fixtures with the rsync of the integration
# runner, local directory to local directory (no network), using the same
# three directory sets as ../../rclone/testdata/gen.sh. Run from the
# repository root:
#
#   docker compose -f test/integration/compose.yaml run --rm runner \
#     sh internal/engine/rsync/testdata/gen.sh
set -eu

out=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cd "$work"

# 1. a mirror dry run: one new file, one unchanged, two extra to delete.
mkdir -p src/sub dst/old
printf 'hello' > src/a.txt
printf 'goodbye' > src/sub/b.txt
printf 'hello' > dst/a.txt
touch -r src/a.txt dst/a.txt
printf 'x' > dst/old/x.txt
printf 'y' > dst/old/y.txt
rsync -aH --delete --dry-run --stats '--out-format=%i %n%L' src/ dst/ > "$out/dryrun.txt"

# 2. a real copy into an empty directory.
mkdir dst2
rsync -aH --no-inc-recursive --info=progress2 --stats '--out-format=%i %n%L' src/ dst2/ > "$out/copy.txt"

# 3. awkward names: a space and CJK, a leading dash, a newline.
mkdir src3 dst3
printf '1' > 'src3/名 字.txt'
printf '2' > 'src3/-dash.txt'
printf '3' > "src3/new
line.txt"
printf '4' > src3/plain.txt
printf 'old' > 'dst3/-old 旧.txt'
rsync -aH --delete --dry-run --stats '--out-format=%i %n%L' src3/ dst3/ > "$out/weird.txt"

wc -l "$out"/*.txt
