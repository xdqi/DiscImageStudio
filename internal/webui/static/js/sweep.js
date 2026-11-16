// Port of CalibrationAngleSweepReader.cs + CalibrationAngleSweepTemplates.cs.
// Locates the radial turning point of each chirped angular pilot in a
// rectified photo. samplePhoto(xMm, yMm) returns [r, g, b] in [0, 1].

import { fitWideBandRadii } from "./fit.js";
import { CalibrationGeometry } from "./geometry.js";

const RAYS = 17;
const STEP = 0.05;
const TRIM_SAMPLES = 3;

function median(values) {
  const sorted = values.slice().sort((a, b) => a - b);
  return (sorted[(sorted.length - 1) >> 1] + sorted[sorted.length >> 1]) / 2;
}

function square(a) { return a * a; }

function highPass(values) {
  const tmp = new Array(values.length), background = new Array(values.length);
  box(values, tmp);
  box(tmp, background);
  for (let i = 0; i < values.length; i++) values[i] -= background[i];
}

function box(input, output) {
  const half = 11;
  let sum = 0;
  for (let i = -half; i <= half; i++) sum += input[Math.min(Math.max(i, 0), input.length - 1)];
  for (let i = 0; i < input.length; i++) {
    output[i] = sum / (2 * half + 1);
    sum += input[Math.min(i + half + 1, input.length - 1)] - input[Math.max(i - half, 0)];
  }
}

// Refine each band boundary locally along one fan of rays.
function findEdge(nominal, directions, sample) {
  const found = [];
  for (const { sin, cos } of directions) {
    let peak = 0, radius = nominal;
    for (let i = -14; i <= 14; i++) {
      const r = nominal + 0.05 * i;
      const a = sample((r - 0.13) * cos, (r - 0.13) * sin);
      const b = sample((r + 0.13) * cos, (r + 0.13) * sin);
      const strength = (a[0] - b[0]) ** 2 + (a[1] - b[1]) ** 2 + (a[2] - b[2]) ** 2;
      if (strength > peak) {
        peak = strength;
        radius = r;
      }
    }
    if (peak > square(0.012) && Math.abs(radius - nominal) < 0.69) found.push(radius);
  }
  if (found.length < (RAYS + 1) / 2) return null;
  const result = median(found);
  return median(found.map((r) => Math.abs(r - result))) <= 0.18 ? result : null;
}

function extractPhoto(lo, n, directions, sample) {
  const rows = [];
  const energy = [0, 0, 0];
  for (let j = 0; j < RAYS; j++) {
    const { sin, cos } = directions[j];
    const row = new Array(n);
    for (let k = 0; k < n; k++) {
      const r = lo + k * STEP;
      row[k] = sample(r * cos, r * sin);
    }
    for (let component = 0; component < 3; component++) {
      const channelSamples = new Array(n);
      for (let k = 0; k < n; k++) channelSamples[k] = row[k][component];
      highPass(channelSamples);
      for (let k = 0; k < n; k++) {
        row[k] = component === 0 ? [channelSamples[k], row[k][1], row[k][2]]
          : component === 1 ? [row[k][0], channelSamples[k], row[k][2]]
          : [row[k][0], row[k][1], channelSamples[k]];
        energy[component] += square(channelSamples[k]);
      }
    }
    rows.push(row);
  }
  // Marks can be conspicuous in a colour channel while luminance is almost constant.
  let channel = energy[1] > energy[0] ? 1 : 0;
  if (energy[2] > energy[channel]) channel = 2;
  const cosineProjection = new Array(n).fill(0);
  const sineProjection = new Array(n).fill(0);
  let power = 0, cosineWeight = 0, sineWeight = 0;
  for (let j = 0; j < RAYS; j++) {
    const angle = (j / (RAYS - 1.0) - 0.5) * Math.PI / 4;
    const s = Math.sin(angle), c = Math.cos(angle);
    cosineWeight += c * c;
    sineWeight += s * s;
    for (let k = 0; k < n; k++) {
      const y = rows[j][k][channel];
      cosineProjection[k] += y * c;
      sineProjection[k] += y * s;
      if (k >= TRIM_SAMPLES && k < n - TRIM_SAMPLES) power += y * y;
    }
  }
  let slopePower = 0;
  for (let k = TRIM_SAMPLES; k < n - TRIM_SAMPLES; k++) slopePower += square(sineProjection[k]);
  const usableSamples = n - 2 * TRIM_SAMPLES;
  return power > usableSamples * RAYS * square(0.001) && slopePower > usableSamples * square(0.0008 * sineWeight)
    ? { cosine: cosineProjection, sine: sineProjection, cosineWeight, sineWeight, energy: power, sineEnergy: slopePower }
    : null;
}

// A quadratic-phase signal convolved with a Gaussian has a closed form.
function bestTemplate(trace, n, offset, curvature) {
  const a = new Array(n), b = new Array(n);
  let best = { correlation: 0, angularGain: 0, amplitude: 0 };
  for (const sigma of [0.11, 0.20, 0.30]) {
    const z = curvature * square(sigma), divisor = 1 + z * z;
    const decay = -0.5 * square(curvature * sigma) / divisor;
    const omega = 0.5 * curvature / divisor;
    for (let k = 0; k < n; k++) {
      const x2 = square(offset + STEP * k);
      const angle = omega * x2;
      const amplitude = Math.exp(decay * x2);
      a[k] = amplitude * Math.cos(angle);
      b[k] = amplitude * Math.sin(angle);
    }
    highPass(a);
    highPass(b);
    let aa = 0, ab = 0, bb = 0, aCos = 0, bSin = 0, bCos = 0, aSin = 0;
    for (let k = TRIM_SAMPLES; k < n - TRIM_SAMPLES; k++) {
      aa += a[k] * a[k];
      bb += b[k] * b[k];
      ab += a[k] * b[k];
      aCos += a[k] * trace.cosine[k];
      bSin += b[k] * trace.sine[k];
      bCos += b[k] * trace.cosine[k];
      aSin += a[k] * trace.sine[k];
    }
    const uu = aa * trace.cosineWeight + bb * trace.sineWeight;
    const vv = bb * trace.cosineWeight + aa * trace.sineWeight;
    const uv = ab * (trace.cosineWeight - trace.sineWeight);
    const det = uu * vv - uv * uv;
    if (det <= 1e-12) continue;
    for (let hand = -1; hand <= 1; hand += 2) {
      const yu = aCos - hand * bSin, yv = bCos + hand * aSin;
      const u = (yu * vv - yv * uv) / det, v = (yv * uu - yu * uv) / det;
      const corr = Math.sqrt(Math.max(0, u * yu + v * yv) / trace.energy);
      if (corr <= best.correlation) continue;
      const s2 = trace.sineWeight;
      const angularGain = (2 * hand * s2 * (v * aSin - u * bSin) - s2 * s2 * (v * v * aa + u * u * bb - 2 * u * v * ab)) / trace.sineEnergy;
      best = { correlation: corr, angularGain, amplitude: Math.hypot(u, v) };
    }
  }
  return best;
}

function locate(station, intercept, slope, slopeEstimate, directions, sample, length) {
  const edgeMarginMm = 0.48;
  const lo = Math.sqrt(intercept + slope * station.startProgress) + edgeMarginMm;
  const hi = Math.sqrt(intercept + slope * station.endProgress) - edgeMarginMm;
  const n = Math.floor((hi - lo) / STEP) + 1;
  if (!Number.isFinite(lo) || !Number.isFinite(hi) || n < 38 || n > 500) return null;
  const trace = extractPhoto(lo, n, directions, sample);
  if (trace === null) return null;
  const candidates = [];
  const sweptSlope = (square(station.endVirtualRadiusMm) - square(station.startVirtualRadiusMm)) /
    (station.endProgress - station.startProgress);
  for (let center = lo + 0.10; center < hi - 0.10; center += 0.025) {
    const q = (square(center) - intercept) / slope;
    const v = station.virtualRadiusAt(q);
    // phi'' in photo millimetres at the lock point.
    const curvature = length * (slopeEstimate - sweptSlope) / (2 * v * v * v) * square(2 * center / slope);
    const match = bestTemplate(trace, n, lo - center, curvature);
    candidates.push({ radius: center, match });
  }
  if (candidates.length === 0) return null;
  const best = candidates.reduce((m, c) => (c.match.correlation > m.match.correlation ? c : m));
  if (best.match.correlation < 0.24 || best.match.angularGain < -0.60 || best.match.amplitude < 0.004 ||
      best.radius < lo + 0.16 || best.radius > hi - 0.16) return null;
  if (candidates.some((s) => Math.abs(s.radius - best.radius) > 0.27 && s.match.correlation > best.match.correlation * 0.94))
    return null;
  const lockProgress = (square(best.radius) - intercept) / slope;
  return { progress: lockProgress, radius: station.virtualRadiusAt(lockProgress), angularGain: best.match.angularGain };
}

// Reads the coarse target's angle markers. observations = 6 edge observations
// [{ringIndex, radiusMm, points:[...]}]. Returns {status, corrected,
// stationCount, coverageDegrees, degreesPerTurnOffset}.
export function readAngleSweep(target, observations, canvasRadiusMm, samplePhoto, preferredAngleDegrees = null) {
  const insufficient = (status, maxStations, coverage) => ({
    status, corrected: null, stationCount: maxStations, coverageDegrees: coverage, degreesPerTurnOffset: [],
  });
  if (!target.hasWideAddressBands || !target.angleSweepStations || observations.length !== 6 ||
      !Number.isFinite(canvasRadiusMm) || canvasRadiusMm <= 7.5)
    return insufficient("insufficient", 0, 0);
  const radial = fitWideBandRadii(target, observations);
  if (!radial.succeeded) return insufficient("insufficient", 0, 0);
  const apparent = radial.bestParameters;
  const radii = observations.slice().sort((a, b) => a.ringIndex - b.ringIndex).map((o) => o.radiusMm);
  const allPoints = [];
  for (const o of observations) if (o.points) allPoints.push(...o.points);
  let first = preferredAngleDegrees ?? (allPoints.length > 0 ? Math.atan2(allPoints[0].y, allPoints[0].x) * 180 / Math.PI : 0);
  if (!Number.isFinite(first)) first = 0;
  let maxStations = 0;
  let inconsistent = false;
  const readings = [];
  const nominal = new CalibrationGeometry(target.geometryParameters);
  const spanSquared = apparent.outerRadiusMm * apparent.outerRadiusMm - apparent.innerRadiusMm * apparent.innerRadiusMm;
  for (let sector = 0; sector < 16; sector++) {
    const angle = ((first + sector * 22.5) % 360 + 360) % 360;
    const directions = [];
    for (let i = 0; i < RAYS; i++) {
      const a = (angle + (i / (RAYS - 1) - 0.5) * 45) * Math.PI / 180;
      directions.push({ sin: Math.sin(a), cos: Math.cos(a) });
    }
    const points = [];
    for (let station = 0; station < target.angleSweepStations.length; station++) {
      const pilot = target.angleSweepStations[station];
      const band = target.wideBandProgressRanges[station];
      const inner = findEdge(radii[station], directions, samplePhoto);
      const outer = findEdge(radii[station + 3], directions, samplePhoto);
      if (inner === null || outer === null || outer <= inner) continue;
      const slope = (outer * outer - inner * inner) / (band.endExclusive - band.start);
      const intercept = inner * inner - slope * band.start;
      const anchor = locate(pilot, intercept, slope, spanSquared, directions, samplePhoto,
        trackLengthOf(target));
      if (anchor !== null) points.push(anchor);
    }
    maxStations = Math.max(maxStations, points.length);
    if (points.length !== 3 || !points.some((p) => p.angularGain > 0.15) || points.reduce((s, p) => s + p.angularGain, 0) < 0.15) continue;
    const left = points[0], middle = points[1], right = points[2];
    const endSquare = (q) => square(left.radius) + (q - left.progress) / (right.progress - left.progress) *
      (square(right.radius) - square(left.radius));
    const check = Math.sqrt(endSquare(middle.progress));
    if (!Number.isFinite(check) || Math.abs(check - middle.radius) > 0.050) {
      inconsistent = true;
      continue;
    }
    const meanProgress = points.reduce((s, a) => s + a.progress, 0) / points.length;
    const meanSquare = points.reduce((s, a) => s + square(a.radius), 0) / points.length;
    const slopeFit = points.reduce((s, a) => s + (a.progress - meanProgress) * (square(a.radius) - meanSquare), 0) /
      points.reduce((s, a) => s + square(a.progress - meanProgress), 0);
    const riFit = Math.sqrt(meanSquare - meanProgress * slopeFit);
    const roFit = Math.sqrt(meanSquare + (1 - meanProgress) * slopeFit);
    if (!Number.isFinite(riFit) || !Number.isFinite(roFit) || riFit < 7.5 || roFit <= riFit ||
        roFit > canvasRadiusMm || roFit > 60) continue;
    const degreesPerTurn = points.map((point) =>
      360 * (nominal.atProgress(point.progress).radius / point.radius - 1));
    readings.push({ inner: riFit, outer: roFit, angle, degreesPerTurn });
  }
  if (readings.length === 0)
    return insufficient(inconsistent ? "conflicting" : "insufficient", maxStations, 0);
  const inners = readings.map((r) => r.inner), outers = readings.map((r) => r.outer);
  if (Math.max(...inners) - Math.min(...inners) > 0.12 ||
      Math.max(...outers) - Math.min(...outers) > 0.12)
    return insufficient("conflicting", maxStations, 0);
  let slices = 0;
  for (let i = 0; i < 16; i++)
    if (readings.some((p) => Math.abs(ieeeRemainderLocal(first + i * 22.5 + 11.25 - p.angle, 360)) < 22.5)) slices++;
  const corrected = {
    ...target.parameters,
    innerRadiusMm: median(readings.map((s) => s.inner)),
    outerRadiusMm: median(readings.map((s) => s.outer)),
  };
  return {
    status: "success",
    corrected,
    stationCount: 3,
    coverageDegrees: 22.5 * slices,
    degreesPerTurnOffset: [0, 1, 2].map((station) => median(readings.map((r) => r.degreesPerTurn[station]))),
  };
}

function ieeeRemainderLocal(x, y) {
  if (y === 0 || !Number.isFinite(x)) return NaN;
  const q = x / y;
  const floor = Math.floor(q);
  const rounded = Math.abs(q % 1) === 0.5 ? (Math.abs(floor) % 2 === 0 ? floor : Math.ceil(q)) : Math.round(q);
  return x - y * rounded;
}

function trackLengthOf(target) {
  return target.geometry.trackLength;
}
