// Port of CalibrationPhotoRectifier.cs / CalibrationPhotoTransform.cs.
// Fits one homography jointly to the physical outer edge and the centre hole.

export class CalibrationPhotoTransform {
  // discToSourceMatrix9: row-major homography mapping disc mm to source pixels.
  constructor(discToSourceMatrix9) {
    if (discToSourceMatrix9.length !== 9 || discToSourceMatrix9.some((v) => !Number.isFinite(v)))
      throw new Error("the photo transform must contain 9 finite values");
    const scale = Math.max(...discToSourceMatrix9.map(Math.abs));
    if (scale <= 0) throw new Error("the photo transform is not invertible");
    this.discToSource = discToSourceMatrix9.map((v) => v / scale);
    this.sourceToDisc = invert(this.discToSource);
  }

  static map(matrix, point) {
    if (!Number.isFinite(point.x) || !Number.isFinite(point.y)) throw new Error("photo coordinates must be finite");
    const divisor = matrix[6] * point.x + matrix[7] * point.y + matrix[8];
    const scale = Math.abs(matrix[6] * point.x) + Math.abs(matrix[7] * point.y) + Math.abs(matrix[8]);
    if (Math.abs(divisor) <= Math.max(1e-15, scale * 1e-12)) throw new Error("this position is close to the projective horizon and cannot be rectified");
    const x = (matrix[0] * point.x + matrix[1] * point.y + matrix[2]) / divisor;
    const y = (matrix[3] * point.x + matrix[4] * point.y + matrix[5]) / divisor;
    if (!Number.isFinite(x) || !Number.isFinite(y)) throw new Error("the photo transform produced an invalid coordinate");
    return { x, y };
  }

  sourceToDiscPoint(p) { return CalibrationPhotoTransform.map(this.sourceToDisc, p); }
  discToSourcePoint(p) { return CalibrationPhotoTransform.map(this.discToSource, p); }

  isValidForDisc(physicalOuterRadiusMm) {
    if (!Number.isFinite(physicalOuterRadiusMm) || physicalOuterRadiusMm <= 0) return false;
    const variation = physicalOuterRadiusMm * Math.sqrt(this.discToSource[6] * this.discToSource[6] + this.discToSource[7] * this.discToSource[7]);
    return Math.abs(this.discToSource[8]) > variation * 1.02 + 1e-12;
  }

  static invert(m) { return invert(m); }
}

function invert(m) {
  const result = [
    m[4] * m[8] - m[5] * m[7], m[2] * m[7] - m[1] * m[8], m[1] * m[5] - m[2] * m[4],
    m[5] * m[6] - m[3] * m[8], m[0] * m[8] - m[2] * m[6], m[2] * m[3] - m[0] * m[5],
    m[3] * m[7] - m[4] * m[6], m[1] * m[6] - m[0] * m[7], m[0] * m[4] - m[1] * m[3],
  ];
  const determinant = m[0] * result[0] + m[1] * result[3] + m[2] * result[6];
  const determinantScale = Math.abs(m[0] * result[0]) + Math.abs(m[1] * result[3]) + Math.abs(m[2] * result[6]);
  if (!Number.isFinite(determinant) || Math.abs(determinant) <= Math.max(1e-20, determinantScale * 1e-12))
    throw new Error("the photo transform is degenerate; disc coordinates cannot be recovered");
  return result.map((v) => v / determinant);
}

function solve(matrix, right) {
  const n = right.length;
  const a = matrix.map((row) => row.slice());
  const b = right.slice();
  for (let column = 0; column < n; column++) {
    let pivot = column;
    for (let row = column + 1; row < n; row++) if (Math.abs(a[row][column]) > Math.abs(a[pivot][column])) pivot = row;
    if (!Number.isFinite(a[pivot][column]) || Math.abs(a[pivot][column]) < 1e-15) return null;
    if (pivot !== column) {
      for (let j = column; j < n; j++) [a[column][j], a[pivot][j]] = [a[pivot][j], a[column][j]];
      [b[column], b[pivot]] = [b[pivot], b[column]];
    }
    for (let row = column + 1; row < n; row++) {
      const multiplier = a[row][column] / a[column][column];
      for (let j = column + 1; j < n; j++) a[row][j] -= multiplier * a[column][j];
      b[row] -= multiplier * b[column];
    }
  }
  const result = new Array(n);
  for (let i = n - 1; i >= 0; i--) {
    let value = b[i];
    for (let j = i + 1; j < n; j++) value -= a[i][j] * result[j];
    result[i] = value / a[i][i];
    if (!Number.isFinite(result[i])) return null;
  }
  return result;
}

function isSpread(points) {
  for (let i = 0; i < points.length; i++)
    for (let j = 0; j < i; j++)
      if ((points[i].x - points[j].x) ** 2 + (points[i].y - points[j].y) ** 2 < 1e-10) return false;
  const mx = points.reduce((s, p) => s + p.x, 0) / points.length;
  const my = points.reduce((s, p) => s + p.y, 0) / points.length;
  let xx = 0, yy = 0, xy = 0;
  for (const p of points) {
    xx += (p.x - mx) * (p.x - mx);
    yy += (p.y - my) * (p.y - my);
    xy += (p.x - mx) * (p.y - my);
  }
  return xx + yy > 1e-6 && xx * yy - xy * xy > (xx + yy) * (xx + yy) * 1e-5;
}

function initialAffineEllipse(points) {
  const normal = Array.from({ length: 5 }, () => new Array(5).fill(0));
  const right = new Array(5).fill(0);
  const meanX = points.reduce((s, p) => s + p.x, 0) / points.length;
  const meanY = points.reduce((s, p) => s + p.y, 0) / points.length;
  for (const p of points) {
    const x = p.x - meanX, y = p.y - meanY;
    const row = [x * x, 2 * x * y, y * y, x, y];
    for (let i = 0; i < 5; i++) {
      right[i] += row[i];
      for (let j = 0; j < 5; j++) normal[i][j] += row[i] * row[j];
    }
  }
  const q = solve(normal, right);
  if (q === null) return null;
  const determinant = q[0] * q[2] - q[1] * q[1];
  if (q[0] <= 0 || q[2] <= 0 || determinant <= 1e-10) return null;
  const cx = -(q[2] * q[3] - q[1] * q[4]) / (2 * determinant);
  const cy = -(q[0] * q[4] - q[1] * q[3]) / (2 * determinant);
  const k = 1 + q[0] * cx * cx + 2 * q[1] * cx * cy + q[2] * cy * cy;
  if (k <= 0) return null;
  const a = Math.sqrt(q[0] / k), b = q[1] / (k * a);
  const dSquared = q[2] / k - b * b;
  if (dSquared <= 1e-10) return null;
  const d = Math.sqrt(dSquared);
  return [Math.log(a), b, -a * (cx + meanX) - b * (cy + meanY), Math.log(d), -d * (cy + meanY), 0, 0];
}

function initialJointProjection(outerAffine, hole, ratio) {
  const a = Math.exp(outerAffine[0]), b = outerAffine[1], c = outerAffine[2];
  const d = Math.exp(outerAffine[3]), e = outerAffine[4];
  const normalizedHole = hole.map((p) => ({ x: a * p.x + b * p.y + c, y: d * p.y + e }));
  const holeEllipse = initialAffineEllipse(normalizedHole);
  if (holeEllipse === null) return null;
  const centerY = -holeEllipse[4] / Math.exp(holeEllipse[3]);
  const centerX = -(holeEllipse[2] + holeEllipse[1] * centerY) / Math.exp(holeEllipse[0]);
  const length = Math.sqrt(centerX * centerX + centerY * centerY);
  if (length < 1e-10) return outerAffine.slice();
  if (length >= 0.95) return null;
  const r2 = ratio * ratio, oneMinusR2 = 1 - r2;
  const v = 2 * length / (oneMinusR2 + Math.sqrt(oneMinusR2 * oneMinusR2 + 4 * length * length * r2));
  if (v >= 0.98) return null;
  const nx = centerX / length, ny = centerY / length;
  const vx = v * nx, vy = v * ny, gamma = 1 / Math.sqrt(1 - v * v);
  const ax = 1 + (gamma - 1) * nx * nx, ay = 1 + (gamma - 1) * ny * ny;
  const cross = (gamma - 1) * nx * ny;
  const matrix = [
    ax * a, ax * b + cross * d, ax * c + cross * e - gamma * vx,
    cross * a, cross * b + ay * d, cross * c + ay * e - gamma * vy,
    -gamma * vx * a, -gamma * (vx * b + vy * d), gamma * (1 - vx * c - vy * e),
  ];
  const firstLength = Math.sqrt(matrix[0] * matrix[0] + matrix[3] * matrix[3]);
  const cosine = matrix[0] / firstLength, sine = matrix[3] / firstLength, scale = matrix[8];
  const newA = firstLength / scale;
  const newB = (cosine * matrix[1] + sine * matrix[4]) / scale;
  const newC = (cosine * matrix[2] + sine * matrix[5]) / scale;
  const newD = (-sine * matrix[1] + cosine * matrix[4]) / scale;
  const newE = (-sine * matrix[2] + cosine * matrix[5]) / scale;
  if (newA <= 0 || newD <= 0) return null;
  return [Math.log(newA), newB, newC, Math.log(newD), newE, matrix[6] / scale, matrix[7] / scale];
}

function evaluate(p, data) {
  if (p.some((v) => !Number.isFinite(v)) || Math.abs(p[0]) > 7 || Math.abs(p[3]) > 7 ||
      Math.abs(p[5]) > 10 || Math.abs(p[6]) > 10) {
    return data.map(() => Number.POSITIVE_INFINITY);
  }
  const a = Math.exp(p[0]), d = Math.exp(p[3]);
  const determinant = a * d - a * p[4] * p[6] + p[1] * p[4] * p[5] - p[2] * d * p[5];
  const horizonX = -d * p[5], horizonY = p[1] * p[5] - a * p[6];
  if (determinant <= a * d * 1e-4 || a * d <= Math.sqrt(horizonX * horizonX + horizonY * horizonY) * 1.02)
    return data.map(() => Number.POSITIVE_INFINITY);
  const result = new Array(data.length);
  for (let i = 0; i < data.length; i++) {
    const point = data[i];
    const u = a * point.x + p[1] * point.y + p[2], v = d * point.y + p[4];
    const w = p[5] * point.x + p[6] * point.y + 1;
    const r2 = point.radius * point.radius;
    const gx = 2 * (a * u - r2 * p[5] * w);
    const gy = 2 * (p[1] * u + d * v - r2 * p[6] * w);
    const gradient = Math.sqrt(gx * gx + gy * gy);
    result[i] = w <= 0.03 || gradient < 1e-12 ? Number.POSITIVE_INFINITY
      : (u * u + v * v - r2 * w * w) / gradient * point.weight;
  }
  return result;
}

function cost(residual) { return residual.reduce((s, v) => s + v * v, 0); }

function optimize(parameters, data) {
  let residual = evaluate(parameters, data);
  let costValue = cost(residual), damping = 1e-3;
  for (let iteration = 0; iteration < 120 && Number.isFinite(costValue); iteration++) {
    const derivatives = [];
    for (let axis = 0; axis < 7; axis++) {
      const step = 1e-5 * Math.max(1, Math.abs(parameters[axis]));
      const shifted = parameters.slice();
      shifted[axis] += step;
      const sample = evaluate(shifted, data);
      derivatives.push(sample.map((value, i) => (value - residual[i]) / step));
    }
    const normal = Array.from({ length: 7 }, () => new Array(7).fill(0));
    const right = new Array(7).fill(0);
    for (let i = 0; i < 7; i++) {
      for (let n = 0; n < data.length; n++) right[i] -= derivatives[i][n] * residual[n];
      for (let j = 0; j < 7; j++)
        for (let n = 0; n < data.length; n++) normal[i][j] += derivatives[i][n] * derivatives[j][n];
      normal[i][i] += damping * Math.max(normal[i][i], 1e-8);
    }
    const delta = solve(normal, right);
    if (delta === null) break;
    const candidate = parameters.map((value, i) => value + delta[i]);
    const candidateResidual = evaluate(candidate, data);
    const candidateCost = cost(candidateResidual);
    if (candidateCost < costValue) {
      const improvement = costValue - candidateCost;
      parameters = candidate;
      residual = candidateResidual;
      costValue = candidateCost;
      damping = Math.max(1e-10, damping / 3);
      if (costValue < 1e-22 || improvement < Math.max(1e-22, costValue * 1e-10)) break;
    } else {
      damping *= 10;
      if (damping > 1e12) break;
    }
  }
  return { parameters, cost: costValue };
}

function coversCircle(points, transform) {
  const angles = points.map((p) => Math.atan2(transform.sourceToDiscPoint(p).y, transform.sourceToDiscPoint(p).x)).sort((a, b) => a - b);
  let maximumGap = angles[0] + 2 * Math.PI - angles[angles.length - 1];
  for (let i = 1; i < angles.length; i++) maximumGap = Math.max(maximumGap, angles[i] - angles[i - 1]);
  return maximumGap < Math.PI * 0.95;
}

function residualStats(points, transform, radius) {
  let squared = 0, maximum = 0;
  for (const source of points) {
    const disc = transform.sourceToDiscPoint(source);
    let angle = Math.atan2(disc.y, disc.x);
    for (let i = 0; i < 15; i++) {
      const step = 1e-5;
      const p = transform.discToSourcePoint({ x: radius * Math.cos(angle), y: radius * Math.sin(angle) });
      const q = transform.discToSourcePoint({ x: radius * Math.cos(angle + step), y: radius * Math.sin(angle + step) });
      const dx = (q.x - p.x) / step, dy = (q.y - p.y) / step;
      const length = dx * dx + dy * dy;
      if (length < 1e-12) break;
      const delta = Math.min(0.3, Math.max(-0.3, ((p.x - source.x) * dx + (p.y - source.y) * dy) / length));
      angle -= delta;
      if (Math.abs(delta) < 1e-10) break;
    }
    const closest = transform.discToSourcePoint({ x: radius * Math.cos(angle), y: radius * Math.sin(angle) });
    const distance = Math.sqrt((closest.x - source.x) ** 2 + (closest.y - source.y) ** 2);
    squared += distance * distance;
    maximum = Math.max(maximum, distance);
  }
  return { rms: Math.sqrt(squared / points.length), maximum };
}

// Fits the homography from outer-edge and hole-edge source pixel points.
// Returns {succeeded, transform, outerRmsPixels, holeRmsPixels, message}.
export function fitPhotoTransform(outerPixels, holePixels, physicalOuterRadiusMm = 60, physicalHoleRadiusMm = 7.5) {
  const failed = (message) => ({ succeeded: false, transform: null, outerRmsPixels: NaN, holeRmsPixels: NaN, message });
  if (!outerPixels || !holePixels) return failed("Please mark both the physical outer edge and the centre-hole edge.");
  if (!Number.isFinite(physicalOuterRadiusMm) || !Number.isFinite(physicalHoleRadiusMm) ||
      physicalOuterRadiusMm < 10 || physicalOuterRadiusMm > 100 || physicalHoleRadiusMm <= 0 ||
      physicalHoleRadiusMm >= physicalOuterRadiusMm * 0.5)
    return failed("The physical disc and hole sizes are invalid. A regular disc is 120 mm across with a 15 mm hole.");
  if (outerPixels.length < 5 || outerPixels.length > 256 || holePixels.length < 5 || holePixels.length > 256)
    return failed("The outer and the hole edge each need at least 5 spread points, at most 256 per outline.");
  if (outerPixels.concat(holePixels).some((p) => !Number.isFinite(p.x) || !Number.isFinite(p.y)))
    return failed("The outlines contain invalid coordinates.");
  const centerX = (Math.max(...outerPixels.map((p) => p.x)) + Math.min(...outerPixels.map((p) => p.x))) / 2;
  const centerY = (Math.max(...outerPixels.map((p) => p.y)) + Math.min(...outerPixels.map((p) => p.y))) / 2;
  const sourceScale = Math.max(
    Math.max(...outerPixels.map((p) => p.x)) - Math.min(...outerPixels.map((p) => p.x)),
    Math.max(...outerPixels.map((p) => p.y)) - Math.min(...outerPixels.map((p) => p.y))) / 2;
  if (!Number.isFinite(sourceScale) || sourceScale < 5) return failed("The disc is too small in the photo for a stable rectification.");
  const outer = outerPixels.map((p) => ({ x: (p.x - centerX) / sourceScale, y: (p.y - centerY) / sourceScale }));
  const hole = holePixels.map((p) => ({ x: (p.x - centerX) / sourceScale, y: (p.y - centerY) / sourceScale }));
  if (!isSpread(outer) || !isSpread(hole))
    return failed("The outline points are repeated or near a straight line; spread them along each edge.");
  const initial = initialAffineEllipse(outer);
  if (initial === null) return failed("The outer edge cannot form a stable ellipse; check the points on the physical outer edge.");
  const holeExtent = hole.reduce((s, p) => s + Math.sqrt(
    (Math.exp(initial[0]) * p.x + initial[1] * p.y + initial[2]) ** 2 +
    (Math.exp(initial[3]) * p.y + initial[4]) ** 2), 0) / hole.length;
  if (holeExtent < 0.015 || holeExtent > 0.9)
    return failed("The hole edge does not match the outer edge's position; make sure you marked the actual centre hole, not a printed ring.");

  const ratio = physicalHoleRadiusMm / physicalOuterRadiusMm;
  const data = outer.map((p) => ({ x: p.x, y: p.y, radius: 1, weight: 1 / Math.sqrt(outer.length) }))
    .concat(hole.map((p) => ({ x: p.x, y: p.y, radius: ratio, weight: 1 / Math.sqrt(hole.length) })));
  let best = null, bestCost = Number.POSITIVE_INFINITY;
  const seeds = [];
  const jointInitial = initialJointProjection(initial, hole, ratio);
  if (jointInitial !== null) seeds.push(jointInitial);
  for (const [gx, gy] of [[0, 0], [0.25, 0], [-0.25, 0], [0, 0.25], [0, -0.25]]) {
    const seed = initial.slice();
    seed[5] = gx; seed[6] = gy;
    seeds.push(seed);
  }
  for (const seed of seeds) {
    const { parameters: solution, cost: c } = optimize(seed, data);
    if (c < bestCost) { best = solution; bestCost = c; }
    if (bestCost < 1e-20) break;
  }
  if (best === null || !Number.isFinite(bestCost)) return failed("The two outlines cannot produce a valid joint projection; check the points.");
  try {
    const a = Math.exp(best[0]), b = best[1], c = best[2], d = Math.exp(best[3]), e = best[4];
    const g = best[5], h = best[6], radius = physicalOuterRadiusMm;
    const sourceToDisc = [
      radius * a / sourceScale, radius * b / sourceScale, radius * (c - (a * centerX + b * centerY) / sourceScale),
      0, radius * d / sourceScale, radius * (e - d * centerY / sourceScale),
      g / sourceScale, h / sourceScale, 1 - (g * centerX + h * centerY) / sourceScale,
    ];
    const transform = new CalibrationPhotoTransform(invert(sourceToDisc));
    if (!transform.isValidForDisc(radius)) return failed("The camera angle or outline correspondence degenerates the projection; retake the photo or adjust the points.");
    if (!coversCircle(outerPixels, transform) || !coversCircle(holePixels, transform))
      return failed("The outline points are concentrated in one area; add points in other directions along both circles.");
    const outerR = residualStats(outerPixels, transform, radius);
    const holeR = residualStats(holePixels, transform, physicalHoleRadiusMm);
    const tolerance = Math.max(2, sourceScale * 0.006);
    if (outerR.rms > tolerance || holeR.rms > tolerance || Math.max(outerR.maximum, holeR.maximum) > tolerance * 3)
      return { succeeded: false, transform: null, outerRmsPixels: outerR.rms, holeRmsPixels: holeR.rms,
        message: `The two physical outlines cannot both fit: outer edge error ${outerR.rms.toFixed(2)} px, hole edge error ${holeR.rms.toFixed(2)} px. Check the edges and the disc size.` };
    return { succeeded: true, transform, outerRmsPixels: outerR.rms, holeRmsPixels: holeR.rms,
      message: `Both edges rectified jointly. Outline error: outer ${outerR.rms.toFixed(2)} px, hole ${holeR.rms.toFixed(2)} px. Please verify both outlines visually.` };
  } catch (error) {
    return failed(error.message);
  }
}
