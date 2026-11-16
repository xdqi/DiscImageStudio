package dvd

import "fmt"

// runSelfTests ports Program.RunSelfTests.
func runSelfTests() error {
	return selfTestsRun()
}

// printHelp ports Program.PrintHelp (raw string content ends without a
// trailing newline; Console.WriteLine appends one).
func printHelp() {
	fmt.Println(`DVD NRZI image constraint solver

Solve one 16-sector ECC Block:
  DvdImageSolver solve (--target TARGET.txt | --image IMAGE.png)
    (--output payload.bin | --iso-output image.iso) [options]

Encode one 32768-byte payload block:
  DvdImageSolver encode --input payload.bin --output channel.bin [options]

Render a radius-calibration preview:
  DvdImageSolver calibrate --image IMAGE.png --output PREVIEW.png [options]

Simulate the read-back of a generated ISO:
  DvdImageSolver simulate --iso IMAGE.iso --output PREVIEW.png [options]

Common options:
  --lba N                   Initial LBA only; following 15 sectors are consecutive (default 0)
  --psn-offset N            Decimal or 0x-prefixed PSN offset (default 0x30000)
  --initial-state 1..4      Initial EFMPlus state (default 1)
  --initial-dsv N           Initial DSV (default 0)
  --initial-level pit|land Initial physical NRZI level (default land)

Solve options:
  --image FILE              Source image mapped onto the disc (e.g. PNG or JPEG)
  --total-sectors N         Reported count of 2048-byte sectors (required with --image)
  --total-blocks N          Alias for --total-sectors
  --inner-radius-mm N       Track start radius (default 24.0)
  --outer-radius-mm N       Track end radius (default 58.0)
  --pitch-linear N          Linear radial pitch coefficient (default 0)
  --pitch-quadratic N       Quadratic radial pitch coefficient (default 0)
  --pitch-cubic N           Cubic radial pitch coefficient (default 0)
                            pitch(r)=p0*(1+a1*x+a2*x^2+a3*x^3),
                            x=(r-inner)/(outer-inner); p0 follows total track length
  --channel-bit-nm N        Physical channel-bit length (default 133.3)
  --start-angle-deg N       Track start angle (default 0)
  --spiral-direction ccw|cw Polar direction (default cw)
  --image-threshold 0..255  Binary full/state-control threshold (default 128)
  --tone-dither 0..1        Mix black/white textures per sample; 0 is the hard threshold
  --tone-levels 2           DVD dispersion has two textures; only 2 is accepted
  --tone-brightness N       Shift the tone ramp by -64..64 (default 0)
  --tone-gamma true|false   Decode sRGB before dithering (default false)
  --alpha-threshold 1..255  Lower alpha is unconstrained (default 1)
  --constraint-output FILE  Save the mapped 619008-symbol target
  --constraint-step N       Constrain every Nth channel bit (single block default 1)
  --fill-sectors N          Fill N consecutive sectors; N must be a multiple of 16
  --data-dir DIR            Hybrid disc: store this directory together with the drawing
  --file-position MODE      inner (default) or outer; outer requires --data-dir
  --drawing-start-lba N     Hybrid override; must follow all files and align to an ECC Block
  --fast-output true|false Direct dispersion ISO output without ECC/EFM replay
                            (default false; true in hybrid mode)
  --fast-parallelism 0..64 Fast-output workers; 0 selects processor count (default 0)
  --read-compatibility true|false
                            Experimental periodic 0x9B protection for fast dispersion (default false)
  --iterations-per-block N  Full/state-control passes; dispersion ignores search (defaults 100/3/1)
  --iso-output FILE         Write a directly burnable 2048-byte/sector ISO image
  --usage-map FILE          Also write a PNG map of sector usage: data sectors #FF00FF,
                            picture sectors in their drawn tone (black/white, or gray
                            inside the dispersed gray band), unoccupied sectors #00FFFF
  --iso-sectors N           Sector count for a new minimal ISO (image mode reuses total sectors)
  --iso-template FILE       Copy an existing ISO and replace sectors LBA..LBA+15
  --volume-label TEXT       Label for a new minimal ISO (default DVD_IMAGE)
  --target-sector 0..15     Placement for a one-sector target (default 0)
  --algorithm full|state-control|dispersion
                            Bit search, state control, or black/white dispersion texture pool
  --iterations N            Full/state-control passes; dispersion ignores search (defaults 1000/3/1)
  --mutation-bytes N        Bytes changed per full-mode proposal (default 4)
  --bridge-bytes N          Payload bridge bytes at each free-frame edge (default 2)
  --beam-width N            Base EFMPlus prefix paths per Sync Frame (default 8)
  --retry-beam-width N      Beam for failed single-frame retries (default 32; 0 disables)
  --retry-frame-fraction N  Worst-frame fraction attempted per pass; 0 means all (default 0.25)
  --batch-frames N          Frames per ECC-closed candidate batch (default 8)
  --batch-candidates N      Fully rebuilt candidates per batch (default 4)
  --search-lanes 1..8       Deterministic independent restarts; exact best wins (default 1)
  --seed N                  Deterministic random seed (default 1)
  --initial random|zero     Initial payload mode (default random)
  --seed-payload FILE       Initial 32768-byte payload
  --channel-output FILE     Also write packed final NRZI levels

Target format:
  38688 symbols for one sector or 619008 for a full ECC Block.
  0/P = pit/black, 1/L = land/white, ? = unconstrained; whitespace is ignored.

State-control chooses payload bytes directly from image-ranked EFMPlus code words and
keeps a beam of exact dual-stream prefix states. Search is driven by controllable payload
words; the reported majority/gray scores cover every 16-bit channel word, including
fixed/EDC/PI/PO and both SYNC words. Stream, DSV and final boundaries remain exact.

Dispersion maps luminance bins 0..127 and 128..255 to scrambled
EFMPlus input bytes 0xA5 and 0x92 (5 and 2 transitions), then
reverses DVD payload scrambling for ISO output. Nominal preview levels are 0 and 255.
It samples every payload code-word center regardless of --constraint-step and
--image-threshold. These values are not calibrated optical luminance. Opaque
black/white images use nearest-neighbor sampling to preserve their dither pattern.
Other images retain alpha-weighted bilinear sampling. Q=0x66 and 0xB9 are not
generated; old ISOs retain readback of 66 as black and B9 as nominal dark gray 85.
With --fast-output true, final ECC/EFM boundary and DSV verification are skipped;
the JSON finalBoundary is therefore null. --read-compatibility true optionally replaces
eligible white drawing bytes with 0x9B at a 16-byte interval to interrupt long 0x92 runs. It may
improve readback on affected drives/media; it is experimental and defaults off.
Full/state-control output and exact scrambled-byte test patterns do not accept this option.

Hybrid --data-dir mode builds a mountable ISO9660/Joliet filesystem. Files default to the inner ring;
--file-position outer reserves the outer ring for files and draws between front metadata and files.
Drawing boundaries align to ECC Blocks and never overwrite the filesystem.
It defaults to dispersion plus fast output. --lba and --fill-sectors are
automatic in this mode; use --drawing-start-lba to leave additional inner-ring space.

Calibration options:
  --total-sectors N
  --generated-inner-radius-mm N
  --generated-outer-radius-mm N
  --actual-inner-radius-mm N
  --actual-outer-radius-mm N
  --pitch-linear N          Generated linear pitch coefficient (default 0)
  --pitch-quadratic N       Generated quadratic pitch coefficient (default 0)
  --pitch-cubic N           Generated cubic pitch coefficient (default 0)
                            --generated-pitch-* aliases are also accepted
  --actual-pitch-linear N   Actual linear pitch coefficient (default 0)
  --actual-pitch-quadratic N Actual quadratic pitch coefficient (default 0)
  --actual-pitch-cubic N    Actual cubic pitch coefficient (default 0)
  --channel-bit-nm N        Default 133.3
  --spiral-direction ccw|cw Default cw; no rotation-offset calibration
  --image-threshold 0..255  Accepted for compatibility; black/white preview uses 128
  --alpha-threshold 1..255  Default 1
  --preview-size 64..8192   Square PNG size (default 1024)
  --samples-per-sector 1..4096 Forward geometry samples per sector (default 16)
  --lba N                   First visible sector (default 0)
  --fill-sectors N          Visible sector count (default: remainder of disc)

Simulation options (source is the generated ISO, not an image):
  --iso FILE                Generated ISO image (required)
  --inner-radius-mm N       Measured inner radius of the burned spiral
  --outer-radius-mm N       Measured outer radius of the burned spiral
  --pitch-linear N          Measured linear pitch coefficient (default 0)
  --pitch-quadratic N       Measured quadratic pitch coefficient (default 0)
  --pitch-cubic N           Measured cubic pitch coefficient (default 0)
  --total-sectors N         Disc sector count; defaults to the solve sidecar
                            JSON (IMAGE.iso.json) or the ISO file size
  --psn-offset N            Decimal or 0x-prefixed PSN offset (default 0x30000)
  --lba N                   First visible sector; defaults to the sidecar
                            drawing start (0 when absent)
  --fill-sectors N          Visible sector count; defaults to the sidecar
                            drawing length (remainder of the ISO when absent)
  --channel-bit-nm N        Default 133.3
  --spiral-direction ccw|cw Default cw
  --preview-size 64..8192   Square PNG size (default 1024)
  --samples-per-sector 1..2048 Payload bytes sampled per sector (default 16)`)
}
