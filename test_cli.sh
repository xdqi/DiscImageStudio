#!/bin/sh
# Smoke-test the Go CLI, mirroring tests/test_cli_package.py from the .NET repo.
# Usage: sh test_cli.sh [path-to-disc-image-studio-binary]
set -e
HERE=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
EXE=${1:-$HERE/disc-image-studio}
EXE=$(CDPATH= cd -- "$(dirname -- "$EXE")" && pwd)/$(basename -- "$EXE")
FIXTURE=$HERE/../DiscImageStudio/src/DiscImageStudio.App/Assets/DiscImageStudio-256.png
WORK=$(mktemp -d /tmp/opencode/cli-smoke.XXXXXX)
trap 'rm -rf "$WORK"' EXIT
cp "$FIXTURE" "$WORK/input image.png"
cd "$WORK"

"$EXE" --help > /dev/null
"$EXE" selftest > /dev/null
"$EXE" cd-generate --input "input image.png" --output track.wav --sectors 2 > /dev/null
[ "$(stat -c %s track.wav)" = "$((44 + 2 * 2352))" ] || { echo "track.wav size wrong"; exit 1; }
grep -q 'FILE "track.wav" WAVE' track.cue || { echo "CUE missing WAV reference"; exit 1; }
"$EXE" solve --image "input image.png" --iso-output dvd.iso --total-sectors 160 --fill-sectors 160 > /dev/null
[ "$(stat -c %s dvd.iso)" = "$((160 * 2048))" ] || { echo "dvd.iso size wrong"; exit 1; }
"$EXE" bd-generate --input "input image.png" --output bd.iso --total-sectors 1024 > /dev/null
[ "$(stat -c %s bd.iso)" = "$((1024 * 2048))" ] || { echo "bd.iso size wrong"; exit 1; }
echo "CLI smoke test passed"
