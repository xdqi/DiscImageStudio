#!/usr/bin/env bash
# convert-ubuntu-iso.sh — Ubuntu live ISO -> picture hybrid disc (see
# README.md). On the bridge: what GRUB reads; the hidden ESP goes where
# Ubuntu ISOs up to 21.10 kept it. prepare-udf.sh lets casper mount the disc
# as UDF (not needed with --udf-full).
here=$(dirname -- "$0")
exec "$here/../convert-live-iso.sh" --image "$here/cover.png" --efi-path boot/grub/efi.img \
	--prepare "$here/prepare-udf.sh" -e .disk -e boot -e EFI -e casper/vmlinuz -e casper/initrd "$@"
