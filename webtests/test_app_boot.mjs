// Boot test: import the DOM-free math modules for real (catches bad
// import/export wiring), then boot app.js against a minimal DOM stub.

import { test } from "node:test";
import assert from "node:assert/strict";

const JS = "../internal/webui/static/js/";

test("math modules import cleanly", async () => {
  for (const m of ["trackgeom.js", "geometry.js", "target.js", "fit.js", "sweep.js", "boundaryfit.js", "crossfit.js", "crossmodel.js"]) {
    const module = await import(JS + m);
    assert.ok(module, m);
  }
});

test("app boots with a stub DOM", async () => {
  // Minimal DOM: elements with the ids the app touches.
  const elements = new Map();
  const makeElement = (id) => {
    const el = {
      id,
      value: "",
      textContent: "",
      innerHTML: "",
      disabled: false,
      className: "",
      style: {},
      classList: {
        _set: new Set(),
        add(c) { this._set.add(c); },
        remove(c) { this._set.delete(c); },
        contains(c) { return this._set.has(c); },
      },
      addEventListener() {},
      append() {},
      remove() {},
      setAttribute() {},
      getBoundingClientRect() { return { width: 800, height: 600, left: 0, top: 0 }; },
      scrollIntoView() {},
      getContext() { return null; },
    };
    return el;
  };
  const documentStub = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, makeElement(id));
      return elements.get(id);
    },
    createElement: (tag) => makeElement(tag),
    querySelectorAll: () => [],
  };
  globalThis.document = documentStub;
  Object.defineProperty(globalThis, "navigator", { value: { clipboard: { writeText: async () => {} } }, configurable: true });
  globalThis.ResizeObserver = class { observe() {} };
  // Elements the app reads at boot.
  for (const [id, value] of Object.entries({
    "kind": "dvd", "stage": "coarse", "p-r0": "24", "p-r1": "58",
    "p-sectors": "2297888", "p-density": "133.3", "p-c1": "0", "p-c2": "0",
    "p-c3": "0", "p-usable": "",
  })) {
    documentStub.getElementById(id).value = value;
  }
  await import(JS + "app.js");
  // The burn command must be rendered at boot.
  const command = documentStub.getElementById("burn-command").textContent;
  assert.ok(command.includes("calibration-target"), command);
  assert.ok(command.includes("--kind dvd"), command);
  assert.ok(command.includes("--stage coarse"), command);
  assert.ok(command.includes("--sectors 2297888"), command);
});
