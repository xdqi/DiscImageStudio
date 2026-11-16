#!/bin/sh
# Byte-compares the Go CLI's outputs against the original .NET CLI for the
# commands whose outputs are deterministic (no embedded wall-clock timestamps
# in compared ranges). PNGs are compared pixel-wise by the Go tests instead.
# Usage: sh compare_cli.sh <go-binary> <dotnet-cli-binary>
set -e
abs() { CDPATH= cd -- "$(dirname -- "$1")" && echo "$(pwd)/$(basename -- "$1")"; }
GO=$(abs "$1")
FX=$(abs "$2")
GOLDEN=/tmp/opencode/golden
WORK=$(mktemp -d /tmp/opencode/cli-compare.XXXXXX)
trap 'rm -rf "$WORK"' EXIT
cd "$WORK"
cp "$GOLDEN/input.png" input.png
mkdir -p data/sub
printf 'hello-world\n' > data/hello.txt
printf 'binary\0data' > data/sub/blob.bin
head -c 65536 /dev/urandom > bd-payload.bin
head -c 32768 /dev/urandom > payload.bin

check() { # name file
	if ! cmp -s "$1" "$2"; then echo "MISMATCH: $1 vs $2"; exit 1; fi
	echo "identical: $1"
}

# cmp_udf_masked compares two images like check(), but ignores the
# uid/gid/permissions fields of UDF file entries (tag 261) and the tag
# checksum/CRC they feed: the Go writers default ownership to root and copy
# modes from the source, while the original .NET CLI writes fixed defaults.
cmp_udf_masked() {
	python3 - "$1" "$2" <<'EOF'
import sys

def masked(path):
    data = bytearray(open(path, "rb").read())
    for lba in range(len(data) // 2048):
        base = lba * 2048
        if int.from_bytes(data[base:base+2], "little") != 261:
            continue
        data[base+4] = 0
        data[base+8] = data[base+9] = 0
        for i in range(base+36, base+48):
            data[i] = 0
    return data

a, b = masked(sys.argv[1]), masked(sys.argv[2])
if len(a) != len(b):
    sys.exit(1)
for i in range(len(a)):
    if a[i] != b[i]:
        print(f"first diff at byte {i} (sector {i//2048} offset {i%2048})", file=sys.stderr)
        sys.exit(1)
sys.exit(0)
EOF
}

# --- CD audio (byte-exact) ---
"$GO" cd-generate --input input.png --output go-track.wav --sectors 32 > /dev/null
"$FX" cd-generate --input input.png --output fx-track.wav --sectors 32 > /dev/null
cmp -s go-track.wav fx-track.wav && echo "identical: cd-generate wav" || { echo "MISMATCH: cd wav"; exit 1; }

"$GO" cd-generate --input input.png --output go-track.raw --sectors 32 --interleave false > /dev/null
"$FX" cd-generate --input input.png --output fx-track.raw --sectors 32 --interleave false > /dev/null
cmp -s go-track.raw fx-track.raw && echo "identical: cd-generate raw" || { echo "MISMATCH: cd raw"; exit 1; }

"$GO" cd-generate --input input.png --output go-t4.wav --sectors 32 --tone-levels 4 --tone-dither 0.75 --pitch-linear 0.2 --pitch-quadratic -0.1 --pitch-cubic 0.05 --theta0 40 > /dev/null
"$FX" cd-generate --input input.png --output fx-t4.wav --sectors 32 --tone-levels 4 --tone-dither 0.75 --pitch-linear 0.2 --pitch-quadratic -0.1 --pitch-cubic 0.05 --theta0 40 > /dev/null
cmp -s go-t4.wav fx-t4.wav && echo "identical: cd-generate tone4+pitch" || { echo "MISMATCH: cd tone4"; exit 1; }

# --- DVD (byte-exact) ---
"$GO" solve --image input.png --iso-output go-dvd.iso --total-sectors 160 --fill-sectors 160 > /dev/null
"$FX" solve --image input.png --iso-output fx-dvd.iso --total-sectors 160 --fill-sectors 160 > /dev/null
cmp -s go-dvd.iso fx-dvd.iso && echo "identical: dvd solve" || { echo "MISMATCH: dvd solve"; exit 1; }

"$GO" encode --input payload.bin --output go-channel.bin > /dev/null
"$FX" encode --input payload.bin --output fx-channel.bin > /dev/null
cmp -s go-channel.bin fx-channel.bin && echo "identical: dvd encode" || { echo "MISMATCH: dvd encode"; exit 1; }

# --- Blu-ray (byte-exact) ---
"$GO" bd-info > go-bd-info.txt
"$FX" bd-info > fx-bd-info.txt
cmp -s go-bd-info.txt fx-bd-info.txt && echo "identical: bd-info" || { echo "MISMATCH: bd-info"; diff go-bd-info.txt fx-bd-info.txt | head; exit 1; }

"$GO" bd-encode --input bd-payload.bin --output go-bd-ecc.bin > /dev/null
"$FX" bd-encode --input bd-payload.bin --output fx-bd-ecc.bin > /dev/null
cmp -s go-bd-ecc.bin fx-bd-ecc.bin && echo "identical: bd-encode" || { echo "MISMATCH: bd-encode"; exit 1; }

"$GO" bd-probe --output go-bd-probe.bin --start-sector 0 --sectors 32 --byte 0x55 > /dev/null
"$FX" bd-probe --output fx-bd-probe.bin --start-sector 0 --sectors 32 --byte 0x55 > /dev/null
cmp -s go-bd-probe.bin fx-bd-probe.bin && echo "identical: bd-probe" || { echo "MISMATCH: bd-probe"; exit 1; }

"$GO" bd-generate --input input.png --output go-bd.iso --total-sectors 1024 > /dev/null
"$FX" bd-generate --input input.png --output fx-bd.iso --total-sectors 1024 > /dev/null
# The UDF file entries differ in uid/gid/permissions by design (root
# ownership, source modes); everything else must be byte-identical.
cmp_udf_masked go-bd.iso fx-bd.iso && echo "identical: bd-generate (uid/gid/mode ignored)" || { echo "MISMATCH: bd-generate"; exit 1; }

echo "ALL CLI COMPARISONS IDENTICAL"
