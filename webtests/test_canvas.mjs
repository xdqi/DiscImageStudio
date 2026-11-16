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
  const calls = { drawImage: [], moveTo: 0, lineTo: 0, arc: 0, fillRect: 0 };
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
    fillText() {}, setLineDash() {},
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
