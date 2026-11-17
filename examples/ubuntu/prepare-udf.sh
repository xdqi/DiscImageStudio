#!/usr/bin/env bash
# prepare-udf.sh TREE — let casper read a udf-bridge picture disc: the
# bridge's ISO9660 side lists only the boot files, and casper mounts the
# disc with the type klibc's fstype reports first, iso9660, where it finds
# no squashfs. GRUB is untouched (it reads the bridge). Appends to
# casper/initrd an overlay (the standard concatenated-cpio mechanism) with
# scripts/casper-helpers patched so that get_fstype() reports udf when
# blkid's low-level probe does, and updates its md5sum.txt line. The initrd
# is not covered by Secure Boot, so that keeps working.
#
# Needs GNU grep (-P), zstd/xz/gzip for the initrd's main archive, cpio,
# md5sum.
set -euo pipefail

die() { echo "prepare-udf.sh: error: $*" >&2; exit 1; }
TREE=${1:?usage: prepare-udf.sh TREE}
INITRD=casper/initrd
[ -f "$TREE/$INITRD" ] || die "$INITRD not found"
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

# The main archive follows the uncompressed early cpio archives (microcode):
# it starts at the first compression magic after their last trailer.
TRAILER=$(LC_ALL=C grep -obUa 'TRAILER!!!' "$TREE/$INITRD" | tail -n 1 | cut -d: -f1)
START=$(LC_ALL=C grep -obUaP '\x28\xb5\x2f\xfd|\xfd7zXZ\x00|\x1f\x8b\x08' "$TREE/$INITRD" |
	awk -F: -v t="${TRAILER:--1}" '$1 > t { print $1; exit }')
[ -n "$START" ] || die "no compressed main archive found in $INITRD"
case $(tail -c +$((START + 1)) "$TREE/$INITRD" | head -c 4 | od -An -tx1 | tr -d ' ') in
28b52ffd) DECOMPRESS="zstd -dc";;
fd377a58) DECOMPRESS="xz -dc";;
1f8b08*) DECOMPRESS="gzip -dc";;
*) die "unknown compression of the main archive";;
esac
mkdir -p "$WORK/overlay"
tail -c +$((START + 1)) "$TREE/$INITRD" | $DECOMPRESS 2>/dev/null |
	(cd "$WORK/overlay" && cpio -id --quiet scripts/casper-helpers) || true
HELPERS="$WORK/overlay/scripts/casper-helpers"
[ -f "$HELPERS" ] || die "scripts/casper-helpers not found in $INITRD"

# get_fstype(): prefer UDF when the disc carries it (blkid -p says udf).
sed -i '/^get_fstype() {$/a\
    # A UDF bridge picture disc lists the whole medium only in UDF.\
    if [ "$(/sbin/blkid -p -s TYPE -o value "$1" 2>/dev/null)" = udf ]; then echo udf; return 0; fi' "$HELPERS"
grep -q 'blkid -p -s TYPE' "$HELPERS" || die "get_fstype() not found in scripts/casper-helpers"

CPIO_OWNER="--owner 0:0"
cpio --help 2>&1 | grep -q -- '--owner' || CPIO_OWNER="-R 0:0"
(cd "$WORK/overlay" && find . -print0 | cpio -o -H newc --null $CPIO_OWNER 2>/dev/null | gzip) > "$WORK/overlay.cpio.gz"

# Append to the read-only initrd and rewrite its md5sum.txt line, keeping
# both files' permission bits.
echo "appending the UDF overlay to $INITRD"
MODE=$(stat -c %a "$TREE/$INITRD")
chmod u+w "$TREE/$INITRD"
cat "$WORK/overlay.cpio.gz" >> "$TREE/$INITRD"
chmod "$MODE" "$TREE/$INITRD"
if [ -f "$TREE/md5sum.txt" ]; then
	SUM=$(md5sum "$TREE/$INITRD" | cut -d' ' -f1)
	MODE=$(stat -c %a "$TREE/md5sum.txt")
	chmod u+w "$TREE/md5sum.txt"
	awk -v p="./$INITRD" -v s="$SUM" '$2 == p { $0 = s "  " p } { print }' "$TREE/md5sum.txt" > "$WORK/md5sum.txt"
	cat "$WORK/md5sum.txt" > "$TREE/md5sum.txt"
	chmod "$MODE" "$TREE/md5sum.txt"
fi
