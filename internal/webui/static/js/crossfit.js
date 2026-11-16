// Port of CalibrationCrossFitter.cs + CalibrationCubicFitter.cs: fits the
// continuous phase along radial cross-arm traces. One integer turn offset per
// complete trace; individual samples never jump arms or turns.

import { CalibrationGeometry } from "./geometry.js";
import { CalibrationCrossModel } from "./crossmodel.js";
import { CalibrationFitCandidate, CalibrationFitResult } from "./fit.js";

const NEAR_HALF_TURN_WARNING =
  "Some adjacent trace points turn nearly half a turn; the winding judgement may be unreliable — add points and recheck.";

function normalizeAngle(angle) { return Math.atan2(Math.sin(angle), Math.cos(angle)); }

function invalid(inner, outer) {
  return { inner, outer, rotation: 0, cost: Number.POSITIVE_INFINITY, rms: Number.POSITIVE_INFINITY, residuals: [] };
}

function residualStats(residuals, delta) {
  if (residuals.length === 0) return { maxError: 0, outliers: 0 };
  const threshold = Math.max(0.05, delta * 3);
  let maximum = 0, outliers = 0;
  for (const residual of residuals) {
    const error = Math.abs(residual);
    maximum = Math.max(maximum, error);
    if (error > threshold) outliers++;
  }
  return { maxError: maximum, outliers };
}

function medianAbsolute(residuals) {
  if (residuals.length === 0) return 0;
  const magnitudes = residuals.map(Math.abs).sort((a, b) => a - b);
  return magnitudes.length % 2 === 1
    ? magnitudes[magnitudes.length >> 1]
    : (magnitudes[(magnitudes.length >> 1) - 1] + magnitudes[magnitudes.length >> 1]) / 2;
}

function prepare(traces, bandWidth, warnings) {
  const data = [];
  let traceNumber = 0;
  const sorted = traces.slice().sort((a, b) => a.armIndex - b.armIndex);
  for (const trace of sorted) {
    const points = trace.points.filter((point, index) =>
      index === 0 || point.x !== trace.points[index - 1].x || point.y !== trace.points[index - 1].y);
    if (points.length < 2) continue;
    const radii = new Array(points.length);
    const angles = new Array(points.length);
    for (let i = 0; i < points.length; i++) {
      radii[i] = Math.sqrt(points[i].x * points[i].x + points[i].y * points[i].y);
      const angle = Math.atan2(points[i].y, points[i].x);
      if (i === 0) {
        angles[i] = angle - (-Math.PI / 2 + trace.armIndex * Math.PI / 2);
      } else {
        if (radii[i] < radii[i - 1] - Math.max(0.1, bandWidth * 0.005)) {
          return { failure: "The trace visibly steps back inward; mark points along the same line from the inside out." };
        }
        const change = normalizeAngle(angle - Math.atan2(points[i - 1].y, points[i - 1].x));
        if (Math.abs(change) > Math.PI * 0.995) {
          return { failure: "Two adjacent trace points turn by nearly half a turn; add points between them along the actual curve." };
        }
        if (Math.abs(change) > Math.PI * 0.9 && !warnings.includes(NEAR_HALF_TURN_WARNING)) {
          warnings.push(NEAR_HALF_TURN_WARNING);
        }
        angles[i] = angles[i - 1] + change;
      }
    }
    if (radii[radii.length - 1] - radii[0] < bandWidth * 0.02) continue;
    if (points.length > 1024) warnings.push("Many trace points; the fit uses evenly sampled points, and the full order is still used for the angle unwrapping.");
    const count = Math.min(points.length, 1024);
    for (let slot = 0; slot < count; slot++) {
      const i = Math.round(slot * (points.length - 1) / (count - 1));
      const before = Math.max(0, i - 2), after = Math.min(points.length - 1, i + 2);
      let meanRadius = 0, meanAngle = 0;
      for (let neighbor = before; neighbor <= after; neighbor++) {
        meanRadius += radii[neighbor];
        meanAngle += angles[neighbor];
      }
      meanRadius /= after - before + 1;
      meanAngle /= after - before + 1;
      let covariance = 0, variance = 0;
      for (let neighbor = before; neighbor <= after; neighbor++) {
        covariance += (radii[neighbor] - meanRadius) * (angles[neighbor] - meanAngle);
        variance += (radii[neighbor] - meanRadius) * (radii[neighbor] - meanRadius);
      }
      const slope = covariance / Math.max(variance, 1e-12);
      const scale = radii[i] / Math.sqrt(1 + radii[i] * radii[i] * slope * slope);
      const previous = slot === 0 ? 0 : Math.round((slot - 1) * (points.length - 1) / (count - 1));
      const next = slot === count - 1 ? points.length - 1 : Math.round((slot + 1) * (points.length - 1) / (count - 1));
      const weight = Math.max(1e-8, radii[next] - radii[previous]) / (radii[radii.length - 1] - radii[0]);
      data.push({ trace: traceNumber, radius: radii[i], angle: angles[i], angularScale: scale, weight });
    }
    traceNumber++;
  }
  return { data };
}

function refineSolution(initial, data, delta, evaluate) {
  let current = initial;
  let damping = 1e-4;
  for (let iteration = 0; iteration < 80; iteration++) {
    const jacobian = jacobianOf(current, evaluate);
    if (jacobian === null) break;
    const [first, second] = jacobian;
    let a = 0, b = 0, c = 0, x = 0, y = 0;
    for (let i = 0; i < data.length; i++) {
      const error = Math.abs(current.residuals[i]);
      const weight = data[i].weight * (error <= delta ? 1 : delta / error);
      a += weight * first[i] * first[i];
      b += weight * first[i] * second[i];
      c += weight * second[i] * second[i];
      x -= weight * first[i] * current.residuals[i];
      y -= weight * second[i] * current.residuals[i];
    }
    a *= 1 + damping;
    c *= 1 + damping;
    const determinant = a * c - b * b;
    if (!Number.isFinite(determinant) || determinant <= 1e-20) break;
    const innerStep = (x * c - y * b) / determinant;
    const outerStep = (y * a - x * b) / determinant;
    const proposed = evaluate(current.inner + innerStep, current.outer + outerStep);
    if (proposed.cost < current.cost) {
      const improvement = current.cost - proposed.cost;
      current = proposed;
      damping = Math.max(1e-10, damping / 3);
      if (improvement < 1e-15 || Math.max(Math.abs(innerStep), Math.abs(outerStep)) < 1e-10) break;
    } else {
      damping *= 8;
      if (damping > 1e12) break;
    }
  }
  return current;
}

function jacobianOf(current, evaluate) {
  const step = 1e-6;
  const first = new Array(current.residuals.length);
  const second = new Array(current.residuals.length);
  let shiftedInner = evaluate(current.inner + step, current.outer);
  let shiftedOuter = evaluate(current.inner, current.outer + step);
  let innerStep = step, outerStep = step;
  if (!Number.isFinite(shiftedInner.cost)) { innerStep = -step; shiftedInner = evaluate(current.inner - step, current.outer); }
  if (!Number.isFinite(shiftedOuter.cost)) { outerStep = -step; shiftedOuter = evaluate(current.inner, current.outer - step); }
  if (!Number.isFinite(shiftedInner.cost) || !Number.isFinite(shiftedOuter.cost)) return null;
  for (let i = 0; i < first.length; i++) {
    first[i] = (shiftedInner.residuals[i] - current.residuals[i]) / innerStep;
    second[i] = (shiftedOuter.residuals[i] - current.residuals[i]) / outerStep;
  }
  return [first, second];
}

function parameterCorrelation(solution, data, evaluate) {
  const jacobian = jacobianOf(solution, evaluate);
  if (jacobian === null) return 1;
  const [first, second] = jacobian;
  let a = 0, b = 0, c = 0;
  for (let i = 0; i < data.length; i++) {
    a += data[i].weight * first[i] * first[i];
    b += data[i].weight * first[i] * second[i];
    c += data[i].weight * second[i] * second[i];
  }
  return Math.abs(b) / Math.sqrt(Math.max(1e-30, a * c));
}

function* estimateShapeSeeds(data, traceCount, generated, actual) {
  const weights = new Array(traceCount).fill(0);
  const radii = new Array(traceCount).fill(0);
  const inverses = new Array(traceCount).fill(0);
  const angles = new Array(traceCount).fill(0);
  let totalWeight = 0, radiusSum = 0;
  for (const point of data) {
    const weight = point.weight * point.angularScale * point.angularScale;
    weights[point.trace] += weight;
    radii[point.trace] += weight * point.radius;
    inverses[point.trace] += weight / point.radius;
    angles[point.trace] += weight * point.angle;
    totalWeight += weight;
    radiusSum += weight * point.radius;
  }
  const radiusScale = radiusSum / totalWeight;
  if (!Number.isFinite(radiusScale) || radiusScale <= 0) return;
  for (let trace = 0; trace < traceCount; trace++) {
    if (!Number.isFinite(weights[trace]) || weights[trace] <= 0) return;
    radii[trace] /= weights[trace];
    inverses[trace] /= weights[trace];
    angles[trace] /= weights[trace];
  }
  let uu = 0, uv = 0, vv = 0, uy = 0;
  for (const point of data) {
    const weight = point.weight * point.angularScale * point.angularScale;
    const u = (point.radius - radii[point.trace]) / radiusScale;
    const v = (1 / point.radius - inverses[point.trace]) * radiusScale;
    const y = point.angle - angles[point.trace];
    uu += weight * u * u; uv += weight * u * v;
    vv += weight * v * v; uy += weight * u * y;
  }
  if (!Number.isFinite(uu) || !Number.isFinite(vv) || uu <= 0 || vv <= 0) return;
  const projection = uv / uu;
  let perpendicularSquared = 0, perpendicularY = 0;
  for (const point of data) {
    const weight = point.weight * point.angularScale * point.angularScale;
    const u = (point.radius - radii[point.trace]) / radiusScale;
    const v = (1 / point.radius - inverses[point.trace]) * radiusScale - projection * u;
    perpendicularSquared += weight * v * v;
    perpendicularY += weight * v * (point.angle - angles[point.trace]);
  }
  if (!Number.isFinite(perpendicularSquared) || perpendicularSquared <= vv * 1e-14) return;
  const inverseCoefficient = perpendicularY / perpendicularSquared;
  const alpha = (uy - inverseCoefficient * uv) / (uu * radiusScale);
  const beta = inverseCoefficient * radiusScale;
  const bg = (generated.outerRadiusMm - generated.innerRadiusMm) *
    (generated.outerRadiusMm + generated.innerRadiusMm) / (2 * trackLengthOf(generated));
  const eta = trackLengthOf(actual) / trackLengthOf(generated);
  const discriminant = 1 + 4 * eta * alpha * bg;
  if (!Number.isFinite(alpha) || !Number.isFinite(beta) || !Number.isFinite(discriminant) || discriminant < 0) return;
  const q = (1 + Math.sqrt(discriminant)) / 2;
  for (const z of [q / eta, -alpha * bg / q]) {
    if (!Number.isFinite(z) || z <= 0) continue;
    const k = z * z, d = -2 * z * bg * beta;
    const innerSquared = (generated.innerRadiusMm * generated.innerRadiusMm - d) / k;
    const outerSquared = (generated.outerRadiusMm * generated.outerRadiusMm - d) / k;
    if (!Number.isFinite(innerSquared) || !Number.isFinite(outerSquared) ||
        innerSquared <= 0 || outerSquared <= innerSquared) continue;
    yield { inner: Math.sqrt(innerSquared), outer: Math.sqrt(outerSquared) };
  }
}

function trackLengthOf(p) { return p.trackLength ?? new CalibrationGeometry(p).trackLength; }

function onBoundary(solution, lowerInner, upperInner, lowerOuter, upperOuter, range) {
  return Math.min(solution.inner - lowerInner, upperInner - solution.inner) < range * 0.005 ||
    Math.min(solution.outer - lowerOuter, upperOuter - solution.outer) < range * 0.005;
}

// Fits the fine-stage cross traces.
// traces: [{armIndex, points: [{x, y}...]}]; options:
// {searchCenter?, radiusRangeMm=0.02, huberDeltaMm=0.8, maxCandidates=8,
//  fitPitch=true, pitchCoefficientRange=0.05, adaptiveRobustScale=false}
export function fitCross(target, traces, options = {}) {
  const failed = (reason) => ({ result: new CalibrationFitResult(false, null, 0, 0, [], [reason]), residuals: [] });
  const radiusRangeMm = options.radiusRangeMm ?? 0.02;
  const huberDeltaMm = options.huberDeltaMm ?? 0.8;
  const maxCandidates = options.maxCandidates ?? 8;
  const fitPitch = options.fitPitch ?? true;
  const adaptiveRobustScale = options.adaptiveRobustScale ?? false;

  const band = target.parameters.outerRadiusMm - target.parameters.innerRadiusMm;
  const inputWarnings = [];
  const prepared = prepare(traces, band, inputWarnings);
  if (prepared.failure) return failed(prepared.failure).result;
  const data = prepared.data;
  if (data.length < 6)
    return failed("Trace at least 6 points along the same curve from the inside out, covering inner, middle and outer positions.").result;
  const coverage = (Math.max(...data.map((p) => p.radius)) - Math.min(...data.map((p) => p.radius))) / band;
  if (coverage < 0.18)
    return failed("The trace coverage is too narrow to separate inner and outer radius; extend the trace further in and out.").result;

  const first = solveCross(target, options, data, coverage, inputWarnings, huberDeltaMm,
    radiusRangeMm, maxCandidates, fitPitch);
  if (!adaptiveRobustScale || !first.result.succeeded || first.residuals.length === 0) return first.result;
  const median = medianAbsolute(first.residuals);
  if (first.result.rmsErrorMm <= median * 2.5) return first.result;
  const adaptiveDelta = Math.min(Math.max(3 * 1.4826 * median, 0.01), huberDeltaMm);
  if (adaptiveDelta >= huberDeltaMm * 0.75) return first.result;
  const tightened = solveCross(target, options, data, coverage, inputWarnings, adaptiveDelta,
    radiusRangeMm, maxCandidates, fitPitch);
  return tightened.result.succeeded ? tightened.result : first.result;
}

function solveCross(target, options, data, coverage, inputWarnings, delta,
    radiusRangeMm, maxCandidates, fitPitch) {
  const failed = (reason) => ({ result: new CalibrationFitResult(false, null, 0, NaN, [], [reason]), residuals: [] });
  const warnings = [...inputWarnings];
  const center = options.searchCenter ?? target.parameters;
  const original = new CalibrationGeometry(target.geometryParameters);
  const lowerInner = Math.max(0.1, center.innerRadiusMm - radiusRangeMm);
  const upperInner = center.innerRadiusMm + radiusRangeMm;
  const lowerOuter = Math.max(lowerInner + 0.1, center.outerRadiusMm - radiusRangeMm);
  const upperOuter = center.outerRadiusMm + radiusRangeMm;
  const traceCount = Math.max(...data.map((point) => point.trace)) + 1;
  const centerTrackLength = trackLengthOf(center);

  const evaluate = (inner, outer) => {
    if (inner < lowerInner || inner > upperInner || outer < lowerOuter || outer > upperOuter ||
        outer <= inner || centerTrackLength <= outer - inner) return invalid(inner, outer);
    let geometry;
    try {
      geometry = new CalibrationGeometry({ ...center, innerRadiusMm: inner, outerRadiusMm: outer });
    } catch { return invalid(inner, outer); }
    const identical = parametersEqual(target.geometryParameters,
      { ...center, innerRadiusMm: inner, outerRadiusMm: outer });
    const model = new CalibrationCrossModel(original, geometry, target.parameters.kind, identical, null);
    const angles = new Array(data.length);
    const means = new Array(traceCount).fill(0);
    const meanWeights = new Array(traceCount).fill(0);
    for (let i = 0; i < data.length; i++) {
      const point = data[i];
      const predicted = model.tryAtRadius(point.radius);
      if (predicted === null) return invalid(inner, outer);
      angles[i] = point.angle - predicted.phase;
      const weight = point.weight * point.angularScale * point.angularScale;
      means[point.trace] += weight * angles[i];
      meanWeights[point.trace] += weight;
    }
    let cosine = 0, sine = 0;
    for (let trace = 0; trace < traceCount; trace++) {
      means[trace] /= meanWeights[trace];
      cosine += Math.cos(means[trace]);
      sine += Math.sin(means[trace]);
    }
    let rotation = Math.atan2(sine, cosine);
    for (let i = 0; i < angles.length; i++)
      angles[i] += 2 * Math.PI * Math.round((rotation - means[data[i].trace]) / (2 * Math.PI));
    for (let iteration = 0; iteration < 10; iteration++) {
      let sum = 0, weightSum = 0;
      for (let i = 0; i < data.length; i++) {
        const point = data[i];
        const error = Math.abs((angles[i] - rotation) * point.angularScale);
        const weight = point.weight * point.angularScale * point.angularScale *
          (error <= delta ? 1 : delta / error);
        sum += weight * angles[i];
        weightSum += weight;
      }
      const updated = sum / weightSum;
      if (Math.abs(updated - rotation) < 1e-12) break;
      rotation = updated;
    }
    const residuals = new Array(data.length);
    let cost = 0, squared = 0, weights = 0;
    for (let i = 0; i < data.length; i++) {
      const residual = (angles[i] - rotation) * data[i].angularScale;
      residuals[i] = residual;
      const error = Math.abs(residual);
      cost += data[i].weight * (error <= delta ? 0.5 * error * error : delta * (error - delta / 2));
      squared += data[i].weight * residual * residual;
      weights += data[i].weight;
    }
    return { inner, outer, rotation, cost: cost / weights, rms: Math.sqrt(squared / weights), residuals };
  };

  const shapeSeeds = [...estimateShapeSeeds(data, traceCount, target.geometryParameters, center)]
    .map((seed) => evaluate(
      Math.min(Math.max(seed.inner, lowerInner), upperInner),
      Math.min(Math.max(seed.outer, lowerOuter), upperOuter)))
    .filter((seed) => Number.isFinite(seed.cost));

  const gridSize = radiusRangeMm <= 0.05 ? 9 : 17;
  const seeds = [evaluate(center.innerRadiusMm, center.outerRadiusMm)];
  for (let row = 0; row < gridSize; row++) {
    for (let column = 0; column < gridSize; column++) {
      seeds.push(evaluate(lowerInner + (upperInner - lowerInner) * row / (gridSize - 1),
        lowerOuter + (upperOuter - lowerOuter) * column / (gridSize - 1)));
    }
  }
  const solutions = [];
  const allSeeds = shapeSeeds.concat(seeds.filter((seed) => Number.isFinite(seed.cost))
    .sort((a, b) => a.cost - b.cost).slice(0, 16));
  for (const seed of allSeeds) {
    const result = refineSolution(seed, data, delta, evaluate);
    const duplicate = solutions.findIndex((other) => Math.abs(other.inner - result.inner) < 1e-6 &&
      Math.abs(other.outer - result.outer) < 1e-6);
    if (duplicate < 0) solutions.push(result);
    else if (result.cost < solutions[duplicate].cost) solutions[duplicate] = result;
  }
  solutions.sort((a, b) => a.cost - b.cost);
  if (solutions.length === 0)
    return failed("No valid positive-radius curve continuation covers all observed points in the search range; check the photo scale, the disc centre and the parameter range.");
  const best = solutions[0];
  const boundaryLimited = onBoundary(best, lowerInner, upperInner, lowerOuter, upperOuter, radiusRangeMm);
  if (coverage < 0.6) warnings.push("The trace covers a narrow inner-to-outer range; parameters may compensate each other — add points further in and out.");
  if (best.rms > delta * 2) warnings.push("The curve deviation is large; check the rectification, the points, and that you followed the same line throughout.");
  if (boundaryLimited) warnings.push("The best result is near the search boundary; widen the search range and add trace points to recheck.");
  if (parameterCorrelation(best, data, evaluate) > 0.998)
    warnings.push("The trace constrains the two radii too similarly to judge uniqueness from the residual alone; widen the radial coverage and check point accuracy.");
  if (solutions.slice(1).some((solution) => solution.cost <= best.cost + Math.max(0.005, best.cost * 0.25) &&
      (Math.abs(solution.inner - best.inner) > 0.0002 || Math.abs(solution.outer - best.outer) > 0.0002)))
    warnings.push("Another parameter candidate fits the shape similarly; compare against the whole curve before applying.");
  if (radiusRangeMm > 0.05)
    warnings.push("A wide range only gets a limited multi-start search; the candidate list is not exhaustive.");
  warnings.push("The angle unwrapping assumes no missed full turn between adjacent points; check the whole curve and add points at bends before confirming.");
  const candidates = solutions.slice(0, maxCandidates).map((solution) => {
    const { maxError, outliers } = residualStats(solution.residuals, delta);
    return new CalibrationFitCandidate(
      { ...center, innerRadiusMm: solution.inner, outerRadiusMm: solution.outer },
      normalizeAngle(solution.rotation), solution.rms, solution.cost, maxError, outliers);
  });
  if (fitPitch) {
    return fitCubic(target, data, coverage, { ...options, radiusRangeMm, huberDeltaMm: delta, maxCandidates },
      candidates, warnings, best.residuals, delta, boundaryLimited, center, lowerInner, upperInner, lowerOuter, upperOuter);
  }
  const fitResult = new CalibrationFitResult(true, candidates[0].parameters, candidates[0].rotationRadians,
    best.rms, candidates, warnings);
  fitResult.boundaryLimited = boundaryLimited;
  return { result: fitResult, residuals: best.residuals };
}

// ---- Cubic pitch fit (CalibrationCubicFitter.cs) ----

function cubicToVector(p) {
  return [p.innerRadiusMm, p.outerRadiusMm, p.pitchLinear, p.pitchQuadratic, p.pitchCubic];
}

function cubicFromVector(p, values) {
  return {
    ...p,
    innerRadiusMm: values[0], outerRadiusMm: values[1],
    pitchLinear: values[2], pitchQuadratic: values[3], pitchCubic: values[4],
  };
}

function solveCubicSystem(matrix, rhs) {
  for (let pivot = 0; pivot < 5; pivot++) {
    let selected = pivot;
    for (let row = pivot + 1; row < 5; row++)
      if (Math.abs(matrix[row][pivot]) > Math.abs(matrix[selected][pivot])) selected = row;
    if (!Number.isFinite(matrix[selected][pivot]) || Math.abs(matrix[selected][pivot]) < 1e-16) return false;
    for (let column = pivot; column < 5; column++)
      [matrix[pivot][column], matrix[selected][column]] = [matrix[selected][column], matrix[pivot][column]];
    [rhs[pivot], rhs[selected]] = [rhs[selected], rhs[pivot]];
    const diagonal = matrix[pivot][pivot];
    for (let column = pivot; column < 5; column++) matrix[pivot][column] /= diagonal;
    rhs[pivot] /= diagonal;
    for (let row = 0; row < 5; row++) {
      if (row === pivot) continue;
      const factor = matrix[row][pivot];
      for (let column = pivot; column < 5; column++) matrix[row][column] -= factor * matrix[pivot][column];
      rhs[row] -= factor * rhs[pivot];
    }
  }
  return rhs.every(Number.isFinite);
}

function cubicJacobian(current, evaluate) {
  const jacobian = new Array(5);
  const vector = cubicToVector(current.parameters);
  for (let column = 0; column < 5; column++) {
    const step = column < 2 ? 2e-6 : 2e-7;
    const plus = vector.slice(), minus = vector.slice();
    plus[column] += step; minus[column] -= step;
    let forward = evaluate(cubicFromVector(current.parameters, plus));
    let backward = evaluate(cubicFromVector(current.parameters, minus));
    let span = 2 * step;
    if (!Number.isFinite(forward.cost)) { forward = current; span -= step; }
    if (!Number.isFinite(backward.cost)) { backward = current; span -= step; }
    if (span <= 0) return null;
    jacobian[column] = forward.residuals.map((v, i) => (v - backward.residuals[i]) / span);
  }
  return jacobian;
}

function jacobianIndependence(jacobian, data) {
  const basis = [];
  let independence = 1;
  for (const column of jacobian) {
    const values = column.map((value, i) => value * Math.sqrt(data[i].weight));
    let norm = Math.sqrt(values.reduce((s, v) => s + v * v, 0));
    if (!Number.isFinite(norm) || norm <= 0) return 0;
    for (let i = 0; i < values.length; i++) values[i] /= norm;
    for (let pass = 0; pass < 2; pass++) {
      for (const previous of basis) {
        let projection = 0;
        for (let i = 0; i < values.length; i++) projection += values[i] * previous[i];
        for (let i = 0; i < values.length; i++) values[i] -= projection * previous[i];
      }
    }
    const remaining = Math.sqrt(values.reduce((s, v) => s + v * v, 0));
    independence = Math.min(independence, remaining);
    if (remaining < 1e-12) return 0;
    for (let i = 0; i < values.length; i++) values[i] /= remaining;
    basis.push(values);
  }
  return independence;
}

function refineCubic(initial, data, delta, evaluate) {
  let current = initial;
  let damping = 1e-5;
  for (let iteration = 0; iteration < 120 && Number.isFinite(current.cost); iteration++) {
    const jacobian = cubicJacobian(current, evaluate);
    if (jacobian === null) break;
    const matrix = Array.from({ length: 5 }, () => new Array(5).fill(0));
    const rhs = new Array(5).fill(0);
    const scales = new Array(5);
    for (let i = 0; i < data.length; i++) {
      const error = Math.abs(current.residuals[i]);
      const weight = data[i].weight * (error <= delta ? 1 : delta / error);
      for (let a = 0; a < 5; a++) {
        rhs[a] -= weight * jacobian[a][i] * current.residuals[i];
        for (let b = 0; b <= a; b++) matrix[a][b] += weight * jacobian[a][i] * jacobian[b][i];
      }
    }
    for (let a = 0; a < 5; a++) scales[a] = Math.sqrt(Math.max(matrix[a][a], 1e-30));
    for (let a = 0; a < 5; a++) {
      rhs[a] /= scales[a];
      for (let b = 0; b <= a; b++) {
        matrix[a][b] /= scales[a] * scales[b];
        matrix[b][a] = matrix[a][b];
      }
      matrix[a][a] += damping;
    }
    if (!solveCubicSystem(matrix, rhs)) break;
    const vector = cubicToVector(current.parameters);
    let proposed = current;
    for (let backtrack = 0; backtrack < 12; backtrack++) {
      const fraction = Math.pow(0.5, backtrack);
      proposed = evaluate(cubicFromVector(current.parameters,
        vector.map((value, i) => value + fraction * rhs[i] / scales[i])));
      if (proposed.cost < current.cost) break;
    }
    if (proposed.cost < current.cost) {
      const improvement = current.cost - proposed.cost;
      current = proposed;
      damping = Math.max(1e-12, damping / 3);
      if (current.rms < 1e-7 || improvement < 1e-17) break;
    } else {
      damping *= 10;
      if (damping > 1e10) break;
    }
  }
  return current;
}

function fitCubic(target, data, coverage, options, baseline, warnings, baselineResiduals,
    delta, boundaryLimited, center, lowerInner, upperInner, lowerOuter, upperOuter) {
  const pitchCoefficientRange = options.pitchCoefficientRange ?? 0.05;
  const maxCandidates = options.maxCandidates ?? 8;
  const baselineResult = () => ({
    result: (() => {
      const r = new CalibrationFitResult(true, baseline[0].parameters, baseline[0].rotationRadians,
        baseline[0].rmsErrorMm, baseline, warnings);
      r.boundaryLimited = boundaryLimited;
      return r;
    })(),
    residuals: baselineResiduals,
  });
  const radialLevels = new Set(data.map((d) => Math.round(d.radius * 10000) / 10000)).size;
  if (radialLevels < 12 || coverage < 0.6) {
    warnings.push("Cubic pitch fitting needs at least 12 distinct radii and 60% coverage of the drawing band; this run keeps the pitch coefficients and fits the radii only.");
    return baselineResult();
  }
  if (baseline[0].rmsErrorMm < 1e-6) return baselineResult();

  const generated = new CalibrationGeometry(target.geometryParameters);
  const traces = Math.max(...data.map((d) => d.trace)) + 1;

  const evaluate = (parameters) => {
    const invalidCubic = () => ({ parameters, rotation: 0, cost: Number.POSITIVE_INFINITY, rms: Number.POSITIVE_INFINITY, residuals: [] });
    if (Math.abs(parameters.innerRadiusMm - center.innerRadiusMm) > options.radiusRangeMm ||
        Math.abs(parameters.outerRadiusMm - center.outerRadiusMm) > options.radiusRangeMm ||
        Math.abs(parameters.pitchLinear - center.pitchLinear) > pitchCoefficientRange ||
        Math.abs(parameters.pitchQuadratic - center.pitchQuadratic) > pitchCoefficientRange ||
        Math.abs(parameters.pitchCubic - center.pitchCubic) > pitchCoefficientRange)
      return invalidCubic();
    let geometry;
    try {
      validateParameters(parameters);
      geometry = new CalibrationGeometry(parameters);
    } catch { return invalidCubic(); }
    const identical = parametersEqual(target.geometryParameters, parameters);
    const model = new CalibrationCrossModel(generated, geometry, target.parameters.kind, identical, null);
    const angles = new Array(data.length);
    const means = new Array(traces).fill(0);
    const meanWeights = new Array(traces).fill(0);
    for (let i = 0; i < data.length; i++) {
      const point = data[i];
      const predicted = model.tryAtRadius(point.radius);
      if (predicted === null) return invalidCubic();
      angles[i] = point.angle - predicted.phase;
      const weight = point.weight * point.angularScale * point.angularScale;
      means[point.trace] += weight * angles[i];
      meanWeights[point.trace] += weight;
    }
    let cosine = 0, sine = 0;
    for (let trace = 0; trace < traces; trace++) {
      means[trace] /= meanWeights[trace];
      cosine += Math.cos(means[trace]);
      sine += Math.sin(means[trace]);
    }
    let rotation = Math.atan2(sine, cosine);
    for (let i = 0; i < angles.length; i++)
      angles[i] += 2 * Math.PI * Math.round((rotation - means[data[i].trace]) / (2 * Math.PI));
    for (let iteration = 0; iteration < 12; iteration++) {
      let sum = 0, weights = 0;
      for (let i = 0; i < data.length; i++) {
        const point = data[i];
        const error = Math.abs((angles[i] - rotation) * point.angularScale);
        const weight = point.weight * point.angularScale * point.angularScale *
          (error <= delta ? 1 : delta / error);
        sum += weight * angles[i];
        weights += weight;
      }
      const updated = sum / weights;
      if (Math.abs(updated - rotation) < 1e-12) break;
      rotation = updated;
    }
    const residuals = new Array(data.length);
    let cost = 0, squared = 0, weightSum = 0;
    for (let i = 0; i < data.length; i++) {
      const residual = (angles[i] - rotation) * data[i].angularScale;
      residuals[i] = residual;
      const error = Math.abs(residual);
      cost += data[i].weight * (error <= delta ? 0.5 * error * error : delta * (error - delta / 2));
      squared += data[i].weight * residual * residual;
      weightSum += data[i].weight;
    }
    return { parameters, rotation, cost: cost / weightSum, rms: Math.sqrt(squared / weightSum), residuals };
  };

  const starts = [];
  for (const c of baseline.slice(0, 3).map((c) => c.parameters).concat([center])) {
    if (!starts.some((s) => parametersEqual(s, c))) starts.push(c);
  }
  const solutions = [];
  for (const start of starts) {
    const result = refineCubic(evaluate(start), data, delta, evaluate);
    if (!Number.isFinite(result.cost)) continue;
    const duplicate = solutions.findIndex((other) =>
      cubicToVector(other.parameters).every((v, index) =>
        Math.abs(v - cubicToVector(result.parameters)[index]) < (index < 2 ? 1e-6 : 1e-8)));
    if (duplicate < 0) solutions.push(result);
    else if (result.cost < solutions[duplicate].cost) solutions[duplicate] = result;
  }
  solutions.sort((a, b) => a.cost - b.cost);
  if (solutions.length === 0 || solutions[0].cost >= baseline[0].robustCost * 0.995) {
    warnings.push("The cubic pitch brought no stable improvement; keeping the previous pitch coefficients — add inner, middle and outer trace points and retry.");
    return baselineResult();
  }
  const best = solutions[0];
  const jacobian = cubicJacobian(best, evaluate);
  if (jacobian === null || jacobianIndependence(jacobian, data) < 1e-7) {
    warnings.push("The current points cannot stably separate the three pitch coefficients from the two radii; keeping the previous pitch — widen the radial coverage and improve point accuracy.");
    return baselineResult();
  }
  for (const prefix of ["The curve deviation is large", "The best result is near", "The trace constrains the two radii", "Another parameter candidate"]) {
    for (let i = warnings.length - 1; i >= 0; i--) if (warnings[i].startsWith(prefix)) warnings.splice(i, 1);
  }
  if (best.rms > delta * 2)
    warnings.push("The curve deviation is large; check the rectification, the points, and that you followed the same line throughout.");
  if (Math.max(Math.abs(best.parameters.innerRadiusMm - center.innerRadiusMm),
      Math.abs(best.parameters.outerRadiusMm - center.outerRadiusMm)) > options.radiusRangeMm * 0.995)
    warnings.push("The best result is near the search boundary; widen the search range and add trace points to recheck.");
  warnings.push("Cubic pitch fitted; the three shape coefficients and the two radii can compensate each other — a low residual does not prove uniqueness, verify the whole predicted curve before applying.");
  if (Math.max(Math.abs(best.parameters.pitchLinear - center.pitchLinear),
      Math.max(Math.abs(best.parameters.pitchQuadratic - center.pitchQuadratic),
        Math.abs(best.parameters.pitchCubic - center.pitchCubic))) > pitchCoefficientRange * 0.995)
    warnings.push("A pitch coefficient is near the search boundary; check the points and widen the coefficient range if needed.");
  const candidates = solutions.slice(0, maxCandidates).map((solution) => {
    const { maxError, outliers } = residualStats(solution.residuals, delta);
    return new CalibrationFitCandidate(solution.parameters, normalizeAngle(solution.rotation),
      solution.rms, solution.cost, maxError, outliers);
  });
  const cubicBoundary = Math.min(best.parameters.innerRadiusMm - lowerInner, upperInner - best.parameters.innerRadiusMm) < options.radiusRangeMm * 0.005 ||
    Math.min(best.parameters.outerRadiusMm - lowerOuter, upperOuter - best.parameters.outerRadiusMm) < options.radiusRangeMm * 0.005;
  const cubicResult = new CalibrationFitResult(true, best.parameters, normalizeAngle(best.rotation),
    best.rms, candidates, warnings);
  cubicResult.boundaryLimited = cubicBoundary;
  return { result: cubicResult, residuals: best.residuals };
}

function parametersEqual(a, b) {
  return a.kind === b.kind && a.sectors === b.sectors &&
    a.innerRadiusMm === b.innerRadiusMm && a.outerRadiusMm === b.outerRadiusMm &&
    a.linearDensity === b.linearDensity &&
    a.pitchLinear === b.pitchLinear && a.pitchQuadratic === b.pitchQuadratic && a.pitchCubic === b.pitchCubic &&
    (a.usableSectors ?? 0) === (b.usableSectors ?? 0);
}

function validateParameters(p) {
  const trackLength = trackLengthOf(p);
  if (p.sectors <= 0 || !Number.isFinite(p.innerRadiusMm) || !Number.isFinite(p.outerRadiusMm) ||
      p.innerRadiusMm <= 0 || p.outerRadiusMm <= p.innerRadiusMm ||
      !Number.isFinite(p.linearDensity) || p.linearDensity <= 0 ||
      !Number.isFinite(trackLength) || trackLength <= p.outerRadiusMm - p.innerRadiusMm ||
      !Number.isFinite(p.outerRadiusMm * p.outerRadiusMm) ||
      !Number.isFinite(2 * trackLength / (p.outerRadiusMm * p.outerRadiusMm - p.innerRadiusMm * p.innerRadiusMm)))
    throw new Error("invalid calibration parameters");
  // Pitch positivity is enforced by the CalibrationGeometry constructor.
}
