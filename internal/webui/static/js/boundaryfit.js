// Port of CalibrationBoundaryFitter.cs: fits separated half-disc boundaries
// without inventing turns between them.

import { CalibrationGeometry } from "./geometry.js";
import { CalibrationCrossModel } from "./crossmodel.js";
import { CalibrationFitCandidate, CalibrationFitResult } from "./fit.js";

export const BOUNDARY_MIN_SEGMENTS = 4;
export const BOUNDARY_MIN_POINTS_PER_SEGMENT = 3;
export const BOUNDARY_MIN_CONTINUOUS_POINTS = 6;
export const BOUNDARY_MIN_EXTENDED_SEGMENTS = 2;
export const BOUNDARY_MIN_EXTENDED_COVERAGE = 0.10;
export const BOUNDARY_MIN_COVERAGE = 0.55;
export const BOUNDARY_MAX_RADIUS_RANGE_MM = 0.05;

function normalizeAngle(angle) { return Math.atan2(Math.sin(angle), Math.cos(angle)); }
function radiusOf(point) { return Math.sqrt(point.x * point.x + point.y * point.y); }
function halfTurn(angle) { return 0.5 * Math.atan2(Math.sin(2 * angle), Math.cos(2 * angle)); }

// A quick UI-level gate; Fit performs the physical checks.
export function boundaryHasEnoughPoints(segments) {
  const nonempty = segments.filter((s) => s.points.length > 0);
  return nonempty.length > 0 && nonempty.every((s) => s.points.length >= BOUNDARY_MIN_POINTS_PER_SEGMENT) &&
    (nonempty.length >= BOUNDARY_MIN_SEGMENTS || nonempty.some((s) => s.points.length >= BOUNDARY_MIN_CONTINUOUS_POINTS));
}

function localScale(radii, angles, index) {
  // Local quadratic in observed polar coordinates.
  const nearby = radii.map((r, i) => i)
    .sort((a, b) => Math.abs(radii[a] - radii[index]) - Math.abs(radii[b] - radii[index]))
    .slice(0, 5);
  const meanRadius = nearby.reduce((s, i) => s + radii[i], 0) / nearby.length;
  const meanAngle = nearby.reduce((s, i) => s + angles[i], 0) / nearby.length;
  let xx = 0, xxx = 0, xxxx = 0, xy = 0, xxy = 0;
  for (const i of nearby) {
    const x = radii[i] - meanRadius, y = angles[i] - meanAngle, x2 = x * x;
    xx += x2; xxx += x2 * x; xxxx += x2 * x2; xy += x * y; xxy += x2 * y;
  }
  const curvatureVariance = xxxx - xx * xx / nearby.length;
  const determinant = xx * curvatureVariance - xxx * xxx;
  let slope = xy / Math.max(xx, 1e-15);
  if (determinant > Math.max(1e-20, xx * curvatureVariance * 1e-8)) {
    const linear = (xy * curvatureVariance - xxy * xxx) / determinant;
    const quadratic = (xx * xxy - xy * xxx) / determinant;
    slope = linear + 2 * quadratic * (radii[index] - meanRadius);
  }
  return Math.max(0.025, radii[index] / Math.sqrt(1 + radii[index] * radii[index] * slope * slope));
}

function prepare(segments, continuousIds, extendedInput) {
  const complete = segments.filter((s) => s.points.length > 0);
  if ((continuousIds.size === 0 && !extendedInput && complete.length < BOUNDARY_MIN_SEGMENTS) ||
      complete.some((s) => s.points.length < BOUNDARY_MIN_POINTS_PER_SEGMENT)) {
    return { failure: "For a continuous boundary, trace at least 6 points over 55% of the radius range; or two long pieces each covering 10% with at least 6 points; or at least 4 short segments of at least 3 points." };
  }
  const data = [];
  for (let group = 0; group < complete.length; group++) {
    // The saved order is the user's editable polyline; build a private radial
    // order for phase unwrapping and local tangents.
    const points = complete[group].points.filter((p, i) => i === 0 || p.x !== complete[group].points[i - 1].x || p.y !== complete[group].points[i - 1].y)
      .slice()
      .sort((a, b) => radiusOf(a) - radiusOf(b) || a.x - b.x || a.y - b.y);
    if (points.length < BOUNDARY_MIN_POINTS_PER_SEGMENT) {
      return { failure: "Each segment needs at least 3 distinct points; spread them along the same boundary piece." };
    }
    const radii = points.map(radiusOf);
    const angles = new Array(points.length);
    angles[0] = Math.atan2(points[0].y, points[0].x) + Math.PI / 2;
    for (let i = 1; i < points.length; i++) {
      const change = normalizeAngle(Math.atan2(points[i].y, points[i].x) - Math.atan2(points[i - 1].y, points[i - 1].x));
      if (Math.abs(change) > Math.PI * 0.95) {
        return { failure: "A segment has a near-half-turn gap between two points; add points on the same visible boundary piece, or start a new segment where the connection is not visible." };
      }
      angles[i] = angles[i - 1] + change;
    }
    const meanRadius = radii.reduce((a, b) => a + b, 0) / radii.length;
    const meanAngle = angles.reduce((a, b) => a + b, 0) / angles.length;
    let xx = 0, xy = 0, yy = 0;
    for (let i = 0; i < points.length; i++) {
      const x = radii[i] - meanRadius, y = meanRadius * (angles[i] - meanAngle);
      xx += x * x; xy += x * y; yy += y * y;
    }
    if (Math.max(...radii) - Math.min(...radii) < 0.03 || Math.sqrt((xx + yy) / points.length) < 0.3) {
      return { failure: "A segment is too short to judge the boundary's local direction; spread its points further apart." };
    }
    const tangent = 0.5 * Math.atan2(2 * xy, xx - yy);
    const scale = Math.max(0.025, meanRadius * Math.abs(Math.cos(tangent)));
    const localTangent = points.length >= BOUNDARY_MIN_CONTINUOUS_POINTS && Math.max(...radii) - Math.min(...radii) >= 1;
    for (let i = 0; i < points.length; i++) {
      data.push({
        segment: group,
        radius: radii[i],
        angle: angles[i],
        scale: localTangent ? localScale(radii, angles, i) : scale,
        weight: 1 / (complete.length * points.length),
      });
    }
  }
  return { data };
}

function independentWeights(data) {
  const groupIds = [...new Set(data.map((d) => d.segment))].sort((a, b) => a - b);
  const groups = groupIds.map((id) => data.filter((d) => d.segment === id));
  const low = groups.map((g) => Math.min(...g.map((p) => p.radius)));
  const high = groups.map((g) => Math.max(...g.map((p) => p.radius)));
  const parent = groups.map((_, i) => i);
  const root = (i) => { while (parent[i] !== i) i = parent[i]; return i; };
  for (let a = 0; a < groups.length; a++) {
    for (let b = a + 1; b < groups.length; b++) {
      const overlap = Math.min(high[a], high[b]) - Math.max(low[a], low[b]);
      if (overlap > 0.25 * Math.min(high[a] - low[a], high[b] - low[b])) {
        const ra = root(a), rb = root(b);
        parent[rb] = ra;
      }
    }
  }
  const regions = new Map();
  groups.forEach((_, i) => {
    const r = root(i);
    if (!regions.has(r)) regions.set(r, []);
    regions.get(r).push(i);
  });
  const independentRegions = regions.size;
  const groupWeight = new Array(groups.length);
  for (const region of regions.values()) {
    for (const group of region) {
      groupWeight[group] = 1 / (independentRegions * region.length * groups[group].length);
    }
  }
  return { data: data.map((d) => ({ ...d, weight: groupWeight[d.segment] })), independentRegions };
}

function effectivePointCount(data) {
  const clusters = [];
  for (const point of data) {
    const x = point.radius * Math.cos(point.angle), y = point.radius * Math.sin(point.angle);
    const found = clusters.findIndex((c) => (c.x - x) * (c.x - x) + (c.y - y) * (c.y - y) <= 0.2 * 0.2);
    if (found < 0) clusters.push({ x, y, weight: point.weight });
    else clusters[found].weight += point.weight;
  }
  return 1 / clusters.reduce((s, c) => s + c.weight * c.weight, 0);
}

function buildFastGroups(data, count, origin, inner, outer) {
  const result = [];
  for (let group = 0; group < count; group++) {
    const indices = data.map((d, i) => i).filter((i) => data[i].segment === group);
    let weight = 0, mean = 0, mi = 0, mo = 0;
    for (const i of indices) {
      const w = data[i].weight * data[i].scale * data[i].scale;
      weight += w; mean += w * (data[i].angle - origin[i]); mi -= w * inner[i]; mo -= w * outer[i];
    }
    mean /= weight; mi /= weight; mo /= weight;
    let uu = 0, ui = 0, uo = 0, ii = 0, io = 0, oo = 0;
    for (const i of indices) {
      const w = data[i].weight * data[i].scale * data[i].scale;
      const u = data[i].angle - origin[i] - mean, v = -inner[i] - mi, z = -outer[i] - mo;
      uu += w * u * u; ui += w * u * v; uo += w * u * z;
      ii += w * v * v; io += w * v * z; oo += w * z * z;
    }
    result.push({ weight, mean, inner: mi, outer: mo, uu, ui, uo, ii, io, oo });
  }
  return result;
}

function fastCost(groups, inner, outer) {
  let cosine = 0, sine = 0, totalWeight = 0, local = 0;
  for (const g of groups) {
    const mean = g.mean + g.inner * inner + g.outer * outer;
    cosine += g.weight * Math.cos(2 * mean);
    sine += g.weight * Math.sin(2 * mean);
    totalWeight += g.weight;
    local += g.uu + 2 * inner * g.ui + 2 * outer * g.uo + inner * inner * g.ii +
      2 * inner * outer * g.io + outer * outer * g.oo;
  }
  const rotation = 0.5 * Math.atan2(sine, cosine);
  let sum = 0, squared = 0;
  for (const g of groups) {
    let mean = g.mean + g.inner * inner + g.outer * outer;
    mean -= Math.PI * Math.round((mean - rotation) / Math.PI);
    sum += g.weight * mean;
    squared += g.weight * mean * mean;
  }
  return Math.max(0, local + squared - sum * sum / totalWeight);
}

function gridCount(width, derivative) {
  const spread = Math.max(...derivative) - Math.min(...derivative);
  return Math.max(17, 1 + Math.ceil(width * spread / (Math.PI / 4)));
}

function refine(seed, data, delta, evaluate) {
  let current = seed;
  let damping = 1e-4;
  for (let iteration = 0; iteration < 60 && Number.isFinite(current.cost); iteration++) {
    const h = 1e-6;
    let hi = h, ho = h;
    let a = evaluate(current.inner + hi, current.outer);
    let b = evaluate(current.inner, current.outer + ho);
    if (!Number.isFinite(a.cost)) { hi = -h; a = evaluate(current.inner + hi, current.outer); }
    if (!Number.isFinite(b.cost)) { ho = -h; b = evaluate(current.inner, current.outer + ho); }
    if (!Number.isFinite(a.cost) || !Number.isFinite(b.cost)) break;
    let ii = 0, io = 0, oo = 0, ix = 0, ox = 0;
    for (let i = 0; i < data.length; i++) {
      const error = current.residuals[i];
      const w = data[i].weight * (Math.abs(error) <= delta ? 1 : delta / Math.abs(error));
      const di = (a.residuals[i] - error) / hi, dO = (b.residuals[i] - error) / ho;
      ii += w * di * di; io += w * di * dO; oo += w * dO * dO;
      ix -= w * di * error; ox -= w * dO * error;
    }
    ii *= 1 + damping; oo *= 1 + damping;
    const determinant = ii * oo - io * io;
    if (!Number.isFinite(determinant) || determinant <= 1e-20) break;
    const si = (ix * oo - ox * io) / determinant, so = (ox * ii - ix * io) / determinant;
    if (Math.max(Math.abs(si), Math.abs(so)) < 1e-9) break;
    const proposed = evaluate(current.inner + si, current.outer + so);
    if (proposed.cost < current.cost) {
      const improvement = current.cost - proposed.cost;
      current = proposed;
      damping = Math.max(1e-10, damping / 3);
      if (improvement < 1e-16 || Math.max(Math.abs(si), Math.abs(so)) < 1e-10) break;
    } else {
      damping *= 8;
      if (damping > 1e12) break;
    }
  }
  return current;
}

// Fits separated half-disc boundaries. target: Target (stage boundary).
// segments: [{id, points: [{x, y}...]}] in rectified disc-centred mm.
// options: {searchCenter?, radiusRangeMm=0.01, huberDeltaMm=0.8, maxCandidates=8}
export function fitBoundary(target, segments, options = {}) {
  const failed = (warning) => new CalibrationFitResult(false, null, 0, NaN, [], [warning]);
  const radiusRangeMm = options.radiusRangeMm ?? 0.01;
  const huberDelta = Math.min(0.15, options.huberDeltaMm ?? 0.8);
  const maxCandidates = options.maxCandidates ?? 8;
  if (target.stage !== "boundary") return failed("Boundary fitting needs a boundary-stage target record.");
  if (radiusRangeMm > BOUNDARY_MAX_RADIUS_RANGE_MM)
    return failed("The boundary radius search range cannot exceed ±0.05 mm; finish the coarse stage first.");

  const drawingWidth = target.drawingOuterRadiusMm - target.drawingInnerRadiusMm;
  const continuousIds = new Set(segments
    .filter((s) => new Set(s.points).size >= BOUNDARY_MIN_CONTINUOUS_POINTS)
    .filter((s) => Math.max(...s.points.map(radiusOf)) - Math.min(...s.points.map(radiusOf)) >= drawingWidth * BOUNDARY_MIN_COVERAGE)
    .map((s) => s.id));
  const extendedIds = new Set(segments
    .filter((s) => new Set(s.points).size >= BOUNDARY_MIN_CONTINUOUS_POINTS)
    .filter((s) => Math.max(...s.points.map(radiusOf)) - Math.min(...s.points.map(radiusOf)) >= drawingWidth * BOUNDARY_MIN_EXTENDED_COVERAGE)
    .map((s) => s.id));
  const extendedPieces = segments.filter((s) => extendedIds.has(s.id));
  const extendedInput = extendedPieces.some((a) => extendedPieces.some((b) => a.id !== b.id &&
    (Math.max(...a.points.map(radiusOf)) <= Math.min(...b.points.map(radiusOf)) ||
     Math.max(...b.points.map(radiusOf)) <= Math.min(...a.points.map(radiusOf)))));

  const prepared = prepare(segments, continuousIds, extendedInput);
  if (prepared.failure) return failed(prepared.failure);
  let data = prepared.data;
  const coverage = (Math.max(...data.map((d) => d.radius)) - Math.min(...data.map((d) => d.radius))) / drawingWidth;
  if (coverage < BOUNDARY_MIN_COVERAGE)
    return failed("The boundary segments must cover at least 55% of the drawing radius range; add segments further in and out.");
  const radialCenters = [...new Set(data.map((d) => d.segment))]
    .map((id) => data.filter((d) => d.segment === id))
    .map((g) => g.reduce((s, d) => s + d.radius, 0) / g.length)
    .sort((a, b) => a - b);
  let separateRadii = 1;
  let previousRadius = radialCenters[0];
  for (const radius of radialCenters.slice(1)) {
    if (radius - previousRadius >= 0.5) { separateRadii++; previousRadius = radius; }
  }
  if (continuousIds.size === 0 && !extendedInput && separateRadii < BOUNDARY_MIN_SEGMENTS)
    return failed("The segments cluster at a few similar radii; pick boundary pieces at least at 4 distinct radii and avoid retracing the same piece.");
  const independent = independentWeights(data);
  data = independent.data;
  const independentRegions = independent.independentRegions;
  if (continuousIds.size === 0 && independentRegions < (extendedInput ? BOUNDARY_MIN_EXTENDED_SEGMENTS : BOUNDARY_MIN_SEGMENTS))
    return failed("Multiple segments cover the same radius range; pick segments at least at 4 non-overlapping radii.");

  // Preserve the written pitch profile: this stage resolves phase winding and radii.
  const center = {
    ...(options.searchCenter ?? target.parameters),
    pitchLinear: target.parameters.pitchLinear,
    pitchQuadratic: target.parameters.pitchQuadratic,
    pitchCubic: target.parameters.pitchCubic,
  };
  const generated = new CalibrationGeometry(target.geometryParameters);
  const lowerInner = Math.max(0.1, center.innerRadiusMm - radiusRangeMm);
  const upperInner = center.innerRadiusMm + radiusRangeMm;
  const lowerOuter = Math.max(lowerInner + 0.1, center.outerRadiusMm - radiusRangeMm);
  const upperOuter = center.outerRadiusMm + radiusRangeMm;
  const delta = huberDelta;
  const groupCount = Math.max(...data.map((d) => d.segment)) + 1;

  const phases = (inner, outer) => {
    if (inner < lowerInner || inner > upperInner || outer < lowerOuter || outer > upperOuter || outer <= inner) return null;
    const actual = { ...center, innerRadiusMm: inner, outerRadiusMm: outer };
    let geometry;
    try { geometry = new CalibrationGeometry(actual); } catch { return null; }
    const model = new CalibrationCrossModel(generated, geometry, target.parameters.kind, false, target);
    const result = new Array(data.length);
    for (let i = 0; i < data.length; i++) {
      const sample = model.tryAtRadius(data[i].radius);
      if (sample === null) return null;
      result[i] = sample.phase;
    }
    return result;
  };

  const invalid = (inner, outer) => ({ inner, outer, rotation: 0, cost: Number.POSITIVE_INFINITY, rms: Number.POSITIVE_INFINITY, residuals: [] });
  const evaluate = (inner, outer) => {
    const ph = phases(inner, outer);
    if (ph === null) return invalid(inner, outer);
    const angles = new Array(data.length);
    const means = new Array(groupCount).fill(0);
    const weights = new Array(groupCount).fill(0);
    for (let i = 0; i < data.length; i++) {
      const point = data[i];
      angles[i] = point.angle - ph[i];
      const w = point.weight * point.scale * point.scale;
      means[point.segment] += angles[i] * w;
      weights[point.segment] += w;
    }
    let cosine = 0, sine = 0;
    for (let group = 0; group < groupCount; group++) {
      means[group] /= weights[group];
      cosine += weights[group] * Math.cos(2 * means[group]);
      sine += weights[group] * Math.sin(2 * means[group]);
    }
    let rotation = 0.5 * Math.atan2(sine, cosine);
    for (let i = 0; i < angles.length; i++)
      angles[i] -= Math.PI * Math.round((means[data[i].segment] - rotation) / Math.PI);
    for (let iteration = 0; iteration < 8; iteration++) {
      let sum = 0, weight = 0;
      for (let i = 0; i < data.length; i++) {
        const error = Math.abs((angles[i] - rotation) * data[i].scale);
        const w = data[i].weight * data[i].scale * data[i].scale * (error <= delta ? 1 : delta / error);
        sum += w * angles[i]; weight += w;
      }
      const updated = sum / weight;
      if (Math.abs(updated - rotation) < 1e-12) break;
      rotation = updated;
    }
    const residuals = new Array(data.length);
    let cost = 0, squared = 0;
    for (let i = 0; i < data.length; i++) {
      const error = residuals[i] = (angles[i] - rotation) * data[i].scale;
      const magnitude = Math.abs(error);
      squared += data[i].weight * error * error;
      cost += data[i].weight * (magnitude <= delta ? error * error / 2 : delta * (magnitude - delta / 2));
    }
    return { inner, outer, rotation, cost, rms: Math.sqrt(squared), residuals };
  };

  // Enumerate all resolved phase wells in the small coarse-calibrated box.
  const differential = 1e-6;
  const origin = phases(center.innerRadiusMm, center.outerRadiusMm);
  const shiftedInner = phases(center.innerRadiusMm + differential, center.outerRadiusMm);
  const shiftedOuter = phases(center.innerRadiusMm, center.outerRadiusMm + differential);
  if (origin === null || shiftedInner === null || shiftedOuter === null)
    return failed("The current radius range has no valid disc model; check the generation record and the photo outlines.");
  const derivativeInner = origin.map((p, i) => (shiftedInner[i] - p) / differential);
  const derivativeOuter = origin.map((p, i) => (shiftedOuter[i] - p) / differential);
  for (const inner of [lowerInner, upperInner]) {
    for (const outer of [lowerOuter, upperOuter]) {
      const corner = phases(inner, outer);
      if (corner === null) return failed("The search boundary leaves the valid disc model; narrow the radius search range.");
      const error = corner.map((p, i) => p - origin[i] -
        (inner - center.innerRadiusMm) * derivativeInner[i] -
        (outer - center.outerRadiusMm) * derivativeOuter[i]);
      if (Math.max(...error) - Math.min(...error) > Math.PI / 8)
        return failed("The angular change over the range is too large to enumerate windings reliably; narrow the radius search range.");
    }
  }
  const fast = buildFastGroups(data, groupCount, origin, derivativeInner, derivativeOuter);
  const columns = gridCount(upperInner - lowerInner, derivativeInner);
  const rows = gridCount(upperOuter - lowerOuter, derivativeOuter);
  if (columns * rows > 800000)
    return failed("The range contains too many winding candidates; narrow the radius search range.");
  const grid = new Float64Array(rows * columns);
  const stepInner = (upperInner - lowerInner) / (columns - 1);
  const stepOuter = (upperOuter - lowerOuter) / (rows - 1);
  for (let row = 0; row < rows; row++) {
    const outer = lowerOuter + row * stepOuter - center.outerRadiusMm;
    for (let column = 0; column < columns; column++)
      grid[row * columns + column] = fastCost(fast, lowerInner + column * stepInner - center.innerRadiusMm, outer);
  }
  const starts = [];
  for (let row = 0; row < rows; row++) {
    for (let column = 0; column < columns; column++) {
      const cost = grid[row * columns + column];
      let minimum = true;
      for (let y = Math.max(0, row - 1); y <= Math.min(rows - 1, row + 1) && minimum; y++) {
        for (let x = Math.max(0, column - 1); x <= Math.min(columns - 1, column + 1); x++) {
          if (grid[y * columns + x] < cost) { minimum = false; break; }
        }
      }
      if (minimum) starts.push({ cost, inner: lowerInner + column * stepInner, outer: lowerOuter + row * stepOuter });
    }
  }
  if (starts.length > 8192)
    return failed("The search range has too many feasible winding regions; narrow the radius range or add a longer boundary segment.");
  const solutions = [];
  for (const start of starts.sort((a, b) => a.cost - b.cost)) {
    const solution = refine(evaluate(start.inner, start.outer), data, delta, evaluate);
    if (!Number.isFinite(solution.cost)) continue;
    const duplicate = solutions.findIndex((s) => Math.max(Math.abs(s.inner - solution.inner), Math.abs(s.outer - solution.outer)) < 1e-6);
    if (duplicate < 0) solutions.push(solution);
    else if (solutions[duplicate].cost > solution.cost) solutions[duplicate] = solution;
  }
  solutions.sort((a, b) => a.cost - b.cost);
  if (solutions.length === 0) return failed("These boundary segments have no valid parameters; check that the points are on the black/white boundary.");
  const best = solutions[0];
  const candidates = solutions.slice(0, maxCandidates).map((s) => new CalibrationFitCandidate(
    { ...center, innerRadiusMm: s.inner, outerRadiusMm: s.outer },
    halfTurn(s.rotation), s.rms, s.cost,
    Math.max(...s.residuals.map(Math.abs)),
    s.residuals.filter((e) => Math.abs(e) > 3 * delta).length));
  const boundaryLimited = Math.min(Math.min(best.inner - lowerInner, upperInner - best.inner),
    Math.min(best.outer - lowerOuter, upperOuter - best.outer)) <= radiusRangeMm * 0.002;
  const warnings = [];
  if (boundaryLimited) warnings.push("The best candidate sits on the search boundary; the winding count is not confirmed — recheck the coarse result or widen the range.");
  if (best.rms > 0.15 || Math.max(...best.residuals.map(Math.abs)) > Math.max(0.3, best.rms * 4))
    warnings.push("The boundary segments are inconsistent with one disc model; check for mis-clicks, the rectification, or segments crossing the other boundary.");
  const noise = Math.max(0.02, best.rms);
  const effectiveCount = Math.min(effectivePointCount(data), independentRegions * 5);
  const gap = noise * noise * 12 / Math.max(1, effectiveCount - 3);
  if (solutions.slice(1).some((s) => s.rms * s.rms <= best.rms * best.rms + gap &&
      Math.max(Math.abs(s.inner - best.inner), Math.abs(s.outer - best.outer)) > 0.00015))
    warnings.push("Other winding counts still explain these segments; add a longer clear boundary piece at a different radius and recompute.");
  if (warnings.length > 0) {
    const result = new CalibrationFitResult(false, null, halfTurn(best.rotation), best.rms, candidates, warnings);
    result.boundaryLimited = boundaryLimited;
    return result;
  }
  return new CalibrationFitResult(true, candidates[0].parameters, candidates[0].rotationRadians, best.rms, candidates, []);
}
