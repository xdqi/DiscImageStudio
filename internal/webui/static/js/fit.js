// Ports of CalibrationRingFitter (wide-band edge fit) and the shared fit
// result types.

import { CalibrationGeometry, DISC_BD } from "./geometry.js";

export class CalibrationFitCandidate {
  constructor(parameters, rotationRadians, rmsErrorMm, robustCost = 0, maxErrorMm = 0, outlierCount = 0) {
    this.parameters = parameters;
    this.rotationRadians = rotationRadians;
    this.rmsErrorMm = rmsErrorMm;
    this.robustCost = robustCost;
    this.maxErrorMm = maxErrorMm;
    this.outlierCount = outlierCount;
  }
}

export class CalibrationFitResult {
  constructor(succeeded, bestParameters, rotationRadians, rmsErrorMm, candidates, warnings) {
    this.succeeded = succeeded;
    this.bestParameters = bestParameters;
    this.rotationRadians = rotationRadians;
    this.rmsErrorMm = rmsErrorMm;
    this.candidates = candidates;
    this.warnings = warnings;
    this.boundaryLimited = false;
  }
  get isReliable() { return this.succeeded && this.warnings.length === 0; }
}

// For a constant-pitch optical track, squared radius interpolates the wide edges.
function interpolateWideRadius(q, q0, r0, q1, r1) {
  const fraction = (q - q0) / (q1 - q0);
  return Math.sqrt((1 - fraction) * r0 * r0 + fraction * r1 * r1);
}

// Fits the two full-track endpoint radii from wide-band edge observations:
// `observations` = [{ringIndex: 0..5, radiusMm}].
export function fitWideBandRadii(target, observations) {
  const failed = (warning) => new CalibrationFitResult(false, null, 0, Number.POSITIVE_INFINITY, [], [warning]);
  const count = target.wideBandProgressRanges.length;
  const edgeProgress = (id) => {
    const b = target.wideBandProgressRanges[id < count ? id : id - count];
    return id < count ? b.start : b.endExclusive;
  };
  const ids = new Set(observations.map((o) => o.ringIndex));
  if (!ids.has(0) || !ids.has(count - 1) || !ids.has(count) || !ids.has(2 * count - 1) ||
      ids.size !== observations.length || [...ids].some((id) => id < 0 || id >= 2 * count)) {
    return failed("Please measure the inner and outer edge of the first and third dark band (edges 1, 3, 4, 6).");
  }
  const ordered = observations.slice().sort((a, b) => edgeProgress(a.ringIndex) - edgeProgress(b.ringIndex));
  const progress = ordered.map((o) => edgeProgress(o.ringIndex));
  for (let i = 1; i < ordered.length; i++)
    if (ordered[i].radiusMm <= ordered[i - 1].radiusMm)
      return failed("The edge radii are not in radial order; check the band numbers and inner/outer edges.");
  // Interior observations must match the prediction from the extreme edges.
  for (let i = 1; i < ordered.length - 1; i++) {
    const predicted = interpolateWideRadius(progress[i], progress[0], ordered[0].radiusMm,
      progress[progress.length - 1], ordered[ordered.length - 1].radiusMm);
    if (Math.abs(ordered[i].radiusMm - predicted) > 0.40)
      return failed("An edge position differs from the position predicted by the other edges by more than 0.40 mm; check the band numbers and the rectification.");
  }

  // Two-parameter Gauss–Newton on (inner, outer) through the physical model.
  let parameters = { ...target.geometryParameters };
  const invalid = "The bands cannot be fitted to valid inner/outer radii; check the measured edges.";
  const residuals = (candidate) => {
    const geometry = new CalibrationGeometry(candidate);
    return progress.map((q, i) => geometry.atProgress(q).radius - ordered[i].radiusMm);
  };
  let residual = residuals(parameters);
  let cost = residual.reduce((s, r) => s + r * r, 0);
  for (let iteration = 0; iteration < 30; iteration++) {
    const bandWidth = parameters.outerRadiusMm - parameters.innerRadiusMm;
    const trackLength = new CalibrationGeometry(parameters).trackLength;
    const h = Math.min(0.0001, Math.min(bandWidth, trackLength - bandWidth) / 4);
    const ho = parameters.kind === DISC_BD && parameters.outerRadiusMm + h > 60 ? -h : h;
    const ri = residuals({ ...parameters, innerRadiusMm: parameters.innerRadiusMm + h });
    const ro = residuals({ ...parameters, outerRadiusMm: parameters.outerRadiusMm + ho });
    let aa = 0, ab = 0, bb = 0, ar = 0, br = 0;
    for (let i = 0; i < residual.length; i++) {
      const a = (ri[i] - residual[i]) / h, b = (ro[i] - residual[i]) / ho;
      aa += a * a; ab += a * b; bb += b * b; ar += a * residual[i]; br += b * residual[i];
    }
    const det = aa * bb - ab * ab;
    if (!Number.isFinite(det) || det <= 1e-12 * aa * bb) return failed(invalid);
    const innerStep = (ab * br - bb * ar) / det;
    const outerStep = (ab * ar - aa * br) / det;
    const largestStep = Math.max(Math.abs(innerStep), Math.abs(outerStep));
    if (!Number.isFinite(largestStep)) return failed(invalid);
    if (largestStep < 1e-8) break;
    let improved = false;
    for (let fraction = 1; fraction >= 1.0 / 1024; fraction /= 2) {
      const next = {
        ...parameters,
        innerRadiusMm: parameters.innerRadiusMm + fraction * innerStep,
        outerRadiusMm: parameters.outerRadiusMm + fraction * outerStep,
      };
      const nextTrack = new CalibrationGeometry(next).trackLength;
      if (next.innerRadiusMm <= 0 || next.outerRadiusMm <= next.innerRadiusMm ||
          next.outerRadiusMm - next.innerRadiusMm >= nextTrack ||
          (next.kind === DISC_BD && next.outerRadiusMm > 60)) continue;
      const trial = residuals(next);
      const newCost = trial.reduce((s, r) => s + r * r, 0);
      if (!Number.isFinite(newCost) || newCost >= cost) continue;
      parameters = next; residual = trial; cost = newCost;
      improved = true;
      break;
    }
    if (!improved) {
      if (largestStep < 1e-5) break;
      return failed(invalid);
    }
  }
  const rms = Math.sqrt(cost / residual.length);
  if (parameters.innerRadiusMm < 7.5 || parameters.outerRadiusMm > 60 + 1e-8)
    return failed("The fitted radii are outside the measurable disc range; check the photo scale and the edges.");
  const candidate = new CalibrationFitCandidate(parameters, 0, rms, cost, Math.max(...residual.map(Math.abs)));
  return new CalibrationFitResult(true, parameters, 0, rms, [candidate], []);
}

// Five alternating dark/light widths, two measured inner-edge spacings, and
// their predictions from the first and last outer edges (CalibrationWideBandMeasurements).
export function wideBandIntervals(target, observations) {
  const count = target.wideBandProgressRanges.length;
  const radii = new Array(2 * count).fill(null);
  for (const edge of observations) radii[edge.ringIndex] = edge.radiusMm;
  const width = (from, to) => {
    const w = radii[to] !== null && radii[from] !== null ? radii[to] - radii[from] : null;
    return w !== null && w > 0 ? w : null;
  };
  const edgeOrder = [0, 3, 1, 4, 2, 5];
  const segments = edgeOrder.slice(0, -1).map((from, i) => width(from, edgeOrder[i + 1]));
  const spacings = [];
  const predicted = [];
  for (let i = 0; i < count - 1; i++) spacings.push(width(i, i + 1));
  const firstOuter = radii[count], lastOuter = radii[2 * count - 1];
  if (firstOuter !== null && lastOuter !== null && lastOuter > firstOuter) {
    const predictedInner = (i) => interpolateWideRadius(target.wideBandProgressRanges[i].start,
      target.wideBandProgressRanges[0].endExclusive, firstOuter,
      target.wideBandProgressRanges[count - 1].endExclusive, lastOuter);
    for (let i = 0; i < count - 1; i++) {
      const spacing = predictedInner(i + 1) - predictedInner(i);
      predicted.push(Number.isFinite(spacing) && spacing > 0 ? spacing : null);
    }
  } else {
    for (let i = 0; i < count - 1; i++) predicted.push(null);
  }
  return { segmentWidthsMm: segments, innerEdgeSpacingsMm: spacings, predictedInnerEdgeSpacingsMm: predicted };
}
