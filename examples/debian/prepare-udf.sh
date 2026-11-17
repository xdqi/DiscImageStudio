#!/usr/bin/env bash
# prepare-udf.sh TREE — let debian-installer read a udf-bridge picture disc:
# the bridge's ISO9660 side lists only the boot files, so cdrom-detect must
# mount the disc as UDF. GRUB and isolinux are untouched (they read the
# bridge). Appends to install.amd/initrd.gz and install.amd/gtk/initrd.gz an
# overlay (the standard concatenated-cpio mechanism) holding the udf-modules
# udeb and a d-i startup hook switching cdrom-detect from iso9660 to udf,
# and updates their md5sum.txt lines. The initrd is not covered by Secure
# Boot, so that keeps working.
#
# Needs ar + tar (+ the xz/zstd/gzip decompressors; dpkg-deb is used when
# available), cpio (GNU or BusyBox), md5sum.
set -euo pipefail

die() { echo "prepare-udf.sh: error: $*" >&2; exit 1; }
TREE=${1:?usage: prepare-udf.sh TREE}
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

# Unpack a .deb/.udeb data payload into a directory.
unpack_deb() { # $1 = package file, $2 = output directory
	local pkg=$1 out=$2 member
	mkdir -p "$out"
	if command -v dpkg-deb >/dev/null 2>&1; then
		dpkg-deb -x "$pkg" "$out"
		return
	fi
	member=$(ar t "$pkg" | grep '^data\.tar' | head -n 1)
	case "$member" in
	data.tar.gz) ar p "$pkg" "$member" | gzip -dc | tar -x -C "$out";;
	data.tar.xz) ar p "$pkg" "$member" | xz -dc | tar -x -C "$out";;
	data.tar.zst) ar p "$pkg" "$member" | zstd -dc | tar -x -C "$out";;
	data.tar) ar p "$pkg" "$member" | tar -x -C "$out";;
	*) die "unsupported payload in $pkg: ${member:-none}";;
	esac
}

# The udf-modules udeb, moved from /lib to /usr/lib (usrmerge d-i initrd).
UDEB=$(find "$TREE/pool/main/l" -name 'udf-modules-*_amd64.udeb' | sort | head -n 1)
[ -n "$UDEB" ] || die "udf-modules udeb not found in the pool"
OVERLAY="$WORK/overlay"
unpack_deb "$UDEB" "$OVERLAY"
if [ -d "$OVERLAY/lib" ]; then
	mkdir -p "$OVERLAY/usr/lib"
	cp -a "$OVERLAY/lib/." "$OVERLAY/usr/lib/"
	rm -rf "$OVERLAY/lib"
fi
# The d-i startup hook: cdrom-detect mounts the disc as udf.
mkdir -p "$OVERLAY/usr/lib/debian-installer-startup.d"
cat > "$OVERLAY/usr/lib/debian-installer-startup.d/S40udf" <<'EOF'
#!/bin/sh
sed -i '/CDFS/s/iso9660$/udf/' /var/lib/dpkg/info/cdrom-detect.postinst
EOF
chmod +x "$OVERLAY/usr/lib/debian-installer-startup.d/S40udf"

# newc cpio, uid:gid 0:0, gzipped.
CPIO_OWNER="--owner 0:0"
cpio --help 2>&1 | grep -q -- '--owner' || CPIO_OWNER="-R 0:0"
(cd "$OVERLAY" && find . -print0 | cpio -o -H newc --null $CPIO_OWNER 2>/dev/null | gzip) > "$WORK/overlay.cpio.gz"

# Append to a read-only tree file, keeping its permission bits.
append_to() { # $1 = tree-relative path, $2 = data file
	local f="$TREE/$1" mode
	mode=$(stat -c %a "$f")
	chmod u+w "$f"
	cat "$2" >> "$f"
	chmod "$mode" "$f"
}
# Rewrite a file's md5sum.txt line ("<md5>  ./path").
update_md5() { # $1 = tree-relative path
	local list="$TREE/md5sum.txt" mode sum
	[ -f "$list" ] || return 0
	sum=$(md5sum "$TREE/$1" | cut -d' ' -f1)
	mode=$(stat -c %a "$list")
	chmod u+w "$list"
	awk -v p="./$1" -v s="$sum" '$2 == p { $0 = s "  " p } { print }' "$list" > "$WORK/md5sum.txt"
	cat "$WORK/md5sum.txt" > "$list"
	chmod "$mode" "$list"
}
for initrd in install.amd/initrd.gz install.amd/gtk/initrd.gz; do
	[ -f "$TREE/$initrd" ] || continue
	echo "appending the UDF overlay to $initrd"
	append_to "$initrd" "$WORK/overlay.cpio.gz"
	update_md5 "$initrd"
done
