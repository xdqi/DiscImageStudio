// The current-schema calibration targets shared by the browser fitters:
// v13 coarse (wide bands + angle sweep), v16 boundary (wavy), v14 fine
// (reference rings + cross). Ports the model half of CalibrationTarget.cs.

import { CalibrationGeometry, DISC_CD, DISC_DVD, DISC_BD, trackLengthMm } from "./geometry.js";

export const STAGE_COARSE = "coarse";
export const STAGE_BOUNDARY = "boundary";
export const STAGE_FINE = "fine";

// CalibrationWideBandPattern.AlternatingBands (schema 13), in permille of the
// drawable track.
export const WIDE_BANDS_PERMILLE = [[35, 170], [305, 510], [705, 955]];

// CalibrationReferencePattern.
export const REFERENCE_PIXEL_SIZE = 2400;
export const REFERENCE_RING_RADII_PIXELS = [490, 570, 650, 730, 810, 890, 970, 1050, 1130];
export const REFERENCE_LINE_WIDTH_PIXELS = 10;

export function referenceMillimetresPerPixel(outerRadiusMm, kind) {
  return outerRadiusMm / (kind === DISC_DVD ? (REFERENCE_PIXEL_SIZE - 1) / 2 : REFERENCE_PIXEL_SIZE / 2);
}

export class AngleSweepStation {
  constructor(start, end, inner, outer, length) {
    this.startProgress = start;
    this.endProgress = end;
    this.startVirtualRadiusMm = inner;
    this.endVirtualRadiusMm = outer;
    this.trackLength = length;
    this.squaredSlope = (outer - inner) * (outer + inner) / (end - start);
  }
  virtualRadiusAt(progress) {
    return Math.sqrt(this.startVirtualRadiusMm * this.startVirtualRadiusMm +
      (progress - this.startProgress) * this.squaredSlope);
  }
  phaseAt(progress) {
    return 2 * this.trackLength * (progress - this.startProgress) /
      (this.startVirtualRadiusMm + this.virtualRadiusAt(progress));
  }
  static tone(radians) {
    const r = ieeeRemainder(radians, 2 * Math.PI);
    return r >= 0 && r < Math.PI ? 0 : 255;
  }
}

export function ieeeRemainder(x, y) {
  // IEEE 754 remainder, round-to-even (Math.IEEERemainder).
  if (y === 0 || !Number.isFinite(x) || !Number.isFinite(y)) return NaN;
  const q = x / y;
  const rounded = Math.round(q);
  const evenQ = Math.abs(q % 1) === 0.5
    ? (Math.abs(Math.floor(q)) % 2 === 0 ? Math.floor(q) : Math.ceil(q))
    : rounded;
  let result = x - y * evenQ;
  if (result === 0) return x < 0 ? -0 : 0;
  return result;
}

export class Target {
  // parameters: {kind, innerRadiusMm, outerRadiusMm, sectors, linearDensity,
  //              pitchLinear, pitchQuadratic, pitchCubic, usableSectors}
  constructor(parameters, stage) {
    this.parameters = parameters;
    this.stage = stage;
    // ParametersForGeometry: coarse targets are constant pitch.
    const gp = stage === STAGE_COARSE
      ? { ...parameters, pitchLinear: 0, pitchQuadratic: 0, pitchCubic: 0 }
      : parameters;
    this.geometry = new CalibrationGeometry(gp);
    this.geometryParameters = gp;
    this.generatedGeometry = this.geometry;

    this.drawingInnerRadiusMm = parameters.innerRadiusMm;
    this.drawingOuterRadiusMm = parameters.usableSectors > 0
      ? this.geometry.atProgress(parameters.usableSectors / parameters.sectors).radius
      : parameters.outerRadiusMm;

    // Wide bands (coarse only).
    this.wideBandProgressRanges = null;
    this.angleSweepStations = null;
    if (stage === STAGE_COARSE) {
      this.wideBandProgressRanges = [];
      for (const [startPermille, endPermille] of WIDE_BANDS_PERMILLE) {
        let start, end;
        if (parameters.kind === DISC_CD) {
          start = startPermille / 1000;
          end = endPermille / 1000;
        } else {
          const u = parameters.kind === DISC_BD ? BD_SECTORS_PER_CLUSTER : 1;
          const output = parameters.usableSectors > 0 ? parameters.usableSectors : parameters.sectors;
          const units = Math.floor(output / u);
          const startUnits = Math.floor((units * startPermille + 999) / 1000);
          const endUnits = Math.floor((units * endPermille + 999) / 1000);
          start = (u * startUnits) / parameters.sectors;
          end = (u * endUnits) / parameters.sectors;
        }
        this.wideBandProgressRanges.push({ start, endExclusive: end });
      }
      this.angleSweepStations = this.wideBandProgressRanges.map((band, i) => {
        const guard = 0.14 * (band.endExclusive - band.start);
        const start = band.start + guard, end = band.endExclusive - guard;
        const offset = i % 2 === 0 ? 0.65 : -0.65;
        return new AngleSweepStation(start, end,
          this.geometry.atProgress(start).radius - offset,
          this.geometry.atProgress(end).radius + offset,
          trackLengthMm(gp));
      });
    }

    // Fine-stage rings (v14 reference cross).
    this.referenceRingRadiiMm = null;
    if (stage === STAGE_FINE) {
      const pixelSizeMm = referenceMillimetresPerPixel(parameters.outerRadiusMm, parameters.kind);
      this.referenceRingRadiiMm = REFERENCE_RING_RADII_PIXELS.map((r) => r * pixelSizeMm);
      this.referenceCrossWidthMm = REFERENCE_LINE_WIDTH_PIXELS * pixelSizeMm;
    }
  }

  get hasWideAddressBands() { return this.stage === STAGE_COARSE; }
  get hasBoundaryPattern() { return this.stage === STAGE_BOUNDARY; }
  get usesReferencePattern() { return this.stage === STAGE_FINE; }
  // Boundary traces have 2 arms, fine traces have 4.
  get traceArmCount() { return this.stage === STAGE_BOUNDARY ? 2 : 4; }

  boundaryPhaseAtSourceRadius(radius) {
    return this.stage === STAGE_BOUNDARY
      ? Math.PI / 2 * Math.sin(6 * Math.PI * (radius - this.drawingInnerRadiusMm) /
          (this.drawingOuterRadiusMm - this.drawingInnerRadiusMm))
      : 0;
  }

  boundaryPhaseDerivative(radius) {
    if (this.stage !== STAGE_BOUNDARY) return 0;
    return this.boundaryPhaseDerivativeBound * Math.cos(6 * Math.PI *
      (radius - this.drawingInnerRadiusMm) / (this.drawingOuterRadiusMm - this.drawingInnerRadiusMm));
  }

  get boundaryPhaseDerivativeBound() {
    return this.stage === STAGE_BOUNDARY
      ? 3 * Math.PI * Math.PI / (this.drawingOuterRadiusMm - this.drawingInnerRadiusMm)
      : 0;
  }
}
