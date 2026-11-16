// Exact-value parity against the original C# implementation (harness output).
// The C# harness prints with "R" (shortest round-trip) formatting.

import { test } from "node:test";
import assert from "node:assert/strict";
import { Target, STAGE_COARSE, STAGE_BOUNDARY, STAGE_FINE } from "../internal/webui/static/js/target.js";
import { fitWideBandRadii } from "../internal/webui/static/js/fit.js";
import { fitBoundary } from "../internal/webui/static/js/boundaryfit.js";
import { fitCross } from "../internal/webui/static/js/crossfit.js";
import { CalibrationGeometry, DISC_DVD } from "../internal/webui/static/js/geometry.js";
import { CalibrationCrossModel } from "../internal/webui/static/js/crossmodel.js";

const generated = { kind: DISC_DVD, innerRadiusMm: 24, outerRadiusMm: 58, sectors: 2297888, linearDensity: 133.3, pitchLinear: 0, pitchQuadratic: 0, pitchCubic: 0, usableSectors: 0 };

function close(actual, expected, tol, label) {
  assert.ok(Math.abs(actual - expected) <= tol, `${label}: js=${actual} cs=${expected} tol=${tol}`);
}

// Same synthesized inputs as the C# harness.
function edgeObservations(actualInner, actualOuter) {
  const actualGeometry = new CalibrationGeometry({ ...generated, innerRadiusMm: actualInner, outerRadiusMm: actualOuter });
  const observations = [];
  const target = new Target(generated, STAGE_COARSE);
  for (let id = 0; id < 6; id++) {
    const band = target.wideBandProgressRanges[id < 3 ? id : id - 3];
    const q = id < 3 ? band.start : band.endExclusive;
    const r = actualGeometry.atProgress(q).radius;
    observations.push({ ringIndex: id, radiusMm: r, points: [{ x: r * Math.cos(0.3), y: r * Math.sin(0.3) }] });
  }
  return observations;
}

test("parity: wide-band ring fit", () => {
  const target = new Target(generated, STAGE_COARSE);
  const fit = fitWideBandRadii(target, edgeObservations(23.97, 58.04));
  assert.ok(fit.succeeded);
  // The C# Gauss-Newton stops at its own convergence rule (rms 6.9e-6, about
  // 12.6 nm from the true minimum); the JS port converges to the exact answer
  // (rms 7e-12). Both are ~1000x below the physical measurement floor (the
  // coarse stage works at ~0.01 mm); assert agreement within 5e-5 mm.
  close(fit.bestParameters.innerRadiusMm, 23.97001256076974, 5e-5, "inner");
  close(fit.bestParameters.outerRadiusMm, 58.040004650608964, 5e-5, "outer");
  assert.ok(fit.rmsErrorMm < 1e-5, "rms at the noise floor");
});

test("parity: boundary fit", () => {
  const target = new Target(generated, STAGE_BOUNDARY);
  const model = new CalibrationCrossModel(
    new CalibrationGeometry(generated),
    new CalibrationGeometry({ ...generated, innerRadiusMm: 23.992, outerRadiusMm: 58.008 }),
    DISC_DVD, false, target);
  const rotation = 0.3;
  const segments = [];
  let id = 0;
  for (const [q0, q1] of [[24.3, 33.0], [40.0, 49.0], [52.0, 57.5]]) {
    const points = [];
    for (let r = q0; r <= q1; r += 0.35) {
      const s = model.tryAtRadius(r);
      const angle = -Math.PI / 2 + s.phase + rotation;
      points.push({ x: r * Math.cos(angle), y: r * Math.sin(angle) });
    }
    segments.push({ id: id++, points });
  }
  const fit = fitBoundary(target, segments, { radiusRangeMm: 0.02 });
  assert.ok(fit.succeeded, fit.warnings.join(";"));
  close(fit.bestParameters.innerRadiusMm, 23.991999999687117, 1e-9, "inner");
  close(fit.bestParameters.outerRadiusMm, 58.00800000024263, 1e-9, "outer");
  close(fit.rotationRadians, 0.2999994386486161, 1e-6, "rotation");
});

test("parity: cross fit radii only", () => {
  const target = new Target(generated, STAGE_FINE);
  const model = new CalibrationCrossModel(
    new CalibrationGeometry(generated),
    new CalibrationGeometry({ ...generated, innerRadiusMm: 24.011, outerRadiusMm: 57.979 }),
    DISC_DVD, false, null);
  const rotation = 0.42;
  const points = [];
  for (let r = 25.5; r <= 56.5; r += 0.8) {
    const s = model.tryAtRadius(r);
    const angle = -Math.PI / 2 + s.phase + rotation;
    points.push({ x: r * Math.cos(angle), y: r * Math.sin(angle) });
  }
  const fit = fitCross(target, [{ armIndex: 0, points }], { radiusRangeMm: 0.05, fitPitch: true, adaptiveRobustScale: true });
  assert.ok(fit.succeeded, fit.warnings.join(";"));
  close(fit.bestParameters.innerRadiusMm, 24.01099999999999, 1e-9, "inner");
  close(fit.bestParameters.outerRadiusMm, 57.97900000000003, 1e-9, "outer");
  close(fit.rotationRadians, 0.4200000000142558, 1e-9, "rotation");
});

test("parity: cross fit cubic pitch", () => {
  const target = new Target(generated, STAGE_FINE);
  const model = new CalibrationCrossModel(
    new CalibrationGeometry(generated),
    new CalibrationGeometry({ ...generated, innerRadiusMm: 24.008, outerRadiusMm: 57.988, pitchLinear: -7.9e-5, pitchQuadratic: 1.46e-4, pitchCubic: -8.8e-5 }),
    DISC_DVD, false, null);
  const rotation = 0.42;
  const points = [];
  for (let r = 25.5; r <= 57.2; r += 0.25) {
    const s = model.tryAtRadius(r);
    const angle = -Math.PI / 2 + s.phase + rotation;
    points.push({ x: r * Math.cos(angle), y: r * Math.sin(angle) });
  }
  const fit = fitCross(target, [{ armIndex: 0, points }], { radiusRangeMm: 0.05, fitPitch: true, adaptiveRobustScale: true });
  assert.ok(fit.succeeded, fit.warnings.join(";"));
  const p = fit.bestParameters;
  close(p.innerRadiusMm, 24.00800000087024, 1e-8, "inner");
  close(p.outerRadiusMm, 57.98800000105773, 1e-8, "outer");
  close(p.pitchLinear, -7.899792221895008e-05, 1e-9, "c1");
  close(p.pitchQuadratic, 0.00014599525759983244, 1e-9, "c2");
  close(p.pitchCubic, -8.799670858850753e-05, 1e-9, "c3");
  close(fit.rotationRadians, 0.4200005657576253, 1e-8, "rotation");
});
