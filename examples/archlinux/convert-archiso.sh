#!/usr/bin/env bash
# convert-archiso.sh — Arch Linux install ISO -> picture hybrid disc (see
# README.md). On the bridge: what syslinux and GRUB read;
# the hidden ESP goes where older archiso releases kept it.
here=$(dirname -- "$0")
exec "$here/../convert-live-iso.sh" --image "$here/cover.png" --efi-path EFI/archiso/efiboot.img \
	-e boot -e arch/boot -e arch/grubenv -e EFI -e loader -e shellx64.efi "$@"
