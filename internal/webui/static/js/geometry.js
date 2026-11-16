// Port of CalibrationGeometry + ConstantPitchCalibrationGeometry to JS.
// The continuous track envelope shared by the CD, DVD and Blu-ray models.

import { TrackGeometry } from "./trackgeom.js";

export const DISC_CD = "cd";
export const DISC_DVD = "dvd";
export const DISC_BD = "bd";

export const DVD_CHANNEL_BITS_PER_SECTOR = 38688;
export const BD_SECTORS_PER_CLUSTER = 32;
export const BD_CHANNEL_BITS_PER_RU = 962136;
export const BD_CHANNEL_BIT_LENGTH_NM = 74.5;
export const DVD_CHANNEL_BIT_LENGTH_NM = 133.3;
export const CD_LINEAR_VELOCITY = 1200;

export function trackLengthMm(p) {
  switch (p.kind) {
    case DISC_CD: return p.sectors / 75.0 * p.linearDensity;
    case DISC_DVD: return p.sectors * DVD_CHANNEL_BITS_PER_SECTOR * p.linearDensity * 1e-6;
    case DISC_BD: return p.sectors / BD_SECTORS_PER_CLUSTER * BD_CHANNEL_BITS_PER_RU * p.linearDensity * 1e-6;
  }
  return NaN;
}

function primitive(radius, b) {
  return (radius * Math.sqrt(radius * radius + b * b) + b * b * Math.asinh(radius / b)) / (2 * b);
}

class ConstantPitchGeometry {
  constructor(p) {
    this.params = p;
    const trackLength = trackLengthMm(p);
    let low = 1e-15, high = 1;
    const length = (growth) => primitive(p.outerRadiusMm, growth) - primitive(p.innerRadiusMm, growth);
    while (length(high) > trackLength) high *= 2;
    for (let i = 0; i < 100; i++) {
      const middle = (low + high) / 2;
      if (length(middle) > trackLength) low = middle; else high = middle;
    }
    this.growth = (low + high) / 2;
    this.innerPrimitive = primitive(p.innerRadiusMm, this.growth);
    this.trackLength = trackLength;
  }
  progressAtRadius(radius) {
    return (primitive(radius, this.growth) - this.innerPrimitive) / this.trackLength;
  }
  tryAtProgress(progress) {
    const targetPrimitive = this.innerPrimitive + progress * this.trackLength;
    if (!Number.isFinite(targetPrimitive) || targetPrimitive <= 0) return null;
    const squared = this.params.innerRadiusMm * this.params.innerRadiusMm + 2 * this.growth * progress * this.trackLength;
    let radius = Math.sqrt(Math.max(squared, this.growth * this.growth));
    const iterations = this.growth <= 0.01 ? 2 : 12;
    for (let i = 0; i < iterations; i++) {
      const next = radius - (primitive(radius, this.growth) - targetPrimitive) * this.growth / Math.sqrt(radius * radius + this.growth * this.growth);
      radius = next > 0 ? next : radius / 2;
    }
    if (progress === 0) radius = this.params.innerRadiusMm;
    if (progress === 1) radius = this.params.outerRadiusMm;
    return Number.isFinite(radius) && radius > 0 ? radius : null;
  }
}

export class CalibrationGeometry {
  // p: {kind, innerRadiusMm, outerRadiusMm, sectors, linearDensity, pitchLinear, pitchQuadratic, pitchCubic}
  constructor(p) {
    this.params = p;
    this.squaredRange = p.outerRadiusMm * p.outerRadiusMm - p.innerRadiusMm * p.innerRadiusMm;
    this.trackLength = trackLengthMm(p);
    this.b = this.squaredRange / (2 * this.trackLength);
    this.track = null;
    this.constant = null;
    if (p.kind === DISC_DVD || p.pitchLinear !== 0 || p.pitchQuadratic !== 0 || p.pitchCubic !== 0) {
      this.track = new TrackGeometry(p.innerRadiusMm, p.outerRadiusMm, this.trackLength,
        p.pitchLinear, p.pitchQuadratic, p.pitchCubic);
      this.b = this.track.radialGrowthPerRadianMm;
    } else if (p.kind === DISC_BD) {
      this.constant = new ConstantPitchGeometry(p);
      this.b = this.constant.growth;
    }
  }

  get isConstantPitch() { return this.track ? this.track.isConstantPitch : true; }
  get usesAreaApproximation() { return this.params.kind === DISC_CD && this.track === null; }

  radialGrowthAtRadius(radius) {
    return this.track ? this.track.radialGrowthAtRadius(radius) : this.b;
  }

  // Bounds of the local radial growth over [first, last].
  radialGrowthBounds(first, last) {
    if (this.isConstantPitch) return { minimum: this.b, maximum: this.b };
    const width = this.params.outerRadiusMm - this.params.innerRadiusMm;
    const low = (first - this.params.innerRadiusMm) / width;
    const high = (last - this.params.innerRadiusMm) / width;
    const a = this.params.pitchLinear, b = this.params.pitchQuadratic, c = this.params.pitchCubic;
    const Q = (x) => 1 + x * (a + x * (b + x * c));
    let minimum = Math.min(Q(low), Q(high)), maximum = Math.max(Q(low), Q(high));
    const include = (x) => {
      if (x <= low || x >= high || !Number.isFinite(x)) return;
      minimum = Math.min(minimum, Q(x)); maximum = Math.max(maximum, Q(x));
    };
    if (c === 0) { if (b !== 0) include(-a / (2 * b)); }
    else {
      const discriminant = b * b - 3 * c * a;
      if (discriminant >= 0) {
        const root = Math.sqrt(discriminant);
        include((-b - root) / (3 * c)); include((-b + root) / (3 * c));
      }
    }
    return { minimum: minimum * this.b, maximum: maximum * this.b };
  }

  arcLengthFactor(radius) {
    const growth = this.radialGrowthAtRadius(radius);
    return this.usesAreaApproximation ? radius : Math.sqrt(radius * radius + growth * growth);
  }

  progressAtRadius(radius) {
    let progress;
    if (this.constant !== null) progress = this.constant.progressAtRadius(radius);
    else if (this.track === null) progress = (radius * radius - this.params.innerRadiusMm * this.params.innerRadiusMm) / this.squaredRange;
    else progress = this.track.arcLengthAtRadius(radius) / this.trackLength;
    return Math.min(1, Math.max(0, progress));
  }

  angleAtRadius(radius) {
    return this.track ? this.track.angleAtRadius(radius) : (radius - this.params.innerRadiusMm) / this.b;
  }

  atProgress(progress) {
    if (this.constant !== null) {
      const r = this.constant.tryAtProgress(progress);
      if (r === null) throw new Error("no valid positive-radius track at this progress");
      return { radius: r, angle: this.angleAtRadius(r) };
    }
    const radius = this.track === null
      ? Math.sqrt(this.params.innerRadiusMm * this.params.innerRadiusMm + progress * this.squaredRange)
      : this.track.radiusAtArcLength(progress * this.trackLength);
    return { radius, angle: this.angleAtRadius(radius) };
  }

  tryProgressAtRadiusExtended(radius) {
    if (this.track !== null) return this.track.tryProgressAtRadiusExtended(radius);
    if (!Number.isFinite(radius) || radius <= 0 || !Number.isFinite(radius * radius)) return null;
    let progress;
    if (this.constant !== null) {
      progress = this.constant.progressAtRadius(radius);
    } else {
      progress = (radius - this.params.innerRadiusMm) * (radius + this.params.innerRadiusMm) / this.squaredRange;
    }
    return Number.isFinite(progress) ? progress : null;
  }

  tryAtProgressExtended(progress) {
    if (this.track !== null) return this.track.tryAtProgressExtended(progress);
    if (!Number.isFinite(progress)) return null;
    if (this.constant !== null) {
      const constantRadius = this.constant.tryAtProgress(progress);
      if (constantRadius === null) return null;
      const angle = this.angleAtRadius(constantRadius);
      return Number.isFinite(angle) ? { radius: constantRadius, angle } : null;
    }
    const squared = this.params.innerRadiusMm * this.params.innerRadiusMm + progress * this.squaredRange;
    if (!Number.isFinite(squared) || squared <= 0) return null;
    const radius = Math.sqrt(squared), angle = this.angleAtRadius(radius);
    if (!Number.isFinite(angle)) return null;
    return { radius, angle };
  }
}
