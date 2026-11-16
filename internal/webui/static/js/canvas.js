// A pan/zoom canvas that displays an image plus editable points, polylines
// and overlay paths. All interactions use pointer events.

// While panning/zooming, frames are drawn with fast smoothing; this long
// after the last interaction a final high-quality frame is drawn.
const INTERACTION_IDLE_MS = 140;
// Polyline vertices closer together than this on screen (squared) are
// collapsed — prediction curves carry up to 20k vertices per arm.
const MIN_VERTEX_PX2 = 0.56; // ~0.75 px

const nextFrame = (fn) =>
  typeof requestAnimationFrame === "function" ? requestAnimationFrame(fn) : setTimeout(fn, 0);

export class ImageCanvas {
  // canvas: the <canvas> element. image: an offscreen canvas with the pixels.
  // toWorld maps screen-image pixels to logical coordinates (identity for
  // photos; to millimetres for the rectified view).
  constructor(canvas, image, toWorld = null) {
    this.canvas = canvas;
    this.ctx = canvas.getContext("2d");
    this.image = image;
    this.toWorld = toWorld ?? ((px, py) => ({ x: px, y: py }));
    this.fromWorld = null; // set via setWorldTransform
    this.scale = 1;
    this.offsetX = 0;
    this.offsetY = 0;
    this.pointSets = []; // {points:[{x,y}], color, size, labels:[], world:true/false}
    this.polylines = []; // {points:[{x,y}], color, closed, width, world}
    this.overlays = [];  // {points:[[x,y]...world], color, width, dash}
    this.onClick = null;       // (worldPoint, event)
    this.onPointDrag = null;   // (set, index, worldPoint)
    this.onPointRemove = null; // (set, index)
    this._drag = null;
    this._pan = null;
    this._fitValid = false; // fit() ran with a real (non-hidden) layout size
    this._drawQueued = false;
    this._fast = false;     // an interaction (wheel/pan/drag) is in progress
    this._idleTimer = null;

    canvas.addEventListener("pointerdown", (e) => this._down(e));
    canvas.addEventListener("pointermove", (e) => this._move(e));
    canvas.addEventListener("pointerup", (e) => this._up(e));
    canvas.addEventListener("wheel", (e) => this._wheel(e), { passive: false });
    canvas.addEventListener("contextmenu", (e) => e.preventDefault());
    new ResizeObserver(() => this._resize()).observe(canvas);
  }

  // Replaces the displayed image. Keeps the current view unless refit is set.
  setImage(image, refit = true) {
    this.image = image;
    if (refit) this.fit();
    else this.redraw();
  }

  // Defines the world transform: worldRect {x0, y0, x1, y1} maps onto the
  // whole image.
  setWorldTransform(x0, y0, x1, y1) {
    const w = this.image.width, h = this.image.height;
    const sx = (x1 - x0) / w, sy = (y1 - y0) / h;
    this.fromWorld = (wx, wy) => ({ x: (wx - x0) / sx, y: (wy - y0) / sy });
    this.toWorld = (px, py) => ({ x: x0 + px * sx, y: y0 + py * sy });
  }

  // Syncs the bitmap size with the CSS layout size. Returns false while the
  // canvas has no layout box (its panel is display:none).
  _syncSize() {
    const rect = this.canvas.getBoundingClientRect();
    if (rect.width < 1) return false;
    const w = Math.max(1, Math.round(rect.width));
    const h = Math.max(1, Math.round(rect.width * this.image.height / this.image.width));
    if (w !== this.canvas.width || h !== this.canvas.height) {
      this.canvas.width = w;
      this.canvas.height = h;
    }
    return true;
  }

  fit() {
    if (!this._syncSize()) {
      // Hidden panel: refit once a real size arrives (see _resize).
      this._fitValid = false;
      return;
    }
    this.scale = Math.min(this.canvas.width / this.image.width, this.canvas.height / this.image.height);
    this.offsetX = (this.canvas.width - this.image.width * this.scale) / 2;
    this.offsetY = (this.canvas.height - this.image.height * this.scale) / 2;
    this._fitValid = true;
    this.redraw();
  }

  _resize() {
    if (!this._syncSize()) return;
    if (!this._fitValid) {
      // The panel became visible after a fit() with a zero size: compute a
      // proper view, otherwise the image would stay a one-pixel speck.
      this.fit();
      return;
    }
    this.redraw();
  }

  screenToImage(sx, sy) {
    const rect = this.canvas.getBoundingClientRect();
    return {
      x: (sx - rect.left - this.offsetX) / this.scale,
      y: (sy - rect.top - this.offsetY) / this.scale,
    };
  }

  imageToScreen(ix, iy) {
    return { x: ix * this.scale + this.offsetX, y: iy * this.scale + this.offsetY };
  }

  screenToWorld(sx, sy) {
    const p = this.screenToImage(sx, sy);
    return this.toWorld(p.x, p.y);
  }

  worldToScreen(wx, wy) {
    const p = this.fromWorld ? this.fromWorld(wx, wy) : { x: wx, y: wy };
    return this.imageToScreen(p.x, p.y);
  }

  _down(e) {
    e.preventDefault();
    this.canvas.setPointerCapture(e.pointerId);
    const world = this.screenToWorld(e.clientX, e.clientY);
    if (e.button === 2 || e.button === 1) {
      this._pan = { x: e.clientX, y: e.clientY };
      return;
    }
    // Hit-test existing points first (drag / right-drag remove with alt).
    const hit = this._hitTest(e.clientX, e.clientY);
    if (hit) {
      if (e.altKey && this.onPointRemove) this.onPointRemove(hit.set, hit.index);
      else this._drag = hit;
      return;
    }
    if (e.shiftKey) {
      this._pan = { x: e.clientX, y: e.clientY };
      return;
    }
    if (this.onClick) this.onClick(world, e);
  }

  _move(e) {
    if (this._pan) {
      this.offsetX += e.clientX - this._pan.x;
      this.offsetY += e.clientY - this._pan.y;
      this._pan = { x: e.clientX, y: e.clientY };
      this._markInteraction();
      this.redraw();
      return;
    }
    if (this._drag && this.onPointDrag) {
      this._markInteraction();
      this.onPointDrag(this._drag.set, this._drag.index, this.screenToWorld(e.clientX, e.clientY));
    }
  }

  _up() {
    this._drag = null;
    this._pan = null;
  }

  _wheel(e) {
    e.preventDefault();
    const factor = Math.exp(-e.deltaY * 0.0015);
    const rect = this.canvas.getBoundingClientRect();
    const mx = e.clientX - rect.left, my = e.clientY - rect.top;
    const newScale = Math.min(64, Math.max(0.01, this.scale * factor));
    this.offsetX = mx - (mx - this.offsetX) * (newScale / this.scale);
    this.offsetY = my - (my - this.offsetY) * (newScale / this.scale);
    this.scale = newScale;
    this._markInteraction();
    this.redraw();
  }

  _markInteraction() {
    this._fast = true;
    if (this._idleTimer !== null) clearTimeout(this._idleTimer);
    this._idleTimer = setTimeout(() => {
      this._idleTimer = null;
      this._fast = false;
      this.redraw(); // settle with one high-quality frame
    }, INTERACTION_IDLE_MS);
  }

  _hitTest(clientX, clientY) {
    const rect = this.canvas.getBoundingClientRect();
    const sx = clientX - rect.left, sy = clientY - rect.top;
    for (const set of this.pointSets) {
      for (let i = 0; i < set.points.length; i++) {
        const p = set.world ? this.worldToScreen(set.points[i].x, set.points[i].y)
          : this.imageToScreen(set.points[i].x, set.points[i].y);
        if ((p.x - sx) ** 2 + (p.y - sy) ** 2 <= 100) return { set, index: i };
      }
    }
    return null;
  }

  // Schedules a coalesced repaint: bursts of wheel/pointer events within one
  // animation frame produce a single draw.
  redraw() {
    if (this._drawQueued) return;
    this._drawQueued = true;
    nextFrame(() => {
      this._drawQueued = false;
      this._draw();
    });
  }

  _draw() {
    const ctx = this.ctx;
    if (!ctx) return;
    const w = this.canvas.width, h = this.canvas.height;
    ctx.fillStyle = "#05070d";
    ctx.fillRect(0, 0, w, h);

    // Blit only the visible slice of the source image; at high zoom this
    // avoids resampling megapixels the viewport cannot show.
    const iw = this.image.width, ih = this.image.height;
    if (iw > 0 && ih > 0 && this.scale > 0) {
      const sx0 = Math.max(0, -this.offsetX / this.scale);
      const sy0 = Math.max(0, -this.offsetY / this.scale);
      const sx1 = Math.min(iw, (w - this.offsetX) / this.scale);
      const sy1 = Math.min(ih, (h - this.offsetY) / this.scale);
      if (sx1 > sx0 && sy1 > sy0) {
        const smoothing = this.scale < 1;
        ctx.imageSmoothingEnabled = smoothing;
        if (smoothing) ctx.imageSmoothingQuality = this._fast ? "low" : "high";
        ctx.drawImage(this.image, sx0, sy0, sx1 - sx0, sy1 - sy0,
          sx0 * this.scale + this.offsetX, sy0 * this.scale + this.offsetY,
          (sx1 - sx0) * this.scale, (sy1 - sy0) * this.scale);
      }
    }

    // Polylines.
    for (const line of this.polylines) {
      if (line.points.length < 2) continue;
      ctx.strokeStyle = line.color;
      ctx.lineWidth = line.width ?? 2;
      this._stroke(line.points, line.world, false, line.closed);
      ctx.stroke();
    }
    // Overlay paths (predicted curves).
    for (const overlay of this.overlays) {
      if (overlay.points.length < 2) continue;
      ctx.strokeStyle = overlay.color;
      ctx.lineWidth = overlay.width ?? 2;
      ctx.setLineDash(overlay.dash ?? []);
      this._stroke(overlay.points, overlay.world !== false, true, false);
      ctx.stroke();
      ctx.setLineDash([]);
    }
    // Points.
    for (const set of this.pointSets) {
      for (let i = 0; i < set.points.length; i++) {
        const p = set.world ? this.worldToScreen(set.points[i].x, set.points[i].y)
          : this.imageToScreen(set.points[i].x, set.points[i].y);
        if (p.x < -24 || p.y < -24 || p.x > w + 24 || p.y > h + 24) continue;
        const r = set.size ?? 5;
        ctx.beginPath();
        ctx.arc(p.x, p.y, r, 0, 2 * Math.PI);
        ctx.fillStyle = set.color;
        ctx.fill();
        ctx.lineWidth = 1.5;
        ctx.strokeStyle = "#000";
        ctx.stroke();
        if (set.labels) {
          ctx.fillStyle = set.color;
          ctx.font = "12px system-ui";
          ctx.fillText(set.labels[i] ?? "", p.x + r + 3, p.y + 4);
        }
      }
    }
  }

  // Strokes a polyline, collapsing vertices that land on (nearly) the same
  // screen pixel. Skipped vertices stay within ~0.75 px of the drawn path.
  _stroke(points, world, isArray, closed) {
    const ctx = this.ctx;
    ctx.beginPath();
    let lastX = 0, lastY = 0;
    for (let i = 0; i < points.length; i++) {
      const q = points[i];
      const p = world
        ? this.worldToScreen(isArray ? q[0] : q.x, isArray ? q[1] : q.y)
        : this.imageToScreen(isArray ? q[0] : q.x, isArray ? q[1] : q.y);
      if (i === 0) {
        ctx.moveTo(p.x, p.y);
      } else {
        const dx = p.x - lastX, dy = p.y - lastY;
        if (dx * dx + dy * dy < MIN_VERTEX_PX2 && i < points.length - 1) continue;
        ctx.lineTo(p.x, p.y);
      }
      lastX = p.x;
      lastY = p.y;
    }
    if (closed) ctx.closePath();
  }
}
