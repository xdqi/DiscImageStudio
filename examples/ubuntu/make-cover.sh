#!/usr/bin/env bash
# make-cover.sh — regenerate cover.png: the official Ubuntu logo
# (https://design.ubuntu.com/brand/, ubuntu-logo-2022.svg) as the disc
# renders it, in one color: its dark parts (the orange tag and the dark
# wordmark) pure black, its light part (the Circle of Friends) pure white,
# with a pure white halo, on the mid-gray (128) data background, at the size
# of the Debian example's logo. The picture has exactly three tones:
# anti-aliased edges would fall into the gray band and become data.
#
# The logo sits right below the center hole: every spiral turn crossing a
# picture feature is cut there, and a logo close to the hole cuts the fewest
# turns, leaving one wide plain band for the big squashfs images.
#
# Needs curl, rsvg-convert and ImageMagick 7 (magick).
set -euo pipefail

SELF_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
OUT=${1:-$SELF_DIR/cover.png}
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
cd "$WORK"

curl -fsSL -o logo.svg https://assets.ubuntu.com/v1/ff6a9a38-ubuntu-logo-2022.svg
rsvg-convert -w 200 logo.svg -o logo.png
magick logo.png -trim +repage -bordercolor none -border 4 trimmed.png
magick trimmed.png -alpha extract -threshold 40% shape.png # the whole logo
# Dark = luminance below 160 (the orange tag is ~108, the wordmark ~29).
magick trimmed.png -background white -alpha remove -colorspace Gray -threshold 62.5% -negate dark_any.png
magick shape.png dark_any.png -compose Multiply -composite dark.png
magick shape.png -morphology Dilate Disk:2 halo.png
read -r W H < <(magick identify -format '%w %h\n' shape.png)
X=$((512 - W / 2)) Y=$((512 + 220)) # top edge ~0.9 mm outside the 24 mm hole radius
magick -size 1024x1024 xc:'rgb(128,128,128)' \
	\( -size "${W}x${H}" xc:white halo.png -alpha off -compose CopyOpacity -composite \) \
	-geometry "+$X+$Y" -compose Over -composite \
	\( -size "${W}x${H}" xc:black dark.png -alpha off -compose CopyOpacity -composite \) \
	-geometry "+$X+$Y" -compose Over -composite \
	-alpha off "$OUT"
echo "wrote $OUT"
