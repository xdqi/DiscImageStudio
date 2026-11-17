# Disc Image Studio — Go CLI port

This directory contains a Go rewrite of the .NET `disc-image-studio` CLI from
the original [DiscImageStudio](https://github.com/JiaFeiMiao-K-Cat/DiscImageStudio)
project (`src/DiscImageStudio.Cli` and the engine projects behind it),
providing the same 12 commands with byte-identical output files:

| Module | Commands |
| ------ | -------- |
| CD | `cd-generate`, `cd-preview-warp`, `cd-preview-track` |
| DVD data disc | `solve`, `encode`, `calibrate`, `simulate`, `selftest` |
| Blu-ray BD-R | `bd-info`, `bd-probe`, `bd-encode`, `bd-generate` |
| Calibration | `calibration-target`, `calibrate-ui` |

## New features compared to the original

- **Calibration runs in the browser** (`calibrate-ui`): the photo-based
  calibration is a pure web app (HTML + JS + CSS, no build step, everything
  runs locally in the tab), so it is cross-platform — any OS with a browser,
  no desktop app or .NET runtime needed. See
  [Calibration (browser UI)](#calibration-browser-ui).
- **A picture disc can carry actual data mixed into the image**
  (`solve --filesystem udf|udf-bridge|udf-full`): the files of a `--data-dir` tree are
  dispersed into the picture's gray areas as a UDF filesystem — the burned
  disc shows the image *and* mounts as a normal data disc — optionally with an
  ISO9660/Joliet + El Torito bridge so it also boots (BIOS and EFI). With
  `--filesystem udf-full` the ISO9660 side lists the whole tree too
  (fragmented files as multi-extent records), so ISO9660-only software such as
  the Debian installer reads the disc unmodified (boot such discs directly).
  See [Dispersed hybrid discs (DVD)](#dispersed-hybrid-discs-dvd). For a complete
  worked example — turning a Debian installer ISO into a bootable picture
  disc — see [`examples/debian`](examples/debian).

## Dispersed hybrid discs (DVD)

`solve --data-dir DIR --filesystem udf` embeds a UDF 2.01 filesystem whose file
data is *dispersed into the picture*: the 3-tone conversion renders the source
image as black/white/gray, where gray sectors are real data. Mid-tone areas and
the interiors of large flat areas host the data (scrambled sectors read as
mid-gray to the eye), so the files are invisible in the burned picture.

```sh
./disc-image-studio solve --image pic.png --iso-output disc.iso \
  --total-sectors 2297888 --data-dir ./files --filesystem udf
```

`--filesystem udf-bridge --boot-image efi/efi.img` adds an ISO9660 and
Joliet-long bridge with an El Torito boot catalog. `--boot-image` is the EFI
entry: a FAT disk image containing the EFI executable (e.g.
`\EFI\BOOT\BOOTX64.EFI`). `--legacy-boot-image bios/loader.bin` instead gives
an x86 legacy BIOS entry: no-emulation, a Boot Information Table patched into
bytes 8..55 of its on-disc content, and the load size rounded up to 2 KiB —
the BIOS entry always appears before the EFI one; both may be given. Boot
paths are relative to `--data-dir` and appear at the same path in both
filesystems. An EFI image over 32 MiB is recorded with load size 0, as xorriso does.
Repeat `--expose PATH` to expose other files or directories recursively;
their parent directories are included. Exposed files and boot images occupy
continuous extents shared with UDF. The primary ISO9660 tree carries Rock
Ridge (exact names, permission bits), so readers of the primary tree only,
such as syslinux, find names its normalized d-character identifiers cannot
hold (`archiso_sys-linux.cfg` vs `ARCHISO_SYS_LINUX.CFG`); Joliet-long
preserves names up to 103 UTF-16 code units. Names that cannot be
represented in Joliet are rejected rather than renamed.

### Full ISO9660 tree (`--filesystem udf-full`)

With `udf-bridge` the ISO9660 side lists only the boot images and the
`--expose` paths, so software that reads ISO9660 only — GRUB's built-in
driver, `mount -t iso9660`, debian-installer's `cdrom-detect` — sees just
those files. `udf-full` takes the same options and lists every file,
directory and symlink there as well, with Rock Ridge (exact names, permission bits,
symlinks) and a Joliet-long tree. File data is still stored once: each
ISO9660 file points at the same extents as its UDF file entry, and a file the
planner fragmented becomes a multi-extent file (ECMA-119 file sections, one
directory record per fragment). The tree is laid out right after the UDF
metadata; the planner caps every file at 100 sections (the Linux isofs limit)
and widens the gray band when a file would need more. The volume label is
written verbatim into the ISO9660 descriptors when it is printable ASCII of
at most 32 bytes (lookups such as `root=live:CDLABEL=` compare it exactly);
otherwise it is normalized like with `udf-bridge`.

The sections of a multi-extent file are generally not adjacent on the disc.
ECMA-119 allows that, but every common mastering tool writes them adjacent,
and some readers rely on it (measured with non-adjacent, out-of-order
sections):

| Reader | Non-adjacent multi-extent files |
| ------ | ------------------------------- |
| BIOS / UEFI firmware | not involved: El Torito loads the boot images by LBA |
| Linux kernel (`iso9660`) | read correctly, up to 100 sections per file |
| 7-Zip, xorriso (libisofs) | read correctly |
| GRUB 2.14 | misreads: a read starting inside a section runs past its end |
| syslinux / isolinux | single-extent files only |
| libarchive (bsdtar) | misreads (reads the sections as if adjacent) |
| Windows, macOS | not tested; they normally mount the UDF side of a bridge disc |

So name everything the boot loaders read with `--expose` — with `udf-full`
it only keeps those files contiguous (single-section). For a Debian installer
that is `.disk EFI boot install.amd isolinux`; the rest (`pool/`, `dists/`,
...) is read by the Linux kernel.

**Boot a `udf-full` disc directly.** Burn the image and let the firmware
boot the disc through El Torito. Booting the image through a boot loader
that reads it as an ISO9660 file system — GRUB `loopback`, Ventoy, syslinux
`memdisk` and the like — is not supported, as those readers misread files
split into non-adjacent sections. Nor does the image boot from a USB stick:
it carries no isohybrid MBR/GPT.

Knobs: `--gray-band T1:T2` (manual luminance band; default widens from
120:136 until capacity fits), `--interior-margin`/`--interior-margin-black`
(border width kept at the original color on flat areas), `--data-headroom`
(gray capacity target, default 1.10× the data). The `<iso>.json` sidecar
records the resolved constraint list (`{"role","length","fixedAt"}`) and the
per-file fragment placements.

## Symlinks

`--data-dir` trees may contain symlinks; they are stored as links (not
followed, not rejected):

- **UDF** (BD `--data-dir`, DVD `--filesystem udf|udf-bridge`): UDF file type 12
  with CS0 path components, per ECMA-167 and the Linux `fs/udf` reader.
- **ISO9660** (CD/DVD hybrid `--data-dir`): Rock Ridge `SL` entries (+ `SP`/`ER`
  markers, and `PX` with `S_IFLNK|0777` so readers recognize the type) — only
  when the tree actually contains symlinks, so images without links stay
  byte-identical to earlier versions.

A symlinked top-level `--data-dir` path itself is still rejected (pass the
resolved path).

Every command supports `--help` (alias `-h`), e.g. `disc-image-studio
cd-generate --help`, which prints the command's options with types, defaults,
units and choices. `disc-image-studio help COMMAND` is equivalent.

## Calibration (browser UI)

The GUI's photo-based calibration is ported as a pure browser app (HTML + JS +
CSS, no build step, everything runs locally in the tab):

```sh
./disc-image-studio calibrate-ui          # serves the tool at http://127.0.0.1:8484/
./disc-image-studio calibrate-ui --open   # …and opens it in your browser
```

The tool walks through the same three stages as the desktop app
(coarse wide-band + angle markers → boundary tracing → fine crosshair):
import a photo of the burned disc, mark the physical outer edge and centre
hole to rectify, measure the stage pattern, and it solves the inner/outer
radius (plus the cubic pitch coefficients at the fine stage). When a new disc
must be burned, the page shows the exact CLI command to generate the target:

```sh
./disc-image-studio calibration-target --kind dvd --stage coarse \
  --output calibration-coarse.iso --sectors 2297888 --r0 24 --r1 58
```

`calibration-target` embeds the test patterns (the fine stage uses the exact
reference artwork, byte-identical `target_slim.png`; coarse and boundary are
rendered analytically), so no `--input` PNG is needed.

### Layout

- `internal/caltarget` — target patterns, geometry and the `calibration-target` command
- `internal/webui` — the embedded web server (`calibrate-ui`) and `static/` app
- `internal/webui/static/js` — the browser port of the calibration math
  (track geometry, photo rectifier, ring/boundary/cross/cubic fitters,
  angle-sweep reader)
- `webtests/` — Node-based tests for the browser math (`node --test .`),
  including exact-value parity against the original .NET solvers

## Build

```sh
go build -o disc-image-studio ./cmd/disc-image-studio
```

Go 1.26+; the only external dependencies are `golang.org/x/image` (BMP
decoding) and `golang.org/x/text` (Unicode normalization for filesystem
identifiers).

## Layout

- `cmd/disc-image-studio` — CLI entry (help, dispatch, cancellation, exit codes)
- `cmd/dvdtool` — standalone DVD solver entry (mirrors the DvdImageSolver executable)
- `internal/core` — module contracts, option parsing, tone profile/dither, grayscale palettes
- `internal/trackgeom` — the cubic-pitch spiral geometry (`DvdTrackGeometry` port)
- `internal/raster` — image loading (PNG/JPEG/GIF/BMP) to BGRA32 and PNG writing
- `internal/iso` — ISO9660/Joliet and UDF 2.01 image writers, the
  dispersed-extent UDF variant, and the ISO9660 + El Torito bridge layer
- `internal/dvd` — DVD engine plus the dispersed 3-tone hybrid pipeline
  (tone map, constraint planner, interleaved writer)
- `internal/cd` — CD audio/data engine (CIRC interleave, WAV/CUE, Mode 1 ISO, previews)
- `internal/dvd` — DVD engine (EFM+, Reed–Solomon ECC, dispersion solver, previews, self-tests)
- `internal/dvdmodule` — the DVD CLI module adapter (fixed fast-dispersion options)
- `internal/bluray` — BD-R engine (17PP modulation, LDC/BIS ECC, UDF hybrid images)

## Test

```sh
go test ./...
sh test_cli.sh ./disc-image-studio   # end-to-end smoke test; expects the
                                     # original DiscImageStudio/ checkout as a
                                     # sibling directory (it reuses the .NET
                                     # repo's 256px test fixture)
(cd webtests && for f in test_*.mjs; do node --test "$f"; done)   # browser-math tests
```

`go test` additionally compares outputs byte-for-byte against golden files
produced by the original .NET CLI when `/tmp/opencode/golden` is present.

## Notes on fidelity

- SIMD (`System.Runtime.Intrinsics`) fast paths of the original are ported via
  their documented scalar equivalents; the C# code guarantees the same results.
- Parallel sampling pipelines are kept (goroutines) where the C# code proves
  output order independence.
- PNG preview files may differ in compressed bytes from the .NET build (the
  encoders differ); decoded pixels are identical.
- For bit-exact transcendental functions (`Log`/`Asinh`/`Log1p`) the Blu-ray
  package ports the musl libm implementations .NET calls into on Linux/musl,
  with FMA-contraction barriers matching musl's non-fused build.
