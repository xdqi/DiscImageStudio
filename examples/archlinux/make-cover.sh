#!/usr/bin/env bash
# make-cover.sh — regenerate cover.png: the official monochrome Arch Linux
# logo (https://archlinux.org/art/) in pure black with a pure white halo, on
# the mid-gray (128) data background, at the size and position of the Debian
# example's logo. The picture has exactly three tones: anti-aliased edges
# would fall into the gray band and become data. At this size the logo's
# two ™ marks shrink to dots, but they stay, as the logo guidelines ask.
#
# Needs curl, rsvg-convert and ImageMagick 7 (magick).
#
# The Arch Linux name and logo are trademarks of the Arch Linux project
# (https://terms.archlinux.org/docs/trademark-policy/).
set -euo pipefail

SELF_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
OUT=${1:-$SELF_DIR/cover.png}
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
cd "$WORK"

curl -fsSL -o logo.svg https://archlinux.org/static/logos/archlinux-logo-black-scalable.svg
rsvg-convert -w 200 logo.svg -o logo.png
magick logo.png -trim +repage -bordercolor none -border 4 -alpha extract -threshold 40% mask.png
magick mask.png -morphology Dilate Disk:2 halo.png
read -r W H < <(magick identify -format '%w %h\n' mask.png)
X=$((862 - W / 2)) Y=$((500 - H / 2)) # right of the hole, like the Debian logo
magick -size 1024x1024 xc:'rgb(128,128,128)' \
	\( -size "${W}x${H}" xc:white halo.png -alpha off -compose CopyOpacity -composite \) \
	-geometry "+$X+$Y" -compose Over -composite \
	\( -size "${W}x${H}" xc:black mask.png -alpha off -compose CopyOpacity -composite \) \
	-geometry "+$X+$Y" -compose Over -composite \
	-alpha off "$OUT"
echo "wrote $OUT"
