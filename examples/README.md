# Examples: bootable Linux ISOs → picture hybrid discs

Each example turns a distribution's bootable ISO into a *picture hybrid*
disc: the whole medium is dispersed into the picture (invisible in the
mid-tone and flat areas) as a UDF filesystem, with an ISO9660 (Rock Ridge) +
Joliet-long bridge carrying the El Torito catalog for x86 BIOS and UEFI. No
boot loader is modified, so signed boot chains keep working under UEFI
Secure Boot.

Two modes:

- **Bridge (default, `--filesystem udf-bridge`).** The bridge lists what the
  boot loaders read (the `-e` paths); everything else is in UDF only. A
  system that finds its root on the disc through UDF needs no change (Arch
  Linux, Fedora). An installer that mounts the disc as ISO9660 gets an initrd
  overlay letting it mount UDF instead (`--prepare`: Debian, Ubuntu); the
  initrd is not covered by Secure Boot.
- **Full tree (`--udf-full`, `--filesystem udf-full`).** The bridge lists the
  whole tree as well (fragmented files as multi-extent records) and no file
  is modified. Boot such a disc directly from the optical drive: booting the
  image through a boot loader that reads it as an ISO9660 file system (GRUB
  `loopback`, Ventoy, syslinux `memdisk`, ...) is not supported.

Neither image has an isohybrid MBR/GPT: they boot from optical discs, not
from USB sticks.

The work is done by [`convert-live-iso.sh`](convert-live-iso.sh); each
example's `convert-*.sh` is a short wrapper passing its cover and the
distribution's specifics:

| Example | BIOS boot | UEFI boot | On the bridge (`-e`) | Hidden ESP placed at (`--efi-path`) | Bridge mode `--prepare` |
|---------|-----------|-----------|----------------------|-------------------------------------|-------------------------|
| [debian](debian) | isolinux | GRUB (ESP is the tree file `boot/grub/efi.img`) | `.disk EFI boot install.amd isolinux` | — | `cdrom-detect` mounts UDF |
| [archlinux](archlinux) | syslinux | systemd-boot | `boot arch/boot arch/grubenv EFI loader shellx64.efi` | `EFI/archiso/efiboot.img` | — |
| [fedora](fedora) | GRUB2 `eltorito.img` | shim + GRUB | `boot EFI` | `boot/x86_64/efi` | — |
| [ubuntu](ubuntu) | GRUB2 `eltorito.img` | shim + GRUB | `.disk boot EFI casper/vmlinuz casper/initrd` | `boot/grub/efi.img` | casper mounts UDF |

## What `convert-live-iso.sh` does

1. Unpacks the ISO with xorriso and reads its volume label, kept verbatim
   (boot chains may look the medium up by it, e.g. `root=live:CDLABEL=`).
2. Reads the El Torito catalog. The BIOS image keeps the original's patches
   — the Boot Information Table and, for GRUB2's `eltorito.img`, the GRUB2
   boot info (`--grub2-boot-info true`) — and load size. The EFI image is
   either a tree file or a hidden appended partition; the latter is
   extracted into the tree at `--efi-path`.
3. In bridge mode, runs the `--prepare` script on the unpacked tree.
4. Generates the picture disc (`solve --filesystem udf-bridge`, or
   `udf-full`) with both boot images and every `-e` path that exists, each
   stored contiguously.
5. If the original carries an implanted media check MD5, implants a new one
   (`implantisomd5 --force`); it covers the whole volume, picture sectors
   included.
6. Optionally renders the simulated read-back preview.

Apart from the `--prepare` changes (an initrd overlay and the matching
`md5sum.txt` lines), the converted disc differs from the original medium
only by the El Torito catalog listed as `/boot.cat`, the BIOS image's
patched bytes (its new LBA), the extracted ESP file (when it was hidden) and
the implanted MD5.

## Prerequisites

- `disc-image-studio` (this repo; `go build -o disc-image-studio ./cmd/disc-image-studio`)
- `xorriso` 1.5.4 or newer (`-extract_boot_images`)
- `implantisomd5` from `isomd5sum`, for ISOs with an implanted MD5 (Fedora's)
- `bash`, `awk`, `sed`, `dd`, GNU `grep` and `stat`, `md5sum`
- for the `--prepare` scripts: `cpio`, `gzip`; Debian's also `ar`, `tar`
  and `xz`/`zstd` (or `dpkg-deb`), Ubuntu's `zstd`
- optional: `qemu-system-x86_64` (+ OVMF) to boot-test the result

## Usage

```sh
./fedora/convert-fedora-iso.sh -s preview.png Fedora-Workstation-Live-45-1.4.x86_64.iso fedora-picture.iso
./ubuntu/convert-ubuntu-iso.sh --udf-full ubuntu-26.04.1-live-server-amd64.iso ubuntu-picture.iso
```

```
Options (the wrappers pass them through):
  -i, --image PATH         cover image (wrappers: their cover.png)
  -e, --expose PATH        put PATH on the bridge, contiguous; repeatable, missing paths are skipped
      --efi-path PATH      tree path for a hidden EFI boot image (default: EFI/efiboot.img)
      --prepare SCRIPT     bridge mode: run SCRIPT TREE on the unpacked tree first
  -f, --udf-full           list the whole tree on the bridge; skips --prepare
  -l, --volume-label LABEL volume label (default: the input ISO's own)
  -t, --total-sectors N    disc sector count (default: 2295104, a 4.7 GB DVD)
      --inner-radius-mm N  inner drawing radius (default: 24)
      --outer-radius-mm N  outer drawing radius (default: 58)
      --pitch-linear X     measured linear pitch coefficient (spiral model)
      --pitch-quadratic X  measured quadratic pitch coefficient
      --pitch-cubic X      measured cubic pitch coefficient
  -s, --simulate PATH      also render a simulated read-back preview PNG
  -- ...                   extra args for solve (e.g. -- --usage-map map.png)
```

## Adding a distribution

Write a wrapper that passes a cover, `-e` for every path the boot loaders
read from the ISO9660 tree (GRUB 2.x and syslinux cannot read files split
into non-adjacent sections; the Linux kernel can), and `--efi-path` if the
ESP is hidden. Then check how the booted system finds and mounts its
medium. blkid reports the disc as UDF, so a system that probes the type
needs `udf.ko` in its initramfs; one that mounts ISO9660 regardless sees
only the bridge in bridge mode and needs a `--prepare` script (or
`--udf-full`).

## Covers

A cover has exactly three tones: mid-gray (128) background, which holds the
data, and pure black and pure white artwork (anti-aliased edges would fall
into the gray band and become data). Every spiral turn that crosses the
artwork is cut there, while the files on the bridge must be contiguous and
(with `--udf-full`) every file within 100 sections (the Linux isofs limit):
keep artwork radially compact. For discs with gigabyte-sized images (Fedora,
Ubuntu) the logo sits right below the center hole, which cuts the fewest
turns.
