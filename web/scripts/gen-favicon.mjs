// Renders the OpenLeaderboard favicon (the podium mark, matching favicon.svg
// and the Logo component) to raster PNGs and an .ico, using only Node built-ins
// (zlib) — no image dependencies. Re-run after changing the geometry/colors:
//   node scripts/gen-favicon.mjs
import { deflateSync } from "node:zlib";
import { writeFileSync, mkdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const OUT = join(dirname(fileURLToPath(import.meta.url)), "..", "public");
mkdirSync(OUT, { recursive: true });

// Geometry on a 32-unit canvas (identical to public/favicon.svg).
const BG = "#0a0b0d";
const bars = [
  { x: 6, y: 17, w: 5, h: 9, c: "#5a626d" },
  { x: 13.5, y: 10, w: 5, h: 16, c: "#c6f135" },
  { x: 21, y: 14, w: 5, h: 12, c: "#38d6f2" },
];
const CANVAS = 32;
const BG_RADIUS = 7;

const hex = (h) => [1, 3, 5].map((i) => parseInt(h.slice(i, i + 2), 16));
const bgRGB = hex(BG);

// insideRoundRect: is (px,py) within a rounded rectangle?
function insideRoundRect(px, py, x0, y0, x1, y1, r) {
  if (px < x0 || px > x1 || py < y0 || py > y1) return false;
  const cx = Math.min(Math.max(px, x0 + r), x1 - r);
  const cy = Math.min(Math.max(py, y0 + r), y1 - r);
  const dx = px - cx;
  const dy = py - cy;
  return dx * dx + dy * dy <= r * r;
}

// Sample the artwork at a point in canvas units -> [r,g,b,a].
function sample(px, py) {
  if (!insideRoundRect(px, py, 0, 0, CANVAS, CANVAS, BG_RADIUS)) return [0, 0, 0, 0];
  for (const b of bars) {
    if (px >= b.x && px <= b.x + b.w && py >= b.y && py <= b.y + b.h) {
      return [...hex(b.c), 255];
    }
  }
  return [...bgRGB, 255];
}

// Render at `size` px with SxS supersampling and box downsample for anti-aliasing.
function render(size) {
  const S = 4;
  const buf = Buffer.alloc(size * size * 4);
  for (let y = 0; y < size; y++) {
    for (let x = 0; x < size; x++) {
      let r = 0, g = 0, b = 0, a = 0;
      for (let sy = 0; sy < S; sy++) {
        for (let sx = 0; sx < S; sx++) {
          const px = ((x + (sx + 0.5) / S) / size) * CANVAS;
          const py = ((y + (sy + 0.5) / S) / size) * CANVAS;
          const [sr, sg, sb, sa] = sample(px, py);
          // premultiply so edges blend against transparency cleanly
          r += sr * (sa / 255);
          g += sg * (sa / 255);
          b += sb * (sa / 255);
          a += sa;
        }
      }
      const n = S * S;
      const alpha = a / n;
      const o = (y * size + x) * 4;
      if (alpha === 0) {
        buf[o] = buf[o + 1] = buf[o + 2] = buf[o + 3] = 0;
      } else {
        // un-premultiply back to straight alpha: straight = sum(color*aFrac)/sum(aFrac),
        // and sum(aFrac) = a/255, so the factor is 255/a.
        const k = 255 / a;
        buf[o] = Math.round(r * k);
        buf[o + 1] = Math.round(g * k);
        buf[o + 2] = Math.round(b * k);
        buf[o + 3] = Math.round(alpha);
      }
    }
  }
  return buf;
}

// --- minimal PNG encoder ---
const crcTable = (() => {
  const t = new Uint32Array(256);
  for (let n = 0; n < 256; n++) {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    t[n] = c >>> 0;
  }
  return t;
})();
function crc32(buf) {
  let c = 0xffffffff;
  for (let i = 0; i < buf.length; i++) c = crcTable[(c ^ buf[i]) & 0xff] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
}
function chunk(type, data) {
  const len = Buffer.alloc(4);
  len.writeUInt32BE(data.length, 0);
  const typeBuf = Buffer.from(type, "ascii");
  const body = Buffer.concat([typeBuf, data]);
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(body), 0);
  return Buffer.concat([len, body, crc]);
}
function encodePNG(rgba, size) {
  const sig = Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]);
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(size, 0);
  ihdr.writeUInt32BE(size, 4);
  ihdr[8] = 8; // bit depth
  ihdr[9] = 6; // color type RGBA
  // rows prefixed with filter byte 0 (none)
  const stride = size * 4;
  const raw = Buffer.alloc((stride + 1) * size);
  for (let y = 0; y < size; y++) {
    raw[y * (stride + 1)] = 0;
    rgba.copy(raw, y * (stride + 1) + 1, y * stride, y * stride + stride);
  }
  const idat = deflateSync(raw, { level: 9 });
  return Buffer.concat([sig, chunk("IHDR", ihdr), chunk("IDAT", idat), chunk("IEND", Buffer.alloc(0))]);
}

// --- ICO wrapping a single 32x32 PNG (accepted by all modern browsers) ---
function encodeICO(png32, size) {
  const header = Buffer.alloc(6);
  header.writeUInt16LE(0, 0); // reserved
  header.writeUInt16LE(1, 2); // type: icon
  header.writeUInt16LE(1, 4); // count
  const entry = Buffer.alloc(16);
  entry[0] = size >= 256 ? 0 : size; // width
  entry[1] = size >= 256 ? 0 : size; // height
  entry[2] = 0; // palette
  entry[3] = 0; // reserved
  entry.writeUInt16LE(1, 4); // planes
  entry.writeUInt16LE(32, 6); // bpp
  entry.writeUInt32LE(png32.length, 8);
  entry.writeUInt32LE(6 + 16, 12); // offset
  return Buffer.concat([header, entry, png32]);
}

const png32 = encodePNG(render(32), 32);
writeFileSync(join(OUT, "favicon-32.png"), png32);
writeFileSync(join(OUT, "favicon-180.png"), encodePNG(render(180), 180)); // apple-touch
writeFileSync(join(OUT, "favicon-512.png"), encodePNG(render(512), 512)); // PWA / large
writeFileSync(join(OUT, "favicon.ico"), encodeICO(png32, 32));
console.log("wrote favicon.ico, favicon-32.png, favicon-180.png, favicon-512.png to public/");
