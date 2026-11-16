# Debian installer → picture hybrid disc

`convert-debian-iso.sh` converts an existing Debian installer ISO into a
*picture hybrid* disc for DiscImageStudio: the installer payload is written
into a UDF filesystem dispersed into the picture (invisible in the mid-tone
and flat areas), with an ISO9660 + Joliet-long bridge carrying the El Torito
boot catalog and the boot-critical files.

## Prerequisites

- `disc-image-studio` (this repo; `go build -o disc-image-studio ./cmd/disc-image-studio`)
- one of `xorriso`, `7z` or `bsdtar` (to unpack the installer ISO)
- `ar` + `tar` (+ `xz`/`zstd`/`gzip` decompressors for the packages' payloads;
  `dpkg-deb` is used instead when available)
- `cpio` (GNU or BusyBox), `bash`, `sed`, `find`
- optional: `qemu-system-x86_64` to boot-test the result

## Usage

```sh
./convert-debian-iso.sh -i cover.png -s preview.png \
    debian-13.x-installer-amd64.iso debian-picture.iso
```

```
Options:
  -i, --image PATH         cover image (default: cover.png next to the script)
  -l, --volume-label LABEL volume label (default: the input ISO's own)
  -t, --total-sectors N    disc sector count (default: 2295104, a 4.7 GB DVD)
      --inner-radius-mm N  inner drawing radius (default: 24)
      --outer-radius-mm N  outer drawing radius (default: 58)
      --pitch-linear X     measured linear pitch coefficient (spiral model)
      --pitch-quadratic X  measured quadratic pitch coefficient
      --pitch-cubic X      measured cubic pitch coefficient
  -s, --simulate PATH      also render a simulated read-back preview PNG
  -- ...                   extra args for solve and simulate
                           (e.g. -- --channel-bit-nm 133.3)
```

## What it does

1. Unpacks the installer ISO (xorriso, else 7z, else bsdtar).
2. Extracts `udf.mod` from the `grub-efi-amd64-bin` package into
   `boot/grub/x86_64-efi/` (GRUB needs it to read UDF).
3. Extracts the `udf-modules` udeb into a temporary initrd overlay, moving
   `/lib` into `/usr/lib` (usrmerge layout of the d-i initrd).
4. Adds the d-i startup hook `usr/lib/debian-installer-startup.d/S40udf`
   (makes `cdrom-detect` mount UDF instead of iso9660).
5. Packs the overlay (`cpio -H newc`, uid:gid 0:0, gzipped).
6. Appends it to `install.amd/gtk/initrd.gz` and `install.amd/initrd.gz`
   (concatenated-member gzip, the standard d-i overlay mechanism).
7. Inserts `insmod udf` into `EFI/debian/grub.cfg` before the real
   `grub.cfg` is sourced.
8. Generates the picture disc: `--filesystem udf-bridge` with the EFI boot
   image (`boot/grub/efi.img`) and the x86 legacy BIOS one
   (`isolinux/isolinux.bin`, BootInfoTable patched in), exposing the
   boot-critical paths on the ISO9660 side.
9. Optionally renders the simulated read-back preview.

`cover.png` here is a generated placeholder — drop your own cover over it.

Note: the `.disk` marker directory keeps its exact name on the disc (the
Joliet-long tree does not normalize `.disk` to `disk`).
