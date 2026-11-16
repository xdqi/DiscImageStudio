// Tests for the pan/zoom ImageCanvas and the app wiring around it:
//  - fit() inside a hidden panel must defer until a real layout exists
//    (regression: the measurement view rendered as a one-pixel speck);
//  - redraws are coalesced per animation frame, never reallocate the bitmap,
//    and blit only the visible slice of the source image (zoom performance);
//  - dense prediction overlays are decimated to screen resolution;
//  - the Clear button empties the very arrays the canvas draws
//    (regression: it reassigned them, leaving the markers on screen);
//  - re-importing a photo keeps a single set of canvas handlers.

import { test } from "node:test";
import assert from "node:assert/strict";
import { ImageCanvas } from "../internal/webui/static/js/canvas.js";

const JS = "../internal/webui/static/js/";

let roCallbacks = [];
let rafQueue = [];

class FakeResizeObserver {
  constructor(cb) { roCallbacks.push(cb); }
  observe() {}
  unobserve() {}
  disconnect() {}
}

function installGlobals() {
  roCallbacks = [];
  rafQueue = [];
  globalThis.ResizeObserver = FakeResizeObserver;
  globalThis.requestAnimationFrame = (fn) => { rafQueue.push(fn); return 0; };
}

function flushFrames() {
  const queue = rafQueue;
  rafQueue = [];
  for (const fn of queue) fn();
}

async function settleIdle() {
  await new Promise((resolve) => setTimeout(resolve, 180)); // > INTERACTION_IDLE_MS
  flushFrames();
}

// A <canvas>-element mock with a recording 2D context.
function makeCanvas(cssWidth) {
  const calls = { drawImage: [], moveTo: 0, lineTo: 0, arc: 0, fillRect: 0, fillText: [] };
  const el = {
    cssWidth,
    _width: 300,
    _height: 150,
    widthWrites: 0,
    listeners: {},
    get width() { return this._width; },
    set width(v) { this._width = v; this.widthWrites++; },
    get height() { return this._height; },
    set height(v) { this._height = v; },
    getContext() { return this.ctx; },
    addEventListener(type, fn) { (this.listeners[type] ??= []).push(fn); },
    setPointerCapture() {},
    getBoundingClientRect() {
      return { width: this.cssWidth, height: this.cssWidth / 2, left: 0, top: 0 };
    },
    fire(type, event) { for (const fn of this.listeners[type] ?? []) fn(event); },
  };
  el.ctx = {
    calls,
    fillRect() { calls.fillRect++; },
    drawImage(...args) { calls.drawImage.push(args); },
    beginPath() {}, closePath() {}, stroke() {}, fill() {},
    moveTo() { calls.moveTo++; },
    lineTo() { calls.lineTo++; },
    arc() { calls.arc++; },
    fillText(text) { calls.fillText.push(String(text)); },
    setLineDash() {},
    save() {}, restore() {}, translate() {}, scale() {},
    getImageData(x, y, w, h) { return { data: new Uint8ClampedArray(w * h * 4), width: w, height: h }; },
    createImageData(w, h) { return { data: new Uint8ClampedArray(w * h * 4), width: w, height: h }; },
    putImageData() {},
  };
  return el;
}

const fakeImage = { width: 2400, height: 2400 };

const wheel = (el, deltaY) =>
  el.fire("wheel", { deltaY, clientX: 400, clientY: 300, preventDefault() {} });

test("fit() inside a hidden panel defers to the first real layout", () => {
  installGlobals();
  const el = makeCanvas(0); // display:none measures as zero width
  const view = new ImageCanvas(el, fakeImage);
  view.fit();
  assert.equal(view._fitValid, false);
  // The panel becomes visible; the ResizeObserver fires with the real size.
  el.cssWidth = 800;
  roCallbacks[0]();
  assert.equal(view._fitValid, true);
  assert.equal(el.width, 800);
  assert.ok(Math.abs(view.scale - 800 / 2400) < 1e-12, `scale=${view.scale}`);
  flushFrames();
  const blit = el.ctx.calls.drawImage.at(-1);
  assert.deepEqual(blit.slice(1, 5), [0, 0, 2400, 2400]); // full source …
  assert.ok(Math.abs(blit[7] - 800) < 1e-9);              // … fills the viewport
});

test("wheel zooms are coalesced, reallocation-free and clipped", async () => {
  installGlobals();
  const el = makeCanvas(800);
  const view = new ImageCanvas(el, fakeImage);
  view.fit();
  flushFrames();
  const writesAfterFit = el.widthWrites;
  el.ctx.calls.drawImage.length = 0;

  for (let i = 0; i < 5; i++) wheel(el, -120);
  assert.equal(rafQueue.length, 1, "a burst produces one scheduled frame");
  flushFrames();
  assert.equal(el.ctx.calls.drawImage.length, 1, "one blit for the whole burst");
  assert.equal(el.widthWrites, writesAfterFit, "bitmap never reallocated");
  assert.ok(view.scale > 800 / 2400, "zoomed in");

  // Deep zoom: only the visible source slice is resampled.
  view.scale = 10;
  view.offsetX = -100;
  view.offsetY = -50;
  el.ctx.calls.drawImage.length = 0;
  view.redraw();
  flushFrames();
  const b = el.ctx.calls.drawImage.at(-1);
  assert.deepEqual(b.slice(1, 5), [10, 5, 80, 80]); // 80×80 source px …
  assert.deepEqual(b.slice(5), [0, 0, 800, 800]);   // … stretched over the viewport
  await settleIdle(); // drain the high-quality settle frame
});

test("interaction draws fast, then settles with high quality", async () => {
  installGlobals();
  const el = makeCanvas(800);
  const view = new ImageCanvas(el, fakeImage);
  view.fit();
  flushFrames();
  assert.equal(el.ctx.imageSmoothingQuality, "high"); // a settled frame
  wheel(el, 120); // zoom out: smoothing applies
  flushFrames();
  assert.equal(el.ctx.imageSmoothingQuality, "low");
  await settleIdle();
  assert.equal(el.ctx.imageSmoothingQuality, "high");
});

test("dense overlay polylines are decimated to screen resolution", () => {
  installGlobals();
  const el = makeCanvas(800);
  const view = new ImageCanvas(el, fakeImage);
  view.fit();
  flushFrames();
  const points = [];
  for (let i = 0; i < 20000; i++) points.push([(i / 19999) * 2400, (i / 19999) * 2400]);
  view.overlays = [{ points, color: "#fff", width: 2 }];
  view.redraw();
  flushFrames();
  assert.ok(el.ctx.calls.moveTo >= 1, "path started");
  assert.ok(el.ctx.calls.lineTo < 2500, `lineTo calls collapsed to ${el.ctx.calls.lineTo}`);
});

// -- app-level wiring: photo import, edge points, the Clear button ----------

function makeElement(id) {
  const el = makeCanvas(800);
  Object.assign(el, {
    id, value: "", textContent: "", innerHTML: "", disabled: false,
    className: "", title: "", style: {},
    classList: {
      _set: new Set(),
      add(c) { this._set.add(c); },
      remove(c) { this._set.delete(c); },
      contains(c) { return this._set.has(c); },
    },
    append() {}, remove() {}, setAttribute() {}, scrollIntoView() {},
  });
  return el;
}

test("rectification overlays the computed disc outline on the photo", async () => {
  installGlobals();
  const elements = new Map();
  globalThis.document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, makeElement(id));
      return elements.get(id);
    },
    createElement: (tag) => (tag === "canvas" ? makeCanvas(800) : makeElement(tag)),
    querySelectorAll: () => [],
  };
  Object.defineProperty(globalThis, "navigator", {
    value: { clipboard: { writeText: async () => {} } },
    configurable: true,
  });
  // A 400×300 photo; the disc spans 2 px/mm: outer 120 px, hole 15 px.
  globalThis.createImageBitmap = async () => ({ width: 400, height: 300, close() {} });
  for (const [id, value] of Object.entries({
    kind: "dvd", stage: "coarse", "p-r0": "24", "p-r1": "58", "p-sectors": "2297888",
    "p-density": "133.3", "p-c1": "0", "p-c2": "0", "p-c3": "0", "p-usable": "",
    "disc-size": "60",
  })) {
    document.getElementById(id).value = value;
  }
  // Fresh module instance: the app state must not leak between tests.
  await import(JS + "app.js?rectify-overlay");

  const photoEl = document.getElementById("photo-canvas");
  document.getElementById("photo-input").fire("change", { target: { files: [{}] } });
  await new Promise((resolve) => setTimeout(resolve, 0)); // let Photo.load resolve
  flushFrames();

  // The fit maps image px -> screen px with scale 2 (fit to 800×600).
  const click = (ix, iy) =>
    photoEl.fire("pointerdown", { button: 0, clientX: ix * 2, clientY: iy * 2, preventDefault() {} });
  for (let k = 0; k < 8; k++) { // outer edge: 120 px around (200,150)
    const a = (k / 8) * 2 * Math.PI;
    click(200 + 120 * Math.cos(a), 150 + 120 * Math.sin(a));
  }
  document.getElementById("mode-hole").fire("click");
  for (let k = 0; k < 8; k++) { // hole edge: 15 px around (200,150), rotated
    const a = (k / 8) * 2 * Math.PI + 0.2;
    click(200 + 15 * Math.cos(a), 150 + 15 * Math.sin(a));
  }
  flushFrames();
  assert.equal(photoEl.ctx.calls.arc, 16, "16 edge markers drawn");

  document.getElementById("rectify").fire("click");
  flushFrames();
  const status = document.getElementById("rectify-status");
  assert.ok(status.className.includes("ok"), `rectify status: ${status.className} ${status.textContent}`);
  assert.equal(photoEl.ctx.calls.moveTo, 2, "two outline polylines (outer edge + hole)");
  assert.ok(photoEl.ctx.calls.lineTo > 100, `outline segments drawn: ${photoEl.ctx.calls.lineTo}`);

  // Clearing the points also drops the computed outline they produced.
  photoEl.ctx.calls.lineTo = 0;
  photoEl.ctx.calls.moveTo = 0;
  photoEl.ctx.calls.arc = 0;
  document.getElementById("clear-points").fire("click");
  flushFrames();
  assert.equal(photoEl.ctx.calls.moveTo, 0, "no outline after Clear");
  assert.equal(photoEl.ctx.calls.arc, 0, "no markers after Clear");
});

test("loading a session restores the measurement points", async () => {
  installGlobals();
  const elements = new Map();
  globalThis.document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, makeElement(id));
      return elements.get(id);
    },
    createElement: (tag) => (tag === "canvas" ? makeCanvas(800) : makeElement(tag)),
    querySelectorAll: () => [],
  };
  Object.defineProperty(globalThis, "navigator", {
    value: { clipboard: { writeText: async () => {} } },
    configurable: true,
  });
  // Photo.fromDataUrl needs an Image; resolve immediately with a 400×300 frame.
  globalThis.Image = class {
    set src(_url) {
      this.naturalWidth = 400;
      this.naturalHeight = 300;
      queueMicrotask(() => this.onload());
    }
  };
  for (const [id, value] of Object.entries({
    kind: "dvd", stage: "coarse", "p-r0": "24", "p-r1": "58", "p-sectors": "2297888",
    "p-density": "133.3", "p-c1": "0", "p-c2": "0", "p-c3": "0", "p-usable": "",
    "disc-size": "60",
  })) {
    document.getElementById(id).value = value;
  }
  await import(JS + "app.js?session-restore"); // fresh module instance

  const baseSession = {
    format: "disc-image-studio-web-calibration-v1",
    stage: "coarse",
    parameters: {
      kind: "dvd", innerRadiusMm: 24, outerRadiusMm: 58, sectors: 2297888,
      linearDensity: 133.3, pitchLinear: 0, pitchQuadratic: 0, pitchCubic: 0, usableSectors: 0,
    },
    physicalOuterRadiusMm: 60,
    outerPoints: [],
    holePoints: [],
    // 2 px/mm, disc centre at (200,150): discToSource homography.
    transform9: [2, 0, 200, 0, 2, 150, 0, 0, 1],
    coarseEdges: [{ x: 30, y: 0 }, { x: -30, y: 0 }, null, null, null, null],
    boundarySegments: [],
    fineTrace: [],
    photoPng: "data:image/png;base64,AAAA",
  };
  const rectEl = document.getElementById("rectified-canvas");
  const load = async (session) => {
    const file = { text: async () => JSON.stringify(session) };
    document.getElementById("load-session").fire("change", { target: { files: [file] } });
    await new Promise((resolve) => setTimeout(resolve, 0)); // let loadSession finish
    flushFrames();
  };
  const reset = () => {
    rectEl.ctx.calls.arc = 0;
    rectEl.ctx.calls.moveTo = 0;
    rectEl.ctx.calls.lineTo = 0;
    rectEl.ctx.calls.fillText.length = 0;
  };

  // Coarse stage: two restored edges are drawn with their names, and the
  // next click continues the sequence instead of restarting it.
  await load(baseSession);
  assert.equal(rectEl.ctx.calls.arc, 2, "restored coarse edges drawn");
  assert.deepEqual(rectEl.ctx.calls.fillText, ["band 1 inner", "band 1 outer"]);
  reset(); // the counters accumulate across frames
  rectEl.fire("pointerdown", { button: 0, clientX: 400, clientY: 100, preventDefault() {} });
  flushFrames();
  assert.equal(rectEl.ctx.calls.arc, 3, "the click adds a third edge");
  assert.ok(rectEl.ctx.calls.fillText.includes("band 2 inner"),
    `the third edge continues the sequence: ${rectEl.ctx.calls.fillText}`);

  // Boundary stage: the restored segment's markers and polyline are drawn.
  reset();
  await load({
    ...baseSession,
    stage: "boundary",
    coarseEdges: [null, null, null, null, null, null],
    boundarySegments: [{ id: 0, points: [{ x: 30, y: 0 }, { x: 35, y: 5 }, { x: 40, y: 10 }, { x: 45, y: 15 }] }],
  });
  assert.equal(rectEl.ctx.calls.arc, 4, "restored boundary points drawn");
  assert.equal(rectEl.ctx.calls.lineTo, 3, "restored boundary polyline drawn");
  assert.equal(rectEl.ctx.calls.moveTo, 1, "one polyline");

  // Fine stage: the restored trace is drawn with numbered labels.
  reset();
  await load({
    ...baseSession,
    stage: "fine",
    boundarySegments: [],
    fineTrace: [{ x: 25, y: 0 }, { x: 30, y: 1 }, { x: 35, y: 2 }, { x: 40, y: 3 }, { x: 45, y: 4 }, { x: 50, y: 5 }],
  });
  assert.equal(rectEl.ctx.calls.arc, 6, "restored trace points drawn");
  assert.deepEqual(rectEl.ctx.calls.fillText, ["1", "2", "3", "4", "5", "6"], "trace labels");
});

test("Clear empties the rectification points; re-import keeps single handlers", async () => {
  installGlobals();
  const elements = new Map();
  globalThis.document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, makeElement(id));
      return elements.get(id);
    },
    createElement: (tag) => (tag === "canvas" ? makeCanvas(800) : makeElement(tag)),
    querySelectorAll: () => [],
  };
  Object.defineProperty(globalThis, "navigator", {
    value: { clipboard: { writeText: async () => {} } },
    configurable: true,
  });
  globalThis.createImageBitmap = async () => ({ width: 8, height: 8, close() {} });
  for (const [id, value] of Object.entries({
    kind: "dvd", stage: "coarse", "p-r0": "24", "p-r1": "58", "p-sectors": "2297888",
    "p-density": "133.3", "p-c1": "0", "p-c2": "0", "p-c3": "0", "p-usable": "",
    "disc-size": "60",
  })) {
    document.getElementById(id).value = value;
  }
  await import(JS + "app.js");

  const photoEl = document.getElementById("photo-canvas");
  const click = (x, y) =>
    photoEl.fire("pointerdown", { button: 0, clientX: x, clientY: y, preventDefault() {} });
  const importFile = async () => {
    document.getElementById("photo-input").fire("change", { target: { files: [{}] } });
    await new Promise((resolve) => setTimeout(resolve, 0)); // let Photo.load resolve
    flushFrames();
  };

  await importFile();
  click(10, 10);
  click(20, 20);
  flushFrames();
  assert.equal(photoEl.ctx.calls.arc, 2, "two edge markers drawn");

  // The Clear button must empty the arrays the canvas actually draws.
  photoEl.ctx.calls.arc = 0;
  document.getElementById("clear-points").fire("click");
  flushFrames();
  assert.equal(photoEl.ctx.calls.arc, 0, "cleared markers stay off screen");

  // Re-importing must not stack duplicate canvas handlers.
  await importFile();
  photoEl.ctx.calls.arc = 0;
  click(15, 15);
  flushFrames();
  assert.equal(photoEl.ctx.calls.arc, 1, "one click adds exactly one marker");
});
