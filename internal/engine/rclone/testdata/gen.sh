#!/bin/sh
# Regenerates the rclone log fixtures with the pinned rclone, local
# directory to local directory (no network). Run in the integration
# runner from the repository root:
#
#   docker compose -f test/integration/compose.yaml run --rm runner \
#     sh internal/engine/rclone/testdata/gen.sh
set -eu

out=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cd "$work"

log="--use-json-log --log-level INFO"

# 1. a mirror dry run: one new file, one unchanged, two extra to delete.
mkdir -p src/sub dst/old
printf 'hello' > src/a.txt
printf 'goodbye' > src/sub/b.txt
printf 'hello' > dst/a.txt
touch -r src/a.txt dst/a.txt
printf 'x' > dst/old/x.txt
printf 'y' > dst/old/y.txt
rclone sync src dst --dry-run $log --stats 2s 2> "$out/dryrun.jsonl"

# 2. a real copy into an empty directory.
mkdir dst2
rclone copy src dst2 $log --stats 1s 2> "$out/copy.jsonl"

# 3. awkward names: a space and CJK, a leading dash, a newline.
mkdir src3 dst3
printf '1' > 'src3/名 字.txt'
printf '2' > 'src3/-dash.txt'
printf '3' > "src3/new
line.txt"
printf '4' > src3/plain.txt
printf 'old' > 'dst3/-old 旧.txt'
rclone sync src3 dst3 --dry-run $log --stats 2s 2> "$out/weird.jsonl"

wc -l "$out"/*.jsonl
