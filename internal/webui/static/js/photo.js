// Photo import, bounded working resolution (CalibrationPhotoLimits) and
// pixel sampling for the rectifier and the sweep reader.

export const MAX_DIMENSION = 8192;
export const MAX_PIXEL_COUNT = 32 * 1024 * 1024;
export const MAX_RECTIFIED_DIMENSION = Math.floor(Math.sqrt(MAX_PIXEL_COUNT));

export class Photo {
  constructor(canvas) {
    this.canvas = canvas; // offscreen canvas holding the working-resolution image
    this.width = canvas.width;
    this.height = canvas.height;
    this.ctx = canvas.getContext("2d", { willReadFrequently: true });
    this.imageData = this.ctx.getImageData(0, 0, this.width, this.height);
  }

  static async load(file) {
    const bitmap = await createImageBitmap(file);
    let width = bitmap.width, height = bitmap.height;
    // Keep the original pixel size inside the limits; downscale otherwise.
    const scaleDown = Math.min(1,
      MAX_DIMENSION / width, MAX_DIMENSION / height,
      Math.sqrt(MAX_PIXEL_COUNT / (width * height)));
    const w = Math.max(1, Math.floor(width * scaleDown));
    const h = Math.max(1, Math.floor(height * scaleDown));
    const canvas = document.createElement("canvas");
    canvas.width = w;
    canvas.height = h;
    const ctx = canvas.getContext("2d", { willReadFrequently: true });
    ctx.drawImage(bitmap, 0, 0, w, h);
    bitmap.close();
    return new Photo(canvas);
  }

  // Bilinear sample at source pixel coordinates; returns [r, g, b] in [0, 1].
  sample(x, y) {
    if (x < 0 || y < 0 || x > this.width - 1 || y > this.height - 1) return [1, 1, 1];
    const x0 = Math.floor(x), y0 = Math.floor(y);
    const x1 = Math.min(x0 + 1, this.width - 1), y1 = Math.min(y0 + 1, this.height - 1);
    const fx = x - x0, fy = y - y0;
    const d = this.imageData.data;
    const i00 = (y0 * this.width + x0) * 4, i10 = (y0 * this.width + x1) * 4;
    const i01 = (y1 * this.width + x0) * 4, i11 = (y1 * this.width + x1) * 4;
    const out = [0, 0, 0];
    for (let c = 0; c < 3; c++) {
      out[c] = (d[i00 + c] * (1 - fx) * (1 - fy) + d[i10 + c] * fx * (1 - fy) +
        d[i01 + c] * (1 - fx) * fy + d[i11 + c] * fx * fy) / 255;
    }
    return out;
  }

  // Downscaled PNG data URL for sessions (null when too large).
  toDataUrl(maxBytes) {
    const url = this.canvas.toDataURL("image/png");
    if (url.length > maxBytes) return null;
    return url;
  }

  static async fromDataUrl(url) {
    const img = await new Promise((resolve, reject) => {
      const image = new Image();
      image.onload = () => resolve(image);
      image.onerror = reject;
      image.src = url;
    });
    const canvas = document.createElement("canvas");
    canvas.width = img.naturalWidth;
    canvas.height = img.naturalHeight;
    const ctx = canvas.getContext("2d", { willReadFrequently: true });
    ctx.drawImage(img, 0, 0);
    return new Photo(canvas);
  }
}

// Renders the rectified disc view: one output pixel per inverse-mapped source
// sample. Returns {canvas, millimetresPerPixel, canvasRadiusMm}.
export function renderRectified(photo, transform, physicalOuterRadiusMm, size) {
  const canvasRadiusMm = physicalOuterRadiusMm + 3;
  size = Math.min(size, MAX_RECTIFIED_DIMENSION);
  const canvas = document.createElement("canvas");
  canvas.width = size;
  canvas.height = size;
  const ctx = canvas.getContext("2d");
  const image = ctx.createImageData(size, size);
  const centre = (size - 1) / 2;
  const scale = (size - 1) / (2 * canvasRadiusMm);
  const data = image.data;
  for (let y = 0; y < size; y++) {
    const ym = (y - centre) / scale;
    for (let x = 0; x < size; x++) {
      const xm = (x - centre) / scale;
      const source = transform.discToSourcePoint({ x: xm, y: ym });
      const [r, g, b] = photo.sample(source.x, source.y);
      const i = (y * size + x) * 4;
      data[i] = Math.round(r * 255);
      data[i + 1] = Math.round(g * 255);
      data[i + 2] = Math.round(b * 255);
      data[i + 3] = 255;
    }
  }
  ctx.putImageData(image, 0, 0);
  return { canvas, millimetresPerPixel: canvasRadiusMm * 2 / (size - 1), canvasRadiusMm };
}

// A sampler over the rectified view in disc-centred millimetres (for the
// sweep reader). Returns [r, g, b] in [0, 1].
export function rectifiedSampler(rectifiedCanvas, canvasRadiusMm) {
  const size = rectifiedCanvas.width;
  const ctx = rectifiedCanvas.getContext("2d", { willReadFrequently: true });
  const imageData = ctx.getImageData(0, 0, size, size);
  const centre = (size - 1) / 2;
  const scale = (size - 1) / (2 * canvasRadiusMm);
  return (xMm, yMm) => {
    const x = centre + xMm * scale;
    const y = centre + yMm * scale;
    if (x < 0 || y < 0 || x > size - 1 || y > size - 1) return [1, 1, 1];
    const x0 = Math.floor(x), y0 = Math.floor(y);
    const x1 = Math.min(x0 + 1, size - 1), y1 = Math.min(y0 + 1, size - 1);
    const fx = x - x0, fy = y - y0;
    const d = imageData.data;
    const i00 = (y0 * size + x0) * 4, i10 = (y0 * size + x1) * 4;
    const i01 = (y1 * size + x0) * 4, i11 = (y1 * size + x1) * 4;
    const out = [0, 0, 0];
    for (let c = 0; c < 3; c++) {
      out[c] = (d[i00 + c] * (1 - fx) * (1 - fy) + d[i10 + c] * fx * (1 - fy) +
        d[i01 + c] * (1 - fx) * fy + d[i11 + c] * fx * fy) / 255;
    }
    return out;
  };
}
