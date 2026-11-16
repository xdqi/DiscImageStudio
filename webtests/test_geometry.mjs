// Node-based unit tests for the browser calibration math.
// Run: node --test /tmp/opencode/webtests/

import { test } from "node:test";
import assert from "node:assert/strict";
import { TrackGeometry } from "../internal/webui/static/js/trackgeom.js";
import { CalibrationGeometry, DISC_CD, DISC_DVD, DISC_BD } from "../internal/webui/static/js/geometry.js";

const TOL = 1e-9;

test("constant-pitch track geometry: radius/angle roundtrip", () => {
  // DVD-like: 2297888 sectors × 38688 bits × 133.3 nm.
  const trackLength = 2297888 * 38688 * 133.3e-6;
  const g = new TrackGeometry(24, 58, trackLength, 0, 0, 0);
  // Archimedean: b from exact arc solve; radius at half length ≈ sqrt mid area.
  const mid = g.radiusAtArcLength(trackLength / 2);
  assert.ok(Math.abs(mid - Math.sqrt((24 * 24 + 58 * 58) / 2)) < 1e-3, `mid radius ${mid}`);
  // Endpoints.
  assert.equal(g.radiusAtArcLength(0), 24);
  assert.ok(Math.abs(g.radiusAtArcLength(trackLength) - 58) < 1e-9);
  // Angle integrates to range/b.
  assert.ok(Math.abs(g.totalAngleRadians - 34 / g.radialGrowthPerRadianMm) < 1e-12);
});

test("cubic-pitch track geometry inverts arc length", () => {
  const trackLength = 2297888 * 38688 * 133.3e-6;
  const g = new TrackGeometry(24, 58, trackLength, 0.1, -0.05, 0.02);
  for (const q of [0.01, 0.1, 0.37, 0.5, 0.83, 0.999]) {
    const arc = q * trackLength;
    const r = g.radiusAtArcLength(arc);
    const back = g.arcLengthAtRadius(r);
    assert.ok(Math.abs(back - arc) < 1e-6 * trackLength, `progress ${q}: ${r} -> ${back} vs ${arc}`);
  }
});

test("CD calibration geometry uses the area law without pitch", () => {
  const p = { kind: DISC_CD, innerRadiusMm: 24.5, outerRadiusMm: 56.8, sectors: 359849, linearDensity: 1200, pitchLinear: 0, pitchQuadratic: 0, pitchCubic: 0 };
  const g = new CalibrationGeometry(p);
  assert.ok(g.usesAreaApproximation);
  const r = g.atProgress(0.5).radius;
  assert.ok(Math.abs(r - Math.sqrt((24.5 * 24.5 + 56.8 * 56.8) / 2)) < 1e-12);
});

test("BD calibration geometry uses the exact constant-pitch primitive", () => {
  const p = { kind: DISC_BD, innerRadiusMm: 24, outerRadiusMm: 58, sectors: 12219392, linearDensity: 74.5, pitchLinear: 0, pitchQuadratic: 0, pitchCubic: 0 };
  const g = new CalibrationGeometry(p);
  assert.ok(!g.usesAreaApproximation);
  // Progress at the outer edge must be 1; at inner 0.
  assert.ok(Math.abs(g.progressAtRadius(24)) < 1e-12);
  assert.ok(Math.abs(g.progressAtRadius(58) - 1) < 1e-12);
  // Roundtrip.
  for (const q of [0.1, 0.5, 0.9]) {
    const r = g.atProgress(q).radius;
    assert.ok(Math.abs(g.progressAtRadius(r) - q) < 1e-9, `progress ${q}`);
  }
});

test("DVD calibration geometry with pitch coefficients roundtrips", () => {
  const p = { kind: DISC_DVD, innerRadiusMm: 24, outerRadiusMm: 58, sectors: 2297888, linearDensity: 133.3, pitchLinear: 0.08, pitchQuadratic: -0.04, pitchCubic: 0.02 };
  const g = new CalibrationGeometry(p);
  for (const q of [0.05, 0.3, 0.55, 0.8, 0.97]) {
    const r = g.atProgress(q).radius;
    const back = g.progressAtRadius(r);
    assert.ok(Math.abs(back - q) < 1e-9, `progress ${q}: got ${back}`);
  }
});

test("extended progress continuation works both directions", () => {
  const p = { kind: DISC_DVD, innerRadiusMm: 24, outerRadiusMm: 58, sectors: 2297888, linearDensity: 133.3, pitchLinear: 0.05, pitchQuadratic: 0, pitchCubic: 0 };
  const g = new CalibrationGeometry(p);
  // A radius slightly outside [inner, outer] still resolves.
  const before = g.tryProgressAtRadiusExtended(23.5);
  const after = g.tryProgressAtRadiusExtended(58.5);
  assert.ok(before !== null && before < 0, `before=${before}`);
  assert.ok(after !== null && after > 1, `after=${after}`);
  const back = g.tryAtProgressExtended(before);
  assert.ok(Math.abs(back.radius - 23.5) < 1e-9);
});
