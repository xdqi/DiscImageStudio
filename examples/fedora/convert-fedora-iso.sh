#!/usr/bin/env bash
# convert-fedora-iso.sh — Fedora live ISO -> picture hybrid disc (see
# README.md). On the bridge: what GRUB reads; the hidden ESP
# goes where Kiwi used to put it.
here=$(dirname -- "$0")
exec "$here/../convert-live-iso.sh" --image "$here/cover.png" --efi-path boot/x86_64/efi \
	-e boot -e EFI "$@"
