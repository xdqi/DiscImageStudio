#!/usr/bin/env bash
# convert-debian-iso.sh — turn a Debian installer ISO into a picture hybrid
# disc: the installer payload lives in a UDF filesystem dispersed into the
# picture, with an ISO9660 + Joliet-long bridge carrying the El Torito boot
# catalog (x86 legacy BIOS + EFI entries) and the boot-critical files.
#
# Usage:
#   convert-debian-iso.sh [OPTIONS] INPUT.iso OUTPUT.iso
#
# Options:
#   -i, --image PATH         cover image (default: cover.png next to this script)
#   -l, --volume-label LABEL volume label (default: the input ISO's own label)
#   -t, --total-sectors N    disc sector count (default: 2295104, a 4.7 GB DVD)
#       --inner-radius-mm N  inner drawing radius (default: 24)
#       --outer-radius-mm N  outer drawing radius (default: 58)
#       --pitch-linear X     measured linear pitch coefficient (spiral model)
#       --pitch-quadratic X  measured quadratic pitch coefficient
#       --pitch-cubic X      measured cubic pitch coefficient
#   -s, --simulate PATH      also render a simulated read-back preview PNG
#   -- ...                   extra arguments forwarded to solve and simulate
#                            (e.g. -- --channel-bit-nm 133.3)
#
# Environment:
#   DISC_IMAGE_STUDIO  path of the disc-image-studio binary
#                      (default: ./disc-image-studio, then the script's dir).
set -euo pipefail

die() { echo "convert-debian-iso.sh: error: $*" >&2; exit 1; }

# ---------- argument parsing ----------
SELF_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
IMAGE="$SELF_DIR/cover.png"
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
[ -n "$INPUT_ISO" ] && [ -n "$OUTPUT_ISO" ] || die "usage: convert-debian-iso.sh [OPTIONS] INPUT.iso OUTPUT.iso"
[ -f "$INPUT_ISO" ] || die "input ISO not found: $INPUT_ISO"
[ -f "$IMAGE" ] || die "cover image not found: $IMAGE (use --image)"
PITCH_ARGS+=(--inner-radius-mm "$INNER_MM" --outer-radius-mm "$OUTER_MM")

DIS="${DISC_IMAGE_STUDIO:-}"
if [ -z "$DIS" ]; then
	if [ -x ./disc-image-studio ]; then DIS=./disc-image-studio
	elif [ -x "$SELF_DIR/disc-image-studio" ]; then DIS="$SELF_DIR/disc-image-studio"
	else DIS=$(command -v disc-image-studio || die "disc-image-studio binary not found (set DISC_IMAGE_STUDIO)")
	fi
fi
echo "using disc-image-studio: $DIS"

# Unpack a .deb/.udeb data payload into a directory: dpkg-deb when available,
# else ar + tar (data.tar.gz/.xz/.zst/.tar payloads).
unpack_deb() { # $1 = package file, $2 = output directory
	local pkg=$1 out=$2 member
	mkdir -p "$out"
	if command -v dpkg-deb >/dev/null 2>&1; then
		dpkg-deb -x "$pkg" "$out"
		return
	fi
	member=$(ar t "$pkg" | grep '^data\.tar' | head -n 1)
	[ -n "$member" ] || die "no data.tar payload in $pkg"
	case "$member" in
		data.tar.gz|data.tar.tgz) ar p "$pkg" "$member" | gzip -dc | tar -x -C "$out";;
		data.tar.xz)
			command -v xz >/dev/null 2>&1 || die "$pkg is xz-compressed; please install xz"
			ar p "$pkg" "$member" | xz -dc | tar -x -C "$out";;
		data.tar.zst)
			command -v zstd >/dev/null 2>&1 || die "$pkg is zstd-compressed; please install zstd"
			ar p "$pkg" "$member" | zstd -dc | tar -x -C "$out";;
		data.tar) ar p "$pkg" "$member" | tar -x -C "$out";;
		*) die "unsupported payload in $pkg: $member";;
	esac
}

WORK=$(mktemp -d /tmp/debian-iso.XXXXXX)
trap 'rm -rf "$WORK"' EXIT
UNPACKED="$WORK/tree"

# ---------- 1. Unpack the iso using xorriso or bsdtar, whichever is available ----------
# (7z is deliberately not an option: its UDF parser rejects symlinks with
# "Unsupported feature", which would break the round-trip check on the very
# images this script produces.)
echo "unpacking $INPUT_ISO ..."
mkdir -p "$UNPACKED"
if command -v xorriso >/dev/null 2>&1; then
	xorriso -osirrox on -indev "$INPUT_ISO" -extract / "$UNPACKED" >/dev/null
elif command -v bsdtar >/dev/null 2>&1; then
	bsdtar -xf "$INPUT_ISO" -C "$UNPACKED"
else
	die "need one of xorriso or bsdtar to unpack the ISO"
fi
chmod -R u+w "$UNPACKED" # ISOs unpack read-only; we must append/edit files

# The volume label defaults to the input ISO's own (PVD at sector 16, offset
# 40, 32 bytes, space-padded).
if [ -z "$VOLUME_LABEL" ]; then
	VOLUME_LABEL=$(dd if="$INPUT_ISO" bs=2048 skip=16 count=1 2>/dev/null | dd bs=1 skip=40 count=32 2>/dev/null | sed 's/ *$//')
fi
echo "volume label: $VOLUME_LABEL"

# ---------- 2. Find the grub2 package grub-efi-amd64-bin, and extract the
#    udf.mod file into boot/grub/x86_64-efi/ ----------
echo "extracting udf.mod from grub-efi-amd64-bin ..."
GRUB_DEB=$(find "$UNPACKED/pool/main/g" -name 'grub-efi-amd64-bin_*_amd64.deb' | sort | head -n 1)
[ -n "$GRUB_DEB" ] || die "grub-efi-amd64-bin package not found in the ISO's pool"
GRUB_DATA="$WORK/grub-data"
unpack_deb "$GRUB_DEB" "$GRUB_DATA"
UDF_MOD=$(find "$GRUB_DATA" -name udf.mod -path '*x86_64-efi*' | head -n 1)
[ -n "$UDF_MOD" ] || die "udf.mod not found in the grub-efi-amd64-bin package"
mkdir -p "$UNPACKED/boot/grub/x86_64-efi"
cp "$UDF_MOD" "$UNPACKED/boot/grub/x86_64-efi/udf.mod"

# ---------- 3. Extract the udf-modules udeb into a temporary initrd overlay;
#    remember to move /lib into /usr/lib ----------
echo "extracting the udf-modules udeb into an initrd overlay ..."
UDF_UDEB=$(find "$UNPACKED/pool/main/l" -name 'udf-modules-*_amd64.udeb' | sort | head -n 1)
[ -n "$UDF_UDEB" ] || die "udf-modules udeb not found in the ISO's pool"
OVERLAY="$WORK/initrd-overlay"
unpack_deb "$UDF_UDEB" "$OVERLAY"
if [ -d "$OVERLAY/lib" ]; then
	mkdir -p "$OVERLAY/usr/lib"
	cp -a "$OVERLAY/lib/." "$OVERLAY/usr/lib/"
	rm -rf "$OVERLAY/lib"
fi

# ---------- 4. The d-i startup hook ----------
echo "adding the cdrom-detect udf startup hook ..."
STARTUP_DIR="$OVERLAY/usr/lib/debian-installer-startup.d"
mkdir -p "$STARTUP_DIR"
cat > "$STARTUP_DIR/S40udf" <<'EOF'
#!/bin/sh
sed -i '/CDFS/s/iso9660$/udf/' /var/lib/dpkg/info/cdrom-detect.postinst
EOF
chmod +x "$STARTUP_DIR/S40udf"

# ---------- 5. Pack the initrd overlay (newc cpio, uid:gid 0:0, gzipped) ----
echo "packing the initrd overlay ..."
CPIO_CREATE=--create
CPIO_OWNER="--owner 0:0"
cpio --create --null --help >/dev/null 2>&1 || CPIO_CREATE=-o
cpio --help 2>&1 | grep -q -- '--owner' || CPIO_OWNER="-R 0:0"
( cd "$OVERLAY" && find . -print0 | cpio $CPIO_CREATE -H newc --null $CPIO_OWNER | gzip ) > "$WORK/overlay.cpio.gz"

# ---------- 6. Append the overlay to both installer initrds ----------
for INITRD in "$UNPACKED/install.amd/gtk/initrd.gz" "$UNPACKED/install.amd/initrd.gz"; do
	if [ -f "$INITRD" ]; then
		echo "appending the overlay to ${INITRD#"$UNPACKED"/} ..."
		cat "$WORK/overlay.cpio.gz" >> "$INITRD"
	else
		echo "warning: ${INITRD#"$UNPACKED"/} not found, skipping" >&2
	fi
done

# ---------- 7. EFI/debian/grub.cfg: insmod udf before the grub.cfg source ---
GRUB_CFG="$UNPACKED/EFI/debian/grub.cfg"
[ -f "$GRUB_CFG" ] || die "EFI/debian/grub.cfg not found"
if ! grep -q 'insmod udf' "$GRUB_CFG"; then
	echo "adding 'insmod udf' to EFI/debian/grub.cfg ..."
	sed -i '/source .*grub.cfg/i insmod udf' "$GRUB_CFG"
fi

# ---------- 8. Generate the picture hybrid ISO ----------
# Every exposed path must exist; warn about (and skip) the ones that don't.
EXPOSE_ARGS=()
for p in .disk/info .disk/id isolinux boot/grub/x86_64-efi/udf.mod EFI/debian/grub.cfg install.amd; do
	if [ -e "$UNPACKED/$p" ]; then
		EXPOSE_ARGS+=(--expose "$p")
	else
		echo "warning: $p not found in the unpacked ISO, not exposed" >&2
	fi
done
[ -f "$UNPACKED/boot/grub/efi.img" ] || die "boot/grub/efi.img not found"
[ -f "$UNPACKED/isolinux/isolinux.bin" ] || die "isolinux/isolinux.bin not found"

echo "generating the picture disc image ..."
"$DIS" solve --image "$IMAGE" --iso-output "$OUTPUT_ISO" "${EXTRA_ARGS[@]}" "${PITCH_ARGS[@]}" \
	--data-dir "$UNPACKED" --filesystem udf-bridge \
	--boot-image=boot/grub/efi.img --legacy-boot-image=isolinux/isolinux.bin \
	"${EXPOSE_ARGS[@]}" --volume-label "$VOLUME_LABEL" \
	--total-sectors "$TOTAL_SECTORS"

# ---------- 9. Optional simulated read-back preview ----------
if [ -n "$SIMULATE" ]; then
	echo "rendering the simulated preview to $SIMULATE ..."
	"$DIS" simulate "${PITCH_ARGS[@]}" --iso "$OUTPUT_ISO" --output "$SIMULATE" \
		--total-sectors "$TOTAL_SECTORS" \
		--samples-per-sector 1024
fi

echo "done: $OUTPUT_ISO"
