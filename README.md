# Disc Image Studio — Go CLI port

This directory contains a Go rewrite of the .NET `disc-image-studio` CLI
(`src/DiscImageStudio.Cli` and the engine projects behind it), providing the
same 12 commands with byte-identical output files:

| Module | Commands |
| ------ | -------- |
| CD | `cd-generate`, `cd-preview-warp`, `cd-preview-track` |
| DVD data disc | `solve`, `encode`, `calibrate`, `simulate`, `selftest` |
| Blu-ray BD-R | `bd-info`, `bd-probe`, `bd-encode`, `bd-generate` |
| Calibration | `calibration-target`, `calibrate-ui` |

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

`--filesystem udf-bridge --boot-image boot.bin` adds a minimal ISO9660 layer
with an El Torito boot catalog (BIOS, no-emulation) containing only the boot
files; everything else lives in UDF. The boot image is one continuous extent
(position assigned from the picture's gray area); both filesystems share it.

Knobs: `--gray-band T1:T2` (manual luminance band; default widens from
120:136 until capacity fits), `--interior-margin`/`--interior-margin-black`
(border width kept at the original color on flat areas), `--data-headroom`
(gray capacity target, default 1.10× the data). The `<iso>.json` sidecar
records the resolved constraint list (`{"role","length","fixedAt"}`) and the
per-file fragment placements.

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
