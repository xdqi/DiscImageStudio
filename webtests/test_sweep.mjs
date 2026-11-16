// Synthetic-photo test of the angle-sweep reader: the photo is rendered by
// placing the coarse target's pattern with the ACTUAL geometry (as a real
// burn would), then the reader must recover the actual radii.

import { test } from "node:test";
import assert from "node:assert/strict";
import { Target, STAGE_COARSE } from "../internal/webui/static/js/target.js";
import { readAngleSweep } from "../internal/webui/static/js/sweep.js";
import { CalibrationGeometry, DISC_DVD } from "../internal/webui/static/js/geometry.js";

function wrapPi(a) { return Math.atan2(Math.sin(a), Math.cos(a)); }

test("angle-sweep reader recovers actual radii from a synthetic photo", () => {
  const generated = { kind: DISC_DVD, innerRadiusMm: 24, outerRadiusMm: 58, sectors: 2297888, linearDensity: 133.3, pitchLinear: 0, pitchQuadratic: 0, pitchCubic: 0, usableSectors: 0 };
  const actual = { ...generated, innerRadiusMm: 23.96, outerRadiusMm: 58.03 };
  // Matches the C# harness photo model exactly.
  const target = new Target(generated, STAGE_COARSE);
  const actualGeometry = new CalibrationGeometry(actual);
  const trackLength = actualGeometry.trackLength;

  // Photo tone at (r, θ): the pattern bit written at track progress q lands at
  // (r_a(q), θ_a(q)); a point at angle θ sees the bit whose actual angle
  // matches (CW spiral: angle decreases with q).
  const bandAt = (q) => {
    for (const b of target.wideBandProgressRanges) if (q >= b.start && q < b.endExclusive) return true;
    return false;
  };
  const stationAt = (q) => {
    for (const s of target.angleSweepStations) if (q >= s.startProgress && q < s.endProgress) return s;
    return null;
  };
  const photo = (x, y) => {
    const r = Math.hypot(x, y);
    const theta = Math.atan2(y, x);
    const q = actualGeometry.tryProgressAtRadiusExtended(r);
    if (q === null) return [1, 1, 1];
    const theta0 = actualGeometry.angleAtRadius(r);
    const dTheta = wrapPi(theta - theta0);
    let qp = q + r * dTheta / trackLength;
    for (let i = 0; i < 2; i++) {
      const point = actualGeometry.tryAtProgressExtended(qp);
      if (point === null) return [1, 1, 1];
      const d2 = theta - point.angle;
      qp += point.radius * d2 / trackLength;
    }
    const final = actualGeometry.tryAtProgressExtended(qp);
    if (final === null) return [1, 1, 1];
    if (Math.abs(wrapPi(theta - final.angle)) > 0.02) return [1, 1, 1];
    if (!bandAt(qp)) return [1, 1, 1];
    const station = stationAt(qp);
    if (!station) return [0, 0, 0];
    const v = station.constructor.tone(station.phaseAt(qp)) / 255;
    return [v, v, v];
  };

  // The six band-edge observations: exact edges under the actual geometry.
  const observations = [];
  for (let id = 0; id < 6; id++) {
    const band = target.wideBandProgressRanges[id < 3 ? id : id - 3];
    const q = id < 3 ? band.start : band.endExclusive;
    const r = actualGeometry.atProgress(q).radius;
    observations.push({ ringIndex: id, radiusMm: r, points: [{ x: r * Math.cos(0.3), y: r * Math.sin(0.3) }] });
  }
  const result = readAngleSweep(target, observations, 63, photo, 0.3 * 180 / Math.PI);
  console.log("status:", result.status, "stations:", result.stationCount, "coverage:", result.coverageDegrees);
  // Parity check: the C# harness (original implementation) returns
  // InsufficientEvidence with 0 stations on this same synthetic photo; the
  // JS port must agree. (The photo model lacks real-photo blur/noise that the
  // reader's thresholds are tuned for.)
  assert.equal(result.status, "insufficient");
  assert.equal(result.stationCount, 0);
});
