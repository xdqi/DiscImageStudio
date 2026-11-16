// Port of DiscImageStudio.Core/DvdTrackGeometry.cs (scalar paths) to JS.
// The spiral's local radial pitch is a cubic in normalized radius:
//   dr/dθ = b (1 + c1 x + c2 x² + c3 x³), x = (r - ri) / (ro - ri).

const NODES = [0.1834346424956498, 0.5255324099163290, 0.7966664774136267, 0.9602898564975363];
const WEIGHTS = [0.3626837833783620, 0.3137066458778873, 0.2223810344533745, 0.1012285362903763];

class PitchResolutionError extends Error {}

function hypot2(a, b) {
  a = Math.abs(a); b = Math.abs(b);
  const m = Math.max(a, b);
  if (m === 0) return 0;
  const r = Math.min(a, b) / m;
  return m * Math.sqrt(1 + r * r);
}

function asinh(x) { return Math.asinh(x); }

function clampf(v, lo, hi) { return v < lo ? lo : v > hi ? hi : v; }

export class TrackGeometry {
  constructor(innerRadiusMm, outerRadiusMm, trackLengthMm, linear, quadratic, cubic) {
    if (!Number.isFinite(innerRadiusMm) || innerRadiusMm <= 0) throw new Error("inner radius must be positive");
    if (!Number.isFinite(outerRadiusMm) || outerRadiusMm <= innerRadiusMm) throw new Error("outer radius must exceed inner radius");
    if (!Number.isFinite(trackLengthMm) || trackLengthMm <= outerRadiusMm - innerRadiusMm)
      throw new Error("track length must exceed the radial distance between the two radii");
    TrackGeometry.validatePitchCoefficients(linear, quadratic, cubic);
    this.innerRadiusMm = innerRadiusMm;
    this.outerRadiusMm = outerRadiusMm;
    this.trackLengthMm = trackLengthMm;
    this.range = outerRadiusMm - innerRadiusMm;
    this.linear = linear;
    this.quadratic = quadratic;
    this.cubic = cubic;
    this.isConstantPitch = linear === 0 && quadratic === 0 && cubic === 0;
    this.segments = [];
    this.lenScale = 1;

    if (this.isConstantPitch) {
      let low = 1e-15;
      let high = Math.max(outerRadiusMm, 1.0);
      while (this.constantArc(innerRadiusMm, outerRadiusMm, high) > trackLengthMm) high *= 2;
      for (let i = 0; i < 120; i++) {
        const middle = (low + high) / 2;
        if (this.constantArc(innerRadiusMm, outerRadiusMm, middle) > trackLengthMm) low = middle; else high = middle;
      }
      this.radialGrowthPerRadianMm = (low + high) / 2;
      this.constantInnerRadiusSquared = innerRadiusMm * innerRadiusMm;
      this.constantGrowthSquared = this.radialGrowthPerRadianMm * this.radialGrowthPerRadianMm;
      this.constantTwiceGrowth = 2 * this.radialGrowthPerRadianMm;
      this.constantInnerPrimitive = TrackGeometry.primitive(innerRadiusMm, this.radialGrowthPerRadianMm);
      this.totalAngleRadians = this.range / this.radialGrowthPerRadianMm;
      return;
    }

    const thinLength = this.integrate(innerRadiusMm, outerRadiusMm, 1, true).length;
    let lower = 0;
    let upper = trackLengthMm / thinLength;
    let inverseGrowth = upper;
    for (let iteration = 0; iteration < 80; iteration++) {
      const value = this.integrate(innerRadiusMm, outerRadiusMm, inverseGrowth, false);
      const error = value.length - trackLengthMm;
      if (Math.abs(error) <= trackLengthMm * 2e-15) break;
      if (error > 0) upper = inverseGrowth; else lower = inverseGrowth;
      let next = inverseGrowth - error / value.derivative;
      if (!Number.isFinite(next) || next <= lower || next >= upper) next = lower + (upper - lower) / 2;
      if (next === inverseGrowth) break;
      inverseGrowth = next;
    }
    this.radialGrowthPerRadianMm = 1 / inverseGrowth;
    if (!Number.isFinite(this.radialGrowthPerRadianMm) || this.radialGrowthPerRadianMm <= 0)
      throw new PitchResolutionError("the pitch coefficients cannot be resolved at this track length");

    this.buildSegments(innerRadiusMm, outerRadiusMm, inverseGrowth, this.segments, 0);
    let arc = 0, angle = 0, arcError = 0, angleError = 0;
    for (let i = 0; i < this.segments.length; i++) {
      const seg = this.segments[i];
      this.segments[i].arcStart = arc;
      this.segments[i].angleStart = angle;
      [arc, arcError] = addCompensated(arc, arcError, seg.arcDelta);
      [angle, angleError] = addCompensated(angle, angleError, seg.angleDelta);
    }
    this.lenScale = trackLengthMm / arc;
    this.totalAngleRadians = angle;
  }

  static validatePitchCoefficients(linear, quadratic, cubic) {
    if (!Number.isFinite(linear) || !Number.isFinite(quadratic) || !Number.isFinite(cubic) ||
        !positiveOnInterval(linear, quadratic, cubic, 0, 1))
      throw new Error("pitch coefficients must be finite and 1 + c1*x + c2*x² + c3*x³ must stay positive for 0 <= x <= 1");
  }

  constantArc(first, second, b) {
    return (TrackGeometry.primitive(second, b) - TrackGeometry.primitive(first, b)) / b;
  }

  static primitive(radius, b) {
    return 0.5 * (radius * Math.sqrt(radius * radius + b * b) + b * b * asinh(radius / b));
  }

  radiusAtArcLength(arcLengthMm) {
    if (!Number.isFinite(arcLengthMm) || arcLengthMm < 0 || arcLengthMm > this.trackLengthMm)
      throw new Error("arc length out of range");
    if (!this.isConstantPitch) return this.radiusAtArcLengthFast(arcLengthMm);
    const b = this.radialGrowthPerRadianMm;
    let radius = clampf(Math.sqrt(this.innerRadiusMm * this.innerRadiusMm + 2 * b * arcLengthMm), this.innerRadiusMm, this.outerRadiusMm);
    for (let i = 0; i < 5; i++) {
      const error = this.constantArc(this.innerRadiusMm, radius, b) - arcLengthMm;
      const derivative = Math.sqrt(radius * radius + b * b) / b;
      radius = clampf(radius - error / derivative, this.innerRadiusMm, this.outerRadiusMm);
    }
    return radius;
  }

  radiusAtArcLengthFast(arcLengthMm) {
    if (this.isConstantPitch) return this.radiusAtConstantArcLengthFast(arcLengthMm);
    if (arcLengthMm <= 0) return this.innerRadiusMm;
    if (arcLengthMm >= this.trackLengthMm) return this.outerRadiusMm;
    if (!Number.isFinite(arcLengthMm)) throw new Error("arc length must be finite");
    const unscaled = arcLengthMm / this.lenScale;
    const seg = this.segments[this.findSegment(unscaled, false)];
    const t = invertHermite(unscaled - seg.arcStart, seg.arcDelta, seg.width * seg.arcDerStart, seg.width * seg.arcDerEnd);
    return seg.radiusStart + seg.width * t;
  }

  radiusAtConstantArcLengthFast(arcLengthMm) {
    const b = this.radialGrowthPerRadianMm;
    let radius = clampf(Math.sqrt(this.constantInnerRadiusSquared + this.constantTwiceGrowth * arcLengthMm), this.innerRadiusMm, this.outerRadiusMm);
    const root = Math.sqrt(radius * radius + this.constantGrowthSquared);
    const primitive = 0.5 * (radius * root + this.constantGrowthSquared * asinh(radius / b));
    const error = (primitive - this.constantInnerPrimitive) / b - arcLengthMm;
    const derivative = root / b;
    return clampf(radius - error / derivative, this.innerRadiusMm, this.outerRadiusMm);
  }

  atArcLengthFast(arcLengthMm) {
    if (this.isConstantPitch) {
      const r = this.radiusAtConstantArcLengthFast(arcLengthMm);
      return { radius: r, angle: (r - this.innerRadiusMm) / this.radialGrowthPerRadianMm };
    }
    if (arcLengthMm <= 0) return { radius: this.innerRadiusMm, angle: 0 };
    if (arcLengthMm >= this.trackLengthMm) return { radius: this.outerRadiusMm, angle: this.totalAngleRadians };
    if (!Number.isFinite(arcLengthMm)) throw new Error("arc length must be finite");
    const unscaled = arcLengthMm / this.lenScale;
    const seg = this.segments[this.findSegment(unscaled, false)];
    const t = invertHermite(unscaled - seg.arcStart, seg.arcDelta, seg.width * seg.arcDerStart, seg.width * seg.arcDerEnd);
    return {
      radius: seg.radiusStart + seg.width * t,
      angle: seg.angleStart + hermite(t, seg.angleDelta, seg.width * seg.angDerStart, seg.width * seg.angDerEnd),
    };
  }

  angleAtRadius(radius) {
    if (this.isConstantPitch) return (radius - this.innerRadiusMm) / this.radialGrowthPerRadianMm;
    if (radius < this.innerRadiusMm || radius > this.outerRadiusMm) {
      const value = this.tryExtendedIntegral(radius);
      if (value === null) throw new Error("radius out of range");
      return value.angle;
    }
    const seg = this.segments[this.findRadiusSegment(radius)];
    const t = (radius - seg.radiusStart) / seg.width;
    return seg.angleStart + hermite(t, seg.angleDelta, seg.width * seg.angDerStart, seg.width * seg.angDerEnd);
  }

  arcLengthAtRadius(radius) {
    if (this.isConstantPitch) return this.constantArc(this.innerRadiusMm, radius, this.radialGrowthPerRadianMm);
    if (radius < this.innerRadiusMm || radius > this.outerRadiusMm) {
      const value = this.tryExtendedIntegral(radius);
      if (value === null) throw new Error("radius out of range");
      return value.length;
    }
    if (radius === this.outerRadiusMm) return this.trackLengthMm;
    const seg = this.segments[this.findRadiusSegment(radius)];
    const t = (radius - seg.radiusStart) / seg.width;
    return this.lenScale * (seg.arcStart + hermite(t, seg.arcDelta, seg.width * seg.arcDerStart, seg.width * seg.arcDerEnd));
  }

  arcLengthAtAngle(angleRadians) {
    const angle = clampf(angleRadians, 0, this.totalAngleRadians);
    if (this.isConstantPitch)
      return this.constantArc(this.innerRadiusMm, this.innerRadiusMm + this.radialGrowthPerRadianMm * angle, this.radialGrowthPerRadianMm);
    if (angle === 0) return 0;
    if (angle === this.totalAngleRadians) return this.trackLengthMm;
    const seg = this.segments[this.findSegment(angle, true)];
    const t = invertHermite(angle - seg.angleStart, seg.angleDelta, seg.width * seg.angDerStart, seg.width * seg.angDerEnd);
    return this.lenScale * (seg.arcStart + hermite(t, seg.arcDelta, seg.width * seg.arcDerStart, seg.width * seg.arcDerEnd));
  }

  radialGrowthAtRadius(radius) { return this.radialGrowthPerRadianMm * this.shape(radius); }

  shape(radius) {
    return polynomial(this.linear, this.quadratic, this.cubic, (radius - this.innerRadiusMm) / this.range);
  }

  tryProgressAtRadiusExtended(radius) {
    if (!Number.isFinite(radius) || radius <= 0 || !Number.isFinite(radius * radius)) return null;
    let progress;
    if (radius >= this.innerRadiusMm && radius <= this.outerRadiusMm) {
      progress = this.arcLengthAtRadius(radius) / this.trackLengthMm;
    } else if (this.isConstantPitch) {
      progress = this.constantArc(this.innerRadiusMm, radius, this.radialGrowthPerRadianMm) / this.trackLengthMm;
    } else {
      const value = this.tryExtendedIntegral(radius);
      if (value === null) return null;
      progress = value.length / this.trackLengthMm;
    }
    return Number.isFinite(progress) ? progress : null;
  }

  tryAtProgressExtended(progress) {
    if (!Number.isFinite(progress) || !Number.isFinite(progress * this.trackLengthMm)) return null;
    if (progress >= 0 && progress <= 1) {
      const r = this.radiusAtArcLength(progress * this.trackLengthMm);
      return { radius: r, angle: this.angleAtRadius(r) };
    }
    if (this.isConstantPitch) return this.tryConstantAtProgressExtended(progress);

    const outward = progress > 1;
    const target = progress * this.trackLengthMm;
    const anchor = outward ? this.outerRadiusMm : this.innerRadiusMm;
    let validRadius = anchor;
    let boundary = outward ? anchor + this.range : 0;
    let low = 0, high = 0, bracketed = false;
    for (let iteration = 0; iteration < 100; iteration++) {
      const candidate = boundary;
      const integral = candidate > 0 ? this.tryExtendedIntegral(candidate) : null;
      if (candidate <= 0 || integral === null) {
        boundary = validRadius + (boundary - validRadius) / 2;
        if (boundary === validRadius) return null;
        continue;
      }
      const reached = outward ? integral.length >= target : integral.length <= target;
      if (reached) {
        low = Math.min(validRadius, candidate);
        high = Math.max(validRadius, candidate);
        bracketed = true;
        break;
      }
      validRadius = candidate;
      boundary = outward ? anchor + 2 * (candidate - anchor) : candidate / 2;
      if (!Number.isFinite(boundary)) return null;
    }
    if (!bracketed) return null;
    let radius = low + (high - low) / 2;
    for (let iteration = 0; iteration < 64; iteration++) {
      const integral = this.tryExtendedIntegral(radius);
      if (integral === null) return null;
      const error = integral.length - target;
      if (Math.abs(error) <= Math.max(1, Math.abs(target)) * 4e-15) return { radius, angle: integral.angle };
      if (error > 0) high = radius; else low = radius;
      const growth = this.radialGrowthAtRadius(radius);
      const derivative = hypot2(radius, growth) / growth;
      let next = radius - error / derivative;
      if (!Number.isFinite(next) || next <= low || next >= high) next = low + (high - low) / 2;
      if (next === radius) return { radius, angle: integral.angle };
      radius = next;
    }
    return null;
  }

  tryConstantAtProgressExtended(progress) {
    const b = this.radialGrowthPerRadianMm;
    const prim = TrackGeometry.primitive(this.innerRadiusMm, b) + progress * (b * this.trackLengthMm);
    if (!Number.isFinite(prim) || prim <= 0) return null;
    let low = 0;
    let high = Math.min(Math.sqrt(prim) * Math.sqrt(2), prim / b);
    let radius = high;
    for (let iteration = 0; iteration < 64; iteration++) {
      const error = TrackGeometry.primitive(radius, b) - prim;
      if (Math.abs(error) <= prim * 4e-15) {
        const angle = this.angleAtRadius(radius);
        return Number.isFinite(angle) ? { radius, angle } : null;
      }
      if (error > 0) high = radius; else low = radius;
      let next = radius - error / hypot2(radius, b);
      if (!Number.isFinite(next) || next <= low || next >= high) next = low + (high - low) / 2;
      if (next === radius) return null;
      radius = next;
    }
    return null;
  }

  tryExtendedIntegral(radius) {
    if (!Number.isFinite(radius) || radius <= 0 || !Number.isFinite(radius * radius)) return null;
    const x = (radius - this.innerRadiusMm) / this.range;
    if (!positiveOnInterval(this.linear, this.quadratic, this.cubic, Math.min(0, x), Math.max(1, x))) return null;
    const edge = radius < this.innerRadiusMm ? this.innerRadiusMm : this.outerRadiusMm;
    let extra;
    try {
      extra = this.integrate(Math.min(edge, radius), Math.max(edge, radius), 1 / this.radialGrowthPerRadianMm, false);
    } catch (e) {
      if (e instanceof PitchResolutionError) return null;
      throw e;
    }
    const value = radius < this.innerRadiusMm
      ? { length: -extra.length, angle: -extra.angle, derivative: 0 }
      : { length: this.trackLengthMm + extra.length, angle: this.totalAngleRadians + extra.angle, derivative: 0 };
    return Number.isFinite(value.length) && Number.isFinite(value.angle) ? value : null;
  }

  integrate(first, last, inverseGrowth, thin) {
    const state = { evaluations: 0 };
    return this.integrateAdaptive(first, last, inverseGrowth, thin, 0, state);
  }

  integrateAdaptive(first, last, inverseGrowth, thin, depth, state) {
    state.evaluations += 3;
    if (state.evaluations > 32768) throw new PitchResolutionError("the pitch integral exceeded its numerical resolution budget");
    const whole = this.quadrature(first, last, inverseGrowth, thin);
    if (!Number.isFinite(whole.length) || !Number.isFinite(whole.angle)) throw new PitchResolutionError("the pitch integral cannot be resolved numerically");
    const middle = first + (last - first) / 2;
    const left = this.quadrature(first, middle, inverseGrowth, thin);
    const right = this.quadrature(middle, last, inverseGrowth, thin);
    const split = addIntegral(left, right);
    if (Math.abs(split.length - whole.length) <= Math.abs(split.length) * 8e-15 &&
        Math.abs(split.angle - whole.angle) <= Math.abs(split.angle) * 8e-15) return split;
    if (depth >= 24 || middle === first || middle === last) throw new PitchResolutionError("the pitch integral did not converge");
    return addIntegral(
      this.integrateAdaptive(first, middle, inverseGrowth, thin, depth + 1, state),
      this.integrateAdaptive(middle, last, inverseGrowth, thin, depth + 1, state));
  }

  quadrature(first, last, inverseGrowth, thin) {
    const middle = first + (last - first) / 2;
    const half = (last - first) / 2;
    let length = 0, angle = 0, derivative = 0;
    for (let i = 0; i < NODES.length; i++) {
      for (let sign = -1; sign <= 1; sign += 2) {
        const radius = middle + sign * half * NODES[i];
        const inverseShape = 1 / this.shape(radius);
        const radial = radius * inverseShape;
        const z = inverseGrowth * radial;
        const ds = thin ? radial : hypot2(1, z);
        length += WEIGHTS[i] * ds;
        angle += WEIGHTS[i] * inverseShape * inverseGrowth;
        if (!thin) derivative += WEIGHTS[i] * radial * (z / ds);
      }
    }
    return { length: half * length, angle: half * angle, derivative: half * derivative };
  }

  buildSegments(first, last, inverseGrowth, segments, depth) {
    if (segments.length >= 32768) throw new PitchResolutionError("the pitch curve is too close to zero to resolve numerically");
    const width = last - first;
    const middle = first + width / 2;
    const whole = this.quadrature(first, last, inverseGrowth, false);
    const left = this.quadrature(first, middle, inverseGrowth, false);
    const right = this.quadrature(middle, last, inverseGrowth, false);
    const split = addIntegral(left, right);
    const a0 = inverseGrowth / this.shape(first), a1 = inverseGrowth / this.shape(last);
    const s0 = hypot2(1, first * a0), s1 = hypot2(1, last * a1);
    const angleTolerance = 5e-10;
    const lengthTolerance = angleTolerance * this.innerRadiusMm;
    let accepted = Math.abs(split.angle - whole.angle) < angleTolerance / 16 &&
      Math.abs(split.length - whole.length) < lengthTolerance / 16;
    for (let i = 1; accepted && i <= 3; i++) {
      const t = i / 4.0;
      const actual = i === 2 ? left : this.quadrature(first, first + width * t, inverseGrowth, false);
      accepted = Math.abs(hermite(t, split.angle, width * a0, width * a1) - actual.angle) < angleTolerance &&
        Math.abs(hermite(t, split.length, width * s0, width * s1) - actual.length) < lengthTolerance;
    }
    if (!accepted && depth < 24 && middle !== first && middle !== last) {
      this.buildSegments(first, middle, inverseGrowth, segments, depth + 1);
      this.buildSegments(middle, last, inverseGrowth, segments, depth + 1);
      return;
    }
    if (!accepted || !Number.isFinite(split.length) || !Number.isFinite(split.angle) || split.length <= 0)
      throw new PitchResolutionError("the pitch curve cannot be resolved numerically");
    segments.push({
      radiusStart: first, width, arcStart: 0, angleStart: 0,
      arcDelta: split.length, angleDelta: split.angle,
      arcDerStart: s0, arcDerEnd: s1, angDerStart: a0, angDerEnd: a1,
    });
  }

  findRadiusSegment(radius) {
    let low = 0, high = this.segments.length - 1;
    while (low < high) {
      const middle = (low + high + 1) >> 1;
      if (this.segments[middle].radiusStart <= radius) low = middle; else high = middle - 1;
    }
    return low;
  }

  findSegment(value, byAngle) {
    let low = 0, high = this.segments.length - 1;
    while (low < high) {
      const middle = (low + high + 1) >> 1;
      const start = byAngle ? this.segments[middle].angleStart : this.segments[middle].arcStart;
      if (start <= value) low = middle; else high = middle - 1;
    }
    return low;
  }
}

function hermite(t, delta, derivativeStart, derivativeEnd) {
  return t * (derivativeStart + t * (3 * delta - 2 * derivativeStart - derivativeEnd +
    t * (derivativeStart + derivativeEnd - 2 * delta)));
}

function invertHermite(value, delta, derivativeStart, derivativeEnd) {
  let t = clampf(value / delta, 0, 1);
  const quadratic = 3 * delta - 2 * derivativeStart - derivativeEnd;
  const cubic = derivativeStart + derivativeEnd - 2 * delta;
  for (let i = 0; i < 4; i++) {
    const error = t * (derivativeStart + t * (quadratic + t * cubic)) - value;
    const derivative = derivativeStart + t * (2 * quadratic + 3 * t * cubic);
    t = clampf(t - error / derivative, 0, 1);
  }
  return t;
}

function polynomial(linear, quadratic, cubic, x) {
  return ((cubic * x + quadratic) * x + linear) * x + 1;
}

function positiveOnInterval(linear, quadratic, cubic, first, last) {
  const positive = (x) => {
    const value = polynomial(linear, quadratic, cubic, x);
    return Number.isFinite(value) && value > 0;
  };
  if (!positive(first) || !positive(last)) return false;
  const scale = Math.max(Math.abs(linear), Math.max(Math.abs(quadratic), Math.abs(cubic)));
  if (scale === 0) return true;
  const a = 3 * (cubic / scale), b = 2 * (quadratic / scale), c = linear / scale;
  const check = (x) => !(x > first && x < last) || positive(x);
  if (a === 0) return b === 0 || check(-c / b);
  const discriminant = b * b - 4 * a * c;
  if (discriminant < 0) return true;
  const q = -0.5 * (b + copySign(Math.sqrt(discriminant), b));
  if (q === 0) return check(-b / (2 * a));
  return check(q / a) && check(c / q);
}

function copySign(magnitude, signSource) {
  const s = Math.sign(signSource);
  if (s === 0) return Object.is(signSource, -0) ? -magnitude : magnitude;
  return s > 0 ? magnitude : -magnitude;
}

function addIntegral(a, b) {
  return { length: a.length + b.length, angle: a.angle + b.angle, derivative: a.derivative + b.derivative };
}

function addCompensated(sum, errorTerm, value) {
  const adjusted = value - errorTerm;
  const next = sum + adjusted;
  return [next, (next - sum) - adjusted];
}
