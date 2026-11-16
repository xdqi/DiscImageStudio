// Roundtrip tests: synthesize measurements from a known "actual" geometry and
// check the fitters recover the parameters.

import { test } from "node:test";
import assert from "node:assert/strict";
import { fitPhotoTransform, CalibrationPhotoTransform } from "../internal/webui/static/js/rectify.js";
import { Target, STAGE_COARSE, STAGE_BOUNDARY, STAGE_FINE } from "../internal/webui/static/js/target.js";
import { fitWideBandRadii } from "../internal/webui/static/js/fit.js";
import { fitBoundary } from "../internal/webui/static/js/boundaryfit.js";
import { fitCross } from "../internal/webui/static/js/crossfit.js";
import { CalibrationGeometry, DISC_CD, DISC_DVD } from "../internal/webui/static/js/geometry.js";
import { CalibrationCrossModel } from "../internal/webui/static/js/crossmodel.js";

const dvdParams = { kind: DISC_DVD, innerRadiusMm: 24, outerRadiusMm: 58, sectors: 2297888, linearDensity: 133.3, pitchLinear: 0, pitchQuadratic: 0, pitchCubic: 0, usableSectors: 0 };

test("photo rectifier recovers a perspective transform", () => {
  // True transform: disc mm -> source pixels, a tilted camera view.
  // Build a homography: scale 60 px/mm, disc centre at (800, 640), mild tilt.
  const tilt = 0.35;
  const s = 60;
  const cx = 800, cy = 640;
  // x' = (s*x + cx + tilt*0.0004*x*y) / (1 + 0.0009*y) style — construct a real 3x3.
  const m = [
    s, 0, cx,
    0, s, cy,
    0.00035, 0.00022, 1,
  ];
  const toSource = (p) => CalibrationPhotoTransform.map(m, p);
  const outer = [], hole = [];
  for (let i = 0; i < 24; i++) {
    const a = (i / 24) * 2 * Math.PI;
    outer.push(toSource({ x: 60 * Math.cos(a), y: 60 * Math.sin(a) }));
    hole.push(toSource({ x: 7.5 * Math.cos(a + 0.13), y: 7.5 * Math.sin(a + 0.13) }));
  }
  const fit = fitPhotoTransform(outer, hole, 60, 7.5);
  assert.ok(fit.succeeded, `fit failed: ${fit.message}`);
  assert.ok(fit.outerRmsPixels < 1 && fit.holeRmsPixels < 1, `rms ${fit.outerRmsPixels}/${fit.holeRmsPixels}`);
  // The homography is recovered up to a free in-plane rotation of the disc
  // (the cost function is rotation-invariant; rotation is fitted later during
  // measurement). Check: radii map exactly, and the angular offset is constant.
  const trueInv = CalibrationPhotoTransform.invert(m);
  const offsets = [];
  for (let i = 0; i < 24; i++) {
    const a = (i / 24) * 2 * Math.PI;
    const disc = { x: 40 * Math.cos(a), y: 40 * Math.sin(a) };
    const back = CalibrationPhotoTransform.map(trueInv, fit.transform.discToSourcePoint(disc));
    assert.ok(Math.abs(Math.hypot(back.x, back.y) - 40) < 1e-6, `radius ${i}: ${Math.hypot(back.x, back.y)}`);
    offsets.push(Math.atan2(back.y, back.x) - a);
  }
  const mean = Math.atan2(...offsets.reduce((s, o) => [s[0] + Math.sin(o), s[1] + Math.cos(o)], [0, 0]));
  for (const o of offsets) {
    assert.ok(Math.abs(Math.atan2(Math.sin(o - mean), Math.cos(o - mean))) < 1e-6, "rotation offset not constant");
  }
});

test("rectifier rejects collinear points", () => {
  const line = [0, 1, 2, 3, 4].map((i) => ({ x: 100 + i * 10, y: 200 }));
  const fit = fitPhotoTransform(line, line, 60, 7.5);
  assert.ok(!fit.succeeded);
});

test("wide-band ring fit recovers actual radii", () => {
  const target = new Target(dvdParams, STAGE_COARSE);
  // Simulate a burned disc whose actual radii differ from the generation
  // parameters.
  const actual = { ...dvdParams, innerRadiusMm: 23.97, outerRadiusMm: 58.04 };
  const actualGeometry = new CalibrationGeometry(actual);
  const observations = [];
  for (let id = 0; id < 6; id++) {
    const band = target.wideBandProgressRanges[id < 3 ? id : id - 3];
    const q = id < 3 ? band.start : band.endExclusive;
    observations.push({ ringIndex: id, radiusMm: actualGeometry.atProgress(q).radius });
  }
  const fit = fitWideBandRadii(target, observations);
  assert.ok(fit.succeeded, fit.warnings.join(";"));
  assert.ok(Math.abs(fit.bestParameters.innerRadiusMm - 23.97) < 1e-6, `inner ${fit.bestParameters.innerRadiusMm}`);
  assert.ok(Math.abs(fit.bestParameters.outerRadiusMm - 58.04) < 1e-6, `outer ${fit.bestParameters.outerRadiusMm}`);
});

test("boundary fitter recovers actual radii from synthesized segments", () => {
  const target = new Target(dvdParams, STAGE_BOUNDARY);
  const actual = { ...dvdParams, innerRadiusMm: 23.992, outerRadiusMm: 58.008 };
  // Synthesize boundary points: the boundary in photo coordinates follows the
  // cross model with the wavy source profile, plus a rotation.
  const rotation = 0.3;
  const model = new CalibrationCrossModel(
    new CalibrationGeometry(dvdParams), new CalibrationGeometry(actual), DISC_DVD, false, target);
  const segments = [];
  let id = 0;
  for (const [q0, q1] of [[24.3, 33], [40, 49], [52, 57.5]]) {
    const points = [];
    for (let r = q0; r <= q1; r += 0.35) {
      const s = model.tryAtRadius(r);
      assert.ok(s !== null, `no model at ${r}`);
      const angle = -Math.PI / 2 + s.phase + rotation;
      points.push({ x: r * Math.cos(angle), y: r * Math.sin(angle) });
    }
    segments.push({ id: id++, points });
  }
  const fit = fitBoundary(target, segments, { radiusRangeMm: 0.02 });
  assert.ok(fit.succeeded, `warnings: ${fit.warnings.join("; ")}`);
  assert.ok(Math.abs(fit.bestParameters.innerRadiusMm - 23.992) < 5e-4, `inner ${fit.bestParameters.innerRadiusMm}`);
  assert.ok(Math.abs(fit.bestParameters.outerRadiusMm - 58.008) < 5e-4, `outer ${fit.bestParameters.outerRadiusMm}`);
});

test("cross fitter recovers radii and rotation from a synthesized arm", () => {
  const target = new Target(dvdParams, STAGE_FINE);
  const actual = { ...dvdParams, innerRadiusMm: 24.011, outerRadiusMm: 57.979 };
  const rotation = 0.42;
  const model = new CalibrationCrossModel(
    new CalibrationGeometry(dvdParams), new CalibrationGeometry(actual), DISC_DVD, false, null);
  const points = [];
  for (let r = 25.5; r <= 56.5; r += 0.8) {
    const s = model.tryAtRadius(r);
    assert.ok(s !== null);
    const angle = -Math.PI / 2 + s.phase + rotation;
    points.push({ x: r * Math.cos(angle), y: r * Math.sin(angle) });
  }
  const fit = fitCross(target, [{ armIndex: 0, points }], { radiusRangeMm: 0.05, fitPitch: true });
  assert.ok(fit.succeeded, `warnings: ${fit.warnings.join("; ")}`);
  assert.ok(Math.abs(fit.bestParameters.innerRadiusMm - 24.011) < 2e-4, `inner ${fit.bestParameters.innerRadiusMm}`);
  assert.ok(Math.abs(fit.bestParameters.outerRadiusMm - 57.979) < 2e-4, `outer ${fit.bestParameters.outerRadiusMm}`);
  assert.ok(Math.abs(fit.rotationRadians - rotation) < 1e-3, `rotation ${fit.rotationRadians}`);
});

test("cross fitter recovers cubic pitch coefficients", () => {
  const target = new Target(dvdParams, STAGE_FINE);
  const actual = {
    ...dvdParams,
    innerRadiusMm: 24.008, outerRadiusMm: 57.988,
    pitchLinear: -7.9e-5, pitchQuadratic: 1.46e-4, pitchCubic: -8.8e-5,
  };
  const model = new CalibrationCrossModel(
    new CalibrationGeometry(dvdParams), new CalibrationGeometry(actual), DISC_DVD, false, null);
  const points = [];
  // Dense, full-coverage trace so the cubic fit is identifiable.
  for (let r = 25.2; r <= 57.2; r += 0.25) {
    const s = model.tryAtRadius(r);
    assert.ok(s !== null);
    const angle = -Math.PI / 2 + s.phase + 0.1;
    points.push({ x: r * Math.cos(angle), y: r * Math.sin(angle) });
  }
  const fit = fitCross(target, [{ armIndex: 0, points }], { radiusRangeMm: 0.05, fitPitch: true });
  assert.ok(fit.succeeded, `warnings: ${fit.warnings.join("; ")}`);
  const p = fit.bestParameters;
  assert.ok(Math.abs(p.innerRadiusMm - 24.008) < 2e-3, `inner ${p.innerRadiusMm}`);
  assert.ok(Math.abs(p.outerRadiusMm - 57.988) < 2e-3, `outer ${p.outerRadiusMm}`);
  assert.ok(Math.abs(p.pitchLinear - -7.9e-5) < 5e-5, `c1 ${p.pitchLinear}`);
  assert.ok(Math.abs(p.pitchQuadratic - 1.46e-4) < 5e-5, `c2 ${p.pitchQuadratic}`);
  assert.ok(Math.abs(p.pitchCubic - -8.8e-5) < 5e-5, `c3 ${p.pitchCubic}`);
});
