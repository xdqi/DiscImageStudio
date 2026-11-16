// A pan/zoom canvas that displays an image plus editable points, polylines
// and overlay paths. All interactions use pointer events.

export class ImageCanvas {
  // canvas: the <canvas> element. image: an offscreen canvas with the pixels.
  // toWorld maps screen-image pixels to logical coordinates (identity for
  // photos; to millimetres for the rectified view).
  constructor(canvas, image, toWorld = null) {
    this.canvas = canvas;
    this.image = image;
    this.toWorld = toWorld ?? ((px, py) => ({ x: px, y: py }));
    this.fromWorld = null; // set via setWorldTransform
    this.scale = 1;
    this.offsetX = 0;
    this.offsetY = 0;
    this.pointSets = []; // {points:[{x,y}], color, size, labels:[], world:true/false}
    this.polylines = []; // {points:[{x,y}], color, closed, width}
    this.overlays = [];  // {points:[[x,y]...world], color, width}
    this.crosshair = null;
    this.onClick = null;       // (worldPoint, event)
    this.onPointDrag = null;   // (set, index, worldPoint)
    this.onPointRemove = null; // (set, index)
    this._drag = null;
    this._pan = null;

    canvas.addEventListener("pointerdown", (e) => this._down(e));
    canvas.addEventListener("pointermove", (e) => this._move(e));
    canvas.addEventListener("pointerup", (e) => this._up(e));
    canvas.addEventListener("wheel", (e) => this._wheel(e), { passive: false });
    canvas.addEventListener("contextmenu", (e) => e.preventDefault());
    new ResizeObserver(() => this.redraw()).observe(canvas);
  }

  // Defines the world transform: worldRect {x0, y0, x1, y1} maps onto the
  // whole image.
  setWorldTransform(x0, y0, x1, y1) {
    const w = this.image.width, h = this.image.height;
    const sx = (x1 - x0) / w, sy = (y1 - y0) / h;
    this.fromWorld = (wx, wy) => ({ x: (wx - x0) / sx, y: (wy - y0) / sy });
    this.toWorld = (px, py) => ({ x: x0 + px * sx, y: y0 + py * sy });
  }

  fit() {
    const rect = this.canvas.getBoundingClientRect();
    this.canvas.width = Math.max(1, Math.floor(rect.width));
    this.canvas.height = Math.max(1, Math.floor(rect.width * this.image.height / this.image.width));
    this.scale = Math.min(this.canvas.width / this.image.width, this.canvas.height / this.image.height);
    this.offsetX = (this.canvas.width - this.image.width * this.scale) / 2;
    this.offsetY = (this.canvas.height - this.image.height * this.scale) / 2;
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
      this.redraw();
      return;
    }
    if (this._drag && this.onPointDrag) {
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
    this.redraw();
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

  redraw() {
    const ctx = this.canvas.getContext("2d");
    const rect = this.canvas.getBoundingClientRect();
    this.canvas.width = Math.max(1, Math.floor(rect.width));
    this.canvas.height = Math.max(1, Math.floor(rect.width * this.image.height / this.image.width));
    ctx.fillStyle = "#05070d";
    ctx.fillRect(0, 0, this.canvas.width, this.canvas.height);
    ctx.imageSmoothingEnabled = this.scale < 1;
    ctx.imageSmoothingQuality = "high";
    ctx.save();
    ctx.translate(this.offsetX, this.offsetY);
    ctx.scale(this.scale, this.scale);
    ctx.drawImage(this.image, 0, 0);
    ctx.restore();

    // Polylines.
    for (const line of this.polylines) {
      if (line.points.length < 2) continue;
      ctx.strokeStyle = line.color;
      ctx.lineWidth = (line.width ?? 2) / (this.scale > 1 ? 1 : 1);
      ctx.beginPath();
      for (let i = 0; i < line.points.length; i++) {
        const p = line.world ? this.worldToScreen(line.points[i].x, line.points[i].y)
          : this.imageToScreen(line.points[i].x, line.points[i].y);
        if (i === 0) ctx.moveTo(p.x, p.y); else ctx.lineTo(p.x, p.y);
      }
      if (line.closed) ctx.closePath();
      ctx.stroke();
    }
    // Overlay paths (predicted curves).
    for (const overlay of this.overlays) {
      if (overlay.points.length < 2) continue;
      ctx.strokeStyle = overlay.color;
      ctx.lineWidth = overlay.width ?? 2;
      ctx.setLineDash(overlay.dash ?? []);
      ctx.beginPath();
      for (let i = 0; i < overlay.points.length; i++) {
        const p = overlay.world === false
          ? this.imageToScreen(overlay.points[i][0], overlay.points[i][1])
          : this.worldToScreen(overlay.points[i][0], overlay.points[i][1]);
        if (i === 0) ctx.moveTo(p.x, p.y); else ctx.lineTo(p.x, p.y);
      }
      ctx.stroke();
      ctx.setLineDash([]);
    }
    // Points.
    for (const set of this.pointSets) {
      for (let i = 0; i < set.points.length; i++) {
        const p = set.world ? this.worldToScreen(set.points[i].x, set.points[i].y)
          : this.imageToScreen(set.points[i].x, set.points[i].y);
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
}
