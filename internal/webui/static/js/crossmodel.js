// Port of the CalibrationCrossModel (CalibrationCrossPrediction.cs) and the
// forward curve sampler used for prediction overlays.

import { CalibrationGeometry, DISC_CD } from "./geometry.js";

// One shared forward phase model for fitting and visible prediction.
// generated/actual are CalibrationGeometry; kind is the disc kind; identical
// marks generated === actual (by reference equality of the parameter objects);
// sourceTarget may be null or a Target with boundary phase profile.
export class CalibrationCrossModel {
  constructor(generated, actual, kind, identical, sourceTarget = null) {
    this.generated = generated;
    this.actual = actual;
    this.kind = kind;
    this.identical = identical;
    this.sourceTarget = sourceTarget;
  }

  // Returns {radius, sourceRadius, phase, derivative} or null.
  tryAtRadius(radius) {
    if (!Number.isFinite(radius) || radius <= 0 || !Number.isFinite(radius * radius)) return null;
    if (this.identical) {
      if (!this.actual.isConstantPitch && this.actual.tryProgressAtRadiusExtended(radius) === null) return null;
      return this.addSourceProfile({ radius, sourceRadius: radius, phase: 0, derivative: 0 }, 1);
    }
    const progress = this.actual.tryProgressAtRadiusExtended(radius);
    if (progress === null) return null;
    const source = this.generated.tryAtProgressExtended(progress);
    if (source === null) return null;
    const ba = this.actual.radialGrowthAtRadius(radius);
    const bg = this.generated.radialGrowthAtRadius(source.radius);
    const phase = this.actual.angleAtRadius(radius) - source.angle;
    const ratio = this.actual.arcLengthFactor(radius) / this.generated.arcLengthFactor(source.radius);
    const derivative = (1 - ratio) / ba;
    if (!Number.isFinite(phase) || !Number.isFinite(derivative)) return null;
    return this.addSourceProfile({ radius, sourceRadius: source.radius, phase, derivative }, bg / ba * ratio);
  }

  addSourceProfile(sample, sourceDerivative) {
    if (!this.sourceTarget || this.sourceTarget.stage !== "boundary") return sample;
    return {
      ...sample,
      phase: sample.phase + this.sourceTarget.boundaryPhaseAtSourceRadius(sample.sourceRadius),
      derivative: sample.derivative + this.sourceTarget.boundaryPhaseDerivative(sample.sourceRadius) * sourceDerivative,
    };
  }

  derivativeBound(first, last) {
    const profileBound = this.sourceTarget ? this.sourceTarget.boundaryPhaseDerivativeBound : 0;
    if (this.identical) return profileBound;
    if (this.kind === DISC_CD && this.generated.usesAreaApproximation && this.actual.usesAreaApproximation) {
      const firstRatio = first.radius / first.sourceRadius, lastRatio = last.radius / last.sourceRadius;
      const baCd = this.actual.radialGrowthAtRadius(first.radius);
      const bgCd = this.generated.radialGrowthAtRadius(first.sourceRadius);
      return Math.max(Math.abs(1 - firstRatio), Math.abs(1 - lastRatio)) / baCd +
        profileBound * bgCd / baCd * Math.max(firstRatio, lastRatio);
    }
    const ba = this.actual.radialGrowthBounds(first.radius, last.radius);
    const bg = this.generated.radialGrowthBounds(first.sourceRadius, last.sourceRadius);
    const actualMinimumSquared = this.actual.usesAreaApproximation ? 0 : ba.minimum * ba.minimum;
    const actualMaximumSquared = this.actual.usesAreaApproximation ? 0 : ba.maximum * ba.maximum;
    const sourceMinimumSquared = this.generated.usesAreaApproximation ? 0 : bg.minimum * bg.minimum;
    const sourceMaximumSquared = this.generated.usesAreaApproximation ? 0 : bg.maximum * bg.maximum;
    const minimumRatio = Math.sqrt(first.radius * first.radius + actualMinimumSquared) /
      Math.sqrt(last.sourceRadius * last.sourceRadius + sourceMaximumSquared);
    const maximumRatio = Math.sqrt(last.radius * last.radius + actualMaximumSquared) /
      Math.sqrt(first.sourceRadius * first.sourceRadius + sourceMinimumSquared);
    return Math.max(Math.abs(1 - minimumRatio), Math.abs(1 - maximumRatio)) / ba.minimum +
      profileBound * bg.maximum / ba.minimum * maximumRatio;
  }
}

function toPoint(sample, startAngle) {
  return {
    x: sample.radius * Math.cos(startAngle + sample.phase),
    y: sample.radius * Math.sin(startAngle + sample.phase),
  };
}

function chordDistance(p, a, b) {
  const dx = b.x - a.x, dy = b.y - a.y;
  const t = Math.min(1, Math.max(0, ((p.x - a.x) * dx + (p.y - a.y) * dy) / Math.max(1e-30, dx * dx + dy * dy)));
  return Math.sqrt((p.x - a.x - t * dx) ** 2 + (p.y - a.y - t * dy) ** 2);
}

// Trace the complete observed-radius interval of one predicted curve.
// Ports CalibrationCrossPrediction.GetOutline.
export function predictedOutline(target, actualParameters, minimumRadiusMm, maximumRadiusMm,
    rotationRadians = 0, armIndex = 0, maxPoints = 20000) {
  if (!Number.isFinite(minimumRadiusMm) || !Number.isFinite(maximumRadiusMm) ||
      minimumRadiusMm <= 0 || maximumRadiusMm <= minimumRadiusMm)
    throw new Error("the prediction curve needs a finite, increasing positive radius interval");
  if (maxPoints < 2 || maxPoints > 1000000) throw new Error("maxPoints out of range");
  const model = new CalibrationCrossModel(
    new CalibrationGeometry(target.geometryParameters),
    new CalibrationGeometry(actualParameters),
    target.parameters.kind,
    target.parameters === actualParameters,
    target);
  const startAngle = -Math.PI / 2 + armIndex * (target.hasBoundaryPattern ? Math.PI : Math.PI / 2) + rotationRadians;
  const points = [];
  const sample = (radius) => {
    const s = model.tryAtRadius(radius);
    if (s === null) throw new Error("this observed radius has no valid positive-radius continuation under the current parameters");
    return s;
  };
  const first = sample(minimumRadiusMm);
  const last = sample(maximumRadiusMm);
  const segment = (a, b, depth) => {
    const span = b.radius - a.radius;
    const middle = sample(a.radius + span / 2);
    const pa = toPoint(a, startAngle), pb = toPoint(b, startAngle);
    const variation = model.derivativeBound(a, b) * span;
    let split = span > 0.2 || variation > Math.PI / 36;
    if (!split) {
      const q1 = sample(a.radius + span / 4);
      const q3 = sample(a.radius + span * 0.75);
      split = chordDistance(toPoint(middle, startAngle), pa, pb) > 0.01 ||
        chordDistance(toPoint(q1, startAngle), pa, pb) > 0.01 ||
        chordDistance(toPoint(q3, startAngle), pa, pb) > 0.01;
    }
    if (split) {
      if (depth >= 48 || points.length >= maxPoints - 1) throw new Error("the complete curve exceeds the point budget");
      segment(a, middle, depth + 1);
      segment(middle, b, depth + 1);
    } else {
      if (points.length >= maxPoints - 1) throw new Error("the complete curve exceeds the point budget");
      points.push(pa);
    }
  };
  segment(first, last, 0);
  points.push(toPoint(last, startAngle));
  return points;
}

