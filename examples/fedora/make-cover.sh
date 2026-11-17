#!/usr/bin/env bash
# make-cover.sh — regenerate cover.png: the official one-color (black)
# horizontal Fedora logo from the Fedora brand standards
# (https://docs.fedoraproject.org/en-US/project/brand/), unaltered, in pure
# black with a pure white halo on the mid-gray (128) data background, at the
# size of the Debian example's logo. The picture has exactly three tones:
# anti-aliased edges would fall into the gray band and become data.
#
# The logo sits right below the center hole, not halfway out like the Debian
# and Arch logos: every spiral turn crossing a picture feature is cut there,
# and a logo close to the hole cuts the fewest turns, leaving one wide plain
# band outside it for Fedora's 2.6 GiB LiveOS/squashfs.img.
#
# Needs curl and ImageMagick 7 (magick).
set -euo pipefail

SELF_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
OUT=${1:-$SELF_DIR/cover.png}
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
cd "$WORK"

curl -fsSL -o logo.png https://docs.fedoraproject.org/en-US/project/_images/logo/fedora_one-color-horizontal.png
magick logo.png -trim +repage -resize 200x -bordercolor none -border 4 -alpha extract -threshold 40% mask.png
magick mask.png -morphology Dilate Disk:2 halo.png
read -r W H < <(magick identify -format '%w %h\n' mask.png)
# Horizontally centered; the top edge 8 px (~0.9 mm) outside the 24 mm hole
# radius (212 px on the 1024 px cover).
X=$((512 - W / 2)) Y=$((512 + 220))
magick -size 1024x1024 xc:'rgb(128,128,128)' \
	\( -size "${W}x${H}" xc:white halo.png -alpha off -compose CopyOpacity -composite \) \
	-geometry "+$X+$Y" -compose Over -composite \
	\( -size "${W}x${H}" xc:black mask.png -alpha off -compose CopyOpacity -composite \) \
	-geometry "+$X+$Y" -compose Over -composite \
	-alpha off "$OUT"
echo "wrote $OUT"
