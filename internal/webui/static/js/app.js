// Disc Image Studio — browser calibration tool.
// Workflow: pick disc kind + stage + parameters → burn the shown CLI command's
// output → photograph the disc → rectify the photo → measure → apply the
// fitted parameters → next stage.

import { Photo, renderRectified, rectifiedSampler, MAX_RECTIFIED_DIMENSION } from "./photo.js";
import { fitPhotoTransform } from "./rectify.js";
import { ImageCanvas } from "./canvas.js";
import { Target, STAGE_COARSE, STAGE_BOUNDARY, STAGE_FINE } from "./target.js";
import { DISC_CD, DISC_DVD, DISC_BD, CD_LINEAR_VELOCITY, DVD_CHANNEL_BIT_LENGTH_NM, BD_CHANNEL_BIT_LENGTH_NM } from "./geometry.js";
import { fitWideBandRadii, wideBandIntervals } from "./fit.js";
import { readAngleSweep } from "./sweep.js";
import { fitBoundary, boundaryHasEnoughPoints } from "./boundaryfit.js";
import { fitCross } from "./crossfit.js";
import { predictedOutline } from "./crossmodel.js";

const $ = (id) => document.getElementById(id);

const state = {
  photo: null,
  physicalOuterRadiusMm: 60,
  outerPoints: [],
  holePoints: [],
  transform: null,
  rectified: null,
  coarseEdges: [null, null, null, null, null, null], // {x,y} in rectified mm
  coarseEdgePoints: [null, null, null, null, null, null], // source points for reference
  boundarySegments: [],
  boundaryCurrent: null,
  fineTrace: [],
  fitResult: null,
  selectedCandidate: 0,
};

const COARSE_EDGE_NAMES = ["band 1 inner", "band 1 outer", "band 2 inner", "band 2 outer", "band 3 inner", "band 3 outer"];

// Physical centre-hole radius (mm) used by the rectifier.
const PHYSICAL_HOLE_RADIUS_MM = 7.5;

// ---------- parameters & burn command ----------

function readParams() {
  const kind = $("kind").value;
  const density = parseFloat($("p-density").value);
  // DVD calibration always draws the full disc.
  const usable = kind === DISC_DVD ? 0 : (parseInt($("p-usable").value, 10) || 0);
  return {
    kind,
    innerRadiusMm: parseFloat($("p-r0").value),
    outerRadiusMm: parseFloat($("p-r1").value),
    sectors: parseInt($("p-sectors").value, 10),
    linearDensity: Number.isFinite(density) ? density : defaultDensity(kind),
    pitchLinear: parseFloat($("p-c1").value) || 0,
    pitchQuadratic: parseFloat($("p-c2").value) || 0,
    pitchCubic: parseFloat($("p-c3").value) || 0,
    usableSectors: usable,
  };
}

function defaultDensity(kind) {
  return kind === DISC_CD ? CD_LINEAR_VELOCITY : kind === DISC_BD ? BD_CHANNEL_BIT_LENGTH_NM : DVD_CHANNEL_BIT_LENGTH_NM;
}

function writeParams(p) {
  $("kind").value = p.kind;
  $("p-r0").value = trimNumber(p.innerRadiusMm);
  $("p-r1").value = trimNumber(p.outerRadiusMm);
  $("p-sectors").value = p.sectors;
  $("p-density").value = trimNumber(p.linearDensity);
  $("p-c1").value = trimNumber(p.pitchLinear);
  $("p-c2").value = trimNumber(p.pitchQuadratic);
  $("p-c3").value = trimNumber(p.pitchCubic);
  $("p-usable").value = p.usableSectors > 0 ? p.usableSectors : "";
  updateKindUI();
  renderCommand();
}

function trimNumber(v) {
  if (!Number.isFinite(v)) return "0";
  return String(Math.round(v * 1e12) / 1e12);
}

function updateKindUI() {
  const kind = $("kind").value;
  $("density-label").textContent = kind === DISC_CD ? "Linear velocity [mm/s]" : "Channel bit length [nm]";
  if (kind === DISC_CD) $("p-density").value = CD_LINEAR_VELOCITY;
  else if (kind === DISC_BD) $("p-density").value = BD_CHANNEL_BIT_LENGTH_NM;
  else $("p-density").value = DVD_CHANNEL_BIT_LENGTH_NM;
  if (kind === DISC_CD && $("p-sectors").value === "2297888") $("p-sectors").value = "359849";
  if (kind !== DISC_CD && $("p-sectors").value === "359849") $("p-sectors").value = "2297888";
  // DVD targets always draw the full disc.
  $("p-usable").disabled = kind === DISC_DVD;
  $("p-usable").title = kind === DISC_DVD ? "DVD targets always draw the full disc" : "";
}

function renderCommand() {
  const p = readParams();
  const stage = $("stage").value;
  const ext = p.kind === DISC_CD ? "wav" : "iso";
  const parts = [
    "disc-image-studio", "calibration-target",
    "--kind", p.kind, "--stage", stage,
    "--output", `calibration-${stage}.${ext}`,
    "--sectors", String(p.sectors),
    "--r0", trimNumber(p.innerRadiusMm), "--r1", trimNumber(p.outerRadiusMm),
  ];
  if (stage !== STAGE_COARSE && (p.pitchLinear || p.pitchQuadratic || p.pitchCubic)) {
    parts.push("--pitch-linear", trimNumber(p.pitchLinear),
      "--pitch-quadratic", trimNumber(p.pitchQuadratic),
      "--pitch-cubic", trimNumber(p.pitchCubic));
  }
  if (p.usableSectors > 0) parts.push("--usable-sectors", String(p.usableSectors));
  if (p.kind === DISC_CD) parts.push("--velocity", trimNumber(p.linearDensity));
  if (p.kind === DISC_DVD) parts.push("--channel-bit-nm", trimNumber(p.linearDensity));
  $("burn-command").textContent = parts.join(" ");
}

// ---------- photo & rectification ----------

let photoCanvas = null;
let rectCanvas = null;
let photoClickMode = "outer";

// Binds the photo canvas to the current edge-point arrays. The point sets
// hold live references to state.outerPoints / state.holePoints, so those
// arrays must always be mutated in place (never reassigned) afterwards.
function wirePhotoCanvas() {
  photoCanvas.pointSets = [
    { points: state.outerPoints, color: "#4da3ff", world: false },
    { points: state.holePoints, color: "#ffa54d", world: false },
  ];
  photoCanvas.onClick = (world) => {
    const list = photoClickMode === "outer" ? state.outerPoints : state.holePoints;
    list.push({ x: world.x, y: world.y });
    photoCanvas.redraw();
  };
  photoCanvas.onPointDrag = (set, index, world) => {
    set.points[index] = { x: world.x, y: world.y };
    photoCanvas.redraw();
  };
  photoCanvas.onPointRemove = (set, index) => {
    set.points.splice(index, 1);
    photoCanvas.redraw();
  };
}

async function importPhoto(file) {
  try {
    state.photo = await Photo.load(file);
  } catch (error) {
    setStatus($("rectify-status"), `Could not read the photo: ${error.message ?? error}`, "error");
    return;
  }
  state.transform = null;
  state.rectified = null;
  state.outerPoints = [];
  state.holePoints = [];
  // A new photo starts a new measurement session.
  state.coarseEdges = [null, null, null, null, null, null];
  state.boundarySegments = [];
  state.boundaryCurrent = null;
  state.fineTrace = [];
  $("measure-panel").classList.add("hidden");
  if (!photoCanvas) photoCanvas = new ImageCanvas($("photo-canvas"), state.photo.canvas);
  else photoCanvas.setImage(state.photo.canvas, false);
  photoCanvas.overlays = []; // any previous rectification belongs to the old photo
  wirePhotoCanvas();
  photoCanvas.fit();
  setStatus($("rectify-status"),
    `Photo loaded (${state.photo.width}×${state.photo.height}). Mark the physical outer edge and the centre-hole edge, then compute the rectification.`, "");
}

// Draws the computed disc outline (physical outer edge and centre hole) back
// onto the photo through the rectification transform, so the fit can be
// verified against the picture. Clears the overlay when no transform is set.
function updateRectificationOverlay() {
  if (!photoCanvas) return;
  photoCanvas.overlays = [];
  if (state.transform) {
    try {
      for (const [radiusMm, color] of [
        [state.physicalOuterRadiusMm, "#4da3ff"],
        [PHYSICAL_HOLE_RADIUS_MM, "#ffa54d"],
      ]) {
        const points = [];
        for (let i = 0; i <= 360; i++) {
          const angle = (i / 360) * 2 * Math.PI;
          const p = state.transform.discToSourcePoint({ x: radiusMm * Math.cos(angle), y: radiusMm * Math.sin(angle) });
          points.push([p.x, p.y]);
        }
        photoCanvas.overlays.push({ points, color, width: 2, dash: [6, 4], world: false });
      }
    } catch {
      photoCanvas.overlays = []; // near the projective horizon: no overlay
    }
  }
  photoCanvas.redraw();
}

function computeRectification() {
  if (!state.photo) {
    setStatus($("rectify-status"), "Import a photo first.", "error");
    return;
  }
  const physicalOuter = parseFloat($("disc-size").value);
  const result = fitPhotoTransform(state.outerPoints, state.holePoints, physicalOuter, PHYSICAL_HOLE_RADIUS_MM);
  setStatus($("rectify-status"), result.message, result.succeeded ? "ok" : "error");
  if (!result.succeeded) {
    // No new rectification: drop the stale overlay (the points may have moved).
    if (photoCanvas) {
      photoCanvas.overlays = [];
      photoCanvas.redraw();
    }
    return;
  }
  state.transform = result.transform;
  state.physicalOuterRadiusMm = physicalOuter;
  updateRectificationOverlay();
  renderRectifiedView(2400);
}

function renderRectifiedView(size) {
  const rectified = renderRectified(state.photo, state.transform, state.physicalOuterRadiusMm, size);
  state.rectified = rectified;
  // Unhide before touching the canvas layout: fitting a canvas inside a
  // display:none panel measures a zero size and renders a one-pixel speck.
  $("measure-panel").classList.remove("hidden");
  if (!rectCanvas) rectCanvas = new ImageCanvas($("rectified-canvas"), rectified.canvas);
  else rectCanvas.setImage(rectified.canvas, false);
  rectCanvas.setWorldTransform(-rectified.canvasRadiusMm, -rectified.canvasRadiusMm,
    rectified.canvasRadiusMm, rectified.canvasRadiusMm);
  rectCanvas.fit();
  setupMeasurement();
  $("measure-panel").scrollIntoView({ behavior: "smooth", block: "start" });
}

// ---------- measurement ----------

function setupMeasurement() {
  const stage = $("stage").value;
  state.fitResult = null;
  state.selectedCandidate = 0;
  $("fit-result").innerHTML = "";
  $("apply-fit").disabled = true;
  const buttons = $("measure-buttons");
  buttons.innerHTML = "";
  const instructions = $("measure-instructions");
  const p = readParams();
  const target = new Target(p, stage);
  // Clear leftovers from a previous stage.
  rectCanvas.polylines = [];
  rectCanvas.overlays = [];
  rectCanvas.pointSets = [];

  if (stage === STAGE_COARSE) {
    instructions.textContent =
      "Along one radius, click the six band edges in order: band 1 inner, band 1 outer, band 2 inner, band 2 outer, band 3 inner, band 3 outer. Then estimate the radii and read the angle markers.";
    const estimate = makeButton("Estimate radii and read angle markers", runCoarseFit, "go");
    buttons.append(estimate);
    setupCoarseCanvas();
  } else if (stage === STAGE_BOUNDARY) {
    instructions.textContent =
      "Trace the clear light/dark boundary: click points along one continuous piece; start a new segment for another piece. Cover at least 55% of the drawing radius range.";
    const newSegment = makeButton("New segment", () => {
      state.boundaryCurrent = { id: state.boundarySegments.length, points: [] };
      state.boundarySegments.push(state.boundaryCurrent);
      rebuildBoundaryCanvas();
    });
    const solve = makeButton("Solve parameters", runBoundaryFit, "go");
    const rangeLabel = document.createElement("label");
    rangeLabel.innerHTML = 'Search ±mm <input id="fit-range" type="number" step="0.005" value="0.01" min="0.001" max="0.05">';
    buttons.append(newSegment, solve, rangeLabel);
    // Keep state.boundarySegments (session restore / stage switch); new
    // clicks must not append to a loaded segment.
    state.boundaryCurrent = null;
    setupBoundaryCanvas();
  } else {
    instructions.textContent =
      "From the disc centre outward, click along the centreline of one clear cross arm. At least 6 points; 12+ distinct radii over 60% of the band also fits the cubic pitch.";
    const solve = makeButton("Solve parameters", runFineFit, "go");
    const pitchLabel = document.createElement("label");
    pitchLabel.innerHTML = 'Fit cubic pitch <input id="fit-pitch" type="checkbox" checked>';
    const rangeLabel = document.createElement("label");
    rangeLabel.innerHTML = 'Search ±mm <input id="fit-range" type="number" step="0.01" value="0.02" min="0.001" max="5">';
    buttons.append(solve, pitchLabel, rangeLabel);
    // state.fineTrace is kept (session restore / stage switch).
    setupFineCanvas(target);
  }
}

function makeButton(text, onClick, cls = "") {
  const button = document.createElement("button");
  button.type = "button";
  button.textContent = text;
  if (cls) button.className = cls;
  button.addEventListener("click", onClick);
  return button;
}

// -- coarse --

function setupCoarseCanvas() {
  // Restore edges already marked (session load / stage switch): the filled
  // slots of state.coarseEdges form a prefix, so they resume in order.
  const restored = state.coarseEdges.filter((p) => p != null);
  let nextEdge = restored.length;
  rectCanvas.pointSets = [{
    points: restored.map((p) => ({ x: p.x, y: p.y })),
    color: "#67e0b8",
    world: true,
    labels: restored.map((_, i) => COARSE_EDGE_NAMES[i]),
  }];
  const set = rectCanvas.pointSets[0];
  rectCanvas.onClick = (world) => {
    if (nextEdge >= 6) return;
    state.coarseEdges[nextEdge] = { x: world.x, y: world.y };
    set.points.push(world);
    set.labels.push(COARSE_EDGE_NAMES[nextEdge]);
    nextEdge++;
    rectCanvas.redraw();
    setStatus($("measure-status"), nextEdge < 6
      ? `Marked ${COARSE_EDGE_NAMES[nextEdge - 1]}. Next: ${COARSE_EDGE_NAMES[nextEdge]}.`
      : "All six edges marked. Run the estimate.", "");
  };
  rectCanvas.onPointDrag = (s, index, world) => {
    s.points[index] = { x: world.x, y: world.y };
    state.coarseEdges[index] = { x: world.x, y: world.y };
    rectCanvas.redraw();
  };
  rectCanvas.onPointRemove = (s, index) => {
    s.points.splice(index, 1);
    s.labels.splice(index, 1);
    state.coarseEdges.splice(index, 1);
    state.coarseEdges.push(null);
    nextEdge--;
    rectCanvas.redraw();
  };
}

function runCoarseFit() {
  const p = readParams();
  const target = new Target(p, STAGE_COARSE);
  // The six UI slots (band 1 inner/outer … band 3 inner/outer) map to the
  // shared edge IDs 0..5 as [0, 3, 1, 4, 2, 5] (inner edges are 0..2, outer 3..5).
  const slotToEdgeId = [0, 3, 1, 4, 2, 5];
  const observations = [];
  for (let i = 0; i < 6; i++) {
    const point = state.coarseEdges[i];
    if (!point) {
      setStatus($("measure-status"), `Edge "${COARSE_EDGE_NAMES[i]}" is missing.`, "error");
      return;
    }
    const radius = Math.hypot(point.x, point.y);
    observations.push({ ringIndex: slotToEdgeId[i], radiusMm: radius, points: [point] });
  }
  const radial = fitWideBandRadii(target, observations);
  if (!radial.succeeded) {
    setStatus($("measure-status"), radial.warnings.join("\n"), "error");
    return;
  }
  const sweep = readAngleSweep(target, observations, state.rectified.canvasRadiusMm,
    rectifiedSampler(state.rectified.canvas, state.rectified.canvasRadiusMm));
  const intervals = wideBandIntervals(target, observations);
  const bandFit = radial.bestParameters;
  let html = `<table><tr><th></th><th>Inner radius</th><th>Outer radius</th></tr>
    <tr><td>Band edges</td><td>${bandFit.innerRadiusMm.toFixed(4)} mm</td><td>${bandFit.outerRadiusMm.toFixed(4)} mm</td></tr>`;
  let finalParameters = bandFit;
  let sweepNote = "";
  if (sweep.status === "success") {
    finalParameters = sweep.corrected;
    html += `<tr><td>Angle-mark corrected</td><td>${sweep.corrected.innerRadiusMm.toFixed(4)} mm</td><td>${sweep.corrected.outerRadiusMm.toFixed(4)} mm</td></tr>`;
    sweepNote = ` Angle markers read at 3 stations, coverage ${sweep.coverageDegrees.toFixed(1)}°.` +
      " Turn-rate offset at inner/middle/outer lock: " +
      sweep.degreesPerTurnOffset.map((d) => d.toFixed(3) + "°/turn").join(", ") + ".";
  } else if (sweep.status === "conflicting") {
    sweepNote = " Angle markers conflict across stations; keeping the band-edge estimate. Retake the photo with different lighting if in doubt.";
  } else {
    sweepNote = " Angle markers too weak to read; keeping the band-edge estimate.";
  }
  html += "</table>";
  html += `<p class="hint">Wide-band spacings: ${intervals.innerEdgeSpacingsMm.map((v) => v === null ? "—" : v.toFixed(3) + " mm").join(" / ")};` +
    ` predicted from the outer edges: ${intervals.predictedInnerEdgeSpacingsMm.map((v) => v === null ? "—" : v.toFixed(3) + " mm").join(" / ")}.</p>`;
  $("fit-result").innerHTML = html;
  setStatus($("measure-status"),
    `Band RMS ${radial.rmsErrorMm.toFixed(4)} mm.${sweepNote}`, "ok");
  state.fitResult = { succeeded: true, bestParameters: finalParameters, candidates: [{ parameters: finalParameters, rotationRadians: 0, rmsErrorMm: radial.rmsErrorMm }], warnings: [] };
  state.selectedCandidate = 0;
  $("apply-fit").disabled = false;
}

// -- boundary --

function setupBoundaryCanvas() {
  rebuildBoundaryCanvas();
}

function rebuildBoundaryCanvas() {
  const pointSets = [];
  const polylines = [];
  const colors = ["#67e0b8", "#4da3ff", "#ffa54d", "#ff7d6b", "#d38bff", "#ffe47d", "#7dd8ff", "#a5ff8a"];
  state.boundarySegments.forEach((segment, i) => {
    const color = colors[i % colors.length];
    pointSets.push({ points: segment.points, color, world: true, labels: segment.points.map((_, j) => `${i + 1}.${j + 1}`) });
    polylines.push({ points: segment.points, color, world: true, width: 1 });
  });
  rectCanvas.pointSets = pointSets;
  rectCanvas.polylines = polylines;
  rectCanvas.onClick = (world) => {
    if (!state.boundaryCurrent) {
      state.boundaryCurrent = { id: state.boundarySegments.length, points: [] };
      state.boundarySegments.push(state.boundaryCurrent);
    }
    state.boundaryCurrent.points.push({ x: world.x, y: world.y });
    rebuildBoundaryCanvas();
    rectCanvas.redraw();
  };
  rectCanvas.onPointDrag = (set, index, world) => {
    set.points[index] = { x: world.x, y: world.y };
    rebuildBoundaryCanvas();
    rectCanvas.redraw();
  };
  rectCanvas.onPointRemove = (set, index) => {
    set.points.splice(index, 1);
    state.boundarySegments = state.boundarySegments.filter((s) => s.points.length > 0);
    rebuildBoundaryCanvas();
    rectCanvas.redraw();
  };
  rectCanvas.redraw();
}

function runBoundaryFit() {
  const p = readParams();
  const target = new Target(p, STAGE_BOUNDARY);
  const segments = state.boundarySegments.filter((s) => s.points.length > 0);
  if (!boundaryHasEnoughPoints(segments)) {
    setStatus($("measure-status"),
      "Not enough boundary points: trace one continuous piece (≥6 points over 55% of the radius), two 10% pieces, or at least 4 short segments.", "error");
    return;
  }
  const range = parseFloat($("fit-range")?.value ?? "0.01") || 0.01;
  setStatus($("measure-status"), "Solving…", "");
  // Let the status paint before the heavy synchronous fit.
  setTimeout(() => {
    const result = fitBoundary(target, segments, { radiusRangeMm: range });
    state.fitResult = result;
    state.selectedCandidate = 0;
    showFitResult(result, target, 2);
    // A verification failure still carries the candidates the solver found:
    // let the user review the predicted curve and apply anyway.
    $("apply-fit").disabled = result.candidates.length === 0;
  }, 30);
}

// -- fine --

function setupFineCanvas(target) {
  // state.fineTrace keeps any restored trace; number the points.
  rectCanvas.pointSets = [{
    points: state.fineTrace,
    color: "#67e0b8",
    world: true,
    labels: state.fineTrace.map((_, i) => String(i + 1)),
  }];
  rectCanvas.onClick = (world) => {
    state.fineTrace.push({ x: world.x, y: world.y });
    rectCanvas.pointSets[0].labels = state.fineTrace.map((_, i) => String(i + 1));
    rectCanvas.redraw();
  };
  rectCanvas.onPointDrag = (set, index, world) => {
    set.points[index] = { x: world.x, y: world.y };
    rectCanvas.redraw();
  };
  rectCanvas.onPointRemove = (set, index) => {
    set.points.splice(index, 1);
    rectCanvas.pointSets[0].labels = state.fineTrace.map((_, i) => String(i + 1));
    rectCanvas.redraw();
  };
}

function runFineFit() {
  const p = readParams();
  const target = new Target(p, STAGE_FINE);
  if (state.fineTrace.length < 6) {
    setStatus($("measure-status"), "Trace at least 6 points along one cross arm.", "error");
    return;
  }
  const range = parseFloat($("fit-range")?.value ?? "0.02") || 0.02;
  const fitPitch = $("fit-pitch")?.checked ?? true;
  setStatus($("measure-status"), "Solving…", "");
  setTimeout(() => {
    const result = fitCross(target, [{ armIndex: 0, points: state.fineTrace }], {
      radiusRangeMm: range,
      fitPitch,
      adaptiveRobustScale: true,
    });
    state.fitResult = result;
    state.selectedCandidate = 0;
    showFitResult(result, target, 4);
    $("apply-fit").disabled = !result.succeeded;
  }, 30);
}

// -- fit result display & prediction overlay --

function showFitResult(result, target, armCount) {
  const verified = result.succeeded && result.bestParameters;
  if (!verified && result.candidates.length === 0) {
    // Hard failure: the solver found nothing — show only the reason.
    $("fit-result").innerHTML = `<p class="warn">${result.warnings.map(escapeHtml).join("<br>")}</p>`;
    drawPrediction(target, null, 0);
    return;
  }
  // Verification failures still carry candidates: present them as a warning
  // with the full result and the predicted curve, so the user can judge the
  // fit against the photo.
  let html = `<p>RMS ${result.rmsErrorMm.toFixed(4)} mm · rotation ${(result.rotationRadians * 180 / Math.PI).toFixed(3)}°</p>`;
  html += `<table><tr><th>#</th><th>Inner</th><th>Outer</th><th>pitch c1</th><th>c2</th><th>c3</th><th>RMS</th><th>Max</th></tr>`;
  result.candidates.forEach((c, i) => {
    html += `<tr class="candidate" data-index="${i}" style="cursor:pointer">
      <td>${i + 1}</td><td>${c.parameters.innerRadiusMm.toFixed(4)}</td><td>${c.parameters.outerRadiusMm.toFixed(4)}</td>
      <td>${trimNumber(c.parameters.pitchLinear)}</td><td>${trimNumber(c.parameters.pitchQuadratic)}</td><td>${trimNumber(c.parameters.pitchCubic)}</td>
      <td>${c.rmsErrorMm.toFixed(4)}</td><td>${c.maxErrorMm.toFixed(3)}</td></tr>`;
  });
  html += "</table>";
  if (result.warnings.length > 0) {
    html += `<p class="warn">${result.warnings.map(escapeHtml).join("<br>")}</p>`;
  }
  html += '<p class="hint">Select a row to overlay its prediction; apply only after checking the overlay follows the visible curve.</p>';
  $("fit-result").innerHTML = html;
  for (const row of document.querySelectorAll(".candidate")) {
    row.addEventListener("click", () => {
      state.selectedCandidate = parseInt(row.dataset.index, 10);
      drawPrediction(target, result.candidates[state.selectedCandidate], armCount);
      for (const r of document.querySelectorAll(".candidate")) r.style.background = "";
      row.style.background = "#223252";
    });
  }
  drawPrediction(target, result.candidates[state.selectedCandidate], armCount);
  if (!verified) {
    setStatus($("measure-status"),
      "Automatic verification did not pass — showing the best fit anyway. Check the predicted curve against the photo before applying.", "warning");
  } else {
    setStatus($("measure-status"), result.warnings.length === 0
      ? "Fit succeeded. Check the predicted curve against the photo, then apply."
      : "Fit produced warnings; review them before applying.", result.warnings.length === 0 ? "ok" : "warning");
  }
}

function drawPrediction(target, candidate, armCount) {
  rectCanvas.overlays = [];
  if (!candidate) {
    rectCanvas.redraw();
    return;
  }
  const colors = ["#ff7d6b", "#ffd479", "#4da3ff", "#d38bff"];
  for (let arm = 0; arm < armCount; arm++) {
    try {
      const outline = predictedOutline(target, candidate.parameters,
        target.drawingInnerRadiusMm, target.drawingOuterRadiusMm,
        candidate.rotationRadians, arm);
      rectCanvas.overlays.push({
        points: outline.map((p) => [p.x, p.y]),
        color: colors[arm % colors.length],
        width: 2,
      });
    } catch { /* outside the valid model */ }
  }
  rectCanvas.redraw();
}

function escapeHtml(text) {
  return text.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

// ---------- apply ----------

function applyFit() {
  const result = state.fitResult;
  if (!result || result.candidates.length === 0) return;
  const candidate = result.candidates[state.selectedCandidate] ?? result.candidates[0];
  const p = { ...readParams(), ...candidate.parameters };
  writeParams(p);
  const stage = $("stage").value;
  if (stage === STAGE_COARSE) $("stage").value = STAGE_BOUNDARY;
  renderCommand();
  setStatus($("measure-status"),
    "Parameters applied to the setup panel. Generate the next target with the updated burn command, burn a new disc, and measure its own photo.", "ok");
  window.scrollTo({ top: 0, behavior: "smooth" });
}

// ---------- sessions ----------

function saveSession() {
  const session = {
    format: "disc-image-studio-web-calibration-v1",
    stage: $("stage").value,
    parameters: readParams(),
    physicalOuterRadiusMm: state.physicalOuterRadiusMm,
    outerPoints: state.outerPoints,
    holePoints: state.holePoints,
    transform9: state.transform ? state.transform.discToSource : null,
    coarseEdges: state.coarseEdges,
    boundarySegments: state.boundarySegments,
    fineTrace: state.fineTrace,
    photoPng: state.photo ? state.photo.toDataUrl(96 * 1024 * 1024) : null,
  };
  const blob = new Blob([JSON.stringify(session, null, 2)], { type: "application/json" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = `calibration-${$("stage").value}-${new Date().toISOString().slice(0, 10)}.json`;
  a.click();
  URL.revokeObjectURL(url);
}

async function loadSession(file) {
  let session;
  try {
    session = JSON.parse(await file.text());
  } catch {
    setStatus($("rectify-status"), "The session file is not valid JSON.", "error");
    return;
  }
  if (session.format !== "disc-image-studio-web-calibration-v1") {
    setStatus($("rectify-status"), "Not a browser calibration session.", "error");
    return;
  }
  writeParams(session.parameters);
  $("stage").value = session.stage;
  state.physicalOuterRadiusMm = session.physicalOuterRadiusMm ?? 60;
  $("disc-size").value = String(state.physicalOuterRadiusMm);
  state.outerPoints = session.outerPoints ?? [];
  state.holePoints = session.holePoints ?? [];
  state.coarseEdges = session.coarseEdges ?? [null, null, null, null, null, null];
  state.boundarySegments = session.boundarySegments ?? [];
  state.fineTrace = session.fineTrace ?? [];
  state.transform = null;
  state.rectified = null;
  if (session.photoPng) {
    state.photo = await Photo.fromDataUrl(session.photoPng);
    if (!photoCanvas) photoCanvas = new ImageCanvas($("photo-canvas"), state.photo.canvas);
    else photoCanvas.setImage(state.photo.canvas, false);
    wirePhotoCanvas();
    photoCanvas.fit();
    if (session.transform9) {
      try {
        const { CalibrationPhotoTransform } = await import("./rectify.js");
        state.transform = new CalibrationPhotoTransform(session.transform9);
        updateRectificationOverlay();
        renderRectifiedView(2400);
      } catch { /* keep unrectified */ }
    }
  }
  renderCommand();
}

// ---------- wiring ----------

function setStatus(element, message, cls) {
  element.textContent = message;
  element.className = `status ${cls}`;
}

function main() {
  for (const id of ["kind", "stage", "p-r0", "p-r1", "p-sectors", "p-density", "p-c1", "p-c2", "p-c3", "p-usable"]) {
    $(id).addEventListener("change", () => {
      if (id === "kind") updateKindUI();
      renderCommand();
    });
    $(id).addEventListener("input", renderCommand);
  }
  $("stage").addEventListener("change", () => {
    if (state.rectified) setupMeasurement();
  });
  $("copy-command").addEventListener("click", async () => {
    await navigator.clipboard.writeText($("burn-command").textContent);
    $("copy-command").textContent = "Copied";
    setTimeout(() => { $("copy-command").textContent = "Copy"; }, 1200);
  });
  $("photo-input").addEventListener("change", (e) => {
    if (e.target.files.length > 0) importPhoto(e.target.files[0]);
  });
  $("mode-outer").addEventListener("click", () => {
    photoClickMode = "outer";
    $("mode-outer").classList.add("active");
    $("mode-hole").classList.remove("active");
  });
  $("mode-hole").addEventListener("click", () => {
    photoClickMode = "hole";
    $("mode-hole").classList.add("active");
    $("mode-outer").classList.remove("active");
  });
  $("clear-points").addEventListener("click", () => {
    // Clear in place: the canvas point sets reference these exact arrays, so
    // reassigning them here would leave the old points on screen.
    state.outerPoints.length = 0;
    state.holePoints.length = 0;
    if (photoCanvas) {
      photoCanvas.overlays = []; // the computed outline belonged to those points
      photoCanvas.redraw();
    }
    setStatus($("rectify-status"), "Edge points cleared.", "");
  });
  $("rectify").addEventListener("click", computeRectification);
  $("apply-fit").addEventListener("click", applyFit);
  $("save-session").addEventListener("click", saveSession);
  $("load-session").addEventListener("change", (e) => {
    if (e.target.files.length > 0) loadSession(e.target.files[0]);
  });
  updateKindUI();
  renderCommand();
}

main();
