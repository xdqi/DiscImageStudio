#!/usr/bin/env bash
# convert-debian-iso.sh — Debian installer ISO -> picture hybrid disc (see
# README.md). On the bridge: what isolinux and GRUB read. prepare-udf.sh
# lets debian-installer mount the disc as UDF (not needed with --udf-full).
here=$(dirname -- "$0")
exec "$here/../convert-live-iso.sh" --image "$here/cover.png" --prepare "$here/prepare-udf.sh" \
	-e .disk -e EFI -e boot -e install.amd -e isolinux "$@"
