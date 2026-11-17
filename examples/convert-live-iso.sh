#!/usr/bin/env bash
# convert-live-iso.sh — turn a bootable Linux ISO (x86 BIOS + UEFI El Torito)
# into a picture hybrid disc for DiscImageStudio: the whole medium is
# dispersed into the picture as a UDF filesystem, with an ISO9660 (Rock
# Ridge) + Joliet-long bridge carrying the El Torito boot catalog.
#
# By default (--filesystem udf-bridge) the bridge lists the -e paths: what
# the boot loaders read. An installer that mounts the disc as ISO9660 then
# needs a --prepare step to read UDF instead (Debian, Ubuntu). With
# --udf-full the bridge lists the whole tree and no file is modified, but
# the burned disc must be booted directly: boot loaders reading the image
# itself (GRUB loopback, Ventoy) are not supported.
#
# Taken from the input ISO: the volume label (verbatim), the BIOS boot image
# with its patches (Boot Information Table, GRUB2 boot info) and load size,
# the EFI boot image — extracted to --efi-path when it is a hidden appended
# partition rather than a tree file — and, when present, an implanted media
# check MD5, which the new disc gets anew via implantisomd5.
#
# Usage:
#   convert-live-iso.sh [OPTIONS] INPUT.iso OUTPUT.iso
#
# Options:
#   -i, --image PATH         cover image (required)
#   -e, --expose PATH        keep PATH contiguous: what boot loaders read from the
#                            ISO9660 tree (GRUB and isolinux cannot read files
#                            split into non-adjacent sections); repeatable,
#                            missing paths are skipped
#       --efi-path PATH      tree path for a hidden EFI boot image
#                            (default: EFI/efiboot.img)
#       --prepare SCRIPT     udf-bridge only: run SCRIPT TREE on the unpacked
#                            tree before generating (e.g. let the installer
#                            mount UDF)
#   -f, --udf-full           list the whole tree on ISO9660 (--filesystem
#                            udf-full) and skip --prepare
#   -l, --volume-label LABEL volume label (default: the input ISO's own)
#   -t, --total-sectors N    disc sector count (default: 2295104, a 4.7 GB DVD)
#       --inner-radius-mm N  inner drawing radius (default: 24)
#       --outer-radius-mm N  outer drawing radius (default: 58)
#       --pitch-linear X     measured linear pitch coefficient (spiral model)
#       --pitch-quadratic X  measured quadratic pitch coefficient
#       --pitch-cubic X      measured cubic pitch coefficient
#   -s, --simulate PATH      also render a simulated read-back preview PNG
#   -- ...                   extra arguments forwarded to solve
#                            (e.g. -- --usage-map map.png)
#
# Environment:
#   DISC_IMAGE_STUDIO  path of the disc-image-studio binary
#                      (default: ./disc-image-studio, then the script's dir).
set -euo pipefail

die() { echo "convert-live-iso.sh: error: $*" >&2; exit 1; }

# ---------- argument parsing ----------
SELF_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
IMAGE=""
EXPOSE=()
EFI_PATH=EFI/efiboot.img
PREPARE=""
FILESYSTEM=udf-bridge
VOLUME_LABEL=""
TOTAL_SECTORS=2295104
INNER_MM=24
OUTER_MM=58
SIMULATE=""
EXTRA_ARGS=()
PITCH_ARGS=()
INPUT_ISO=""
OUTPUT_ISO=""
while [ $# -gt 0 ]; do
	case "$1" in
	-i|--image) IMAGE=$2; shift 2;;
	-e|--expose) EXPOSE+=("$2"); shift 2;;
	--efi-path) EFI_PATH=$2; shift 2;;
	--prepare) PREPARE=$2; shift 2;;
	-f|--udf-full) FILESYSTEM=udf-full; shift;;
	-l|--volume-label) VOLUME_LABEL=$2; shift 2;;
	-t|--total-sectors) TOTAL_SECTORS=$2; shift 2;;
	--inner-radius-mm) INNER_MM=$2; shift 2;;
	--outer-radius-mm) OUTER_MM=$2; shift 2;;
	--pitch-linear|--pitch-quadratic|--pitch-cubic) PITCH_ARGS+=("$1" "$2"); shift 2;;
	-s|--simulate) SIMULATE=$2; shift 2;;
	--) shift; EXTRA_ARGS=("$@"); break;;
	-*) die "unknown option $1";;
	*)
		if [ -z "$INPUT_ISO" ]; then INPUT_ISO=$1
		elif [ -z "$OUTPUT_ISO" ]; then OUTPUT_ISO=$1
		else die "unexpected argument $1"; fi
		shift;;
	esac
done
[ -n "$INPUT_ISO" ] && [ -n "$OUTPUT_ISO" ] || die "usage: convert-live-iso.sh [OPTIONS] INPUT.iso OUTPUT.iso"
[ -f "$INPUT_ISO" ] || die "input ISO not found: $INPUT_ISO"
[ -n "$IMAGE" ] && [ -f "$IMAGE" ] || die "cover image not found: ${IMAGE:-(none)} (use --image)"
command -v xorriso >/dev/null 2>&1 || die "xorriso is required"
PITCH_ARGS+=(--inner-radius-mm "$INNER_MM" --outer-radius-mm "$OUTER_MM")
# A media check against an implanted MD5 (e.g. Fedora's default boot entry)
# fails on a disc without one: check for implantisomd5 before the long run.
IMPLANT_MD5=""
if dd if="$INPUT_ISO" bs=1 skip=$((16 * 2048 + 883)) count=512 2>/dev/null | grep -aq "ISO MD5SUM"; then
	command -v implantisomd5 >/dev/null 2>&1 ||
		die "$INPUT_ISO carries an implanted MD5 for its media check; install isomd5sum (implantisomd5)"
	IMPLANT_MD5=yes
fi

DIS="${DISC_IMAGE_STUDIO:-}"
if [ -z "$DIS" ]; then
	if [ -x ./disc-image-studio ]; then DIS=./disc-image-studio
	elif [ -x "$SELF_DIR/disc-image-studio" ]; then DIS="$SELF_DIR/disc-image-studio"
	else DIS=$(command -v disc-image-studio || die "disc-image-studio binary not found (set DISC_IMAGE_STUDIO)")
	fi
fi
echo "using disc-image-studio: $DIS"

WORK=$(mktemp -d /tmp/live-iso.XXXXXX)
# The unpacked tree is read-only (ISO permissions): make it writable before
# removing it.
trap 'chmod -R u+w "$WORK" 2>/dev/null; rm -rf "$WORK"' EXIT
UNPACKED="$WORK/tree"

# ---------- 1. Unpack the ISO ----------
echo "unpacking $INPUT_ISO ..."
mkdir -p "$UNPACKED"
xorriso -osirrox on -indev "$INPUT_ISO" -extract / "$UNPACKED" >/dev/null 2>&1 ||
	die "xorriso could not unpack $INPUT_ISO"
# The label, verbatim (PVD at sector 16, offset 40, 32 bytes, space-padded):
# boot chains may look the medium up by it (root=live:CDLABEL=...).
if [ -z "$VOLUME_LABEL" ]; then
	VOLUME_LABEL=$(dd if="$INPUT_ISO" bs=2048 skip=16 count=1 2>/dev/null | dd bs=1 skip=40 count=32 2>/dev/null | sed 's/ *$//')
fi
echo "volume label: $VOLUME_LABEL"

# ---------- 2. The El Torito boot images ----------
# "El Torito boot img :   N  BIOS|UEFI  y  none  seg  hdpt  Ldsiz  LBA",
# "El Torito img path :   N  /path" and "El Torito img opts :   N  opts...".
REPORT=$(xorriso -indev "$INPUT_ISO" -report_el_torito plain 2>/dev/null)
field() { # $1 = line kind, $2 = entry number, $3 = field number
	printf '%s\n' "$REPORT" | awk -v k="El Torito $1" -v n="$2" -v f="$3" 'index($0, k) == 1 && $6 == n { print $f; exit }'
}
entry_of() { # $1 = BIOS | UEFI -> entry number
	printf '%s\n' "$REPORT" | awk -v p="$1" '/El Torito boot img/ && $7 == p { print $6; exit }'
}
BIOS_ENTRY=$(entry_of BIOS)
EFI_ENTRY=$(entry_of UEFI)
[ -n "$BIOS_ENTRY" ] && [ -n "$EFI_ENTRY" ] || die "$INPUT_ISO lacks an x86 BIOS or a UEFI El Torito entry"
BIOS_IMAGE=$(field "img path" "$BIOS_ENTRY" 7)
BIOS_IMAGE=${BIOS_IMAGE#/}
[ -n "$BIOS_IMAGE" ] && [ -f "$UNPACKED/$BIOS_IMAGE" ] || die "the BIOS boot image is not a file of the ISO tree"
BIOS_ARGS=()
LOAD_SIZE=$(field "boot img" "$BIOS_ENTRY" 12)
[ "${LOAD_SIZE:-0}" -gt 0 ] && BIOS_ARGS+=(--legacy-boot-load-size "$LOAD_SIZE")
if printf '%s\n' "$REPORT" | grep "^El Torito img opts : *$BIOS_ENTRY " | grep -q grub2-boot-info; then
	BIOS_ARGS+=(--grub2-boot-info true)
fi
EFI_IMAGE=$(field "img path" "$EFI_ENTRY" 7)
EFI_IMAGE=${EFI_IMAGE#/}
if [ -z "$EFI_IMAGE" ] || [ ! -f "$UNPACKED/$EFI_IMAGE" ]; then
	# A hidden appended ESP: extract it into the tree at --efi-path.
	echo "extracting the hidden EFI boot image ..."
	xorriso -osirrox on -indev "$INPUT_ISO" -extract_boot_images "$WORK/eltorito/" >/dev/null 2>&1 ||
		die "xorriso could not extract the boot images (needs -extract_boot_images, xorriso >= 1.5.4)"
	EFI_IMAGE=$EFI_PATH
	[ ! -e "$UNPACKED/$EFI_IMAGE" ] || die "$EFI_IMAGE already exists in the tree (use --efi-path)"
	# Make the nearest existing ancestor writable just for this, then restore
	# its mode: the disc records the tree's permission bits.
	ANCESTOR=$(dirname "$UNPACKED/$EFI_IMAGE")
	while [ ! -d "$ANCESTOR" ]; do ANCESTOR=$(dirname "$ANCESTOR"); done
	MODE=$(stat -c %a "$ANCESTOR")
	chmod u+w "$ANCESTOR"
	mkdir -p "$(dirname "$UNPACKED/$EFI_IMAGE")"
	cp "$WORK/eltorito/eltorito_img${EFI_ENTRY}_uefi.img" "$UNPACKED/$EFI_IMAGE"
	chmod 444 "$UNPACKED/$EFI_IMAGE"
	chmod "$MODE" "$ANCESTOR"
fi
echo "BIOS boot image: $BIOS_IMAGE ${BIOS_ARGS[*]}"
echo "EFI boot image:  $EFI_IMAGE"

# ---------- 3. udf-bridge: let the installer read UDF ----------
if [ "$FILESYSTEM" = udf-bridge ] && [ -n "$PREPARE" ]; then
	echo "preparing the tree: $PREPARE ..."
	"$PREPARE" "$UNPACKED" || die "$PREPARE failed"
fi

# ---------- 4. Generate the picture hybrid ISO ----------
EXPOSE_ARGS=()
for p in "${EXPOSE[@]}"; do
	[ -e "$UNPACKED/$p" ] && EXPOSE_ARGS+=(--expose "$p")
done
echo "generating the picture disc image ..."
"$DIS" solve --image "$IMAGE" --iso-output "$OUTPUT_ISO" "${EXTRA_ARGS[@]}" "${PITCH_ARGS[@]}" \
	--data-dir "$UNPACKED" --filesystem "$FILESYSTEM" \
	--boot-image="$EFI_IMAGE" --legacy-boot-image="$BIOS_IMAGE" "${BIOS_ARGS[@]}" \
	"${EXPOSE_ARGS[@]}" --volume-label "$VOLUME_LABEL" \
	--total-sectors "$TOTAL_SECTORS"

# ---------- 5. The media check MD5 (whole volume but its last 15 sectors) ----------
if [ -n "$IMPLANT_MD5" ]; then
	echo "implanting the media check MD5 ..."
	implantisomd5 --force "$OUTPUT_ISO" >/dev/null
fi

# ---------- 6. Optional simulated read-back preview ----------
if [ -n "$SIMULATE" ]; then
	echo "rendering the simulated preview to $SIMULATE ..."
	"$DIS" simulate "${PITCH_ARGS[@]}" --iso "$OUTPUT_ISO" --output "$SIMULATE" \
		--total-sectors "$TOTAL_SECTORS" \
		--samples-per-sector 1024
fi

echo "done: $OUTPUT_ISO"
